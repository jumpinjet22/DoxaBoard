"use strict";

const TOKEN_KEY = "doxaboard.adminToken";
let token = "";
try { token = localStorage.getItem(TOKEN_KEY) || ""; } catch (_) {}

let profiles = [];
let albums = [];
let connectors = [];
let contentSources = [];
let editingSource = null;
let editing = null;

const $ = (sel) => document.querySelector(sel);

// Build DOM without innerHTML so device-provided strings can't inject markup.
function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") el.className = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (v === true) el.setAttribute(k, "");
    else if (v !== false && v != null) el.setAttribute(k, v);
  }
  for (const c of children.flat()) {
    if (c == null) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: { Authorization: "Bearer " + token, ...(body ? { "Content-Type": "application/json" } : {}) },
    body: body ? JSON.stringify(body) : undefined,
  });
  if (res.status === 401) { showLogin(); throw new Error("Signed out"); }
  if (res.status === 204) return null;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

// ---- navigation ----

function showTab(name) {
  for (const s of ["login", "live", "devices", "profiles", "content"]) $("#" + s).hidden = s !== name;
  document.querySelectorAll(".tab").forEach((b) => b.classList.toggle("active", b.dataset.tab === name));
  if (name === "devices") loadDevices();
  if (name === "profiles") loadProfiles();
  if (name === "content") loadContent();
  if (name === "live") loadLive();
}

function showLogin() {
  token = "";
  try { localStorage.removeItem(TOKEN_KEY); } catch (_) {}
  showTab("login");
}

document.querySelectorAll(".tab").forEach((b) => b.addEventListener("click", () => showTab(b.dataset.tab)));
$("#logout").addEventListener("click", showLogin);

$("#login-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  token = $("#token").value.trim();
  try {
    await api("GET", "/api/v1/admin/profiles");
    try { localStorage.setItem(TOKEN_KEY, token); } catch (_) {}
    $("#login-error").textContent = "";
    showTab("devices");
  } catch (err) {
    $("#login-error").textContent = "That token didn't work.";
  }
});

// ---- devices ----

function ago(iso) {
  if (!iso) return "never";
  const s = Math.round((Date.now() - new Date(iso)) / 1000);
  if (s < 60) return s + "s ago";
  if (s < 3600) return Math.round(s / 60) + "m ago";
  if (s < 86400) return Math.round(s / 3600) + "h ago";
  return Math.round(s / 86400) + "d ago";
}

function showing(d) {
  const m = d.heartbeat && d.heartbeat.mode;
  if (!m) return "—";
  if (m.kind === "live") return "Live: " + (m.source && (m.source.name || m.source.id));
  if (m.kind === "slideshow") return "Photos";
  return "Nothing";
}

async function patchDevice(id, patch) {
  try { await api("PATCH", "/api/v1/admin/devices/" + encodeURIComponent(id), patch); }
  catch (err) { alert(err.message); }
  loadDevices();
}

