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
		Description:    "Infoclimat hourly weather station observations. Requires a token. Each location must have a station_id configured.",
		RequiresAPIKey: true,
		SignupURL:      "https://www.infoclimat.fr/opendata/",
		Options: []providers.Option{
			{Name: "past_days", Kind: "int", Default: "7", Description: "Days of past observations to fetch (1-30, default 7)"},
		},
	})
}

// Provider fetches hourly weather station observations from the Infoclimat opendata API.
// The station to query is taken from each location's StationID field, not from provider options.
type Provider struct {
	name     string
	token    string
	baseURL  string
	pastDays int
	client   *http.Client
}

func New(cfg config.ProviderConfig, client *http.Client) (weather.Provider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("provider %q: api_key (token) is required", cfg.Name)
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
		name:     cfg.Name,
		token:    cfg.APIKey,
		baseURL:  base,
		pastDays: pastDays,
		client:   client,
	}, nil
}

func (p *Provider) Name() string { return p.name }
func (p *Provider) Type() string { return Type }

// apiResponse is the top-level Infoclimat opendata v2 response.
// Observations are in the "hourly" field, keyed by station ID.
type apiResponse struct {
	Hourly map[string][]rawReading `json:"hourly"`
}

// rawReading holds one observation record. All measurement fields are nullable.
// Wind speeds are already in km/h; visibility is in meters.
type rawReading struct {
	DhUTC          string   `json:"dh_utc"`
	Temperature    *float64 `json:"temperature"`    // °C
	TemperatureMin *float64 `json:"temperature_min"` // °C
	TemperatureMax *float64 `json:"temperature_max"` // °C
	Humidite       *float64 `json:"humidite"`        // %
	PointDeRosee   *float64 `json:"point_de_rosee"`  // °C
	Pression       *float64 `json:"pression"`        // hPa
	VentMoyen      *float64 `json:"vent_moyen"`      // km/h
	VentRafales    *float64 `json:"vent_rafales"`    // km/h
	VentDirection  *float64 `json:"vent_direction"`  // degrees
	Pluie1h        *float64 `json:"pluie_1h"`        // mm
	Visibilite     *float64 `json:"visibilite"`      // meters
	TempsOmm       *float64 `json:"temps_omm"`       // WMO present weather code
	UVIndex        *float64 `json:"uv_index"`
}

func (p *Provider) Fetch(ctx context.Context, loc weather.Location) ([]weather.Forecast, error) {
	if loc.StationID == "" {
		return nil, fmt.Errorf("provider %q: location %q has no station_id configured", p.name, loc.Name)
	}

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
	rawURL := p.baseURL + "?" + q.Encode() + "&stations[]=" + url.QueryEscape(loc.StationID)

	var r apiResponse
	if err := providers.GetJSON(ctx, p.client, rawURL, &r); err != nil {
		return nil, err
	}

	stationReadings, ok := r.Hourly[loc.StationID]
	if !ok {
		return nil, fmt.Errorf("provider %q: station %q not found in response", p.name, loc.StationID)
	}

	out := make([]weather.Forecast, 0, len(stationReadings))
	for _, rd := range stationReadings {
		t, err := time.ParseInLocation("2006-01-02 15:04:05", rd.DhUTC, time.UTC)
		if err != nil {
			return nil, fmt.Errorf("provider %q: parse timestamp %q: %w", p.name, rd.DhUTC, err)
		}
		if !t.Before(now) {
			continue
		}
		// Keep only top-of-hour readings to align with hourly forecast granularity.
		if t.Minute() != 0 {
			continue
		}
		f := weather.Forecast{
			Kind:             weather.KindObservation,
			ForecastTime:     t,
			Granularity:      weather.GranularityHourly,
			TemperatureC:     rd.Temperature,
			TemperatureMinC:  rd.TemperatureMin,
			TemperatureMaxC:  rd.TemperatureMax,
			HumidityPct:      rd.Humidite,
			PressureHPa:      rd.Pression,
			WindSpeedKmh:     rd.VentMoyen,
			WindGustKmh:      rd.VentRafales,
			WindDirectionDeg: rd.VentDirection,
			PrecipitationMm:  rd.Pluie1h,
			DewPointC:        rd.PointDeRosee,
			UVIndex:          rd.UVIndex,
		}
		if rd.Visibilite != nil {
			v := *rd.Visibilite / 1000.0
			f.VisibilityKm = &v
		}
		setCondition(&f, rd.TempsOmm)
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
