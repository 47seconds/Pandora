package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"vocallabs.com/pandora/services/model"
)

func open(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestMigrateIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	for i := 0; i < 2; i++ { // reopening must not re-apply or fail
		st, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != 4 {
			t.Fatalf("run %d: migrations=%d err=%v", i, n, err)
		}
		st.Close()
	}
}

// A database created by phase 1 (email/password users) must upgrade cleanly.
func TestMigrationFromLegacyUsers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := migrationsFS.ReadFile("migrations/0001_init.sql")
	db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)`)
	if _, err := db.Exec(string(first)); err != nil {
		t.Fatal(err)
	}
	db.Exec(`INSERT INTO schema_migrations VALUES (1, '0001_init.sql', 'x')`)
	ts := "2026-01-01T00:00:00Z"
	for _, u := range [][]string{
		{"admin@x.com", "admin", "octoadmin"}, // linked admin: stays admin
		{"dev@x.com", "reviewer", "octodev"},  // linked non-admin: becomes user
		{"ghost@x.com", "developer", ""},      // never linked: cannot sign in, dropped
	} {
		if _, err := db.Exec(`INSERT INTO users (email, password_hash, role, github_login, created_at, updated_at) VALUES (?, 'h', ?, ?, ?, ?)`,
			u[0], u[1], u[2], ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer st.Close()
	users, _ := st.ListUsers(context.Background())
	if len(users) != 2 {
		t.Fatalf("users after upgrade: %+v", users)
	}
	roles := map[string]string{}
	for _, u := range users {
		roles[u.GitHubLogin] = u.Role
	}
	if roles["octoadmin"] != "admin" || roles["octodev"] != "user" {
		t.Fatalf("roles: %v", roles)
	}
	// the legacy admin adopts their GitHub id on first sign-in and stays admin
	u, created, err := st.UpsertGitHubUser(context.Background(), model.GitHubProfile{ID: 9, Login: "OctoAdmin", Name: "Octo"}, "")
	if err != nil || created || u.Role != "admin" || u.GitHubID != 9 {
		t.Fatalf("adopt: %+v created=%v err=%v", u, created, err)
	}
}

func TestUpsertGitHubUser(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	const boss = "boss"

	// first account with a bootstrap admin configured: pending, not owner
	u, created, err := st.UpsertGitHubUser(ctx, model.GitHubProfile{ID: 1, Login: "alice", Name: "Alice", Email: "a@x.com"}, boss)
	if err != nil || !created || u.Role != "pending" {
		t.Fatalf("new: %+v %v %v", u, created, err)
	}
	// renamed on GitHub: same account, new login; empty email keeps the old one
	u2, created, _ := st.UpsertGitHubUser(ctx, model.GitHubProfile{ID: 1, Login: "alice2", Name: "Alice", Email: ""}, boss)
	if created || u2.ID != u.ID || u2.GitHubLogin != "alice2" || u2.Email != "a@x.com" {
		t.Fatalf("rename: %+v", u2)
	}
	// someone else takes the old name: allowed, and does not break alice
	b, _, err := st.UpsertGitHubUser(ctx, model.GitHubProfile{ID: 2, Login: "ALICE2", Name: "Other"}, boss)
	if err != nil || b.ID == u.ID || b.GitHubLogin != "ALICE2" {
		t.Fatalf("login reuse: %+v %v", b, err)
	}
	if again, _ := st.GetUser(ctx, u.ID); again.GitHubLogin != "" || again.Role != "pending" {
		t.Fatalf("stale holder should lose the login only: %+v", again)
	}
	// the configured bootstrap login becomes superadmin, case-insensitively
	owner, _, _ := st.UpsertGitHubUser(ctx, model.GitHubProfile{ID: 3, Login: "Boss"}, boss)
	if owner.Role != "superadmin" {
		t.Fatalf("bootstrap: %+v", owner)
	}
	if n, _ := st.CountSuperAdmins(ctx); n != 1 {
		t.Fatalf("superadmins: %d", n)
	}
}

func TestSuperAdminRules(t *testing.T) {
	ctx := context.Background()

	// nothing configured: the very first account is the owner, later ones are pending
	st := open(t)
	first, _, _ := st.UpsertGitHubUser(ctx, model.GitHubProfile{ID: 1, Login: "first"}, "")
	second, _, _ := st.UpsertGitHubUser(ctx, model.GitHubProfile{ID: 2, Login: "second"}, "")
	if first.Role != "superadmin" || second.Role != "pending" {
		t.Fatalf("first=%s second=%s", first.Role, second.Role)
	}
	// the owner is re-affirmed on every sign-in
	if again, _, _ := st.UpsertGitHubUser(ctx, model.GitHubProfile{ID: 1, Login: "first"}, ""); again.Role != "superadmin" {
		t.Fatal("owner lost the role")
	}

	// configuring a bootstrap login later moves ownership: still exactly one superadmin
	newOwner, _, err := st.UpsertGitHubUser(ctx, model.GitHubProfile{ID: 2, Login: "second"}, "second")
	if err != nil || newOwner.Role != "superadmin" {
		t.Fatalf("new owner: %+v %v", newOwner, err)
	}
	if old, _ := st.GetUser(ctx, first.ID); old.Role != "admin" {
		t.Fatalf("previous owner becomes a plain admin, got %s", old.Role)
	}
	if n, _ := st.CountSuperAdmins(ctx); n != 1 {
		t.Fatalf("superadmins: %d", n)
	}
	// the database itself refuses a second superadmin
	if _, err := st.db.Exec(`UPDATE users SET role='superadmin' WHERE id=?`, first.ID); err == nil {
		t.Fatal("unique index must allow only one superadmin")
	}
	// delete
	if err := st.DeleteUser(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser(ctx, first.ID); err != ErrNotFound {
		t.Fatalf("second delete: %v", err)
	}
}

func TestListsAreNeverNil(t *testing.T) { // JSON must be [] not null, or the dashboard breaks
	st := open(t)
	ctx := context.Background()
	if prs, _ := st.ListPRs(ctx, PRFilter{}); prs == nil {
		t.Fatal("ListPRs nil")
	}
	if runs, _ := st.ListRuns(ctx, 1); runs == nil {
		t.Fatal("ListRuns nil")
	}
	if us, _ := st.ListUsers(ctx); us == nil {
		t.Fatal("ListUsers nil")
	}
	s, err := st.Stats(ctx)
	if err != nil || s.TopAuthors == nil || s.ByRepo == nil || s.AwaitingReview == nil || len(s.Weekly) != 12 {
		t.Fatalf("empty stats: %+v %v", s, err)
	}
}

func TestPRFiltersAndStats(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	add := func(repo string, n int, author, state string, merged bool, created, mergedAt string, reviewers ...string) {
		id, err := st.UpsertPR(ctx, model.PR{Repo: repo, Number: n, Author: author, State: state, Merged: merged, GHCreatedAt: created, MergedAt: mergedAt})
		if err != nil {
			t.Fatal(err)
		}
		st.SetReviewers(ctx, id, reviewers)
	}
	add("o/a", 1, "alice", "open", false, "2026-11-20T00:00:00Z", "", "bob")
	add("o/a", 2, "alice", "closed", true, "2026-11-10T00:00:00Z", "2026-11-11T00:00:00Z")
	add("o/b", 3, "bob", "closed", false, "2026-11-01T00:00:00Z", "")

	count := func(f PRFilter) int { l, _ := st.ListPRs(ctx, f); return len(l) }
	if count(PRFilter{State: "open"}) != 1 || count(PRFilter{State: "merged"}) != 1 || count(PRFilter{State: "closed"}) != 1 {
		t.Fatal("state filters")
	}
	if count(PRFilter{Author: "ALICE"}) != 2 || count(PRFilter{Repo: "O/B"}) != 1 || count(PRFilter{Reviewer: "bob", State: "open"}) != 1 {
		t.Fatal("author/repo/reviewer filters")
	}
	// paging
	if count(PRFilter{Limit: 2}) != 2 || count(PRFilter{Limit: 2, Offset: 2}) != 1 {
		t.Fatal("paging")
	}
	s, err := st.Stats(ctx)
	if err != nil || s.Open != 1 || s.Merged != 1 || s.Closed != 1 {
		t.Fatalf("stats: %+v %v", s, err)
	}
	if s.AvgHoursToMerge < 23.9 || s.AvgHoursToMerge > 24.1 {
		t.Fatalf("avg hours: %v", s.AvgHoursToMerge)
	}
	if s.TopAuthors[0].Name != "alice" || s.TopAuthors[0].Total != 2 || s.AwaitingReview[0].Name != "bob" {
		t.Fatalf("leaderboards: %+v %+v", s.TopAuthors, s.AwaitingReview)
	}
	if s.ByAIStatus["none"] != 1 {
		t.Fatalf("by ai: %v", s.ByAIStatus)
	}
	// re-upserting keeps GitHub's creation time when an event omits it
	st.UpsertPR(ctx, model.PR{Repo: "o/a", Number: 1, Author: "alice", State: "open"})
	l, _ := st.ListPRs(ctx, PRFilter{Repo: "o/a", State: "open"})
	if l[0].GHCreatedAt != "2026-11-20T00:00:00Z" {
		t.Fatalf("created_at lost: %q", l[0].GHCreatedAt)
	}
}

func TestMigrationFilesAreOrdered(t *testing.T) {
	entries, _ := migrationsFS.ReadDir("migrations")
	prev := ""
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") || e.Name() <= prev {
			t.Fatalf("bad migration order/name: %s after %s", e.Name(), prev)
		}
		prev = e.Name()
	}
}
