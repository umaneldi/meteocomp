package openmeteo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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
    "wind_speed_10m": [14.2, 12.0],
    "wind_gusts_10m": [25, 20],
    "surface_pressure": [980, 981],
    "pressure_msl": [1010, 1011],
    "snowfall": [0, 1.4]
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
		q := r.URL.Query()
		if q.Has("daily") || q.Get("wind_speed_unit") != "kmh" || q.Get("timezone") != "UTC" ||
			q.Get("hourly") != hourlyVars || !strings.Contains(q.Get("hourly"), "surface_pressure") {
			t.Errorf("unexpected variables or units: %s", r.URL.RawQuery)
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
	if len(got) != 2 {
		t.Fatalf("got %d readings, want 2 hourly readings", len(got))
	}

	hPast, hFuture := got[0], got[1]

	if hPast.Kind != weather.KindForecast {
		t.Errorf("past hourly: expected KindForecast, got %q", hPast.Kind)
	}
	if hFuture.Kind != weather.KindForecast {
		t.Errorf("future hourly: expected KindForecast, got %q", hFuture.Kind)
	}
	if *hPast.TemperatureC != 8.4 || *hPast.HumidityPct != 91 || *hPast.VisibilityKm != 12 ||
		*hPast.PressureHPa != 980 || *hPast.WindSpeedKmh != 14.2 || *hPast.WindGustKmh != 25 ||
		hPast.Condition != weather.ConditionRain || hPast.ConditionText != "Rain" {
		t.Errorf("hPast values: %+v", hPast)
	}
	if hPast.SnowfallCm == nil || *hPast.SnowfallCm != 0 || hPast.DewPointC != nil ||
		hFuture.SnowfallCm == nil || *hFuture.SnowfallCm != 1.4 {
		t.Errorf("optional measurements: %+v, %+v", hPast, hFuture)
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
	if len(got) != 1 {
		t.Fatalf("expected one nonempty hourly reading, got %+v", got)
	}
	for _, r := range got {
		if r.Kind != weather.KindForecast {
			t.Errorf("expected KindForecast for future row, got %q", r.Kind)
		}
	}
}

func TestFetchZeroAndMissingMeasurements(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"hourly":{
			"time":["2026-11-05T00:00","2026-11-05T01:00","2026-11-05T02:00","2026-11-05T03:00"],
			"temperature_2m":[0,null,null,null],
			"weather_code":[null,0,999,null],
			"visibility":[0,null],
			"wind_speed_10m":[0,null],
			"precipitation_probability":[0,null]
		}}`))
	}))
	defer srv.Close()
	p, err := New(config.ProviderConfig{Name: "om", BaseURL: srv.URL}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Fetch(context.Background(), weather.Location{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d readings, want 3", len(got))
	}
	f := got[0]
	for _, v := range []*float64{f.TemperatureC, f.VisibilityKm, f.WindSpeedKmh, f.PrecipitationProbabilityPct} {
		if v == nil || *v != 0 {
			t.Errorf("zero measurement lost: %v", v)
		}
	}
	if f.FeelsLikeC != nil || f.PressureHPa != nil || f.Condition != weather.ConditionUnknown ||
		got[1].Condition != weather.ConditionClear || got[1].TemperatureC != nil ||
		got[2].Condition != weather.ConditionUnknown {
		t.Errorf("missing measurements or condition-only rows: %+v", got)
	}
}

func TestInvalidEndpoint(t *testing.T) {
	for _, ep := range []string{"../forecast", "forecast?x=1", "Meteo France"} {
		if _, err := New(config.ProviderConfig{Name: "om", Options: map[string]any{"endpoint": ep}}, http.DefaultClient); err == nil {
			t.Errorf("endpoint %q: expected error", ep)
		}
	}
}
