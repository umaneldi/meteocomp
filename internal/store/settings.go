package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/yanndethe/meteocomp/internal/config"
	"github.com/yanndethe/meteocomp/internal/weather"
)

const keyPollInterval = "poll_interval"
const keyForecastDays = "forecast_days"

// LoadSettings reads the settings saved by SaveSettings. The presence of the
// poll_interval key marks the settings as initialized.
func (s *SQLDB) LoadSettings(ctx context.Context) (*config.Settings, bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, s.d.rebind(`SELECT value FROM settings WHERE key = ?`), keyPollInterval).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("load settings: %w", err)
	}
	out := &config.Settings{Locations: []weather.Location{}, Providers: []config.ProviderConfig{}}
	if out.PollInterval, err = time.ParseDuration(raw); err != nil {
		return nil, false, fmt.Errorf("load settings: poll_interval: %w", err)
	}
	var fdRaw string
	if err := s.db.QueryRowContext(ctx, s.d.rebind(`SELECT value FROM settings WHERE key = ?`), keyForecastDays).Scan(&fdRaw); err == nil {
		if _, err := fmt.Sscanf(fdRaw, "%d", &out.ForecastDays); err != nil {
			return nil, false, fmt.Errorf("load settings: forecast_days: %w", err)
		}
	}
	if out.ForecastDays == 0 {
		out.ForecastDays = config.DefaultSettings().ForecastDays
	}

	rows, err := s.db.QueryContext(ctx,
		s.d.rebind(`SELECT name, city, country, latitude, longitude, station_id FROM locations ORDER BY position, name`))
	if err != nil {
		return nil, false, fmt.Errorf("load locations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var l weather.Location
		if err := rows.Scan(&l.Name, &l.City, &l.Country, &l.Latitude, &l.Longitude, &l.StationID); err != nil {
			return nil, false, err
		}
		out.Locations = append(out.Locations, l)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	prows, err := s.db.QueryContext(ctx,
		s.d.rebind(`SELECT name, type, api_key, base_url, enabled, options FROM providers ORDER BY position, name`))
	if err != nil {
		return nil, false, fmt.Errorf("load providers: %w", err)
	}
	defer prows.Close()
	for prows.Next() {
		var p config.ProviderConfig
		var enabled bool
		var opts string
		if err := prows.Scan(&p.Name, &p.Type, &p.APIKey, &p.BaseURL, &enabled, &opts); err != nil {
			return nil, false, err
		}
		p.Enabled = new(enabled)
		if err := json.Unmarshal([]byte(opts), &p.Options); err != nil {
			return nil, false, fmt.Errorf("provider %q: options: %w", p.Name, err)
		}
		out.Providers = append(out.Providers, p)
	}
	return out, true, prows.Err()
}

// SaveSettings replaces all settings in one transaction.
func (s *SQLDB) SaveSettings(ctx context.Context, cfg config.Settings) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, s.d.rebind(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`), keyPollInterval, cfg.PollInterval.String()); err != nil {
		return fmt.Errorf("save poll_interval: %w", err)
	}
	if _, err := tx.ExecContext(ctx, s.d.rebind(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`), keyForecastDays, fmt.Sprintf("%d", cfg.ForecastDays)); err != nil {
		return fmt.Errorf("save forecast_days: %w", err)
	}
	if _, err := tx.ExecContext(ctx, s.d.rebind(`DELETE FROM locations`)); err != nil {
		return err
	}
	for i, l := range cfg.Locations {
		if _, err := tx.ExecContext(ctx, s.d.rebind(`INSERT INTO locations (name, position, city, country, latitude, longitude, station_id)
			VALUES (?, ?, ?, ?, ?, ?, ?)`), l.Name, i, l.City, l.Country, l.Latitude, l.Longitude, l.StationID); err != nil {
			return fmt.Errorf("save location %q: %w", l.Name, err)
		}
	}
	if _, err := tx.ExecContext(ctx, s.d.rebind(`DELETE FROM providers`)); err != nil {
		return err
	}
	for i, p := range cfg.Providers {
		opts := p.Options
		if opts == nil {
			opts = map[string]any{}
		}
		rawOpts, err := json.Marshal(opts)
		if err != nil {
			return fmt.Errorf("provider %q: options: %w", p.Name, err)
		}
		if _, err := tx.ExecContext(ctx, s.d.rebind(`INSERT INTO providers (name, position, type, api_key, base_url, enabled, options)
			VALUES (?, ?, ?, ?, ?, ?, ?)`), p.Name, i, p.Type, p.APIKey, p.BaseURL, p.IsEnabled(), string(rawOpts)); err != nil {
			return fmt.Errorf("save provider %q: %w", p.Name, err)
		}
	}
	return tx.Commit()
}

func (s *SQLDB) LastPollTimes(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, s.d.rebind(`SELECT provider, MAX(started_at) FROM polls GROUP BY provider`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var name, at string
		if err := rows.Scan(&name, &at); err != nil {
			return nil, err
		}
		if out[name], err = parseTime(at); err != nil {
			return nil, err
		}
	}
	return out, rows.Err()
}
