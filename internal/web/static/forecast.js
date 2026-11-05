"use strict";

const ForecastEvolution = (() => {
  const HOUR = 3600e3;
  const DAY = 24 * HOUR;
  const params = [
    { key: "temperature_c", label: "Daily mean temperature", unit: "°C", reducer: "mean" },
    { key: "temperature_min_c", label: "Daily minimum temperature", unit: "°C", reducer: "min", fallback: "temperature_c" },
    { key: "temperature_max_c", label: "Daily maximum temperature", unit: "°C", reducer: "max", fallback: "temperature_c" },
    { key: "feels_like_c", label: "Daily maximum feels-like", unit: "°C", reducer: "max" },
    { key: "precipitation_mm", label: "Daily precipitation total", unit: "mm", reducer: "sum" },
    { key: "humidity_pct", label: "Daily mean humidity", unit: "%", reducer: "mean" },
    { key: "wind_speed_kmh", label: "Daily maximum wind speed", unit: "km/h", reducer: "max" },
    { key: "wind_gust_kmh", label: "Daily maximum wind gust", unit: "km/h", reducer: "max" },
    { key: "pressure_hpa", label: "Daily mean pressure", unit: "hPa", reducer: "mean" },
    { key: "cloud_cover_pct", label: "Daily mean cloud cover", unit: "%", reducer: "mean" },
    { key: "uv_index", label: "Daily maximum UV index", unit: "", reducer: "max" },
    { key: "visibility_km", label: "Daily mean visibility", unit: "km", reducer: "mean" },
  ];

  function today(now = new Date()) {
    return new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate(), 12));
  }

  // dayStart returns the UTC midnight that begins the calendar day containing target (noon).
  function dayStart(target) { return target.getTime() - 12 * HOUR; }

  async function loadRecords(getPage, location, target, pageSize = 1000) {
    const ds = dayStart(target);
    const query = new URLSearchParams({
      location, kind: "forecast",
      from: new Date(ds).toISOString(),
      to: new Date(ds + DAY).toISOString(),
      order: "id_desc", limit: String(pageSize),
    });
    const records = [];
    let before = Infinity;
    for (;;) {
      const page = await getPage(query);
      if (!Array.isArray(page)) throw new Error("Invalid readings response");
      for (const record of page) {
        if (!Number.isSafeInteger(record.id) || record.id <= 0 || record.id >= before) {
          throw new Error("Invalid readings pagination cursor");
        }
        before = record.id;
        records.push(record);
      }
      if (page.length < pageSize) return records;
      query.set("before_id", String(before));
    }
  }

  async function loadObservations(getPage, location, target, pageSize = 1000) {
    const ds = dayStart(target);
    const query = new URLSearchParams({
      location, kind: "observation",
      from: new Date(ds).toISOString(),
      to: new Date(ds + DAY).toISOString(),
      order: "id_desc", limit: String(pageSize),
    });
    const records = [];
    let before = Infinity;
    for (;;) {
      const page = await getPage(query);
      if (!Array.isArray(page)) throw new Error("Invalid readings response");
      for (const record of page) {
        if (!Number.isSafeInteger(record.id) || record.id <= 0 || record.id >= before) {
          throw new Error("Invalid readings pagination cursor");
        }
        before = record.id;
        records.push(record);
      }
      if (page.length < pageSize) return records;
      query.set("before_id", String(before));
    }
  }

  function buildObservation(records, target) {
    const ds = dayStart(target);
    const byProvider = new Map();
    for (const r of records) {
      const time = new Date(r.forecast_time).getTime();
      if (r.kind !== "observation" || time < ds || time >= ds + DAY) continue;
      if (!["hourly", "3h"].includes(r.granularity)) continue;
      if (!byProvider.has(r.provider)) byProvider.set(r.provider, new Map());
      const slots = byProvider.get(r.provider);
      if (!slots.has(r.forecast_time) || slots.get(r.forecast_time).id < r.id) slots.set(r.forecast_time, r);
    }
    return [...byProvider.entries()].map(([provider, slotMap]) => {
      const slots = [...slotMap.values()].sort((a, b) => (a.forecast_time < b.forecast_time ? -1 : 1));
      const latest = slots[slots.length - 1];
      return {
        provider,
        values: Object.fromEntries(params.map((param) => {
          if (!latest) return [param.key, null];
          const value = latest[param.key] ?? (param.fallback ? latest[param.fallback] : null);
          if (!Number.isFinite(value)) return [param.key, null];
          return [param.key, { value, source: `Observed at ${latest.forecast_time.slice(11, 16)}Z`, partial: false }];
        })),
      };
    });
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

  function dailyValue(native, slots, param, ds) {
    if (Number.isFinite(native?.[param.key])) {
      return { value: native[param.key], source: "Native daily", partial: false };
    }
    const available = slots.flatMap((r) => {
      const value = r[param.key] ?? (param.fallback ? r[param.fallback] : null);
      if (!Number.isFinite(value)) return [];
      const start = new Date(r.forecast_time).getTime();
      const end = Math.min(start + (r.granularity === "3h" ? 3 : 1) * HOUR, ds + DAY);
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
      value, source: `Derived ${slots[0].granularity}`, coverage,
      slots: available.length, totalSlots: slots.length, partial: coverage < 24,
    };
  }

  function build(records, target, providerOrder = [], maxLeadHours = Infinity) {
    const noon = target.getTime();
    const ds = dayStart(target); // midnight of the same UTC day
    const groups = new Map();
    for (const r of records) {
      const time = new Date(r.forecast_time).getTime();
      const polled = new Date(r.polled_at).getTime();
      if (!Number.isFinite(time) || !Number.isFinite(polled)) throw new Error("Invalid forecast timestamp");
      if (r.kind !== "forecast" || time < ds || time >= ds + DAY || polled > ds) continue;
      if (!["daily", "hourly", "3h"].includes(r.granularity)) continue;
      const key = JSON.stringify([r.provider, r.poll_id]);
      if (!groups.has(key)) groups.set(key, new Map());
      const slots = groups.get(key);
      const slot = `${r.granularity}:${time}`;
      if (!slots.has(slot) || slots.get(slot).id < r.id) slots.set(slot, r);
    }
    const byProvider = new Map();
    for (const group of groups.values()) {
      const records = [...group.values()];
      const representative = records[0];
      const lead = (noon - new Date(representative.polled_at).getTime()) / HOUR;
      if (lead > maxLeadHours) continue;
      const native = records.filter((r) => r.granularity === "daily").sort((a, b) => b.id - a.id)[0];
      const hourly = records.filter((r) => r.granularity === "hourly");
      const slots = hourly.length ? hourly : records.filter((r) => r.granularity === "3h");
      const point = {
        provider: representative.provider, pollID: representative.poll_id,
        polledAt: representative.polled_at, lead,
        values: Object.fromEntries(params.map((param) => [param.key, dailyValue(native, slots, param, ds)])),
      };
      if (!byProvider.has(point.provider)) byProvider.set(point.provider, []);
      byProvider.get(point.provider).push(point);
    }
    const names = [...new Set(providerOrder)].filter((name) => byProvider.has(name));
    names.push(...[...byProvider.keys()].filter((name) => !names.includes(name)).sort());
    const series = names.map((provider) => ({
      provider,
      points: byProvider.get(provider).sort((a, b) => b.lead - a.lead || a.pollID - b.pollID),
    }));
    const maxLead = Number.isFinite(maxLeadHours) ? maxLeadHours
      : series.flatMap((s) => s.points).reduce((m, p) => Math.max(m, p.lead), 0);
    return {
      series,
      leads: [...new Set(series.flatMap((s) => s.points.map((p) => p.lead)))].sort((a, b) => b - a),
      maxLead,
    };
  }

  return { params, today, loadRecords, loadObservations, buildObservation, build };
})();
