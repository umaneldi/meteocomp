package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

//go:embed schema_sqlite.sql
var sqliteSchema string

//go:embed schema_postgres.sql
var postgresSchema string

// dialect captures the SQL differences between the supported databases.
type dialect struct {
	name string
	// numbered placeholders ($1, $2, ...) instead of '?'.
	numbered bool
}

var (
	dialectSQLite   = dialect{name: "sqlite"}
	dialectPostgres = dialect{
		name:     "postgres",
		numbered: true,
	}
)

// rebind converts '?' placeholders to the dialect's syntax. Queries in this
// package never contain a literal '?' outside placeholders.
func (d dialect) rebind(q string) string {
	if !d.numbered {
		return q
	}
	var b strings.Builder
	n := 0
	for _, r := range q {
		if r == '?' {
			n++
			b.WriteString("$" + strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Open opens the database designated by uri and applies the schema. Supported forms:
//
//	postgres://user:pass@host:5432/dbname?sslmode=disable  (or postgresql://)
//	sqlite:///absolute/path.db, sqlite://relative/path.db, sqlite:path.db
//	path.db                                                (bare path: SQLite)
func Open(ctx context.Context, uri string) (*SQLDB, error) {
	scheme, rest, ok := strings.Cut(uri, ":")
	if !ok || strings.ContainsAny(scheme, `/\.`) || len(scheme) < 2 {
		// No scheme (a single letter is a Windows drive): plain SQLite path.
		return OpenSQLite(ctx, uri)
	}
	switch strings.ToLower(scheme) {
	case "postgres", "postgresql":
		return OpenPostgres(ctx, uri)
	case "sqlite", "sqlite3", "file":
		path := strings.TrimPrefix(rest, "//")
		if path == "" {
			return nil, fmt.Errorf("database URI %q: missing SQLite file path", uri)
		}
		return OpenSQLite(ctx, path)
	default:
		return nil, fmt.Errorf("database URI: unsupported scheme %q (use postgres:// or sqlite://)", scheme)
	}
}

// OpenSQLite opens (creating if needed) the SQLite database at path and applies the schema.
func OpenSQLite(ctx context.Context, path string) (*SQLDB, error) {
	dsn := "file:" + path + "?" + url.Values{"_pragma": {
		"foreign_keys(1)", "journal_mode(WAL)", "busy_timeout(5000)",
	}}.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database %q (check that its directory exists and is writable by uid %d): %w",
			path, os.Getuid(), err)
	}
	if err := migrateSQLite(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return initDB(ctx, db, dialectSQLite, sqliteSchema)
}

// OpenPostgres connects to the PostgreSQL database designated by dsn (URL or
// key=value form, see pgx) and applies the schema.
func OpenPostgres(ctx context.Context, dsn string) (*SQLDB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	return initDB(ctx, db, dialectPostgres, postgresSchema)
}

func initDB(ctx context.Context, db *sql.DB, d dialect, schema string) (*SQLDB, error) {
	if err := applyReadingSchema(ctx, db, d, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply %s schema: %w", d.name, err)
	}
	return &SQLDB{db: db, d: d}, nil
}

// applyReadingSchema removes legacy poll metadata atomically while retaining
// sub-daily readings. Daily aggregates cannot be distinguished in the new model.
func applyReadingSchema(ctx context.Context, db *sql.DB, d dialect, schema string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var legacy int
	query := `SELECT COUNT(*) FROM pragma_table_info('readings') WHERE name = 'granularity'`
	if d.name == "postgres" {
		query = `SELECT COUNT(*) FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'readings' AND column_name = 'granularity'`
	}
	if err := tx.QueryRowContext(ctx, query).Scan(&legacy); err != nil {
		return err
	}
	if legacy > 0 {
		start := strings.Index(schema, "CREATE TABLE IF NOT EXISTS readings (")
		end := strings.Index(schema[start:], ");") + start + 2
		create := strings.Replace(schema[start:end], "CREATE TABLE IF NOT EXISTS readings", "CREATE TABLE readings_new", 1)
		if _, err := tx.ExecContext(ctx, create); err != nil {
			return fmt.Errorf("create simplified readings: %w", err)
		}
		columns := strings.ReplaceAll(readingColumns, "condition_text", "COALESCE(condition_text, '')")
		if _, err := tx.ExecContext(ctx, "INSERT INTO readings_new ("+readingColumns+") SELECT "+
			columns+" FROM readings WHERE granularity <> 'daily'"); err != nil {
			return fmt.Errorf("copy readings: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "DROP TABLE readings; ALTER TABLE readings_new RENAME TO readings"); err != nil {
			return fmt.Errorf("replace readings: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS polls"); err != nil {
		return fmt.Errorf("remove legacy polls: %w", err)
	}
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateSQLite handles historical SQLite-only changes before the shared migration.
func migrateSQLite(ctx context.Context, db *sql.DB) error {
	// Migration 1: rename forecasts table and add kind column.
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='forecasts'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		if _, err := db.ExecContext(ctx, `ALTER TABLE forecasts RENAME TO readings`); err != nil {
			return fmt.Errorf("rename forecasts: %w", err)
		}
		if _, err := db.ExecContext(ctx,
			`ALTER TABLE readings ADD COLUMN kind TEXT NOT NULL DEFAULT 'forecast' CHECK (kind IN ('forecast', 'observation'))`); err != nil {
			return fmt.Errorf("add kind column: %w", err)
		}
		if _, err := db.ExecContext(ctx, `ALTER TABLE polls RENAME COLUMN forecast_count TO reading_count`); err != nil {
			return fmt.Errorf("rename forecast_count: %w", err)
		}
	}

	// Migration 2: add station_id to existing locations tables.
	// Skip if the locations table doesn't exist yet (fresh DB gets the column from the schema).
	var tableExists int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='locations'`).Scan(&tableExists); err != nil {
		return err
	}
	if tableExists > 0 {
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pragma_table_info('locations') WHERE name='station_id'`).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.ExecContext(ctx,
				`ALTER TABLE locations ADD COLUMN station_id TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("add station_id column: %w", err)
			}
		}
	}
	return nil
}
