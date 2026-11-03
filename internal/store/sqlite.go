package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/yanndethe/meteocomp/internal/weather"
)

//go:embed schema.sql
var schema string

const timeLayout = "2006-01-02T15:04:05Z"

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(s string) (time.Time, error) { return time.Parse(timeLayout, s) }

// SQLite is a Store backed by a SQLite database file.
type SQLite struct {
	db *sql.DB
}

var _ Store = (*SQLite)(nil)

// OpenSQLite opens (creating if needed) the database at path and applies the schema.
func OpenSQLite(ctx context.Context, path string) (*SQLite, error) {
	dsn := "file:" + path + "?" + url.Values{"_pragma": {
		"foreign_keys(1)", "journal_mode(WAL)", "busy_timeout(5000)",
	}}.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &SQLite{db: db}, nil
}

// migrate handles one-time schema evolution from older database layouts.
func migrate(ctx context.Context, db *sql.DB) error {
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
	// Skip if the locations table doesn't exist yet (fresh DB gets the column from schema.sql).
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

func (s *SQLite) Close() error { return s.db.Close() }

// readingColumns lists the non-id columns of the readings table, in insertion order.
const readingColumns = `poll_id, polled_at, provider, provider_type,
	location_name, city, country, latitude, longitude,
	forecast_time, granularity, lead_time_hours, kind,
	temperature_c, temperature_min_c, temperature_max_c, feels_like_c, humidity_pct, dew_point_c,
	pressure_hpa, wind_speed_kmh, wind_gust_kmh, wind_direction_deg, cloud_cover_pct,
	precipitation_mm, precipitation_probability_pct, snowfall_cm, uv_index, visibility_km,
	condition, condition_code, condition_text`

func (s *SQLite) RecordPoll(ctx context.Context, r PollResult) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	status, errText := StatusOK, sql.NullString{}
	if r.Err != nil {
		status, errText = StatusError, sql.NullString{String: r.Err.Error(), Valid: true}
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO polls
		(provider, provider_type, location_name, started_at, finished_at, duration_ms, status, error, reading_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Provider, r.ProviderType, r.Location.Name, formatTime(r.StartedAt), formatTime(r.FinishedAt),
		r.FinishedAt.Sub(r.StartedAt).Milliseconds(), status, errText, len(r.Forecasts))
	if err != nil {
		return 0, fmt.Errorf("insert poll: %w", err)
	}
	pollID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	if len(r.Forecasts) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?, ", 32), ", ")
		stmt, err := tx.PrepareContext(ctx, "INSERT INTO readings ("+readingColumns+") VALUES ("+placeholders+")")
		if err != nil {
			return 0, fmt.Errorf("prepare reading insert: %w", err)
		}
		defer stmt.Close()
		polledAt := formatTime(r.StartedAt)
		l := r.Location
		for _, f := range r.Forecasts {
			lead := f.ForecastTime.Sub(r.StartedAt).Hours()
			kind := string(f.Kind)
			if kind == "" {
				kind = string(weather.KindForecast)
			}
			if _, err := stmt.ExecContext(ctx,
				pollID, polledAt, r.Provider, r.ProviderType,
				l.Name, l.City, l.Country, l.Latitude, l.Longitude,
				formatTime(f.ForecastTime), string(f.Granularity), lead, kind,
				f.TemperatureC, f.TemperatureMinC, f.TemperatureMaxC, f.FeelsLikeC, f.HumidityPct, f.DewPointC,
				f.PressureHPa, f.WindSpeedKmh, f.WindGustKmh, f.WindDirectionDeg, f.CloudCoverPct,
				f.PrecipitationMm, f.PrecipitationProbabilityPct, f.SnowfallCm, f.UVIndex, f.VisibilityKm,
				string(f.Condition), f.ConditionCode, f.ConditionText,
			); err != nil {
				return 0, fmt.Errorf("insert reading: %w", err)
			}
		}
	}
	return pollID, tx.Commit()
}

// where accumulates SQL conditions and their arguments.
type where struct {
	conds []string
	args  []any
}

func (w *where) add(cond string, arg any) {
	w.conds = append(w.conds, cond)
	w.args = append(w.args, arg)
}

