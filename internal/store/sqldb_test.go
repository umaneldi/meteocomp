package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yanndethe/meteocomp/internal/weather"
)

func testReading(at time.Time) ReadingRecord {
	return ReadingRecord{
		Provider: "om", ProviderType: "openmeteo", LocationName: "paris", PolledAt: at,
		Forecast: weather.Forecast{
			Kind: weather.KindForecast, ForecastTime: at.Add(6 * time.Hour),
			TemperatureC: new(0.0), Condition: weather.ConditionRain, ConditionText: "Rain",
		},
	}
}

func TestRecordAndList(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *SQLDB) {
		ctx := context.Background()
		start := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
		forecast := testReading(start)
		observation := testReading(start)
		observation.Kind = weather.KindObservation
		observation.ForecastTime = start.Add(-time.Hour)
		observation.TemperatureC = nil
		if err := s.RecordReadings(ctx, []ReadingRecord{forecast, observation}); err != nil {
			t.Fatal(err)
		}
		got, err := s.ListReadings(ctx, ReadingFilter{Provider: "om", Location: "paris", Kind: "forecast"})
		forecast.LeadTimeHours = 6
		if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], forecast) {
			t.Fatalf("forecast round trip: %+v, %v", got, err)
		}
		got, err = s.ListReadings(ctx, ReadingFilter{To: start, Kind: "observation"})
		if err != nil || len(got) != 1 || got[0].TemperatureC != nil || got[0].LeadTimeHours != -1 {
			t.Fatalf("observation round trip: %+v, %v", got, err)
		}
		for _, filter := range []ReadingFilter{
			{Provider: "other"}, {Location: "other"}, {From: start.Add(7 * time.Hour)},
			{PolledFrom: start.Add(time.Second)}, {PolledTo: start},
		} {
			got, err := s.ListReadings(ctx, filter)
			if err != nil || len(got) != 0 {
				t.Fatalf("filter %+v: %+v, %v", filter, got, err)
			}
		}
	})
}

func TestReadingsPagination(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *SQLDB) {
		ctx := context.Background()
		start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
		for i := range 5 {
			if err := s.RecordReadings(ctx, []ReadingRecord{testReading(start.Add(time.Duration(i) * time.Hour))}); err != nil {
				t.Fatal(err)
			}
		}
		filter := ReadingFilter{Limit: 2, PolledTo: start.Add(5 * time.Hour)}
		var got []ReadingRecord
		for {
			page, err := s.ListReadings(ctx, filter)
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, page...)
			if len(page) < filter.Limit {
				break
			}
			filter.Offset += len(page)
			if filter.Offset == 2 {
				if err := s.RecordReadings(ctx, []ReadingRecord{testReading(start.Add(6 * time.Hour))}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if len(got) != 5 {
			t.Fatalf("got %d rows", len(got))
		}
		for i, r := range got {
			if !r.PolledAt.Equal(start.Add(time.Duration(4-i) * time.Hour)) {
				t.Fatalf("unexpected page ordering: %+v", got)
			}
		}
	})
}

func TestRecordReadingsAtomicValidation(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *SQLDB) {
		ctx := context.Background()
		valid := testReading(time.Now().UTC())
		for _, field := range []string{"provider", "provider_type", "location_name", "forecast_time", "polled_at", "kind"} {
			invalid := valid
			switch field {
			case "provider":
				invalid.Provider = ""
			case "provider_type":
				invalid.ProviderType = ""
			case "location_name":
				invalid.LocationName = ""
			case "forecast_time":
				invalid.ForecastTime = time.Time{}
			case "polled_at":
				invalid.PolledAt = time.Time{}
			case "kind":
				invalid.Kind = "invalid"
			}
			if err := s.RecordReadings(ctx, []ReadingRecord{valid, invalid}); err == nil {
				t.Fatalf("accepted invalid %s", field)
			}
		}
		rows, err := s.ListReadings(ctx, ReadingFilter{})
		if err != nil || len(rows) != 0 {
			t.Fatalf("partial transaction persisted: %+v, %v", rows, err)
		}
	})
}

