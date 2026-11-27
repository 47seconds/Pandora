package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"vocallabs.com/pandora/services/model"
)

var ErrNotFound = errors.New("not found")

const userCols = `id, github_id, github_login, name, email, avatar_url, role, active, created_at, last_login_at`

func scanUser(row interface{ Scan(...any) error }) (*model.User, error) {
	u := &model.User{}
	var gid sql.NullInt64
	var active int
	if err := row.Scan(&u.ID, &gid, &u.GitHubLogin, &u.Name, &u.Email, &u.AvatarURL, &u.Role, &active, &u.CreatedAt, &u.LastLoginAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.GitHubID, u.Active = gid.Int64, active == 1
	return u, nil
}

// UpsertGitHubUser records a GitHub sign-in. New accounts start as "pending".
// A legacy account linked only by github_login adopts the GitHub id. bootstrapAdmin,
// if it matches the login, is always an admin (so the very first admin can exist).
func (s *Store) UpsertGitHubUser(ctx context.Context, g model.GitHubProfile, bootstrapAdmin string) (*model.User, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	// A login belongs to one account: if another account still holds it (renamed or
	// released), clear it there so this account can take it.
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET github_login='' WHERE github_login=? COLLATE NOCASE AND github_id IS NOT NULL AND github_id<>?`,
		g.Login, g.ID); err != nil {
		return nil, false, err
	}

	var id int64
	created := false
	err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE github_id=?`, g.ID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx,
			`SELECT id FROM users WHERE github_id IS NULL AND github_login=? COLLATE NOCASE`, g.Login).Scan(&id)
	}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		t := now()
		if err := tx.QueryRowContext(ctx, `INSERT INTO users (github_id, role, created_at, updated_at) VALUES (?, ?, ?, ?) RETURNING id`,
			g.ID, model.RolePending, t, t).Scan(&id); err != nil {
			return nil, false, err
		}
		created = true
	case err != nil:
		return nil, false, err
	}

	t := now()
	row := tx.QueryRowContext(ctx, `
UPDATE users SET github_id=?, github_login=?, name=?, avatar_url=?,
  email=CASE WHEN ?<>'' THEN ? ELSE email END, last_login_at=?, updated_at=?
WHERE id=? RETURNING `+userCols, g.ID, g.Login, g.Name, g.AvatarURL, g.Email, g.Email, t, t, id)
	u, err := scanUser(row)
	if err != nil {
		return nil, false, err
	}
	if bootstrapAdmin != "" && strings.EqualFold(g.Login, bootstrapAdmin) && u.Role != model.RoleAdmin {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET role=? WHERE id=?`, model.RoleAdmin, id); err != nil {
			return nil, false, err
		}
		u.Role = model.RoleAdmin
	}
	return u, created, tx.Commit()
}

func (s *Store) GetUser(ctx context.Context, id int64) (*model.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id=?`, id))
}

func (s *Store) ListUsers(ctx context.Context) ([]model.User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY
	  CASE role WHEN 'pending' THEN 0 WHEN 'admin' THEN 1 ELSE 2 END, github_login COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role='admin' AND active=1`).Scan(&n)
	return n, err
}

// UpdateUser applies admin-editable fields; nil means "leave unchanged".
func (s *Store) UpdateUser(ctx context.Context, id int64, role *string, active *bool) (*model.User, error) {
	u, err := s.GetUser(ctx, id)
	if err != nil {
		return nil, err
	}
	if role != nil {
		u.Role = *role
	}
	if active != nil {
		u.Active = *active
	}
	_, err = s.db.ExecContext(ctx, `UPDATE users SET role=?, active=?, updated_at=? WHERE id=?`, u.Role, b2i(u.Active), now(), id)
	return u, err
}
