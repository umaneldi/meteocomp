package meteofrance

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yanndethe/meteocomp/internal/config"
	"github.com/yanndethe/meteocomp/internal/weather"
)

// Run 12Z is only partially published (one coverage missing), so 09Z must be used.
const capsFixture = `<?xml version="1.0"?><wcs:Capabilities><wcs:Contents>
<wcs:CoverageSummary><wcs:CoverageId>TEMPERATURE__SPECIFIC_HEIGHT_LEVEL_ABOVE_GROUND___2026-11-02T09.00.00Z</wcs:CoverageId></wcs:CoverageSummary>
<wcs:CoverageSummary><wcs:CoverageId>TOTAL_PRECIPITATION__GROUND_OR_WATER_SURFACE___2026-11-02T09.00.00Z_PT3H</wcs:CoverageId></wcs:CoverageSummary>
<wcs:CoverageSummary><wcs:CoverageId>TEMPERATURE__SPECIFIC_HEIGHT_LEVEL_ABOVE_GROUND___2026-11-02T12.00.00Z</wcs:CoverageId></wcs:CoverageSummary>
</wcs:Contents></wcs:Capabilities>`

const pointFixture = `<?xml version="1.0" encoding="UTF-8"?>
<gmlcov:ReferenceableGridCoverage xmlns:gml="http://www.opengis.net/gml/3.2" xmlns:gmlcov="http://www.opengis.net/gmlcov/1.0">
  <gml:rangeSet><gml:DataBlock><gml:rangeParameters/><gml:tupleList>
    %v
  </gml:tupleList></gml:DataBlock></gml:rangeSet>
</gmlcov:ReferenceableGridCoverage>`

// Raw model values per coverage, in model units.
var values = map[string]float64{
	"TEMPERATURE__SPECIFIC_HEIGHT_LEVEL_ABOVE_GROUND":           284.15, // K
	"RELATIVE_HUMIDITY__SPECIFIC_HEIGHT_LEVEL_ABOVE_GROUND":     80,
	"DEW_POINT_TEMPERATURE__SPECIFIC_HEIGHT_LEVEL_ABOVE_GROUND": 278.15,
	"PRESSURE__MEAN_SEA_LEVEL":                                  101300, // Pa
	"TOTAL_CLOUD_COVER__GROUND_OR_WATER_SURFACE":                100.01,
	"U_COMPONENT_OF_WIND__SPECIFIC_HEIGHT_LEVEL_ABOVE_GROUND":   0, // wind from the south:
	"V_COMPONENT_OF_WIND__SPECIFIC_HEIGHT_LEVEL_ABOVE_GROUND":   5, // blowing northwards at 5 m/s
	"WIND_SPEED_GUST_MAX__SPECIFIC_HEIGHT_LEVEL_ABOVE_GROUND":   10,
	"TOTAL_PRECIPITATION__GROUND_OR_WATER_SURFACE":              3,
	"TOTAL_SNOW_PRECIPITATION__GROUND_OR_WATER_SURFACE":         0,
	// VISIBILITY is absent: the server answers 404 and the field stays nil.
}

