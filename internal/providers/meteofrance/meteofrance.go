// Package meteofrance implements a provider for the Météo-France AROME model
// through the official API portal (https://portail-api.meteofrance.fr), using
// the OGC WCS 2.0.1 interface of the "AROME" API. An API key subscribed to
// that API is required; it is sent in the "apikey" header.
//
// The WCS service only returns one value per request (one variable, one time,
// one point), so a poll issues roughly 11 requests per time step and location.
// Requests are rate limited (portal quota: 50 requests/minute).
//
// The forecast comes from the newest complete AROME run listed by
// GetCapabilities. Each data point has ForecastTime = valid time; instantaneous
// fields are taken at that time, precipitation, snowfall and maximum gust are
// accumulated over the step ending at that time, and visibility is the
// minimum over the hour ending at that time.
//
// There is no weather code in the model output: Condition is derived from
// precipitation, snow fraction, visibility and cloud cover, and
// ConditionText is "derived from model fields".
//
// Options:
//   - horizon_hours (int, default 48, max 51): how far ahead to fetch, from now
//   - step_hours (int, 1 or 3, default 3): spacing of the data points
//   - requests_per_minute (int, default 45, max 50)
//   - grid (string, "0025" or "001", default "0025"): 0.025° or 0.01° grid
package meteofrance

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yanndethe/meteocomp/internal/config"
	"github.com/yanndethe/meteocomp/internal/providers"
	"github.com/yanndethe/meteocomp/internal/weather"
)

const (
	Type           = "meteofrance"
	defaultBaseURL = "https://public-api.meteofrance.fr/public/arome/1.0"

	runLayout   = "2006-01-02T15.04.05Z"
	capsTTL     = 15 * time.Minute
	maxHorizonH = 51
)

// AROME France domain (CRS:84).
const (
	minLon, maxLon = -12.0, 16.0
	minLat, maxLat = 37.5, 55.4
)

func init() { providers.Register(Type, New) }

type Provider struct {
	name     string
	apiKey   string
	wcsURL   string
	horizon  int
	step     int
	interval time.Duration // minimum spacing between requests
	client   *http.Client
	now      func() time.Time

	rateMu  sync.Mutex
	nextReq time.Time

	capsMu   sync.Mutex
	capsRun  time.Time
	capsTime time.Time
}

func New(cfg config.ProviderConfig, client *http.Client) (weather.Provider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("provider %q: api_key is required", cfg.Name)
	}
	p := &Provider{
		name:    cfg.Name,
		apiKey:  cfg.APIKey,
		horizon: cfg.IntOption("horizon_hours", 48),
		step:    cfg.IntOption("step_hours", 3),
		client:  client,
		now:     time.Now,
	}
	base := cfg.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	grid, _ := cfg.Options["grid"].(string)
	if grid == "" {
		grid = "0025"
	}
	if grid != "0025" && grid != "001" {
		return nil, fmt.Errorf("provider %q: grid must be \"0025\" or \"001\"", cfg.Name)
	}
	p.wcsURL = strings.TrimSuffix(base, "/") + "/wcs/MF-NWP-HIGHRES-AROME-" + grid + "-FRANCE-WCS"
	if p.horizon < 1 || p.horizon > maxHorizonH {
		return nil, fmt.Errorf("provider %q: horizon_hours must be between 1 and %d", cfg.Name, maxHorizonH)
	}
	if p.step != 1 && p.step != 3 {
		return nil, fmt.Errorf("provider %q: step_hours must be 1 or 3", cfg.Name)
	}
	rpm := cfg.IntOption("requests_per_minute", 45)
	if rpm < 1 || rpm > 50 {
		return nil, fmt.Errorf("provider %q: requests_per_minute must be between 1 and 50", cfg.Name)
	}
	p.interval = time.Minute / time.Duration(rpm)
	return p, nil
}

func (p *Provider) Name() string { return p.name }
func (p *Provider) Type() string { return Type }

// FetchTimeout tells the poller how long a single location fetch may take,
// derived from the number of requests and the rate limit.
func (p *Provider) FetchTimeout() time.Duration {
	steps := p.horizon/p.step + 1
	return time.Duration(steps*len(variables)+2)*p.interval*2 + time.Minute
}

// variable is one WCS coverage queried per time step.
type variable struct {
	coverage    string // coverage ID prefix, before "___<run>"
	height      int    // value for subset=height(...); 0 when the coverage has no height axis
	accumulated bool   // coverage ID carries a "_PT<step>H" accumulation suffix
	set         func(f *weather.Forecast, v float64)
}

// wind holds the U/V components until both are known.
type wind struct{ u, v *float64 }

