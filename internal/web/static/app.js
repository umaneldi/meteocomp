// meteocomp settings UI. Vanilla JS, no build step. All dynamic text goes
// through textContent (via h()) so provider error messages cannot inject markup.
"use strict";

const PRESETS = ["15m", "30m", "1h", "3h", "6h", "12h"];

const $ = (id) => document.getElementById(id);

const state = {
  types: [],      // provider type descriptions from /api/v1/provider-types
  interval: "1h",
  forecastDays: 7,
  locations: [],
  providers: [],
  lastDoc: null,  // last settings document received from the server
  baseline: "",   // serialized settings matching lastDoc, for dirty tracking
  saving: false,
};
let uid = 0;

const TABS = {
  forecast: ["forecast"],
  activity: ["activity"],
  settings: ["polling", "locations", "providers"],
};
let currentTab = "forecast";

function setTab(name) {
  currentTab = name;
  for (const [tab, ids] of Object.entries(TABS)) {
    const active = tab === name;
    $(`tab-${tab}`).setAttribute("aria-selected", String(active));
    for (const id of ids) {
      const el = $(id);
      if (el) el.hidden = !active;
    }
  }
  if (name === "forecast" && fc.ready) loadForecast();
}

const PARAMS = ForecastEvolution.params;

const CHART_COLORS = ["#e8590c","#1c7ed6","#2f9e44","#9c36b5","#f08c00","#0c8599","#e03131","#364fc7"];
const SVG_NS = "http://www.w3.org/2000/svg";

function svgEl(tag, attrs = {}) {
  const el = document.createElementNS(SVG_NS, tag);
  for (const [k, v] of Object.entries(attrs)) if (v != null) el.setAttribute(k, String(v));
  return el;
}

function fmtParamValue(v, param) {
  if (v == null) return "—";
  if (param.unit === "°C") return `${v > 0 ? "+" : ""}${Math.round(v)}°`;
  if (param.unit === "%")  return `${Math.round(v)}%`;
  if (param.unit === "hPa") return String(Math.round(v));
  if (param.unit === "mm")  return `${v.toFixed(1)} mm`;
  if (param.unit === "km/h") return `${Math.round(v)} km/h`;
  if (param.unit === "km")   return `${v.toFixed(1)} km`;
  return v.toFixed(1);
}

function niceYTicks(min, max, count) {
  const range = max - min || 1;
  const raw = range / count;
  const mag = Math.pow(10, Math.floor(Math.log10(raw)));
  const nice = [1, 2, 2.5, 5, 10].find(f => f * mag >= raw) * mag;
  const start = Math.ceil(min / nice) * nice;
  const ticks = [];
  for (let t = start; t <= max + nice * 0.01; t = parseFloat((t + nice).toFixed(10))) ticks.push(t);
  return ticks;
}

// ── Helpers ────────────────────────────────────────────────────────────────
function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v == null || v === false) continue;
    if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "class") el.className = v;
    else if (k === "value") el.value = v;
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat()) {
    if (c != null && c !== false) el.append(c.nodeType ? c : String(c));
  }
  return el;
}

async function api(method, path, body, signal) {
  const res = await fetch(path, {
    method,
    headers: body ? { "Content-Type": "application/json" } : {},
    body: body ? JSON.stringify(body) : undefined,
    signal,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || `${res.status} ${res.statusText}`);
  return data;
}

function toast(msg, isError = false) {
  const t = $("toast");
  t.textContent = msg;
  t.className = "toast" + (isError ? " error" : "");
  t.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => (t.hidden = true), isError ? 6000 : 3000);
}

// "1h30m0s" (Go's Duration.String) -> "1h30m"
function prettyDuration(s) {
  return s.replace(/([hm])0s$/, "$1").replace(/h0m$/, "h");
}

function durationMs(s) {
  const m = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$/.exec(s.trim());
  if (!m || s.trim() === "") return NaN;
  return ((+m[1] || 0) * 3600 + (+m[2] || 0) * 60 + (+m[3] || 0)) * 1000;
}

function slug(s) {
  return s.normalize("NFD").replace(/[\u0300-\u036f]/g, "").toLowerCase()
    .replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "") || "location";
}

function uniqueName(base, taken) {
  let name = base;
  for (let i = 2; taken.has(name); i++) name = `${base}-${i}`;
  return name;
}

function relTime(iso) {
  const sec = Math.round((Date.now() - new Date(iso).getTime()) / 1000);
  if (sec < 45) return "just now";
  if (sec < 3600) return `${Math.round(sec / 60)} min ago`;
  if (sec < 86400) return `${Math.round(sec / 3600)} h ago`;
  return `${Math.round(sec / 86400)} d ago`;
}

const typeInfo = (t) => state.types.find((x) => x.type === t);

// ── State <-> server document ─────────────────────────────────────────────
function loadDoc(doc) {
  state.lastDoc = doc;
  state.interval = prettyDuration(doc.poll_interval);
  state.forecastDays = doc.forecast_days || 7;
  state.locations = doc.locations.map((l) => ({ ...l, _id: ++uid }));
  state.providers = doc.providers.map((p) => ({
    _id: ++uid,
    orig: p.name,
    origType: p.type,
    savedEnabled: p.enabled,
    name: p.name,
    type: p.type,
    enabled: p.enabled,
    options: { ...p.options },
    has_api_key: p.has_api_key,
    api_key_hint: p.api_key_hint || "",
    newKey: "",
    clearKey: false,
  }));
  state.baseline = JSON.stringify(serialize());
}

