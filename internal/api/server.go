// Package api exposes the stored polls and forecasts over HTTP/JSON.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/yanndethe/meteocomp/internal/config"
	"github.com/yanndethe/meteocomp/internal/poller"
	"github.com/yanndethe/meteocomp/internal/store"
	"github.com/yanndethe/meteocomp/internal/weather"
)

const (
	defaultLimit = 500
	maxLimit     = 10000
)

type Server struct {
	cfg     *config.Config
	store   store.Store
	poller  *poller.Poller
	log     *slog.Logger
	baseCtx context.Context // lifetime of the service; used for manually triggered polls
}

func New(baseCtx context.Context, cfg *config.Config, st store.Store, p *poller.Poller, log *slog.Logger) *Server {
	return &Server{cfg: cfg, store: st, poller: p, log: log, baseCtx: baseCtx}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /api/v1/providers", s.listProviders)
	mux.HandleFunc("GET /api/v1/locations", s.listLocations)
	mux.HandleFunc("GET /api/v1/polls", s.listPolls)
	mux.HandleFunc("POST /api/v1/polls", s.triggerPoll)
	mux.HandleFunc("GET /api/v1/forecasts", s.listForecasts)
	return mux
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type providerView struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Interval string `json:"interval"`
	Enabled  bool   `json:"enabled"`
}

func (s *Server) listProviders(w http.ResponseWriter, _ *http.Request) {
	out := make([]providerView, 0, len(s.cfg.Providers))
	for _, p := range s.cfg.Providers {
		out = append(out, providerView{Name: p.Name, Type: p.Type, Interval: p.Interval.String(), Enabled: p.IsEnabled()})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listLocations(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.Locations)
}

func (s *Server) listPolls(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, err := parseLimit(q.Get("limit"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	polls, err := s.store.ListPolls(r.Context(), store.PollFilter{
		Provider: q.Get("provider"),
		Location: q.Get("location"),
		Status:   q.Get("status"),
		Limit:    limit,
	})
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, polls)
}

// triggerPoll starts an immediate poll of one provider (?provider=name) or all
// enabled providers, in the background. It responds 202 Accepted.
func (s *Server) triggerPoll(w http.ResponseWriter, r *http.Request) {
	var targets []weather.Provider
	if name := r.URL.Query().Get("provider"); name != "" {
		p, ok := s.poller.Lookup(name)
		if !ok {
			writeError(w, http.StatusNotFound, fmt.Errorf("unknown or disabled provider %q", name))
			return
		}
		targets = append(targets, p)
	} else {
		for _, j := range s.poller.Jobs() {
			targets = append(targets, j.Provider)
		}
	}
	names := make([]string, 0, len(targets))
	for _, p := range targets {
		names = append(names, p.Name())
		s.poller.Trigger(s.baseCtx, p)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "started", "providers": names})
}

func (s *Server) listForecasts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ForecastFilter{
		Provider:    q.Get("provider"),
		Location:    q.Get("location"),
		Granularity: q.Get("granularity"),
	}
	var err error
	if f.Limit, err = parseLimit(q.Get("limit")); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	for key, dst := range map[string]*time.Time{
		"from": &f.From, "to": &f.To, "polled_from": &f.PolledFrom, "polled_to": &f.PolledTo,
	} {
		if v := q.Get(key); v != "" {
			if *dst, err = time.Parse(time.RFC3339, v); err != nil {
				writeError(w, http.StatusBadRequest, fmt.Errorf("%s: expected RFC3339 time, got %q", key, v))
				return
			}
		}
	}
	if v := q.Get("poll_id"); v != "" {
		if f.PollID, err = strconv.ParseInt(v, 10, 64); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("poll_id: %w", err))
			return
		}
	}
	out, err := s.store.ListForecasts(r.Context(), f)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func parseLimit(v string) (int, error) {
	if v == "" {
		return defaultLimit, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > maxLimit {
		return 0, fmt.Errorf("limit must be an integer between 1 and %d", maxLimit)
	}
	return n, nil
}

func (s *Server) internalError(w http.ResponseWriter, err error) {
	s.log.Error("request failed", "error", err)
	writeError(w, http.StatusInternalServerError, fmt.Errorf("internal error"))
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
