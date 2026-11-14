package poller

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yanndethe/meteocomp/internal/store"
	"github.com/yanndethe/meteocomp/internal/weather"
)

type fakeProvider struct{ fail map[string]bool }

func (fakeProvider) Name() string { return "fake" }
func (fakeProvider) Type() string { return "faketype" }
func (f fakeProvider) Fetch(_ context.Context, loc weather.Location) ([]weather.Forecast, error) {
	if f.fail[loc.Name] {
		return nil, errors.New("boom")
	}
	return []weather.Forecast{{ForecastTime: time.Now().UTC(), Kind: weather.KindForecast, Condition: weather.ConditionClear}}, nil
}

// syncBuffer is a goroutine-safe bytes.Buffer for capturing logs.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestRunPollsImmediatelyAndLogs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	var logs syncBuffer
	locs := []weather.Location{{Name: "paris"}, {Name: "oslo"}}
	p := New([]weather.Provider{fakeProvider{fail: map[string]bool{"oslo": true}}}, time.Hour,
		locs, st, slog.New(slog.NewTextHandler(&logs, nil)))

	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()

	// The startup poll must happen without waiting for the 1h interval.
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), "next provider poll scheduled") {
		if time.Now().After(deadline) {
			t.Fatalf("startup poll did not complete; logs:\n%s", logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	readings, err := st.ListReadings(context.Background(), store.ReadingFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(readings) != 1 || readings[0].LocationName != "paris" {
		t.Fatalf("expected only successful paris readings: %+v", readings)
	}

	out := logs.String()
	for _, want := range []string{
		`msg="provider poll started" provider=fake provider_type=faketype reason=startup locations=2`,
		`msg="location poll ok" provider=fake provider_type=faketype reason=startup location=paris forecasts=1`,
		`msg="location poll failed" provider=fake provider_type=faketype reason=startup location=oslo error=boom`,
		`msg="provider poll finished" provider=fake provider_type=faketype reason=startup locations_ok=1 locations_failed=1 locations_skipped=0 forecasts=1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing log %q in:\n%s", want, out)
		}
	}
}

// A provider polled less than an interval ago is not polled again at startup;
// its schedule resumes from the last poll.
func TestRunResumesFromLastPoll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	recent := time.Now().UTC().Add(-10 * time.Minute)
	if err := st.RecordReadings(ctx, []store.ReadingRecord{{
		Provider: "fake", ProviderType: "faketype", LocationName: "paris", PolledAt: recent,
		Forecast: weather.Forecast{Kind: weather.KindForecast, ForecastTime: recent.Add(time.Hour)},
	}}); err != nil {
		t.Fatal(err)
	}

	var logs syncBuffer
	p := New([]weather.Provider{fakeProvider{}}, time.Hour, []weather.Location{{Name: "paris"}}, st,
		slog.New(slog.NewTextHandler(&logs, nil)))
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { p.RunUntil(ctx, stop); close(done) }()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), "next provider poll scheduled") {
		if time.Now().After(deadline) {
			t.Fatalf("schedule not logged; logs:\n%s", logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(stop)
	<-done

	if strings.Contains(logs.String(), "provider poll started") {
		t.Errorf("provider polled although its last poll is recent:\n%s", logs.String())
	}
	want := recent.Add(time.Hour).Format(time.RFC3339)
	if !strings.Contains(logs.String(), "next_poll_at="+want) {
		t.Errorf("expected next_poll_at=%s in:\n%s", want, logs.String())
	}
}

type obsProvider struct{ at time.Time }

func (obsProvider) Name() string { return "ic" }
func (obsProvider) Type() string { return "infoclimat" }
func (o obsProvider) Fetch(context.Context, weather.Location) ([]weather.Forecast, error) {
	return []weather.Forecast{{ForecastTime: o.at, Kind: weather.KindObservation, Condition: weather.ConditionUnknown}}, nil
}

type sourceProvider struct{ at time.Time }

func (sourceProvider) Name() string     { return "synop" }
func (sourceProvider) Type() string     { return "meteofrance_synop" }
func (sourceProvider) FillsConditions() {}
func (s sourceProvider) Fetch(context.Context, weather.Location) ([]weather.Forecast, error) {
	return []weather.Forecast{{ForecastTime: s.at, Kind: weather.KindObservation,
		Condition: weather.ConditionSnow, ConditionText: "Snow"}}, nil
}

func TestConditionSourceFillsWithoutStoring(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	at := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Hour)
	ic, synop := obsProvider{at: at.Add(40 * time.Minute)}, sourceProvider{at: at}
	p := New([]weather.Provider{ic, synop, fakeProvider{}}, time.Hour,
		[]weather.Location{{Name: "paris"}}, st, slog.New(slog.DiscardHandler))

	conditions := func() map[string]int {
		rs, err := st.ListReadings(ctx, store.ReadingFilter{})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int{}
		for _, r := range rs {
			out[r.Provider+"/"+string(r.Condition)+"/"+r.ConditionText]++
		}
		return out
	}
	poll := func(prov weather.Provider) {
		if failures := p.PollProvider(ctx, prov, ReasonManual); failures != 0 {
			t.Fatalf("%s: %d failures", prov.Name(), failures)
		}
	}

	// An observation stored before the SYNOP poll is filled by it.
	poll(ic)
	poll(synop)
	poll(fakeProvider{})
	want := map[string]int{"ic/snow/Snow (SYNOP)": 1, "fake/clear/": 1}
	if got := conditions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after SYNOP poll: %v, want %v", got, want)
	}
	// An observation stored after the SYNOP poll is filled from the SYNOP data in memory.
	poll(ic)
	want["ic/snow/Snow (SYNOP)"] = 2
	if got := conditions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after second observation poll: %v, want %v", got, want)
	}
}
