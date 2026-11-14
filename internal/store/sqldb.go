package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yanndethe/meteocomp/internal/weather"
)

const timeLayout = "2006-01-02T15:04:05Z"

func formatTime(t time.Time) string         { return t.UTC().Format(timeLayout) }
func parseTime(s string) (time.Time, error) { return time.Parse(timeLayout, s) }

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

type SQLDB struct {
	db *sql.DB
	d  dialect
}

var _ Store = (*SQLDB)(nil)

func (s *SQLDB) Close() error { return s.db.Close() }

const readingColumns = `provider, provider_type, location_name,
	forecast_time, lead_time_hours, kind,
	temperature_c, feels_like_c, humidity_pct, dew_point_c,
	pressure_hpa, wind_speed_kmh, wind_gust_kmh, wind_direction_deg, cloud_cover_pct,
	precipitation_probability_pct, snowfall_cm, visibility_km,
	condition, condition_text, polled_at`

func (s *SQLDB) RecordReadings(ctx context.Context, readings []ReadingRecord) error {
	if len(readings) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(strings.Split(readingColumns, ","))), ", ")
	stmt, err := tx.PrepareContext(ctx, s.d.rebind("INSERT INTO readings ("+readingColumns+") VALUES ("+placeholders+")"))
	if err != nil {
		return fmt.Errorf("prepare reading insert: %w", err)
	}
	defer stmt.Close()
	for _, r := range readings {
		if r.Provider == "" || r.ProviderType == "" || r.LocationName == "" ||
			r.PolledAt.IsZero() || r.ForecastTime.IsZero() {
			return fmt.Errorf("reading requires provider, provider_type, location_name, polled_at and forecast_time")
		}
		if r.Kind != weather.KindForecast && r.Kind != weather.KindObservation {
			return fmt.Errorf("invalid reading kind %q", r.Kind)
		}
		// Compute from the timestamps actually stored, so persisted lead times agree exactly.
		polledAt := r.PolledAt.UTC().Truncate(time.Second)
		forecastTime := r.ForecastTime.UTC().Truncate(time.Second)
		if _, err := stmt.ExecContext(ctx,
			r.Provider, r.ProviderType, r.LocationName,
			formatTime(forecastTime), forecastTime.Sub(polledAt).Hours(), string(r.Kind),
			r.TemperatureC, r.FeelsLikeC, r.HumidityPct, r.DewPointC,
			r.PressureHPa, r.WindSpeedKmh, r.WindGustKmh, r.WindDirectionDeg, r.CloudCoverPct,
			r.PrecipitationProbabilityPct, r.SnowfallCm, r.VisibilityKm,
			string(r.Condition), r.ConditionText, formatTime(polledAt),
		); err != nil {
			return fmt.Errorf("insert reading: %w", err)
		}
	}
	return tx.Commit()
}

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

func (s *SQLDB) LatestObservationTimes(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT location_name, MAX(forecast_time) FROM readings WHERE kind = 'observation' GROUP BY location_name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]time.Time)
	for rows.Next() {
		var loc, ts string
		if err := rows.Scan(&loc, &ts); err != nil {
			return nil, err
		}
		t, err := parseTime(ts)
		if err != nil {
			return nil, err
		}
		out[loc] = t
	}
	return out, rows.Err()
}

func (s *SQLDB) LastReadingTimes(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT provider, MAX(polled_at) FROM readings GROUP BY provider")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]time.Time)
	for rows.Next() {
		var provider, timestamp string
		if err := rows.Scan(&provider, &timestamp); err != nil {
			return nil, err
		}
		t, err := parseTime(timestamp)
		if err != nil {
			return nil, err
		}
		out[provider] = t
	}
	return out, rows.Err()
}

