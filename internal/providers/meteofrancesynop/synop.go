// Package meteofrancesynop implements the Météo-France SYNOP observation source,
// reading the WMO SYNOP archive published on data.gouv.fr ("archive-synop-omm").
// No API key is required.
//
// The archive is one gzipped CSV per year, refreshed once a day with data up to the
// previous day, one row per station every 3 hours. Its readings are never stored:
// the poller uses them to fill the condition of stored observations lacking one.
// The condition is the observed present weather (WMO code 4677), falling back to
// the observed total cloud cover when no significant weather is reported.
//
// Each location uses its station_id if it is a SYNOP station, otherwise the nearest
// SYNOP station within max_distance_km. The lookback matches the global forecast_days.
package meteofrancesynop

import (
	"compress/gzip"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yanndethe/meteocomp/internal/config"
	"github.com/yanndethe/meteocomp/internal/providers"
	"github.com/yanndethe/meteocomp/internal/weather"
)

const (
	Type                 = "meteofrance_synop"
	defaultBaseURL       = "https://meteofrance.s3.sbg.io.cloud.ovh.net/data/OBS/SYNOP"
	defaultMaxDistanceKm = 30
)

func init() {
	providers.Register(Type, New, providers.Info{
		Description: "Météo-France SYNOP observed weather. Stores no readings: fills the condition of " +
			"stored observations that lack one. No API key. Updated daily up to the previous day.",
		Options: []providers.Option{
			{Name: "max_distance_km", Kind: "int", Default: strconv.Itoa(defaultMaxDistanceKm),
				Description: "Locations whose station_id is not a SYNOP station use the nearest SYNOP station within this distance"},
		},
	})
}

type Provider struct {
	name          string
	baseURL       string
	pastDays      int
	maxDistanceKm float64
	client        *http.Client
	now           func() time.Time

	mu    sync.Mutex
	files map[string]*yearFile // URL -> parsed content
}

// yearFile is the parsed content of one yearly archive, kept to answer polls
// while the file is unchanged.
type yearFile struct {
	lastModified string
	stations     map[string]station
	obs          map[string]map[time.Time]weather.Forecast // station ID -> validity time -> reading
}

func New(cfg config.ProviderConfig, client *http.Client) (weather.Provider, error) {
	base := cfg.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	maxDist := cfg.IntOption("max_distance_km", defaultMaxDistanceKm)
	if maxDist < 0 {
		return nil, fmt.Errorf("provider %q: max_distance_km must not be negative", cfg.Name)
	}
	return &Provider{
		name:          cfg.Name,
		baseURL:       strings.TrimSuffix(base, "/"),
		pastDays:      cfg.IntOption("forecast_days", 7),
		maxDistanceKm: float64(maxDist),
		client:        client,
		now:           time.Now,
		files:         map[string]*yearFile{},
	}, nil
}

func (p *Provider) Name() string { return p.name }
func (p *Provider) Type() string { return Type }

// FillsConditions marks SYNOP as a weather.ConditionSource: its readings are not stored.
func (p *Provider) FillsConditions() {}

func (p *Provider) Fetch(ctx context.Context, loc weather.Location) ([]weather.Forecast, error) {
	results, err := p.FetchAll(ctx, []weather.Location{loc})
	if err != nil {
		return nil, err
	}
	lr := results[loc.Name]
	return lr.Forecasts, lr.Err
}

type station struct {
	id, name string
	lat, lon float64
}

