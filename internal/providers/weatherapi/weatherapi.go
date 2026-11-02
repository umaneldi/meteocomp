// Package weatherapi implements the WeatherAPI.com provider
// (https://www.weatherapi.com/docs/). Requires an API key.
//
// Options:
//   - days (int, default 3, max 14; the free plan is limited to 3)
package weatherapi

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
	Type           = "weatherapi"
	defaultBaseURL = "https://api.weatherapi.com/v1/forecast.json"
)

func init() { providers.Register(Type, New) }

type Provider struct {
	name    string
	apiKey  string
	baseURL string
	days    int
	client  *http.Client
}

func New(cfg config.ProviderConfig, client *http.Client) (weather.Provider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("provider %q: api_key is required", cfg.Name)
	}
	p := &Provider{name: cfg.Name, apiKey: cfg.APIKey, baseURL: cfg.BaseURL, days: cfg.IntOption("days", 3), client: client}
	if p.baseURL == "" {
		p.baseURL = defaultBaseURL
	}
	if p.days < 1 || p.days > 14 {
		return nil, fmt.Errorf("provider %q: days must be between 1 and 14", cfg.Name)
	}
	return p, nil
}

func (p *Provider) Name() string { return p.name }
func (p *Provider) Type() string { return Type }

type cond struct {
	Text string `json:"text"`
	Code int    `json:"code"`
}

type response struct {
	Forecast struct {
		ForecastDay []struct {
			Date string `json:"date"`
			Day  struct {
				MaxTempC          *float64 `json:"maxtemp_c"`
				MinTempC          *float64 `json:"mintemp_c"`
				AvgTempC          *float64 `json:"avgtemp_c"`
				MaxWindKph        *float64 `json:"maxwind_kph"`
				TotalPrecipMm     *float64 `json:"totalprecip_mm"`
				TotalSnowCm       *float64 `json:"totalsnow_cm"`
				AvgVisKm          *float64 `json:"avgvis_km"`
				AvgHumidity       *float64 `json:"avghumidity"`
				DailyChanceOfRain *float64 `json:"daily_chance_of_rain"`
				DailyChanceOfSnow *float64 `json:"daily_chance_of_snow"`
				Condition         cond     `json:"condition"`
				UV                *float64 `json:"uv"`
			} `json:"day"`
			Hour []struct {
				TimeEpoch    int64    `json:"time_epoch"`
				TempC        *float64 `json:"temp_c"`
				Condition    cond     `json:"condition"`
				WindKph      *float64 `json:"wind_kph"`
				WindDegree   *float64 `json:"wind_degree"`
				PressureMb   *float64 `json:"pressure_mb"`
				PrecipMm     *float64 `json:"precip_mm"`
				SnowCm       *float64 `json:"snow_cm"`
				Humidity     *float64 `json:"humidity"`
				Cloud        *float64 `json:"cloud"`
				FeelsLikeC   *float64 `json:"feelslike_c"`
				DewPointC    *float64 `json:"dewpoint_c"`
				VisKm        *float64 `json:"vis_km"`
				GustKph      *float64 `json:"gust_kph"`
				ChanceOfRain *float64 `json:"chance_of_rain"`
				ChanceOfSnow *float64 `json:"chance_of_snow"`
				UV           *float64 `json:"uv"`
			} `json:"hour"`
		} `json:"forecastday"`
	} `json:"forecast"`
}

