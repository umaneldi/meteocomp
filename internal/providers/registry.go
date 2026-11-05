// Package providers holds the registry of weather provider implementations.
//
// Each implementation lives in its own sub-package and registers a factory
// from an init function. Import the sub-packages (see all.go) to make them available.
package providers

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/yanndethe/meteocomp/internal/config"
	"github.com/yanndethe/meteocomp/internal/weather"
)

// Factory builds a provider instance from its configuration.
type Factory func(cfg config.ProviderConfig, client *http.Client) (weather.Provider, error)

// Info describes a provider type for the web UI.
type Info struct {
	Type           string `json:"type"`
	Description    string `json:"description"`
	RequiresAPIKey bool   `json:"requires_api_key"`
	SignupURL      string `json:"signup_url,omitempty"` // where to obtain an API key
	// APIKeyEnv names an environment variable that, when set, provides the API key
	// for every instance of this type, overriding the key stored in the settings.
	APIKeyEnv string `json:"api_key_env,omitempty"`
	// APIKeyEnvSet is computed by Infos: whether APIKeyEnv is currently set.
	APIKeyEnvSet bool     `json:"api_key_env_set"`
	Options      []Option `json:"options"`
}

// Option documents one provider-specific option.
type Option struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"` // "int" or "string"
	Default     string `json:"default,omitempty"`
	Description string `json:"description"`
}

var (
	mu        sync.RWMutex
	factories = map[string]Factory{}
	infos     = map[string]Info{}
)

// Register makes a provider type available. It panics on duplicate registration.
func Register(typ string, f Factory, info Info) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := factories[typ]; dup {
		panic("providers: duplicate registration of " + typ)
	}
	info.Type = typ
	if info.Options == nil {
		info.Options = []Option{}
	}
	factories[typ] = f
	infos[typ] = info
}

// Infos describes the registered provider types, sorted by type.
func Infos() []Info {
	out := []Info{}
	for _, t := range Types() {
		mu.RLock()
		info := infos[t]
		mu.RUnlock()
		_, _, info.APIKeyEnvSet = EnvAPIKey(t)
		out = append(out, info)
	}
	return out
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

// EnvAPIKey returns the name of the environment variable providing the API key
// for provider type typ, if that variable is set and non-empty.
func EnvAPIKey(typ string) (name, value string, ok bool) {
	mu.RLock()
	info := infos[typ]
	mu.RUnlock()
	if info.APIKeyEnv == "" {
		return "", "", false
	}
	if v := os.Getenv(info.APIKeyEnv); v != "" {
		return info.APIKeyEnv, v, true
	}
	return info.APIKeyEnv, "", false
}

// New builds a provider instance from cfg. An API key set in the type's
// environment variable (see Info.APIKeyEnv) overrides cfg.APIKey.
func New(cfg config.ProviderConfig) (weather.Provider, error) {
	mu.RLock()
	f, ok := factories[cfg.Type]
	mu.RUnlock()
	if _, key, ok := EnvAPIKey(cfg.Type); ok {
		cfg.APIKey = key
	}
	if !ok {
		return nil, fmt.Errorf("provider %q: unknown type %q (available: %v)", cfg.Name, cfg.Type, Types())
	}
	return f(cfg, &http.Client{Timeout: 30 * time.Second})
}

// Build instantiates every enabled provider in s. Providers that cannot be
// built (e.g. unknown type or missing API key) are skipped and their errors joined.
func Build(s config.Settings) ([]weather.Provider, error) {
	var out []weather.Provider
	var errs []error
	for _, pc := range s.Providers {
		if !pc.IsEnabled() {
			continue
		}
		if pc.Options == nil {
			pc.Options = map[string]any{}
		}
		pc.Options["forecast_days"] = s.ForecastDays
		p, err := New(pc)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, p)
	}
	return out, errors.Join(errs...)
}
