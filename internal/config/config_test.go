package config

import (
	"strings"
	"testing"
	"time"
)

func TestParseDefaultsAndEnv(t *testing.T) {
	t.Setenv("TEST_KEY", "secret")
	cfg, err := Parse([]byte(`
default_interval: 30m
locations:
  - { name: paris, city: Paris, country: FR, latitude: 48.85, longitude: 2.35 }
providers:
  - { name: a, type: openmeteo, options: { forecast_days: 3 } }
  - { name: b, type: weatherapi, api_key: "${TEST_KEY}", interval: 2h, enabled: false }
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Addr != ":8080" || cfg.Database.Path != "meteocomp.db" {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	a, b := cfg.Providers[0], cfg.Providers[1]
	if a.Interval != 30*time.Minute || !a.IsEnabled() || a.IntOption("forecast_days", 7) != 3 {
		t.Errorf("provider a: %+v", a)
	}
	if b.APIKey != "secret" || b.Interval != 2*time.Hour || b.IsEnabled() {
		t.Errorf("provider b: %+v", b)
	}
}

func TestParseValidation(t *testing.T) {
	cases := map[string]string{
		"no locations":       `providers: [{name: a, type: openmeteo}]`,
		"duplicate provider": "locations: [{name: x}]\nproviders: [{name: a, type: t}, {name: a, type: t}]",
		"bad coordinates":    `locations: [{name: x, latitude: 100}]`,
		"missing type":       "locations: [{name: x}]\nproviders: [{name: a}]",
		"interval too short": "locations: [{name: x}]\nproviders: [{name: a, type: t, interval: 10s}]",
	}
	for name, in := range cases {
		if _, err := Parse([]byte(in)); err == nil || !strings.Contains(err.Error(), "config") {
			t.Errorf("%s: expected config error, got %v", name, err)
		}
	}
}
