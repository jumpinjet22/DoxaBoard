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
let scheduleDirty = false;
let scheduleZone = "";
let calAnchor = null; // UTC-midnight Date the schedule calendar is showing; null = today
let calView = "month";
try { calView = localStorage.getItem("doxaboard.calView") || "month"; } catch (_) {}

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

// ---- branding ----

// An instance theme can rename the console and add a logo; colors and
// fonts arrive through theme.css.
fetch("/api/v1/theme").then((r) => (r.ok ? r.json() : {})).then((t) => {
  // An instance's name and logo take the header; DoxaBoard's own mark
  // stays on the sign-in page, the footer and the browser tab.
  if (t.name) {
    $("#brand-name").textContent = t.name;
    $("#login-org").textContent = t.name;
    $("#login-org").hidden = false;
    document.title = t.name + " · DoxaBoard";
  }
  if (t.logo) {
    for (const id of ["#brand-logo", "#login-logo"]) { $(id).src = t.logo; $(id).hidden = false; }
    $("#brand-mark").style.display = "none";
    $("#login-mark").style.display = "none";
  }
}).catch(() => {});

// ---- appearance ----

// Auto follows the system's light/dark setting; Light and Dark force one.
const MODE_KEY = "doxaboard.mode";
function setMode(mode) {
  if (mode === "auto") delete document.documentElement.dataset.mode;
  else document.documentElement.dataset.mode = mode;
  try { mode === "auto" ? localStorage.removeItem(MODE_KEY) : localStorage.setItem(MODE_KEY, mode); } catch (_) {}
  const label = mode === "auto" ? "Appearance: follows your system setting. Click for light."
    : `Appearance: always ${mode}. Click for ${mode === "light" ? "dark" : "automatic"}.`;
  $("#mode").dataset.mode = mode; // picks the icon
  $("#mode").title = label;
  $("#mode").setAttribute("aria-label", label);
}
$("#mode").addEventListener("click", () => {
  const order = ["auto", "light", "dark"];
  setMode(order[(order.indexOf(document.documentElement.dataset.mode || "auto") + 1) % 3]);
});
setMode(document.documentElement.dataset.mode || "auto");

// ---- navigation ----

