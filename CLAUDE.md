# CLAUDE.md

## Language

**Only English shall be used in this repository.** This applies to code, identifiers,
comments, documentation, commit messages, log and error messages, configuration keys,
database table/column names, test data descriptions and API payloads.

## Project overview

meteocomp is a Go web service that polls any number of user-configured weather providers
for a set of locations, stores every poll and every forecast data point in a database, and
exposes them over an HTTP/JSON API. Rows are append-only so forecasts can be compared
across providers and across lead times (how the forecast for a given time evolves).

## Layout

- `cmd/meteocomp` — entry point: loads config, opens the store, starts the poller and HTTP server.
- `internal/weather` — domain types: `Location`, `Forecast`, normalized `Condition`, `Provider` interface.
- `internal/config` — YAML config loading (with `${ENV}` expansion), defaults and validation.
- `internal/providers` — provider registry and shared HTTP helper; one sub-package per provider.
  `internal/providers/all` blank-imports every provider so they register themselves.
- `internal/store` — `Store` interface and SQLite implementation (`schema.sql` is embedded).
- `internal/poller` — one goroutine per provider instance, polling all locations at its interval.
- `internal/api` — HTTP handlers.

## Conventions

- Units stored and returned: °C, %, hPa, km/h, mm (precipitation), cm (snowfall), km (visibility),
  degrees (wind direction). Providers convert to these units.
- Times are UTC. Optional measures are `*float64`; `nil` means the provider did not supply it (NULL in DB).
- Every provider maps its raw condition codes to `weather.Condition` and keeps the raw
  code/text in `ConditionCode`/`ConditionText`.
- Failed polls are recorded too (`polls.status = 'error'`).
- Provider tests use `httptest` with JSON fixtures; tests must not hit real APIs.

## Adding a provider

1. Create `internal/providers/<type>/` implementing `weather.Provider`.
2. Register it in `init()` with `providers.Register("<type>", New)`.
3. Add a blank import in `internal/providers/all/all.go`.
4. Map provider condition codes to `weather.Condition`; convert units.
5. Add a fixture-based test and document the type/options in `config.example.yaml` and `README.md`.

## Commands

- Build: `make build` (or `go build ./cmd/meteocomp`)
- Test: `make test` (runs `go vet` and `go test ./...`)
- Run: `cp config.example.yaml config.yaml && make run`
