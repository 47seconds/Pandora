// Package store persists PRs, review runs and audit events in SQLite.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"vocallabs.com/pandora/services/model"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // single writer; plenty for this workload
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// CreateRun registers a run for (pr, sha, descHash). It is idempotent: duplicate
// webhook deliveries return created=false. A run that was superseded or errored is
// revived (e.g. PR reopened) and reported as created=true.
func (s *Store) CreateRun(ctx context.Context, prID int64, sha, descHash string) (id int64, created bool, err error) {
	t := now()
	res, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO runs (pr_id, sha, desc_hash, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)`, prID, sha, descHash, model.StatusQueued, t, t)
	if err != nil {
		return 0, false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		id, err = res.LastInsertId()
		return id, true, err
	}
	var status string
	if err = s.db.QueryRowContext(ctx,
		`SELECT id, status FROM runs WHERE pr_id=? AND sha=? AND desc_hash=?`, prID, sha, descHash).Scan(&id, &status); err != nil {
		return 0, false, err
	}
	if status == model.StatusSuperseded || status == model.StatusError {
		_, err = s.db.ExecContext(ctx,
			`UPDATE runs SET status=?, verdict='', report='', error='', updated_at=? WHERE id=?`, model.StatusQueued, now(), id)
		return id, err == nil, err
	}
	return id, false, nil
}

// SupersedeOthers marks every run of the PR except keepID as superseded and
// returns the ids that changed (so in-flight work can be cancelled).
// Pass keepID=0 to supersede all (PR closed).
func (s *Store) SupersedeOthers(ctx context.Context, prID, keepID int64) ([]int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM runs WHERE pr_id=? AND id<>? AND status NOT IN (?, ?)`, prID, keepID, model.StatusSuperseded, model.StatusRejected)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if _, err := tx.ExecContext(ctx,
		`UPDATE runs SET status=?, updated_at=? WHERE pr_id=? AND id<>? AND status NOT IN (?, ?)`,
		model.StatusSuperseded, now(), prID, keepID, model.StatusSuperseded, model.StatusRejected); err != nil {
		return nil, err
	}
	return ids, tx.Commit()
}

// StartReview moves queued -> ai_review. False means the run is no longer eligible.
func (s *Store) StartReview(ctx context.Context, runID int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status=?, updated_at=? WHERE id=? AND status IN (?, ?)`,
		model.StatusAIReview, now(), runID, model.StatusQueued, model.StatusAIReview)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// FinishReview records the outcome only if the run is still in ai_review
// (i.e. was not superseded meanwhile). Returns whether it was applied.
func (s *Store) FinishReview(ctx context.Context, runID int64, status, verdict, reportJSON string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status=?, verdict=?, report=?, updated_at=? WHERE id=? AND status=?`,
		status, verdict, reportJSON, now(), runID, model.StatusAIReview)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// FailRun records an error only if the run is still in ai_review.
func (s *Store) FailRun(ctx context.Context, runID int64, msg string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status=?, error=?, updated_at=? WHERE id=? AND status=?`,
		model.StatusError, msg, now(), runID, model.StatusAIReview)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SupersedeRun marks a single run superseded (e.g. GitHub head moved on).
func (s *Store) SupersedeRun(ctx context.Context, runID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET status=?, updated_at=? WHERE id=?`, model.StatusSuperseded, now(), runID)
	return err
}

func (s *Store) GetRunWithPR(ctx context.Context, runID int64) (*model.Run, *model.PR, error) {
	r, p := &model.Run{}, &model.PR{}
	err := s.db.QueryRowContext(ctx, `
SELECT r.id, r.pr_id, r.sha, r.desc_hash, r.status, r.verdict, r.report, r.error, r.created_at, r.updated_at,
       p.id, p.repo, p.number, p.title, p.author, p.state, p.head_sha, p.created_at, p.updated_at
FROM runs r JOIN prs p ON p.id = r.pr_id WHERE r.id=?`, runID).Scan(
		&r.ID, &r.PRID, &r.SHA, &r.DescHash, &r.Status, &r.Verdict, &r.Report, &r.Error, &r.CreatedAt, &r.UpdatedAt,
		&p.ID, &p.Repo, &p.Number, &p.Title, &p.Author, &p.State, &p.HeadSHA, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, nil, err
	}
	return r, p, nil
}

// PendingRunIDs returns runs that were queued or mid-review (used after a restart).
func (s *Store) PendingRunIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM runs WHERE status IN (?, ?) ORDER BY id`, model.StatusQueued, model.StatusAIReview)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListRuns returns all runs of a PR, newest first.
func (s *Store) ListRuns(ctx context.Context, prID int64) ([]model.Run, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, pr_id, sha, desc_hash, status, verdict, report, error, created_at, updated_at
FROM runs WHERE pr_id=? ORDER BY id DESC`, prID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Run{}
	for rows.Next() {
		var r model.Run
		if err := rows.Scan(&r.ID, &r.PRID, &r.SHA, &r.DescHash, &r.Status, &r.Verdict, &r.Report, &r.Error, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AddEvent appends an audit entry.
func (s *Store) AddEvent(ctx context.Context, runID int64, kind, detail string) {
	_, _ = s.db.ExecContext(ctx, `INSERT INTO events (run_id, kind, detail, created_at) VALUES (?, ?, ?, ?)`,
		runID, kind, detail, now())
}
