# Database schema

SQLite and PostgreSQL use the same logical schema. Timestamps are UTC text in
`2006-01-02T15:04:05Z` format.

## Configuration tables

| Table | Purpose |
| --- | --- |
| `settings` | Shared poll interval and forecast horizon |
| `locations` | Location names, coordinates, station IDs and display order |
| `providers` | Provider instance names, types, API keys, options and enabled flags |

## Readings

`readings` contains exactly the fields of `Reading` in `proto/global_map.py`, without
reading IDs, poll IDs, extra timestamps or duplicated location details. Each successful
fetch atomically appends its readings. Failed fetches are logged but do not insert rows.

| Column | Meaning / unit |
| --- | --- |
| `provider` | Configured provider instance name |
| `provider_type` | Provider implementation identifier |
| `location_name` | Configured location name |
| `forecast_time` | UTC target/observation time |
| `lead_time_hours` | `(forecast_time - polled_at)` in hours |
| `kind` | `forecast` or `observation` |
| `temperature_c` | Temperature, Celsius |
| `feels_like_c` | Apparent temperature, Celsius |
| `humidity_pct` | Relative humidity, % |
| `dew_point_c` | Dew point, Celsius |
| `pressure_hpa` | Pressure, hPa (Open-Meteo uses surface pressure) |
| `wind_speed_kmh` | Wind speed, km/h |
| `wind_gust_kmh` | Wind gust, km/h |
| `wind_direction_deg` | Wind direction, degrees |
| `cloud_cover_pct` | Cloud cover, % |
| `precipitation_probability_pct` | Rain probability, % |
| `snowfall_cm` | Snowfall depth, cm |
| `visibility_km` | Visibility, km |
| `condition` | Normalized condition, such as `rain`, `clear` or `unknown` |
| `condition_text` | Provider description, or derived WMO description |
| `polled_at` | UTC retrieval start time |

Measurements are nullable (`*float64` in Go, `REAL` in SQLite, `DOUBLE PRECISION`
in PostgreSQL). SQL `NULL` / JSON `null` means unavailable; zero remains a real value.
Prototype sentinel values and incorrect unit conversions are not reproduced.
OpenWeatherMap `snow.3h` (mm water equivalent) is stored as snowfall depth in cm
(standard 10:1 snow-to-water ratio).

Location and provider names are references by value, not foreign keys: removing or
renaming configuration entries does not delete weather history. No location metadata
snapshot is retained in readings.

Only sub-daily data is collected: Open-Meteo and WeatherAPI return hourly forecasts,
OpenWeatherMap returns 3-hourly forecasts, and Infoclimat returns observations at their
original timestamps. No granularity column or native daily aggregates are stored.

Indexes cover provider/location/target time, target time, kind, and provider/retrieval
time. Scheduling resumes from each provider's latest stored `polled_at`; failed or empty
fetches leave no persisted scheduling timestamp and may be retried after restart.

## History and querying

Forecast evolution compares the same provider, location and target time across retrieval
times. Different providers can be compared at the same target time; use `kind` to separate
forecasts from observations. Lead times refer to our retrieval clock, not the model run.

API pagination uses `limit` and `offset`, ordered by retrieval time descending with
field-based tie breakers. Keep filters and a `polled_to` cutoff fixed across pages.
Concurrent insertion of an older in-flight retrieval can still shift offsets.
The chart groups readings by provider and retrieval time, derives daily summaries from
sub-daily values, and flags incomplete coverage. Same-second retrievals share a group.

## Upgrading an existing database

Back up the database first. On startup, both backends transactionally rebuild legacy
readings using only these fields, preserve supported sub-daily measurements, discard
legacy daily aggregate rows, and remove poll history. Settings remain intact.
This is an irreversible schema reduction, not a migration between database backends.
