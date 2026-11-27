package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"vocallabs.com/pandora/services/model"
)

const prCols = `p.id, p.repo, p.number, p.title, p.author, p.state, p.head_sha, p.base_ref, p.draft, p.merged,
 p.gh_created_at, p.gh_updated_at, p.closed_at, p.merged_at, p.created_at, p.updated_at`

const runCols = `r.id, r.pr_id, r.sha, r.desc_hash, r.status, r.verdict, r.report, r.error, r.created_at, r.updated_at`

// UpsertPR creates or refreshes a PR row from GitHub data and returns its id.
func (s *Store) UpsertPR(ctx context.Context, p model.PR) (int64, error) {
	t := now()
	if p.State == "" {
		p.State = "open"
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `
INSERT INTO prs (repo, number, title, author, state, head_sha, base_ref, draft, merged,
                 gh_created_at, gh_updated_at, closed_at, merged_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (repo, number) DO UPDATE SET
  title=excluded.title, author=excluded.author, state=excluded.state, head_sha=excluded.head_sha,
  base_ref=CASE WHEN excluded.base_ref<>'' THEN excluded.base_ref ELSE prs.base_ref END,
  draft=excluded.draft, merged=excluded.merged,
  gh_created_at=CASE WHEN excluded.gh_created_at<>'' THEN excluded.gh_created_at ELSE prs.gh_created_at END,
  gh_updated_at=CASE WHEN excluded.gh_updated_at<>'' THEN excluded.gh_updated_at ELSE prs.gh_updated_at END,
  closed_at=excluded.closed_at, merged_at=excluded.merged_at, updated_at=excluded.updated_at
RETURNING id`,
		p.Repo, p.Number, p.Title, p.Author, p.State, p.HeadSHA, p.BaseRef, b2i(p.Draft), b2i(p.Merged),
		p.GHCreatedAt, p.GHUpdatedAt, p.ClosedAt, p.MergedAt, t, t).Scan(&id)
	return id, err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SetReviewers replaces the requested-reviewer set of a PR.
func (s *Store) SetReviewers(ctx context.Context, prID int64, logins []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM pr_reviewers WHERE pr_id=?`, prID); err != nil {
		return err
	}
	for _, l := range logins {
		if l == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO pr_reviewers (pr_id, login) VALUES (?, ?)`, prID, l); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type PRFilter struct {
	State    string // open | closed (closed, unmerged) | merged | "" (all)
	Author   string
	Reviewer string
	Repo     string
	Limit    int
	Offset   int
}

func scanPRWithRun(row interface{ Scan(...any) error }) (*model.PRWithRun, error) {
	var x model.PRWithRun
	var draft, merged int
	var rid, rprid sql.NullInt64
	var rsha, rdh, rst, rv, rrep, rerr, rc, ru sql.NullString
	if err := row.Scan(&x.ID, &x.Repo, &x.Number, &x.Title, &x.Author, &x.State, &x.HeadSHA, &x.BaseRef, &draft, &merged,
		&x.GHCreatedAt, &x.GHUpdatedAt, &x.ClosedAt, &x.MergedAt, &x.CreatedAt, &x.UpdatedAt,
		&rid, &rprid, &rsha, &rdh, &rst, &rv, &rrep, &rerr, &rc, &ru); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	x.Draft, x.Merged = draft == 1, merged == 1
	x.Reviewers = []string{}
	if rid.Valid {
		x.LatestRun = &model.Run{ID: rid.Int64, PRID: rprid.Int64, SHA: rsha.String, DescHash: rdh.String, Status: rst.String,
			Verdict: rv.String, Report: rrep.String, Error: rerr.String, CreatedAt: rc.String, UpdatedAt: ru.String}
	}
	return &x, nil
}

const prSelect = `SELECT ` + prCols + `, ` + runCols + `
FROM prs p LEFT JOIN runs r ON r.id = (SELECT MAX(id) FROM runs WHERE pr_id = p.id)`

// ListPRs returns PRs (most recently updated on GitHub first) with latest run and reviewers.
func (s *Store) ListPRs(ctx context.Context, f PRFilter) ([]model.PRWithRun, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 100
	}
	var where []string
	var args []any
	switch f.State {
	case "open":
		where = append(where, "p.state='open'")
	case "closed":
		where = append(where, "p.state='closed' AND p.merged=0")
	case "merged":
		where = append(where, "p.merged=1")
	}
	if f.Author != "" {
		where = append(where, "p.author = ? COLLATE NOCASE")
		args = append(args, f.Author)
	}
	if f.Repo != "" {
		where = append(where, "p.repo = ? COLLATE NOCASE")
		args = append(args, f.Repo)
	}
	if f.Reviewer != "" {
		where = append(where, "EXISTS (SELECT 1 FROM pr_reviewers rv WHERE rv.pr_id = p.id AND rv.login = ?)")
		args = append(args, f.Reviewer)
	}
	q := prSelect
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY COALESCE(NULLIF(p.gh_updated_at,''), p.updated_at) DESC, p.id DESC LIMIT ? OFFSET ?"
	args = append(args, f.Limit, f.Offset)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PRWithRun{}
	for rows.Next() {
		x, err := scanPRWithRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.attachReviewers(ctx, out)
}

// GetPR returns one PR with its latest run and reviewers.
func (s *Store) GetPR(ctx context.Context, id int64) (*model.PRWithRun, error) {
	x, err := scanPRWithRun(s.db.QueryRowContext(ctx, prSelect+" WHERE p.id = ?", id))
	if err != nil {
		return nil, err
	}
	one := []model.PRWithRun{*x}
	if err := s.attachReviewers(ctx, one); err != nil {
		return nil, err
	}
	return &one[0], nil
}

func (s *Store) attachReviewers(ctx context.Context, prs []model.PRWithRun) error {
	if len(prs) == 0 {
		return nil
	}
	idx := map[int64]int{}
	ph := make([]string, len(prs))
	args := make([]any, len(prs))
	for i, p := range prs {
		idx[p.ID] = i
		ph[i] = "?"
		args[i] = p.ID
	}
	rows, err := s.db.QueryContext(ctx, `SELECT pr_id, login FROM pr_reviewers WHERE pr_id IN (`+strings.Join(ph, ",")+`) ORDER BY login`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var login string
		if err := rows.Scan(&id, &login); err != nil {
			return err
		}
		prs[idx[id]].Reviewers = append(prs[idx[id]].Reviewers, login)
	}
	return rows.Err()
}

const prCreatedExpr = `COALESCE(NULLIF(p.gh_created_at,''), p.created_at)`

// Stats computes the admin overview numbers.
func (s *Store) Stats(ctx context.Context) (*model.Stats, error) {
	st := &model.Stats{ByAIStatus: map[string]int{}, TopAuthors: []model.Count{}, AwaitingReview: []model.Count{}, ByRepo: []model.Count{}}
	err := s.db.QueryRowContext(ctx, `
SELECT COALESCE(SUM(state='open'),0), COALESCE(SUM(state='closed' AND merged=0),0),
       COALESCE(SUM(merged=1),0), COALESCE(SUM(state='open' AND draft=1),0) FROM prs`).
		Scan(&st.Open, &st.Closed, &st.Merged, &st.Drafts)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT COALESCE(r.status, 'none'), COUNT(*) FROM prs p
LEFT JOIN runs r ON r.id = (SELECT MAX(id) FROM runs WHERE pr_id = p.id)
WHERE p.state='open' GROUP BY 1`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			rows.Close()
			return nil, err
		}
		st.ByAIStatus[k] = n
	}
	rows.Close()

	counts := func(q string, args ...any) ([]model.Count, error) {
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []model.Count{}
		for rows.Next() {
			var c model.Count
			if err := rows.Scan(&c.Name, &c.Open, &c.Total); err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		return out, rows.Err()
	}
	if st.TopAuthors, err = counts(`SELECT author, SUM(state='open'), COUNT(*) FROM prs WHERE author<>'' GROUP BY author COLLATE NOCASE ORDER BY 3 DESC, 1 LIMIT 10`); err != nil {
		return nil, err
	}
	if st.AwaitingReview, err = counts(`SELECT rv.login, COUNT(*), COUNT(*) FROM pr_reviewers rv JOIN prs p ON p.id=rv.pr_id WHERE p.state='open' GROUP BY rv.login ORDER BY 2 DESC, 1 LIMIT 10`); err != nil {
		return nil, err
	}
	if st.ByRepo, err = counts(`SELECT repo, SUM(state='open'), COUNT(*) FROM prs GROUP BY repo ORDER BY 3 DESC, 1 LIMIT 15`); err != nil {
		return nil, err
	}

	// last 12 weeks, weeks start on Monday
	weekOf := func(col string) string { return `strftime('%Y-%m-%d', ` + col + `, 'weekday 0', '-6 days')` }
	since := time.Now().UTC().AddDate(0, 0, -7*12).Format(time.RFC3339)
	opened, merged := map[string]int{}, map[string]int{}
	for _, q := range []struct {
		m   map[string]int
		sql string
	}{
		{opened, `SELECT ` + weekOf(prCreatedExpr) + `, COUNT(*) FROM prs p WHERE ` + prCreatedExpr + ` >= ? GROUP BY 1`},
		{merged, `SELECT ` + weekOf("p.merged_at") + `, COUNT(*) FROM prs p WHERE p.merged=1 AND p.merged_at >= ? GROUP BY 1`},
	} {
		rows, err := s.db.QueryContext(ctx, q.sql, since)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var w sql.NullString
			var n int
			if err := rows.Scan(&w, &n); err != nil {
				rows.Close()
				return nil, err
			}
			if w.Valid {
				q.m[w.String] = n
			}
		}
		rows.Close()
	}
	monday := time.Now().UTC()
	monday = monday.AddDate(0, 0, -((int(monday.Weekday()) + 6) % 7))
	for i := 11; i >= 0; i-- {
		w := monday.AddDate(0, 0, -7*i).Format("2006-01-02")
		st.Weekly = append(st.Weekly, model.WeekCount{Week: w, Opened: opened[w], Merged: merged[w]})
	}

	var avg sql.NullFloat64
	if err := s.db.QueryRowContext(ctx, `
SELECT AVG((julianday(p.merged_at) - julianday(`+prCreatedExpr+`)) * 24) FROM prs p WHERE p.merged=1 AND p.merged_at<>''`).Scan(&avg); err != nil {
		return nil, err
	}
	st.AvgHoursToMerge = avg.Float64
	return st, nil
}
