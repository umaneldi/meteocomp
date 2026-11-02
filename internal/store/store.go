// Package store persists polls and forecasts.
package store

import (
	"context"
	"time"

	"github.com/yanndethe/meteocomp/internal/weather"
)

// Store is the persistence interface used by the poller and the HTTP API.
type Store interface {
	// RecordPoll stores a poll and its forecasts atomically and returns the poll ID.
	RecordPoll(ctx context.Context, r PollResult) (int64, error)
	ListPolls(ctx context.Context, f PollFilter) ([]Poll, error)
	ListForecasts(ctx context.Context, f ForecastFilter) ([]ForecastRecord, error)
	Close() error
}

// PollResult is the outcome of fetching one provider for one location.
type PollResult struct {
	Provider     string
	ProviderType string
	Location     weather.Location
	StartedAt    time.Time
	FinishedAt   time.Time
	Err          error
	Forecasts    []weather.Forecast
}

const (
	StatusOK    = "ok"
	StatusError = "error"
)

// Poll is a stored poll row.
type Poll struct {
	ID            int64     `json:"id"`
	Provider      string    `json:"provider"`
	ProviderType  string    `json:"provider_type"`
	LocationName  string    `json:"location_name"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
	DurationMs    int64     `json:"duration_ms"`
	Status        string    `json:"status"`
	Error         string    `json:"error,omitempty"`
	ForecastCount int       `json:"forecast_count"`
}

// ForecastRecord is a stored forecast row.
type ForecastRecord struct {
	ID            int64            `json:"id"`
	PollID        int64            `json:"poll_id"`
	PolledAt      time.Time        `json:"polled_at"`
	Provider      string           `json:"provider"`
	ProviderType  string           `json:"provider_type"`
	Location      weather.Location `json:"location"`
	LeadTimeHours float64          `json:"lead_time_hours"`
	weather.Forecast
}

// PollFilter selects polls. Zero values mean "no filter".
type PollFilter struct {
	Provider string
	Location string
	Status   string
	Limit    int
}

// ForecastFilter selects forecasts. Zero values mean "no filter".
type ForecastFilter struct {
	Provider    string
	Location    string
	Granularity string
	From, To    time.Time // forecast_time range [From, To)
	PolledFrom  time.Time // polled_at range [PolledFrom, PolledTo)
	PolledTo    time.Time
	PollID      int64
	Limit       int
}
