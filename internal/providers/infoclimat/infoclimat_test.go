package infoclimat

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yanndethe/meteocomp/internal/config"
	"github.com/yanndethe/meteocomp/internal/weather"
)

// pastHourTimestamp returns a timestamp 2 hours in the past, truncated to the hour.
func pastHourTimestamp() string {
	return time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour).Format("2006-01-02 15:04:05")
}

func fixture(stationID, ts string) string {
	return `{
  "status": "OK",
  "errors": [],
  "data": [],
  "hourly": {
    "` + stationID + `": [
      {
        "id_station": "` + stationID + `",
        "dh_utc": "` + ts + `",
        "temperature": 12.3,
        "temperature_min": null,
        "temperature_max": null,
        "humidite": 85.0,
        "point_de_rosee": 9.8,
        "pression": 1015.2,
        "vent_moyen": 11.5,
        "vent_rafales": 28.8,
        "vent_direction": 180.0,
        "pluie_1h": 0.5,
        "visibilite": 30000.0,
        "temps_omm": 61,
        "uv_index": null
      }
    ]
  }
}`
}

func TestFetch(t *testing.T) {
	const stationID = "07156"
	ts := pastHourTimestamp()

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
		Options: map[string]any{"forecast_days": 1},
	}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	got, err := p.Fetch(context.Background(), weather.Location{Name: "paris", StationID: stationID, Latitude: 48.85, Longitude: 2.35})
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
	// Wind speed: 11.5 km/h (already in km/h, no conversion)
	if obs.WindSpeedKmh == nil || *obs.WindSpeedKmh != 11.5 {
		t.Errorf("wind speed: %v", obs.WindSpeedKmh)
	}
	// Wind gusts: 28.8 km/h (already in km/h)
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

func TestFetchAll(t *testing.T) {
	const stationA = "07156"
	const stationB = "000PU"
	ts := pastHourTimestamp()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.URL.Query()["stations[]"]
		if len(got) != 2 {
			t.Errorf("expected 2 stations[], got %v", got)
		}
		w.Header().Set("Content-Type", "application/json")
		// Return data for both stations in a single response.
		fmt.Fprintf(w, `{"status":"OK","errors":[],"data":[],"hourly":{%q:%s,%q:%s,"_params":["id_station","dh_utc","temperature"]}}`,
			stationA, `[{"dh_utc":"`+ts+`","temperature":12.3,"humidite":85.0,"pression":null,"vent_moyen":null,"vent_rafales":null,"vent_direction":null,"pluie_1h":null,"visibilite":null,"temps_omm":0,"uv_index":null}]`,
			stationB, `[{"dh_utc":"`+ts+`","temperature":"22.1","humidite":"60","pression":null,"vent_moyen":null,"vent_rafales":null,"vent_direction":null,"pluie_1h":null,"visibilite":"","temps_omm":"0","uv_index":null}]`,
		)
	}))
	defer srv.Close()

	p, err := New(config.ProviderConfig{Name: "ic", APIKey: "tok", BaseURL: srv.URL + "/"}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	locs := []weather.Location{
		{Name: "paris", StationID: stationA},
		{Name: "castanet", StationID: stationB},
		{Name: "nostation"},
	}
	results, err := p.(interface {
		FetchAll(context.Context, []weather.Location) (map[string]weather.LocationResult, error)
	}).FetchAll(context.Background(), locs)
	if err != nil {
		t.Fatal(err)
	}

	if lr := results["paris"]; lr.Err != nil || len(lr.Forecasts) != 1 {
		t.Errorf("paris: err=%v forecasts=%d", lr.Err, len(lr.Forecasts))
	} else if *lr.Forecasts[0].TemperatureC != 12.3 {
		t.Errorf("paris temp: %v", *lr.Forecasts[0].TemperatureC)
	}
	if lr := results["castanet"]; lr.Err != nil || len(lr.Forecasts) != 1 {
		t.Errorf("castanet: err=%v forecasts=%d", lr.Err, len(lr.Forecasts))
	} else if *lr.Forecasts[0].TemperatureC != 22.1 {
		t.Errorf("castanet temp: %v", *lr.Forecasts[0].TemperatureC)
	}
	if lr := results["nostation"]; lr.Err == nil {
		t.Error("nostation: expected error for missing station_id")
	}
}

func TestFutureTimestampFiltered(t *testing.T) {
	const stationID = "07156"
	futureTS := time.Now().UTC().Add(1 * time.Hour).Truncate(time.Hour).Format("2006-01-02 15:04:05")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fixture(stationID, futureTS))) //nolint:errcheck
	}))
	defer srv.Close()

	p, err := New(config.ProviderConfig{Name: "ic", APIKey: "tok", BaseURL: srv.URL + "/"}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Fetch(context.Background(), weather.Location{Name: "paris", StationID: stationID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 readings (future filtered), got %d", len(got))
	}
}

func TestMissingStationID(t *testing.T) {
	p, err := New(config.ProviderConfig{Name: "ic", APIKey: "tok"}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background(), weather.Location{Name: "paris"}); err == nil {
		t.Error("expected error for location with no station_id")
	}
}

func TestValidation(t *testing.T) {
	client := http.DefaultClient
	if _, err := New(config.ProviderConfig{Name: "ic"}, client); err == nil {
		t.Error("missing api_key: expected error")
	}
}
