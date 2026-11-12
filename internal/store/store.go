// Package store persists settings and readings.
package store

import (
	"context"
	"time"

	"github.com/yanndethe/meteocomp/internal/config"
	"github.com/yanndethe/meteocomp/internal/weather"
)

type Store interface {
	// RecordReadings appends a provider/location snapshot atomically.
	RecordReadings(ctx context.Context, readings []ReadingRecord) error
	ListReadings(ctx context.Context, f ReadingFilter) ([]ReadingRecord, error)
	// LastReadingTimes returns the latest retrieval time per provider instance.
	LastReadingTimes(ctx context.Context) (map[string]time.Time, error)
	LoadSettings(ctx context.Context) (s *config.Settings, found bool, err error)
	SaveSettings(ctx context.Context, s config.Settings) error
	Close() error
}

// ReadingRecord matches the Reading model in proto/global_map.py.
type ReadingRecord struct {
	Provider      string    `json:"provider"`
	ProviderType  string    `json:"provider_type"`
	LocationName  string    `json:"location_name"`
	LeadTimeHours float64   `json:"lead_time_hours"`
	PolledAt      time.Time `json:"polled_at"`
	weather.Forecast
}

// ReadingFilter selects readings. Time ranges are inclusive/exclusive.
type ReadingFilter struct {
	Provider   string
	Location   string
	Kind       string
	From, To         time.Time
	PolledFrom       time.Time
	PolledTo         time.Time
	ForecastTime     time.Time
	ForecastTimeDelta time.Duration
	Limit      int
	Offset     int
}