function showTab(name) {
  // Leaving the schedule with unsaved edits would silently drop them.
  if (scheduleDirty && !$("#schedule").hidden && name !== "schedule") {
    if (!confirm("Discard your unsaved schedule changes?")) return;
    setScheduleDirty(false);
  }
  for (const s of ["login", "live", "schedule", "devices", "profiles", "content"]) $("#" + s).hidden = s !== name;
  document.body.classList.toggle("signed-out", name === "login");
  document.querySelectorAll(".tab").forEach((b) => b.classList.toggle("active", b.dataset.tab === name));
  if (name === "devices") loadDevices();
  if (name === "profiles") loadProfiles();
  if (name === "content") loadContent();
  if (name === "live") loadLive();
  if (name === "schedule") loadSchedule();
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

// ---- cloud folders ----

let cloud = { available: false, providers: [], drives: [], folders: [] };
let cloudPick = { drive: null, path: [] };
let cloudTimer = null;

function cloudFolderStatus(f) {
  if (f.syncing) return h("div", { class: "status-line" }, h("span", { class: "dot warn" }), "Syncing now…");
  if (f.last_error) return h("div", { class: "status-line bad" }, h("span", { class: "dot bad" }), f.last_error);
  if (!f.last_sync || f.last_sync.startsWith("0001")) return h("div", { class: "status-line" }, "Waiting for the first sync");
  return h("div", { class: "status-line" }, h("span", { class: "dot ok" }), "In step · checked " + ago(f.last_sync));
}

async function loadCloud() {
  clearTimeout(cloudTimer);
  try { cloud = await api("GET", "/api/v1/admin/cloud"); }
  catch (err) { cloud = { available: false, problem: err.message, providers: [], drives: [], folders: [] }; }
  $("#cloud-problem").hidden = !cloud.problem;
  $("#cloud-problem").textContent = cloud.problem || "";
  $("#new-cloud").disabled = !cloud.available;
  const title = (name) => { const d = cloud.drives.find((x) => x.name === name); return d ? d.title : name; };
  $("#cloud-list").replaceChildren(...(cloud.folders.length ? cloud.folders.map((f) => h("div", { class: "card" },
    h("div", {},
      h("strong", {}, "Album: " + f.album),
      h("div", { class: "meta" }, `${title(f.drive)} · ${f.path ? "/" + f.path : "top folder"}`),
      cloudFolderStatus(f)),
    h("div", { class: "actions-inline" },
      h("button", { class: "secondary", disabled: f.syncing || !cloud.available, onclick: async () => {
        await api("POST", "/api/v1/admin/cloud/folders/" + encodeURIComponent(f.id) + "/sync").catch((e) => alert(e.message));
        setTimeout(loadCloud, 600);
      } }, "Sync now"),
      h("button", { class: "danger", onclick: async () => {
        if (!confirm(`Stop syncing the album "${f.album}"? The photos already in the album stay.`)) return;
        await api("DELETE", "/api/v1/admin/cloud/folders/" + encodeURIComponent(f.id)).catch((e) => alert(e.message));
        loadCloud();
      } }, "Remove"))))
    : [h("p", { class: "muted" }, cloud.available ? "No cloud folders yet." : "")]));
  $("#cloud-drives").replaceChildren(...(cloud.drives.length ? [
    "Signed-in accounts: ",
    ...cloud.drives.flatMap((d, i) => [i ? " · " : "", `${d.title} (${d.name}) `,
      h("button", { class: "link", title: "Sign out of this account", onclick: async () => {
        if (!confirm(`Disconnect ${d.title} (${d.name})?`)) return;
        await api("DELETE", "/api/v1/admin/cloud/drives/" + encodeURIComponent(d.name)).catch((e) => alert(e.message));
        loadCloud();
      } }, "disconnect")]),
  ] : []));
  // Keep the status fresh while something is syncing.
  if (!$("#content").hidden && cloud.folders.some((f) => f.syncing)) cloudTimer = setTimeout(loadCloud, 3000);
}

function cloudStep(step) {
  for (const s of ["drive", "signin", "folder"]) $("#cloud-step-" + s).hidden = s !== step;
  $("#cloud-save").hidden = step !== "folder";
  $("#cloud-back").hidden = step === "drive";
  $("#cloud-error").textContent = "";
}

function cloudChooseDrive() {
  cloudStep("drive");
  $("#cloud-choices").replaceChildren(
    ...cloud.drives.map((d) => h("button", { type: "button", onclick: () => cloudBrowse(d.name, []) },
      d.title, h("small", {}, "Already signed in · " + d.name))),
    ...cloud.providers.map((p) => h("button", { type: "button", onclick: () => cloudSignIn(p.type) },
      (cloud.drives.some((d) => d.type === p.type) ? "Another " : "") + p.title, h("small", {}, "Sign in from this browser"))));
}

async function cloudSignIn(type) {
  $("#cloud-error").textContent = "Starting the sign-in…";
  try {
    const s = await api("POST", "/api/v1/admin/cloud/connect", { type });
    cloudStep("signin");
    $("#cloud-auth-link").href = s.auth_url;
    $("#cloud-paste").value = "";
  } catch (err) {
    $("#cloud-error").textContent = err.message;
  }
}

async function cloudBrowse(drive, path) {
  cloudPick = { drive, path };
  cloudStep("folder");
  $("#cloud-crumbs").replaceChildren(
    h("button", { type: "button", onclick: () => cloudBrowse(drive, []) }, "Top"),
    ...path.flatMap((name, i) => [h("span", {}, "/"), h("button", { type: "button", onclick: () => cloudBrowse(drive, path.slice(0, i + 1)) }, name)]));
  if (path.length && !$("#cloud-album").dataset.typed) $("#cloud-album").value = path[path.length - 1].replace(/[^A-Za-z0-9 _-]/g, " ").trim().slice(0, 64);
  const list = $("#cloud-folders");
  list.replaceChildren(h("p", { class: "muted" }, "Loading…"));
  try {
    const res = await api("GET", `/api/v1/admin/cloud/drives/${encodeURIComponent(drive)}/folders?path=${encodeURIComponent(path.join("/"))}`);
    if (cloudPick.drive !== drive || cloudPick.path !== path) return; // a newer click won
    list.replaceChildren(...(res.folders.length
      ? res.folders.map((name) => h("button", { type: "button", onclick: () => cloudBrowse(drive, [...path, name]) }, "📁 " + name))
      : [h("p", { class: "muted" }, "No folders inside this one.")]));
  } catch (err) {
    list.replaceChildren(h("p", { class: "error" }, err.message));
  }
}

$("#new-cloud").addEventListener("click", () => {
  $("#cloud-album").value = "";
  delete $("#cloud-album").dataset.typed;
  cloudChooseDrive();
  $("#cloud-dialog").showModal();
});
$("#cloud-album").addEventListener("input", (e) => { e.target.dataset.typed = "1"; });
$("#cloud-back").addEventListener("click", async () => {
  if (!$("#cloud-step-signin").hidden) await api("DELETE", "/api/v1/admin/cloud/connect").catch(() => {});
  cloudChooseDrive();
});
async function closeCloudDialog() {
  if (!$("#cloud-step-signin").hidden) await api("DELETE", "/api/v1/admin/cloud/connect").catch(() => {});
  $("#cloud-dialog").close();
  loadCloud();
}
$("#cancel-cloud").addEventListener("click", closeCloudDialog);
$("#cloud-dialog").addEventListener("cancel", (e) => { e.preventDefault(); closeCloudDialog(); });
$("#cloud-form").addEventListener("submit", (e) => e.preventDefault());

$("#cloud-finish").addEventListener("click", async () => {
  const btn = $("#cloud-finish");
  btn.disabled = true;
  $("#cloud-error").textContent = "Finishing the sign-in…";
  try {
    const drive = await api("POST", "/api/v1/admin/cloud/connect/finish", { url: $("#cloud-paste").value });
    cloud.drives.push(drive);
    await cloudBrowse(drive.name, []);
  } catch (err) {
    $("#cloud-error").textContent = err.message;
  }
  btn.disabled = false;
});

$("#cloud-save").addEventListener("click", async () => {
  try {
    await api("POST", "/api/v1/admin/cloud/folders", { drive: cloudPick.drive, path: cloudPick.path.join("/"), album: $("#cloud-album").value });
    $("#cloud-dialog").close();
    loadCloud();
    setTimeout(loadCloud, 2500);
  } catch (err) {
    $("#cloud-error").textContent = err.message;
  }
});

async function loadContent() {
  loadCloud();
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
  broadcasts = await api("GET", "/api/v1/admin/broadcasts");
  renderLiveStatus();
  $("#broadcast-list").replaceChildren(...(broadcasts.length ? broadcasts.map((b) => h("div", { class: "card" },
    h("div", {}, h("strong", {}, b.name),
      h("div", { class: "meta" }, b.outputs.length ? b.outputs.map((o) => `${o.label}${o.key_set ? "" : " (no key)"}`).join(" · ") : "No outputs yet"),
      h("div", { class: "meta" }, "ID for buttons: ", h("code", {}, b.id))),
    h("button", { class: "secondary", onclick: () => openBroadcast(b) }, "Edit")))
    : [h("p", { class: "muted" }, "No broadcasts yet.")]));
}

// ---- schedule ----

const hmToMin = (s) => { const m = /^(\d\d):(\d\d)$/.exec(s || ""); return m ? Number(m[1]) * 60 + Number(m[2]) : null; };
const pad2 = (n) => String(n).padStart(2, "0");
const ymd = (d) => `${d.getUTCFullYear()}-${pad2(d.getUTCMonth() + 1)}-${pad2(d.getUTCDate())}`;

// "08:15" -> "8:15 AM" (or 24-hour, whatever the browser's locale uses).
function fmtHM(s) {
  const min = hmToMin(s);
  if (min == null) return s;
  return new Date(Date.UTC(2000, 0, 1, Math.floor(min / 60), min % 60))
    .toLocaleTimeString([], { hour: "numeric", minute: "2-digit", timeZone: "UTC" });
}

// The schedule is wall-clock time in the server's zone, so "today" and "now"
// come from that zone, not from wherever this browser happens to be.
function nowInZone(zone) {
  const opts = { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hourCycle: "h23" };
  let parts;
  try { parts = new Intl.DateTimeFormat("en-CA", { ...opts, timeZone: zone || undefined }).formatToParts(new Date()); }
  catch (_) { parts = new Intl.DateTimeFormat("en-CA", opts).formatToParts(new Date()); }
  const p = Object.fromEntries(parts.map((x) => [x.type, x.value]));
  return { date: `${p.year}-${p.month}-${p.day}`, minutes: Number(p.hour) * 60 + Number(p.minute) };
}

function setScheduleDirty(dirty, msg) {
  scheduleDirty = dirty;
  $("#save-schedule").disabled = !dirty;
  $("#discard-schedule").disabled = !dirty;
  $("#schedule-save").classList.toggle("dirty", dirty);
  $("#schedule-msg").textContent = msg != null ? msg : dirty ? "You have unsaved changes." : "";
}

function scheduleChanged() {
  setScheduleDirty(true);
  renderSchedulePreview();
}

function broadcastSelect(selected) {
  const known = broadcasts.some((b) => b.id === selected);
  return h("select", { name: "broadcast", "aria-label": "Broadcast" },
    selected && !known ? h("option", { value: selected, selected: true }, "(deleted broadcast)") : null,
    ...broadcasts.map((b) => h("option", { value: b.id, selected: b.id === selected }, b.name)));
}

function removeButton(row) {
  return h("button", { type: "button", class: "secondary", title: "Remove", "aria-label": "Remove", onclick: () => { row.remove(); scheduleChanged(); } }, "×");
}

function slotRow(sl = {}) {
  const row = h("div", { class: "sched-row", "data-id": sl.id || "" },
    h("select", { name: "weekday", "aria-label": "Day" }, ...DAYS.map((d, i) => h("option", { value: i, selected: (sl.weekday ?? 0) === i }, d))),
    h("input", { name: "start", type: "time", value: sl.start || "", required: true, "aria-label": "Starts" }),
    h("input", { name: "end", type: "time", value: sl.end || "", required: true, "aria-label": "Ends" }),
    broadcastSelect(sl.broadcast_id));
  row.append(removeButton(row));
  return row;
}

function specialRow(sp = {}) {
  const row = h("div", { class: "sched-row special", "data-id": sp.id || "" },
    h("input", { name: "date", type: "date", value: sp.date || "", required: true, "aria-label": "Date" }),
    h("input", { name: "name", value: sp.name || "", placeholder: "Combined service", "aria-label": "Name" }),
    h("input", { name: "start", type: "time", value: sp.start || "", required: true, "aria-label": "Starts" }),
    h("input", { name: "end", type: "time", value: sp.end || "", required: true, "aria-label": "Ends" }),
    broadcastSelect(sp.broadcast_id));
  row.append(removeButton(row));
  return row;
}

// Reads the editor as it stands, saved or not.
function readSchedule() {
  const val = (r, n) => r.querySelector(`[name=${n}]`).value;
  return {
    slots: [...document.querySelectorAll("#slot-rows .sched-row")].map((r) => ({
      row: r, id: r.dataset.id, weekday: Number(val(r, "weekday")), start: val(r, "start"), end: val(r, "end"), broadcast_id: val(r, "broadcast"),
    })),
    specials: [...document.querySelectorAll("#special-rows .sched-row")].map((r) => ({
      row: r, id: r.dataset.id, date: val(r, "date"), name: val(r, "name").trim(), start: val(r, "start"), end: val(r, "end"), broadcast_id: val(r, "broadcast"),
    })),
  };
}

const windowOK = (e) => hmToMin(e.start) != null && hmToMin(e.end) != null && hmToMin(e.end) > hmToMin(e.start);
const overlaps = (a, b) => hmToMin(a.start) < hmToMin(b.end) && hmToMin(b.start) < hmToMin(a.end);

// Mirrors internal/live/schedule.go: on a date with special entries the
// weekly schedule is ignored, and two windows covering the same moment make
// Go live refuse rather than guess.
function windowsOn(day, slots, specials) {
  const date = ymd(day);
  const todays = specials.filter((sp) => sp.date === date);
  const weekly = slots.filter((sl) => sl.weekday === day.getUTCDay());
  const wins = (todays.length ? todays : weekly).map((e) => ({ ...e, special: todays.length > 0 }));
  wins.sort((x, y) => hmToMin(x.start) - hmToMin(y.start));
  for (const w of wins) w.clash = wins.some((o) => o !== w && overlaps(w, o));
  return { date, wins, replaced: todays.length ? weekly.length : 0 };
}

function renderSchedulePreview() {
  const sched = readSchedule();
  const now = nowInZone(scheduleZone);
  const name = (id) => (broadcasts.find((b) => b.id === id) || {}).name || "(deleted broadcast)";
  const problems = [];

  for (const e of [...sched.slots, ...sched.specials]) {
    const incomplete = !e.start || !e.end || (e.date !== undefined && !e.date);
    const backwards = !incomplete && !windowOK(e);
    e.row.classList.toggle("invalid", backwards);
    e.row.classList.toggle("past", e.date !== undefined && !!e.date && e.date < now.date);
    const what = e.date !== undefined ? (e.name || "A special date") : `A ${DAYS[e.weekday]} time`;
    if (incomplete) problems.push(`${what} is missing its ${e.date !== undefined && !e.date ? "date" : "start or end time"}.`);
    else if (backwards) problems.push(`${what} ends before it starts. Windows can't cross midnight.`);
  }
  $("#clear-past").hidden = !sched.specials.some((sp) => sp.date && sp.date < now.date);
  $("#slot-empty").hidden = sched.slots.length > 0;
  $("#special-empty").hidden = sched.specials.length > 0;
  document.querySelectorAll("#schedule-editor .sched-head").forEach((el) => {
    el.hidden = !el.nextElementSibling.children.length;
  });

  const slots = sched.slots.filter(windowOK);
  const specials = sched.specials.filter((sp) => sp.date && windowOK(sp));
  const [ty, tm, td] = now.date.split("-").map(Number);

  // Overlaps anywhere in the next 8 weeks, not just the month on screen.
  const clashDays = [];
  for (let i = 0; i < 56; i++) {
    const day = new Date(Date.UTC(ty, tm - 1, td + i));
    if (windowsOn(day, slots, specials).wins.some((w) => w.clash)) clashDays.push(day);
  }
  if (clashDays.length) {
    const first = clashDays[0].toLocaleDateString([], { weekday: "long", month: "short", day: "numeric", timeZone: "UTC" });
    problems.push(`Two windows overlap on ${first}${clashDays.length > 1 ? ` and ${clashDays.length - 1} more day${clashDays.length > 2 ? "s" : ""}` : ""}. While they overlap, Go live refuses to guess and you'll have to pick the broadcast by hand.`);
  }
  $("#schedule-problems").replaceChildren(...(problems.length
    ? [h("div", { class: "notice" }, h("strong", {}, "Check these before you rely on the schedule"), h("ul", {}, ...problems.map((p) => h("li", {}, p))))]
    : []));

  const today = new Date(Date.UTC(ty, tm - 1, td));
  const anchor = calAnchor || today;
  const ctx = { slots, specials, now, name };
  const longDate = { timeZone: "UTC", month: "long", day: "numeric", year: "numeric" };
  document.querySelectorAll("#cal-views button").forEach((b) => b.setAttribute("aria-pressed", String(b.dataset.view === calView)));
  const cal = $("#schedule-cal");
  cal.className = "calv-" + calView;
  if (calView === "month") {
    $("#cal-title").textContent = anchor.toLocaleDateString([], { month: "long", year: "numeric", timeZone: "UTC" });
    cal.replaceChildren(...monthCells(anchor, ctx));
  } else if (calView === "week") {
    const start = addDays(anchor, -anchor.getUTCDay());
    const days = [0, 1, 2, 3, 4, 5, 6].map((i) => addDays(start, i));
    $("#cal-title").textContent = start.toLocaleDateString([], { timeZone: "UTC", month: "short", day: "numeric" }) +
      " – " + days[6].toLocaleDateString([], { timeZone: "UTC", month: "short", day: "numeric", year: "numeric" });
    cal.replaceChildren(timeGrid(days, ctx));
  } else {
    $("#cal-title").textContent = anchor.toLocaleDateString([], { weekday: "long", ...longDate });
    cal.replaceChildren(timeGrid([anchor], ctx));
  }
  $("#cal-hint").textContent = calView === "month"
    ? "Shows what the Go live button will pick, including changes you haven't saved yet. Click a day to add a special date, or an entry to edit it."
    : "Shows what the Go live button will pick, including changes you haven't saved yet. Click an empty time to add a special date there, or an entry to edit it.";
}

const addDays = (d, n) => new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate() + n));
const minToHM = (min) => `${pad2(Math.floor(min / 60))}:${pad2(min % 60)}`;

