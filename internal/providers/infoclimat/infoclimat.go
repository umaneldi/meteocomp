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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
// Observations are in the "hourly" field, keyed by station ID. The same object also
// carries metadata keys prefixed with "_" (e.g. "_params", a list of field names),
// so values are decoded lazily per station.
type apiResponse struct {
	Hourly map[string]json.RawMessage `json:"hourly"`
}

// flexFloat decodes a JSON number, a numeric string, or null/"" (as nil).
// Infoclimat returns measurements as strings in some responses.
type flexFloat struct{ v *float64 }

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) {
		f.v = nil
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			f.v = nil
			return nil
		}
		x, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("invalid number %q: %w", s, err)
		}
		f.v = &x
		return nil
	}
	var x float64
	if err := json.Unmarshal(b, &x); err != nil {
		return err
	}
	f.v = &x
	return nil
}

// rawReading holds one observation record. All measurement fields are nullable.
// Wind speeds are already in km/h; visibility is in meters.
type rawReading struct {
	DhUTC          string    `json:"dh_utc"`
	Temperature    flexFloat `json:"temperature"`     // °C
	TemperatureMin flexFloat `json:"temperature_min"` // °C
	TemperatureMax flexFloat `json:"temperature_max"` // °C
	Humidite       flexFloat `json:"humidite"`        // %
	PointDeRosee   flexFloat `json:"point_de_rosee"`  // °C
	Pression       flexFloat `json:"pression"`        // hPa
	VentMoyen      flexFloat `json:"vent_moyen"`      // km/h
	VentRafales    flexFloat `json:"vent_rafales"`    // km/h
	VentDirection  flexFloat `json:"vent_direction"`  // degrees
	Pluie1h        flexFloat `json:"pluie_1h"`        // mm
	Visibilite     flexFloat `json:"visibilite"`      // meters
	TempsOmm       flexFloat `json:"temps_omm"`       // WMO present weather code
	UVIndex        flexFloat `json:"uv_index"`
}

// Fetch satisfies weather.Provider by delegating to FetchAll.
func (p *Provider) Fetch(ctx context.Context, loc weather.Location) ([]weather.Forecast, error) {
	results, err := p.FetchAll(ctx, []weather.Location{loc})
	if err != nil {
		return nil, err
	}
	lr := results[loc.Name]
	return lr.Forecasts, lr.Err
}

// FetchAll satisfies weather.BatchProvider, batching all locations into one API call.
func (p *Provider) FetchAll(ctx context.Context, locs []weather.Location) (map[string]weather.LocationResult, error) {
	results := make(map[string]weather.LocationResult, len(locs))

	var validLocs []weather.Location
	for _, loc := range locs {
		if loc.StationID == "" {
			results[loc.Name] = weather.LocationResult{
				Err: fmt.Errorf("provider %q: location %q has no station_id configured", p.name, loc.Name),
			}
		} else {
			validLocs = append(validLocs, loc)
		}
	}
	if len(validLocs) == 0 {
		return results, nil
	}

	now := time.Now().UTC()
	q := url.Values{}
	q.Set("version", "2")
	q.Set("method", "get")
	q.Set("format", "json")
	q.Set("start", now.AddDate(0, 0, -p.pastDays).Format("2006-01-02"))
	q.Set("end", now.Format("2006-01-02"))
	q.Set("token", p.token)

	var sb strings.Builder
	fmt.Fprintf(&sb, "%s?%s", p.baseURL, q.Encode())
	for _, loc := range validLocs {
		fmt.Fprintf(&sb, "&stations[]=%s", url.QueryEscape(loc.StationID))
	}

	var r apiResponse
	if err := providers.GetJSON(ctx, p.client, sb.String(), &r); err != nil {
		return nil, err
	}

	for _, loc := range validLocs {
		raw, ok := r.Hourly[loc.StationID]
		if !ok {
			results[loc.Name] = weather.LocationResult{}
			continue
		}
		var stationReadings []rawReading
		if err := json.Unmarshal(raw, &stationReadings); err != nil {
			results[loc.Name] = weather.LocationResult{
				Err: fmt.Errorf("provider %q: station %q: decode readings: %w", p.name, loc.StationID, err),
			}
			continue
		}
		forecasts, err := p.parseReadings(now, stationReadings)
		if err != nil {
			results[loc.Name] = weather.LocationResult{
				Err: fmt.Errorf("provider %q: station %q: %w", p.name, loc.StationID, err),
			}
		} else {
			results[loc.Name] = weather.LocationResult{Forecasts: forecasts}
		}
	}
	return results, nil
}

func (p *Provider) parseReadings(now time.Time, stationReadings []rawReading) ([]weather.Forecast, error) {
	out := make([]weather.Forecast, 0, len(stationReadings))
	for _, rd := range stationReadings {
		t, err := time.ParseInLocation("2006-01-02 15:04:05", rd.DhUTC, time.UTC)
		if err != nil {
			return nil, fmt.Errorf("parse timestamp %q: %w", rd.DhUTC, err)
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
			TemperatureC:     rd.Temperature.v,
			TemperatureMinC:  rd.TemperatureMin.v,
			TemperatureMaxC:  rd.TemperatureMax.v,
			HumidityPct:      rd.Humidite.v,
			PressureHPa:      rd.Pression.v,
			WindSpeedKmh:     rd.VentMoyen.v,
			WindGustKmh:      rd.VentRafales.v,
			WindDirectionDeg: rd.VentDirection.v,
			PrecipitationMm:  rd.Pluie1h.v,
			DewPointC:        rd.PointDeRosee.v,
			UVIndex:          rd.UVIndex.v,
		}
		if rd.Visibilite.v != nil {
			v := *rd.Visibilite.v / 1000.0
			f.VisibilityKm = &v
		}
		setCondition(&f, rd.TempsOmm.v)
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
