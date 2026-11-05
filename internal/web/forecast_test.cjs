"use strict";

const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const context = vm.createContext({ URLSearchParams });
vm.runInContext(fs.readFileSync(path.join(__dirname, "static/forecast.js"), "utf8"), context);
const evolution = vm.runInContext("ForecastEvolution", context);
const midnight = new Date("2026-11-05T00:00:00Z"); // UTC day boundary — used for data generation
const target = new Date("2026-11-05T12:00:00Z");   // noon — lead reference and display target
const plain = (value) => JSON.parse(JSON.stringify(value));

function reading(overrides = {}) {
  return {
    id: 1, poll_id: 1, provider: "first", polled_at: "2026-11-04T00:00:00Z",
    forecast_time: midnight.toISOString(), granularity: "daily", kind: "forecast",
    temperature_c: 10, ...overrides,
  };
}

function hours(count = 24, overrides = {}) {
  return Array.from({ length: count }, (_, hour) => reading({
    id: hour + 1, granularity: "hourly",
    forecast_time: new Date(midnight.getTime() + hour * 3600e3).toISOString(),
    ...overrides,
  }));
}

test("UTC target, cutoff, kind and date filtering preserve each eligible revision", () => {
  assert.equal(evolution.today(new Date("2026-11-04T23:30:00-03:00")).toISOString(), target.toISOString());
  const rows = [
    reading(),
    reading({ id: 2, poll_id: 2, polled_at: "2026-11-04T12:30:00Z" }),
    reading({ id: 3, poll_id: 3, polled_at: midnight.toISOString() }),
    reading({ id: 4, poll_id: 4, polled_at: "2026-11-05T00:00:01Z" }),
    reading({ id: 5, poll_id: 5, kind: "observation" }),
    reading({ id: 6, poll_id: 6, forecast_time: "2026-11-06T00:00:00Z" }),
    reading({ id: 7, poll_id: 7, forecast_time: "2026-11-04T23:59:59Z" }),
  ];
  const data = evolution.build(rows, target);
  assert.deepEqual(plain(data.leads), [36, 23.5, 12]);
  assert.deepEqual(plain(data.series[0].points.map((p) => p.pollID)), [1, 2, 3]);
});

test("native daily values take precedence per metric; missing mean never falls back to native max", () => {
  const rows = [
    ...hours(24, { temperature_c: 8, wind_speed_kmh: 5 }),
    reading({ id: 30, temperature_c: undefined, temperature_max_c: 20, wind_speed_kmh: 12 }),
  ];
  const data = evolution.build(rows, target);
  assert.equal(data.series[0].points.length, 1);
  const values = data.series[0].points[0].values;
  assert.equal(values.temperature_c.value, 8);
  assert.equal(values.temperature_c.source, "Derived hourly");
  assert.equal(values.temperature_c.partial, false);
  assert.equal(values.temperature_max_c.value, 20);
  assert.equal(values.wind_speed_kmh.value, 12);
  assert.equal(values.wind_speed_kmh.source, "Native daily");
  assert.equal(evolution.build([rows.at(-1)], target).series[0].points[0].values.temperature_c, null);
});

test("all reducers, extrema fallback, missing values and genuine zero", () => {
  const rows = hours(2, {
    humidity_pct: 50, pressure_hpa: 1000, cloud_cover_pct: 10, visibility_km: 8,
    wind_speed_kmh: 4, wind_gust_kmh: 5, feels_like_c: 8, uv_index: 1, precipitation_mm: 0,
  });
  Object.assign(rows[1], {
    temperature_c: 20, temperature_min_c: 15, temperature_max_c: 25,
    humidity_pct: 70, pressure_hpa: 1020, cloud_cover_pct: 30, visibility_km: 10,
    wind_speed_kmh: 8, wind_gust_kmh: 12, feels_like_c: 22, uv_index: 4, precipitation_mm: 2,
  });
  const values = evolution.build(rows, target).series[0].points[0].values;
  const expected = {
    temperature_c: 15, temperature_min_c: 10, temperature_max_c: 25,
    humidity_pct: 60, pressure_hpa: 1010, cloud_cover_pct: 20, visibility_km: 9,
    wind_speed_kmh: 8, wind_gust_kmh: 12, feels_like_c: 22, uv_index: 4, precipitation_mm: 2,
  };
  for (const [key, value] of Object.entries(expected)) {
    assert.equal(values[key].value, value, key);
    assert.equal(values[key].coverage, 2, key);
    assert.equal(values[key].partial, true, key);
  }
  const zero = evolution.build([reading({ temperature_c: 0 })], target).series[0].points[0].values;
  assert.equal(zero.temperature_c.value, 0);
  assert.equal(zero.humidity_pct, null);
});