function serialize() {
  return {
    poll_interval: state.interval.trim(),
    forecast_days: state.forecastDays,
    locations: state.locations.map((l) => ({
      name: l.name.trim(),
      city: l.city.trim(),
      country: l.country.trim().toUpperCase(),
      latitude: Number(l.latitude),
      longitude: Number(l.longitude),
      station_id: (l.station_id || "").trim(),
    })),
    providers: state.providers.map((p) => {
      const d = { name: p.name.trim(), type: p.type, enabled: p.enabled, options: cleanOptions(p) };
      if (p.orig && p.orig !== d.name) d.previous_name = p.orig;
      if (p.newKey.trim()) d.api_key = p.newKey.trim();
      else if (p.clearKey) d.api_key = "";
      return d;
    }),
  };
}

function cleanOptions(p) {
  const out = {};
  for (const [k, v] of Object.entries(p.options)) {
    if (v === "" || v == null) continue;
    out[k] = v;
  }
  return out;
}

// Whether the provider will have an API key after saving.
function keyFromEnv(p) {
  const info = typeInfo(p.type);
  return info?.api_key_env_set ? info.api_key_env : "";
}

function keyAfterSave(p) {
  if (keyFromEnv(p)) return true;
  if (p.newKey.trim()) return true;
  if (p.clearKey) return false;
  return p.has_api_key && p.type === p.origType;
}

// ── Validation & dirty state ──────────────────────────────────────────────
function problems() {
  const out = [];
  const ms = durationMs(state.interval);
  if (!(ms >= 60000)) out.push("Polling interval must be a duration of at least 1m.");
  const fd = state.forecastDays;
  if (!Number.isInteger(fd) || fd < 1 || fd > 16) out.push("Forecast days must be an integer between 1 and 16.");
  const names = new Set();
  for (const l of state.locations) {
    const n = l.name.trim();
    if (!n) out.push("Every location needs a name.");
    else if (names.has(n)) out.push(`Duplicate location name “${n}”.`);
    names.add(n);
    if (!validCoord(l.latitude, 90) || !validCoord(l.longitude, 180)) out.push(`Location “${n || "?"}” has invalid coordinates.`);
  }
  const pnames = new Set();
  for (const p of state.providers) {
    const n = p.name.trim();
    if (!n) out.push("Every provider needs a name.");
    else if (pnames.has(n)) out.push(`Duplicate provider name “${n}”.`);
    pnames.add(n);
    const info = typeInfo(p.type);
    if (p.enabled && info?.requires_api_key && !keyAfterSave(p)) out.push(`Provider “${n}” is enabled but has no API key.`);
  }
  return out;
}

function validCoord(v, max) {
  return v !== "" && v != null && Number.isFinite(Number(v)) && Math.abs(Number(v)) <= max;
}

function updateDirty(errorMsg) {
  const dirty = JSON.stringify(serialize()) !== state.baseline;
  const issues = problems();
  const bar = $("savebar");
  const msg = $("savebar-msg");
  bar.hidden = !dirty && !errorMsg;
  msg.classList.toggle("error", Boolean(errorMsg || issues.length));
  msg.textContent = errorMsg || issues[0] || "Unsaved changes";
  $("save").disabled = state.saving || issues.length > 0 || !dirty;
  $("discard").disabled = state.saving;
  $("interval").setAttribute("aria-invalid", String(!(durationMs(state.interval) >= 60000)));
  $("forecast-days").setAttribute("aria-invalid", String(
    !Number.isInteger(state.forecastDays) || state.forecastDays < 1 || state.forecastDays > 16));
  for (const b of $("interval-presets").children) b.setAttribute("aria-pressed", String(b.dataset.v === state.interval.trim()));
}

// ── Polling interval & global settings ────────────────────────────────────
function renderInterval() {
  const presets = $("interval-presets");
  presets.replaceChildren(...PRESETS.map((v) => h("button", {
    type: "button", "data-v": v,
    onclick: () => { state.interval = v; $("interval").value = v; updateDirty(); },
  }, v)));
  $("interval").value = state.interval;
  $("forecast-days").value = state.forecastDays;
}

