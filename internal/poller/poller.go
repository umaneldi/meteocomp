// Package poller periodically fetches forecasts from every configured provider
// for every configured location and records the results.
package poller

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/yanndethe/meteocomp/internal/store"
	"github.com/yanndethe/meteocomp/internal/weather"
)

// conditionMaxGap is how far a condition source observation may be from an
// observation whose condition it fills.
const conditionMaxGap = 90 * time.Minute

// PollStatus is the outcome of the most recent poll attempt for one provider.
type PollStatus struct {
	PolledAt time.Time `json:"polled_at"`
	Error    string    `json:"error"`   // empty on success
	Polling  bool      `json:"polling"` // true while a poll is in progress
}

// Poller runs one goroutine per provider, all sharing the same interval.
type Poller struct {
	providers []weather.Provider
	interval  time.Duration
	locations []weather.Location
	store     store.Store
	log       *slog.Logger
	timeout   time.Duration

	// mu serializes polls of the same provider (ticker vs. manual trigger).
	mu map[string]*sync.Mutex
	// triggered tracks polls started by Trigger so Run can wait for them on shutdown.
	triggered sync.WaitGroup

	statusMu sync.RWMutex
	status   map[string]PollStatus

	// sources holds, per location, the latest readings of weather.ConditionSource providers.
	sourcesMu sync.Mutex
	sources   map[string][]weather.Forecast
}

func New(provs []weather.Provider, interval time.Duration, locations []weather.Location, st store.Store, log *slog.Logger) *Poller {
	mu := make(map[string]*sync.Mutex, len(provs))
	status := make(map[string]PollStatus, len(provs))
	for _, p := range provs {
		mu[p.Name()] = &sync.Mutex{}
	}
	return &Poller{providers: provs, interval: interval, locations: locations, store: st, log: log,
		timeout: time.Minute, mu: mu, status: status, sources: map[string][]weather.Forecast{}}
}

func (p *Poller) setStatus(name string, s PollStatus) {
	p.statusMu.Lock()
	p.status[name] = s
	p.statusMu.Unlock()
}

// Statuses returns a snapshot of the last poll outcome for every provider.
func (p *Poller) Statuses() map[string]PollStatus {
	p.statusMu.RLock()
	defer p.statusMu.RUnlock()
	out := make(map[string]PollStatus, len(p.status))
	for k, v := range p.status {
		out[k] = v
	}
	return out
}

// Reason tells why a provider poll was started. It is included in logs.
type Reason string

const (
	ReasonStartup   Reason = "startup"
	ReasonScheduled Reason = "scheduled"
	ReasonManual    Reason = "manual"
)

// Run polls each provider until ctx is done. See RunUntil.
func (p *Poller) Run(ctx context.Context) { p.RunUntil(ctx, nil) }

// RunUntil polls each provider at the shared interval until ctx is done or stop
// is closed. A provider whose last stored reading is older than the interval (or
// that was never polled) is polled immediately; otherwise its schedule resumes
// from the last poll, so restarts and settings changes do not cause extra API calls.
// Closing stop lets in-flight polls finish; cancelling ctx aborts them.
func (p *Poller) RunUntil(ctx context.Context, stop <-chan struct{}) {
	p.log.Info("poller started", "providers", len(p.providers), "locations", len(p.locations), "interval", p.interval)
	last, err := p.store.LastReadingTimes(ctx)
	if err != nil {
		p.log.Warn("read last reading times; polling all providers now", "error", err)
	}
	var wg sync.WaitGroup
	for _, prov := range p.providers {
		wg.Go(func() {
			delay, reason := time.Duration(0), ReasonStartup
			if t, ok := last[prov.Name()]; ok {
				if next := t.Add(p.interval); time.Until(next) > 0 {
					delay, reason = time.Until(next), ReasonScheduled
					p.logNext(prov, next)
				}
			}
			timer := time.NewTimer(delay)
			defer timer.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-stop:
					return
				case <-timer.C:
				}
				p.PollProvider(ctx, prov, reason)
				reason = ReasonScheduled
				timer.Reset(p.interval)
				p.logNext(prov, time.Now().Add(p.interval))
			}
		})
	}
	wg.Wait()
	p.triggered.Wait()
}

func (p *Poller) logNext(prov weather.Provider, at time.Time) {
	p.log.Info("next provider poll scheduled", "provider", prov.Name(),
		"interval", p.interval, "next_poll_at", at.UTC().Format(time.RFC3339))
}

// Trigger starts an out-of-schedule poll of prov in the background.
func (p *Poller) Trigger(ctx context.Context, prov weather.Provider) {
	p.triggered.Go(func() { p.PollProvider(ctx, prov, ReasonManual) })
}

// Providers returns the enabled providers.
func (p *Poller) Providers() []weather.Provider { return p.providers }

// Lookup returns the provider with the given instance name.
func (p *Poller) Lookup(name string) (weather.Provider, bool) {
	for _, prov := range p.providers {
		if prov.Name() == name {
			return prov, true
		}
	}
	return nil, false
}

