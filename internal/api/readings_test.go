package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/yanndethe/meteocomp/internal/store"
	"github.com/yanndethe/meteocomp/internal/weather"
)

func TestReadingsCursor(t *testing.T) {
	ts, st := newTestServer(t)
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	addPoll := func() {
		t.Helper()
		_, err := st.RecordPoll(context.Background(), store.PollResult{
			Provider: "test", Location: weather.Location{Name: "test"},
			StartedAt: start, FinishedAt: start,
			Forecasts: []weather.Forecast{
				{ForecastTime: start, Granularity: weather.GranularityDaily},
				{ForecastTime: start, Granularity: weather.GranularityHourly},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	addPoll()
	addPoll()
	addPoll()
	getPage := func(cursor string) []store.ReadingRecord {
		t.Helper()
		res, err := http.Get(ts.URL + "/api/v1/readings?location=test&kind=forecast&order=id_desc&limit=2" + cursor)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status: %d", res.StatusCode)
		}
		var rows []store.ReadingRecord
		if err := json.NewDecoder(res.Body).Decode(&rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	page := getPage("")
	addPoll() // New IDs must not shift later pages.
	var ids []int64
	for len(page) > 0 {
		for _, row := range page {
			ids = append(ids, row.ID)
		}
		page = getPage(fmt.Sprintf("&before_id=%d", page[len(page)-1].ID))
	}
	if len(ids) != 6 {
		t.Fatalf("got IDs %v, want six initial readings", ids)
	}
	for i, id := range ids {
		if id != int64(6-i) {
			t.Fatalf("IDs not contiguous descending: %v", ids)
		}
	}
	for _, query := range []string{
		"order=unknown", "before_id=1", "order=id_desc&before_id=0",
		"order=id_desc&before_id=-1", "order=id_desc&before_id=abc",
		"order=id_desc&before_id=", "order=id_desc&before_id=9223372036854775808",
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
}
