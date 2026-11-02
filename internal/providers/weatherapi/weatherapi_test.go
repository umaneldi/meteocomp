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
           "vis_km":4,"gust_kph":19,"chance_of_rain":10,"chance_of_snow":65,"uv":0}]
}]}}`

func TestFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "k" || r.URL.Query().Get("q") != "48.85,2.35" || r.URL.Query().Get("days") != "3" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		w.Write([]byte(fixture))
	}))
	defer srv.Close()

	p, err := New(config.ProviderConfig{Name: "wa", APIKey: "k", BaseURL: srv.URL}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Fetch(context.Background(), weather.Location{Latitude: 48.85, Longitude: 2.35})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d forecasts, want 2", len(got))
	}
	h := got[0]
	if h.Granularity != weather.GranularityHourly || h.Condition != weather.ConditionSnow || *h.PrecipitationProbabilityPct != 65 || *h.SnowfallCm != 0.5 {
		t.Errorf("hourly: %+v", h)
	}
	d := got[1]
	if d.Granularity != weather.GranularityDaily || !d.ForecastTime.Equal(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("daily time: %+v", d)
	}
	if *d.TemperatureMaxC != 12.1 || *d.PrecipitationProbabilityPct != 70 || d.Condition != weather.ConditionRain || d.ConditionText != "Moderate rain" {
		t.Errorf("daily values: %+v", d)
	}
}
