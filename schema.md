# Database Schema

All timestamps are stored as UTC text in the format `2006-01-02T15:04:05Z`, which sorts
lexicographically in chronological order.

## Configuration tables

| Table | Purpose |
|---|---|
| `settings` | Key/value pairs: shared poll interval and any future runtime options |
| `locations` | Places to poll (name, lat/lon, display order) |
| `providers` | Provider instances (type, API key, options, enabled flag, display order) |

## Data tables

### `polls` — one row per fetch attempt

Each time a provider instance is asked about one location, a row is inserted, whether the
fetch succeeded or not.

| Column | Type | Description |
|---|---|---|
| `id` | INTEGER PK | Auto-increment |
| `provider` | TEXT | Instance name from config |
| `provider_type` | TEXT | Implementation, e.g. `openmeteo` |
| `location_name` | TEXT | Foreign key to `locations.name` (by value) |
| `started_at` | TEXT | When the HTTP request was initiated (UTC) |
| `finished_at` | TEXT | When the response was fully processed (UTC) |
| `duration_ms` | INTEGER | Wall-clock fetch duration |
| `status` | TEXT | `ok` or `error` |
| `error` | TEXT | Error message if status = `error`, else NULL |
| `forecast_count` | INTEGER | Number of forecast rows inserted for this poll |

### `forecasts` — one row per forecast data point

One poll returns many data points (e.g. 48 hourly + 7 daily values). Each becomes its own row,
linked back to its poll via `poll_id`. Rows are **never updated**: every poll appends a fresh
set, so the full history of how any forecast evolved over time is preserved.

#### Time columns

Two times describe each row, plus the gap between them:

| Column | Description | Source |
|---|---|---|
| `polled_at` | When the forecast was retrieved — the poll's `started_at` | Our clock |
| `forecast_time` | The UTC **start** of the period the figures cover | Provider |
| `granularity` | Duration of that period: `hourly`, `3h`, or `daily` | Provider |
| `lead_time_hours` | `forecast_time − polled_at` in hours (decimal, can be negative) | Computed on insert |

`lead_time_hours` is computed at insert time as:

```go
lead := f.ForecastTime.Sub(r.StartedAt).Hours()
```

#### Example

Two polls for the same provider/location, six hours apart:

| polled_at | forecast_time | granularity | lead_time_hours | temperature_c |
|---|---|---|---|---|
| 2026-11-02T10:00Z | 2026-11-03T12:00Z | hourly | 26.0 | 11.2 |
| 2026-11-02T10:00Z | 2026-11-03T00:00Z | daily | 14.0 | NULL (see min/max) |
| 2026-11-02T16:00Z | 2026-11-03T12:00Z | hourly | 20.0 | 10.4 |

- **Forecast evolution for one target time**: fix `provider`, `location_name`,
  `forecast_time`, `granularity`; sort by `polled_at`. Lead time shrinks as the target
  approaches. The index `idx_forecasts_provider_loc_time` is built for this.
- **Provider comparison**: fix `forecast_time` and `granularity`; group by `provider`.
- **Accuracy by lead time**: bucket `lead_time_hours` (0–24 h, 24–48 h, …) and compare
  against the closest-lead-time row, which is the best proxy for "actual" the schema offers.

#### Measurement columns

All optional (`*float64` in Go, `REAL` in SQLite, `DOUBLE PRECISION` in PostgreSQL). `NULL` means the provider did not supply
the value — not zero.

| Column | Unit |
|---|---|
| `temperature_c` | °C (instantaneous or representative for the period) |
| `temperature_min_c` / `temperature_max_c` | °C (daily min/max) |
| `feels_like_c` | °C |
| `humidity_pct` | % |
| `dew_point_c` | °C |
| `pressure_hpa` | hPa |
| `wind_speed_kmh` | km/h |
| `wind_gust_kmh` | km/h |
| `wind_direction_deg` | degrees (meteorological: 0 = N, 90 = E) |
| `cloud_cover_pct` | % |
| `precipitation_mm` | mm (total for the period) |
| `precipitation_probability_pct` | % |
| `snowfall_cm` | cm |
| `uv_index` | dimensionless |
| `visibility_km` | km |

#### Condition columns

| Column | Description |
|---|---|
| `condition` | Normalized `weather.Condition` value (e.g. `rain`, `clear`, `snow`) |
| `condition_code` | Raw provider condition code |
| `condition_text` | Raw provider condition description |

#### Context columns (copied from poll / location at insert time)

Location fields are denormalized into every forecast row so that old rows remain correct
even if a location is later renamed or moved.

| Column | Description |
|---|---|
| `poll_id` | Foreign key to `polls.id` (CASCADE DELETE) |
| `polled_at` | Duplicate of `polls.started_at`, kept for query convenience |
| `provider` / `provider_type` | Duplicates of `polls` columns |
| `location_name`, `city`, `country`, `latitude`, `longitude` | Snapshot of location at poll time |

## Caveats

1. **Lead time is from our poll clock, not the provider's model run.** Providers don't
   report when their model last ran, so two polls an hour apart may return identical figures
   with different `lead_time_hours`.

2. **`forecast_time` is the start of the period.** For a daily row, lead time is measured
   to midnight at the start of that day. A daily row for "tomorrow" polled at 10:00 UTC
   will show `lead_time_hours ≈ 14`, not 24.

3. **Daily rows from different providers may not align.** Open-Meteo is queried with
   `timezone=UTC`, so its daily rows are UTC midnight–midnight. WeatherAPI returns a local
   date string which is stored as midnight UTC of that date — the two "daily" rows may cover
   different 24-hour windows.

4. **Granularity differs by provider.** OpenWeatherMap returns only `3h`; Open-Meteo and
   WeatherAPI return `hourly` and `daily`. Always filter on `granularity` when comparing
   providers.
