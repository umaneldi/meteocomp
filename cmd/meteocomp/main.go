// Command meteocomp polls the configured weather providers, stores every
// forecast in a database and serves them over an HTTP API.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/yanndethe/meteocomp/internal/api"
	"github.com/yanndethe/meteocomp/internal/config"
	"github.com/yanndethe/meteocomp/internal/poller"
	"github.com/yanndethe/meteocomp/internal/providers"
	_ "github.com/yanndethe/meteocomp/internal/providers/all"
	"github.com/yanndethe/meteocomp/internal/store"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to the YAML configuration file")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(*configPath, log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(configPath string, log *slog.Logger) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.OpenSQLite(ctx, cfg.Database.Path)
	if err != nil {
		return err
	}
	defer st.Close()

	var jobs []poller.Job
	for _, pc := range cfg.Providers {
		if !pc.IsEnabled() {
			log.Info("provider disabled", "provider", pc.Name)
			continue
		}
		p, err := providers.New(pc)
		if err != nil {
			return err
		}
		jobs = append(jobs, poller.Job{Provider: p, Interval: pc.Interval})
		log.Info("provider enabled", "provider", pc.Name, "type", pc.Type, "interval", pc.Interval)
	}
	if len(jobs) == 0 {
		log.Warn("no enabled providers; only the HTTP API will run")
	}

	pl := poller.New(jobs, cfg.Locations, st, log)
	srv := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           api.New(ctx, cfg, st, pl, log).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	var wg sync.WaitGroup
	wg.Go(func() { pl.Run(ctx) })

	errc := make(chan error, 1)
	go func() {
		log.Info("http server listening", "addr", cfg.HTTP.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- fmt.Errorf("http server: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err = <-errc:
		stop()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	wg.Wait()
	return err
}