func TestSimplifiedSchemaMigration(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *SQLDB) {
		ctx := context.Background()
		schema := sqliteSchema
		if s.d.name == "postgres" {
			schema = postgresSchema
		}
		start := strings.Index(schema, "CREATE TABLE IF NOT EXISTS readings (")
		end := strings.Index(schema[start:], ");") + start + 2
		legacy := strings.Replace(schema[start:end], "readings (", `readings (
			id INTEGER PRIMARY KEY,
			poll_id INTEGER REFERENCES polls(id),
			granularity TEXT NOT NULL,`, 1)
		legacy = strings.Replace(legacy, "condition_text                TEXT    NOT NULL", "condition_text TEXT", 1)
		if _, err := s.db.ExecContext(ctx, `DROP TABLE readings;
			CREATE TABLE polls (id INTEGER PRIMARY KEY);
			INSERT INTO polls VALUES (1);`+legacy); err != nil {
			t.Fatal(err)
		}
		at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
		for i, granularity := range []string{"hourly", "3h", "daily"} {
			if _, err := s.db.ExecContext(ctx, s.d.rebind(`INSERT INTO readings
				(id, poll_id, granularity, provider, provider_type, location_name, polled_at,
				forecast_time, lead_time_hours, kind, temperature_c, condition, condition_text)
				VALUES (?, 1, ?, 'om', 'openmeteo', 'paris', ?, ?, 6, 'forecast', 0, 'rain', NULL)`),
				i+1, granularity, formatTime(at), formatTime(at.Add(6*time.Hour))); err != nil {
				t.Fatal(err)
			}
		}
		for range 2 {
			if err := applyReadingSchema(ctx, s.db, s.d, schema); err != nil {
				t.Fatal(err)
			}
		}
		rows, err := s.ListReadings(ctx, ReadingFilter{})
		if err != nil || len(rows) != 2 || rows[0].TemperatureC == nil || *rows[0].TemperatureC != 0 ||
			rows[0].ConditionText != "" || rows[0].LeadTimeHours != 6 {
			t.Fatalf("migration lost retained values: %+v, %v", rows, err)
		}
		columns, err := s.db.QueryContext(ctx, "SELECT * FROM readings LIMIT 0")
		if err != nil {
			t.Fatal(err)
		}
		names, err := columns.Columns()
		columns.Close()
		if err != nil {
			t.Fatal(err)
		}
		expected := strings.Fields(strings.ReplaceAll(readingColumns, ",", " "))
		if len(names) != len(expected) {
			t.Fatalf("unexpected reading columns: %v", names)
		}
		for _, name := range names {
			if !strings.Contains(","+strings.Join(expected, ",")+",", ","+name+",") {
				t.Fatalf("unexpected reading column %q", name)
			}
		}
		var tables int
		query := "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='polls'"
		if s.d.name == "postgres" {
			query = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='polls'"
		}
		if err := s.db.QueryRowContext(ctx, query).Scan(&tables); err != nil || tables != 0 {
			t.Fatalf("polls table remains: %d, %v", tables, err)
		}
	})
}

func TestFillObservationConditions(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *SQLDB) {
		ctx := context.Background()
		day := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
		at := func(h, m int) time.Time { return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
		obs := func(provider, typ string, ft time.Time, c weather.Condition, text string) ReadingRecord {
			return ReadingRecord{
				Provider: provider, ProviderType: typ, LocationName: "paris", PolledAt: day.Add(24 * time.Hour),
				Forecast: weather.Forecast{Kind: weather.KindObservation, ForecastTime: ft, Condition: c, ConditionText: text},
			}
		}
		ic := func(ft time.Time, c weather.Condition) ReadingRecord { return obs("ic", "infoclimat", ft, c, "") }
		sources := []weather.Forecast{
			{ForecastTime: at(15, 0), Condition: weather.ConditionFog, ConditionText: "Fog"},
			{ForecastTime: at(12, 0), Condition: weather.ConditionRain, ConditionText: "Rain"},
			{ForecastTime: at(18, 0), Condition: weather.ConditionUnknown},
		}
		readings := []ReadingRecord{
			ic(at(12, 10), weather.ConditionUnknown),
			ic(at(12, 10), weather.ConditionUnknown),
			ic(at(13, 20), weather.ConditionUnknown),
			ic(at(13, 40), weather.ConditionUnknown),
			ic(at(17, 0), weather.ConditionUnknown),
			ic(at(12, 20), weather.ConditionCloudy),
		}
		if err := s.RecordReadings(ctx, readings); err != nil {
			t.Fatal(err)
		}
		n, err := s.FillObservationConditions(ctx, "paris", sources, 90*time.Minute)
		if err != nil || n != 4 {
			t.Fatalf("updated %d, %v; want 4", n, err)
		}
		got, err := s.ListReadings(ctx, ReadingFilter{})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{
			"12:10": "rain/Rain (SYNOP)", "13:20": "rain/Rain (SYNOP)", "13:40": "fog/Fog (SYNOP)",
			"17:00": "unknown/", "12:20": "cloudy/",
		}
		if len(got) != 6 {
			t.Fatalf("got %d readings", len(got))
		}
		for _, r := range got {
			key := r.ForecastTime.Format("15:04")
			if v := string(r.Condition) + "/" + r.ConditionText; v != want[key] {
				t.Errorf("%s: got %q, want %q", key, v, want[key])
			}
		}
		if n, err := s.FillObservationConditions(ctx, "paris", sources, 90*time.Minute); err != nil || n != 0 {
			t.Errorf("second fill: %d, %v", n, err)
		}
		known, err := s.ListReadings(ctx, ReadingFilter{KnownCondition: true})
		if err != nil || len(known) != 5 {
			t.Fatalf("known_condition filter: %d readings, %v; want 5", len(known), err)
		}
		for _, r := range known {
			if r.Condition == weather.ConditionUnknown {
				t.Errorf("known_condition filter returned unknown at %s", r.ForecastTime.Format("15:04"))
			}
		}
	})
}