// FetchAll returns every observation within the lookback window for each location.
// A yearly file unchanged since the previous poll is not downloaded again.
func (p *Provider) FetchAll(ctx context.Context, locs []weather.Location) (map[string]weather.LocationResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now().UTC()
	start := now.AddDate(0, 0, -p.pastDays)

	stations := map[string]station{}
	obs := map[string][]weather.Forecast{}
	files := map[string]*yearFile{}
	for year := start.Year(); year <= now.Year(); year++ {
		url := fmt.Sprintf("%s/synop_%d.csv.gz", p.baseURL, year)
		f, err := p.loadYear(ctx, url, year < now.Year(), start)
		if err != nil {
			return nil, err
		}
		if f == nil {
			continue
		}
		files[url] = f
		for id, byTime := range f.obs {
			for t, fc := range byTime {
				if !t.Before(start) && t.Before(now) {
					obs[id] = append(obs[id], fc)
					stations[id] = f.stations[id]
				}
			}
		}
	}
	p.files = files

	results := make(map[string]weather.LocationResult, len(locs))
	for _, loc := range locs {
		st, err := p.resolve(loc, stations)
		if err != nil {
			results[loc.Name] = weather.LocationResult{Err: fmt.Errorf("provider %q: %w", p.name, err)}
			continue
		}
		out := slices.Clone(obs[st.id])
		slices.SortFunc(out, func(a, b weather.Forecast) int { return a.ForecastTime.Compare(b.ForecastTime) })
		results[loc.Name] = weather.LocationResult{Forecasts: out}
	}
	return results, nil
}

// loadYear returns the parsed content of url, reusing the cached copy when the server
// reports it unchanged. A missing past-year file yields nil.
func (p *Provider) loadYear(ctx context.Context, url string, pastYear bool, start time.Time) (*yearFile, error) {
	cached := p.files[url]
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "meteocomp/0.1")
	if cached != nil && cached.lastModified != "" {
		req.Header.Set("If-Modified-Since", cached.lastModified)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified && cached != nil {
		return cached, nil
	}
	if resp.StatusCode == http.StatusNotFound && pastYear {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, body)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	f := &yearFile{
		lastModified: resp.Header.Get("Last-Modified"),
		stations:     map[string]station{},
		obs:          map[string]map[time.Time]weather.Forecast{},
	}
	if err := parseCSV(gz, start, f); err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	return f, nil
}

// parseCSV adds the rows at or after start to f.
func parseCSV(r io.Reader, start time.Time, f *yearFile) error {
	cr := csv.NewReader(r)
	cr.Comma = ';'
	cr.FieldsPerRecord = -1
	cr.ReuseRecord = true
	header, err := cr.Read()
	if err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.TrimPrefix(strings.TrimSpace(h), "\ufeff")] = i
	}
	for _, name := range []string{"lat", "lon", "geo_id_wmo", "validity_time"} {
		if _, ok := col[name]; !ok {
			return fmt.Errorf("missing column %q", name)
		}
	}
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		field := func(name string) string {
			if i, ok := col[name]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		t, err := time.Parse(time.RFC3339, field("validity_time"))
		if err != nil || t.Before(start) {
			continue
		}
		id := field("geo_id_wmo")
		lat, latErr := strconv.ParseFloat(field("lat"), 64)
		lon, lonErr := strconv.ParseFloat(field("lon"), 64)
		if id == "" || latErr != nil || lonErr != nil {
			continue
		}
		f.stations[id] = station{id: id, name: field("name"), lat: lat, lon: lon}
		num := func(name string) *float64 { return parseNum(field(name)) }
		if f.obs[id] == nil {
			f.obs[id] = map[time.Time]weather.Forecast{}
		}
		f.obs[id][t] = toForecast(t, num)
	}
}

func parseNum(s string) *float64 {
	if s == "" || s == "mq" {
		return nil
	}
	x, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &x
}

func scale(v *float64, factor, offset float64) *float64 {
	if v == nil {
		return nil
	}
	x := math.Round((*v*factor+offset)*100) / 100
	return &x
}

func toForecast(t time.Time, num func(string) *float64) weather.Forecast {
	f := weather.Forecast{
		Kind:             weather.KindObservation,
		ForecastTime:     t,
		TemperatureC:     scale(num("t"), 1, -273.15),
		DewPointC:        scale(num("td"), 1, -273.15),
		HumidityPct:      num("u"),
		PressureHPa:      scale(num("pmer"), 0.01, 0),
		WindSpeedKmh:     scale(num("ff"), 3.6, 0),
		WindGustKmh:      scale(num("raf10"), 3.6, 0),
		WindDirectionDeg: num("dd"),
		CloudCoverPct:    num("n"),
		VisibilityKm:     scale(num("vv"), 0.001, 0),
	}
	f.Condition, f.ConditionText = condition(num("ww"), f.CloudCoverPct)
	return f
}

