// Package weather defines the provider-agnostic domain types shared by
// providers, the store and the HTTP API.
package weather

import (
	"context"
	"time"
)

// Location is a place forecasts are requested for.
type Location struct {
	Name      string  `json:"name" yaml:"name"`
	City      string  `json:"city" yaml:"city"`
	Country   string  `json:"country" yaml:"country"`
	Latitude  float64 `json:"latitude" yaml:"latitude"`
	Longitude float64 `json:"longitude" yaml:"longitude"`
}

// Condition is a normalized sky/precipitation condition shared by all providers.
type Condition string

const (
	ConditionClear        Condition = "clear"
	ConditionPartlyCloudy Condition = "partly_cloudy"
	ConditionCloudy       Condition = "cloudy"
	ConditionFog          Condition = "fog"
	ConditionDrizzle      Condition = "drizzle"
	ConditionRain         Condition = "rain"
	ConditionFreezingRain Condition = "freezing_rain"
	ConditionSleet        Condition = "sleet"
	ConditionSnow         Condition = "snow"
	ConditionThunderstorm Condition = "thunderstorm"
	ConditionUnknown      Condition = "unknown"
)

// Granularity describes the time span a single forecast data point covers.
type Granularity string

const (
	GranularityHourly     Granularity = "hourly"
	GranularityThreeHours Granularity = "3h"
	GranularityDaily      Granularity = "daily"
)

// Forecast is a single forecast data point returned by a provider.
//
// Units: °C, %, hPa, km/h, mm (precipitation), cm (snowfall), km (visibility),
// degrees (wind direction). Nil pointers mean the provider did not supply the value.
type Forecast struct {
	ForecastTime time.Time   `json:"forecast_time"` // UTC start of the covered period
	Granularity  Granularity `json:"granularity"`

	TemperatureC                *float64 `json:"temperature_c,omitempty"`
	TemperatureMinC             *float64 `json:"temperature_min_c,omitempty"`
	TemperatureMaxC             *float64 `json:"temperature_max_c,omitempty"`
	FeelsLikeC                  *float64 `json:"feels_like_c,omitempty"`
	HumidityPct                 *float64 `json:"humidity_pct,omitempty"`
	DewPointC                   *float64 `json:"dew_point_c,omitempty"`
	PressureHPa                 *float64 `json:"pressure_hpa,omitempty"`
	WindSpeedKmh                *float64 `json:"wind_speed_kmh,omitempty"`
	WindGustKmh                 *float64 `json:"wind_gust_kmh,omitempty"`
	WindDirectionDeg            *float64 `json:"wind_direction_deg,omitempty"`
	CloudCoverPct               *float64 `json:"cloud_cover_pct,omitempty"`
	PrecipitationMm             *float64 `json:"precipitation_mm,omitempty"`
	PrecipitationProbabilityPct *float64 `json:"precipitation_probability_pct,omitempty"`
	SnowfallCm                  *float64 `json:"snowfall_cm,omitempty"`
	UVIndex                     *float64 `json:"uv_index,omitempty"`
	VisibilityKm                *float64 `json:"visibility_km,omitempty"`

	Condition     Condition `json:"condition"`
	ConditionCode string    `json:"condition_code,omitempty"` // raw provider code
	ConditionText string    `json:"condition_text,omitempty"` // raw provider description
}

// Provider fetches forecasts for a location from one weather service.
type Provider interface {
	// Name is the unique instance name from the configuration.
	Name() string
	// Type is the provider implementation identifier, e.g. "openmeteo".
	Type() string
	// Fetch returns all forecast data points available for loc.
	Fetch(ctx context.Context, loc Location) ([]Forecast, error)
}