var variables = []variable{
	{"TEMPERATURE__SPECIFIC_HEIGHT_LEVEL_ABOVE_GROUND", 2, false, func(f *weather.Forecast, v float64) { f.TemperatureC = new(v - 273.15) }},
	{"RELATIVE_HUMIDITY__SPECIFIC_HEIGHT_LEVEL_ABOVE_GROUND", 2, false, func(f *weather.Forecast, v float64) { f.HumidityPct = new(pct(v)) }},
	{"DEW_POINT_TEMPERATURE__SPECIFIC_HEIGHT_LEVEL_ABOVE_GROUND", 2, false, func(f *weather.Forecast, v float64) { f.DewPointC = new(v - 273.15) }},
	{"PRESSURE__MEAN_SEA_LEVEL", 0, false, func(f *weather.Forecast, v float64) { f.PressureHPa = new(v / 100) }},
	{"TOTAL_CLOUD_COVER__GROUND_OR_WATER_SURFACE", 0, false, func(f *weather.Forecast, v float64) { f.CloudCoverPct = new(pct(v)) }},
	{"U_COMPONENT_OF_WIND__SPECIFIC_HEIGHT_LEVEL_ABOVE_GROUND", 10, false, nil}, // handled via wind
	{"V_COMPONENT_OF_WIND__SPECIFIC_HEIGHT_LEVEL_ABOVE_GROUND", 10, false, nil}, // handled via wind
	{"WIND_SPEED_GUST_MAX__SPECIFIC_HEIGHT_LEVEL_ABOVE_GROUND", 10, true, func(f *weather.Forecast, v float64) { f.WindGustKmh = new(v * 3.6) }},
	{"TOTAL_PRECIPITATION__GROUND_OR_WATER_SURFACE", 0, true, func(f *weather.Forecast, v float64) { f.PrecipitationMm = new(v) }},
	// Snow water equivalent in mm; converted to snow depth with the usual 10:1 ratio.
	{"TOTAL_SNOW_PRECIPITATION__GROUND_OR_WATER_SURFACE", 0, true, func(f *weather.Forecast, v float64) { f.SnowfallCm = new(v) }},
	{"VISIBILITY_MINI_60MIN__GROUND_OR_WATER_SURFACE", 0, false, func(f *weather.Forecast, v float64) { f.VisibilityKm = new(v / 1000) }},
}

func (p *Provider) Fetch(ctx context.Context, loc weather.Location) ([]weather.Forecast, error) {
	if loc.Longitude < minLon || loc.Longitude > maxLon || loc.Latitude < minLat || loc.Latitude > maxLat {
		return nil, fmt.Errorf("location %q is outside the AROME France domain", loc.Name)
	}
	run, err := p.latestRun(ctx)
	if err != nil {
		return nil, err
	}
	runID := run.Format(runLayout)

	// Valid times: aligned on the step from the run start, from now to now+horizon,
	// capped by the run's own horizon.
	now := p.now().UTC()
	end := now.Add(time.Duration(p.horizon) * time.Hour)
	if runEnd := run.Add(maxHorizonH * time.Hour); end.After(runEnd) {
		end = runEnd
	}
	stepDur := time.Duration(p.step) * time.Hour
	first := run.Add(stepDur) // accumulations need at least one full step after the run start
	if now.After(first) {
		first = run.Add(now.Sub(run).Truncate(stepDur))
	}

	granularity := weather.GranularityThreeHours
	if p.step == 1 {
		granularity = weather.GranularityHourly
	}
	accSuffix := fmt.Sprintf("_PT%dH", p.step)

	var out []weather.Forecast
	for t := first; !t.After(end); t = t.Add(stepDur) {
		f := weather.Forecast{ForecastTime: t, Granularity: granularity}
		var w wind
		got := 0
		for _, v := range variables {
			id := v.coverage + "___" + runID
			if v.accumulated {
				id += accSuffix
			}
			val, err := p.point(ctx, id, loc, t, v.height)
			if errors.Is(err, errNoData) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("%s at %s: %w", v.coverage, t.Format(time.RFC3339), err)
			}
			got++
			switch {
			case strings.HasPrefix(v.coverage, "U_COMPONENT"):
				w.u = &val
			case strings.HasPrefix(v.coverage, "V_COMPONENT"):
				w.v = &val
			default:
				v.set(&f, val)
			}
		}
		if got == 0 {
			continue
		}
		if w.u != nil && w.v != nil {
			f.WindSpeedKmh = new(math.Hypot(*w.u, *w.v) * 3.6)
			// Meteorological convention: direction the wind blows from, 0° = north.
			f.WindDirectionDeg = new(math.Mod(math.Atan2(-*w.u, -*w.v)*180/math.Pi+360, 360))
		}
		f.Condition = deriveCondition(f)
		f.ConditionText = "derived from model fields"
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no data returned for run %s", runID)
	}
	return out, nil
}

// deriveCondition approximates a sky/precipitation condition from model fields.
func deriveCondition(f weather.Forecast) weather.Condition {
	precip := deref(f.PrecipitationMm)
	if precip >= 0.1 {
		snow := deref(f.SnowfallCm)
		switch ratio := snow / precip; {
		case ratio >= 0.8:
			return weather.ConditionSnow
		case ratio >= 0.2:
			return weather.ConditionSleet
		}
		if precip < 0.5*float64(granularityHours(f.Granularity)) {
			return weather.ConditionDrizzle
		}
		return weather.ConditionRain
	}
	if f.VisibilityKm != nil && *f.VisibilityKm < 1 {
		return weather.ConditionFog
	}
	if f.CloudCoverPct == nil {
		return weather.ConditionUnknown
	}
	switch c := *f.CloudCoverPct; {
	case c < 20:
		return weather.ConditionClear
	case c < 70:
		return weather.ConditionPartlyCloudy
	}
	return weather.ConditionCloudy
}

