package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigrateIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	for i := 0; i < 2; i++ { // reopening must not re-apply or fail
		st, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("run %d: migrations=%d err=%v", i, n, err)
		}
		st.Close()
	}
}

func TestListsAreNeverNil(t *testing.T) { // JSON must be [] not null, or the dashboard breaks
	st, err := Open(filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if prs, _ := st.ListPRs(ctx, "", 10); prs == nil {
		t.Fatal("ListPRs nil")
	}
	if runs, _ := st.ListRuns(ctx, 1); runs == nil {
		t.Fatal("ListRuns nil")
	}
	if us, _ := st.ListUsers(ctx); us == nil {
		t.Fatal("ListUsers nil")
	}
}
