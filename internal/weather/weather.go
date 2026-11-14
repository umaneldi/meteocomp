// Package weather defines the provider-agnostic domain types shared by
// providers, the store and the HTTP API.
package weather

import (
	"context"
	"time"
)

// Location is a place forecasts are requested for.
type Location struct {
	Name      string  `json:"name"                 yaml:"name"`
	City      string  `json:"city"                 yaml:"city"`
	Country   string  `json:"country"              yaml:"country"`
	Latitude  float64 `json:"latitude"             yaml:"latitude"`
	Longitude float64 `json:"longitude"            yaml:"longitude"`
	StationID string  `json:"station_id,omitempty" yaml:"station_id,omitempty"`
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

// Kind distinguishes a forecast data point from a recorded observation.
type Kind string

const (
	KindForecast    Kind = "forecast"
	KindObservation Kind = "observation"
)

// Forecast is a single weather data point returned by a provider — either a
// future forecast (Kind = KindForecast) or a past/current observation (Kind =
// KindObservation). Units: °C, %, hPa, km/h, cm (snowfall),
// km (visibility), degrees (wind direction). Nil pointers mean the provider did
// not supply the value.
type Forecast struct {
	Kind         Kind      `json:"kind"`
	ForecastTime time.Time `json:"forecast_time"`

	TemperatureC                *float64 `json:"temperature_c"`
	FeelsLikeC                  *float64 `json:"feels_like_c"`
	HumidityPct                 *float64 `json:"humidity_pct"`
	DewPointC                   *float64 `json:"dew_point_c"`
	PressureHPa                 *float64 `json:"pressure_hpa"`
	WindSpeedKmh                *float64 `json:"wind_speed_kmh"`
	WindGustKmh                 *float64 `json:"wind_gust_kmh"`
	WindDirectionDeg            *float64 `json:"wind_direction_deg"`
	CloudCoverPct               *float64 `json:"cloud_cover_pct"`
	PrecipitationProbabilityPct *float64 `json:"precipitation_probability_pct"`
	SnowfallCm                  *float64 `json:"snowfall_cm"`
	VisibilityKm                *float64 `json:"visibility_km"`

	Condition     Condition `json:"condition"`
	ConditionText string    `json:"condition_text"`
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

// LocationResult holds the outcome of fetching one location in a batch.
type LocationResult struct {
	Forecasts []Forecast
	Err       error
}

// BatchProvider is an optional Provider extension. When implemented, the poller
// calls FetchAll once for all locations instead of Fetch per-location, allowing
// a single API request to serve many stations.
type BatchProvider interface {
	Provider
	// FetchAll fetches data for all locs in a single API call.
	// The map is keyed by location Name. A location absent from the map is
	// treated as a successful poll with zero readings. A non-nil error means
	// the entire batch request failed; per-location errors are in LocationResult.Err.
	FetchAll(ctx context.Context, locs []Location) (map[string]LocationResult, error)
}

// ConditionSource is an optional Provider extension. The poller does not store the
// readings of such a provider; it uses them to fill the condition of stored
// observations of the same location that lack one.
type ConditionSource interface {
	Provider
	FillsConditions()
}
