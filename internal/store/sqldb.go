package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/yanndethe/meteocomp/internal/weather"
)

const timeLayout = "2006-01-02T15:04:05Z"

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(s string) (time.Time, error) { return time.Parse(timeLayout, s) }

// SQLDB is a Store backed by a SQL database (SQLite or PostgreSQL).
type SQLDB struct {
	db *sql.DB
	d  dialect
}

var _ Store = (*SQLDB)(nil)

func (s *SQLDB) Close() error { return s.db.Close() }

// readingColumns lists the non-id columns of the readings table, in insertion order.
const readingColumns = `poll_id, polled_at, provider, provider_type,
	location_name, city, country, latitude, longitude,
	forecast_time, granularity, lead_time_hours, kind,
	temperature_c, temperature_min_c, temperature_max_c, feels_like_c, humidity_pct, dew_point_c,
	pressure_hpa, wind_speed_kmh, wind_gust_kmh, wind_direction_deg, cloud_cover_pct,
	precipitation_mm, precipitation_probability_pct, snowfall_cm, uv_index, visibility_km,
	condition, condition_code, condition_text`

func (s *SQLDB) RecordPoll(ctx context.Context, r PollResult) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	status, errText := StatusOK, sql.NullString{}
	if r.Err != nil {
		status, errText = StatusError, sql.NullString{String: r.Err.Error(), Valid: true}
	}
	var pollID int64
	if err := tx.QueryRowContext(ctx, s.d.rebind(`INSERT INTO polls
		(provider, provider_type, location_name, started_at, finished_at, duration_ms, status, error, reading_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`),
		r.Provider, r.ProviderType, r.Location.Name, formatTime(r.StartedAt), formatTime(r.FinishedAt),
		r.FinishedAt.Sub(r.StartedAt).Milliseconds(), status, errText, len(r.Forecasts)).Scan(&pollID); err != nil {
		return 0, fmt.Errorf("insert poll: %w", err)
	}

	if len(r.Forecasts) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?, ", 32), ", ")
		stmt, err := tx.PrepareContext(ctx, s.d.rebind("INSERT INTO readings ("+readingColumns+") VALUES ("+placeholders+")"))
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

func (s *SQLDB) ListPolls(ctx context.Context, f PollFilter) ([]Poll, error) {
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
		(SELECT ` + s.d.distinctKinds + ` FROM readings WHERE poll_id = polls.id) AS kinds
		FROM polls` + w.String() + " ORDER BY started_at DESC, id DESC"
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, s.d.rebind(q), w.args...)
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

func (s *SQLDB) ListReadings(ctx context.Context, f ReadingFilter) ([]ReadingRecord, error) {
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
	rows, err := s.db.QueryContext(ctx, s.d.rebind(q), w.args...)
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
