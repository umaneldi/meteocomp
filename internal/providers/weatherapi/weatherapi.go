// Package weatherapi implements the WeatherAPI.com provider
// (https://www.weatherapi.com/docs/). Requires an API key.
//
// forecast_days is set globally in Settings and injected at build time; capped at 14 (the free plan is limited to 3).
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

func init() {
	providers.Register(Type, New, providers.Info{
		Description:    "WeatherAPI.com hourly forecast.",
		RequiresAPIKey: true,
		APIKeyEnv:      "WEATHER_API_KEY",
		SignupURL:      "https://www.weatherapi.com/my/",
	})
}

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
	days := min(cfg.IntOption("forecast_days", 7), 14)
	p := &Provider{name: cfg.Name, apiKey: cfg.APIKey, baseURL: cfg.BaseURL, days: days, client: client}
	if p.baseURL == "" {
		p.baseURL = defaultBaseURL
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
			Hour []struct {
				TimeEpoch    int64    `json:"time_epoch"`
				TempC        *float64 `json:"temp_c"`
				Condition    cond     `json:"condition"`
				WindKph      *float64 `json:"wind_kph"`
				WindDegree   *float64 `json:"wind_degree"`
				PressureMb   *float64 `json:"pressure_mb"`
				SnowCm       *float64 `json:"snow_cm"`
				Humidity     *float64 `json:"humidity"`
				Cloud        *float64 `json:"cloud"`
				FeelsLikeC   *float64 `json:"feelslike_c"`
				DewPointC    *float64 `json:"dewpoint_c"`
				VisKm        *float64 `json:"vis_km"`
				GustKph      *float64 `json:"gust_kph"`
				ChanceOfRain *float64 `json:"chance_of_rain"`
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
				Kind:                        weather.KindForecast,
				ForecastTime:                time.Unix(h.TimeEpoch, 0).UTC(),
				TemperatureC:                h.TempC,
				FeelsLikeC:                  h.FeelsLikeC,
				HumidityPct:                 h.Humidity,
				DewPointC:                   h.DewPointC,
				PressureHPa:                 h.PressureMb,
				WindSpeedKmh:                h.WindKph,
				WindGustKmh:                 h.GustKph,
				WindDirectionDeg:            h.WindDegree,
				CloudCoverPct:               h.Cloud,
				PrecipitationProbabilityPct: h.ChanceOfRain,
				SnowfallCm:                  h.SnowCm,
				VisibilityKm:                h.VisKm,
			}
			setCondition(&f, h.Condition)
			out = append(out, f)
		}
	}
	return out, nil
}

func setCondition(f *weather.Forecast, c cond) {
	f.ConditionText = c.Text
	if c.Code == 0 {
		f.Condition = weather.ConditionUnknown
		return
	}
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