function winTitle(w, replaced, name) {
  return `${fmtHM(w.start)} – ${fmtHM(w.end)} · ${w.special && w.name ? w.name + " → " : ""}${name(w.broadcast_id)}` +
    (w.clash ? " · overlaps another window" : "") +
    (w.special && replaced ? ` · replaces ${replaced} weekly time${replaced === 1 ? "" : "s"}` : "");
}

// Jump from a calendar entry to the row that defines it.
function focusRow(row) {
  row.scrollIntoView({ block: "center", behavior: "smooth" });
  row.classList.add("flash");
  setTimeout(() => row.classList.remove("flash"), 1500);
  row.querySelector("[name=start]").focus({ preventScroll: true });
}

function showDay(day) {
  calAnchor = day;
  setCalView("day");
}

// Month grid, Sunday first, padded to whole weeks.
function monthCells(anchor, { slots, specials, now, name }) {
  const first = new Date(Date.UTC(anchor.getUTCFullYear(), anchor.getUTCMonth(), 1));
  const lead = first.getUTCDay();
  const daysInMonth = new Date(Date.UTC(anchor.getUTCFullYear(), anchor.getUTCMonth() + 1, 0)).getUTCDate();
  const out = DAYS.map((d) => h("div", { class: "cal-dow" }, d.slice(0, 3)));
  for (let i = 0; i < Math.ceil((lead + daysInMonth) / 7) * 7; i++) {
    const day = addDays(first, i - lead);
    const { date, wins, replaced } = windowsOn(day, slots, specials);
    const isToday = date === now.date;
    const past = date < now.date;
    const cls = ["cal-day"];
    if (day.getUTCMonth() !== first.getUTCMonth()) cls.push("other");
    if (past) cls.push("past");
    if (isToday) cls.push("today");
    const label = day.toLocaleDateString([], { weekday: "long", month: "long", day: "numeric", timeZone: "UTC" });
    out.push(h("div", { class: cls.join(" "), title: past ? "" : "Add a special date", onclick: () => { if (!past) addSpecial(date); } },
      h("button", { type: "button", class: "cal-num", title: "Open this day", "aria-label": `${label}: open day view`,
        onclick: (e) => { e.stopPropagation(); showDay(day); } }, day.getUTCDate()),
      ...wins.map((w) => {
        const live = isToday && hmToMin(w.start) <= now.minutes && now.minutes < hmToMin(w.end);
        return h("button", {
          type: "button",
          class: "cal-chip" + (w.special ? " special" : "") + (w.clash ? " clash" : "") + (live ? " now" : ""),
          title: winTitle(w, replaced, name),
          onclick: (e) => { e.stopPropagation(); focusRow(w.row); },
        }, h("b", {}, fmtHM(w.start)), " ", h("span", {}, w.special && w.name ? w.name : name(w.broadcast_id)));
      })));
  }
  return out;
}