func granularityHours(g weather.Granularity) int {
	if g == weather.GranularityThreeHours {
		return 3
	}
	return 1
}

// pct clamps model percentages, which can slightly overshoot (e.g. 100.009).
func pct(v float64) float64 { return min(max(v, 0), 100) }

func deref(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

// errNoData means the coverage has no value for the requested point/time.
var errNoData = errors.New("no data")

var coverageIDRe = regexp.MustCompile(`<wcs:CoverageId>([A-Z0-9_]+)___(\d{4}-\d\d-\d\dT\d\d\.\d\d\.\d\dZ)(?:_[A-Z0-9]+)?</wcs:CoverageId>`)

// latestRun returns the newest run for which all coverages are published.
// The result is cached for capsTTL because GetCapabilities is several MB.
func (p *Provider) latestRun(ctx context.Context) (time.Time, error) {
	p.capsMu.Lock()
	defer p.capsMu.Unlock()
	if !p.capsRun.IsZero() && p.now().Sub(p.capsTime) < capsTTL {
		return p.capsRun, nil
	}

	q := url.Values{"service": {"WCS"}, "version": {"2.0.1"}, "language": {"eng"}}
	body, err := p.get(ctx, p.wcsURL+"/GetCapabilities?"+q.Encode())
	if err != nil {
		return time.Time{}, fmt.Errorf("GetCapabilities: %w", err)
	}

	counts := map[string]int{}
	for _, m := range coverageIDRe.FindAllSubmatch(body, -1) {
		counts[string(m[2])]++
	}
	if len(counts) == 0 {
		return time.Time{}, fmt.Errorf("GetCapabilities: no AROME runs listed")
	}
	full := 0
	for _, c := range counts {
		full = max(full, c)
	}
	runs := make([]string, 0, len(counts))
	for r := range counts {
		runs = append(runs, r)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(runs)))
	for _, r := range runs {
		if counts[r] == full {
			t, err := time.Parse(runLayout, r)
			if err != nil {
				return time.Time{}, err
			}
			p.capsRun, p.capsTime = t, p.now()
			return t, nil
		}
	}
	panic("unreachable")
}

type pointCoverage struct {
	TupleList string `xml:"rangeSet>DataBlock>tupleList"`
}

// point returns the value of a coverage at loc and time t.
func (p *Provider) point(ctx context.Context, coverageID string, loc weather.Location, t time.Time, height int) (float64, error) {
	q := url.Values{
		"service":    {"WCS"},
		"version":    {"2.0.1"},
		"coverageid": {coverageID},
		"format":     {"application/wmo-grib"},
	}
	q.Add("subset", "time("+t.UTC().Format(time.RFC3339)+")")
	q.Add("subset", "lat("+strconv.FormatFloat(loc.Latitude, 'f', -1, 64)+")")
	q.Add("subset", "long("+strconv.FormatFloat(loc.Longitude, 'f', -1, 64)+")")
	if height > 0 {
		q.Add("subset", "height("+strconv.Itoa(height)+")")
	}
	body, err := p.get(ctx, p.wcsURL+"/GetCoverage?"+q.Encode())
	if err != nil {
		return 0, err
	}
	var pc pointCoverage
	if err := xml.Unmarshal(body, &pc); err != nil {
		return 0, fmt.Errorf("decode coverage: %w", err)
	}
	fields := strings.Fields(pc.TupleList)
	if len(fields) == 0 {
		return 0, errNoData
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || math.IsNaN(v) || math.Abs(v) > 1e20 { // huge values are GRIB "missing"
		return 0, errNoData
	}
	return v, nil
}

// statusError is a non-2xx HTTP response.
type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string { return fmt.Sprintf("http %d: %s", e.code, e.body) }

// get performs a rate-limited GET. HTTP 429 is retried after a pause; HTTP 404
// (coverage or time step not available) is reported as errNoData.
func (p *Provider) get(ctx context.Context, u string) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		if err := p.wait(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("apikey", p.apiKey)
		req.Header.Set("User-Agent", "meteocomp/0.1")
		resp, err := p.client.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		switch {
		case resp.StatusCode == http.StatusTooManyRequests && attempt < 3:
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 20 * time.Second):
			}
			continue
		case resp.StatusCode == http.StatusNotFound:
			return nil, errNoData
		case resp.StatusCode < 200 || resp.StatusCode > 299:
			return nil, &statusError{resp.StatusCode, string(bytes.TrimSpace(body[:min(len(body), 300)]))}
		}
		return body, nil
	}
}

// wait blocks until the next request slot.
func (p *Provider) wait(ctx context.Context) error {
	p.rateMu.Lock()
	now := time.Now()
	slot := p.nextReq
	if slot.Before(now) {
		slot = now
	}
	p.nextReq = slot.Add(p.interval)
	p.rateMu.Unlock()

	if d := time.Until(slot); d > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}
	return nil
}
