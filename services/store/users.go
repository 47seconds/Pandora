package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"vocallabs.com/pandora/services/model"
)

var (
	ErrEmailTaken = errors.New("email already registered")
	ErrNotFound   = errors.New("not found")
	ErrConflict   = errors.New("conflict")
)

const userCols = `id, email, name, password_hash, role, github_login, active, created_at, last_login_at`

func scanUser(row interface{ Scan(...any) error }) (*model.User, error) {
	u := &model.User{}
	var active int
	if err := row.Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.Role, &u.GitHubLogin, &active, &u.CreatedAt, &u.LastLoginAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.Active = active == 1
	return u, nil
}

// CreateUser inserts an account. adminEmail, if non-empty, is the only address that
// becomes admin on signup; if empty, the very first account does. Everyone else is a developer.
func (s *Store) CreateUser(ctx context.Context, email, name, passwordHash, adminEmail string) (*model.User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return nil, err
	}
	role := model.RoleDeveloper
	if adminEmail != "" {
		if strings.EqualFold(email, adminEmail) {
			role = model.RoleAdmin
		}
	} else if n == 0 {
		role = model.RoleAdmin
	}
	t := now()
	row := tx.QueryRowContext(ctx, `
INSERT INTO users (email, name, password_hash, role, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?) RETURNING `+userCols, email, name, passwordHash, role, t, t)
	u, err := scanUser(row)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: users.email") {
			return nil, ErrEmailTaken
		}
		return nil, err
	}
	return u, tx.Commit()
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (*model.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE email=?`, email))
}

func (s *Store) GetUser(ctx context.Context, id int64) (*model.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id=?`, id))
}

func (s *Store) TouchLogin(ctx context.Context, id int64) {
	_, _ = s.db.ExecContext(ctx, `UPDATE users SET last_login_at=? WHERE id=?`, now(), id)
}

func (s *Store) ListUsers(ctx context.Context) ([]model.User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY id`)
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

// UpdateUser applies admin-editable fields; nil means "leave unchanged".
func (s *Store) UpdateUser(ctx context.Context, id int64, role, githubLogin *string, active *bool) (*model.User, error) {
	u, err := s.GetUser(ctx, id)
	if err != nil {
		return nil, err
	}
	if role != nil {
		u.Role = *role
	}
	if githubLogin != nil {
		u.GitHubLogin = *githubLogin
	}
	if active != nil {
		u.Active = *active
	}
	a := 0
	if u.Active {
		a = 1
	}
	_, err = s.db.ExecContext(ctx, `UPDATE users SET role=?, github_login=?, active=?, updated_at=? WHERE id=?`,
		u.Role, u.GitHubLogin, a, now(), id)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: users.github_login") {
		return nil, ErrConflict
	}
	return u, err
}