// ── Locations ─────────────────────────────────────────────────────────────
function renderLocations(freshId) {
  const body = $("locations-body");
  body.replaceChildren(...state.locations.map((l) => {
    const bind = (key, extra = {}) => h("input", {
      type: "text", value: l[key] ?? "", spellcheck: "false", autocomplete: "off",
      "aria-label": `${key} of ${l.name || "location"}`, ...extra,
      oninput: (e) => { l[key] = e.target.value; checkCoord(e.target, key); updateDirty(); },
    });
    const coord = (key, max) => {
      const el = bind(key, { type: "number", step: "any", min: -max, max, class: "coord" });
      checkCoord(el, key);
      return el;
    };
    return h("tr", { class: l._id === freshId ? "fresh" : null },
      h("td", {}, bind("name", { class: "mono", placeholder: "paris" })),
      h("td", {}, bind("city", { placeholder: "Paris" })),
      h("td", {}, bind("country", { class: "cc", maxlength: "2", placeholder: "FR" })),
      h("td", { class: "num" }, coord("latitude", 90)),
      h("td", { class: "num" }, coord("longitude", 180)),
      h("td", {}, bind("station_id", { class: "mono", placeholder: "000PU" })),
      h("td", { class: "num" }, h("button", {
        type: "button", class: "icon-btn", title: "Remove location", "aria-label": `Remove ${l.name}`,
        onclick: () => { state.locations = state.locations.filter((x) => x !== l); renderLocations(); updateDirty(); },
      }, "✕")),
    );
  }));
  $("locations-empty").hidden = state.locations.length > 0;
  $("locations-table").hidden = state.locations.length === 0;
}

function checkCoord(el, key) {
  if (key !== "latitude" && key !== "longitude") return;
  el.setAttribute("aria-invalid", String(!validCoord(el.value, key === "latitude" ? 90 : 180)));
}

function addLocation(loc) {
  const taken = new Set(state.locations.map((l) => l.name));
  const l = { _id: ++uid, name: "", city: "", country: "", latitude: "", longitude: "", station_id: "", ...loc };
  if (loc.name) l.name = uniqueName(loc.name, taken);
  state.locations.push(l);
  renderLocations(l._id);
  updateDirty();
  return l;
}

// Station search (Infoclimat station list via /api/v1/stations).
const sta = { results: [], active: -1, timer: 0, seq: 0 };

function setupStationSearch() {
  const q = $("geo-q");
  const list = $("geo-results");
  q.addEventListener("input", () => {
    clearTimeout(sta.timer);
    const term = q.value.trim();
    if (term.length < 2) return closeStation();
    sta.timer = setTimeout(() => searchStations(term), 250);
  });
  q.addEventListener("keydown", (e) => {
    if (list.hidden) return;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const n = sta.results.length;
      if (!n) return;
      sta.active = (sta.active + (e.key === "ArrowDown" ? 1 : -1) + n) % n;
      renderStations();
    } else if (e.key === "Enter" && sta.active >= 0) {
      e.preventDefault();
      pickStation(sta.results[sta.active]);
    } else if (e.key === "Escape") {
      closeStation();
    }
  });
  q.addEventListener("blur", () => setTimeout(closeStation, 150));
}

async function searchStations(term) {
  const seq = ++sta.seq;
  try {
    const data = await api("GET", `/api/v1/stations?q=${encodeURIComponent(term)}`);
    if (seq !== sta.seq) return;
    sta.results = Array.isArray(data) ? data : [];
    sta.active = sta.results.length ? 0 : -1;
    renderStations(true);
  } catch {
    if (seq !== sta.seq) return;
    sta.results = [];
    renderStations(true, "Station search unavailable — use “Add manually”.");
  }
}

function renderStations(open = false, note) {
  const list = $("geo-results");
  const items = sta.results.map((s, i) => h("li", {
    role: "option", id: `geo-${i}`, "aria-selected": String(i === sta.active),
    onmousedown: (e) => { e.preventDefault(); pickStation(s); },
  },
  h("span", {}, `${s.id} — ${s.name}${s.country ? ` (${s.country})` : ""}`),
  h("span", { class: "geo-meta" }, `${s.latitude.toFixed(2)}, ${s.longitude.toFixed(2)}${s.elevation != null ? ` · ${s.elevation} m` : ""}`)));
  if (!items.length) items.push(h("li", { class: "geo-none", role: "option", "aria-disabled": "true" }, note || "No matching station."));
  list.replaceChildren(...items);
  if (open) list.hidden = false;
  $("geo-q").setAttribute("aria-expanded", String(!list.hidden));
  if (sta.active >= 0) $("geo-q").setAttribute("aria-activedescendant", `geo-${sta.active}`);
  else $("geo-q").removeAttribute("aria-activedescendant");
}

function closeStation() {
  $("geo-results").hidden = true;
  $("geo-q").setAttribute("aria-expanded", "false");
  sta.active = -1;
}

function pickStation(s) {
  if (!s) return;
  addLocation({
    name: slug(s.name),
    city: s.name,
    country: (s.country || "").toUpperCase(),
    latitude: Math.round(s.latitude * 1e4) / 1e4,
    longitude: Math.round(s.longitude * 1e4) / 1e4,
    station_id: s.id,
  });
  $("geo-q").value = "";
  closeStation();
  toast(`Added ${s.name}. Save to start polling it.`);
}

// ── Providers ─────────────────────────────────────────────────────────────
function renderProviders() {
  $("provider-cards").replaceChildren(...state.providers.map(providerCard));
}

