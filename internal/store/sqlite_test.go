package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/yanndethe/meteocomp/internal/weather"
)

func TestRecordAndList(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	loc := weather.Location{Name: "paris", City: "Paris", Country: "FR", Latitude: 48.85, Longitude: 2.35}
	start := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	temp := 10.5
	fc := []weather.Forecast{
		{ForecastTime: start.Add(6 * time.Hour), Granularity: weather.GranularityHourly, TemperatureC: &temp,
			Condition: weather.ConditionRain, ConditionCode: "61", ConditionText: "Rain"},
		{ForecastTime: start.Add(24 * time.Hour), Granularity: weather.GranularityDaily, Condition: weather.ConditionClear},
	}

	okID, err := s.RecordPoll(ctx, PollResult{Provider: "om", ProviderType: "openmeteo", Location: loc,
		StartedAt: start, FinishedAt: start.Add(250 * time.Millisecond), Forecasts: fc})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordPoll(ctx, PollResult{Provider: "owm", ProviderType: "openweathermap", Location: loc,
		StartedAt: start, FinishedAt: start, Err: errors.New("http 401")}); err != nil {
		t.Fatal(err)
	}

	polls, err := s.ListPolls(ctx, PollFilter{Status: StatusError})
	if err != nil {
		t.Fatal(err)
	}
	if len(polls) != 1 || polls[0].Provider != "owm" || polls[0].Error != "http 401" {
		t.Errorf("error polls: %+v", polls)
	}
	polls, _ = s.ListPolls(ctx, PollFilter{Provider: "om"})
	if len(polls) != 1 || polls[0].ReadingCount != 2 || polls[0].DurationMs != 250 || !polls[0].StartedAt.Equal(start) {
		t.Errorf("ok polls: %+v", polls)
	}

	got, err := s.ListReadings(ctx, ReadingFilter{PollID: okID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d readings, want 2", len(got))
	}
	// Ordered by granularity then forecast_time: "daily" < "hourly".
	d, h := got[0], got[1]
	if d.Granularity != weather.GranularityDaily || d.TemperatureC != nil || d.LeadTimeHours != 24 {
		t.Errorf("daily row: %+v", d)
	}
	if *h.TemperatureC != 10.5 || h.Condition != weather.ConditionRain || h.ConditionCode != "61" ||
		h.Location != loc || h.LeadTimeHours != 6 || !h.PolledAt.Equal(start) {
		t.Errorf("hourly row: %+v", h)
	}
	// Both data points have forecast_time in the future relative to start, so Kind defaults to forecast.
	for _, r := range got {
		if r.Kind != weather.KindForecast {
			t.Errorf("expected KindForecast, got %q", r.Kind)
		}
	}

	got, _ = s.ListReadings(ctx, ReadingFilter{From: start.Add(12 * time.Hour)})
	if len(got) != 1 || got[0].Granularity != weather.GranularityDaily {
		t.Errorf("time filter: %+v", got)
	}

	// Kind filter: only forecasts (both rows are forecasts, so count stays 2).
	got, _ = s.ListReadings(ctx, ReadingFilter{Kind: "forecast"})
	if len(got) != 2 {
		t.Errorf("kind=forecast filter: got %d, want 2", len(got))
	}
	// Kind filter: no observations yet.
	got, _ = s.ListReadings(ctx, ReadingFilter{Kind: "observation"})
	if len(got) != 0 {
		t.Errorf("kind=observation filter: got %d, want 0", len(got))
	}
}
