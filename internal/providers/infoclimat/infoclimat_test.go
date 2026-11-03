package infoclimat

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yanndethe/meteocomp/internal/config"
	"github.com/yanndethe/meteocomp/internal/weather"
)

func pastTimestamp() string {
	return time.Now().UTC().Add(-2 * time.Hour).Format("2006-01-02 15:04:05")
}

func fixture(stationID, ts string) string {
	return `{
  "request": {},
  "data": {
    "` + stationID + `": {
      "` + ts + `": {
        "tc": 12.3,
        "u": 85.0,
        "pmer": 1015.2,
        "ff": 3.2,
        "dd": 180.0,
        "rr1": 0.5,
        "td": 9.8,
        "vv": 30000.0,
        "ww": 61,
        "rafales": 8.0
      }
    }
  }
}`
}

func TestFetch(t *testing.T) {
	const stationID = "07156"
	ts := pastTimestamp()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("token"); got != "tok" {
			t.Errorf("unexpected token: %q", got)
		}
		// stations[] is passed raw (not encoded) so it parses as a normal query param.
		if got := r.URL.Query()["stations[]"]; len(got) != 1 || got[0] != stationID {
			t.Errorf("unexpected stations[]: %v", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fixture(stationID, ts))) //nolint:errcheck
	}))
	defer srv.Close()

	p, err := New(config.ProviderConfig{
		Name:    "ic",
		APIKey:  "tok",
		BaseURL: srv.URL + "/",
		Options: map[string]any{"station_id": stationID, "past_days": 1},
	}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	got, err := p.Fetch(context.Background(), weather.Location{Name: "paris", Latitude: 48.85, Longitude: 2.35})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d readings, want 1", len(got))
	}

	obs := got[0]
	if obs.Kind != weather.KindObservation {
		t.Errorf("kind: got %q, want KindObservation", obs.Kind)
	}
	if obs.Granularity != weather.GranularityHourly {
		t.Errorf("granularity: %q", obs.Granularity)
	}
	if obs.TemperatureC == nil || *obs.TemperatureC != 12.3 {
		t.Errorf("temperature: %v", obs.TemperatureC)
	}
	if obs.HumidityPct == nil || *obs.HumidityPct != 85.0 {
		t.Errorf("humidity: %v", obs.HumidityPct)
	}
	// Wind speed: 3.2 m/s × 3.6 = 11.52 km/h
	if obs.WindSpeedKmh == nil || math.Abs(*obs.WindSpeedKmh-11.52) > 0.001 {
		t.Errorf("wind speed: %v", obs.WindSpeedKmh)
	}
	// Wind gusts: 8.0 m/s × 3.6 = 28.8 km/h
	if obs.WindGustKmh == nil || math.Abs(*obs.WindGustKmh-28.8) > 0.001 {
		t.Errorf("wind gust: %v", obs.WindGustKmh)
	}
	// Visibility: 30000 m ÷ 1000 = 30 km
	if obs.VisibilityKm == nil || *obs.VisibilityKm != 30.0 {
		t.Errorf("visibility: %v", obs.VisibilityKm)
	}
	if obs.Condition != weather.ConditionRain {
		t.Errorf("condition: got %q, want ConditionRain (ww=61)", obs.Condition)
	}
	if obs.ConditionCode != "61" {
		t.Errorf("condition_code: %q", obs.ConditionCode)
	}
}

func TestFutureTimestampFiltered(t *testing.T) {
	const stationID = "07156"
	futureTS := time.Now().UTC().Add(1 * time.Hour).Format("2006-01-02 15:04:05")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fixture(stationID, futureTS))) //nolint:errcheck
	}))
	defer srv.Close()

	p, err := New(config.ProviderConfig{
		Name:    "ic",
		APIKey:  "tok",
		BaseURL: srv.URL + "/",
		Options: map[string]any{"station_id": stationID},
	}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Fetch(context.Background(), weather.Location{Name: "paris"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 readings (future filtered), got %d", len(got))
	}
}

func TestValidation(t *testing.T) {
	client := http.DefaultClient
	if _, err := New(config.ProviderConfig{Name: "ic", APIKey: "tok"}, client); err == nil {
		t.Error("missing station_id: expected error")
	}
	if _, err := New(config.ProviderConfig{Name: "ic", Options: map[string]any{"station_id": "07156"}}, client); err == nil {
		t.Error("missing api_key: expected error")
	}
	if _, err := New(config.ProviderConfig{Name: "ic", APIKey: "tok",
		Options: map[string]any{"station_id": "07156", "past_days": 31}}, client); err == nil {
		t.Error("past_days=31: expected error")
	}
	if _, err := New(config.ProviderConfig{Name: "ic", APIKey: "tok",
		Options: map[string]any{"station_id": "07156", "past_days": 0}}, client); err == nil {
		t.Error("past_days=0: expected error")
	}
}
