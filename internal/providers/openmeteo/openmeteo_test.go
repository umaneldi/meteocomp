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

// pastTime is a timestamp clearly in the past; futureTime is clearly in the future.
// Using fixed dates relative to now ensures Kind classification is deterministic.
var (
	pastTime   = time.Now().Add(-48 * time.Hour).UTC()
	futureTime = time.Now().Add(48 * time.Hour).UTC()
)

func pastHour() string   { return pastTime.Format("2006-01-02T15:04") }
func futureHour() string { return futureTime.Format("2006-01-02T15:04") }
func futureDay() string  { return futureTime.Format("2006-01-02") }

func fixture() string {
	return `{
  "hourly": {
    "time": ["` + pastHour() + `", "` + futureHour() + `"],
    "temperature_2m": [8.4, 10.0],
    "relative_humidity_2m": [91, 80],
    "weather_code": [61, 3],
    "visibility": [12000, 8000],
    "wind_speed_10m": [14.2, 12.0]
  },
  "daily": {
    "time": ["` + futureDay() + `"],
    "weather_code": [73],
    "temperature_2m_max": [11.2],
    "temperature_2m_min": [5.1],
    "snowfall_sum": [1.4]
  }
}`
}

func TestFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/forecast" || r.URL.Query().Has("models") {
			t.Errorf("unexpected path or models: %s", r.URL)
		}
		if r.URL.Query().Get("latitude") != "48.85" || r.URL.Query().Get("forecast_days") != "2" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		w.Write([]byte(fixture()))
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
		t.Fatalf("got %d readings, want 3", len(got))
	}

	// Hourly entries come before daily in the response.
	hPast, hFuture, d := got[0], got[1], got[2]

	if hPast.Kind != weather.KindForecast {
		t.Errorf("past hourly: expected KindForecast, got %q", hPast.Kind)
	}
	if hFuture.Kind != weather.KindForecast {
		t.Errorf("future hourly: expected KindForecast, got %q", hFuture.Kind)
	}
	if d.Kind != weather.KindForecast {
		t.Errorf("future daily: expected KindForecast, got %q", d.Kind)
	}

	if hPast.Granularity != weather.GranularityHourly {
		t.Errorf("hPast granularity: %+v", hPast)
	}
	if *hPast.TemperatureC != 8.4 || *hPast.HumidityPct != 91 || *hPast.VisibilityKm != 12 ||
		hPast.Condition != weather.ConditionRain || hPast.ConditionCode != "61" {
		t.Errorf("hPast values: %+v", hPast)
	}
	if d.Granularity != weather.GranularityDaily || *d.TemperatureMaxC != 11.2 || *d.SnowfallCm != 1.4 || d.Condition != weather.ConditionSnow {
		t.Errorf("daily: %+v", d)
	}
}

// The meteofrance endpoint returns null rows past the model horizon; they must be dropped.
const meteofranceFixture = `{
  "hourly": {
    "time": ["2026-11-05T23:00", "2026-11-06T00:00"],
    "temperature_2m": [9.1, null],
    "weather_code": [3, null]
  },
  "daily": {
    "time": ["2026-11-05", "2026-11-06"],
    "weather_code": [3, null],
    "temperature_2m_max": [10.8, null]
  }
}`

func TestFetchEndpointAndModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/meteofrance" || r.URL.Query().Get("models") != "arome_france" {
			t.Errorf("unexpected request: %s", r.URL)
		}
		w.Write([]byte(meteofranceFixture))
	}))
	defer srv.Close()

	p, err := New(config.ProviderConfig{Name: "mf", BaseURL: srv.URL,
		Options: map[string]any{"endpoint": "meteofrance", "models": "arome_france"}}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Fetch(context.Background(), weather.Location{Name: "paris", Latitude: 48.85, Longitude: 2.35})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Granularity != weather.GranularityHourly || got[1].Granularity != weather.GranularityDaily {
		t.Fatalf("expected one hourly and one daily reading, got %+v", got)
	}
	// 2026-11-05 is in the future from today (2026-11-03), so both should be forecasts.
	for _, r := range got {
		if r.Kind != weather.KindForecast {
			t.Errorf("expected KindForecast for future row, got %q", r.Kind)
		}
	}
}

func TestInvalidEndpoint(t *testing.T) {
	for _, ep := range []string{"../forecast", "forecast?x=1", "Meteo France"} {
		if _, err := New(config.ProviderConfig{Name: "om", Options: map[string]any{"endpoint": ep}}, http.DefaultClient); err == nil {
			t.Errorf("endpoint %q: expected error", ep)
		}
	}
}