function providerCard(p) {
  const info = typeInfo(p.type) || { type: p.type, description: "Unknown provider type.", options: [], requires_api_key: false };
  const rerender = () => { renderProviders(); updateDirty(); };
  const id = (s) => `p${p._id}-${s}`;

  const typeSelect = h("select", {
    id: id("type"),
    onchange: (e) => { p.type = e.target.value; p.options = {}; p.newKey = ""; p.clearKey = false; rerender(); },
  }, state.types.map((t) => h("option", { value: t.type, selected: t.type === p.type }, t.type)),
  typeInfo(p.type) ? null : h("option", { value: p.type, selected: true }, `${p.type} (unknown)`));

  const top = h("div", { class: "card-top" },
    h("label", { class: "field" }, h("span", {}, "Instance name"),
      h("input", { type: "text", class: "mono", value: p.name, spellcheck: "false", autocomplete: "off",
        oninput: (e) => { p.name = e.target.value; updateDirty(); } })),
    h("label", { class: "field" }, h("span", {}, "Type"), typeSelect),
    h("label", { class: "switch" },
      h("input", { type: "checkbox", role: "switch", checked: p.enabled,
        onchange: (e) => { p.enabled = e.target.checked; rerender(); } }),
      p.enabled ? "Enabled" : "Disabled"),
    h("div", { class: "card-actions" },
      p.orig && p.savedEnabled ? h("button", {
        type: "button", class: "btn ghost small", title: "Poll this provider now (saved settings)",
        onclick: () => pollNow(p.orig),
      }, "Poll now") : null,
      h("button", {
        type: "button", class: "icon-btn", title: "Remove provider", "aria-label": `Remove provider ${p.name}`,
        onclick: () => {
          if (p.orig && !confirm(`Remove provider "${p.orig}"? Its stored API key is deleted; recorded forecasts are kept.`)) return;
          state.providers = state.providers.filter((x) => x !== p);
          rerender();
        },
      }, "✕")),
  );

  const body = h("div", { class: "card-body" });
  if (info.requires_api_key || (p.has_api_key && p.type === p.origType)) body.append(keyBlock(p, info, rerender, id));
  for (const o of info.options) {
    const isInt = o.kind === "int";
    body.append(h("label", { class: "field", title: o.description },
      h("span", {}, o.name.replace(/_/g, " ")),
      h("input", {
        type: isInt ? "number" : "text", step: isInt ? "1" : null, class: "mono",
        value: p.options[o.name] ?? "", placeholder: o.default ? `default ${o.default}` : "optional",
        spellcheck: "false", autocomplete: "off",
        oninput: (e) => {
          const v = e.target.value.trim();
          p.options[o.name] = isInt ? (v === "" ? "" : Number(v)) : v;
          updateDirty();
        },
      }),
      h("small", { class: "hint small" }, o.description)));
  }

  const warn = p.enabled && info.requires_api_key && !keyAfterSave(p)
    ? h("p", { class: "card-warn" }, "Enabled but no API key — add one or disable this provider.") : null;

  return h("article", { class: "card" + (p.enabled ? " on" : ""), "aria-label": `Provider ${p.name}` },
    top,
    h("p", { class: "card-desc" }, info.description),
    body.childElementCount ? body : null,
    warn);
}

function keyBlock(p, info, rerender, id) {
  const env = keyFromEnv(p);
  if (env) {
    return h("div", { class: "field key" },
      h("span", {}, "API key"),
      h("div", { class: "key-row" },
        h("span", { class: "key-state set" }, "key from environment"),
        h("span", { class: "hint small key-env" }, "Provided by the ", h("code", {}, env),
          " environment variable, which overrides any key entered here. Unset it to manage the key from this page.")));
  }
  let pill;
  if (p.newKey.trim()) pill = h("span", { class: "key-state pending" }, "new key — unsaved");
  else if (p.clearKey) pill = h("span", { class: "key-state pending" }, "will be removed");
  else if (keyAfterSave(p)) pill = h("span", { class: "key-state set" }, `key set ${p.api_key_hint}`.trim());
  else pill = h("span", { class: "key-state unset" }, "no key");

  const input = h("input", {
    id: id("key"), type: "password", value: p.newKey, autocomplete: "off", spellcheck: "false",
    placeholder: keyAfterSave(p) ? "Paste a new key to replace it" : "Paste API key",
    oninput: (e) => {
      const had = Boolean(p.newKey.trim());
      p.newKey = e.target.value;
      if (had !== Boolean(p.newKey.trim())) { // refresh pill/warning, keep focus
        const pos = e.target.selectionStart;
        rerender();
        const again = $(id("key"));
        again.focus();
        again.setSelectionRange(pos, pos);
      } else updateDirty();
    },
  });
  const reveal = h("button", {
    type: "button", class: "btn ghost small", "aria-controls": id("key"),
    onclick: (e) => {
      const show = input.type === "password";
      input.type = show ? "text" : "password";
      e.target.textContent = show ? "Hide" : "Show";
    },
  }, "Show");
  const canRemove = p.has_api_key && p.type === p.origType;
  const remove = canRemove ? h("button", {
    type: "button", class: "btn danger-link small",
    onclick: () => { p.clearKey = !p.clearKey; p.newKey = ""; rerender(); },
  }, p.clearKey ? "Keep key" : "Remove key") : null;

  return h("div", { class: "field key" },
    h("span", {}, "API key"),
    h("div", { class: "key-row" }, pill, input, reveal, remove,
      info.signup_url ? h("a", { class: "key-link", href: info.signup_url, target: "_blank", rel: "noopener noreferrer" }, "Get a key ↗") : null));
}

