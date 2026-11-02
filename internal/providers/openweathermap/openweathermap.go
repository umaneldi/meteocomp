// Package openweathermap implements the OpenWeatherMap provider using the free
// 5 day / 3 hour forecast API (https://openweathermap.org/forecast5). Requires an API key.
package openweathermap

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
	Type           = "openweathermap"
	defaultBaseURL = "https://api.openweathermap.org/data/2.5/forecast"
)

func init() {
	providers.Register(Type, New, providers.Info{
		Description:    "OpenWeatherMap 5 day / 3 hour forecast (free plan).",
		RequiresAPIKey: true,
		APIKeyEnv:      "OWM_API_KEY",
		SignupURL:      "https://home.openweathermap.org/api_keys",
	})
}

type Provider struct {
	name    string
	apiKey  string
	baseURL string
	client  *http.Client
}

func New(cfg config.ProviderConfig, client *http.Client) (weather.Provider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("provider %q: api_key is required", cfg.Name)
	}
	p := &Provider{name: cfg.Name, apiKey: cfg.APIKey, baseURL: cfg.BaseURL, client: client}
	if p.baseURL == "" {
		p.baseURL = defaultBaseURL
	}
	return p, nil
}

func (p *Provider) Name() string { return p.name }
func (p *Provider) Type() string { return Type }

type response struct {
	List []struct {
		Dt   int64 `json:"dt"`
		Main struct {
			Temp      *float64 `json:"temp"`
			FeelsLike *float64 `json:"feels_like"`
			TempMin   *float64 `json:"temp_min"`
			TempMax   *float64 `json:"temp_max"`
			Pressure  *float64 `json:"pressure"`
			Humidity  *float64 `json:"humidity"`
		} `json:"main"`
		Weather []struct {
			ID          int    `json:"id"`
			Description string `json:"description"`
		} `json:"weather"`
		Clouds struct {
			All *float64 `json:"all"`
		} `json:"clouds"`
		Wind struct {
			Speed *float64 `json:"speed"` // m/s with units=metric
			Deg   *float64 `json:"deg"`
			Gust  *float64 `json:"gust"`
		} `json:"wind"`
		Visibility *float64 `json:"visibility"` // meters
		Pop        *float64 `json:"pop"`        // 0..1
		Rain       struct {
			ThreeH *float64 `json:"3h"`
		} `json:"rain"`
		Snow struct {
			ThreeH *float64 `json:"3h"`
		} `json:"snow"`
	} `json:"list"`
}

func (p *Provider) Fetch(ctx context.Context, loc weather.Location) ([]weather.Forecast, error) {
	q := url.Values{}
	q.Set("lat", strconv.FormatFloat(loc.Latitude, 'f', -1, 64))
	q.Set("lon", strconv.FormatFloat(loc.Longitude, 'f', -1, 64))
	q.Set("units", "metric")
	q.Set("appid", p.apiKey)

	var r response
	if err := providers.GetJSON(ctx, p.client, p.baseURL+"?"+q.Encode(), &r); err != nil {
		return nil, err
	}

	out := make([]weather.Forecast, 0, len(r.List))
	for _, e := range r.List {
		f := weather.Forecast{
			ForecastTime:                time.Unix(e.Dt, 0).UTC(),
			Granularity:                 weather.GranularityThreeHours,
			TemperatureC:                e.Main.Temp,
			TemperatureMinC:             e.Main.TempMin,
			TemperatureMaxC:             e.Main.TempMax,
			FeelsLikeC:                  e.Main.FeelsLike,
			HumidityPct:                 e.Main.Humidity,
			PressureHPa:                 e.Main.Pressure,
			WindDirectionDeg:            e.Wind.Deg,
			CloudCoverPct:               e.Clouds.All,
			WindSpeedKmh:                scale(e.Wind.Speed, 3.6),
			WindGustKmh:                 scale(e.Wind.Gust, 3.6),
			VisibilityKm:                scale(e.Visibility, 0.001),
			PrecipitationProbabilityPct: scale(e.Pop, 100),
			Condition:                   weather.ConditionUnknown,
		}
		// rain.3h and snow.3h are both liquid-equivalent millimeters.
		if e.Rain.ThreeH != nil || e.Snow.ThreeH != nil {
			f.PrecipitationMm = new(deref(e.Rain.ThreeH) + deref(e.Snow.ThreeH))
		} else {
			f.PrecipitationMm = new(0.0)
		}
		if len(e.Weather) > 0 {
			w := e.Weather[0]
			f.ConditionCode = strconv.Itoa(w.ID)
			f.ConditionText = w.Description
			f.Condition = condition(w.ID)
		}
		out = append(out, f)
	}
	return out, nil
}

func scale(v *float64, k float64) *float64 {
	if v == nil {
		return nil
	}
	return new(*v * k)
}

func deref(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

// condition maps OpenWeatherMap condition IDs (https://openweathermap.org/weather-conditions).
func condition(id int) weather.Condition {
	switch {
	case id >= 200 && id < 300:
		return weather.ConditionThunderstorm
	case id >= 300 && id < 400:
		return weather.ConditionDrizzle
	case id == 511:
		return weather.ConditionFreezingRain
	case id >= 500 && id < 600:
		return weather.ConditionRain
	case id >= 611 && id <= 616:
		return weather.ConditionSleet
	case id >= 600 && id < 700:
		return weather.ConditionSnow
	case id >= 700 && id < 800:
		return weather.ConditionFog
	case id == 800:
		return weather.ConditionClear
	case id == 801 || id == 802:
		return weather.ConditionPartlyCloudy
	case id == 803 || id == 804:
		return weather.ConditionCloudy
	}
	return weather.ConditionUnknown
}
