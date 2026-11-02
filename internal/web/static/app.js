// meteocomp settings UI. Vanilla JS, no build step. All dynamic text goes
// through textContent (via h()) so provider error messages cannot inject markup.
"use strict";

const PRESETS = ["15m", "30m", "1h", "3h", "6h", "12h"];
const GEO_URL = "https://geocoding-api.open-meteo.com/v1/search";

const $ = (id) => document.getElementById(id);

const state = {
  types: [],      // provider type descriptions from /api/v1/provider-types
  interval: "1h",
  locations: [],
  providers: [],
  lastDoc: null,  // last settings document received from the server
  baseline: "",   // serialized settings matching lastDoc, for dirty tracking
  saving: false,
};
let uid = 0;

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

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: body ? { "Content-Type": "application/json" } : {},
    body: body ? JSON.stringify(body) : undefined,
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
    locations: state.locations.map((l) => ({
      name: l.name.trim(),
      city: l.city.trim(),
      country: l.country.trim().toUpperCase(),
      latitude: Number(l.latitude),
      longitude: Number(l.longitude),
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
  for (const b of $("interval-presets").children) b.setAttribute("aria-pressed", String(b.dataset.v === state.interval.trim()));
}

// ── Polling interval ──────────────────────────────────────────────────────
function renderInterval() {
  const presets = $("interval-presets");
  presets.replaceChildren(...PRESETS.map((v) => h("button", {
    type: "button", "data-v": v,
    onclick: () => { state.interval = v; $("interval").value = v; updateDirty(); },
  }, v)));
  $("interval").value = state.interval;
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
  const l = { _id: ++uid, name: "", city: "", country: "", latitude: "", longitude: "", ...loc };
  if (loc.name) l.name = uniqueName(loc.name, taken);
  state.locations.push(l);
  renderLocations(l._id);
  updateDirty();
  return l;
}

// Place search (Open-Meteo geocoding, called from the browser).
const geo = { results: [], active: -1, timer: 0, seq: 0 };

function setupGeoSearch() {
  const q = $("geo-q");
  const list = $("geo-results");
  q.addEventListener("input", () => {
    clearTimeout(geo.timer);
    const term = q.value.trim();
    if (term.length < 2) return closeGeo();
    geo.timer = setTimeout(() => searchGeo(term), 250);
  });
  q.addEventListener("keydown", (e) => {
    if (list.hidden) return;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const n = geo.results.length;
      if (!n) return;
      geo.active = (geo.active + (e.key === "ArrowDown" ? 1 : -1) + n) % n;
      renderGeo();
    } else if (e.key === "Enter" && geo.active >= 0) {
      e.preventDefault();
      pickGeo(geo.results[geo.active]);
    } else if (e.key === "Escape") {
      closeGeo();
    }
  });
  q.addEventListener("blur", () => setTimeout(closeGeo, 150));
}

async function searchGeo(term) {
  const seq = ++geo.seq;
  try {
    const url = `${GEO_URL}?${new URLSearchParams({ name: term, count: "8", language: "en", format: "json" })}`;
    const res = await fetch(url);
    const data = await res.json();
    if (seq !== geo.seq) return; // a newer search is in flight
    geo.results = data.results || [];
    geo.active = geo.results.length ? 0 : -1;
    renderGeo(true);
  } catch {
    if (seq !== geo.seq) return;
    geo.results = [];
    renderGeo(true, "Place search unavailable — use “Add manually”.");
  }
}

function renderGeo(open = false, note) {
  const list = $("geo-results");
  const items = geo.results.map((r, i) => h("li", {
    role: "option", id: `geo-${i}`, "aria-selected": String(i === geo.active),
    onmousedown: (e) => { e.preventDefault(); pickGeo(r); },
  },
  h("span", {}, [r.name, r.admin1, r.country].filter(Boolean).join(", ")),
  h("span", { class: "geo-meta" }, `${r.latitude.toFixed(2)}, ${r.longitude.toFixed(2)}`)));
  if (!items.length) items.push(h("li", { class: "geo-none", role: "option", "aria-disabled": "true" }, note || "No matching place."));
  list.replaceChildren(...items);
  if (open) list.hidden = false;
  $("geo-q").setAttribute("aria-expanded", String(!list.hidden));
  if (geo.active >= 0) $("geo-q").setAttribute("aria-activedescendant", `geo-${geo.active}`);
  else $("geo-q").removeAttribute("aria-activedescendant");
}

function closeGeo() {
  $("geo-results").hidden = true;
  $("geo-q").setAttribute("aria-expanded", "false");
  geo.active = -1;
}

function pickGeo(r) {
  if (!r) return;
  addLocation({
    name: slug(r.name),
    city: r.name,
    country: (r.country_code || "").toUpperCase(),
    latitude: Math.round(r.latitude * 1e4) / 1e4,
    longitude: Math.round(r.longitude * 1e4) / 1e4,
  });
  $("geo-q").value = "";
  closeGeo();
  toast(`Added ${r.name}. Save to start polling it.`);
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
          if (p.orig && !confirm(`Remove provider “${p.orig}”? Its stored API key is deleted; recorded forecasts are kept.`)) return;
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
      h("td", { class: "num" }, p.forecast_count),
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
}

// ── Forecast comparison ───────────────────────────────────────────────────
const fc = {
  location: "",
  granularity: "hourly",
  windowStart: null, // Date; null means "now"
};

const CONDITION_LABEL = {
  clear: "clear", partly_cloudy: "partly cloudy", cloudy: "cloudy", fog: "fog",
  drizzle: "drizzle", rain: "rain", freezing_rain: "freezing rain", sleet: "sleet",
  snow: "snow", thunderstorm: "storm", unknown: "",
};

function tempClass(c) {
  if (c == null) return "";
  if (c < 0) return "cold";
  if (c < 10) return "cool";
  if (c < 20) return "mild";
  if (c < 28) return "warm";
  return "hot";
}

function fcWindowStart() {
  if (fc.windowStart) return fc.windowStart;
  const now = new Date();
  if (fc.granularity === "daily") {
    return new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate()));
  }
  return new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate(), now.getUTCHours()));
}

