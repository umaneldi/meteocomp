package poller

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
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
	return []weather.Forecast{{ForecastTime: time.Now().UTC(), Granularity: weather.GranularityHourly, Condition: weather.ConditionClear}}, nil
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
	p := New([]Job{{Provider: fakeProvider{fail: map[string]bool{"oslo": true}}, Interval: time.Hour}},
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

	polls, err := st.ListPolls(context.Background(), store.PollFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(polls) != 2 {
		t.Fatalf("got %d polls, want 2", len(polls))
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