// resolve returns the location's SYNOP station: its station_id if present in the data,
// otherwise the nearest station within maxDistanceKm.
func (p *Provider) resolve(loc weather.Location, stations map[string]station) (station, error) {
	if st, ok := stations[loc.StationID]; ok {
		return st, nil
	}
	var best station
	bestDist := math.Inf(1)
	for _, st := range stations {
		if d := haversineKm(loc.Latitude, loc.Longitude, st.lat, st.lon); d < bestDist {
			best, bestDist = st, d
		}
	}
	if bestDist > p.maxDistanceKm {
		return station{}, fmt.Errorf("location %q: no SYNOP station within %.0f km", loc.Name, p.maxDistanceKm)
	}
	return best, nil
}

func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadiusKm = 6371.0
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLon := (lon2 - lon1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadiusKm * math.Asin(math.Sqrt(a))
}

// condition maps the WMO 4677 present weather code ww to a condition. Codes without
// current-weather meaning (or a missing code) fall back to the total cloud cover n (%).
func condition(ww, n *float64) (weather.Condition, string) {
	if ww != nil {
		if c, text, ok := presentWeather(int(*ww)); ok {
			return c, text
		}
	}
	if n == nil {
		return weather.ConditionUnknown, ""
	}
	switch {
	case *n <= 25:
		return weather.ConditionClear, "Clear sky"
	case *n <= 75:
		return weather.ConditionPartlyCloudy, "Partly cloudy"
	default:
		return weather.ConditionCloudy, "Overcast"
	}
}

func presentWeather(code int) (weather.Condition, string, bool) {
	switch {
	case code >= 4 && code <= 9:
		return weather.ConditionFog, "Haze, smoke or dust", true
	case code == 10:
		return weather.ConditionFog, "Mist", true
	case code == 11 || code == 12:
		return weather.ConditionFog, "Shallow fog", true
	case code == 13:
		return weather.ConditionThunderstorm, "Lightning, no thunder", true
	case code == 17:
		return weather.ConditionThunderstorm, "Thunder, no precipitation", true
	case code >= 30 && code <= 35:
		return weather.ConditionFog, "Dust or sandstorm", true
	case code >= 36 && code <= 39:
		return weather.ConditionSnow, "Blowing snow", true
	case code >= 40 && code <= 49:
		return weather.ConditionFog, "Fog", true
	case code >= 50 && code <= 55:
		return weather.ConditionDrizzle, "Drizzle", true
	case code == 56 || code == 57:
		return weather.ConditionFreezingRain, "Freezing drizzle", true
	case code == 58 || code == 59:
		return weather.ConditionRain, "Drizzle and rain", true
	case code >= 60 && code <= 65:
		return weather.ConditionRain, "Rain", true
	case code == 66 || code == 67:
		return weather.ConditionFreezingRain, "Freezing rain", true
	case code == 68 || code == 69:
		return weather.ConditionSleet, "Rain and snow", true
	case code >= 70 && code <= 75:
		return weather.ConditionSnow, "Snow", true
	case code >= 76 && code <= 78:
		return weather.ConditionSnow, "Snow grains or ice crystals", true
	case code == 79:
		return weather.ConditionSleet, "Ice pellets", true
	case code >= 80 && code <= 82:
		return weather.ConditionRain, "Rain showers", true
	case code == 83 || code == 84:
		return weather.ConditionSleet, "Rain and snow showers", true
	case code == 85 || code == 86:
		return weather.ConditionSnow, "Snow showers", true
	case code >= 87 && code <= 90:
		return weather.ConditionSleet, "Hail or snow pellet showers", true
	case code == 91 || code == 92:
		return weather.ConditionRain, "Rain after recent thunderstorm", true
	case code == 93 || code == 94:
		return weather.ConditionSnow, "Snow after recent thunderstorm", true
	case code >= 95 && code <= 99:
		return weather.ConditionThunderstorm, "Thunderstorm", true
	}
	return "", "", false
}
