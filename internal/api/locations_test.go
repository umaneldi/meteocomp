package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/yanndethe/meteocomp/internal/store"
	"github.com/yanndethe/meteocomp/internal/weather"
)

func TestLocationsLatest(t *testing.T) {
	ts, st := newTestServer(t)
	base := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

	// Insert three observations for "paris" at increasing forecast_times,
	// and one observation for "lyon" at a fixed time.
	for i := range 3 {
		if err := st.RecordReadings(context.Background(), []store.ReadingRecord{{
			Provider:     "obs",
			ProviderType: "infoclimat",
			LocationName: "paris",
			PolledAt:     base.Add(time.Duration(i) * time.Hour),
			Forecast: weather.Forecast{
				Kind:         weather.KindObservation,
				ForecastTime: base.Add(time.Duration(i) * time.Hour),
			},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	lyonFt := base.Add(30 * time.Minute)
	if err := st.RecordReadings(context.Background(), []store.ReadingRecord{{
		Provider:     "obs",
		ProviderType: "infoclimat",
		LocationName: "lyon",
		PolledAt:     base,
		Forecast: weather.Forecast{
			Kind:         weather.KindObservation,
			ForecastTime: lyonFt,
		},
	}}); err != nil {
		t.Fatal(err)
	}
	// A forecast record — must not influence latest_forecast_time.
	if err := st.RecordReadings(context.Background(), []store.ReadingRecord{{
		Provider:     "fc",
		ProviderType: "openmeteo",
		LocationName: "paris",
		PolledAt:     base,
		Forecast: weather.Forecast{
			Kind:         weather.KindForecast,
			ForecastTime: base.Add(48 * time.Hour),
		},
	}}); err != nil {
		t.Fatal(err)
	}

	// ?latest=true returns location objects augmented with latest_forecast_time.
	res, err := http.Get(ts.URL + "/api/v1/locations?latest=true")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", res.StatusCode)
	}
	var rows []map[string]any
	if err := json.NewDecoder(res.Body).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	// newTestServer starts with no configured locations, so the response is empty.
	// Re-verify: the endpoint returns configured locations, not raw readings.
	// (The test server has no locations in settings, so we get []).
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows (no configured locations), got %d: %+v", len(rows), rows)
	}

	// Without ?latest=true, the endpoint still returns configured locations.
	res2, err := http.Get(ts.URL + "/api/v1/locations")
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", res2.StatusCode)
	}

	// Verify the store method directly: paris → max forecast_time (base+2h), lyon → lyonFt.
	times, err := st.LatestObservationTimes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantParis := base.Add(2 * time.Hour).UTC().Truncate(time.Second)
	if got := times["paris"].UTC(); !got.Equal(wantParis) {
		t.Errorf("paris: want %s, got %s", wantParis, got)
	}
	wantLyon := lyonFt.UTC().Truncate(time.Second)
	if got := times["lyon"].UTC(); !got.Equal(wantLyon) {
		t.Errorf("lyon: want %s, got %s", wantLyon, got)
	}
	if _, ok := times["fc"]; ok {
		t.Error("forecast provider should not appear in LatestObservationTimes")
	}

	// Empty result when no observations exist.
	ts2, st2 := newTestServer(t)
	_ = ts2
	times2, err := st2.LatestObservationTimes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(times2) != 0 {
		t.Fatalf("expected empty map, got %+v", times2)
	}
}
