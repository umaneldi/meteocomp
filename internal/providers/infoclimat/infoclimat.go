// Package infoclimat implements the Infoclimat observation provider
// (https://www.infoclimat.fr/opendata/). Requires a token.
//
// Options:
//   - station_id (string, required): Infoclimat station ID, e.g. "07156".
//   - past_days (int, default 7, max 30): days of past observations to fetch per poll.
//
// All returned data points have kind=observation. One provider instance corresponds
// to one station; configure multiple instances to cover several locations.
package infoclimat

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/yanndethe/meteocomp/internal/config"
	"github.com/yanndethe/meteocomp/internal/providers"
	"github.com/yanndethe/meteocomp/internal/weather"
)

const (
	Type            = "infoclimat"
	defaultBaseURL  = "https://www.infoclimat.fr/opendata/"
	defaultPastDays = 7
	maxPastDays     = 30
)

func init() {
	providers.Register(Type, New, providers.Info{
		Description:    "Infoclimat hourly weather station observations. Requires a token.",
		RequiresAPIKey: true,
		SignupURL:      "https://www.infoclimat.fr/opendata/",
		Options: []providers.Option{
			{Name: "station_id", Kind: "string", Description: `Infoclimat station ID (e.g. "07156")`},
			{Name: "past_days", Kind: "int", Default: "7", Description: "Days of past observations to fetch (1-30, default 7)"},
		},
	})
}

// Provider fetches hourly weather station observations from the Infoclimat opendata API.
type Provider struct {
	name      string
	token     string
	baseURL   string
	stationID string
	pastDays  int
	client    *http.Client
}

func New(cfg config.ProviderConfig, client *http.Client) (weather.Provider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("provider %q: api_key (token) is required", cfg.Name)
	}
	stationID := cfg.StringOption("station_id", "")
	if stationID == "" {
		return nil, fmt.Errorf("provider %q: station_id option is required", cfg.Name)
	}
	pastDays := cfg.IntOption("past_days", defaultPastDays)
	if pastDays < 1 || pastDays > maxPastDays {
		return nil, fmt.Errorf("provider %q: past_days must be between 1 and %d", cfg.Name, maxPastDays)
	}
	base := cfg.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	return &Provider{
		name:      cfg.Name,
		token:     cfg.APIKey,
		baseURL:   base,
		stationID: stationID,
		pastDays:  pastDays,
		client:    client,
	}, nil
}

func (p *Provider) Name() string { return p.name }
func (p *Provider) Type() string { return Type }

// apiResponse is the top-level Infoclimat opendata v2 response.
// The data map is keyed by station ID, then by timestamp string.
type apiResponse struct {
	Data map[string]map[string]rawReading `json:"data"`
}

// rawReading holds one hour's observed data. All fields are nullable.
// ww is decoded as *float64 because the API may encode it as a JSON number with a decimal point.
type rawReading struct {
	Tc      *float64 `json:"tc"`      // temperature °C
	U       *float64 `json:"u"`       // relative humidity %
	Pmer    *float64 `json:"pmer"`    // sea-level pressure hPa
	Ff      *float64 `json:"ff"`      // wind speed m/s
	Dd      *float64 `json:"dd"`      // wind direction degrees
	Rr1     *float64 `json:"rr1"`     // precipitation last hour mm
	Td      *float64 `json:"td"`      // dew point °C
	Vv      *float64 `json:"vv"`      // visibility meters
	Ww      *float64 `json:"ww"`      // WMO present weather code
	Rafales *float64 `json:"rafales"` // wind gusts m/s
}

