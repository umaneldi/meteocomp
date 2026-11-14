package meteofrancesynop

import (
	"bytes"
	"compress/gzip"
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yanndethe/meteocomp/internal/config"
	"github.com/yanndethe/meteocomp/internal/weather"
)

const lastModified = "Fri, 13 Nov 2026 06:30:22 GMT"

type server struct {
	*httptest.Server
	mu       sync.Mutex
	files    map[string][]byte // path -> gzipped CSV
	requests []string
}

func gz(t *testing.T, csv string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write([]byte(csv)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func newServer(t *testing.T, files map[string]string) *server {
	t.Helper()
	s := &server{files: map[string][]byte{}}
	for path, csv := range files {
		s.files[path] = gz(t, csv)
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.requests = append(s.requests, r.URL.Path)
		body, ok := s.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("If-Modified-Since") == lastModified {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Last-Modified", lastModified)
		w.Write(body) //nolint:errcheck
	}))
	t.Cleanup(s.Close)
	return s
}

func fixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/synop_sample.csv")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func newProvider(t *testing.T, srv *server, now time.Time, opts map[string]any) *Provider {
	t.Helper()
	if opts == nil {
		opts = map[string]any{}
	}
	if _, ok := opts["forecast_days"]; !ok {
		opts["forecast_days"] = 1
	}
	p, err := New(config.ProviderConfig{Name: "synop", BaseURL: srv.URL + "/", Options: opts}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	sp := p.(*Provider)
	sp.now = func() time.Time { return now }
	return sp
}

var (
	paris    = weather.Location{Name: "paris", StationID: "07156", Latitude: 48.8566, Longitude: 2.3522}
	castanet = weather.Location{Name: "castanet", StationID: "000PU", Latitude: 43.52, Longitude: 1.512}
	nice     = weather.Location{Name: "nice", Latitude: 43.70, Longitude: 7.27}
)

func near(v *float64, want float64) bool { return v != nil && math.Abs(*v-want) < 1e-9 }

func TestFetchAll(t *testing.T) {
	srv := newServer(t, map[string]string{"/synop_2026.csv.gz": fixture(t)})
	p := newProvider(t, srv, time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC), nil)

	results, err := p.FetchAll(context.Background(), []weather.Location{paris, castanet, nice})
	if err != nil {
		t.Fatal(err)
	}
	if lr := results["nice"]; lr.Err == nil {
		t.Error("nice: expected error, no SYNOP station within 30 km in the fixture")
	}
	if lr := results["paris"]; lr.Err != nil || len(lr.Forecasts) != 4 {
		t.Fatalf("paris: err=%v forecasts=%d", lr.Err, len(lr.Forecasts))
	}

	// castanet has an Infoclimat-only station_id and falls back to Toulouse-Blagnac.
	lr := results["castanet"]
	if lr.Err != nil || len(lr.Forecasts) != 4 {
		t.Fatalf("castanet: err=%v forecasts=%d", lr.Err, len(lr.Forecasts))
	}
	for i := 1; i < len(lr.Forecasts); i++ {
		if !lr.Forecasts[i].ForecastTime.After(lr.Forecasts[i-1].ForecastTime) {
			t.Fatal("forecasts not sorted by time")
		}
	}
	// 07630;2026-11-12T18:00:00Z;pmer 99580;dd 270;ff 7.3;t 288.15;td 286.55;u 90;vv 4500;ww 61;n 100;raf10 10.4
	f := lr.Forecasts[2]
	if !f.ForecastTime.Equal(time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)) || f.Kind != weather.KindObservation {
		t.Fatalf("unexpected reading: %+v", f)
	}
	checks := []struct {
		name string
		got  *float64
		want float64
	}{
		{"temperature", f.TemperatureC, 15},
		{"dew point", f.DewPointC, 13.4},
		{"humidity", f.HumidityPct, 90},
		{"pressure", f.PressureHPa, 995.8},
		{"wind speed", f.WindSpeedKmh, 7.3 * 3.6},
		{"wind gust", f.WindGustKmh, 10.4 * 3.6},
		{"wind direction", f.WindDirectionDeg, 270},
		{"cloud cover", f.CloudCoverPct, 100},
		{"visibility", f.VisibilityKm, 4.5},
	}
	for _, c := range checks {
		if c.got == nil || math.Abs(*c.got-c.want) > 1e-6 {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}
	if f.FeelsLikeC != nil || f.PrecipitationProbabilityPct != nil || f.SnowfallCm != nil {
		t.Errorf("unsupported measurements must remain nil: %+v", f)
	}
	if f.Condition != weather.ConditionRain || f.ConditionText != "Rain" {
		t.Errorf("condition: %q %q", f.Condition, f.ConditionText)
	}
	// ww 0 with n 75 falls back to the cloud cover.
	if c := lr.Forecasts[1]; c.Condition != weather.ConditionPartlyCloudy {
		t.Errorf("cloud cover fallback: %q", c.Condition)
	}
	// Paris reports ww 0 and no cloud cover.
	if c := results["paris"].Forecasts[0]; c.Condition != weather.ConditionUnknown || c.CloudCoverPct != nil {
		t.Errorf("paris condition: %q cloud=%v", c.Condition, c.CloudCoverPct)
	}
}

