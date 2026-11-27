package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"vocallabs.com/pandora/services/model"
)

// ErrGitHub marks a failure talking to GitHub while applying a decision.
var ErrGitHub = errors.New("github request failed")

const decisionMarker = "<!-- pandora-decision -->"

// Decide applies a human verdict to the PR's current run (which must be in needs_human).
//
// Approve: the run becomes ai_passed. Reject: the run becomes rejected and the PR is closed
// on GitHub. The database transition happens first and atomically (it refuses stale runs);
// if closing on GitHub then fails, the rejection is rolled back so the reviewer can retry.
func (o *Service) Decide(ctx context.Context, actor *model.User, p *model.PRWithRun, runID int64, approve bool, reason string) error {
	decision := model.DecisionRejected
	if approve {
		decision = model.DecisionApproved
	}
	d, err := o.store.DecideRun(ctx, runID, actor, decision, reason)
	if err != nil {
		return err
	}
	if !approve {
		if err := o.gh.ClosePR(ctx, p.Repo, p.Number); err != nil {
			if rerr := o.store.RevertRejection(ctx, runID); rerr != nil {
				o.log.Error("revert rejection failed", "run", runID, "err", rerr)
			}
			return fmt.Errorf("%w: could not close the pull request: %v", ErrGitHub, err)
		}
		if err := o.store.MarkPRClosed(ctx, p.ID); err != nil {
			o.log.Error("mark closed", "pr", p.ID, "err", err)
		}
	}
	o.store.AddEvent(ctx, runID, "human_"+decision, actor.GitHubLogin)
	o.log.Info("human decision", "pr", p.Repo, "number", p.Number, "decision", decision, "by", actor.GitHubLogin)

	// Tell GitHub (best effort: the decision itself is already recorded).
	rctx, cancel := context.WithTimeout(o.baseCtx, 30*time.Second)
	defer cancel()
	if err := o.gh.UpsertComment(rctx, p.Repo, p.Number, decisionMarker, DecisionComment(d)); err != nil {
		o.log.Warn("decision comment failed", "err", err)
	}
	state, desc := "success", "Approved by @"+actor.GitHubLogin+" after human review"
	if !approve {
		state, desc = "failure", "Rejected by @"+actor.GitHubLogin+": "+oneLine(reason)
	}
	if err := o.gh.SetStatus(rctx, p.Repo, d.SHA, state, statusContext, desc); err != nil {
		o.log.Warn("decision status failed", "err", err)
	}
	return nil
}

// DecisionComment renders the PR comment announcing a human decision.
func DecisionComment(d *model.Decision) string {
	short := d.SHA
	if len(short) > 7 {
		short = short[:7]
	}
	quote := func(s string) string { return "> " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n> ") }
	var b strings.Builder
	b.WriteString(decisionMarker + "\n")
	if d.Decision == model.DecisionApproved {
		fmt.Fprintf(&b, "## ✅ Approved by @%s\nA human reviewer accepted this pull request at commit `%s` after the AI asked for review. It continues through the pipeline.\n", d.ActorLogin, short)
		if strings.TrimSpace(d.Reason) != "" {
			b.WriteString("\n" + quote(d.Reason) + "\n")
		}
		return b.String()
	}
	fmt.Fprintf(&b, "## ⛔ Rejected by @%s\nThis pull request was rejected at commit `%s` and has been closed.\n\n**Reason**\n\n%s\n\nTo continue, fix the issue and open a new pull request.\n", d.ActorLogin, short, quote(d.Reason))
	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