// Week and day views: hours down the side, one column per day.
function timeGrid(days, { slots, specials, now, name }) {
  const HOUR = 46; // px per hour
  const cols = days.map((day) => ({ day, ...windowsOn(day, slots, specials) }));
  const all = cols.flatMap((c) => c.wins);
  // Show the working day by default, stretched to fit whatever is scheduled.
  let lo = 7, hi = 21;
  for (const w of all) {
    lo = Math.min(lo, Math.floor(hmToMin(w.start) / 60));
    hi = Math.max(hi, Math.ceil(hmToMin(w.end) / 60));
  }
  const px = (min) => (min - lo * 60) / 60 * HOUR;
  const height = (hi - lo) * HOUR;

  const grid = h("div", { class: "tg" + (days.length === 1 ? " single" : "") });
  grid.style.gridTemplateColumns = `52px repeat(${days.length}, minmax(0, 1fr))`;
  grid.style.setProperty("--hour", HOUR + "px");

  grid.append(h("div", { class: "tg-corner" }));
  for (const c of cols) {
    const isToday = c.date === now.date;
    const label = c.day.toLocaleDateString([], { weekday: "long", month: "long", day: "numeric", timeZone: "UTC" });
    grid.append(h("button", {
      type: "button", class: "tg-head" + (isToday ? " today" : "") + (c.date < now.date ? " past" : ""),
      title: days.length > 1 ? "Open this day" : "", "aria-label": label, disabled: days.length === 1,
      onclick: () => showDay(c.day),
    },
      h("span", {}, c.day.toLocaleDateString([], { weekday: "short", timeZone: "UTC" })),
      h("span", { class: "cal-num" }, c.day.getUTCDate()),
      c.replaced ? h("small", {}, `Special date: replaces ${c.replaced} weekly time${c.replaced === 1 ? "" : "s"}`)
        : c.wins.some((w) => w.special) ? h("small", {}, "Special date") : null));
  }

  const hours = h("div", { class: "tg-hours" });
  hours.style.height = height + "px";
  for (let hr = lo; hr <= hi; hr++) {
    const lab = h("span", {}, fmtHM(minToHM((hr % 24) * 60)).replace(":00", ""));
    lab.style.top = (hr - lo) * HOUR + "px";
    hours.append(lab);
  }
  grid.append(hours);

  for (const c of cols) {
    const past = c.date < now.date;
    const col = h("div", { class: "tg-col" + (past ? " past" : "") + (c.date === now.date ? " today" : ""), title: past ? "" : "Add a special date at this time" });
    col.style.height = height + "px";
    col.addEventListener("click", (e) => {
      if (past) return;
      const y = e.clientY - col.getBoundingClientRect().top;
      const start = Math.min(22 * 60 + 30, Math.max(0, Math.floor((lo * 60 + y / HOUR * 60) / 30) * 30));
      addSpecial(c.date, minToHM(start), minToHM(Math.min(start + 90, 23 * 60 + 59)));
    });
    for (const w of c.wins) {
      const s = hmToMin(w.start), e = hmToMin(w.end);
      const others = c.wins.filter((o) => overlaps(w, o));
      const live = c.date === now.date && s <= now.minutes && now.minutes < e;
      const block = h("button", {
        type: "button",
        class: "tg-block" + (w.special ? " special" : "") + (w.clash ? " clash" : "") + (live ? " now" : ""),
        title: winTitle(w, c.replaced, name),
        onclick: (ev) => { ev.stopPropagation(); focusRow(w.row); },
      },
        h("b", {}, `${fmtHM(w.start)} – ${fmtHM(w.end)}`),
        h("span", {}, w.special && w.name ? w.name : name(w.broadcast_id)),
        w.special && w.name ? h("span", { class: "sub" }, "→ " + name(w.broadcast_id)) : null,
        w.clash ? h("span", { class: "sub" }, "Overlaps: Go live won't pick") : null);
      block.style.top = px(s) + "px";
      block.style.height = Math.max(22, px(e) - px(s) - 2) + "px";
      block.style.left = `calc(${others.indexOf(w) / others.length * 100}% + 2px)`;
      block.style.width = `calc(${100 / others.length}% - 4px)`;
      col.append(block);
    }
    if (c.date === now.date && now.minutes >= lo * 60 && now.minutes <= hi * 60) {
      const line = h("div", { class: "tg-now", title: "Now" });
      line.style.top = px(now.minutes) + "px";
      col.append(line);
    }
    grid.append(col);
  }
  return grid;
}

