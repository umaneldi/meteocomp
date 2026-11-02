// Package providers holds the registry of weather provider implementations.
//
// Each implementation lives in its own sub-package and registers a factory
// from an init function. Import the sub-packages (see all.go) to make them available.
package providers

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/yanndethe/meteocomp/internal/config"
	"github.com/yanndethe/meteocomp/internal/weather"
)

// Factory builds a provider instance from its configuration.
type Factory func(cfg config.ProviderConfig, client *http.Client) (weather.Provider, error)

var (
	mu        sync.RWMutex
	factories = map[string]Factory{}
)

// Register makes a provider type available. It panics on duplicate registration.
func Register(typ string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := factories[typ]; dup {
		panic("providers: duplicate registration of " + typ)
	}
	factories[typ] = f
}

// Types lists the registered provider types.
func Types() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(factories))
	for t := range factories {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// New builds a provider instance from cfg.
func New(cfg config.ProviderConfig) (weather.Provider, error) {
	mu.RLock()
	f, ok := factories[cfg.Type]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("provider %q: unknown type %q (available: %v)", cfg.Name, cfg.Type, Types())
	}
	return f(cfg, &http.Client{Timeout: 30 * time.Second})
}
