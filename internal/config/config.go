// Package config defines the service settings, loads the optional YAML seed file
// and validates settings.
//
// Runtime settings (poll interval, locations, providers and their API keys) are
// persisted in the database and edited from the web UI. The YAML file is optional
// and only used to seed an empty database (e.g. for automated tests).
package config

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/yanndethe/meteocomp/internal/weather"
)

// DefaultPollInterval is used when no interval is set.
const DefaultPollInterval = time.Hour

// MinPollInterval is the shortest accepted poll interval.
const MinPollInterval = time.Minute

// Config is the root of the optional YAML file.
type Config struct {
	HTTP     HTTPConfig `yaml:"http"`
	Settings `yaml:",inline"`
}

type HTTPConfig struct {
	Addr string `yaml:"addr"`
}

// Settings are the runtime settings persisted in the database.
type Settings struct {
	// PollInterval is shared by all providers.
	PollInterval time.Duration `yaml:"poll_interval"`
	// ForecastDays is the number of forecast days requested from every provider (1-16).
	ForecastDays int                `yaml:"forecast_days"`
	Locations    []weather.Location `yaml:"locations"`
	Providers    []ProviderConfig   `yaml:"providers"`
}

// ProviderConfig describes one provider instance. The same Type may be used by
// several instances (e.g. two API keys or different endpoints).
type ProviderConfig struct {
	Name    string         `yaml:"name"`
	Type    string         `yaml:"type"`
	APIKey  string         `yaml:"api_key"`
	BaseURL string         `yaml:"base_url"` // optional override, mainly for tests
	Enabled *bool          `yaml:"enabled"`  // defaults to true
	Options map[string]any `yaml:"options"`  // provider-specific options
}

// IsEnabled reports whether the provider should be polled.
func (p ProviderConfig) IsEnabled() bool { return p.Enabled == nil || *p.Enabled }

// IntOption returns an integer option or def if absent or not an integer.
// Whole floats are accepted because options stored as JSON decode to float64.
func (p ProviderConfig) IntOption(key string, def int) int {
	switch v := p.Options[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		if v == math.Trunc(v) {
			return int(v)
		}
	}
	return def
}

// StringOption returns a string option or def if absent, empty or not a string.
func (p ProviderConfig) StringOption(key, def string) string {
	if v, ok := p.Options[key].(string); ok && v != "" {
		return v
	}
	return def
}

// DefaultSettings are used when neither the database nor a seed file provide settings.
// Open-Meteo needs no key so it is enabled; keyed providers wait for an API key.
func DefaultSettings() Settings {
	return Settings{
		PollInterval: DefaultPollInterval,
		ForecastDays: 7,
		Locations:    []weather.Location{},
		Providers: []ProviderConfig{
			{Name: "open-meteo", Type: "openmeteo", Enabled: new(true), Options: map[string]any{}},
			{Name: "meteofrance", Type: "openmeteo", Enabled: new(false),
				Options: map[string]any{"endpoint": "meteofrance"}},
			{Name: "owm", Type: "openweathermap", Enabled: new(false)},
			{Name: "weatherapi", Type: "weatherapi", Enabled: new(false), Options: map[string]any{"days": 3}},
		},
	}
}

// Load reads the YAML file at path, expands ${ENV_VARS}, applies defaults and validates.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(raw)
}

// Parse parses YAML bytes, expands ${ENV_VARS} (and ${ENV_VARS:-default}),
// applies defaults and validates.
func Parse(raw []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal([]byte(ExpandEnv(string(raw))), &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// ExpandEnv replaces ${VAR} with the environment value and ${VAR:-def} with the
// value or def when VAR is unset or empty.
func ExpandEnv(s string) string {
	return envRef.ReplaceAllStringFunc(s, func(m string) string {
		g := envRef.FindStringSubmatch(m)
		if v := os.Getenv(g[1]); v != "" || g[2] == "" {
			return v
		}
		return g[3]
	})
}

// Normalize applies defaults in place.
func (s *Settings) Normalize() {
	if s.PollInterval == 0 {
		s.PollInterval = DefaultPollInterval
	}
	if s.ForecastDays == 0 {
		s.ForecastDays = 7
	}
	if s.Locations == nil {
		s.Locations = []weather.Location{}
	}
	if s.Providers == nil {
		s.Providers = []ProviderConfig{}
	}
	for i := range s.Locations {
		l := &s.Locations[i]
		l.Name, l.City, l.Country = strings.TrimSpace(l.Name), strings.TrimSpace(l.City), strings.TrimSpace(l.Country)
	}
	for i := range s.Providers {
		p := &s.Providers[i]
		p.Name, p.Type, p.APIKey = strings.TrimSpace(p.Name), strings.TrimSpace(p.Type), strings.TrimSpace(p.APIKey)
		if p.Enabled == nil {
			p.Enabled = new(true)
		}
	}
}

// MarshalYAML serializes settings to YAML. PollInterval is written as a human-readable
// duration string so the output can be re-imported by Parse.
func MarshalYAML(s Settings) ([]byte, error) {
	type export struct {
		PollInterval string             `yaml:"poll_interval"`
		ForecastDays int                `yaml:"forecast_days"`
		Locations    []weather.Location `yaml:"locations"`
		Providers    []ProviderConfig   `yaml:"providers"`
	}
	return yaml.Marshal(export{
		PollInterval: s.PollInterval.String(),
		ForecastDays: s.ForecastDays,
		Locations:    s.Locations,
		Providers:    s.Providers,
	})
}

// Validate checks the settings. Errors are prefixed with "config:".
func (s *Settings) Validate() error {
	if s.PollInterval < MinPollInterval {
		return fmt.Errorf("config: poll interval must be at least %s", MinPollInterval)
	}
	if s.ForecastDays < 1 || s.ForecastDays > 16 {
		return fmt.Errorf("config: forecast_days must be between 1 and 16")
	}
	seen := map[string]bool{}
	for i, l := range s.Locations {
		if l.Name == "" {
			return fmt.Errorf("config: locations[%d]: name is required", i)
		}
		if seen[l.Name] {
			return fmt.Errorf("config: duplicate location name %q", l.Name)
		}
		seen[l.Name] = true
		if l.Latitude < -90 || l.Latitude > 90 || l.Longitude < -180 || l.Longitude > 180 {
			return fmt.Errorf("config: location %q: invalid coordinates", l.Name)
		}
	}
	seen = map[string]bool{}
	for i, p := range s.Providers {
		if p.Name == "" {
			return fmt.Errorf("config: providers[%d]: name is required", i)
		}
		if p.Type == "" {
			return fmt.Errorf("config: provider %q: type is required", p.Name)
		}
		if seen[p.Name] {
			return fmt.Errorf("config: duplicate provider name %q", p.Name)
		}
		seen[p.Name] = true
	}
	return nil
}