function addProvider() {
  const taken = new Set(state.providers.map((p) => p.name));
  const type = state.types[0]?.type || "openmeteo";
  state.providers.push({
    _id: ++uid, orig: "", origType: "", savedEnabled: false, name: uniqueName(type, taken), type,
    enabled: true, options: {}, has_api_key: false, api_key_hint: "", newKey: "", clearKey: false,
  });
  renderProviders();
  updateDirty();
  $("provider-cards").lastElementChild?.querySelector("input")?.focus();
}

// ── Activity ──────────────────────────────────────────────────────────────
async function refreshPolls() {
  try {
    const polls = await api("GET", "/api/v1/polls?limit=25");
    $("polls-body").replaceChildren(...polls.map((p) => h("tr", {},
      h("td", {}, h("span", { class: "when", title: new Date(p.started_at).toLocaleString() }, relTime(p.started_at))),
      h("td", { class: "mono" }, p.provider),
      h("td", { class: "mono" }, p.location_name),
      h("td", {},
        h("span", { class: `status ${p.status}` }, p.status),
        p.error ? h("span", { class: "poll-error", title: p.error }, p.error) : null),
      h("td", {}, (p.kinds && p.kinds.length) ? p.kinds.join("+") : "—"),
      h("td", { class: "num" }, p.reading_count),
      h("td", { class: "num" }, `${(p.duration_ms / 1000).toFixed(1)} s`))));
    $("polls-empty").hidden = polls.length > 0;
  } catch (e) {
    console.warn("refresh polls:", e);
  }
}

async function pollNow(provider) {
  try {
    const q = provider ? `?provider=${encodeURIComponent(provider)}` : "";
    const res = await api("POST", `/api/v1/polls${q}`);
    toast(res.providers.length ? `Polling ${res.providers.join(", ")}…` : "No enabled provider to poll.");
    setTimeout(refreshPolls, 2500);
    setTimeout(refreshPolls, 8000);
  } catch (e) {
    toast(e.message, true);
  }
}

// ── Save / discard ────────────────────────────────────────────────────────
async function save() {
  if (state.saving) return;
  state.saving = true;
  $("save").textContent = "Saving…";
  updateDirty();
  try {
    const doc = await api("PUT", "/api/v1/settings", serialize());
    loadDoc(doc);
    renderAll();
    toast("Settings saved. The poller was updated.");
    state.saving = false;
    updateDirty();
  } catch (e) {
    state.saving = false;
    updateDirty(`Not saved: ${e.message}`);
  } finally {
    $("save").textContent = "Save settings";
  }
}

function discard() {
  loadDoc(state.lastDoc);
  renderAll();
  updateDirty();
}

function renderAll() {
  renderInterval();
  renderLocations();
  renderProviders();
  populateFcLocation();
  if (fc.ready) loadForecast();
}

// ── Forecast comparison ───────────────────────────────────────────────────
const fc = {
  location: "",
  param: "temperature_c",
  view: "table",
  data: null,
  observations: [],
  obsRecords: [],
  target: null,
  request: 0,
  controller: null,
  ready: false,
};

function tempClass(c) {
  if (c == null) return "";
  if (c < 0) return "cold";
  if (c < 10) return "cool";
  if (c < 20) return "mild";
  if (c < 28) return "warm";
  return "hot";
}

function populateFcLocation() {
  const sel = $("fc-location");
  if (!sel) return;
  const locs = state.locations;
  if (!locs.length) {
    sel.replaceChildren(h("option", { value: "" }, "No locations configured"));
    fc.location = "";
    return;
  }
  const prev = locs.some((l) => l.name === fc.location) ? fc.location : "";
  fc.location = prev;
  sel.replaceChildren(
    h("option", { value: "" }, "Select location…"),
    ...locs.map((l) => h("option", { value: l.name, selected: l.name === prev }, l.name)),
  );
  if (!fc.location && locs.length === 1) {
    fc.location = locs[0].name;
    sel.value = fc.location;
  }
}

async function loadForecast() {
  const request = ++fc.request;
  fc.controller?.abort();
  const controller = new AbortController();
  fc.controller = controller;
  fc.data = null;
  fc.observations = [];
  fc.obsRecords = [];
  const target = ForecastEvolution.today();
  fc.target = target;
  $("fc-target-date").textContent = `Today: ${new Date().toISOString().slice(0, 16).replace("T", " ")} UTC`;
  const wrap = $("fc-wrap");
  if (!fc.location) {
    fc.controller = null;
    wrap.replaceChildren(h("p", { class: "empty" }, "Select a location above to view forecast data."));
    return;
  }

  wrap.replaceChildren(h("p", { class: "fc-loading" }, "Loading…"));

  try {
    const getPage = (query) => api("GET", `/api/v1/readings?${query}`, undefined, controller.signal);
    const [records, obsRecords] = await Promise.all([
      ForecastEvolution.loadRecords(getPage, fc.location, target),
      ForecastEvolution.loadObservations(getPage, fc.location, target).catch(() => []),
    ]);
    if (request !== fc.request) return;
    if (target.getTime() !== ForecastEvolution.today().getTime()) {
      loadForecast();
      return;
    }
    fc.data = ForecastEvolution.build(records, target, state.providers.map((p) => p.name), state.forecastDays * 24);
    fc.obsRecords = obsRecords;
    fc.observations = ForecastEvolution.buildObservation(obsRecords, target);
    renderForecastView();
  } catch (e) {
    if (request !== fc.request || e.name === "AbortError") return;
    wrap.replaceChildren(h("p", { class: "empty", role: "alert" }, `Could not load forecasts: ${e.message}`));
  } finally {
    if (request === fc.request) fc.controller = null;
  }
}

