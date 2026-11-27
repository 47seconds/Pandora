// Package service drives a PR through the pipeline. Phase 1: AI review only.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"vocallabs.com/pandora/services/config"
	"vocallabs.com/pandora/services/github"
	"vocallabs.com/pandora/services/model"
	"vocallabs.com/pandora/services/review"
	"vocallabs.com/pandora/services/store"
)

const statusContext = "pandora/ai-review"

type Service struct {
	cfg      *config.Config
	store    *store.Store
	gh       *github.Client
	reviewer review.Reviewer
	source   PRSource // nil unless a GitHub App is configured (needed for backfill)
	log      *slog.Logger

	syncMu sync.Mutex
	sync   SyncStatus

	baseCtx context.Context
	wg      sync.WaitGroup
	mu      sync.Mutex
	cancels map[int64]context.CancelFunc
}

func New(ctx context.Context, cfg *config.Config, st *store.Store, gh *github.Client, rv review.Reviewer, src PRSource, log *slog.Logger) *Service {
	return &Service{cfg: cfg, store: st, gh: gh, reviewer: rv, source: src, log: log, baseCtx: ctx, cancels: map[int64]context.CancelFunc{}}
}

// Wait blocks until in-flight reviews finish (used on shutdown).
func (o *Service) Wait() { o.wg.Wait() }

type Outcome struct {
	Action string `json:"action"` // started | duplicate | closed | ignored
	RunID  int64  `json:"run_id,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// HandlePullRequest reacts to a pull_request event. Every event refreshes the stored PR
// (author, reviewers, state, ...); only some of them start an AI review.
func (o *Service) HandlePullRequest(ctx context.Context, ev github.PREvent) (Outcome, error) {
	repo, n := ev.Repository.FullName, ev.Number
	if repo == "" || n == 0 {
		return Outcome{Action: "ignored", Reason: "missing repo or number"}, nil
	}
	if !o.cfg.RepoAllowed(repo) {
		return Outcome{Action: "ignored", Reason: "repo not allowed"}, nil
	}

	pr := ev.PullRequest
	if ev.Action == "closed" {
		pr.State = "closed"
	}
	prID, err := o.saveGitHubPR(ctx, repo, n, pr)
	if err != nil {
		return Outcome{}, err
	}

	switch ev.Action {
	case "closed":
		o.supersede(ctx, prID, 0)
		return Outcome{Action: "closed"}, nil
	case "opened", "reopened", "synchronize", "ready_for_review", "edited":
	default:
		return Outcome{Action: "tracked", Reason: "action " + ev.Action}, nil
	}
	if pr.Draft {
		return Outcome{Action: "tracked", Reason: "draft"}, nil
	}
	if pr.State == "closed" {
		return Outcome{Action: "tracked", Reason: "closed"}, nil
	}
	runID, created, err := o.store.CreateRun(ctx, prID, pr.Head.SHA, descHash(pr.Title, pr.Body))
	if err != nil {
		return Outcome{}, err
	}
	if !created {
		return Outcome{Action: "duplicate", RunID: runID}, nil
	}
	// New commit or edited description: older runs no longer describe this PR.
	o.supersede(ctx, prID, runID)
	o.store.AddEvent(ctx, runID, "run_created", fmt.Sprintf("action=%s sha=%s", ev.Action, pr.Head.SHA))
	o.start(runID)
	return Outcome{Action: "started", RunID: runID}, nil
}

// saveGitHubPR stores PR metadata and its requested reviewers. No review is started.
func (o *Service) saveGitHubPR(ctx context.Context, repo string, n int, p github.PRPayload) (int64, error) {
	state := p.State
	if state == "" {
		state = "open"
	}
	id, err := o.store.UpsertPR(ctx, model.PR{
		Repo: repo, Number: n, Title: p.Title, Author: p.User.Login, State: state, HeadSHA: p.Head.SHA,
		BaseRef: p.Base.Ref, Draft: p.Draft, Merged: p.Merged || p.MergedAt != "",
		GHCreatedAt: p.CreatedAt, GHUpdatedAt: p.UpdatedAt, ClosedAt: p.ClosedAt, MergedAt: p.MergedAt,
	})
	if err != nil {
		return 0, err
	}
	return id, o.store.SetReviewers(ctx, id, p.ReviewerLogins())
}

// Recover restarts runs interrupted by a service restart.
func (o *Service) Recover(ctx context.Context) error {
	ids, err := o.store.PendingRunIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		o.log.Info("resuming interrupted run", "run", id)
		o.start(id)
	}
	return nil
}

func (o *Service) supersede(ctx context.Context, prID, keep int64) {
	ids, err := o.store.SupersedeOthers(ctx, prID, keep)
	if err != nil {
		o.log.Error("supersede failed", "pr", prID, "err", err)
		return
	}
	o.mu.Lock()
	for _, id := range ids {
		if c, ok := o.cancels[id]; ok {
			c() // stop the in-flight review of the stale commit
		}
	}
	o.mu.Unlock()
}

func (o *Service) start(runID int64) {
	ctx, cancel := context.WithTimeout(o.baseCtx, o.cfg.ReviewTimeout)
	o.mu.Lock()
	o.cancels[runID] = cancel
	o.mu.Unlock()
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		defer func() {
			cancel()
			o.mu.Lock()
			delete(o.cancels, runID)
			o.mu.Unlock()
		}()
		o.runReview(ctx, runID)
	}()
}

func (o *Service) runReview(ctx context.Context, runID int64) {
	log := o.log.With("run", runID)
	run, pr, err := o.store.GetRunWithPR(ctx, runID)
	if err != nil {
		log.Error("load run", "err", err)
		return
	}
	if ok, err := o.store.StartReview(ctx, runID); err != nil || !ok {
		return // superseded or already handled
	}
	o.report(run, pr, "pending", "AI review in progress")

	res, err := o.review(ctx, run, pr)
	switch {
	case err == errStale:
		_ = o.store.SupersedeRun(o.baseCtx, runID)
		log.Info("stale run dropped", "sha", run.SHA)
		return
	case err != nil:
		if ctx.Err() != nil && ctx.Err() != context.DeadlineExceeded {
			return // cancelled because superseded or shutting down
		}
		log.Error("review failed", "err", err)
		if ok, _ := o.store.FailRun(o.baseCtx, runID, err.Error()); ok {
			o.store.AddEvent(o.baseCtx, runID, "review_error", err.Error())
			o.report(run, pr, "error", "AI review failed: "+err.Error())
		}
		return
	}

	status, state, desc := model.StatusAIPassed, "success", "AI review passed"
	switch res.Verdict {
	case review.VerdictNeedsHuman:
		status, state, desc = model.StatusNeedsHuman, "pending", "Needs human review"
	case review.VerdictBlock:
		status, state, desc = model.StatusBlocked, "failure", "AI review found critical issues"
	}
	rep, _ := json.Marshal(res)
	applied, err := o.store.FinishReview(o.baseCtx, runID, status, res.Verdict, string(rep))
	if err != nil {
		log.Error("save review", "err", err)
		return
	}
	if !applied {
		return // superseded while the model was thinking: discard
	}
	o.store.AddEvent(o.baseCtx, runID, "review_done", res.Verdict)
	log.Info("review done", "verdict", res.Verdict, "sha", run.SHA)

	rctx, cancel := context.WithTimeout(o.baseCtx, 30*time.Second)
	defer cancel()
	if err := o.gh.UpsertComment(rctx, pr.Repo, pr.Number, review.Marker(), review.Markdown(run.SHA, res)); err != nil {
		log.Warn("comment failed", "err", err)
	}
	o.report(run, pr, state, desc)
}

var errStale = fmt.Errorf("head moved")

func (o *Service) review(ctx context.Context, run *model.Run, pr *model.PR) (*review.Result, error) {
	g, err := o.gh.GetPR(ctx, pr.Repo, pr.Number)
	if err != nil {
		return nil, err
	}
	if g.Head.SHA != run.SHA || g.State != "open" {
		return nil, errStale
	}
	in := review.Input{Repo: pr.Repo, Number: pr.Number, Title: g.Title, Body: g.Body, Author: g.User.Login, Base: g.Base.Ref, Head: g.Head.Ref}
	if len(trim(g.Body)) == 0 {
		return &review.Result{Verdict: review.VerdictNeedsHuman,
			Summary: "The PR has no description, so there is nothing to compare the changes against."}, nil
	}
	in.Diff, in.Truncated, err = o.gh.GetDiff(ctx, pr.Repo, pr.Number, o.cfg.MaxDiffBytes)
	if err != nil {
		return nil, err
	}
	res, err := o.reviewer.Review(ctx, in)
	if err != nil {
		return nil, err
	}
	review.ApplyPolicy(in, res)
	return res, nil
}

func (o *Service) report(run *model.Run, pr *model.PR, state, desc string) {
	ctx, cancel := context.WithTimeout(o.baseCtx, 15*time.Second)
	defer cancel()
	if err := o.gh.SetStatus(ctx, pr.Repo, run.SHA, state, statusContext, desc); err != nil {
		o.log.Warn("set status failed", "run", run.ID, "err", err)
	}
}

func descHash(title, body string) string {
	h := sha256.Sum256([]byte(title + "\x00" + body))
	return hex.EncodeToString(h[:6])
}

func trim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\n' || s[0] == '\r' || s[0] == '\t') {
		s = s[1:]
	}
	return s
}

// PRSource lists recent PRs across everything the GitHub App can see.
type PRSource interface {
	ListRecentPRs(ctx context.Context, since time.Time, fn func(repo string, p github.PRPayload) error) error
}

type SyncStatus struct {
	Enabled    bool   `json:"enabled"`
	Running    bool   `json:"running"`
	Days       int    `json:"days"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	PRs        int    `json:"prs"`
	Error      string `json:"error"`
}