function fcWindowEnd(start) {
  const ms = fc.granularity === "daily" ? 7 * 86400e3 : 48 * 3600e3;
  return new Date(start.getTime() + ms);
}

function shiftWindow(dir) {
  const start = fcWindowStart();
  const shift = fc.granularity === "daily" ? 7 * 86400e3 : 24 * 3600e3;
  fc.windowStart = new Date(start.getTime() + dir * shift);
  loadForecast();
}

function renderFcWindowLabel(start, end) {
  const fmt = (d) => d.toLocaleDateString(undefined, { month: "short", day: "numeric", timeZone: "UTC" });
  if (fc.granularity === "daily") {
    $("fc-window-label").textContent = `${fmt(start)} – ${fmt(new Date(end.getTime() - 86400e3))}`;
  } else {
    const fmtH = (d) => d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false, timeZone: "UTC" });
    $("fc-window-label").textContent = `${fmtH(start)} – ${fmtH(end)} UTC`;
  }
}

function populateFcLocation() {
  const sel = $("fc-location");
  if (!sel) return;
  const locs = state.locations;
  if (!locs.length) {
    sel.replaceChildren(h("option", { value: "" }, "No locations configured"));
    return;
  }
  const prev = fc.location || sel.value;
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
  if (!fc.location) {
    const empty = $("fc-empty");
    empty.textContent = "Select a location above to view forecast data.";
    $("fc-wrap").replaceChildren(empty);
    return;
  }

  const start = fcWindowStart();
  const end = fcWindowEnd(start);
  renderFcWindowLabel(start, end);

  const wrap = $("fc-wrap");
  wrap.replaceChildren(h("p", { class: "fc-loading" }, "Loading…"));

  let records;
  try {
    const params = new URLSearchParams({
      location: fc.location,
      granularity: fc.granularity,
      from: start.toISOString(),
      to: end.toISOString(),
      limit: "2000",
    });
    records = await api("GET", `/api/v1/forecasts?${params}`);
  } catch (e) {
    wrap.replaceChildren(h("p", { class: "empty" }, `Could not load forecasts: ${e.message}`));
    return;
  }

  renderForecastTable(records, wrap);
}

