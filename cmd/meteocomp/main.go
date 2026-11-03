// Command meteocomp polls the configured weather providers, stores every
// forecast in a database and serves them over an HTTP API and a settings web UI.
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
	"syscall"
	"time"

	"github.com/yanndethe/meteocomp/internal/api"
	"github.com/yanndethe/meteocomp/internal/config"
	"github.com/yanndethe/meteocomp/internal/poller"
	_ "github.com/yanndethe/meteocomp/internal/providers/all"
	"github.com/yanndethe/meteocomp/internal/store"
)

// defaultDatabaseURI is used when DATABASE_URI is not set.
const defaultDatabaseURI = "sqlite://meteocomp.db"

func main() {
	configPath := flag.String("config", os.Getenv("METEOCOMP_CONFIG"),
		"optional YAML file used to seed the settings of an empty database (env METEOCOMP_CONFIG)")
	addr := flag.String("addr", os.Getenv("METEOCOMP_HTTP_ADDR"), "HTTP listen address (env METEOCOMP_HTTP_ADDR, default :8080)")
	dbURI := flag.String("database-uri", os.Getenv("DATABASE_URI"),
		"database URI: postgres://user:pass@host/db or sqlite:///path/to.db (env DATABASE_URI, default "+defaultDatabaseURI+")")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(*configPath, *addr, *dbURI, log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func run(configPath, addr, dbURI string, log *slog.Logger) error {
	var seed *config.Config
	if configPath != "" {
		var err error
		if seed, err = config.Load(configPath); err != nil {
			return err
		}
		addr = firstNonEmpty(addr, seed.HTTP.Addr)
	}
	addr = firstNonEmpty(addr, ":8080")
	dbURI = firstNonEmpty(dbURI, defaultDatabaseURI)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, dbURI)
	if err != nil {
		return err
	}
	defer st.Close()

	pollers := poller.NewManager(ctx)
	apiSrv := api.New(ctx, st, pollers, log)

	// The database is the source of truth. The config file (or built-in defaults)
	// only seeds it on first start.
	saved, found, err := st.LoadSettings(ctx)
	switch {
	case err != nil:
		return err
	case found:
		if seed != nil {
			log.Info("settings already stored in the database; ignoring config file", "config", configPath)
		}
		err = apiSrv.ApplySettings(ctx, *saved, false, false)
	case seed != nil:
		log.Info("seeding settings from config file", "config", configPath)
		err = apiSrv.ApplySettings(ctx, seed.Settings, true, false)
	default:
		log.Info("no stored settings; starting with defaults, configure them in the web UI")
		err = apiSrv.ApplySettings(ctx, config.DefaultSettings(), true, false)
	}
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           apiSrv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("http server listening", "addr", addr)
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
	pollers.Wait()
	return err
}
