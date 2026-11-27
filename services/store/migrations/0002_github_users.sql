-- Users now come from GitHub login (no passwords). Roles: pending (new, awaiting
-- approval) -> user -> admin. Identity is the stable GitHub numeric id; the login
-- (username) can change and is refreshed on every sign-in.
--
-- Legacy email/password accounts that were never linked to a GitHub login cannot sign
-- in any more and are dropped. Linked ones (github_login set) are kept and adopt their
-- GitHub id on first GitHub login (admin stays admin, everyone else becomes 'user').
CREATE TABLE users_new (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  github_id INTEGER UNIQUE,
  github_login TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',
  email TEXT NOT NULL DEFAULT '',
  avatar_url TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL DEFAULT 'pending' CHECK (role IN ('pending', 'user', 'admin')),
  active INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  last_login_at TEXT NOT NULL DEFAULT ''
);
INSERT INTO users_new (id, github_login, name, email, role, active, created_at, updated_at, last_login_at)
  SELECT id, github_login, name, email,
         CASE role WHEN 'admin' THEN 'admin' ELSE 'user' END,
         active, created_at, updated_at, last_login_at
  FROM users WHERE github_login <> '';
DROP TABLE users;
ALTER TABLE users_new RENAME TO users;
CREATE UNIQUE INDEX idx_users_github_login ON users(github_login COLLATE NOCASE) WHERE github_login <> '';
