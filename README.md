# meteocomp

A Go web service that polls as many weather providers as you configure, appends weather
readings to a database, and serves them over a JSON API so providers can be compared.
Fetch attempts and failures are logged, not stored in separate poll rows.

## Quick start

```sh
make run          # or: go run ./cmd/meteocomp
```

Open http://localhost:8080/ and configure everything from the web UI:

- **Polling interval** — shared by all providers (minimum `1m`).
- **Locations** — search a place (Open-Meteo geocoding, from the browser) or enter coordinates.
- **Providers** — enable/disable instances, add several of the same type, set options and
  provision API keys. Keys are write-only: the API only reports whether a key is set.
- **Forecast evolution** — the *Forecast* tab shows how each provider's daily forecast for
  **today (UTC)** evolved across historical polls. The table's row index and chart's numeric
  X axis are **lead time in hours**, decreasing from oldest to newest polls. Every poll is
  retained, not just the latest revision. Choose a location, daily statistic, and table/chart
  view; the default statistic is daily mean temperature.

Settings are persisted in the database. On first start the database is seeded with defaults
(Open-Meteo enabled, keyed providers disabled, no locations). Saving restarts the poller; each
provider resumes from its latest stored reading. Failed or empty fetches have no stored
timestamp, so saving or restarting may retry them immediately.
Use *Poll now* to fetch immediately.

### Forecast evolution

Chart lead time is `(today's noon UTC - retrieval time)` in hours, including fractional
hours. Only forecasts polled **at or before that midnight** are shown; observations and
later polls are excluded. History must therefore be collected before the target date.
The view loads all matching history through offset pagination, refreshes every minute while
active and on tab entry, and rolls over to the new UTC date automatically.

Each provider/retrieval time supplies one derived daily result. Only sub-daily readings are
stored: hourly forecasts, or 3-hourly forecasts from OpenWeatherMap. Daily summaries group
by provider and `polled_at`; different retrievals are never mixed:

| Statistic | Sub-daily aggregation |
| --- | --- |
| Temperature, humidity, dew point, pressure, cloud cover, visibility | Mean |
| Minimum / maximum temperature | Minimum / maximum of per-slot temperature |
| Feels-like, wind speed, wind gust, rain probability | Maximum |
| Snowfall | Sum |

Missing values remain
unavailable, not zero. Derived values may use a partial day: the table flags **partial
coverage** with covered hours and available slot counts, and the chart outlines partial
points and shows a warning. Coverage is measured per statistic, so a missing field reduces
its coverage even if other fields span the whole day. Partial snowfall totals and
extrema must not be interpreted as full-day values. Hover a table value or focus/hover a
chart point for the poll time, lead time and provenance.

Derived values cover the actual UTC day. Exact lead times are not bucketed, so
asynchronously polled providers may occupy different table rows. Retrievals by the same
provider in the same second share one chart group because readings have no poll IDs.
All historical readings remain stored and available through the API.

### API keys from environment variables

Keys can also be supplied through environment variables. When set (non-empty), they are used
for every instance of the provider type and override the key typed in the web UI, which then
shows the key as coming from the environment. When unset, enter the key in the web UI.

| Provider type | Variable |
| --- | --- |
| `openweathermap` | `OWM_API_KEY` |
| `weatherapi` | `WEATHER_API_KEY` |

> The UI and settings API have no authentication. Do not expose port 8080 publicly; put it
> behind a reverse proxy with authentication or keep it on a private network.

### Startup options

| Flag | Env | Default |
| --- | --- | --- |
| `-addr` | `METEOCOMP_HTTP_ADDR` | `:8080` |
| `-database-uri` | `DATABASE_URI` | `sqlite://meteocomp.db` |
| `-config` | `METEOCOMP_CONFIG` | none |

### Optional seed file (automated tests)

