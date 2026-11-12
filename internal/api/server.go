// Package api exposes the stored polls and forecasts over HTTP/JSON.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/yanndethe/meteocomp/internal/config"
	"github.com/yanndethe/meteocomp/internal/poller"
	"github.com/yanndethe/meteocomp/internal/store"
	"github.com/yanndethe/meteocomp/internal/version"
	"github.com/yanndethe/meteocomp/internal/weather"
	"github.com/yanndethe/meteocomp/internal/web"
)

const (
	defaultLimit = 500
	maxLimit     = 10000
)

type Server struct {
	store    store.Store
	pollers  *poller.Manager
	log      *slog.Logger
	baseCtx  context.Context // lifetime of the service; used for manually triggered polls
	stations stationCache

	// mu guards settings and serializes ApplySettings.
	mu       sync.RWMutex
	settings config.Settings
}

// New returns a Server. Call ApplySettings before serving.
func New(baseCtx context.Context, st store.Store, pm *poller.Manager, log *slog.Logger) *Server {
	return &Server{store: st, pollers: pm, log: log, baseCtx: baseCtx}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", web.Handler())
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /api/v1/version", s.getVersion)
	mux.HandleFunc("GET /api/v1/settings", s.getSettings)
	mux.HandleFunc("PUT /api/v1/settings", s.putSettings)
	mux.HandleFunc("GET /api/v1/settings/export", s.exportSettings)
	mux.HandleFunc("POST /api/v1/settings/import", s.importSettings)
	mux.HandleFunc("GET /api/v1/provider-types", s.listProviderTypes)
	mux.HandleFunc("GET /api/v1/providers", s.listProviders)
	mux.HandleFunc("GET /api/v1/locations", s.listLocations)
	mux.HandleFunc("POST /api/v1/polls", s.triggerPoll)
	mux.HandleFunc("GET /api/v1/poll-status", s.pollStatus)
	mux.HandleFunc("GET /api/v1/readings", s.listReadings)
	mux.HandleFunc("GET /api/v1/stations", s.listStations)
	return mux
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) getVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": version.Version})
}

type providerView struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Interval string `json:"interval"`
	Enabled  bool   `json:"enabled"`
}

func (s *Server) listProviders(w http.ResponseWriter, _ *http.Request) {
	cur := s.Settings()
	out := make([]providerView, 0, len(cur.Providers))
	for _, p := range cur.Providers {
		out = append(out, providerView{Name: p.Name, Type: p.Type, Interval: cur.PollInterval.String(), Enabled: p.IsEnabled()})
	}
	writeJSON(w, http.StatusOK, out)
}

type locationView struct {
	weather.Location
	LatestForecastTime *time.Time `json:"latest_forecast_time"`
}

func (s *Server) listLocations(w http.ResponseWriter, r *http.Request) {
	locs := s.Settings().Locations
	if r.URL.Query().Get("latest") != "true" {
		writeJSON(w, http.StatusOK, locs)
		return
	}
	times, err := s.store.LatestObservationTimes(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	out := make([]locationView, len(locs))
	for i, loc := range locs {
		var ft *time.Time
		if t, ok := times[loc.Name]; ok {
			ft = &t
		}
		out[i] = locationView{Location: loc, LatestForecastTime: ft}
	}
	writeJSON(w, http.StatusOK, out)
}

// triggerPoll starts an immediate poll of one provider (?provider=name) or all
// enabled providers, in the background. It responds 202 Accepted.
func (s *Server) triggerPoll(w http.ResponseWriter, r *http.Request) {
	pl := s.pollers.Current()
	if pl == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("poller not started"))
		return
	}
	var targets []weather.Provider
	if name := r.URL.Query().Get("provider"); name != "" {
		p, ok := pl.Lookup(name)
		if !ok {
			writeError(w, http.StatusNotFound, fmt.Errorf("unknown or disabled provider %q", name))
			return
		}
		targets = append(targets, p)
	} else {
		targets = pl.Providers()
	}
	names := make([]string, 0, len(targets))
	for _, p := range targets {
		names = append(names, p.Name())
		pl.Trigger(s.baseCtx, p)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "started", "providers": names})
}

func (s *Server) pollStatus(w http.ResponseWriter, _ *http.Request) {
	pl := s.pollers.Current()
	if pl == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	type view struct {
		Name     string     `json:"name"`
		PolledAt *time.Time `json:"polled_at,omitempty"`
		Error    string     `json:"error,omitempty"`
		Polling  bool       `json:"polling"`
	}
	statuses := pl.Statuses()
	out := make([]view, 0, len(statuses))
	for name, ps := range statuses {
		var polledAt *time.Time
		if !ps.PolledAt.IsZero() {
			polledAt = &ps.PolledAt
		}
		out = append(out, view{name, polledAt, ps.Error, ps.Polling})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listReadings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ReadingFilter{
		Provider: q.Get("provider"),
		Location: q.Get("location"),
		Kind:     q.Get("kind"),
	}
	for _, removed := range []string{"order", "before_id", "poll_id", "granularity"} {
		if q.Has(removed) {
			writeError(w, http.StatusBadRequest, fmt.Errorf("%s is no longer supported; use limit and offset for pagination", removed))
			return
		}
	}
	var err error
	if q.Has("offset") {
		f.Offset, err = strconv.Atoi(q.Get("offset"))
		if err != nil || f.Offset < 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("offset must be a non-negative integer"))
			return
		}
	}
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
	if v := q.Get("forecast_time"); v != "" {
		f.ForecastTime, err = time.Parse(time.RFC3339Nano, v)
		if err != nil {
			f.ForecastTime, err = time.Parse(time.RFC3339, v)
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("forecast_time: expected RFC3339 time, got %q", v))
			return
		}
	}
	out, err := s.store.ListReadings(r.Context(), f)
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
