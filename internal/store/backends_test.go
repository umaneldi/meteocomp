package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// forEachBackend runs fn against a fresh SQLite database and, when
// METEOCOMP_TEST_POSTGRES_URI is set (e.g. postgres://localhost/meteocomp_test),
// against a fresh schema of that PostgreSQL database.
func forEachBackend(t *testing.T, fn func(t *testing.T, s *SQLDB)) {
	t.Run("sqlite", func(t *testing.T) {
		s, err := Open(context.Background(), "sqlite://"+filepath.Join(t.TempDir(), "test.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		fn(t, s)
	})
	t.Run("postgres", func(t *testing.T) {
		uri := os.Getenv("METEOCOMP_TEST_POSTGRES_URI")
		if uri == "" {
			t.Skip("METEOCOMP_TEST_POSTGRES_URI not set")
		}
		fn(t, openPostgresTestSchema(t, uri))
	})
}

// openPostgresTestSchema creates a throwaway schema, dropped at cleanup, and
// opens a store whose search_path points at it.
func openPostgresTestSchema(t *testing.T, uri string) *SQLDB {
	ctx := context.Background()
	schema := "test_" + strings.ToLower(rand.Text()[:12])
	admin, err := sql.Open("pgx", uri)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	if _, err := admin.ExecContext(ctx, fmt.Sprintf(`CREATE SCHEMA "%s"`, schema)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.ExecContext(ctx, fmt.Sprintf(`DROP SCHEMA "%s" CASCADE`, schema)) })

	u, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	s, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