async function loadDevices() {
  const [devices] = await Promise.all([
    api("GET", "/api/v1/admin/devices"),
    api("GET", "/api/v1/admin/profiles").then((p) => (profiles = p)),
  ]);
  const online = devices.filter((d) => d.online).length;
  $("#device-summary").textContent = `${online} of ${devices.length} online`;

  const rows = devices.map((d) => {
    const err = d.heartbeat && d.heartbeat.error;
    const dot = !d.online ? "bad" : err ? "warn" : "ok";
    const profileSel = h("select", {
      onchange: (e) => patchDevice(d.id, { profile_id: e.target.value }),
      "aria-label": "Profile",
    },
      h("option", { value: "" }, "— none —"),
      profiles.map((p) => h("option", { value: p.id, selected: p.id === d.profile_id }, p.name)));
    const outputSel = h("select", {
      onchange: (e) => patchDevice(d.id, { display: { ...d.display, output: e.target.value } }),
      "aria-label": "Output",
    },
      ["hdmi", "composite"].map((o) => h("option", { value: o, selected: (d.display.output || "hdmi") === o }, o)));
    const nameInput = h("input", {
      value: d.name, "aria-label": "Name",
      onchange: (e) => patchDevice(d.id, { name: e.target.value }),
    });
    return h("tr", {},
      h("td", { title: err || "" }, h("span", { class: "dot " + dot }), d.online ? "Online" : "Offline"),
      h("td", {}, nameInput, h("div", { class: "muted" }, `${d.hostname} · ${d.platform || "?"}`)),
      h("td", {}, showing(d)),
      h("td", {}, profileSel),
      h("td", {}, outputSel),
      h("td", {}, ago(d.last_seen)),
      h("td", {}, h("button", {
        class: "danger",
        onclick: async () => {
          if (!confirm(`Remove ${d.name}? It will need to enroll again.`)) return;
          await api("DELETE", "/api/v1/admin/devices/" + encodeURIComponent(d.id));
          loadDevices();
        },
      }, "Remove")));
  });
  $("#device-rows").replaceChildren(...(rows.length ? rows : [h("tr", {}, h("td", { colspan: 7, class: "muted" }, "No screens yet."))]));
}

setInterval(() => { if (!$("#devices").hidden && document.visibilityState === "visible") loadDevices().catch(() => {}); }, 10000);

// ---- profiles ----

async function loadProfiles() {
  [profiles, albums] = await Promise.all([api("GET", "/api/v1/admin/profiles"), api("GET", "/api/v1/admin/albums")]);
  const cards = profiles.map((p) => h("div", { class: "card" },
    h("div", {},
      h("strong", {}, p.name),
      h("div", { class: "meta" },
        `${(p.sources || []).length} live source(s)` +
        ` · ${(p.source_ids || []).length} content source(s)` +
        (p.album ? ` · photos: ${p.album}` : ""))),
    h("button", { class: "secondary", onclick: () => openProfile(p) }, "Edit")));
  $("#profile-list").replaceChildren(...(cards.length ? cards : [h("p", { class: "muted" }, "No profiles yet. Create one, then assign it to screens.")]));
}

const KINDS = [
  ["multicast", "Multicast", "udp://239.10.10.1:5000"],
  ["srt", "SRT", "srt://relay.local:8890?streamid=read:live"],
  ["rtsp", "RTSP", "rtsp://relay.local:8554/live"],
  ["hls", "HLS / Vimeo", "https://…/playlist.m3u8"],
];

function sourceRow(src = {}) {
  const kind = h("select", { name: "kind", "aria-label": "Kind" },
    KINDS.map(([v, label]) => h("option", { value: v, selected: v === src.kind }, label)));
  const url = h("input", { name: "url", value: src.url || "", required: true, "aria-label": "URL" });
  const setPlaceholder = () => { url.placeholder = (KINDS.find((k) => k[0] === kind.value) || [])[2] || ""; };
  kind.addEventListener("change", setPlaceholder);
  setPlaceholder();
  const row = h("div", { class: "source-row", "data-id": src.id || "", "data-name": src.name || "" },
    kind, url,
    h("input", { name: "priority", type: "number", value: src.priority ?? 10, "aria-label": "Priority" }),
    h("button", { type: "button", class: "secondary", title: "Remove", onclick: () => row.remove() }, "×"));
  return row;
}

