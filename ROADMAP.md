# DoxaBoard Roadmap

DoxaBoard puts one live stream (and a photo fallback) on every screen in a
building, and keeps each screen running on its own when anything upstream
fails. This is the plan, in the order we intend to build it.

Status key: ✅ done · 🚧 in progress · ⬜ not started

---

## Milestone 0: Foundation ✅

- ✅ Repo, Apache-2.0 license, Go module, CI (vet, test, race, arm64 build)
- ✅ Architecture, network, hardware and OBS docs
- ✅ Security tooling: CodeQL, golangci-lint, govulncheck, dependency review,
  OpenSSF Scorecard, nightly fuzzing, Dependabot, SHA/digest pinning
- ✅ Contributor setup: CONTRIBUTING (DCO), SECURITY policy, issue forms,
  PR template, CODEOWNERS, auto-labeling, gentle stale bot

## Milestone 1: Control plane + Pi agent MVP 🚧

The goal is one OBS stream on a few Pis with automatic failover, and photos when nothing is live.

**Core (`core/`, shared brain, no OS code):**
- ✅ Desired-state model: sources, slideshow, display settings
- ✅ Failover selector with hysteresis, upgrade hold and playback cooldown
- ✅ Engine: sync → probe → decide → apply → heartbeat loop
- ✅ Runs on cached state when the server is unreachable (survives reboots)
- ✅ Poll jitter, so a building full of screens doesn't stampede after a power cut

**Server (`cmd/doxaboard-server`):**
- ✅ SQLite store (pure Go, no cgo)
- ✅ Device enrollment with a shared token → per-device credentials
- ✅ Desired state endpoint with ETag/304
- ✅ Heartbeats with mode, per-source health and link/thermal metrics
- ✅ Profiles: ordered sources + photo album, assigned to screens
- ✅ Photo manifests and files from the OneDrive-synced folder
- ✅ Admin console: screens (status, now showing, profile, output) and profiles editor

**Pi agent (`cmd/doxaboard-agent`):**
- ✅ mpv playback (DRM, no desktop needed) for multicast, SRT, RTSP and HLS
- ✅ Probes: multicast join + MPEG-TS check, ffprobe for SRT/RTSP, HLS playlist check
- ✅ Photo slideshow with a local cache, so it keeps going offline
- ✅ Link speed, error counters and CPU temperature in heartbeats
- ✅ systemd unit, hardware watchdog, install script

**Server stack (`deploy/server`):**
- ✅ mediamtx relay: OBS → SRT/RTMP in; SRT/RTSP/LL-HLS out
- ✅ Multicast fanout (copy, no transcode)
- ✅ Vimeo RTMPS push (copy, no transcode)
- ✅ rclone OneDrive mirror

**Still to do in M1:**
- ⬜ First real-hardware test: Pi 4 + Pi 5, multicast through a managed switch
- ⬜ Tune probe and failover timings on real hardware
- ⬜ Release workflow: tagged builds publish arm64/armv7/amd64 binaries
- ⬜ "Copy setup command" button in the console (one-line Pi install)

## Milestone 2: Day-to-day operations ⬜

- ⬜ Groups/zones (lobby, hallways, classrooms) with bulk assignment
- ⬜ Schedules ("live stream Sundays 9–12, photos otherwise")
- ⬜ HDMI-CEC: turn TVs on/off and select the input, per group and on a schedule
- ⬜ Remote actions: restart agent, reboot, force a source, identify screen (flash its name)
- ⬜ Screenshots: what is each screen showing right now?
- ⬜ Offline alerts (email/webhook) when a screen misses heartbeats
- ⬜ Per-screen health history (link speed drops, error counters, temperature)

## Milestone 2.5: Theming ⬜

Brand everything from one place, with no code changes.

- ⬜ Theme settings in the console: logo, colors, fonts, background
- ⬜ Admin console follows the theme
- ⬜ Branded idle screen (logo + message) instead of black
- ⬜ Optional overlays on screens: logo bug, clock, "Live" badge, lower-third text
- ⬜ Slide templates (title + image + text) that use the theme
- ⬜ Ship a neutral default theme; theme files are importable/exportable so
  churches can share them