function renderForecastTable(records, wrap) {
  const byTime = new Map();
  const providerSet = new Set();

  for (const r of records) {
    const t = r.forecast_time;
    providerSet.add(r.provider);
    if (!byTime.has(t)) byTime.set(t, new Map());
    const byProv = byTime.get(t);
    const existing = byProv.get(r.provider);
    if (!existing || r.polled_at > existing.polled_at) byProv.set(r.provider, r);
  }

  const times = [...byTime.keys()].sort();
  const settingsOrder = state.providers.map((p) => p.name);
  const provList = [
    ...settingsOrder.filter((n) => providerSet.has(n)),
    ...[...providerSet].filter((n) => !settingsOrder.includes(n)).sort(),
  ];

  if (!times.length || !provList.length) {
    wrap.replaceChildren(h("p", { class: "empty" }, "No forecast data for this location and time window. Try polling first."));
    return;
  }

  const thead = h("thead", {},
    h("tr", {},
      h("th", { class: "fc-time-col" }, "Time (UTC)"),
      ...provList.map((p) => h("th", {}, p)),
    ),
  );

  const rows = [];
  let lastDay = null;

  for (const t of times) {
    const date = new Date(t);
    const dayKey = date.toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric", timeZone: "UTC" });
    if (fc.granularity === "hourly" && dayKey !== lastDay) {
      rows.push(h("tr", { class: "fc-day-head" },
        h("td", { colspan: String(provList.length + 1) }, dayKey),
      ));
      lastDay = dayKey;
    }

    const timeLabel = fc.granularity === "daily"
      ? date.toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric", timeZone: "UTC" })
      : date.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hour12: false, timeZone: "UTC" });

    const byProv = byTime.get(t);
    rows.push(h("tr", {},
      h("td", { class: "fc-time" }, timeLabel),
      ...provList.map((p) => {
        const r = byProv.get(p);
        if (!r) return h("td", { class: "fc-cell" }, h("span", { class: "fc-empty-cell" }, "—"));

        const temp = r.temperature_c ?? r.temperature_max_c ?? null;
        const tempEl = temp != null
          ? h("span", { class: `fc-temp ${tempClass(temp)}` }, `${temp > 0 ? "+" : ""}${Math.round(temp)}°`)
          : h("span", { class: "fc-temp mild" }, "—");

        const condText = CONDITION_LABEL[r.condition] || r.condition || "";
        const precipMm = r.precipitation_mm;
        const metaChildren = [];
        if (condText) metaChildren.push(h("span", { class: "fc-cond" }, condText));
        if (precipMm != null && precipMm > 0) metaChildren.push(h("span", { class: "fc-precip" }, `${precipMm.toFixed(1)} mm`));

        return h("td", { class: "fc-cell" },
          tempEl,
          metaChildren.length ? h("div", { class: "fc-meta" }, ...metaChildren) : null,
        );
      }),
    ));
  }

  wrap.replaceChildren(h("table", { class: "fc-table" }, thead, h("tbody", {}, ...rows)));
}

function setupForecast() {
  populateFcLocation();
  if (state.locations.length && !fc.location) {
    fc.location = state.locations[0].name;
    $("fc-location").value = fc.location;
  }
  renderFcWindowLabel(fcWindowStart(), fcWindowEnd(fcWindowStart()));

  $("fc-location").addEventListener("change", (e) => {
    fc.location = e.target.value;
    fc.windowStart = null;
    loadForecast();
  });

  const setGran = (g) => {
    fc.granularity = g;
    fc.windowStart = null;
    $("fc-gran-hourly").setAttribute("aria-pressed", String(g === "hourly"));
    $("fc-gran-daily").setAttribute("aria-pressed", String(g === "daily"));
    loadForecast();
  };
  $("fc-gran-hourly").addEventListener("click", () => setGran("hourly"));
  $("fc-gran-daily").addEventListener("click", () => setGran("daily"));
  $("fc-prev").addEventListener("click", () => shiftWindow(-1));
  $("fc-next").addEventListener("click", () => shiftWindow(1));
  $("fc-now").addEventListener("click", () => { fc.windowStart = null; loadForecast(); });

  if (fc.location) loadForecast();
}

// ── Boot ──────────────────────────────────────────────────────────────────
async function boot() {
  try {
    const [types, doc] = await Promise.all([api("GET", "/api/v1/provider-types"), api("GET", "/api/v1/settings")]);
    state.types = types;
    loadDoc(doc);
  } catch (e) {
    $("loading").textContent = `Could not load settings: ${e.message}`;
    return;
  }
  $("loading").remove();
  for (const s of document.querySelectorAll("main > section")) s.hidden = false;
  $("main").setAttribute("aria-busy", "false");

  renderAll();
  updateDirty();
  setupForecast();
  setupGeoSearch();

  $("interval").addEventListener("input", (e) => { state.interval = e.target.value; updateDirty(); });
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