func (p *Provider) Fetch(ctx context.Context, loc weather.Location) ([]weather.Forecast, error) {
	q := url.Values{}
	q.Set("key", p.apiKey)
	q.Set("q", strconv.FormatFloat(loc.Latitude, 'f', -1, 64)+","+strconv.FormatFloat(loc.Longitude, 'f', -1, 64))
	q.Set("days", strconv.Itoa(p.days))
	q.Set("aqi", "no")
	q.Set("alerts", "no")

	var r response
	if err := providers.GetJSON(ctx, p.client, p.baseURL+"?"+q.Encode(), &r); err != nil {
		return nil, err
	}

	var out []weather.Forecast
	for _, fd := range r.Forecast.ForecastDay {
		for _, h := range fd.Hour {
			f := weather.Forecast{
				ForecastTime:                time.Unix(h.TimeEpoch, 0).UTC(),
				Granularity:                 weather.GranularityHourly,
				TemperatureC:                h.TempC,
				FeelsLikeC:                  h.FeelsLikeC,
				HumidityPct:                 h.Humidity,
				DewPointC:                   h.DewPointC,
				PressureHPa:                 h.PressureMb,
				WindSpeedKmh:                h.WindKph,
				WindGustKmh:                 h.GustKph,
				WindDirectionDeg:            h.WindDegree,
				CloudCoverPct:               h.Cloud,
				PrecipitationMm:             h.PrecipMm,
				PrecipitationProbabilityPct: maxPtr(h.ChanceOfRain, h.ChanceOfSnow),
				SnowfallCm:                  h.SnowCm,
				UVIndex:                     h.UV,
				VisibilityKm:                h.VisKm,
			}
			setCondition(&f, h.Condition)
			out = append(out, f)
		}

		// The date is local to the location; it is stored as midnight UTC of that
		// calendar day, consistent with other daily providers.
		t, err := time.Parse("2006-01-02", fd.Date)
		if err != nil {
			return nil, fmt.Errorf("parse date %q: %w", fd.Date, err)
		}
		d := fd.Day
		f := weather.Forecast{
			ForecastTime:                t.UTC(),
			Granularity:                 weather.GranularityDaily,
			TemperatureC:                d.AvgTempC,
			TemperatureMinC:             d.MinTempC,
			TemperatureMaxC:             d.MaxTempC,
			HumidityPct:                 d.AvgHumidity,
			WindSpeedKmh:                d.MaxWindKph,
			PrecipitationMm:             d.TotalPrecipMm,
			PrecipitationProbabilityPct: maxPtr(d.DailyChanceOfRain, d.DailyChanceOfSnow),
			SnowfallCm:                  d.TotalSnowCm,
			UVIndex:                     d.UV,
			VisibilityKm:                d.AvgVisKm,
		}
		setCondition(&f, d.Condition)
		out = append(out, f)
	}
	return out, nil
}

func maxPtr(a, b *float64) *float64 {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case *b > *a:
		return b
	}
	return a
}

func setCondition(f *weather.Forecast, c cond) {
	f.ConditionText = c.Text
	if c.Code == 0 {
		f.Condition = weather.ConditionUnknown
		return
	}
	f.ConditionCode = strconv.Itoa(c.Code)
	f.Condition = condition(c.Code)
}

// condition maps WeatherAPI.com condition codes
// (https://www.weatherapi.com/docs/weather_conditions.json).
func condition(code int) weather.Condition {
	switch code {
	case 1000:
		return weather.ConditionClear
	case 1003:
		return weather.ConditionPartlyCloudy
	case 1006, 1009:
		return weather.ConditionCloudy
	case 1030, 1135, 1147:
		return weather.ConditionFog
	case 1150, 1153:
		return weather.ConditionDrizzle
	case 1063, 1180, 1183, 1186, 1189, 1192, 1195, 1240, 1243, 1246:
		return weather.ConditionRain
	case 1072, 1168, 1171, 1198, 1201:
		return weather.ConditionFreezingRain
	case 1069, 1204, 1207, 1237, 1249, 1252, 1261, 1264:
		return weather.ConditionSleet
	case 1066, 1114, 1117, 1210, 1213, 1216, 1219, 1222, 1225, 1255, 1258:
		return weather.ConditionSnow
	case 1087, 1273, 1276, 1279, 1282:
		return weather.ConditionThunderstorm
	}
	return weather.ConditionUnknown
}
