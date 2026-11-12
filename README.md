# DoxaBoard

[![CI](https://github.com/jumpinjet22/DoxaBoard/actions/workflows/ci.yml/badge.svg)](https://github.com/jumpinjet22/DoxaBoard/actions/workflows/ci.yml)
[![Lint](https://github.com/jumpinjet22/DoxaBoard/actions/workflows/lint.yml/badge.svg)](https://github.com/jumpinjet22/DoxaBoard/actions/workflows/lint.yml)
[![CodeQL](https://github.com/jumpinjet22/DoxaBoard/actions/workflows/codeql.yml/badge.svg)](https://github.com/jumpinjet22/DoxaBoard/actions/workflows/codeql.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/jumpinjet22/DoxaBoard/badge)](https://scorecard.dev/viewer/?uri=github.com/jumpinjet22/DoxaBoard)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

**One live stream, every screen in the building, and screens that keep going no matter what.**

DoxaBoard takes a single stream from OBS and puts it on every TV in a church
(or school, or office) over the network you already have. When nothing is
live, screens show slides and photos from the places your content already
lives: a OneDrive folder today, RSS feeds, Rock RMS and other systems through
connectors. It also pushes the same stream to Vimeo, so you only encode once.

> *Doxa* (δόξα) is Greek for "glory," as in *Gloria Dei* and *doxology*.
> "Let your light shine before others, that they may see your good deeds and
> glorify your Father in heaven." (Matthew 5:16)

## How it works

```
                         ┌───────────────────── server (Docker) ─────────────────────┐
OBS ──SRT──▶ relay (mediamtx) ──┬── multicast ──▶ every screen on the LAN           │
                                ├── SRT / RTSP / LL-HLS ──▶ screens that pull        │
                                └── RTMPS ──▶ Vimeo                                  │
   OneDrive, RSS, Rock, ... ──▶ connectors ──▶ doxaboard-server ◀── admin console     │
                         └────────────────────────────────────────────────────────────┘
                                                   ▲
                          desired state / heartbeats
                                                   │
                     doxaboard-agent on each screen (Raspberry Pi)
```

Every screen runs a small agent that tries sources in priority order and fails over by itself:

1. **Multicast** from the relay: one stream for the whole building, lowest latency
2. **Unicast SRT/RTSP** from the relay, if multicast isn't arriving
3. **Vimeo** (HLS), if the relay is down
4. **Slideshow**, if nothing is live
5. **Cached slides**, if the network is down

The control plane is central, but screens don't depend on it. If the server
goes down, every screen keeps doing what it was last told, even across reboots.

## Status

**Early, pre-1.0.** Here's what's real today:

| Piece | State |
|---|---|
| Failover core, control plane, admin console | ✅ Built, tested |
| Pi agent (mpv, probes, slideshow, watchdog) | ✅ Built, tested |
| Relay stack (mediamtx, multicast, Vimeo push, rclone) | ✅ Built; config checked against mediamtx 1.21 |
| OneDrive photo slideshow | ✅ Works through the server's photo albums |
| Content sources: RSS/Atom, JSON feed, folder | ✅ Add them in the console's **Content** tab; text-only posts become rendered slides |
| Rock RMS, WordPress, Planning Center connectors | ⬜ Planned |
| Running on real Pis and TVs | ⬜ **Not yet**: the next milestone |

Everything is covered by automated tests, but **nobody has run it on real
hardware yet**. Don't put it in front of a congregation until that changes.
See [ROADMAP.md](ROADMAP.md).

## Quick start

### 1. Server

You need a Linux box on the same network as the screens, with Docker.

```sh
git clone https://github.com/jumpinjet22/DoxaBoard
cd DoxaBoard/deploy/server
cp .env.example .env            # set the two tokens: openssl rand -hex 24
mkdir -p data secrets photos && sudo chown 65532:65532 data secrets
docker compose run --rm rclone config   # create a remote named "onedrive"
docker compose up -d
```

Open `http://<server>:8080` and sign in with `DOXABOARD_ADMIN_TOKEN`.

### 2. OBS

Settings → Stream → Custom:

- **Server:** `srt://<server>:8890?streamid=publish:live&pkt_size=1316`

Settings → Output: H.264, 1080p30, ~6 Mbps CBR, keyframe interval 2s, AAC 48 kHz.
See [docs/obs.md](docs/obs.md).

### 3. Screens (Raspberry Pi)

Raspberry Pi OS Lite, wired Ethernet. Build the agent (`make build`), then on the Pi:

```sh
sudo deploy/agent/install.sh ./doxaboard-agent-linux-arm64 http://<server>:8080 <DOXABOARD_ENROLLMENT_TOKEN>
```

The Pi appears in the admin console. Create a profile, assign it, and you're done.

## Content connectors

Slides can come from almost anywhere, and most systems need no code at all:

- **RSS / Atom:** WordPress, Squarespace, most church website builders
- **JSON feed:** the [`doxaboard.slides/v1`](docs/connectors.md#the-slide-feed) format. Anything that can output JSON can feed DoxaBoard.
- **Folder:** any album the server can see, e.g. OneDrive, Google Drive or Dropbox via rclone

Want to connect something else? Write a **sidecar** in any language (start
from [`examples/sidecar-python`](examples/sidecar-python)), or an **in-tree** Go
connector. The `doxaboard-connector` tool runs the same checks CI does:

```sh
go run ./cmd/doxaboard-connector check-feed http://localhost:8090/feed   # any-language sidecars
go run ./cmd/doxaboard-connector new my-cms                             # scaffold a Go connector
make connector-check NAME=mycms                                         # what CI runs
```

Everything you need is in [docs/connectors.md](docs/connectors.md).

## Build and test

```sh
make lint test        # gofmt, vet, tests with the race detector
make build            # agent, server and connector tool for arm64, armv7 and amd64 → ./dist
golangci-lint run     # the linters CI runs
```

Go 1.26.11+ (the `go` command fetches it automatically). No cgo, so everything cross-compiles.

## Repo layout

| Path | What |
|---|---|
| `core/` | Platform-independent brain: desired state, failover, engine (gomobile-ready for Android TV) |
| `slides/` | The `doxaboard.slides/v1` feed format, validation and JSON Schema |
| `connectors/` | Content connectors, the registry they plug into, and the conformance kit |
| `internal/server/` | Control plane: API, SQLite store, embedded admin console |
| `internal/agent/` | Linux screen agent: mpv, probes, slideshow, metrics |
| `cmd/` | `doxaboard-server`, `doxaboard-agent`, `doxaboard-connector` |
| `deploy/server/` | Docker Compose: server, mediamtx relay, multicast, Vimeo, rclone |
| `deploy/agent/` | systemd unit, install script, example config |
| `examples/` | A sidecar connector in standard-library Python |
| `docs/` | Architecture, connectors, network, hardware, OBS |

## Docs

- [Architecture](docs/architecture.md)
- [Connectors and the slide feed](docs/connectors.md)
- [Network: multicast, IGMP, long cable runs](docs/network.md)
- [Hardware: Pi models, power, composite, Android TV](docs/hardware.md)
- [OBS setup](docs/obs.md)
- [Roadmap](ROADMAP.md)

## Security

Every PR runs tests with the race detector, golangci-lint (including gosec),
CodeQL, govulncheck and dependency review. Actions and container images are
pinned by hash, the parsers are fuzzed nightly, and Dependabot keeps it all
current.

Found a vulnerability? **Please report it privately.** See [SECURITY.md](SECURITY.md).

## Contributing

Issues and PRs are welcome, especially from other churches. Please read
[CONTRIBUTING.md](CONTRIBUTING.md) (short version: open an issue first, sign
off your commits with `git commit -s`, and never commit real member data).

## License

[Apache-2.0](LICENSE)
