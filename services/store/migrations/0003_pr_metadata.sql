-- PR metadata needed for "my PRs", "review PRs" and admin analytics.
ALTER TABLE prs ADD COLUMN base_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE prs ADD COLUMN draft INTEGER NOT NULL DEFAULT 0;
ALTER TABLE prs ADD COLUMN merged INTEGER NOT NULL DEFAULT 0;
ALTER TABLE prs ADD COLUMN gh_created_at TEXT NOT NULL DEFAULT '';
ALTER TABLE prs ADD COLUMN gh_updated_at TEXT NOT NULL DEFAULT '';
ALTER TABLE prs ADD COLUMN closed_at TEXT NOT NULL DEFAULT '';
ALTER TABLE prs ADD COLUMN merged_at TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_prs_author ON prs(author COLLATE NOCASE);
CREATE INDEX idx_prs_state ON prs(state);

-- Requested reviewers (users only; team requests are not tracked yet).
CREATE TABLE pr_reviewers (
  pr_id INTEGER NOT NULL REFERENCES prs(id) ON DELETE CASCADE,
  login TEXT NOT NULL COLLATE NOCASE,
  PRIMARY KEY (pr_id, login)
);
CREATE INDEX idx_pr_reviewers_login ON pr_reviewers(login);
