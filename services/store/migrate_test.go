package store

import (
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