async function openProfile(p) {
  // Always refresh albums: the list may still be loading, or OneDrive may
  // have synced a new folder since the page loaded.
  try { albums = await api("GET", "/api/v1/admin/albums"); } catch (_) {}
  try { contentSources = await api("GET", "/api/v1/admin/sources"); } catch (_) { contentSources = []; }
  editing = p || null;
  const chosen = new Set((p && p.source_ids) || []);
  $("#profile-content-sources").replaceChildren(
    contentSources.length
      ? h("div", { class: "check-list" },
          h("div", { class: "muted" }, "Content sources"),
          ...contentSources.map((src) => h("label", {},
            h("input", { type: "checkbox", value: src.id, checked: chosen.has(src.id) }),
            `${src.name} (${src.connector_title || src.connector})`)))
      : h("p", { class: "muted" }, "No content sources yet. Add one in the Content tab."));
  const f = $("#profile-form");
  $("#profile-title").textContent = p ? "Edit profile" : "New profile";
  f.name.value = p ? p.name : "";
  f.interval.value = p ? p.interval_seconds || 10 : 10;
  f.shuffle.checked = p ? !!p.shuffle : false;
  f.album.replaceChildren(
    h("option", { value: "" }, "— none —"),
    ...albums.map((a) => h("option", { value: a, selected: !!p && p.album === a }, a)));
  const srcs = p ? p.sources || [] : [
    { kind: "multicast", url: "udp://239.10.10.1:5000", priority: 10 },
    // The relay normally runs on the same host as this console.
    { kind: "srt", url: `srt://${location.hostname}:8890?streamid=read:live`, priority: 20 },
  ];
  $("#source-rows").replaceChildren(...srcs.map(sourceRow));
  $("#delete-profile").hidden = !p;
  $("#profile-error").textContent = "";
  $("#profile-dialog").showModal();
}

$("#new-profile").addEventListener("click", () => openProfile(null));
$("#add-source").addEventListener("click", () => $("#source-rows").append(sourceRow({ priority: 30 })));
$("#cancel-profile").addEventListener("click", () => $("#profile-dialog").close());

$("#delete-profile").addEventListener("click", async () => {
  if (!editing || !confirm(`Delete profile ${editing.name}? Screens using it will show nothing until reassigned.`)) return;
  await api("DELETE", "/api/v1/admin/profiles/" + encodeURIComponent(editing.id));
  $("#profile-dialog").close();
  loadProfiles();
});

$("#profile-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const f = e.target;
  const body = {
    name: f.name.value,
    album: f.album.value,
    interval_seconds: Number(f.interval.value) || 10,
    shuffle: f.shuffle.checked,
    source_ids: [...document.querySelectorAll("#profile-content-sources input:checked")].map((c) => c.value),
    sources: [...document.querySelectorAll("#source-rows .source-row")].map((r, i) => ({
      id: r.dataset.id || "s" + (i + 1),
      name: r.dataset.name || "",
      kind: r.querySelector("[name=kind]").value,
      url: r.querySelector("[name=url]").value.trim(),
      priority: Number(r.querySelector("[name=priority]").value) || 0,
    })),
  };
  try {
    if (editing) await api("PUT", "/api/v1/admin/profiles/" + encodeURIComponent(editing.id), body);
    else await api("POST", "/api/v1/admin/profiles", body);
    $("#profile-dialog").close();
    loadProfiles();
  } catch (err) {
    $("#profile-error").textContent = err.message;
  }
});

showTab(token ? "devices" : "login");

// ---- content sources ----

function statusLine(st) {
  if (!st || !st.fetched_at || st.fetched_at.startsWith("0001")) return h("div", { class: "status-line" }, "Not fetched yet");
  const when = ago(st.fetched_at);
  if (st.ok) {
    return h("div", { class: "status-line" },
      h("span", { class: "dot ok" }), `${st.slides} slide(s) · updated ${when}` + (st.dropped ? ` · ${st.dropped} skipped` : ""));
  }
  return h("div", { class: "status-line bad" },
    h("span", { class: "dot bad" }), `Last fetch failed ${when}: ${st.error}` + (st.slides ? ` (still showing ${st.slides} older slide(s))` : ""));
}

// Thumbnails need the admin token, which <img src> can't send, so fetch
// them with it and show blobs.
async function authedImage(url) {
  const res = await fetch(url, { headers: { Authorization: "Bearer " + token } });
  if (!res.ok) throw new Error(res.statusText);
  return URL.createObjectURL(await res.blob());
}

