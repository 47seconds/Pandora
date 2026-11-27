package store

import (
	"context"
	"errors"

	"vocallabs.com/pandora/services/model"
)

// ErrStale means the run is no longer awaiting a decision: it was superseded by a new commit
// or edit, was already decided, or the PR is closed. The caller should refresh and look again.
var ErrStale = errors.New("this pull request changed or was already decided")

// DecideRun records a human verdict on a run in needs_human. It succeeds only if that run is
// still the PR's latest, was reviewed at the PR's current head commit, and the PR is open, so an
// approval can never apply to code the reviewer did not see. Approve moves the run to ai_passed,
// reject to rejected. The status change and the audit row are one transaction.
func (s *Store) DecideRun(ctx context.Context, runID int64, actor *model.User, decision, reason string) (*model.Decision, error) {
	newStatus := model.StatusAIPassed
	if decision == model.DecisionRejected {
		newStatus = model.StatusRejected
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
UPDATE runs SET status=?, updated_at=? WHERE id=? AND status=?
  AND id = (SELECT MAX(r2.id) FROM runs r2 WHERE r2.pr_id = runs.pr_id)
  AND sha = (SELECT p.head_sha FROM prs p WHERE p.id = runs.pr_id)
  AND (SELECT p.state FROM prs p WHERE p.id = runs.pr_id) = 'open'`,
		newStatus, now(), runID, model.StatusNeedsHuman)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrStale
	}
	d := &model.Decision{RunID: runID, Decision: decision, Reason: reason, ActorID: actor.ID, ActorLogin: actor.GitHubLogin, CreatedAt: now()}
	if err := tx.QueryRowContext(ctx, `SELECT pr_id, sha FROM runs WHERE id=?`, runID).Scan(&d.PRID, &d.SHA); err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `
INSERT INTO decisions (run_id, pr_id, sha, decision, reason, actor_id, actor_login, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		d.RunID, d.PRID, d.SHA, d.Decision, d.Reason, d.ActorID, d.ActorLogin, d.CreatedAt).Scan(&d.ID)
	if err != nil {
		return nil, err
	}
	return d, tx.Commit()
}

// RevertRejection undoes a rejection whose follow-up on GitHub (closing the PR) failed, so the
// reviewer can try again: the run goes back to needs_human and the audit row is removed.
func (s *Store) RevertRejection(ctx context.Context, runID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM decisions WHERE run_id=? AND decision=?`, runID, model.DecisionRejected); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status=?, updated_at=? WHERE id=? AND status=?`,
		model.StatusNeedsHuman, now(), runID, model.StatusRejected); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkPRClosed reflects a close we just performed on GitHub; the webhook confirms it later.
func (s *Store) MarkPRClosed(ctx context.Context, prID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE prs SET state='closed', closed_at=?, updated_at=? WHERE id=?`, now(), now(), prID)
	return err
}

// ListDecisions returns every human decision on a PR, newest first.
func (s *Store) ListDecisions(ctx context.Context, prID int64) ([]model.Decision, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, run_id, pr_id, sha, decision, reason, actor_id, actor_login, created_at
FROM decisions WHERE pr_id=? ORDER BY id DESC`, prID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Decision{}
	for rows.Next() {
		var d model.Decision
		if err := rows.Scan(&d.ID, &d.RunID, &d.PRID, &d.SHA, &d.Decision, &d.Reason, &d.ActorID, &d.ActorLogin, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
