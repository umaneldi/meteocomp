# meteocomp

A Go web service that polls as many weather providers as you configure, logs every
poll and forecast to a database, and serves them over a JSON API so providers can be compared.

## Quick start

```sh
cp config.example.yaml config.yaml   # edit locations/providers; set API keys via env vars
make run                             # or: go run ./cmd/meteocomp -config config.yaml
```

Open-Meteo needs no API key, so the example config works out of the box.

## Providers

| type             | key | granularity       | options                          |
|------------------|-----|-------------------|----------------------------------|
| `openmeteo`      | no  | hourly + daily    | `forecast_days` (1-16, default 7) |
| `openweathermap` | yes | 3-hourly, 5 days  | —                                |
| `weatherapi`     | yes | hourly + daily    | `days` (1-14, default 3)         |

Any number of instances can be configured, including several of the same type.

## Database

SQLite (`database.path`). Two tables:

- `polls` — one row per provider/location fetch: timestamps, duration, status (`ok`/`error`), error, forecast count.
- `forecasts` — one row per forecast point: poll date, forecast date, granularity, lead time, provider,
  location (name, city, country, lat/lon), temperature (current/min/max/feels-like), humidity, dew point,
  pressure, wind speed/gust/direction, cloud cover, precipitation amount/probability, snowfall, UV index,
  visibility, normalized condition (`clear`, `partly_cloudy`, `cloudy`, `fog`, `drizzle`, `rain`,
  `freezing_rain`, `sleet`, `snow`, `thunderstorm`, `unknown`) plus the raw provider code and text.

See `internal/store/schema.sql` for the exact schema.

## HTTP API

| method | path                 | query parameters |
|--------|----------------------|------------------|
| GET    | `/healthz`           | |
| GET    | `/api/v1/providers`  | |
| GET    | `/api/v1/locations`  | |
| GET    | `/api/v1/polls`      | `provider`, `location`, `status`, `limit` |
| POST   | `/api/v1/polls`      | `provider` (optional) — trigger an immediate poll |
| GET    | `/api/v1/forecasts`  | `provider`, `location`, `granularity`, `from`, `to`, `polled_from`, `polled_to` (RFC3339), `poll_id`, `limit` |

`limit` defaults to 500 (max 10000).