function addSpecial(date, start, end) {
  const row = specialRow({ date, start, end });
  $("#special-rows").append(row);
  scheduleChanged();
  row.scrollIntoView({ block: "center", behavior: "smooth" });
  row.querySelector(date ? "[name=name]" : "[name=date]").focus({ preventScroll: true });
}

function setCalView(view) {
  calView = view;
  try { localStorage.setItem("doxaboard.calView", view); } catch (_) {}
  renderSchedulePreview();
}

function moveCal(delta) {
  const [y, m, d] = nowInZone(scheduleZone).date.split("-").map(Number);
  const a = calAnchor || new Date(Date.UTC(y, m - 1, d));
  calAnchor = calView === "month" ? new Date(Date.UTC(a.getUTCFullYear(), a.getUTCMonth() + delta, 1))
    : addDays(a, calView === "week" ? 7 * delta : delta);
  renderSchedulePreview();
}

function fillSchedule(sched) {
  const slots = [...sched.slots].sort((a, b) => a.weekday - b.weekday || a.start.localeCompare(b.start));
  const specials = [...sched.specials].sort((a, b) => a.date.localeCompare(b.date) || a.start.localeCompare(b.start));
  $("#slot-rows").replaceChildren(...slots.map(slotRow));
  $("#special-rows").replaceChildren(...specials.map(specialRow));
  renderSchedulePreview();
}

