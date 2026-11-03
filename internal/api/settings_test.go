package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yanndethe/meteocomp/internal/config"
	"github.com/yanndethe/meteocomp/internal/poller"
	_ "github.com/yanndethe/meteocomp/internal/providers/all"
	"github.com/yanndethe/meteocomp/internal/store"
)

// newTestServer starts a server with no locations, so no provider is ever fetched.
func newTestServer(t *testing.T) (*httptest.Server, *store.SQLDB) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	st, err := store.OpenSQLite(ctx, filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	pm := poller.NewManager(ctx)
	srv := New(ctx, st, pm, slog.New(slog.NewTextHandler(io.Discard, nil)))
	initial := config.Settings{
		PollInterval: time.Hour,
		Providers: []config.ProviderConfig{
			{Name: "owm", Type: "openweathermap", Enabled: new(false)},
		},
	}
	if err := srv.ApplySettings(ctx, initial, true, true); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		cancel()
		pm.Wait()
		st.Close()
	})
	return ts, st
}

func put(t *testing.T, ts *httptest.Server, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/settings", strings.NewReader(body))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestSettingsAPI(t *testing.T) {
	ts, st := newTestServer(t)
	ctx := context.Background()

	// Enabling a keyed provider without a key is rejected and nothing is saved.
	code, body := put(t, ts, `{"poll_interval":"30m","locations":[],"providers":[{"name":"owm","type":"openweathermap","enabled":true}]}`)
	if code != http.StatusBadRequest || !strings.Contains(body, "api_key") {
		t.Fatalf("missing key: %d %s", code, body)
	}

	// Provision a key; the response must not echo it.
	const key = "abcdef0123456789"
	code, body = put(t, ts, `{"poll_interval":"30m",
		"locations":[{"name":"lyon","city":"Lyon","country":"FR","latitude":45.76,"longitude":4.83}],
		"providers":[{"name":"owm","type":"openweathermap","enabled":true,"api_key":"`+key+`"}]}`)
	if code != http.StatusOK {
		t.Fatalf("save: %d %s", code, body)
	}
	if strings.Contains(body, key) {
		t.Errorf("API key leaked in response: %s", body)
	}
	var doc settingsDoc
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.PollInterval != "30m0s" || len(doc.Locations) != 1 || !doc.Providers[0].HasAPIKey || doc.Providers[0].APIKeyHint != "…6789" {
		t.Errorf("unexpected response: %+v", doc)
	}

	// Renaming without sending the key keeps it.
	code, body = put(t, ts, `{"poll_interval":"1h","locations":[],
		"providers":[{"name":"owm2","previous_name":"owm","type":"openweathermap","enabled":true}]}`)
	if code != http.StatusOK {
		t.Fatalf("rename: %d %s", code, body)
	}
	saved, _, err := st.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Providers[0].Name != "owm2" || saved.Providers[0].APIKey != key || saved.PollInterval != time.Hour {
		t.Errorf("stored settings: %+v", saved)
	}

	// An empty key removes it (and the provider must then be disabled).
	code, _ = put(t, ts, `{"poll_interval":"1h","locations":[],
		"providers":[{"name":"owm2","type":"openweathermap","enabled":false,"api_key":""}]}`)
	if code != http.StatusOK {
		t.Fatalf("clear key: %d", code)
	}
	if saved, _, _ = st.LoadSettings(ctx); saved.Providers[0].APIKey != "" {
		t.Errorf("key not cleared")
	}

	// Validation errors.
	for _, bad := range []string{
		`{"poll_interval":"10s","locations":[],"providers":[]}`,
		`{"poll_interval":"nope","locations":[],"providers":[]}`,
		`{"poll_interval":"1h","locations":[{"name":"x","latitude":120}],"providers":[]}`,
		`{"poll_interval":"1h","locations":[],"providers":[{"name":"a","type":"nosuchtype","enabled":true}]}`,
	} {
		if code, body := put(t, ts, bad); code != http.StatusBadRequest {
			t.Errorf("%s: got %d %s", bad, code, body)
		}
	}

	// Live settings are reflected by the read-only endpoints.
	res, err := http.Get(ts.URL + "/api/v1/providers")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !bytes.Contains(b, []byte(`"name":"owm2"`)) || !bytes.Contains(b, []byte(`"interval":"1h0m0s"`)) {
		t.Errorf("providers: %s", b)
	}
}

func TestUIAndProviderTypes(t *testing.T) {
	ts, _ := newTestServer(t)
	for path, want := range map[string]string{
		"/":                      "<title>meteocomp",
		"/app.js":                "/api/v1/settings",
		"/api/v1/provider-types": `"requires_api_key":true`,
	} {
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK || !strings.Contains(string(b), want) {
			t.Errorf("%s: %d, missing %q", path, res.StatusCode, want)
		}
	}
}

// An API key from the environment overrides the stored key: a keyed provider can
// be enabled without a stored key, and the env source is reported.
func TestSettingsAPIEnvKey(t *testing.T) {
	t.Setenv("OWM_API_KEY", "env-key-0000")
	ts, st := newTestServer(t)

	code, body := put(t, ts, `{"poll_interval":"1h","locations":[],"providers":[{"name":"owm","type":"openweathermap","enabled":true}]}`)
	if code != http.StatusOK {
		t.Fatalf("enable with env key: %d %s", code, body)
	}
	if strings.Contains(body, "env-key-0000") {
		t.Errorf("env key leaked: %s", body)
	}
	var doc settingsDoc
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	if p := doc.Providers[0]; p.APIKeyEnv != "OWM_API_KEY" || p.HasAPIKey {
		t.Errorf("provider doc: %+v", p)
	}
	// The env key is not persisted.
	if saved, _, _ := st.LoadSettings(context.Background()); saved.Providers[0].APIKey != "" {
		t.Errorf("env key persisted")
	}

	res, err := http.Get(ts.URL + "/api/v1/provider-types")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(b), `"api_key_env":"OWM_API_KEY","api_key_env_set":true`) ||
		!strings.Contains(string(b), `"api_key_env":"WEATHER_API_KEY","api_key_env_set":false`) {
		t.Errorf("provider types: %s", b)
	}
}
