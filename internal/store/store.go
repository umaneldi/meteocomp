// Package store persists settings, polls and readings.
package store

import (
	"context"
	"time"

	"github.com/yanndethe/meteocomp/internal/config"
	"github.com/yanndethe/meteocomp/internal/weather"
)

// Store is the persistence interface used by the poller and the HTTP API.
type Store interface {
	// RecordPoll stores a poll and its readings atomically and returns the poll ID.
	RecordPoll(ctx context.Context, r PollResult) (int64, error)
	ListPolls(ctx context.Context, f PollFilter) ([]Poll, error)
	ListReadings(ctx context.Context, f ReadingFilter) ([]ReadingRecord, error)
	// LastPollTimes returns, per provider instance name, the start time of its latest poll.
	LastPollTimes(ctx context.Context) (map[string]time.Time, error)

	// LoadSettings returns the persisted settings; found is false if none were saved yet.
	LoadSettings(ctx context.Context) (s *config.Settings, found bool, err error)
	// SaveSettings atomically replaces the persisted settings.
	SaveSettings(ctx context.Context, s config.Settings) error

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
	ID           int64     `json:"id"`
	Provider     string    `json:"provider"`
	ProviderType string    `json:"provider_type"`
	LocationName string    `json:"location_name"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
	DurationMs   int64     `json:"duration_ms"`
	Status       string    `json:"status"`
	Error        string    `json:"error,omitempty"`
	ReadingCount int       `json:"reading_count"`
	Kinds        []string  `json:"kinds"`
}

// ReadingRecord is a stored readings row (forecast or observation).
type ReadingRecord struct {
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

// ReadingFilter selects readings. Zero values mean "no filter".
type ReadingFilter struct {
	Provider    string
	Location    string
	Granularity string
	Kind        string    // "forecast", "observation", or "" for both
	From, To    time.Time // forecast_time range [From, To)
	PolledFrom  time.Time // polled_at range [PolledFrom, PolledTo)
	PolledTo    time.Time
	PollID      int64
	Limit       int
	Order       string // "id_desc" enables stable cursor pagination; empty keeps chronological ordering
	BeforeID    int64  // exclusive cursor, used with Order = "id_desc"
}