func (w *where) String() string {
	if len(w.conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.conds, " AND ")
}

func (s *SQLite) ListPolls(ctx context.Context, f PollFilter) ([]Poll, error) {
	var w where
	if f.Provider != "" {
		w.add("provider = ?", f.Provider)
	}
	if f.Location != "" {
		w.add("location_name = ?", f.Location)
	}
	if f.Status != "" {
		w.add("status = ?", f.Status)
	}
	q := `SELECT id, provider, provider_type, location_name, started_at, finished_at, duration_ms,
		status, COALESCE(error, ''), reading_count,
		(SELECT GROUP_CONCAT(DISTINCT kind) FROM readings WHERE poll_id = polls.id) AS kinds
		FROM polls` + w.String() + " ORDER BY started_at DESC, id DESC"
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, w.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Poll{}
	for rows.Next() {
		var p Poll
		var started, finished string
		var kinds sql.NullString
		if err := rows.Scan(&p.ID, &p.Provider, &p.ProviderType, &p.LocationName, &started, &finished,
			&p.DurationMs, &p.Status, &p.Error, &p.ReadingCount, &kinds); err != nil {
			return nil, err
		}
		if kinds.Valid && kinds.String != "" {
			p.Kinds = strings.Split(kinds.String, ",")
		}
		if p.StartedAt, err = parseTime(started); err != nil {
			return nil, err
		}
		if p.FinishedAt, err = parseTime(finished); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *SQLite) ListReadings(ctx context.Context, f ReadingFilter) ([]ReadingRecord, error) {
	var w where
	if f.Provider != "" {
		w.add("provider = ?", f.Provider)
	}
	if f.Location != "" {
		w.add("location_name = ?", f.Location)
	}
	if f.Granularity != "" {
		w.add("granularity = ?", f.Granularity)
	}
	if f.Kind != "" {
		w.add("kind = ?", f.Kind)
	}
	if !f.From.IsZero() {
		w.add("forecast_time >= ?", formatTime(f.From))
	}
	if !f.To.IsZero() {
		w.add("forecast_time < ?", formatTime(f.To))
	}
	if !f.PolledFrom.IsZero() {
		w.add("polled_at >= ?", formatTime(f.PolledFrom))
	}
	if !f.PolledTo.IsZero() {
		w.add("polled_at < ?", formatTime(f.PolledTo))
	}
	if f.PollID != 0 {
		w.add("poll_id = ?", f.PollID)
	}
	q := "SELECT id, " + readingColumns + " FROM readings" + w.String() +
		" ORDER BY polled_at DESC, provider, location_name, granularity, forecast_time"
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, w.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []ReadingRecord{}
	for rows.Next() {
		var r ReadingRecord
		var polledAt, forecastTime, granularity, kind, condition string
		var city, country, code, text sql.NullString
		if err := rows.Scan(&r.ID, &r.PollID, &polledAt, &r.Provider, &r.ProviderType,
			&r.Location.Name, &city, &country, &r.Location.Latitude, &r.Location.Longitude,
			&forecastTime, &granularity, &r.LeadTimeHours, &kind,
			&r.TemperatureC, &r.TemperatureMinC, &r.TemperatureMaxC, &r.FeelsLikeC, &r.HumidityPct, &r.DewPointC,
			&r.PressureHPa, &r.WindSpeedKmh, &r.WindGustKmh, &r.WindDirectionDeg, &r.CloudCoverPct,
			&r.PrecipitationMm, &r.PrecipitationProbabilityPct, &r.SnowfallCm, &r.UVIndex, &r.VisibilityKm,
			&condition, &code, &text,
		); err != nil {
			return nil, err
		}
		if r.PolledAt, err = parseTime(polledAt); err != nil {
			return nil, err
		}
		if r.ForecastTime, err = parseTime(forecastTime); err != nil {
			return nil, err
		}
		r.Location.City, r.Location.Country = city.String, country.String
		r.Granularity = weather.Granularity(granularity)
		r.Kind = weather.Kind(kind)
		r.Condition = weather.Condition(condition)
		r.ConditionCode, r.ConditionText = code.String, text.String
		out = append(out, r)
	}
	return out, rows.Err()
}
