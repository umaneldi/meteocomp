package openweathermap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yanndethe/meteocomp/internal/config"
	"github.com/yanndethe/meteocomp/internal/weather"
)

const fixture = `{"list":[
 {"dt":1762041600,"main":{"temp":9.5,"feels_like":7.1,"temp_min":9.0,"temp_max":10.2,"pressure":1012,"humidity":88},
  "weather":[{"id":500,"description":"light rain"}],"clouds":{"all":90},
  "wind":{"speed":5,"deg":220,"gust":10},"visibility":10000,"pop":0.6,"rain":{"3h":1.2}},
 {"dt":1762052400,"main":{"temp":-1},"weather":[{"id":612,"description":"light shower sleet"}],"wind":{},"snow":{"3h":2.5}},
 {"dt":1762063200,"main":{"temp":0,"feels_like":0},"wind":{"speed":0,"gust":0,"deg":0},"visibility":0,"pop":0},
 {"dt":1762074000,"main":{"temp":null},"wind":{"speed":null,"gust":null},"visibility":null,"pop":null}
]}`

func TestFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("appid") != "k" || r.URL.Query().Get("units") != "metric" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		w.Write([]byte(fixture))
	}))
	defer srv.Close()

	if _, err := New(config.ProviderConfig{Name: "owm"}, nil); err == nil {
		t.Error("expected error without api_key")
	}
	p, _ := New(config.ProviderConfig{Name: "owm", APIKey: "k", BaseURL: srv.URL}, srv.Client())
	got, err := p.Fetch(context.Background(), weather.Location{Latitude: 1, Longitude: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d forecasts, want 4", len(got))
	}
	f := got[0]
	if f.ForecastTime.Unix() != 1762041600 || f.Kind != weather.KindForecast {
		t.Errorf("time/kind: %+v", f)
	}
	if *f.WindSpeedKmh != 18 || *f.WindGustKmh != 36 || *f.VisibilityKm != 10 || *f.PrecipitationProbabilityPct != 60 ||
		*f.TemperatureC != 9.5 || *f.FeelsLikeC != 7.1 || *f.PressureHPa != 1012 ||
		*f.HumidityPct != 88 || *f.WindDirectionDeg != 220 || *f.CloudCoverPct != 90 {
		t.Errorf("converted values: %+v", f)
	}
	if f.Condition != weather.ConditionRain || f.ConditionText != "light rain" {
		t.Errorf("condition: %+v", f)
	}
	if got[1].Condition != weather.ConditionSleet || got[1].WindSpeedKmh != nil {
		t.Errorf("second entry: %+v", got[1])
	}
	z := got[2]
	for _, v := range []*float64{z.TemperatureC, z.FeelsLikeC, z.WindSpeedKmh, z.WindGustKmh, z.WindDirectionDeg, z.VisibilityKm, z.PrecipitationProbabilityPct} {
		if v == nil || *v != 0 {
			t.Errorf("zero measurement lost: %v", v)
		}
	}
	m := got[3]
	if m.TemperatureC != nil || m.WindSpeedKmh != nil || m.WindGustKmh != nil ||
		m.VisibilityKm != nil || m.PrecipitationProbabilityPct != nil || m.Condition != weather.ConditionUnknown {
		t.Errorf("missing measurements: %+v", m)
	}
	if got[1].SnowfallCm == nil || *got[1].SnowfallCm != 2.5 {
		t.Errorf("snowfall from snow.3h: %v", got[1].SnowfallCm)
	}
	for i, f := range got {
		if f.Kind != weather.KindForecast || f.DewPointC != nil || (i != 1 && f.SnowfallCm != nil) {
			t.Errorf("kind or unsupported measurements: %+v", f)
		}
	}
}