function closestObsTime() {
  const now = Date.now();
  let best = null, bestDiff = Infinity;
  for (const r of fc.obsRecords) {
    const t = new Date(r.forecast_time).getTime();
    const diff = Math.abs(t - now);
    if (diff < bestDiff) { bestDiff = diff; best = r.forecast_time; }
  }
  return best ? best.slice(0, 19) + "Z" : new Date().toISOString().slice(0, 19) + "Z";
}

function fmtLead(lead) {
  return String(Number(lead.toFixed(6)));
}

function coverageText(value) {
  if (value.coverage == null) return value.source;
  return `${value.partial ? "Partial coverage: " : ""}${value.coverage.toFixed(1)}/24 h; ` +
    `${value.slots}/${value.totalSlots} available slots; ${value.source}`;
}

function pointDescription(point, value, param) {
  return `${point.provider}; poll #${point.pollID} at ${point.polledAt}; ` +
    `lead ${fmtLead(point.lead)} h; ${param.label}: ${fmtParamValue(value.value, param)}; ${coverageText(value)}`;
}

function renderForecastView() {
  if (!fc.data) return;
  if (fc.target.getTime() !== ForecastEvolution.today().getTime()) {
    loadForecast();
    return;
  }
  const wrap = $("fc-wrap");
  if (!fc.data.series.length) {
    wrap.replaceChildren(h("p", { class: "empty" },
      "No forecasts for today from polls made at or before midnight UTC. History must be collected before the target date."));
    return;
  }
  const param = PARAMS.find((p) => p.key === fc.param) || PARAMS[0];
  if (!fc.data.series.some((s) => s.points.some((p) => p.values[param.key]))) {
    wrap.replaceChildren(h("p", { class: "empty" }, `No "${param.label}" data for today's eligible polls.`));
    return;
  }
  if (fc.view === "chart") renderForecastChart(wrap, fc.data, param);
  else renderForecastTable(wrap, fc.data, param);
}

function renderForecastTable(wrap, data, param) {
  const ftLabel = h("p", { class: "fc-time-heading" }, "Forecast time: " + closestObsTime());
  const obsWithValue = fc.observations.filter((obs) => obs.values[param.key]);
  const thead = h("thead", {},
    h("tr", {},
      h("th", { class: "fc-time-col", scope: "col" }, "Lead time (h)"),
      ...data.series.map((s) => h("th", { scope: "col" }, s.provider)),
      ...obsWithValue.map((obs) => h("th", { scope: "col", class: "fc-obs-col" }, `${obs.provider} (obs.)`)),
    ),
  );
  const cells = data.series.map((s) => {
    const byLead = new Map();
    for (const point of s.points) {
      const roundedLead = Math.round(point.lead);
      if (!byLead.has(roundedLead)) byLead.set(roundedLead, []);
      byLead.get(roundedLead).push(point);
    }
    return byLead;
  });
  const roundedLeads = [...new Set(
    data.series.flatMap((s) => s.points.map((p) => Math.round(p.lead)))
  )].sort((a, b) => b - a);
  const rows = roundedLeads.map((lead) => h("tr", {},
    h("th", { class: "fc-time", scope: "row" }, String(lead)),
    ...cells.map((byLead) => {
      const points = byLead.get(lead) || [];
      if (!points.length) return h("td", { class: "fc-cell" }, h("span", { class: "fc-empty-cell" }, "—"));
      return h("td", { class: "fc-cell" }, ...points.map((point) => {
        const value = point.values[param.key];
        if (!value) return h("div", { class: "fc-reading", title: `Poll #${point.pollID} at ${point.polledAt}` }, "—");
        const color = param.unit === "°C" ? tempClass(value.value) : "mild";
        return h("div", { class: "fc-reading", title: pointDescription(point, value, param) },
          h("span", { class: `fc-temp ${color}` }, fmtParamValue(value.value, param)),
          h("span", { class: value.partial ? "fc-coverage partial" : "fc-coverage" }, coverageText(value)),
        );
      }));
    }),
    ...obsWithValue.map((obs) => {
      const value = obs.values[param.key];
      if (!value) return h("td", { class: "fc-cell fc-obs-cell" }, h("span", { class: "fc-empty-cell" }, "—"));
      const color = param.unit === "°C" ? tempClass(value.value) : "mild";
      return h("td", { class: "fc-cell fc-obs-cell" },
        h("div", { class: "fc-reading", title: `${obs.provider} observation; ${coverageText(value)}` },
          h("span", { class: `fc-temp ${color}` }, fmtParamValue(value.value, param)),
          h("span", { class: value.partial ? "fc-coverage partial" : "fc-coverage" }, coverageText(value)),
        ),
      );
    }),
  ));
  wrap.replaceChildren(ftLabel, h("table", { class: "fc-table" },
    h("caption", { class: "sr" }, `${param.label} forecast evolution for ${fc.target.toISOString().slice(0, 10)} UTC`),
    thead, h("tbody", {}, ...rows),
  ));
}