func (o *Service) SyncStatus() SyncStatus {
	o.syncMu.Lock()
	defer o.syncMu.Unlock()
	st := o.sync
	st.Enabled, st.Days = o.source != nil, o.cfg.BackfillDays
	return st
}

// StartSync begins a background backfill of the last BackfillDays days of PRs.
// It returns false if no GitHub App is configured or a sync is already running.
// Backfilled PRs are only recorded; they do not trigger AI reviews.
func (o *Service) StartSync() bool {
	o.syncMu.Lock()
	if o.source == nil || o.sync.Running {
		o.syncMu.Unlock()
		return false
	}
	o.sync = SyncStatus{Running: true, StartedAt: time.Now().UTC().Format(time.RFC3339)}
	o.syncMu.Unlock()

	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		since := time.Now().AddDate(0, 0, -o.cfg.BackfillDays)
		count := 0
		err := o.source.ListRecentPRs(o.baseCtx, since, func(repo string, p github.PRPayload) error {
			if !o.cfg.RepoAllowed(repo) {
				return nil
			}
			if _, err := o.saveGitHubPR(o.baseCtx, repo, p.Number, p); err != nil {
				return err
			}
			count++
			return nil
		})
		o.syncMu.Lock()
		o.sync.Running, o.sync.PRs = false, count
		o.sync.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		if err != nil {
			o.sync.Error = err.Error()
			o.log.Error("sync finished with errors", "prs", count, "err", err)
		} else {
			o.log.Info("sync finished", "prs", count, "days", o.cfg.BackfillDays)
		}
		o.syncMu.Unlock()
	}()
	return true
}
