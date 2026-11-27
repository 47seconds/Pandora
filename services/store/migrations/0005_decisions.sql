-- Human decisions on runs the AI flagged as needing review (audit trail).
-- 'approved' moves the run to ai_passed; 'rejected' moves it to rejected and closes the PR.
CREATE TABLE decisions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id INTEGER NOT NULL REFERENCES runs(id),
  pr_id INTEGER NOT NULL REFERENCES prs(id),
  sha TEXT NOT NULL,
  decision TEXT NOT NULL CHECK (decision IN ('approved', 'rejected')),
  reason TEXT NOT NULL DEFAULT '',
  actor_id INTEGER NOT NULL DEFAULT 0,
  actor_login TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX idx_decisions_pr ON decisions(pr_id);
CREATE INDEX idx_decisions_run ON decisions(run_id);
