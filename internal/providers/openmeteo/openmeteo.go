// Package openmeteo implements the Open-Meteo provider (https://open-meteo.com).
// No API key is required.
//
// Options:
//   - endpoint (string, default "forecast"): Open-Meteo API endpoint, i.e. the path
//     after /v1/. "forecast" blends the best models for the location; model-specific
//     endpoints include "meteofrance" (Météo-France AROME + ARPEGE, about 4 days),
//     "dwd-icon", "gfs", "ecmwf", "metno", "gem", "jma"...
//   - models (string, optional): comma-separated Open-Meteo model names passed as the
//     "models" query parameter, e.g. "arome_france" on the meteofrance endpoint.
//
// forecast_days is set globally in Settings and injected at build time.
//
// Endpoints only return the variables and days their models cover. Points with no
// value at all (e.g. beyond the model horizon) are dropped.
package openmeteo

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/yanndethe/meteocomp/internal/config"
	"github.com/yanndethe/meteocomp/internal/providers"
	"github.com/yanndethe/meteocomp/internal/weather"
)

const (
	Type            = "openmeteo"
	defaultBaseURL  = "https://api.open-meteo.com/v1"
	defaultEndpoint = "forecast"
)

var endpointRE = regexp.MustCompile(`^[a-z0-9-]+$`)

func init() {
	providers.Register(Type, New, providers.Info{
		Description: "Open-Meteo forecast API. No API key. Use the endpoint option to target a specific weather service.",
		Options: []providers.Option{
			{Name: "endpoint", Kind: "string", Default: defaultEndpoint,
				Description: `API path after /v1/: "forecast" (best models blend), "meteofrance", "dwd-icon", "gfs", "ecmwf", "metno"...`},
			{Name: "models", Kind: "string", Description: `Optional comma-separated model names, e.g. "arome_france"`},
		},
	})
}

type Provider struct {
	name         string
	url          string // base URL + "/" + endpoint
	models       string
	forecastDays int
	client       *http.Client
}

func New(cfg config.ProviderConfig, client *http.Client) (weather.Provider, error) {
	base := cfg.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	endpoint := cfg.StringOption("endpoint", defaultEndpoint)
	if !endpointRE.MatchString(endpoint) {
		return nil, fmt.Errorf("provider %q: invalid endpoint %q (expected e.g. \"forecast\" or \"meteofrance\")", cfg.Name, endpoint)
	}
	p := &Provider{
		name:         cfg.Name,
		url:          base + "/" + endpoint,
		models:       cfg.StringOption("models", ""),
		forecastDays: cfg.IntOption("forecast_days", 7),
		client:       client,
	}
	if p.forecastDays < 1 || p.forecastDays > 16 {
		return nil, fmt.Errorf("provider %q: forecast_days must be between 1 and 16", cfg.Name)
	}
	return p, nil
}

func (p *Provider) Name() string { return p.name }
func (p *Provider) Type() string { return Type }

const (
	hourlyVars = "temperature_2m,relative_humidity_2m,dew_point_2m,apparent_temperature," +
		"precipitation_probability,snowfall,weather_code,surface_pressure," +
		"cloud_cover,visibility,wind_speed_10m,wind_direction_10m,wind_gusts_10m"
)

type response struct {
	Hourly struct {
		Time                     []string   `json:"time"`
		Temperature2m            []*float64 `json:"temperature_2m"`
		RelativeHumidity2m       []*float64 `json:"relative_humidity_2m"`
		DewPoint2m               []*float64 `json:"dew_point_2m"`
		ApparentTemperature      []*float64 `json:"apparent_temperature"`
		PrecipitationProbability []*float64 `json:"precipitation_probability"`
		Snowfall                 []*float64 `json:"snowfall"`
		WeatherCode              []*int     `json:"weather_code"`
		SurfacePressure          []*float64 `json:"surface_pressure"`
		CloudCover               []*float64 `json:"cloud_cover"`
		Visibility               []*float64 `json:"visibility"`
		WindSpeed10m             []*float64 `json:"wind_speed_10m"`
		WindDirection10m         []*float64 `json:"wind_direction_10m"`
		WindGusts10m             []*float64 `json:"wind_gusts_10m"`
	} `json:"hourly"`
}

