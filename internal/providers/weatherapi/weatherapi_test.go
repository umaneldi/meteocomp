package weatherapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yanndethe/meteocomp/internal/config"
	"github.com/yanndethe/meteocomp/internal/weather"
)

const fixture = `{"forecast":{"forecastday":[{
  "date":"2026-11-02",
  "day":{"maxtemp_c":12.1,"mintemp_c":4.3,"avgtemp_c":8,"maxwind_kph":20,"totalprecip_mm":3.2,"totalsnow_cm":0,
         "avghumidity":80,"daily_chance_of_rain":70,"daily_chance_of_snow":0,"condition":{"text":"Moderate rain","code":1189},"uv":1},
  "hour":[{"time_epoch":1762041600,"temp_c":6.1,"condition":{"text":"Light snow","code":1213},"wind_kph":11,"wind_degree":200,
           "pressure_mb":1015,"precip_mm":0.4,"snow_cm":0.5,"humidity":95,"cloud":100,"feelslike_c":3.2,"dewpoint_c":5,
           "vis_km":4,"gust_kph":19,"chance_of_rain":10,"chance_of_snow":65,"uv":0},
          {"time_epoch":1762045200,"temp_c":0,"wind_kph":0,"gust_kph":0,"vis_km":0,"chance_of_rain":0,"snow_cm":0},
          {"time_epoch":1762048800,"temp_c":null,"chance_of_rain":null,"chance_of_snow":100,"vis_km":null}]
}]}}`

func TestFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "k" || r.URL.Query().Get("q") != "48.85,2.35" || r.URL.Query().Get("days") != "3" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		w.Write([]byte(fixture))
	}))
	defer srv.Close()

	p, err := New(config.ProviderConfig{Name: "wa", APIKey: "k", BaseURL: srv.URL, Options: map[string]any{"forecast_days": 3}}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Fetch(context.Background(), weather.Location{Latitude: 48.85, Longitude: 2.35})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d forecasts, want 3 hourly forecasts", len(got))
	}
	h := got[0]
	if h.Kind != weather.KindForecast || h.Condition != weather.ConditionSnow ||
		h.ConditionText != "Light snow" || *h.PrecipitationProbabilityPct != 10 || *h.SnowfallCm != 0.5 ||
		*h.TemperatureC != 6.1 || *h.FeelsLikeC != 3.2 || *h.WindSpeedKmh != 11 ||
		*h.WindGustKmh != 19 || *h.PressureHPa != 1015 || *h.VisibilityKm != 4 ||
		*h.DewPointC != 5 || *h.HumidityPct != 95 || *h.CloudCoverPct != 100 || *h.WindDirectionDeg != 200 {
		t.Errorf("hourly: %+v", h)
	}
	if !h.ForecastTime.Equal(time.Unix(1762041600, 0).UTC()) {
		t.Errorf("hourly time: %v", h.ForecastTime)
	}
	z := got[1]
	for _, v := range []*float64{z.TemperatureC, z.WindSpeedKmh, z.WindGustKmh, z.VisibilityKm, z.PrecipitationProbabilityPct, z.SnowfallCm} {
		if v == nil || *v != 0 {
			t.Errorf("zero measurement lost: %v", v)
		}
	}
	m := got[2]
	if m.TemperatureC != nil || m.PrecipitationProbabilityPct != nil || m.VisibilityKm != nil ||
		m.SnowfallCm != nil || m.Condition != weather.ConditionUnknown {
		t.Errorf("missing measurements: %+v", m)
	}
	for _, f := range got {
		if f.Kind != weather.KindForecast {
			t.Errorf("kind: %q", f.Kind)
		}
	}
}
