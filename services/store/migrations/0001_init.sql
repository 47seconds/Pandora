-- Phase 1: PR tracking, review runs, audit events, users.
CREATE TABLE IF NOT EXISTS prs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  repo TEXT NOT NULL,
  number INTEGER NOT NULL,
  title TEXT NOT NULL DEFAULT '',
  author TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'open',
  head_sha TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (repo, number)
);
CREATE TABLE IF NOT EXISTS runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  pr_id INTEGER NOT NULL REFERENCES prs(id),
  sha TEXT NOT NULL,
  desc_hash TEXT NOT NULL,
  status TEXT NOT NULL,
  verdict TEXT NOT NULL DEFAULT '',
  report TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (pr_id, sha, desc_hash)
);
CREATE INDEX IF NOT EXISTS idx_runs_pr ON runs(pr_id);
CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id INTEGER NOT NULL DEFAULT 0,
  kind TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

-- Accounts. Roles: admin (manage users), reviewer (approve/reject PRs, later phase),
-- developer (default; read-only). github_login links an account to GitHub PR authors
-- and is set by an admin only, so it cannot be self-claimed.
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  email TEXT NOT NULL UNIQUE COLLATE NOCASE,
  name TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'developer' CHECK (role IN ('admin', 'reviewer', 'developer')),
  github_login TEXT NOT NULL DEFAULT '',
  active INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  last_login_at TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_github_login ON users(github_login) WHERE github_login <> '';