func (p *Provider) Fetch(ctx context.Context, loc weather.Location) ([]weather.Forecast, error) {
	q := url.Values{}
	q.Set("latitude", strconv.FormatFloat(loc.Latitude, 'f', -1, 64))
	q.Set("longitude", strconv.FormatFloat(loc.Longitude, 'f', -1, 64))
	q.Set("hourly", hourlyVars)
	q.Set("timezone", "UTC")
	q.Set("wind_speed_unit", "kmh")
	q.Set("forecast_days", strconv.Itoa(p.forecastDays))
	if p.models != "" {
		q.Set("models", p.models)
	}

	var r response
	if err := providers.GetJSON(ctx, p.client, p.url+"?"+q.Encode(), &r); err != nil {
		return nil, err
	}

	out := make([]weather.Forecast, 0, len(r.Hourly.Time))
	h := r.Hourly
	for i, ts := range h.Time {
		t, err := time.Parse("2006-01-02T15:04", ts)
		if err != nil {
			return nil, fmt.Errorf("parse hourly time %q: %w", ts, err)
		}
		f := weather.Forecast{
			Kind:                        weather.KindForecast,
			ForecastTime:                t.UTC(),
			TemperatureC:                at(h.Temperature2m, i),
			FeelsLikeC:                  at(h.ApparentTemperature, i),
			HumidityPct:                 at(h.RelativeHumidity2m, i),
			DewPointC:                   at(h.DewPoint2m, i),
			PressureHPa:                 at(h.SurfacePressure, i),
			WindSpeedKmh:                at(h.WindSpeed10m, i),
			WindGustKmh:                 at(h.WindGusts10m, i),
			WindDirectionDeg:            at(h.WindDirection10m, i),
			CloudCoverPct:               at(h.CloudCover, i),
			PrecipitationProbabilityPct: at(h.PrecipitationProbability, i),
			SnowfallCm:                  at(h.Snowfall, i),
		}
		if v := at(h.Visibility, i); v != nil {
			f.VisibilityKm = new(*v / 1000)
		}
		setCondition(&f, atInt(h.WeatherCode, i))
		if atInt(h.WeatherCode, i) != nil || !isEmpty(f) {
			out = append(out, f)
		}
	}
	return out, nil
}

// isEmpty reports whether f carries no value at all, which happens past the
// horizon of the models behind the endpoint.
func isEmpty(f weather.Forecast) bool {
	for _, v := range []*float64{
		f.TemperatureC, f.FeelsLikeC, f.HumidityPct,
		f.DewPointC, f.PressureHPa, f.WindSpeedKmh, f.WindGustKmh, f.WindDirectionDeg,
		f.CloudCoverPct, f.PrecipitationProbabilityPct, f.SnowfallCm, f.VisibilityKm,
	} {
		if v != nil {
			return false
		}
	}
	return true
}

func at(s []*float64, i int) *float64 {
	if i < len(s) {
		return s[i]
	}
	return nil
}

func atInt(s []*int, i int) *int {
	if i < len(s) {
		return s[i]
	}
	return nil
}

func setCondition(f *weather.Forecast, code *int) {
	if code == nil {
		f.Condition = weather.ConditionUnknown
		return
	}
	f.Condition, f.ConditionText = wmo(*code)
}

// wmo maps a WMO weather interpretation code to a normalized condition and description.
func wmo(code int) (weather.Condition, string) {
	switch code {
	case 0:
		return weather.ConditionClear, "Clear sky"
	case 1:
		return weather.ConditionPartlyCloudy, "Mainly clear"
	case 2:
		return weather.ConditionPartlyCloudy, "Partly cloudy"
	case 3:
		return weather.ConditionCloudy, "Overcast"
	case 45:
		return weather.ConditionFog, "Fog"
	case 48:
		return weather.ConditionFog, "Depositing rime fog"
	case 51, 53, 55:
		return weather.ConditionDrizzle, "Drizzle"
	case 56, 57:
		return weather.ConditionFreezingRain, "Freezing drizzle"
	case 61, 63, 65:
		return weather.ConditionRain, "Rain"
	case 66, 67:
		return weather.ConditionFreezingRain, "Freezing rain"
	case 71, 73, 75:
		return weather.ConditionSnow, "Snow fall"
	case 77:
		return weather.ConditionSnow, "Snow grains"
	case 80, 81, 82:
		return weather.ConditionRain, "Rain showers"
	case 85, 86:
		return weather.ConditionSnow, "Snow showers"
	case 95:
		return weather.ConditionThunderstorm, "Thunderstorm"
	case 96, 99:
		return weather.ConditionThunderstorm, "Thunderstorm with hail"
	}
	return weather.ConditionUnknown, ""
}