async function togglePreview(card, src) {
  const existing = card.querySelector(".thumbs");
  if (existing) { existing.remove(); return; }
  const grid = h("div", { class: "thumbs" }, h("span", { class: "muted" }, "Loading…"));
  card.append(grid);
  try {
    const slides = await api("GET", "/api/v1/admin/sources/" + encodeURIComponent(src.id) + "/slides");
    if (!slides.length) { grid.replaceChildren(h("span", { class: "muted" }, "No slides yet.")); return; }
    grid.replaceChildren(...slides.map((sl) => {
      const img = h("img", { alt: sl.title || sl.id, loading: "lazy" });
      authedImage(sl.url).then((u) => { img.src = u; }).catch(() => {});
      return h("figure", { class: sl.active ? "" : "inactive", title: sl.active ? "" : "Not showing now (scheduled or expired)" },
        img, h("figcaption", {}, (sl.active ? "" : "⏸ ") + (sl.title || sl.id)));
    }));
  } catch (err) {
    grid.replaceChildren(h("span", { class: "error" }, err.message));
  }
}

async function loadContent() {
  [connectors, contentSources] = await Promise.all([
    api("GET", "/api/v1/admin/connectors"),
    api("GET", "/api/v1/admin/sources"),
  ]);
  const cards = contentSources.map((src) => {
    const card = h("div", { class: "card stack" });
    card.append(h("div", { class: "card-head" },
      h("div", {},
        h("strong", {}, src.name),
        h("div", { class: "meta" }, src.connector_title || src.connector),
        statusLine(src.status)),
      h("div", { class: "actions-inline" },
        h("button", { class: "secondary", onclick: () => togglePreview(card, src) }, "Preview"),
        h("button", {
          class: "secondary",
          onclick: async (e) => {
            e.target.disabled = true;
            e.target.textContent = "Refreshing…";
            try { await api("POST", "/api/v1/admin/sources/" + encodeURIComponent(src.id) + "/refresh"); }
            catch (err) { alert(err.message); }
            loadContent();
          },
        }, "Refresh now"),
        h("button", { class: "secondary", onclick: () => openSource(src) }, "Edit"))));
    return card;
  });
  $("#content-list").replaceChildren(...(cards.length ? cards
    : [h("p", { class: "muted" }, "No content sources yet. Add an RSS feed, a JSON feed or a photo folder.")]));
}

function fieldInput(field, src) {
  const val = src ? (src.config[field.key] ?? "") : (field.default ?? "");
  const isSet = src && src.secrets_set.includes(field.key);
  let input;
  switch (field.type) {
    case "secret":
      input = h("input", { name: field.key, type: "password", autocomplete: "new-password",
        placeholder: isSet ? "Saved. Leave blank to keep it" : "", required: field.required && !isSet });
      break;
    case "bool":
      input = h("input", { name: field.key, type: "checkbox", checked: val === "true" });
      return h("label", { class: "inline" }, input, field.label,
        field.help ? h("span", { class: "field-help" }, " " + field.help) : null);
    case "number":
      input = h("input", { name: field.key, type: "number", value: val, required: field.required });
      break;
    case "url":
      input = h("input", { name: field.key, type: "url", value: val, required: field.required, placeholder: "https://" });
      break;
    default:
      input = h("input", { name: field.key, value: val, required: field.required });
  }
  return h("label", {}, field.label + (field.required ? " *" : ""), input,
    field.help ? h("div", { class: "field-help" }, field.help) : null);
}

function renderSourceFields(src) {
  const conn = connectors.find((c) => c.name === $("#content-connector").value);
  $("#content-connector-help").textContent = conn ? conn.description : "";
  $("#content-fields").replaceChildren(...(conn ? conn.fields.map((f) => fieldInput(f, src)) : []));
}

