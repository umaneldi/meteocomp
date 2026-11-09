"use strict";

const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const context = vm.createContext({ URLSearchParams });
vm.runInContext(fs.readFileSync(path.join(__dirname, "static/forecast.js"), "utf8"), context);
const evolution = vm.runInContext("ForecastEvolution", context);
const midnight = new Date("2026-11-05T00:00:00Z");
const target = new Date("2026-11-05T12:00:00Z");
const plain = (value) => JSON.parse(JSON.stringify(value));

function reading(overrides = {}) {
  return {
    provider: "first", provider_type: "openmeteo", location_name: "test",
    polled_at: "2026-11-04T00:00:00Z", forecast_time: midnight.toISOString(),
    kind: "forecast", temperature_c: 10, ...overrides,
  };
}

function hours(count = 24, overrides = {}) {
  return Array.from({ length: count }, (_, hour) => reading({
    forecast_time: new Date(midnight.getTime() + hour * 3600e3).toISOString(),
    ...overrides,
  }));
}

test("UTC date, cutoff and kind preserve retrieval revisions without IDs", () => {
  assert.equal(evolution.today(new Date("2026-11-04T23:30:00-03:00")).toISOString(), target.toISOString());
  const data = evolution.build([
    reading(),
    reading({ polled_at: "2026-11-04T12:30:00Z" }),
    reading({ polled_at: midnight.toISOString() }),
    reading({ polled_at: "2026-11-05T00:00:01Z" }),
    reading({ kind: "observation" }),
    reading({ forecast_time: "2026-11-06T00:00:00Z" }),
    reading({ forecast_time: "2026-11-04T23:59:59Z" }),
  ], target);
  assert.deepEqual(plain(data.leads), [36, 23.5, 12]);
  assert.equal(data.series[0].points.length, 3);
});

test("reducers derive extrema from temperature and preserve zero and missing values", () => {
  const rows = hours(2, {
    humidity_pct: 50, pressure_hpa: 1000, cloud_cover_pct: 10, visibility_km: 8,
    wind_speed_kmh: 4, wind_gust_kmh: 5, feels_like_c: 8, snowfall_cm: 0,
    precipitation_probability_pct: 0, dew_point_c: 2,
  });
  Object.assign(rows[1], {
    temperature_c: 20, humidity_pct: 70, pressure_hpa: 1020, cloud_cover_pct: 30, visibility_km: 10,
    wind_speed_kmh: 8, wind_gust_kmh: 12, feels_like_c: 22, snowfall_cm: 2,
    precipitation_probability_pct: 40, dew_point_c: 4,
  });
  const values = evolution.build(rows, target).series[0].points[0].values;
  for (const [key, value] of Object.entries({
    temperature_c: 15, temperature_min_c: 10, temperature_max_c: 20,
    humidity_pct: 60, pressure_hpa: 1010, cloud_cover_pct: 20, visibility_km: 9,
    wind_speed_kmh: 8, wind_gust_kmh: 12, feels_like_c: 22, snowfall_cm: 2,
    precipitation_probability_pct: 40, dew_point_c: 3,
  })) {
    assert.equal(values[key].value, value, key);
    assert.equal(values[key].coverage, 2, key);
    assert.equal(values[key].partial, true, key);
  }
  const zero = evolution.build([reading({ temperature_c: 0 })], target).series[0].points[0].values;
  assert.equal(zero.temperature_c.value, 0);
  assert.equal(zero.humidity_pct, null);
});

test("provider type determines 3-hour coverage; duplicate slots are not double counted", () => {
  const rows = Array.from({ length: 8 }, (_, i) => reading({
    provider_type: "openweathermap",
    forecast_time: new Date(midnight.getTime() + i * 3 * 3600e3).toISOString(),
    temperature_c: i,
  }));
  rows.push({ ...rows[0] });
  let value = evolution.build(rows, target).series[0].points[0].values.temperature_c;
  assert.equal(value.value, 3.5);
  assert.equal(value.coverage, 24);
  assert.equal(value.partial, false);
  const hourly = hours(24);
  hourly[1].temperature_c = null;
  value = evolution.build(hourly, target).series[0].points[0].values.temperature_c;
  assert.equal(value.coverage, 23);
  assert.equal(value.totalSlots, 24);
  assert.equal(value.slots, 23);
});

