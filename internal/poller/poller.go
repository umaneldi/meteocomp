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
}

func New(provs []weather.Provider, interval time.Duration, locations []weather.Location, st store.Store, log *slog.Logger) *Poller {
	mu := make(map[string]*sync.Mutex, len(provs))
	for _, p := range provs {
		mu[p.Name()] = &sync.Mutex{}
	}
	return &Poller{providers: provs, interval: interval, locations: locations, store: st, log: log,
		timeout: time.Minute, mu: mu}
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
// is closed. A provider whose last recorded poll is older than the interval (or
// that was never polled) is polled immediately; otherwise its schedule resumes
// from the last poll, so restarts and settings changes do not cause extra API calls.
// Closing stop lets in-flight polls finish; cancelling ctx aborts them.
func (p *Poller) RunUntil(ctx context.Context, stop <-chan struct{}) {
	p.log.Info("poller started", "providers", len(p.providers), "locations", len(p.locations), "interval", p.interval)
	last, err := p.store.LastPollTimes(ctx)
	if err != nil {
		p.log.Warn("read last poll times; polling all providers now", "error", err)
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

	plog := p.log.With("provider", prov.Name(), "provider_type", prov.Type(), "reason", reason)
	plog.Info("provider poll started", "locations", len(p.locations))
	start := time.Now()
	attempted, forecasts := 0, 0
	defer func() {
		plog.Info("provider poll finished", "locations_ok", attempted-failures, "locations_failed", failures,
			"locations_skipped", len(p.locations)-attempted, "forecasts", forecasts, "duration", time.Since(start))
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
			res := store.PollResult{
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
				log.Warn("location poll failed", "error", res.Err, "duration", batchEnd.Sub(batchStart))
			} else {
				forecasts += len(res.Forecasts)
				log.Info("location poll ok", "forecasts", len(res.Forecasts), "duration", batchEnd.Sub(batchStart))
			}
			if _, err := p.store.RecordPoll(context.WithoutCancel(ctx), res); err != nil {
				log.Error("record poll", "error", err)
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
			log.Warn("location poll failed", "error", res.Err, "duration", res.FinishedAt.Sub(res.StartedAt))
		} else {
			forecasts += len(res.Forecasts)
			log.Info("location poll ok", "forecasts", len(res.Forecasts), "duration", res.FinishedAt.Sub(res.StartedAt))
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