func TestFetch(t *testing.T) {
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("apikey") != "k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/wcs/MF-NWP-HIGHRES-AROME-0025-FRANCE-WCS/") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/GetCapabilities"):
			fmt.Fprint(w, capsFixture)
		case strings.HasSuffix(r.URL.Path, "/GetCoverage"):
			id := r.URL.Query().Get("coverageid")
			requests = append(requests, id+" "+strings.Join(r.URL.Query()["subset"], " "))
			name, run, _ := strings.Cut(id, "___")
			if !strings.HasPrefix(run, "2026-11-02T09.00.00Z") {
				t.Errorf("wrong run in %s", id)
			}
			v, ok := values[name]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			fmt.Fprintf(w, pointFixture, v)
		}
	}))
	defer srv.Close()

	pr, err := New(config.ProviderConfig{Name: "mf", APIKey: "k", BaseURL: srv.URL,
		Options: map[string]any{"horizon_hours": 3}}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	p := pr.(*Provider)
	p.interval = 0
	p.now = func() time.Time { return time.Date(2026, 11, 2, 13, 30, 0, 0, time.UTC) }

	got, err := p.Fetch(context.Background(), weather.Location{Name: "paris", Latitude: 48.85, Longitude: 2.35})
	if err != nil {
		t.Fatal(err)
	}
	// Run 09Z, 3h steps: 12Z (current step) and 15Z (now + 3h = 16:30).
	if len(got) != 2 || !got[0].ForecastTime.Equal(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)) ||
		!got[1].ForecastTime.Equal(time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected forecasts: %+v", got)
	}
	if len(requests) != 2*len(variables) {
		t.Errorf("got %d coverage requests, want %d", len(requests), 2*len(variables))
	}
	if !slices.Contains(requests, "TOTAL_PRECIPITATION__GROUND_OR_WATER_SURFACE___2026-11-02T09.00.00Z_PT3H time(2026-11-02T12:00:00Z) lat(48.85) long(2.35)") ||
		!slices.Contains(requests, "TEMPERATURE__SPECIFIC_HEIGHT_LEVEL_ABOVE_GROUND___2026-11-02T09.00.00Z time(2026-11-02T12:00:00Z) lat(48.85) long(2.35) height(2)") {
		t.Errorf("unexpected requests:\n%s", strings.Join(requests, "\n"))
	}

	f := got[0]
	checks := []struct {
		name string
		got  *float64
		want float64
	}{
		{"temperature", f.TemperatureC, 11},
		{"humidity", f.HumidityPct, 80},
		{"dew point", f.DewPointC, 5},
		{"pressure", f.PressureHPa, 1013},
		{"cloud cover (clamped)", f.CloudCoverPct, 100},
		{"wind speed", f.WindSpeedKmh, 18},
		{"wind direction (from south)", f.WindDirectionDeg, 180},
		{"gust", f.WindGustKmh, 36},
		{"precipitation", f.PrecipitationMm, 3},
		{"snowfall", f.SnowfallCm, 0},
	}
	for _, c := range checks {
		if c.got == nil || math.Abs(*c.got-c.want) > 1e-9 {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}
	if f.VisibilityKm != nil {
		t.Errorf("visibility should be nil on 404, got %v", *f.VisibilityKm)
	}
	if f.Granularity != weather.GranularityThreeHours || f.Condition != weather.ConditionRain {
		t.Errorf("granularity/condition: %s %s", f.Granularity, f.Condition)
	}
}

func TestFetchOutsideDomain(t *testing.T) {
	p, _ := New(config.ProviderConfig{Name: "mf", APIKey: "k"}, http.DefaultClient)
	if _, err := p.Fetch(context.Background(), weather.Location{Name: "nyc", Latitude: 40.7, Longitude: -74}); err == nil ||
		!strings.Contains(err.Error(), "outside the AROME") {
		t.Errorf("expected domain error, got %v", err)
	}
}

func TestFetchAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"code":"900908","message":"Resource forbidden"}`)
	}))
	defer srv.Close()
	p, _ := New(config.ProviderConfig{Name: "mf", APIKey: "k", BaseURL: srv.URL}, srv.Client())
	_, err := p.Fetch(context.Background(), weather.Location{Latitude: 48.85, Longitude: 2.35})
	if err == nil || !strings.Contains(err.Error(), "http 403") {
		t.Errorf("expected 403 error, got %v", err)
	}
}

func TestDeriveCondition(t *testing.T) {
	cases := []struct {
		f    weather.Forecast
		want weather.Condition
	}{
		{weather.Forecast{CloudCoverPct: new(5.0)}, weather.ConditionClear},
		{weather.Forecast{CloudCoverPct: new(50.0)}, weather.ConditionPartlyCloudy},
		{weather.Forecast{CloudCoverPct: new(90.0)}, weather.ConditionCloudy},
		{weather.Forecast{CloudCoverPct: new(90.0), VisibilityKm: new(0.3)}, weather.ConditionFog},
		{weather.Forecast{Granularity: weather.GranularityHourly, PrecipitationMm: new(0.2)}, weather.ConditionDrizzle},
		{weather.Forecast{Granularity: weather.GranularityHourly, PrecipitationMm: new(2.0)}, weather.ConditionRain},
		{weather.Forecast{PrecipitationMm: new(2.0), SnowfallCm: new(1.0)}, weather.ConditionSleet},
		{weather.Forecast{PrecipitationMm: new(2.0), SnowfallCm: new(2.0)}, weather.ConditionSnow},
		{weather.Forecast{}, weather.ConditionUnknown},
	}
	for i, c := range cases {
		if got := deriveCondition(c.f); got != c.want {
			t.Errorf("case %d: got %s, want %s", i, got, c.want)
		}
	}
}
