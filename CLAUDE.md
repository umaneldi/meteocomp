# CLAUDE.md

## Language

**Only English shall be used in this repository.** This applies to code, identifiers,
comments, documentation, commit messages, log and error messages, configuration keys,
database table/column names, test data descriptions and API payloads.

## Project overview

meteocomp is a Go web service that polls any number of user-configured weather providers
for a set of locations, stores every weather reading in a database, and
exposes them over an HTTP/JSON API. Reading rows are append-only so forecasts can be
compared across providers and across lead times (how the forecast for a given time evolves).
Settings (shared poll interval, locations, provider instances and API keys) are stored in the
database and edited from an embedded web UI.

## Layout

- `cmd/meteocomp` — entry point: opens the store (`DATABASE_URI`, default `sqlite://meteocomp.db`), loads settings (DB, else optional seed file, else
  defaults), starts the poller manager and HTTP server.
- `internal/weather` — domain types: `Location`, `Forecast`, normalized `Condition`, `Provider` interface.
- `internal/config` — `Settings` types, defaults and validation; optional YAML seed file loading
  (with `${ENV}` / `${ENV:-default}` expansion), used only to seed an empty database (e.g. CI).
- `internal/providers` — provider registry and shared HTTP helper; one sub-package per provider.
  `internal/providers/all` blank-imports every provider so they register themselves.
- `internal/store` — `Store` interface and its `database/sql` implementation for SQLite and PostgreSQL,
  selected by the `DATABASE_URI` scheme. `schema_sqlite.sql` / `schema_postgres.sql` are embedded and
  must be kept in sync; queries use `?` placeholders, rebound per dialect.
- `internal/poller` — one goroutine per provider instance, polling all locations at the shared interval,
  resuming from the latest stored reading. `Manager` swaps pollers when settings change.
- `internal/api` — HTTP handlers, including `GET/PUT /api/v1/settings` (API keys are write-only).
- `internal/web` — embedded settings UI (`static/`, vanilla HTML/CSS/JS, no build step).

## Conventions

- Units stored and returned: °C, %, hPa, km/h, cm (snowfall), km (visibility),
  degrees (wind direction). Providers convert to these units.
- Times are UTC. Optional measures are `*float64`; `nil` means the provider did not supply it (NULL in DB).
- Every provider maps its raw condition codes to `weather.Condition` and keeps the
  description in `ConditionText`.
- Readings match `Reading` in `proto/global_map.py`, with NULL for missing measurements.
  There are no poll rows, reading IDs, granularity or native daily aggregates.
- Failed or empty fetches leave no stored timestamp; failures are logged.
- Only exception to append-only rows: providers implementing `weather.ConditionSource`
  (`meteofrance_synop`) store no readings. The poller keeps their latest readings in memory and,
  after their poll and after any poll recording observations, fills stored observations with an
  unknown/empty condition from the nearest source observation of the same location within 1h30
  (`Store.FillObservationConditions`, text suffixed " (SYNOP)").
- Provider tests use `httptest` with JSON fixtures; tests must not hit real APIs.

## Adding a provider

1. Create `internal/providers/<type>/` implementing `weather.Provider`.
2. Register it in `init()` with `providers.Register("<type>", New, providers.Info{...})`; `Info`
   (description, API key requirement, options) drives the web UI.
3. Add a blank import in `internal/providers/all/all.go`.
4. Map provider condition codes to `weather.Condition`; convert units.
5. Add a fixture-based test and document the type/options in `config.example.yaml` and `README.md`.

## Commands

- Build: `make build` (or `go build ./cmd/meteocomp`)
- Test: `make test` (runs `go vet` and `go test ./...`)
- Run: `make run`, then open http://localhost:8080/ to configure.