async function openSource(src) {
  if (!connectors.length) connectors = await api("GET", "/api/v1/admin/connectors");
  editingSource = src || null;
  const f = $("#content-form");
  $("#content-title").textContent = src ? "Edit content source" : "Add content source";
  f.name.value = src ? src.name : "";
  const sel = $("#content-connector");
  sel.replaceChildren(...connectors.map((c) => h("option", { value: c.name, selected: src && src.connector === c.name },
    c.title + (c.status === "official" ? "" : ` (${c.status})`))));
  sel.disabled = !!src; // a source's type can't change
  renderSourceFields(src);
  $("#delete-content").hidden = !src;
  $("#content-error").textContent = "";
  $("#content-dialog").showModal();
}

$("#content-connector").addEventListener("change", () => renderSourceFields(null));
$("#new-content").addEventListener("click", () => openSource(null));
$("#cancel-content").addEventListener("click", () => $("#content-dialog").close());

$("#delete-content").addEventListener("click", async () => {
  if (!editingSource || !confirm(`Delete ${editingSource.name}? Its slides disappear from every screen.`)) return;
  await api("DELETE", "/api/v1/admin/sources/" + encodeURIComponent(editingSource.id));
  $("#content-dialog").close();
  loadContent();
});

$("#content-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const config = {};
  for (const el of document.querySelectorAll("#content-fields input")) {
    config[el.name] = el.type === "checkbox" ? String(el.checked) : el.value.trim();
  }
  const body = { name: e.target.name.value, connector: $("#content-connector").value, config };
  try {
    if (editingSource) await api("PUT", "/api/v1/admin/sources/" + encodeURIComponent(editingSource.id), body);
    else await api("POST", "/api/v1/admin/sources", body);
    $("#content-dialog").close();
    loadContent();
    setTimeout(() => { if (!$("#content").hidden) loadContent(); }, 4000); // first fetch runs in the background
  } catch (err) {
    $("#content-error").textContent = err.message;
  }
});

// ---- live broadcast ----

let broadcasts = [];
let schedule = { slots: [], specials: [] };
let editingBroadcast = null;
const DAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

