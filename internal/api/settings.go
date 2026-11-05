package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/yanndethe/meteocomp/internal/config"
	"github.com/yanndethe/meteocomp/internal/poller"
	"github.com/yanndethe/meteocomp/internal/providers"
	"github.com/yanndethe/meteocomp/internal/weather"
)

// InvalidSettingsError reports settings rejected by validation.
type InvalidSettingsError struct{ Err error }

func (e InvalidSettingsError) Error() string { return e.Err.Error() }
func (e InvalidSettingsError) Unwrap() error { return e.Err }

// Settings returns the settings currently in effect. Callers must not modify them.
func (s *Server) Settings() config.Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// ApplySettings validates set, optionally persists it, and restarts the poller with it.
//
// In strict mode an enabled provider that cannot be built (unknown type, missing
// API key, bad option) rejects the whole update; otherwise it is logged and skipped,
// so that a bad stored setting cannot prevent the service (and the UI) from starting.
func (s *Server) ApplySettings(ctx context.Context, set config.Settings, persist, strict bool) error {
	set.Normalize()
	if err := set.Validate(); err != nil {
		return InvalidSettingsError{err}
	}
	provs, err := providers.Build(set)
	if err != nil {
		if strict {
			return InvalidSettingsError{err}
		}
		s.log.Error("some providers could not be started; fix them in the settings UI", "error", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if persist {
		if err := s.store.SaveSettings(ctx, set); err != nil {
			return fmt.Errorf("save settings: %w", err)
		}
	}
	s.settings = set
	for _, p := range provs {
		s.log.Info("provider enabled", "provider", p.Name(), "type", p.Type())
	}
	if len(provs) == 0 {
		s.log.Warn("no enabled providers; configure them in the web UI")
	}
	if len(set.Locations) == 0 {
		s.log.Warn("no locations; configure them in the web UI")
	}
	s.pollers.Start(poller.New(provs, set.PollInterval, set.Locations, s.store, s.log))
	return nil
}

// settingsDoc is the JSON representation of the settings. API keys are write-only.
type settingsDoc struct {
	PollInterval string             `json:"poll_interval"`
	ForecastDays int                `json:"forecast_days"`
	Locations    []weather.Location `json:"locations"`
	Providers    []providerDoc      `json:"providers"`
}

type providerDoc struct {
	Name    string         `json:"name"`
	Type    string         `json:"type"`
	Enabled bool           `json:"enabled"`
	Options map[string]any `json:"options"`

	// Response only.
	HasAPIKey  bool   `json:"has_api_key"`            // a key is stored in the settings
	APIKeyHint string `json:"api_key_hint,omitempty"` // last characters of the stored key
	APIKeyEnv  string `json:"api_key_env,omitempty"`  // set when an environment variable provides the key (it wins)

	// Request only. APIKey nil keeps the stored key, "" removes it.
	// PreviousName identifies the stored provider when it is renamed.
	APIKey       *string `json:"api_key,omitempty"`
	PreviousName string  `json:"previous_name,omitempty"`
}

func keyHint(k string) string {
	if len(k) < 8 {
		return ""
	}
	return "…" + k[len(k)-4:]
}

func (s *Server) getSettings(w http.ResponseWriter, _ *http.Request) {
	cur := s.Settings()
	doc := settingsDoc{PollInterval: cur.PollInterval.String(), ForecastDays: cur.ForecastDays, Locations: cur.Locations, Providers: []providerDoc{}}
	for _, p := range cur.Providers {
		opts := p.Options
		if opts == nil {
			opts = map[string]any{}
		}
		d := providerDoc{
			Name: p.Name, Type: p.Type, Enabled: p.IsEnabled(), Options: opts,
			HasAPIKey: p.APIKey != "", APIKeyHint: keyHint(p.APIKey),
		}
		if name, _, ok := providers.EnvAPIKey(p.Type); ok {
			d.APIKeyEnv = name
		}
		doc.Providers = append(doc.Providers, d)
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var doc settingsDoc
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&doc); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	interval, err := time.ParseDuration(doc.PollInterval)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("poll_interval: expected a duration such as 30m or 1h, got %q", doc.PollInterval))
		return
	}

	cur := s.Settings()
	stored := make(map[string]config.ProviderConfig, len(cur.Providers))
	for _, p := range cur.Providers {
		stored[p.Name] = p
	}
	next := config.Settings{PollInterval: interval, ForecastDays: doc.ForecastDays, Locations: doc.Locations}
	for _, d := range doc.Providers {
		prevName := d.PreviousName
		if prevName == "" {
			prevName = d.Name
		}
		prev := stored[prevName]
		pc := config.ProviderConfig{
			Name: d.Name, Type: d.Type, Enabled: new(d.Enabled),
			APIKey: prev.APIKey, BaseURL: prev.BaseURL, Options: map[string]any{},
		}
		if prev.Type != d.Type {
			pc.APIKey, pc.BaseURL = "", "" // keys are specific to a provider type
		}
		if d.APIKey != nil {
			pc.APIKey = *d.APIKey
		}
		for k, v := range d.Options {
			if v != nil && v != "" {
				pc.Options[k] = v
			}
		}
		next.Providers = append(next.Providers, pc)
	}

	if err := s.ApplySettings(r.Context(), next, true, true); err != nil {
		if errors.As(err, new(InvalidSettingsError)) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		s.internalError(w, err)
		return
	}
	s.log.Info("settings updated", "poll_interval", interval, "locations", len(next.Locations), "providers", len(next.Providers))
	s.getSettings(w, r)
}

func (s *Server) listProviderTypes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, providers.Infos())
}