`-config` / `METEOCOMP_CONFIG` points to a YAML file (see `config.example.yaml`) used **only when
the database holds no settings yet**, e.g. for CI. Once settings are stored, the file is ignored and
the web UI is the source of truth. `${VAR}` and `${VAR:-default}` are expanded.

## Docker

Images are published to `ghcr.io/yanndethe/meteocomp` (multi-arch, distroless, ~6 MB compressed)
by `.github/workflows/docker.yml`: `latest` from the default branch and semver tags from `v*` git tags.

```sh
docker run -d -p 8080:8080 -v meteocomp-data:/data ghcr.io/yanndethe/meteocomp
# optionally provide API keys instead of typing them in the UI:
#   -e OWM_API_KEY=... -e WEATHER_API_KEY=...
#   or store data in PostgreSQL instead of the /data volume:
#   -e DATABASE_URI=postgres://meteocomp:secret@db:5432/meteocomp?sslmode=disable
```

then configure it at http://localhost:8080/. Settings and data live in `/data/meteocomp.db`.

For automated tests the image also ships `docker/config.yaml`, an env-driven seed file that is
only used when enabled explicitly:

```sh
docker run --rm -p 8080:8080 \
  -e METEOCOMP_CONFIG=/etc/meteocomp/config.yaml \
  -e METEOCOMP_LOCATIONS='[{name: lyon, city: Lyon, country: FR, latitude: 45.76, longitude: 4.83}]' \
  -e OWM_ENABLED=true -e OWM_API_KEY=... \
  ghcr.io/yanndethe/meteocomp
```

| Variable | Default |
| --- | --- |
| `METEOCOMP_POLL_INTERVAL` | `1h` |
| `METEOCOMP_LOCATIONS` | `[]` (YAML flow sequence of locations) |
| `OPENMETEO_ENABLED` / `OPENMETEO_FORECAST_DAYS` | `true` / `7` |
| `METEOFRANCE_ENABLED` / `METEOFRANCE_FORECAST_DAYS` | `false` / `4` |
| `OWM_ENABLED` | `false` (key: `OWM_API_KEY`, see above) |
| `WEATHERAPI_ENABLED` / `WEATHERAPI_DAYS` | `false` / `3` (key: `WEATHER_API_KEY`, see above) |

## Providers

| type             | key | granularity       | options                          |
|------------------|-----|-------------------|----------------------------------|
| `openmeteo`      | no  | hourly           | `endpoint` (default `forecast`), `models`, `forecast_days` (1-16, default 7) |
| `openweathermap` | yes | 3-hourly, 5 days  | —                                |
| `weatherapi`     | yes | hourly           | global `forecast_days` (capped at 14) |
| `infoclimat`     | yes | observations     | global `forecast_days` controls lookback |

`openmeteo` can target any Open-Meteo endpoint with the `endpoint` option (the path after
`/v1/`): `forecast` (default) blends the best models for each location, while model-specific
endpoints such as `meteofrance` (Météo-France AROME 2.5 km over France + ARPEGE, about 4 days),
`dwd-icon`, `gfs`, `ecmwf` or `metno` return a single weather service's forecast. `models`
optionally narrows it further (e.g. `arome_france`). Points beyond the models' horizon are dropped.
Configure several `openmeteo` instances to compare weather services side by side.