func (s *SQLDB) ListReadings(ctx context.Context, f ReadingFilter) ([]ReadingRecord, error) {
	if f.Offset < 0 || f.Limit < 0 || (f.Offset > 0 && f.Limit == 0) {
		return nil, fmt.Errorf("readings require a non-negative limit and offset; offset requires a limit")
	}
	var w where
	if f.Provider != "" {
		w.add("provider = ?", f.Provider)
	}
	if f.Location != "" {
		w.add("location_name = ?", f.Location)
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
	// Include all fields in the ordering: rows tied on these values are identical.
	q := "SELECT " + readingColumns + " FROM readings" + w.String() +
		" ORDER BY polled_at DESC, " + strings.TrimSuffix(readingColumns, ", polled_at")
	// When ForecastTime is set we fetch all matching rows and filter in Go, so LIMIT/OFFSET
	// must not be applied to the SQL query.
	if f.Limit > 0 && f.ForecastTime.IsZero() {
		q += fmt.Sprintf(" LIMIT %d OFFSET %d", f.Limit, f.Offset)
	}
	rows, err := s.db.QueryContext(ctx, s.d.rebind(q), w.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all := []ReadingRecord{}
	for rows.Next() {
		var r ReadingRecord
		var polledAt, forecastTime string
		if err := rows.Scan(&r.Provider, &r.ProviderType, &r.LocationName,
			&forecastTime, &r.LeadTimeHours, &r.Kind,
			&r.TemperatureC, &r.FeelsLikeC, &r.HumidityPct, &r.DewPointC,
			&r.PressureHPa, &r.WindSpeedKmh, &r.WindGustKmh, &r.WindDirectionDeg, &r.CloudCoverPct,
			&r.PrecipitationProbabilityPct, &r.SnowfallCm, &r.VisibilityKm,
			&r.Condition, &r.ConditionText, &polledAt,
		); err != nil {
			return nil, err
		}
		if r.PolledAt, err = parseTime(polledAt); err != nil {
			return nil, err
		}
		if r.ForecastTime, err = parseTime(forecastTime); err != nil {
			return nil, err
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if f.ForecastTime.IsZero() {
		return all, nil
	}
	// Find the globally closest forecast_time to the target.
	// All returned rows must share that single forecast_time value.
	// On exact ties (equidistant before and after), prefer the earlier timestamp.
	target := f.ForecastTime.UTC()
	var globalMinDiff time.Duration = -1
	for _, r := range all {
		if d := absDuration(r.ForecastTime.UTC().Sub(target)); globalMinDiff < 0 || d < globalMinDiff {
			globalMinDiff = d
		}
	}
	var closestFT time.Time
	for _, r := range all {
		if absDuration(r.ForecastTime.UTC().Sub(target)) != globalMinDiff {
			continue
		}
		if ft := r.ForecastTime.UTC(); closestFT.IsZero() || ft.Before(closestFT) {
			closestFT = ft
		}
	}
	out := all[:0]
	for _, r := range all {
		if r.ForecastTime.UTC().Equal(closestFT) {
			out = append(out, r)
		}
	}
	if f.Limit > 0 {
		start := f.Offset
		if start > len(out) {
			start = len(out)
		}
		end := start + f.Limit
		if end > len(out) {
			end = len(out)
		}
		out = out[start:end]
	}
	return out, nil
}

type sourceCondition struct {
	at   time.Time
	cond string
	text string
}

func (s *SQLDB) FillObservationConditions(ctx context.Context, location string,
	sources []weather.Forecast, maxGap time.Duration) (int64, error) {
	var known []sourceCondition
	for _, f := range sources {
		if f.Condition != weather.ConditionUnknown && f.Condition != "" {
			known = append(known, sourceCondition{at: f.ForecastTime.UTC(), cond: string(f.Condition), text: f.ConditionText})
		}
	}
	if len(known) == 0 {
		return 0, nil
	}
	slices.SortFunc(known, func(a, b sourceCondition) int { return a.at.Compare(b.at) })
	from, to := known[0].at.Add(-maxGap), known[len(known)-1].at.Add(maxGap)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, s.d.rebind(`SELECT DISTINCT forecast_time FROM readings
		WHERE kind = 'observation' AND location_name = ? AND condition IN ('unknown', '')
		AND forecast_time >= ? AND forecast_time <= ?`),
		location, formatTime(from), formatTime(to))
	if err != nil {
		return 0, err
	}
	var targets []string
	for rows.Next() {
		var ts string
		if err := rows.Scan(&ts); err != nil {
			rows.Close()
			return 0, err
		}
		targets = append(targets, ts)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	rows.Close()

	stmt, err := tx.PrepareContext(ctx, s.d.rebind(`UPDATE readings SET condition = ?, condition_text = ?
		WHERE kind = 'observation' AND location_name = ? AND forecast_time = ? AND condition IN ('unknown', '')`))
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var updated int64
	for _, ts := range targets {
		at, err := parseTime(ts)
		if err != nil {
			return 0, err
		}
		src, ok := nearestSource(known, at, maxGap)
		if !ok {
			continue
		}
		res, err := stmt.ExecContext(ctx, src.cond, src.text+" (SYNOP)", location, ts)
		if err != nil {
			return 0, fmt.Errorf("update condition: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		updated += n
	}
	return updated, tx.Commit()
}

// nearestSource returns the source closest to at, if at most maxGap away. sources is sorted by time.
func nearestSource(sources []sourceCondition, at time.Time, maxGap time.Duration) (sourceCondition, bool) {
	i, _ := slices.BinarySearchFunc(sources, at, func(s sourceCondition, t time.Time) int { return s.at.Compare(t) })
	best, bestGap := sourceCondition{}, maxGap+1
	for _, j := range []int{i - 1, i} {
		if j >= 0 && j < len(sources) {
			if gap := absDuration(sources[j].at.Sub(at)); gap < bestGap {
				best, bestGap = sources[j], gap
			}
		}
	}
	return best, bestGap <= maxGap
}