test("coverage unions overlaps and clips at UTC midnight", () => {
  const rows = [0, 1, 23].map((hour) => reading({
    provider_type: "openweathermap",
    forecast_time: new Date(midnight.getTime() + hour * 3600e3).toISOString(),
  }));
  assert.equal(evolution.build(rows, target).series[0].points[0].values.temperature_c.coverage, 5);
});

test("provider ordering and asynchronous retrievals preserve distinct snapshots", () => {
  const data = evolution.build([
    reading({ provider: "z" }), reading({ provider: "a" }),
    reading({ provider: "configured" }),
    reading({ provider: "configured", polled_at: "2026-11-04T23:59:59Z" }),
  ], target, ["configured", "removed", "configured"]);
  assert.deepEqual(plain(data.series.map((s) => s.provider)), ["configured", "a", "z"]);
  assert.equal(data.series[0].points.length, 2);
  assert.deepEqual(plain(data.leads), [36, 12 + 1 / 3600]);
});

test("observations select latest time and revision without IDs, including sub-hour data", () => {
  const rows = [
    reading({ kind: "observation", forecast_time: "2026-11-05T09:10:00Z", temperature_c: 4 }),
    reading({ kind: "observation", forecast_time: "2026-11-05T09:10:00Z", polled_at: "2026-11-05T10:00:00Z", temperature_c: 0 }),
    reading({ kind: "observation", forecast_time: "2026-11-05T08:00:00Z", polled_at: "2026-11-05T11:00:00Z", temperature_c: 12 }),
    reading({ temperature_c: 30 }),
  ];
  const values = evolution.buildObservation(rows, target)[0].values;
  assert.equal(values.temperature_c.value, 0);
  assert.equal(values.temperature_c.source, "Observed at 09:10Z");
  assert.equal(values.humidity_pct, null);
});

test("offset pagination loads exact multiples and keeps filters and retrieval cutoff", async () => {
  for (const kind of ["forecast", "observation"]) {
    const queries = [];
    const pages = [[reading(), reading()], [reading(), reading()], []];
    const loader = kind === "forecast" ? evolution.loadRecords : evolution.loadObservations;
    const rows = await loader(async (query) => {
      queries.push(new URLSearchParams(query));
      return pages.shift();
    }, "test location", target, 2);
    assert.equal(rows.length, 4);
    assert.equal(queries.length, 3);
    assert.equal(queries[0].get("location"), "test location");
    assert.equal(queries[0].get("kind"), kind);
    assert.equal(queries[0].get("from"), midnight.toISOString());
    assert.equal(queries[0].get("to"), "2026-11-06T00:00:00.000Z");
    assert.equal(queries[0].get("offset"), "0");
    assert.equal(queries[1].get("offset"), "2");
    assert.equal(queries[2].get("offset"), "4");
    assert.equal(queries[2].get("polled_to"), queries[0].get("polled_to"));
    assert.ok(queries[0].get("polled_to"));
    assert.equal(queries[0].has("order"), false);
  }
});

test("pagination surfaces failed pages and malformed responses", async () => {
  let page = 0;
  await assert.rejects(evolution.loadRecords(async () => {
    if (page++ === 0) return [reading(), reading()];
    throw new Error("Page failed");
  }, "test", target, 2), /Page failed/);
  await assert.rejects(evolution.loadRecords(async () => ({}), "test", target), /Invalid readings response/);
  await assert.rejects(evolution.loadRecords(async () => [reading(), reading()], "test", target, 1), /Invalid readings response/);
});
