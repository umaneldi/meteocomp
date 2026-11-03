package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenURIForms(t *testing.T) {
	dir := t.TempDir()
	for _, uri := range []string{
		filepath.Join(dir, "bare.db"),
		"sqlite://" + filepath.Join(dir, "abs.db"),
		"sqlite:" + filepath.Join(dir, "opaque.db"),
	} {
		s, err := Open(context.Background(), uri)
		if err != nil {
			t.Fatalf("%s: %v", uri, err)
		}
		s.Close()
	}
	for _, name := range []string{"bare.db", "abs.db", "opaque.db"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s not created: %v", name, err)
		}
	}
	if _, err := Open(context.Background(), "mysql://localhost/x"); err == nil {
		t.Error("expected an error for an unsupported scheme")
	}
}

func TestRebind(t *testing.T) {
	q := "SELECT a FROM t WHERE b = ? AND c IN (?, ?)"
	if got := dialectSQLite.rebind(q); got != q {
		t.Errorf("sqlite: %q", got)
	}
	if got, want := dialectPostgres.rebind(q), "SELECT a FROM t WHERE b = $1 AND c IN ($2, $3)"; got != want {
		t.Errorf("postgres: got %q want %q", got, want)
	}
}
