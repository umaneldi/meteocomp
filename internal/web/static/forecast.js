"use strict";

const ForecastEvolution = (() => {
  const HOUR = 3600e3;
  const DAY = 24 * HOUR;
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

  function buildObservation(records, target) {
    const ds = dayStart(target);
    const byProvider = new Map();
    for (const r of records) {
      const time = new Date(r.forecast_time).getTime();
      if (r.kind !== "observation" || !Number.isFinite(time) || time < ds || time >= ds + DAY) continue;
      const previous = byProvider.get(r.provider);
      if (!previous || previous.forecast_time < r.forecast_time ||
          (previous.forecast_time === r.forecast_time && previous.polled_at < r.polled_at)) {
        byProvider.set(r.provider, r);
      }
    }
    return [...byProvider.entries()].map(([provider, latest]) => ({
      provider,
      values: Object.fromEntries(params.map((param) => {
        const value = latest[param.key] ?? (param.fallback ? latest[param.fallback] : null);
        return [param.key, Number.isFinite(value)
          ? { value, source: `Observed at ${latest.forecast_time.slice(11, 16)}Z`, partial: false } : null];
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

  return { params, today, loadRecords, loadObservations, buildObservation, build };
})();
