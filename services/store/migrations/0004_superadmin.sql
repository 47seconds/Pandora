-- Adds the 'superadmin' role: the primary owner. The database enforces that there is
-- at most one (unique partial index). Only the superadmin can demote or deactivate an
-- admin, or delete an account; plain admins can only approve (pending -> user) and
-- promote (user -> admin).
CREATE TABLE users_new (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  github_id INTEGER UNIQUE,
  github_login TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',
  email TEXT NOT NULL DEFAULT '',
  avatar_url TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL DEFAULT 'pending' CHECK (role IN ('pending', 'user', 'admin', 'superadmin')),
  active INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  last_login_at TEXT NOT NULL DEFAULT ''
);
INSERT INTO users_new (id, github_id, github_login, name, email, avatar_url, role, active, created_at, updated_at, last_login_at)
  SELECT id, github_id, github_login, name, email, avatar_url, role, active, created_at, updated_at, last_login_at FROM users;
DROP TABLE users;
ALTER TABLE users_new RENAME TO users;
CREATE UNIQUE INDEX idx_users_github_login ON users(github_login COLLATE NOCASE) WHERE github_login <> '';
CREATE UNIQUE INDEX idx_users_one_superadmin ON users(role) WHERE role = 'superadmin';
