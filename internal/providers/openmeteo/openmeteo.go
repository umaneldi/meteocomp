// Package openmeteo implements the Open-Meteo provider (https://open-meteo.com).
// No API key is required.
//
// Options:
//   - forecast_days (int, default 7, max 16)
package openmeteo

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
	Type           = "openmeteo"
	defaultBaseURL = "https://api.open-meteo.com/v1/forecast"
)

func init() { providers.Register(Type, New) }

type Provider struct {
	name         string
	baseURL      string
	forecastDays int
	client       *http.Client
}

func New(cfg config.ProviderConfig, client *http.Client) (weather.Provider, error) {
	p := &Provider{
		name:         cfg.Name,
		baseURL:      cfg.BaseURL,
		forecastDays: cfg.IntOption("forecast_days", 7),
		client:       client,
	}
	if p.baseURL == "" {
		p.baseURL = defaultBaseURL
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
		"precipitation_probability,precipitation,snowfall,weather_code,pressure_msl," +
		"cloud_cover,visibility,wind_speed_10m,wind_direction_10m,wind_gusts_10m,uv_index"
	dailyVars = "weather_code,temperature_2m_max,temperature_2m_min,apparent_temperature_max," +
		"precipitation_sum,precipitation_probability_max,snowfall_sum,wind_speed_10m_max," +
		"wind_gusts_10m_max,wind_direction_10m_dominant,uv_index_max"
)

type response struct {
	Hourly struct {
		Time                     []string   `json:"time"`
		Temperature2m            []*float64 `json:"temperature_2m"`
		RelativeHumidity2m       []*float64 `json:"relative_humidity_2m"`
		DewPoint2m               []*float64 `json:"dew_point_2m"`
		ApparentTemperature      []*float64 `json:"apparent_temperature"`
		PrecipitationProbability []*float64 `json:"precipitation_probability"`
		Precipitation            []*float64 `json:"precipitation"`
		Snowfall                 []*float64 `json:"snowfall"`
		WeatherCode              []*int     `json:"weather_code"`
		PressureMSL              []*float64 `json:"pressure_msl"`
		CloudCover               []*float64 `json:"cloud_cover"`
		Visibility               []*float64 `json:"visibility"`
		WindSpeed10m             []*float64 `json:"wind_speed_10m"`
		WindDirection10m         []*float64 `json:"wind_direction_10m"`
		WindGusts10m             []*float64 `json:"wind_gusts_10m"`
		UVIndex                  []*float64 `json:"uv_index"`
	} `json:"hourly"`
	Daily struct {
		Time                        []string   `json:"time"`
		WeatherCode                 []*int     `json:"weather_code"`
		Temperature2mMax            []*float64 `json:"temperature_2m_max"`
		Temperature2mMin            []*float64 `json:"temperature_2m_min"`
		ApparentTemperatureMax      []*float64 `json:"apparent_temperature_max"`
		PrecipitationSum            []*float64 `json:"precipitation_sum"`
		PrecipitationProbabilityMax []*float64 `json:"precipitation_probability_max"`
		SnowfallSum                 []*float64 `json:"snowfall_sum"`
		WindSpeed10mMax             []*float64 `json:"wind_speed_10m_max"`
		WindGusts10mMax             []*float64 `json:"wind_gusts_10m_max"`
		WindDirection10mDominant    []*float64 `json:"wind_direction_10m_dominant"`
		UVIndexMax                  []*float64 `json:"uv_index_max"`
	} `json:"daily"`
}

func (p *Provider) Fetch(ctx context.Context, loc weather.Location) ([]weather.Forecast, error) {
	q := url.Values{}
	q.Set("latitude", strconv.FormatFloat(loc.Latitude, 'f', -1, 64))
	q.Set("longitude", strconv.FormatFloat(loc.Longitude, 'f', -1, 64))
	q.Set("hourly", hourlyVars)
	q.Set("daily", dailyVars)
	q.Set("timezone", "UTC")
	q.Set("wind_speed_unit", "kmh")
	q.Set("forecast_days", strconv.Itoa(p.forecastDays))

	var r response
	if err := providers.GetJSON(ctx, p.client, p.baseURL+"?"+q.Encode(), &r); err != nil {
		return nil, err
	}

	out := make([]weather.Forecast, 0, len(r.Hourly.Time)+len(r.Daily.Time))
	h := r.Hourly
	for i, ts := range h.Time {
		t, err := time.Parse("2006-01-02T15:04", ts)
		if err != nil {
			return nil, fmt.Errorf("parse hourly time %q: %w", ts, err)
		}
		f := weather.Forecast{
			ForecastTime:                t.UTC(),
			Granularity:                 weather.GranularityHourly,
			TemperatureC:                at(h.Temperature2m, i),
			FeelsLikeC:                  at(h.ApparentTemperature, i),
			HumidityPct:                 at(h.RelativeHumidity2m, i),
			DewPointC:                   at(h.DewPoint2m, i),
			PressureHPa:                 at(h.PressureMSL, i),
			WindSpeedKmh:                at(h.WindSpeed10m, i),
			WindGustKmh:                 at(h.WindGusts10m, i),
			WindDirectionDeg:            at(h.WindDirection10m, i),
			CloudCoverPct:               at(h.CloudCover, i),
			PrecipitationMm:             at(h.Precipitation, i),
			PrecipitationProbabilityPct: at(h.PrecipitationProbability, i),
			SnowfallCm:                  at(h.Snowfall, i),
			UVIndex:                     at(h.UVIndex, i),
		}
		if v := at(h.Visibility, i); v != nil {
			f.VisibilityKm = new(*v / 1000)
		}
		setCondition(&f, atInt(h.WeatherCode, i))
		out = append(out, f)
	}

	d := r.Daily
	for i, ds := range d.Time {
		t, err := time.Parse("2006-01-02", ds)
		if err != nil {
			return nil, fmt.Errorf("parse daily time %q: %w", ds, err)
		}
		f := weather.Forecast{
			ForecastTime:                t.UTC(),
			Granularity:                 weather.GranularityDaily,
			TemperatureMinC:             at(d.Temperature2mMin, i),
			TemperatureMaxC:             at(d.Temperature2mMax, i),
			FeelsLikeC:                  at(d.ApparentTemperatureMax, i),
			WindSpeedKmh:                at(d.WindSpeed10mMax, i),
			WindGustKmh:                 at(d.WindGusts10mMax, i),
			WindDirectionDeg:            at(d.WindDirection10mDominant, i),
			PrecipitationMm:             at(d.PrecipitationSum, i),
			PrecipitationProbabilityPct: at(d.PrecipitationProbabilityMax, i),
			SnowfallCm:                  at(d.SnowfallSum, i),
			UVIndex:                     at(d.UVIndexMax, i),
		}
		setCondition(&f, atInt(d.WeatherCode, i))
		out = append(out, f)
	}
	return out, nil
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
	f.ConditionCode = strconv.Itoa(*code)
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
