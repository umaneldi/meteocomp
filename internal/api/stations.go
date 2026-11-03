package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

const stationListURL = "https://www.infoclimat.fr/opendata/stations_xhr.php?format=geojson"

// Station is a weather station from the Infoclimat station list.
type Station struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Country   string  `json:"country"`
	Elevation float64 `json:"elevation"`
}

// stationCache lazily fetches and caches the Infoclimat station list.
type stationCache struct {
	mu       sync.RWMutex
	stations []Station
}

func (c *stationCache) get(ctx context.Context) ([]Station, error) {
	c.mu.RLock()
	if c.stations != nil {
		s := c.stations
		c.mu.RUnlock()
		return s, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stations != nil {
		return c.stations, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, stationListURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "meteocomp/0.1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch station list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch station list: http %d", resp.StatusCode)
	}

	var geo struct {
		Features []struct {
			Geometry struct {
				Coordinates [2]float64 `json:"coordinates"` // [lon, lat]
			} `json:"geometry"`
			Properties struct {
				ID        string  `json:"id"`
				Name      string  `json:"name"`
				Country   string  `json:"country"`
				Elevation float64 `json:"elevation"`
			} `json:"properties"`
		} `json:"features"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&geo); err != nil {
		return nil, fmt.Errorf("decode station list: %w", err)
	}

	stations := make([]Station, 0, len(geo.Features))
	for _, f := range geo.Features {
		if f.Properties.ID == "" {
			continue
		}
		stations = append(stations, Station{
			ID:        f.Properties.ID,
			Name:      f.Properties.Name,
			Longitude: f.Geometry.Coordinates[0],
			Latitude:  f.Geometry.Coordinates[1],
			Country:   f.Properties.Country,
			Elevation: f.Properties.Elevation,
		})
	}
	c.stations = stations
	return stations, nil
}

func (s *Server) listStations(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 2 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("q must be at least 2 characters"))
		return
	}

	all, err := s.stations.get(r.Context())
	if err != nil {
		s.log.Error("station list unavailable", "error", err)
		writeError(w, http.StatusBadGateway, fmt.Errorf("station list unavailable"))
		return
	}

	lower := strings.ToLower(q)
	out := make([]Station, 0, 20)
	for _, st := range all {
		if strings.EqualFold(st.ID, q) || strings.Contains(strings.ToLower(st.Name), lower) {
			out = append(out, st)
			if len(out) == 20 {
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}