`infoclimat` fetches weather station observations, including sub-hour readings, from Infoclimat
(https://www.infoclimat.fr/opendata/). All returned data points have `kind=observation`.
Requires a token from the Infoclimat opendata portal. The station queried per location is
taken from each location's `station_id` field; one provider instance handles all configured
locations in a single API call.

Any number of instances can be configured, including several of the same type.

## Database

SQLite or PostgreSQL, selected by `DATABASE_URI` (or `-database-uri`):

| URI | Backend |
| --- | --- |
| `postgres://user:pass@host:5432/dbname?sslmode=disable` (or `postgresql://`) | PostgreSQL |
| `sqlite:///absolute/path.db`, `sqlite://relative/path.db`, `sqlite:path.db`, or a bare path | SQLite |

When `DATABASE_URI` is unset, `sqlite://meteocomp.db` is used. Tables are
created on startup; the PostgreSQL database itself must already exist. Data is not migrated
between backends.

Settings tables (`settings`, `locations`, `providers`) hold the current
configuration; they are replaced as a whole on each save. Data tables:

- `readings` — one row per weather data point, matching `Reading` in `proto/global_map.py`:
  retrieval time, forecast time, kind (`forecast` or `observation`), lead time, provider name/type,
  location name, temperature, feels-like, humidity, dew point, pressure, wind speed/gust/direction,
  cloud cover, rain probability, snowfall, visibility, normalized condition
  (`clear`, `partly_cloudy`, `cloudy`, `fog`, `drizzle`, `rain`, `freezing_rain`, `sleet`, `snow`,
  `thunderstorm`, `unknown`) and provider description.

There are no reading IDs, poll IDs or poll table. Missing measurements are SQL `NULL` and
JSON `null`, never sentinel numbers. Location details remain in `locations`; readings refer
to them by `location_name` without a foreign key, preserving history after settings edits.

**Back up an existing database before upgrading.** Startup migrates both backends
transactionally: it retains hourly/3-hourly readings and their supported values, drops
legacy daily aggregates (which cannot be distinguished in the simplified model), removes
the obsolete columns and poll history, and retains settings. This migration is irreversible.

See `internal/store/schema_sqlite.sql` and `internal/store/schema_postgres.sql` for the exact schema.

Store tests also run against PostgreSQL when `METEOCOMP_TEST_POSTGRES_URI` is set; each test uses
a throwaway schema in that database.

## HTTP API

| method | path                 | query parameters |
|--------|----------------------|------------------|
| GET    | `/`                  | settings web UI |
| GET    | `/healthz`           | |
| GET    | `/api/v1/version`    | service version |
| GET    | `/api/v1/settings`   | current settings; API keys reported as `has_api_key` / `api_key_hint` only |
| PUT    | `/api/v1/settings`   | replace settings (see below) |
| GET    | `/api/v1/provider-types` | available provider types, options and whether they need a key |
| GET    | `/api/v1/providers`  | |
| GET    | `/api/v1/locations`  | |
| POST   | `/api/v1/polls`      | `provider` (optional) — trigger an immediate poll |
| GET    | `/api/v1/readings`   | `provider`, `location`, `kind`, `from`, `to`, `polled_from`, `polled_to` (RFC3339), `limit`, `offset` |
| GET    | `/api/v1/stations`   | `q` (min 2 chars) — search Infoclimat stations by ID or name |

`limit` defaults to 500 (max 10000).

Readings are ordered by retrieval time descending, with deterministic field-based tie
breakers. Paginate with `limit` and a non-negative `offset` (default 0), increasing the
offset by each page's length until a short or empty page. Keep all filters unchanged and
use a fixed `polled_to` cutoff to exclude later retrievals. Offset pagination is not a
database snapshot: an in-flight fetch started before the cutoff can still add rows.
The removed `order`, `before_id`, `poll_id` and `granularity` parameters return `400`.
The former `GET /api/v1/polls` endpoint is removed; `POST` still triggers a fetch.

`PUT /api/v1/settings` takes the same document as `GET` returns. Per provider, `api_key`
is optional: omitted keeps the stored key, `""` removes it, any other value replaces it;
`previous_name` keeps the key when an instance is renamed. Invalid settings (bad interval or
coordinates, duplicate names, enabled provider without a required key, ...) return `400` and
nothing is saved.

## Development checks

- `make test` runs Go vet and all Go tests.
- `make test-web` runs JavaScript syntax and forecast aggregation/pagination tests using
  Node.js 22 or newer, with no npm dependencies or frontend build step.
- `make build` builds the service with its embedded UI.
