package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/yanndethe/meteocomp/internal/config"
	"github.com/yanndethe/meteocomp/internal/weather"
)

func TestSettingsRoundTrip(t *testing.T) { forEachBackend(t, testSettingsRoundTrip) }

func testSettingsRoundTrip(t *testing.T, st *SQLDB) {
	ctx := context.Background()

	if _, found, err := st.LoadSettings(ctx); err != nil || found {
		t.Fatalf("empty db: found=%v err=%v", found, err)
	}

	in := config.Settings{
		PollInterval: 90 * time.Minute,
		Locations: []weather.Location{
			{Name: "oslo", City: "Oslo", Country: "NO", Latitude: 59.91, Longitude: 10.75},
			{Name: "lyon", City: "Lyon", Country: "FR", Latitude: 45.76, Longitude: 4.83},
		},
		Providers: []config.ProviderConfig{
			{Name: "z", Type: "weatherapi", APIKey: "k1", Enabled: new(true), Options: map[string]any{"days": float64(3)}},
			{Name: "a", Type: "openmeteo", Enabled: new(false), Options: map[string]any{"endpoint": "gfs"}},
		},
	}
	for range 2 { // saving twice must replace, not duplicate
		if err := st.SaveSettings(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	out, found, err := st.LoadSettings(ctx)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if !reflect.DeepEqual(*out, in) {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", *out, in)
	}
	if out.Providers[0].IntOption("days", 0) != 3 {
		t.Errorf("days option lost")
	}
}

func TestLastPollTimes(t *testing.T) { forEachBackend(t, testLastPollTimes) }

func testLastPollTimes(t *testing.T, st *SQLDB) {
	ctx := context.Background()
	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{t1, t1.Add(time.Hour)} {
		if _, err := st.RecordPoll(ctx, PollResult{Provider: "p", ProviderType: "x",
			Location: weather.Location{Name: "l"}, StartedAt: at, FinishedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.LastPollTimes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got["p"].Equal(t1.Add(time.Hour)) || len(got) != 1 {
		t.Errorf("got %v", got)
	}
}