function renderForecastChart(wrap, data, param) {
  const series = data.series.map((s, i) => ({
    ...s, color: CHART_COLORS[i % CHART_COLORS.length],
  })).filter((s) => s.points.some((p) => p.values[param.key]));

  let yMin = Infinity, yMax = -Infinity;
  for (const s of series) for (const p of s.points) {
    const value = p.values[param.key];
    if (value) {
      yMin = Math.min(yMin, value.value);
      yMax = Math.max(yMax, value.value);
    }
  }
  for (const obs of fc.observations) {
    const value = obs.values[param.key];
    if (value) {
      yMin = Math.min(yMin, value.value);
      yMax = Math.max(yMax, value.value);
    }
  }
  if (yMin === yMax) { yMin -= 1; yMax += 1; }
  const yPad = (yMax - yMin) * 0.1;
  yMin -= yPad; yMax += yPad;

  const xMax = data.maxLead, xMin = 0;
  const W = 800, H = 280, ML = 62, MR = 26, MT = 16, MB = 54;
  const cW = W - ML - MR, cH = H - MT - MB;
  const xS = (lead) => ML + (xMax === xMin ? 0.5 : (xMax - lead) / (xMax - xMin)) * cW;
  const yS = (v) => MT + (1 - (v - yMin) / (yMax - yMin)) * cH;

  const svg = svgEl("svg", {
    viewBox: `0 0 ${W} ${H}`, class: "fc-chart-svg", role: "img",
    "aria-label": `${param.label} forecast evolution for ${fc.target.toISOString().slice(0, 10)} UTC; lead time decreases from left to right`,
  });

  for (const tick of niceYTicks(yMin, yMax, 5)) {
    const y = yS(tick).toFixed(1);
    const g = svgEl("line", { x1: ML, y1: y, x2: ML + cW, y2: y, class: "fc-chart-grid" });
    svg.append(g);
    const lbl = svgEl("text", { x: ML - 6, y, class: "fc-chart-tick", "text-anchor": "end", "dominant-baseline": "middle" });
    lbl.textContent = fmtParamValue(tick, param);
    svg.append(lbl);
  }

  const xTicks = xMax === xMin ? [xMin] : niceYTicks(xMin, xMax, 6);
  for (const tick of xTicks) {
    const label = svgEl("text", { x: xS(tick), y: H - MB + 18, class: "fc-chart-tick", "text-anchor": "middle" });
    label.textContent = fmtLead(tick);
    svg.append(label);
  }
  const axisLabel = svgEl("text", { x: ML + cW / 2, y: H - 5, class: "fc-chart-tick", "text-anchor": "middle" });
  axisLabel.textContent = "Lead time (h) — longest to shortest";
  svg.append(axisLabel, svgEl("line", {
    x1: ML, y1: MT + cH, x2: ML + cW, y2: MT + cH, class: "fc-chart-axis",
  }));
  for (const s of series) {
    let d = "", gap = true;
    for (const point of s.points) {
      const value = point.values[param.key];
      if (!value) { gap = true; continue; }
      const x = xS(point.lead).toFixed(2), y = yS(value.value).toFixed(2);
      d += gap ? `M${x} ${y}` : ` L${x} ${y}`;
      gap = false;
    }
    svg.append(svgEl("path", { d, class: "fc-chart-line", stroke: s.color }));
  }
  const obsWithColor = fc.observations
    .map((obs, i) => ({ ...obs, color: CHART_COLORS[(series.length + i) % CHART_COLORS.length] }))
    .filter((obs) => obs.values[param.key]);
  for (const obs of obsWithColor) {
    const value = obs.values[param.key];
    const y = yS(value.value).toFixed(2);
    const label = `${obs.provider} observation: ${fmtParamValue(value.value, param)}${value.partial ? " (partial day)" : ""}`;
    const lineEl = svgEl("line", {
      x1: ML, y1: y, x2: ML + cW, y2: y,
      class: "fc-chart-obs-line", stroke: obs.color,
      role: "img", "aria-label": label,
    });
    const obsTitle = svgEl("title");
    obsTitle.textContent = label;
    lineEl.append(obsTitle);
    svg.append(lineEl);
  }
  const detail = h("p", { class: "hint small fc-chart-detail" }, "Focus or hover a point for coverage and poll details.");
  // Draw markers after all paths so even a single-reading series remains visible.
  for (const s of series) for (const point of s.points) {
    const value = point.values[param.key];
    if (!value) continue;
    const description = pointDescription(point, value, param);
    const marker = svgEl("circle", {
      cx: xS(point.lead).toFixed(2), cy: yS(value.value).toFixed(2),
      r: value.partial ? 4.5 : 3.5, fill: s.color,
      class: value.partial ? "fc-chart-point partial" : "fc-chart-point",
      tabindex: "0", role: "img", "aria-label": description,
    });
    const title = svgEl("title");
    title.textContent = description;
    marker.append(title);
    marker.addEventListener("focus", () => { detail.textContent = description; });
    marker.addEventListener("mouseenter", () => { detail.textContent = description; });
    svg.append(marker);
  }
  const legend = h("div", { class: "fc-chart-legend" },
    ...series.map((s) => {
      const swatch = svgEl("svg", { viewBox: "0 0 12 12", class: "fc-chart-swatch", "aria-hidden": "true" });
      swatch.append(svgEl("rect", { width: 12, height: 12, rx: 2, fill: s.color }));
      return h("span", { class: "fc-chart-legend-item" }, swatch, s.provider);
    }),
    ...obsWithColor.map((obs) => {
      const swatch = svgEl("svg", { viewBox: "0 0 24 12", class: "fc-chart-swatch", "aria-hidden": "true" });
      swatch.append(svgEl("line", { x1: 2, y1: 6, x2: 22, y2: 6, stroke: obs.color, "stroke-width": 2.5, "stroke-dasharray": "5 3" }));
      return h("span", { class: "fc-chart-legend-item" }, swatch, `${obs.provider} (obs.)`);
    }),
  );

  const partial = series.some((s) => s.points.some((p) => p.values[param.key]?.partial));
  const ftLabel = h("p", { class: "fc-time-heading" }, "Forecast time: " + closestObsTime());
  wrap.replaceChildren(ftLabel, svg, legend, detail, ...(partial ? [
    h("p", { class: "fc-coverage partial" },
      "Partial coverage: outlined points use fewer than 24 hours of available values. Focus or hover a point for coverage and poll details."),
  ] : []));
}