func (p *Provider) Fetch(ctx context.Context, loc weather.Location) ([]weather.Forecast, error) {
	now := time.Now().UTC()
	start := now.AddDate(0, 0, -p.pastDays).Format("2006-01-02")
	end := now.Format("2006-01-02")

	q := url.Values{}
	q.Set("version", "2")
	q.Set("method", "get")
	q.Set("format", "json")
	q.Set("start", start)
	q.Set("end", end)
	q.Set("token", p.token)

	// Build the URL with raw (un-encoded) brackets for the stations[] parameter,
	// which is what the Infoclimat API expects.
	rawURL := p.baseURL + "?" + q.Encode() + "&stations[]=" + url.QueryEscape(p.stationID)

	var r apiResponse
	if err := providers.GetJSON(ctx, p.client, rawURL, &r); err != nil {
		return nil, err
	}

	stationData, ok := r.Data[p.stationID]
	if !ok {
		return nil, fmt.Errorf("provider %q: station %q not found in response", p.name, p.stationID)
	}

	timestamps := make([]string, 0, len(stationData))
	for ts := range stationData {
		timestamps = append(timestamps, ts)
	}
	sort.Strings(timestamps)

	out := make([]weather.Forecast, 0, len(timestamps))
	for _, ts := range timestamps {
		t, err := time.ParseInLocation("2006-01-02 15:04:05", ts, time.UTC)
		if err != nil {
			return nil, fmt.Errorf("provider %q: parse timestamp %q: %w", p.name, ts, err)
		}
		if !t.Before(now) {
			continue
		}
		rd := stationData[ts]
		f := weather.Forecast{
			Kind:             weather.KindObservation,
			ForecastTime:     t,
			Granularity:      weather.GranularityHourly,
			TemperatureC:     rd.Tc,
			HumidityPct:      rd.U,
			PressureHPa:      rd.Pmer,
			WindDirectionDeg: rd.Dd,
			PrecipitationMm:  rd.Rr1,
			DewPointC:        rd.Td,
		}
		if rd.Ff != nil {
			v := *rd.Ff * 3.6
			f.WindSpeedKmh = &v
		}
		if rd.Rafales != nil {
			v := *rd.Rafales * 3.6
			f.WindGustKmh = &v
		}
		if rd.Vv != nil {
			v := *rd.Vv / 1000.0
			f.VisibilityKm = &v
		}
		setCondition(&f, rd.Ww)
		out = append(out, f)
	}
	return out, nil
}

func setCondition(f *weather.Forecast, ww *float64) {
	if ww == nil {
		f.Condition = weather.ConditionUnknown
		return
	}
	code := int(*ww)
	f.ConditionCode = strconv.Itoa(code)
	f.Condition, f.ConditionText = wmoSynoptic(code)
}

// wmoSynoptic maps WMO synoptic present weather codes (0–99) to normalized conditions.
func wmoSynoptic(code int) (weather.Condition, string) {
	switch {
	case code == 0:
		return weather.ConditionClear, "Clear sky"
	case code == 1:
		return weather.ConditionClear, "Mainly clear"
	case code == 2:
		return weather.ConditionPartlyCloudy, "Partly cloudy"
	case code == 3:
		return weather.ConditionCloudy, "Overcast"
	case code >= 4 && code <= 9:
		return weather.ConditionFog, "Reduced visibility (smoke/haze/dust)"
	case code >= 10 && code <= 19:
		return weather.ConditionPartlyCloudy, "Distant precipitation or past weather"
	case code >= 20 && code <= 29:
		return weather.ConditionPartlyCloudy, "Past weather phenomena"
	case code >= 30 && code <= 39:
		return weather.ConditionFog, "Dust/sand storm"
	case code >= 40 && code <= 49:
		return weather.ConditionFog, "Fog"
	case code >= 50 && code <= 59:
		return weather.ConditionDrizzle, "Drizzle"
	case code >= 60 && code <= 69:
		return weather.ConditionRain, "Rain"
	case code >= 70 && code <= 79:
		return weather.ConditionSnow, "Snow"
	case code >= 80 && code <= 84:
		return weather.ConditionRain, "Rain showers"
	case code >= 85 && code <= 89:
		return weather.ConditionSnow, "Snow showers"
	case code >= 90 && code <= 99:
		return weather.ConditionThunderstorm, "Thunderstorm"
	}
	return weather.ConditionUnknown, ""
}