## Milestone 3: Composite and legacy screens ⬜

- ⬜ Composite output mode end to end: config.txt helper, NTSC/PAL, 4:3 safe area
- ⬜ Documented "Pi in the rack" mode for runs too long for Ethernet
- ⬜ Low-bitrate profile option for marginal 100 Mbps / long runs

## Milestone 4: Android TV / Google TV app ⬜

- ⬜ gomobile facade for `core` (JSON across the boundary)
- ⬜ Kotlin app: ExoPlayer for multicast/HLS, photo slideshow
- ⬜ Device Owner provisioning via adb: kiosk lock, boot launch, remote reboot
- ⬜ Test hardware: Google TV Streamer, onn 4K Pro (built-in Ethernet)
- ⬜ Web player page as a zero-install option for one-off screens

## Milestone 4.5: Content sources (Rock RMS and other CMSes) 🚧

Slides can come from anywhere, through one provider interface on the server.
Screens never talk to a CMS directly: the server pulls, caches images, and
hands screens a manifest, so a CMS outage never blanks a TV.

**The interface:** each provider returns slides (title, body, image,
start/expire, order, duration). A profile's slideshow can merge several sources.

**Foundation:**
- ✅ Slide feed format `doxaboard.slides/v1` with validation and a JSON Schema
- ✅ Connector registry, host-guarded HTTP client, conformance kit
- ✅ `doxaboard-connector` CLI: `check-feed`, `new` (scaffold), `lint`; Python sidecar example
- ✅ Contributor docs (docs/connectors.md), proposal and listing issue forms
- ✅ Wire connectors into the server: Content tab with forms generated from
  connector fields, sources in profiles, server-side image processing and
  text-slide rendering, last-good caching, secrets encrypted at rest
- ⬜ Per-slide durations and tags/zones on screens
- ⬜ Listing bot and nightly re-check for community sidecars

**Universal sources (cover most CMSes with no custom code):**
- ✅ Folder: anything rclone supports (OneDrive, Google Drive, Dropbox, SharePoint, S3, SMB...)
- ✅ RSS / Atom feed with images (WordPress, Squarespace, Rock channel feeds, most CMSes)
- ✅ Generic JSON feed: a small documented schema anyone can point a script,
  Zapier/Make flow or CMS template at

**First-class connectors:**
- ⬜ Rock RMS: content channels (start/expire, order, image attribute),
  calendar → upcoming events, service schedules → DoxaBoard schedules;
  read-only REST key, server-side only
- ⬜ WordPress REST API (posts or a category, with featured images)
- ⬜ Planning Center (Calendar events)
- ⬜ Community-contributed connectors (compiled in, following a documented provider guide)

## Milestone 5: Hardening for other churches ⬜

- ⬜ **Relay publish credentials**: today anyone on the LAN can publish to the
  relay while OBS is offline (takeover during a live stream is already blocked).
  Require a publish user/password (or OBS's IP) and document it in docs/obs.md.
- ⬜ TLS guidance (reverse proxy) and optional built-in ACME
- ⬜ Rotate enrollment token; revoke and re-enroll devices
- ⬜ Agent self-update from the server (signed binaries)
- ⬜ Read-only root filesystem image for Pis (power-cut proof)
- ⬜ Multiple relays: assign screens to relays for very large buildings
- ⬜ Backup/restore of server data
- ⬜ Prebuilt Pi OS image with the agent preinstalled

---

## Non-goals (for now)

- Transcoding on the server. We copy the OBS stream everywhere. If a screen
  can't decode it, fix the OBS settings, not the server.
- Peer-to-peer coordination between screens. Each screen is independent; the
  control plane is the only coordinator, and screens don't need it to keep playing.
- Being a general digital-signage CMS. Live stream + photos, done well.
