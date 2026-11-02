// Package config loads and validates the service configuration.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/yanndethe/meteocomp/internal/weather"
)

// Config is the root configuration.
type Config struct {
	HTTP            HTTPConfig         `yaml:"http"`
	Database        DatabaseConfig     `yaml:"database"`
	DefaultInterval time.Duration      `yaml:"default_interval"`
	Locations       []weather.Location `yaml:"locations"`
	Providers       []ProviderConfig   `yaml:"providers"`
}

type HTTPConfig struct {
	Addr string `yaml:"addr"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

// ProviderConfig describes one provider instance. The same Type may be used by
// several instances (e.g. two API keys or different intervals).
type ProviderConfig struct {
	Name     string         `yaml:"name"`
	Type     string         `yaml:"type"`
	APIKey   string         `yaml:"api_key"`
	BaseURL  string         `yaml:"base_url"` // optional override, mainly for tests
	Interval time.Duration  `yaml:"interval"` // falls back to Config.DefaultInterval
	Enabled  *bool          `yaml:"enabled"`  // defaults to true
	Options  map[string]any `yaml:"options"`  // provider-specific options
}

// IsEnabled reports whether the provider should be polled.
func (p ProviderConfig) IsEnabled() bool { return p.Enabled == nil || *p.Enabled }

// IntOption returns an integer option or def if absent or not an integer.
func (p ProviderConfig) IntOption(key string, def int) int {
	if v, ok := p.Options[key].(int); ok {
		return v
	}
	return def
}

// Load reads the YAML file at path, expands ${ENV_VARS}, applies defaults and validates.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(raw)
}

// Parse parses YAML bytes, expands ${ENV_VARS}, applies defaults and validates.
func Parse(raw []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal([]byte(os.ExpandEnv(string(raw))), &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.HTTP.Addr == "" {
		c.HTTP.Addr = ":8080"
	}
	if c.Database.Path == "" {
		c.Database.Path = "meteocomp.db"
	}
	if c.DefaultInterval == 0 {
		c.DefaultInterval = time.Hour
	}
	for i := range c.Providers {
		if c.Providers[i].Interval == 0 {
			c.Providers[i].Interval = c.DefaultInterval
		}
	}
}

func (c *Config) validate() error {
	if len(c.Locations) == 0 {
		return fmt.Errorf("config: at least one location is required")
	}
	seen := map[string]bool{}
	for i, l := range c.Locations {
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
	for i, p := range c.Providers {
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
		if p.Interval < time.Minute {
			return fmt.Errorf("config: provider %q: interval must be at least 1m", p.Name)
		}
	}
	return nil
}
