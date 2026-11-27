package service

import (
	"context"
	"fmt"
	"time"

	"vocallabs.com/pandora/services/model"
)

func (o *Service) auditRunID(p *model.PRWithRun) int64 {
	if p.LatestRun != nil {
		return p.LatestRun.ID
	}
	return 0
}

// AssignReviewer makes login a reviewer of the PR (admins only; the caller checks). The
// assignment is recorded in Pandora first. It is then mirrored to GitHub so the person gets a
// GitHub notification, but that is best effort: GitHub only accepts collaborators with access to the
// repo, and Pandora's assignment stands either way. A non-empty warning says the mirror failed.
func (o *Service) AssignReviewer(ctx context.Context, actor *model.User, p *model.PRWithRun, login string) (warning string, err error) {
	added, err := o.store.RequestReviewer(ctx, p.ID, login, "pandora")
	if err != nil || !added {
		return "", err
	}
	o.store.AddEvent(ctx, o.auditRunID(p), "reviewer_added", fmt.Sprintf("%s added by %s", login, actor.GitHubLogin))
	o.log.Info("reviewer added", "pr", p.Repo, "number", p.Number, "reviewer", login, "by", actor.GitHubLogin)

	rctx, cancel := context.WithTimeout(o.baseCtx, 20*time.Second)
	defer cancel()
	if gerr := o.gh.RequestReviewers(rctx, p.Repo, p.Number, []string{login}); gerr != nil {
		o.log.Warn("could not request reviewer on github", "reviewer", login, "err", gerr)
		return fmt.Sprintf("Added in Pandora, but GitHub did not request a review from %s (they may not have access to the repository). They can still decide here.", login), nil
	}
	return "", nil
}

// UnassignReviewer removes login from the PR's reviewers. It always succeeds locally and the
// removal sticks; withdrawing the request on GitHub is best effort.
func (o *Service) UnassignReviewer(ctx context.Context, actor *model.User, p *model.PRWithRun, login string) (removed bool, err error) {
	removed, err = o.store.RemoveReviewer(ctx, p.ID, login)
	if err != nil || !removed {
		return removed, err
	}
	o.store.AddEvent(ctx, o.auditRunID(p), "reviewer_removed", fmt.Sprintf("%s removed by %s", login, actor.GitHubLogin))
	o.log.Info("reviewer removed", "pr", p.Repo, "number", p.Number, "reviewer", login, "by", actor.GitHubLogin)

	rctx, cancel := context.WithTimeout(o.baseCtx, 20*time.Second)
	defer cancel()
	if gerr := o.gh.RemoveReviewers(rctx, p.Repo, p.Number, []string{login}); gerr != nil {
		o.log.Debug("could not withdraw review request on github", "reviewer", login, "err", gerr)
	}
	return true, nil
}
