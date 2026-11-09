package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/yanndethe/meteocomp/internal/store"
	"github.com/yanndethe/meteocomp/internal/weather"
)

func TestReadingsPaginationAndShape(t *testing.T) {
	ts, st := newTestServer(t)
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i := range 5 {
		if err := st.RecordReadings(context.Background(), []store.ReadingRecord{{
			Provider: "test", ProviderType: "openmeteo", LocationName: "test", PolledAt: start.Add(time.Duration(i) * time.Hour),
			Forecast: weather.Forecast{Kind: weather.KindForecast, ForecastTime: start.Add(6 * time.Hour)},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	var rows []map[string]any
	for offset := 0; ; offset += 2 {
		res, err := http.Get(ts.URL + fmt.Sprintf("/api/v1/readings?location=test&kind=forecast&limit=2&offset=%d", offset))
		if err != nil {
			t.Fatal(err)
		}
		var page []map[string]any
		err = json.NewDecoder(res.Body).Decode(&page)
		res.Body.Close()
		if err != nil || res.StatusCode != http.StatusOK {
			t.Fatalf("read page: %d, %v", res.StatusCode, err)
		}
		rows = append(rows, page...)
		if len(page) < 2 {
			break
		}
	}
	if len(rows) != 5 || rows[0]["polled_at"] != "2026-11-01T04:00:00Z" || rows[4]["polled_at"] != "2026-11-01T00:00:00Z" {
		t.Fatalf("unexpected pages: %+v", rows)
	}
	expected := []string{
		"provider", "provider_type", "location_name", "forecast_time", "lead_time_hours", "kind",
		"temperature_c", "feels_like_c", "humidity_pct", "dew_point_c", "pressure_hpa",
		"wind_speed_kmh", "wind_gust_kmh", "wind_direction_deg", "cloud_cover_pct",
		"precipitation_probability_pct", "snowfall_cm", "visibility_km", "condition", "condition_text", "polled_at",
	}
	var keys []string
	for key := range rows[0] {
		keys = append(keys, key)
	}
	slices.Sort(expected)
	slices.Sort(keys)
	if !reflect.DeepEqual(keys, expected) || rows[0]["temperature_c"] != nil {
		t.Fatalf("Reading JSON shape differs: %+v", rows[0])
	}
	for _, query := range []string{
		"order=id_desc", "before_id=1", "poll_id=1", "granularity=hourly",
		"offset=-1", "offset=abc", "offset=", "offset=9223372036854775808",
	} {
		t.Run(query, func(t *testing.T) {
			res, err := http.Get(ts.URL + "/api/v1/readings?" + query)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("status: %d", res.StatusCode)
			}
		})
	}
	res, err := http.Get(ts.URL + "/api/v1/polls")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("poll history still exposed: %d", res.StatusCode)
	}
}
