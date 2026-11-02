package openmeteo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yanndethe/meteocomp/internal/config"
	"github.com/yanndethe/meteocomp/internal/weather"
)

const fixture = `{
  "hourly": {
    "time": ["2026-11-02T00:00", "2026-11-02T01:00"],
    "temperature_2m": [8.4, null],
    "relative_humidity_2m": [91, 92],
    "weather_code": [61, 3],
    "visibility": [12000, 8000],
    "wind_speed_10m": [14.2, 12.0]
  },
  "daily": {
    "time": ["2026-11-02"],
    "weather_code": [73],
    "temperature_2m_max": [11.2],
    "temperature_2m_min": [5.1],
    "snowfall_sum": [1.4]
  }
}`

func TestFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("latitude") != "48.85" || r.URL.Query().Get("forecast_days") != "2" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		w.Write([]byte(fixture))
	}))
	defer srv.Close()

	p, err := New(config.ProviderConfig{Name: "om", BaseURL: srv.URL, Options: map[string]any{"forecast_days": 2}}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Fetch(context.Background(), weather.Location{Name: "paris", Latitude: 48.85, Longitude: 2.35})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d forecasts, want 3", len(got))
	}

	h0 := got[0]
	if !h0.ForecastTime.Equal(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)) || h0.Granularity != weather.GranularityHourly {
		t.Errorf("h0 time/granularity: %+v", h0)
	}
	if *h0.TemperatureC != 8.4 || *h0.HumidityPct != 91 || *h0.VisibilityKm != 12 || h0.Condition != weather.ConditionRain || h0.ConditionCode != "61" {
		t.Errorf("h0 values: %+v", h0)
	}
	if got[1].TemperatureC != nil || got[1].Condition != weather.ConditionCloudy {
		t.Errorf("h1: %+v", got[1])
	}
	d := got[2]
	if d.Granularity != weather.GranularityDaily || *d.TemperatureMaxC != 11.2 || *d.SnowfallCm != 1.4 || d.Condition != weather.ConditionSnow {
		t.Errorf("daily: %+v", d)
	}
}