function setupForecast() {
  populateFcLocation();
  if (state.locations.length && !fc.location) {
    fc.location = state.locations[0].name;
    $("fc-location").value = fc.location;
  }
  const paramSel = $("fc-param");
  paramSel.replaceChildren(...PARAMS.map((p) =>
    h("option", { value: p.key, selected: p.key === fc.param },
      p.unit ? `${p.label} (${p.unit})` : p.label),
  ));

  $("fc-location").addEventListener("change", (e) => {
    fc.location = e.target.value;
    loadForecast();
  });

  paramSel.addEventListener("change", (e) => {
    fc.param = e.target.value;
    renderForecastView();
  });

  const setView = (v) => {
    fc.view = v;
    $("fc-view-table").setAttribute("aria-pressed", String(v === "table"));
    $("fc-view-chart").setAttribute("aria-pressed", String(v === "chart"));
    renderForecastView();
  };
  $("fc-view-table").addEventListener("click", () => setView("table"));
  $("fc-view-chart").addEventListener("click", () => setView("chart"));

  fc.ready = true;
  loadForecast();
  setInterval(() => {
    if (currentTab === "forecast" &&
        (!fc.controller || fc.target.getTime() !== ForecastEvolution.today().getTime())) loadForecast();
  }, 60000);
}

// ── Boot ──────────────────────────────────────────────────────────────────
async function boot() {
  try {
    const [types, doc, ver] = await Promise.all([api("GET", "/api/v1/provider-types"), api("GET", "/api/v1/settings"), api("GET", "/api/v1/version")]);
    state.types = types;
    loadDoc(doc);
    state.version = ver?.version ?? "dev";
  } catch (e) {
    $("loading").textContent = `Could not load settings: ${e.message}`;
    return;
  }
  $("loading").remove();
  const vEl = document.getElementById("app-version");
  if (vEl && state.version) vEl.textContent = " · " + state.version;
  $("main").setAttribute("aria-busy", "false");
  for (const name of Object.keys(TABS)) {
    $(`tab-${name}`).addEventListener("click", () => setTab(name));
  }
  setTab(currentTab);

  renderAll();
  updateDirty();
  setupForecast();
  setupStationSearch();

  $("interval").addEventListener("input", (e) => { state.interval = e.target.value; updateDirty(); });
  $("forecast-days").addEventListener("input", (e) => {
    const v = parseInt(e.target.value, 10);
    state.forecastDays = Number.isNaN(v) ? e.target.value : v;
    updateDirty();
  });
  $("add-location").addEventListener("click", () => {
    addLocation({});
    $("locations-body").lastElementChild?.querySelector("input")?.focus();
  });
  $("add-provider").addEventListener("click", addProvider);
  $("poll-all").addEventListener("click", () => pollNow(""));
  $("save").addEventListener("click", save);
  $("discard").addEventListener("click", discard);
  document.addEventListener("keydown", (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === "s") { e.preventDefault(); if (!$("save").disabled) save(); }
  });
  window.addEventListener("beforeunload", (e) => {
    if (JSON.stringify(serialize()) !== state.baseline) e.preventDefault();
  });

  refreshPolls();
  setInterval(refreshPolls, 15000);
}

boot();