test("3-hourly fallback, hourly precedence, duplicates and per-parameter coverage", () => {
  const threeHourly = Array.from({ length: 8 }, (_, i) => reading({
    id: i + 1, granularity: "3h",
    forecast_time: new Date(midnight.getTime() + i * 3 * 3600e3).toISOString(),
    temperature_c: i, precipitation_mm: 1,
  }));
  let values = evolution.build(threeHourly, target).series[0].points[0].values;
  assert.equal(values.temperature_c.value, 3.5);
  assert.equal(values.temperature_c.coverage, 24);
  assert.equal(values.precipitation_mm.value, 8);
  const hourly = hours(2, { id: 20, temperature_c: 10, humidity_pct: 50 });
  hourly[1].id = 21;
  delete hourly[1].humidity_pct;
  const duplicate = { ...hourly[0], id: 22, temperature_c: 30 };
  values = evolution.build([...threeHourly, ...hourly, duplicate], target).series[0].points[0].values;
  assert.equal(values.temperature_c.value, 20);
  assert.equal(values.temperature_c.coverage, 2);
  assert.equal(values.temperature_c.slots, 2);
  assert.equal(values.humidity_pct.coverage, 1);
  assert.equal(values.humidity_pct.slots, 1);
  assert.equal(values.humidity_pct.totalSlots, 2);
  assert.equal(values.precipitation_mm, null); // Do not mix resolutions per parameter.
});

test("coverage unions overlaps and clips at UTC midnight", () => {
  const rows = [0, 1, 23].map((hour, i) => reading({
    id: i + 1, granularity: "3h",
    forecast_time: new Date(midnight.getTime() + hour * 3600e3).toISOString(),
  }));
  const value = evolution.build(rows, target).series[0].points[0].values.temperature_c;
  assert.equal(value.coverage, 5);
  assert.equal(value.partial, true);
});

test("provider ordering and asynchronous or simultaneous polls retain all points", () => {
  const data = evolution.build([
    reading({ provider: "z" }), reading({ provider: "a" }),
    reading({ provider: "configured" }),
    reading({ id: 2, poll_id: 2, provider: "configured", polled_at: "2026-11-04T23:59:59Z" }),
    reading({ id: 3, poll_id: 3, provider: "configured" }),
  ], target, ["configured", "removed", "configured"]);
  assert.deepEqual(plain(data.series.map((s) => s.provider)), ["configured", "a", "z"]);
  assert.equal(data.series[0].points.length, 3);
  assert.deepEqual(plain(data.leads), [36, 12 + 1 / 3600]);
});

test("pagination loads all pages including an exact multiple and keeps filters", async () => {
  const queries = [];
  const pages = [[reading({ id: 4 }), reading({ id: 3 })], [reading({ id: 2 }), reading()], []];
  const rows = await evolution.loadRecords(async (query) => {
    queries.push(new URLSearchParams(query));
    return pages.shift();
  }, "test location", target, 2);
  assert.equal(rows.length, 4);
  assert.equal(queries.length, 3);
  assert.equal(queries[0].get("location"), "test location");
  assert.equal(queries[0].get("kind"), "forecast");
  assert.equal(queries[0].get("from"), midnight.toISOString());
  assert.equal(queries[0].get("to"), "2026-11-06T00:00:00.000Z");
  assert.equal(queries[0].get("before_id"), null);
  assert.equal(queries[1].get("before_id"), "3");
  assert.equal(queries[2].get("before_id"), "1");
});

test("pagination surfaces errors instead of returning partial or looping data", async () => {
  let page = 0;
  await assert.rejects(evolution.loadRecords(async () => {
    if (page++ === 0) return [reading({ id: 2 }), reading()];
    throw new Error("Page failed");
  }, "test", target, 2), /Page failed/);
  await assert.rejects(evolution.loadRecords(async () => [reading()], "test", target, 1), /pagination cursor/);
  await assert.rejects(evolution.loadRecords(async () => ({}), "test", target), /Invalid readings response/);
});