// PollProvider fetches all locations from one provider and records each result.
// Start, every location result and a final summary are logged.
// It returns the number of failed (provider, location) fetches.
func (p *Poller) PollProvider(ctx context.Context, prov weather.Provider, reason Reason) (failures int) {
	m := p.mu[prov.Name()]
	m.Lock()
	defer m.Unlock()

	p.setStatus(prov.Name(), PollStatus{Polling: true})

	plog := p.log.With("provider", prov.Name(), "provider_type", prov.Type(), "reason", reason)
	plog.Info("provider poll started", "locations", len(p.locations))
	start := time.Now()
	attempted, forecasts := 0, 0
	var firstErr string
	defer func() {
		plog.Info("provider poll finished", "locations_ok", attempted-failures, "locations_failed", failures,
			"locations_skipped", len(p.locations)-attempted, "forecasts", forecasts, "duration", time.Since(start))
		p.setStatus(prov.Name(), PollStatus{PolledAt: time.Now().UTC(), Error: firstErr})
	}()

	if bp, ok := prov.(weather.BatchProvider); ok {
		batchCtx, cancel := context.WithTimeout(ctx, p.timeout)
		batchStart := time.Now()
		batchResults, batchErr := bp.FetchAll(batchCtx, p.locations)
		batchEnd := time.Now()
		cancel()
		for _, loc := range p.locations {
			if ctx.Err() != nil {
				return failures
			}
			attempted++
			res := fetchResult{
				Provider:     prov.Name(),
				ProviderType: prov.Type(),
				Location:     loc,
				StartedAt:    batchStart,
				FinishedAt:   batchEnd,
			}
			if batchErr != nil {
				res.Err = batchErr
			} else if lr, ok := batchResults[loc.Name]; ok {
				res.Forecasts, res.Err = lr.Forecasts, lr.Err
			}
			log := plog.With("location", loc.Name)
			if res.Err != nil {
				failures++
				if firstErr == "" {
					firstErr = res.Err.Error()
				}
				log.Warn("location poll failed", "error", res.Err, "duration", batchEnd.Sub(batchStart))
			} else {
				forecasts += len(res.Forecasts)
				log.Info("location poll ok", "forecasts", len(res.Forecasts), "duration", batchEnd.Sub(batchStart))
			}
			if res.Err == nil {
				if err := p.record(ctx, prov, res); err != nil {
					failures++
					log.Error("record readings", "error", err)
				}
			}
		}
		return failures
	}

	for _, loc := range p.locations {
		if ctx.Err() != nil {
			return failures
		}
		attempted++
		res := p.fetch(ctx, prov, loc)
		log := plog.With("location", loc.Name)
		if res.Err != nil {
			failures++
			if firstErr == "" {
				firstErr = res.Err.Error()
			}
			log.Warn("location poll failed", "error", res.Err, "duration", res.FinishedAt.Sub(res.StartedAt))
		} else {
			forecasts += len(res.Forecasts)
			log.Info("location poll ok", "forecasts", len(res.Forecasts), "duration", res.FinishedAt.Sub(res.StartedAt))
		}
		if res.Err == nil {
			if err := p.record(ctx, prov, res); err != nil {
				failures++
				log.Error("record readings", "error", err)
			}
		}
	}
	return failures
}

type fetchResult struct {
	Provider, ProviderType string
	Location               weather.Location
	StartedAt, FinishedAt  time.Time
	Forecasts              []weather.Forecast
	Err                    error
}

// record stores the readings of result. Readings of a weather.ConditionSource are not
// stored: they replace the location's condition sources and fill stored observations.
// Otherwise, observations lacking a condition are filled from the current sources.
func (p *Poller) record(ctx context.Context, prov weather.Provider, result fetchResult) error {
	ctx = context.WithoutCancel(ctx)
	loc := result.Location.Name
	if _, ok := prov.(weather.ConditionSource); ok {
		p.sourcesMu.Lock()
		p.sources[loc] = result.Forecasts
		p.sourcesMu.Unlock()
		p.fillConditions(ctx, result.Provider, loc)
		return nil
	}
	readings := make([]store.ReadingRecord, 0, len(result.Forecasts))
	observations := false
	for _, forecast := range result.Forecasts {
		if forecast.Kind == weather.KindForecast && forecast.ForecastTime.Before(result.StartedAt) {
			continue
		}
		observations = observations || forecast.Kind == weather.KindObservation
		readings = append(readings, store.ReadingRecord{
			Provider: result.Provider, ProviderType: result.ProviderType,
			LocationName: loc, PolledAt: result.StartedAt,
			Forecast: forecast,
		})
	}
	if err := p.store.RecordReadings(ctx, readings); err != nil {
		return err
	}
	if observations {
		p.fillConditions(ctx, result.Provider, loc)
	}
	return nil
}

// fillConditions fills stored observations of loc lacking a condition from its
// condition sources. Failures are logged, not returned.
func (p *Poller) fillConditions(ctx context.Context, provider, loc string) {
	p.sourcesMu.Lock()
	sources := p.sources[loc]
	p.sourcesMu.Unlock()
	if len(sources) == 0 {
		return
	}
	log := p.log.With("provider", provider, "location", loc)
	n, err := p.store.FillObservationConditions(ctx, loc, sources, conditionMaxGap)
	if err != nil {
		log.Warn("fill observation conditions", "error", err)
		return
	}
	if n > 0 {
		log.Info("observation conditions filled", "readings", n)
	}
}

func (p *Poller) fetch(ctx context.Context, prov weather.Provider, loc weather.Location) fetchResult {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	res := fetchResult{
		Provider:     prov.Name(),
		ProviderType: prov.Type(),
		Location:     loc,
		StartedAt:    time.Now().UTC(),
	}
	res.Forecasts, res.Err = prov.Fetch(ctx, loc)
	res.FinishedAt = time.Now().UTC()
	if res.Err != nil {
		res.Forecasts = nil
	}
	return res
}
