-- SQLite schema. schema_postgres.sql is the PostgreSQL variant; keep both files in sync.
-- All timestamps are stored as UTC text in the format 2006-01-02T15:04:05Z,
-- which sorts lexicographically in chronological order.

-- One row per weather data point. Rows are append-only: every poll stores a new
-- snapshot so the evolution of a forecast for a given target time can be compared,
-- and observations can be set against earlier forecasts for the same time.
CREATE TABLE IF NOT EXISTS readings (
    polled_at                     TEXT    NOT NULL,
    provider                      TEXT    NOT NULL,
    provider_type                 TEXT    NOT NULL,

    location_name                 TEXT    NOT NULL,

    forecast_time                 TEXT    NOT NULL,  -- UTC start of the covered period
    lead_time_hours               REAL    NOT NULL,  -- forecast_time - polled_at (negative for observations)
    kind                          TEXT    NOT NULL CHECK (kind IN ('forecast', 'observation')) DEFAULT 'forecast',

    temperature_c                 REAL,
    feels_like_c                  REAL,
    humidity_pct                  REAL,
    dew_point_c                   REAL,
    pressure_hpa                  REAL,
    wind_speed_kmh                REAL,
    wind_gust_kmh                 REAL,
    wind_direction_deg            REAL,
    cloud_cover_pct               REAL,
    precipitation_probability_pct REAL,
    snowfall_cm                   REAL,
    visibility_km                 REAL,

    condition                     TEXT    NOT NULL,  -- normalized, see weather.Condition
    condition_text                TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_readings_provider_loc_time ON readings (provider, location_name, forecast_time);
CREATE INDEX IF NOT EXISTS idx_readings_time ON readings (forecast_time);
CREATE INDEX IF NOT EXISTS idx_readings_provider_polled ON readings (provider, polled_at);
CREATE INDEX IF NOT EXISTS idx_readings_kind ON readings (kind);

-- Runtime settings edited from the web UI. Only the latest state is kept.
-- Known keys: poll_interval (Go duration string, shared by all providers).
CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- Locations to poll, in display order.
CREATE TABLE IF NOT EXISTS locations (
    name       TEXT    PRIMARY KEY,
    position   INTEGER NOT NULL,
    city       TEXT    NOT NULL DEFAULT '',
    country    TEXT    NOT NULL DEFAULT '',
    latitude   REAL    NOT NULL,
    longitude  REAL    NOT NULL,
    station_id TEXT    NOT NULL DEFAULT ''
);

-- Provider instances, in display order.
CREATE TABLE IF NOT EXISTS providers (
    name     TEXT    PRIMARY KEY,               -- instance name, used by readings.provider
    position INTEGER NOT NULL,
    type     TEXT    NOT NULL,                  -- implementation, e.g. openmeteo
    api_key  TEXT    NOT NULL DEFAULT '',
    base_url TEXT    NOT NULL DEFAULT '',
    enabled  INTEGER NOT NULL DEFAULT 1,
    options  TEXT    NOT NULL DEFAULT '{}'      -- JSON object of provider-specific options
);
