"use strict";

const ForecastEvolution = (() => {
  const HOUR = 3600e3;
  const DAY = 24 * HOUR;
  const CONDITION_ORDER = {
    clear: 0, partly_cloudy: 1, cloudy: 2, fog: 3, drizzle: 4,
    rain: 5, freezing_rain: 6, sleet: 7, snow: 8, thunderstorm: 9, unknown: 10,
  };
  const params = [
    { key: "temperature_c", label: "Daily mean temperature", unit: "°C", reducer: "mean" },
    { key: "temperature_min_c", label: "Daily minimum temperature", unit: "°C", reducer: "min", fallback: "temperature_c" },
    { key: "temperature_max_c", label: "Daily maximum temperature", unit: "°C", reducer: "max", fallback: "temperature_c" },
    { key: "feels_like_c", label: "Daily maximum feels-like", unit: "°C", reducer: "max" },
    { key: "humidity_pct", label: "Daily mean humidity", unit: "%", reducer: "mean" },
    { key: "dew_point_c", label: "Daily mean dew point", unit: "°C", reducer: "mean" },
    { key: "wind_speed_kmh", label: "Daily maximum wind speed", unit: "km/h", reducer: "max" },
    { key: "wind_gust_kmh", label: "Daily maximum wind gust", unit: "km/h", reducer: "max" },
    { key: "pressure_hpa", label: "Daily mean pressure", unit: "hPa", reducer: "mean" },
    { key: "cloud_cover_pct", label: "Daily mean cloud cover", unit: "%", reducer: "mean" },
    { key: "precipitation_probability_pct", label: "Daily maximum rain probability", unit: "%", reducer: "max" },
    { key: "snowfall_cm", label: "Daily snowfall total", unit: "cm", reducer: "sum" },
    { key: "visibility_km", label: "Daily mean visibility", unit: "km", reducer: "mean" },
    { key: "condition", label: "Condition", unit: "condition", type: "categorical", reducer: "mode" },
  ];

  function today(now = new Date()) {
    return new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate(), 12));
  }

  function dayStart(target) { return target.getTime() - 12 * HOUR; }
  function slotHours(record) { return record.provider_type === "openweathermap" ? 3 : 1; }

  async function load(getPage, location, target, kind, pageSize) {
    const ds = dayStart(target);
    const query = new URLSearchParams({
      location, kind,
      from: new Date(ds).toISOString(),
      to: new Date(ds + DAY).toISOString(),
      polled_to: kind === "forecast" ? new Date(ds + 1000).toISOString() : new Date().toISOString(),
      limit: String(pageSize), offset: "0",
    });
    const records = [];
    for (;;) {
      const page = await getPage(query);
      if (!Array.isArray(page) || page.length > pageSize) throw new Error("Invalid readings response");
      records.push(...page);
      if (page.length < pageSize) return records;
      query.set("offset", String(records.length));
    }
  }

  function loadRecords(getPage, location, target, pageSize = 1000) {
    return load(getPage, location, target, "forecast", pageSize);
  }

  function loadObservations(getPage, location, target, pageSize = 1000) {
    return load(getPage, location, target, "observation", pageSize);
  }

  function isKnownCondition(c) { return c != null && c !== "unknown" && CONDITION_ORDER[c] != null; }

  function isNewer(r, previous) {
    return !previous || previous.forecast_time < r.forecast_time ||
      (previous.forecast_time === r.forecast_time && previous.polled_at < r.polled_at);
  }

  // Returns Map(provider → latest observation with a known condition) within the 24 h before `before`.
  async function loadLatestKnownConditions(getPage, location, before, pageSize = 1000) {
    const end = before instanceof Date ? before.getTime() : new Date(before).getTime();
    const query = new URLSearchParams({
      location, kind: "observation",
      from: new Date(end - DAY).toISOString(),
      to: new Date(end + 1000).toISOString(),
      limit: String(pageSize), offset: "0",
    });
    const latest = new Map();
    for (;;) {
      const page = await getPage(query);
      if (!Array.isArray(page) || page.length > pageSize) throw new Error("Invalid readings response");
      for (const r of page) {
        if (new Date(r.forecast_time).getTime() > end) continue;
        if (isKnownCondition(r.condition) && isNewer(r, latest.get(r.provider))) latest.set(r.provider, r);
      }
      if (page.length < pageSize) return latest;
      query.set("offset", String(Number(query.get("offset")) + page.length));
    }
  }

  function buildObservation(records, target) {
    const ds = dayStart(target);
    const byProvider = new Map();
    const knownCondition = new Map();
    for (const r of records) {
      const time = new Date(r.forecast_time).getTime();
      if (r.kind !== "observation" || !Number.isFinite(time) || time < ds || time >= ds + DAY) continue;
      if (isNewer(r, byProvider.get(r.provider))) byProvider.set(r.provider, r);
      if (isKnownCondition(r.condition) && isNewer(r, knownCondition.get(r.provider))) {
        knownCondition.set(r.provider, r);
      }
    }
    return [...byProvider.entries()].map(([provider, latestAny]) => ({
      provider,
      values: Object.fromEntries(params.map((param) => {
        // Conditions come from the latest observation whose condition is known.
        const latest = param.type === "categorical" ? knownCondition.get(provider) : latestAny;
        if (!latest) return [param.key, null];
        const value = latest[param.key] ?? (param.fallback ? latest[param.fallback] : null);
        const src = { value, source: `Observed at ${latest.forecast_time.slice(11, 16)}Z`, partial: false };
        const catOrd = param.type === "categorical" ? CONDITION_ORDER[value] : undefined;
        return [param.key, param.type === "categorical"
          ? (catOrd != null ? { ...src, value: catOrd } : null)
          : (Number.isFinite(value) ? src : null)];
      })),
    }));
  }

  function coverageHours(intervals) {
    const sorted = intervals.slice().sort((a, b) => a[0] - b[0]);
    let covered = 0, end = -Infinity;
    for (const [from, to] of sorted) {
      covered += Math.max(0, to - Math.max(from, end));
      end = Math.max(end, to);
    }
    return covered / HOUR;
  }

  function dailyValue(slots, param, ds) {
    if (param.type === "categorical") {
      const freq = new Map();
      for (const r of slots) {
        const ord = CONDITION_ORDER[r[param.key]];
        if (ord != null) freq.set(ord, (freq.get(ord) || 0) + 1);
      }
      if (!freq.size) return null;
      const mode = [...freq.entries()].reduce((a, b) => b[1] > a[1] ? b : a)[0];
      return { value: mode, source: "Most common", coverage: null, partial: false };
    }
    const available = slots.flatMap((r) => {
      const value = r[param.key] ?? (param.fallback ? r[param.fallback] : null);
      if (!Number.isFinite(value)) return [];
      const start = new Date(r.forecast_time).getTime();
      const end = Math.min(start + slotHours(r) * HOUR, ds + DAY);
      return [{ value, interval: [start, end], weight: (end - start) / HOUR }];
    });
    if (!available.length) return null;
    const values = available.map((v) => v.value);
    let value;
    switch (param.reducer) {
      case "min": value = values.reduce((a, b) => Math.min(a, b)); break;
      case "max": value = values.reduce((a, b) => Math.max(a, b)); break;
      case "sum": value = values.reduce((a, b) => a + b, 0); break;
      default:
        value = available.reduce((sum, v) => sum + v.value * v.weight, 0) /
          available.reduce((sum, v) => sum + v.weight, 0);
    }
    const coverage = coverageHours(available.map((v) => v.interval));
    return {
      value, source: `Derived ${slotHours(slots[0]) === 3 ? "3h" : "hourly"}`, coverage,
      slots: available.length, totalSlots: slots.length, partial: coverage < 24,
    };
  }

  function build(records, target, providerOrder = [], maxLeadHours = Infinity) {
    const ds = dayStart(target);
    const groups = new Map();
    for (const r of records) {
      const time = new Date(r.forecast_time).getTime();
      const polled = new Date(r.polled_at).getTime();
      if (!Number.isFinite(time) || !Number.isFinite(polled)) throw new Error("Invalid forecast timestamp");
      if (r.kind !== "forecast" || time < ds || time >= ds + DAY || polled > ds) continue;
      const key = JSON.stringify([r.provider, r.polled_at]);
      if (!groups.has(key)) groups.set(key, new Map());
      const slots = groups.get(key);
      if (!slots.has(time)) slots.set(time, r);
    }
    const byProvider = new Map();
    for (const group of groups.values()) {
      const slots = [...group.values()];
      const representative = slots[0];
      const lead = (target.getTime() - new Date(representative.polled_at).getTime()) / HOUR;
      if (lead > maxLeadHours) continue;
      const point = {
        provider: representative.provider, polledAt: representative.polled_at, lead,
        values: Object.fromEntries(params.map((param) => [param.key, dailyValue(slots, param, ds)])),
      };
      if (!byProvider.has(point.provider)) byProvider.set(point.provider, []);
      byProvider.get(point.provider).push(point);
    }
    const names = [...new Set(providerOrder)].filter((name) => byProvider.has(name));
    names.push(...[...byProvider.keys()].filter((name) => !names.includes(name)).sort());
    const series = names.map((provider) => ({
      provider, points: byProvider.get(provider).sort((a, b) => b.lead - a.lead),
    }));
    return {
      series,
      leads: [...new Set(series.flatMap((s) => s.points.map((p) => p.lead)))].sort((a, b) => b - a),
      maxLead: Number.isFinite(maxLeadHours) ? maxLeadHours
        : series.flatMap((s) => s.points).reduce((m, p) => Math.max(m, p.lead), 0),
    };
  }

  async function findLatestObsTime(getPage, location) {
    const query = new URLSearchParams({ kind: "observation", location, limit: "500" });
    const page = await getPage(query);
    if (!Array.isArray(page) || !page.length) return null;
    let best = null;
    for (const r of page) {
      if (!best || r.forecast_time > best.forecast_time) best = r;
    }
    return { forecastTime: new Date(best.forecast_time), provider: best.provider };
  }

  async function loadLeadRecords(getPage, location, forecastTime, kind, pageSize = 500) {
    const ft = forecastTime instanceof Date
      ? forecastTime.toISOString().slice(0, 19) + "Z"
      : String(forecastTime);
    const query = new URLSearchParams({
      kind, location, forecast_time: ft,
      limit: String(pageSize), offset: "0",
    });
    const records = [];
    for (;;) {
      const page = await getPage(query);
      if (!Array.isArray(page) || page.length > pageSize) throw new Error("Invalid readings response");
      records.push(...page);
      if (page.length < pageSize) return records;
      query.set("offset", String(records.length));
    }
  }

  function buildLeadSeries(forecastRecords, obsRecords, paramKey, knownConditions = new Map()) {
    const fcByProvider = new Map();
    for (const r of forecastRecords) {
      let v = r[paramKey];
      if (v == null) continue;
      if (typeof v === "string") { v = CONDITION_ORDER[v]; if (v == null) continue; }
      if (!fcByProvider.has(r.provider)) fcByProvider.set(r.provider, []);
      fcByProvider.get(r.provider).push({ x: r.lead_time_hours, y: v });
    }
    const series = [...fcByProvider.entries()]
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([provider, points]) => ({ provider, points: points.sort((a, b) => a.x - b.x) }));

    const obsByProvider = new Map();
    if (paramKey === "condition") {
      // Use the latest known condition rather than an unfilled "unknown" one.
      for (const [provider, r] of knownConditions) {
        obsByProvider.set(provider, { r, v: CONDITION_ORDER[r.condition] });
      }
    }
    for (const r of paramKey === "condition" ? [] : obsRecords) {
      let v = r[paramKey];
      if (v == null) continue;
      if (typeof v === "string") { v = CONDITION_ORDER[v]; if (v == null) continue; }
      const prev = obsByProvider.get(r.provider);
      if (!prev || r.forecast_time > prev.r.forecast_time) obsByProvider.set(r.provider, { r, v });
    }
    const obsSeries = [...obsByProvider.entries()]
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([provider, { v }]) => ({ provider, y: v }));

    let xMin = Infinity, xMax = -Infinity;
    for (const s of series) for (const p of s.points) {
      xMin = Math.min(xMin, p.x);
      xMax = Math.max(xMax, p.x);
    }
    if (!Number.isFinite(xMin)) { xMin = 0; xMax = 0; }

    return { series, obsSeries, xMin, xMax };
  }

  return { params, CONDITION_ORDER, today, loadRecords, loadObservations, buildObservation, build, findLatestObsTime, loadLeadRecords, loadLatestKnownConditions, buildLeadSeries };
})();
