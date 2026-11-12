"use strict";

const TOKEN_KEY = "doxacast.adminToken";
let token = "";
try { token = localStorage.getItem(TOKEN_KEY) || ""; } catch (_) {}

let profiles = [];
let albums = [];
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
  for (const s of ["login", "devices", "profiles"]) $("#" + s).hidden = s !== name;
  document.querySelectorAll(".tab").forEach((b) => b.classList.toggle("active", b.dataset.tab === name));
  if (name === "devices") loadDevices();
  if (name === "profiles") loadProfiles();
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
        `${(p.sources || []).length} source(s)` + (p.album ? ` · photos: ${p.album}` : " · no photo fallback"))),
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
  editing = p || null;
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
    { kind: "srt", url: "", priority: 20 },
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
