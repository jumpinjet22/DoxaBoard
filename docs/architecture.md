# Architecture

## Two planes

**Control plane (central):** `doxacast-server` stores screens, profiles and
photos, and answers one question per screen: *what should you be doing?*

**Data plane (decentralized):** each screen's agent pulls its desired state,
caches it to disk, and makes every playback decision locally: which source,
when to fail over, when to fall back to photos.

This is the same split Kubernetes uses: if the control plane goes away, nodes
keep running what they were last told. A server outage means you can't
*change* anything, not that screens go dark.

## Desired state

`GET /api/v1/device/state` returns:

```json
{
  "revision": 42,
  "device_id": "d_1a2b3c",
  "name": "Lobby left",
  "sources": [
    {"id": "s1", "kind": "multicast", "url": "udp://239.10.10.1:5000", "priority": 10},
    {"id": "s2", "kind": "srt", "url": "srt://relay:8890?streamid=read:live", "priority": 20},
    {"id": "s3", "kind": "hls", "url": "https://.../playlist.m3u8", "priority": 30}
  ],
  "slideshow": {"manifest_url": "/api/v1/photos/lobby/manifest", "interval_seconds": 10},
  "display": {"output": "hdmi"},
  "poll_seconds": 20
}
```

- **Revision** is a global counter bumped on any admin change. Screens send
  `If-None-Match` and get `304` when nothing changed, so polling costs almost nothing.
- Polls are jittered ±20%.
- Relative URLs (like `manifest_url`) resolve against the server URL.

## Failover

Implemented in `core/failover.go` as pure logic with an injectable clock and
covered by unit tests.

- Every tick (5s on the Pi), each source is probed concurrently. The source on
  screen isn't probed: the player itself proves it works.
- A source is **up** after `RiseCount` consecutive good probes and **down**
  after `FallCount` bad ones (hysteresis).
- The highest-priority source that's up wins.
- **Downgrades are immediate.** **Upgrades wait** `UpgradeHold` (10s) so a
  flickering multicast doesn't bounce the screen back and forth.
- If the player can't actually play a source that probes fine, that source
  sits out a **cooldown** (30s) and the next one is tried in the same tick.
- No live source → slideshow (if the profile has an album) → idle (black).

## Agent (Linux)

- **mpv** plays everything, including the slideshow, so one dependency covers
  both and it runs on Pi OS Lite with DRM (no desktop).
- **Probes:** multicast joins the group and checks for an MPEG-TS sync byte
  (joining also makes IGMP-snooping switches start forwarding); SRT/RTSP use
  `ffprobe`; HLS fetches the playlist.
- **Storage:** credentials, cached state and photos live in
  `/var/lib/doxacast`, written atomically so a power cut can't corrupt them.
- **Metrics:** link speed, rx/tx errors, drops and CPU temperature go in every
  heartbeat, so you can spot a marginal cable run before anyone complains.

## Server

- Single Go binary, SQLite (pure Go), embedded admin console. No external database.
- Auth: an admin token for the console/API; a shared enrollment token lets
  screens register and receive their own device token (stored hashed).
- Photos come from a directory with one subfolder per album, mirrored from
  OneDrive by the rclone container. Screens download them once and cache them.

## Relay

mediamtx receives OBS once and serves it to everything. `on-ready.sh` starts
two `ffmpeg -c copy` processes when OBS goes live: one sends multicast, one
pushes to Vimeo. Nothing is transcoded, so CPU use is negligible.

## Android TV (planned)

`core` has no OS dependencies so it can be compiled with `gomobile bind`.
The Kotlin app implements `Player`, `Slideshow`, `Prober` and `Store` with
ExoPlayer and Android APIs, and the failover logic stays identical on both
platforms. See the roadmap.
