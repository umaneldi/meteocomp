package config

import (
	"strings"
	"testing"
	"time"
)

func TestParseDefaultsAndEnv(t *testing.T) {
	t.Setenv("TEST_KEY", "secret")
	cfg, err := Parse([]byte(`
poll_interval: 30m
locations:
  - { name: paris, city: Paris, country: FR, latitude: 48.85, longitude: 2.35 }
providers:
  - { name: a, type: openmeteo, options: { forecast_days: 3, endpoint: "${UNSET_TEST_VAR:-meteofrance}" } }
  - { name: b, type: weatherapi, api_key: "${TEST_KEY}", enabled: false }
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PollInterval != 30*time.Minute {
		t.Errorf("poll interval: %v", cfg.PollInterval)
	}
	a, b := cfg.Providers[0], cfg.Providers[1]
	if !a.IsEnabled() || a.IntOption("forecast_days", 7) != 3 || a.StringOption("endpoint", "") != "meteofrance" {
		t.Errorf("provider a: %+v", a)
	}
	if b.APIKey != "secret" || b.IsEnabled() {
		t.Errorf("provider b: %+v", b)
	}
}

func TestParseValidation(t *testing.T) {
	cases := map[string]string{
		"duplicate provider": "locations: [{name: x}]\nproviders: [{name: a, type: t}, {name: a, type: t}]",
		"bad coordinates":    `locations: [{name: x, latitude: 100}]`,
		"missing type":       "locations: [{name: x}]\nproviders: [{name: a}]",
		"interval too short": "poll_interval: 10s\nlocations: [{name: x}]",
	}
	for name, in := range cases {
		if _, err := Parse([]byte(in)); err == nil || !strings.Contains(err.Error(), "config") {
			t.Errorf("%s: expected config error, got %v", name, err)
		}
	}
}

func TestParseDefaultInterval(t *testing.T) {
	cfg, err := Parse([]byte(`providers: [{name: a, type: openmeteo}]`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PollInterval != DefaultPollInterval || len(cfg.Locations) != 0 {
		t.Errorf("got %+v", cfg.Settings)
	}
}

func TestIntOptionAcceptsJSONNumbers(t *testing.T) {
	p := ProviderConfig{Options: map[string]any{"a": float64(4), "b": 2.5, "c": "x"}}
	if p.IntOption("a", 0) != 4 || p.IntOption("b", 9) != 9 || p.IntOption("c", 9) != 9 {
		t.Errorf("IntOption: %+v", p.Options)
	}
}

func TestDefaultSettingsAreValid(t *testing.T) {
	s := DefaultSettings()
	s.Normalize()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

// The env-driven seed file shipped in the Docker image must parse both with no
// environment and with every variable set.
func TestDockerSeedConfig(t *testing.T) {
	cfg, err := Load("../../docker/config.yaml")
	if err != nil {
		t.Fatalf("no env: %v", err)
	}
	if cfg.PollInterval != time.Hour || len(cfg.Locations) != 0 || len(cfg.Providers) != 4 || !cfg.Providers[0].IsEnabled() {
		t.Errorf("no env: %+v", cfg.Settings)
	}

	t.Setenv("METEOCOMP_POLL_INTERVAL", "20m")
	t.Setenv("METEOCOMP_LOCATIONS", "[{name: lyon, latitude: 45.76, longitude: 4.83}]")
	t.Setenv("OWM_ENABLED", "true")
	t.Setenv("OWM_API_KEY", "k")
	t.Setenv("WEATHERAPI_DAYS", "2")
	if cfg, err = Load("../../docker/config.yaml"); err != nil {
		t.Fatalf("with env: %v", err)
	}
	owm, wa := cfg.Providers[2], cfg.Providers[3]
	if cfg.PollInterval != 20*time.Minute || cfg.Locations[0].Name != "lyon" || !owm.IsEnabled() || owm.APIKey != "k" ||
		wa.IntOption("days", 0) != 2 {
		t.Errorf("with env: %+v", cfg.Settings)
	}
}
