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

// Job is a provider together with its polling interval.
type Job struct {
	Provider weather.Provider
	Interval time.Duration
}

// Poller runs one goroutine per provider.
type Poller struct {
	jobs      []Job
	locations []weather.Location
	store     store.Store
	log       *slog.Logger
	timeout   time.Duration

	// mu serializes polls of the same provider (ticker vs. manual trigger).
	mu map[string]*sync.Mutex
	// triggered tracks polls started by Trigger so Run can wait for them on shutdown.
	triggered sync.WaitGroup
}

func New(jobs []Job, locations []weather.Location, st store.Store, log *slog.Logger) *Poller {
	mu := make(map[string]*sync.Mutex, len(jobs))
	for _, j := range jobs {
		mu[j.Provider.Name()] = &sync.Mutex{}
	}
	return &Poller{jobs: jobs, locations: locations, store: st, log: log, timeout: time.Minute, mu: mu}
}

// Run polls each provider immediately, then at its interval, until ctx is done.
func (p *Poller) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, j := range p.jobs {
		wg.Go(func() {
			p.PollProvider(ctx, j.Provider)
			t := time.NewTicker(j.Interval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					p.PollProvider(ctx, j.Provider)
				}
			}
		})
	}
	wg.Wait()
	p.triggered.Wait()
}

// Trigger starts an out-of-schedule poll of prov in the background.
func (p *Poller) Trigger(ctx context.Context, prov weather.Provider) {
	p.triggered.Go(func() { p.PollProvider(ctx, prov) })
}

// Jobs returns the configured provider jobs.
func (p *Poller) Jobs() []Job { return p.jobs }

// Lookup returns the provider with the given instance name.
func (p *Poller) Lookup(name string) (weather.Provider, bool) {
	for _, j := range p.jobs {
		if j.Provider.Name() == name {
			return j.Provider, true
		}
	}
	return nil, false
}

// PollProvider fetches all locations from one provider and records each result.
// It returns the number of failed (provider, location) fetches.
func (p *Poller) PollProvider(ctx context.Context, prov weather.Provider) (failures int) {
	m := p.mu[prov.Name()]
	m.Lock()
	defer m.Unlock()

	for _, loc := range p.locations {
		if ctx.Err() != nil {
			return failures
		}
		res := p.fetch(ctx, prov, loc)
		log := p.log.With("provider", prov.Name(), "location", loc.Name)
		if res.Err != nil {
			failures++
			log.Warn("poll failed", "error", res.Err)
		} else {
			log.Info("poll ok", "forecasts", len(res.Forecasts), "duration", res.FinishedAt.Sub(res.StartedAt))
		}
		// Record even if ctx was cancelled mid-fetch so the attempt is not lost.
		if _, err := p.store.RecordPoll(context.WithoutCancel(ctx), res); err != nil {
			log.Error("record poll", "error", err)
		}
	}
	return failures
}

func (p *Poller) fetch(ctx context.Context, prov weather.Provider, loc weather.Location) store.PollResult {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	res := store.PollResult{
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