func TestWindowAndCachedPolls(t *testing.T) {
	srv := newServer(t, map[string]string{"/synop_2026.csv.gz": fixture(t)})
	p := newProvider(t, srv, time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC), nil)

	got, err := p.Fetch(context.Background(), paris)
	if err != nil {
		t.Fatal(err)
	}
	// Rows at 18:00 and 21:00 are after "now".
	if len(got) != 2 {
		t.Fatalf("first poll: got %d readings, want 2", len(got))
	}

	// The file is unchanged (304): the whole window is still returned, from the cache.
	p.now = func() time.Time { return time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC) }
	got, err = p.Fetch(context.Background(), paris)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[3].ForecastTime.Hour() != 21 {
		t.Fatalf("cached poll: %+v", got)
	}
	if n := len(srv.requests); n != 2 {
		t.Errorf("got %d requests, want 2", n)
	}
}

func TestYearBoundary(t *testing.T) {
	header := strings.SplitN(fixture(t), "\n", 2)[0] + "\n"
	row := "48.821667;2.337833;07156;0-20000-0-07156;PARIS-MONTSOURIS;x;x;%s;;;;;;280.15;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;\n"
	srv := newServer(t, map[string]string{
		"/synop_2025.csv.gz": header + strings.Replace(row, "%s", "2025-12-31T21:00:00Z", 1),
		"/synop_2026.csv.gz": header + strings.Replace(row, "%s", "2026-01-01T00:00:00Z", 1),
	})
	p := newProvider(t, srv, time.Date(2026, 1, 1, 6, 0, 0, 0, time.UTC), nil)
	got, err := p.Fetch(context.Background(), paris)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !near(got[0].TemperatureC, 7) {
		t.Fatalf("got %+v", got)
	}
	if strings.Join(srv.requests, ",") != "/synop_2025.csv.gz,/synop_2026.csv.gz" {
		t.Errorf("requests: %v", srv.requests)
	}
}

func TestHTTPError(t *testing.T) {
	srv := newServer(t, nil)
	p := newProvider(t, srv, time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC), nil)
	if _, err := p.FetchAll(context.Background(), []weather.Location{paris}); err == nil {
		t.Error("expected error when the current-year file is missing")
	}
}

func TestCondition(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	cases := []struct {
		ww, n *float64
		want  weather.Condition
	}{
		{f(45), f(0), weather.ConditionFog},
		{f(10), nil, weather.ConditionFog},
		{f(53), nil, weather.ConditionDrizzle},
		{f(56), nil, weather.ConditionFreezingRain},
		{f(68), nil, weather.ConditionSleet},
		{f(71), nil, weather.ConditionSnow},
		{f(81), nil, weather.ConditionRain},
		{f(95), nil, weather.ConditionThunderstorm},
		{f(0), f(10), weather.ConditionClear},
		{f(2), f(50), weather.ConditionPartlyCloudy},
		{f(25), f(100), weather.ConditionCloudy},
		{nil, f(113), weather.ConditionCloudy},
		{f(0), nil, weather.ConditionUnknown},
		{nil, nil, weather.ConditionUnknown},
	}
	for _, c := range cases {
		if got, _ := condition(c.ww, c.n); got != c.want {
			t.Errorf("condition(%v, %v) = %q, want %q", c.ww, c.n, got, c.want)
		}
	}
}