async function loadSchedule() {
  if (scheduleDirty) return; // coming back to the tab mid-edit: keep the edits
  let st;
  [broadcasts, schedule, st] = await Promise.all([
    api("GET", "/api/v1/admin/broadcasts"), api("GET", "/api/v1/admin/live/schedule"), api("GET", "/api/v1/live/status")]);
  scheduleZone = st.time_zone;
  $("#schedule-tz").textContent = "Times are in " + st.time_zone;
  $("#schedule-empty").hidden = broadcasts.length > 0;
  $("#schedule-editor").hidden = !broadcasts.length;
  setScheduleDirty(false);
  fillSchedule(schedule);
}

$("#schedule-editor").addEventListener("input", (e) => { if (e.target.closest(".sched-row")) scheduleChanged(); });
$("#schedule-to-live").addEventListener("click", () => showTab("live"));

$("#add-slot").addEventListener("click", () => {
  const row = slotRow({ weekday: 0 });
  $("#slot-rows").append(row);
  scheduleChanged();
  row.querySelector("select").focus();
});
$("#add-special").addEventListener("click", () => addSpecial(""));
$("#cal-prev").addEventListener("click", () => moveCal(-1));
$("#cal-next").addEventListener("click", () => moveCal(1));
$("#cal-today").addEventListener("click", () => { calAnchor = null; renderSchedulePreview(); });
document.querySelectorAll("#cal-views button").forEach((b) => b.addEventListener("click", () => setCalView(b.dataset.view)));
$("#clear-past").addEventListener("click", () => {
  document.querySelectorAll("#special-rows .sched-row.past").forEach((r) => r.remove());
  scheduleChanged();
});
$("#discard-schedule").addEventListener("click", () => {
  setScheduleDirty(false);
  fillSchedule(schedule);
});

$("#save-schedule").addEventListener("click", async () => {
  const sched = readSchedule();
  const strip = ({ row, ...rest }) => rest;
  try {
    schedule = await api("PUT", "/api/v1/admin/live/schedule", { slots: sched.slots.map(strip), specials: sched.specials.map(strip) });
    setScheduleDirty(false, "Schedule saved.");
    fillSchedule(schedule);
  } catch (err) {
    setScheduleDirty(true, "Not saved: " + err.message);
  }
});

window.addEventListener("beforeunload", (e) => { if (scheduleDirty) e.preventDefault(); });

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