function fmtTime(iso) {
  return new Date(iso).toLocaleString([], { weekday: "short", month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
}

async function liveAction(path, confirmText) {
  if (confirmText && !confirm(confirmText)) return;
  $("#live-error").textContent = "";
  try { await api("POST", path); }
  catch (err) { $("#live-error").textContent = err.message; }
  renderLiveStatus();
}

async function renderLiveStatus() {
  let st;
  try { st = await api("GET", "/api/v1/live/status"); }
  catch (err) { $("#live-status").replaceChildren(h("p", { class: "error" }, err.message)); return; }
  $("#live-tz").textContent = "Schedule time zone: " + st.time_zone;

  const head = h("div", { class: "live-head" },
    h("span", { class: "live-badge" + (st.live ? " on" : "") }, st.live ? "● LIVE" : "OFF AIR"),
    st.live ? h("strong", {}, st.broadcast.name) : null,
    st.live ? h("span", { class: "muted" }, "since " + fmtTime(st.started_at)) : null);
  const obs = h("div", { class: "status-line" + (st.obs.ready ? "" : " bad") },
    h("span", { class: "dot " + (st.obs.ready ? "ok" : "bad") }),
    st.relay_error ? "Relay: " + st.relay_error : st.obs.ready ? "OBS is sending to the relay" : "OBS is not sending");
  const outs = st.outputs.map((o) => h("div", { class: "out-line" },
    h("span", { class: "dot " + (o.state === "forwarding" ? "ok" : o.state === "error" ? "bad" : "warn") }),
    `${o.label} (${o.host}): ${o.state || "starting"}` + (o.last_error ? ` · ${o.last_error}` : "")));

  const actions = h("div", { class: "live-actions" });
  if (st.live) {
    actions.append(h("button", { class: "danger", onclick: () => liveAction("/api/v1/live/stop", `End ${st.broadcast.name}? Screens in the building keep playing.`) }, "End broadcast"));
  } else {
    if (st.scheduled) {
      const label = st.scheduled.special && st.scheduled.label ? `${st.scheduled.label} (${st.scheduled.name})` : st.scheduled.name;
      actions.append(h("button", { class: "go", onclick: () => liveAction("/api/v1/live/start?broadcast=auto", `Go live to ${label}?`) }, `Go live → ${label}`));
    }
    for (const b of broadcasts) {
      actions.append(h("button", { class: "secondary", onclick: () => liveAction("/api/v1/live/start?broadcast=" + encodeURIComponent(b.id), `Go live to ${b.name}?`) }, `Go live: ${b.name}`));
    }
    if (!broadcasts.length) actions.append(h("span", { class: "muted" }, "Add a broadcast below to go live."));
  }
  const upcoming = st.upcoming.length
    ? h("div", { class: "status-line" }, "Next: " + st.upcoming.slice(0, 3).map((u) =>
        `${u.special && u.label ? u.label + " · " : ""}${u.name} ${fmtTime(u.start)}`).join(" · "))
    : null;
  $("#live-status").replaceChildren(head, obs, ...outs, actions, upcoming || "");
}

async function loadLive() {
  [broadcasts, schedule] = await Promise.all([api("GET", "/api/v1/admin/broadcasts"), api("GET", "/api/v1/admin/live/schedule")]);
  renderLiveStatus();
  $("#broadcast-list").replaceChildren(...(broadcasts.length ? broadcasts.map((b) => h("div", { class: "card" },
    h("div", {}, h("strong", {}, b.name),
      h("div", { class: "meta" }, b.outputs.length ? b.outputs.map((o) => `${o.label}${o.key_set ? "" : " (no key)"}`).join(" · ") : "No outputs yet"),
      h("div", { class: "meta" }, "ID for buttons: ", h("code", {}, b.id))),
    h("button", { class: "secondary", onclick: () => openBroadcast(b) }, "Edit")))
    : [h("p", { class: "muted" }, "No broadcasts yet.")]));
  $("#slot-rows").replaceChildren(...schedule.slots.map(slotRow));
  $("#special-rows").replaceChildren(...schedule.specials.map(specialRow));
  $("#schedule-msg").textContent = "";
}

function broadcastSelect(selected) {
  return h("select", { name: "broadcast", "aria-label": "Broadcast" },
    ...broadcasts.map((b) => h("option", { value: b.id, selected: b.id === selected }, b.name)));
}

function slotRow(sl = {}) {
  const row = h("div", { class: "sched-row", "data-id": sl.id || "" },
    h("select", { name: "weekday", "aria-label": "Day" }, ...DAYS.map((d, i) => h("option", { value: i, selected: (sl.weekday ?? 0) === i }, d))),
    h("input", { name: "start", type: "time", value: sl.start || "", required: true, "aria-label": "Start" }),
    h("input", { name: "end", type: "time", value: sl.end || "", required: true, "aria-label": "End" }),
    broadcastSelect(sl.broadcast_id),
    h("button", { type: "button", class: "secondary", title: "Remove", onclick: () => row.remove() }, "×"));
  return row;
}

function specialRow(sp = {}) {
  const row = h("div", { class: "sched-row special", "data-id": sp.id || "" },
    h("input", { name: "date", type: "date", value: sp.date || "", required: true, "aria-label": "Date" }),
    h("input", { name: "name", value: sp.name || "", placeholder: "Combined service", "aria-label": "Name" }),
    h("input", { name: "start", type: "time", value: sp.start || "", required: true, "aria-label": "Start" }),
    h("input", { name: "end", type: "time", value: sp.end || "", required: true, "aria-label": "End" }),
    broadcastSelect(sp.broadcast_id),
    h("button", { type: "button", class: "secondary", title: "Remove", onclick: () => row.remove() }, "×"));
  return row;
}

$("#add-slot").addEventListener("click", () => {
  if (!broadcasts.length) { alert("Add a broadcast first."); return; }
  $("#slot-rows").append(slotRow({ weekday: 0 }));
});
$("#add-special").addEventListener("click", () => {
  if (!broadcasts.length) { alert("Add a broadcast first."); return; }
  $("#special-rows").append(specialRow());
});

$("#save-schedule").addEventListener("click", async () => {
  const val = (r, n) => r.querySelector(`[name=${n}]`).value;
  const body = {
    slots: [...document.querySelectorAll("#slot-rows .sched-row")].map((r) => ({
      id: r.dataset.id, weekday: Number(val(r, "weekday")), start: val(r, "start"), end: val(r, "end"), broadcast_id: val(r, "broadcast"),
    })),
    specials: [...document.querySelectorAll("#special-rows .sched-row")].map((r) => ({
      id: r.dataset.id, date: val(r, "date"), name: val(r, "name"), start: val(r, "start"), end: val(r, "end"), broadcast_id: val(r, "broadcast"),
    })),
  };
  try {
    schedule = await api("PUT", "/api/v1/admin/live/schedule", body);
    $("#schedule-msg").textContent = "Schedule saved.";
    renderLiveStatus();
  } catch (err) {
    $("#schedule-msg").textContent = err.message;
  }
});

function outputRow(o = {}) {
  const row = h("div", { class: "source-row", "data-id": o.id || "" },
    h("input", { name: "label", value: o.label || "", placeholder: "Vimeo", "aria-label": "Label" }),
    h("input", { name: "url", value: o.url || "", required: true, placeholder: "rtmps://…", "aria-label": "Stream URL" }),
    h("input", { name: "key", type: "password", autocomplete: "new-password", "aria-label": "Stream key",
      placeholder: o.key_set ? "Saved" : "Stream key" }),
    h("button", { type: "button", class: "secondary", title: "Remove", onclick: () => row.remove() }, "×"));
  return row;
}

function openBroadcast(b) {
  editingBroadcast = b || null;
  $("#broadcast-title").textContent = b ? "Edit broadcast" : "Add broadcast";
  $("#broadcast-form").name.value = b ? b.name : "";
  $("#output-rows").replaceChildren(...(b && b.outputs.length ? b.outputs : [{}]).map(outputRow));
  $("#delete-broadcast").hidden = !b;
  $("#broadcast-error").textContent = "";
  $("#broadcast-dialog").showModal();
}

$("#new-broadcast").addEventListener("click", () => openBroadcast(null));
$("#add-output").addEventListener("click", () => $("#output-rows").append(outputRow()));
$("#cancel-broadcast").addEventListener("click", () => $("#broadcast-dialog").close());

$("#delete-broadcast").addEventListener("click", async () => {
  if (!editingBroadcast || !confirm(`Delete ${editingBroadcast.name}? It's also removed from the schedule.`)) return;
  try {
    await api("DELETE", "/api/v1/admin/broadcasts/" + encodeURIComponent(editingBroadcast.id));
    $("#broadcast-dialog").close();
    loadLive();
  } catch (err) {
    $("#broadcast-error").textContent = err.message;
  }
});

$("#broadcast-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const body = {
    name: e.target.name.value,
    outputs: [...document.querySelectorAll("#output-rows .source-row")].map((r) => ({
      id: r.dataset.id,
      label: r.querySelector("[name=label]").value.trim(),
      url: r.querySelector("[name=url]").value.trim(),
      key: r.querySelector("[name=key]").value.trim(),
    })),
  };
  try {
    if (editingBroadcast) await api("PUT", "/api/v1/admin/broadcasts/" + encodeURIComponent(editingBroadcast.id), body);
    else await api("POST", "/api/v1/admin/broadcasts", body);
    $("#broadcast-dialog").close();
    loadLive();
  } catch (err) {
    $("#broadcast-error").textContent = err.message;
  }
});

setInterval(() => { if (!$("#live").hidden && document.visibilityState === "visible") renderLiveStatus(); }, 5000);
