# DoxaCast

**One live stream, every screen in the building, and screens that keep going no matter what.**

DoxaCast takes a single stream from OBS and puts it on every TV in a church
(or school, or office) over the network you already have. When nothing is
live, screens show a photo slideshow synced from OneDrive. It also pushes the
same stream to Vimeo, so you only encode once.

> *Doxa* (δόξα) is Greek for "glory," as in *Gloria Dei* and *doxology*.
> "Let your light shine before others, that they may see your good deeds and
> glorify your Father in heaven." (Matthew 5:16)

## How it works

```
                         ┌──────────────────── server (Docker) ────────────────────┐
OBS ──SRT──▶ relay (mediamtx) ──┬── multicast ──▶ every screen on the LAN         │
                                ├── SRT / RTSP / LL-HLS ──▶ screens that pull      │
                                └── RTMPS ──▶ Vimeo                                │
             OneDrive ──rclone──▶ photos ──▶ doxacast-server ◀── admin console     │
                         └──────────────────────────────────────────────────────────┘
                                                   ▲
                          desired state / heartbeats
                                                   │
                     doxacast-agent on each screen (Raspberry Pi)
```

Every screen runs a small agent that tries sources in priority order and fails over by itself:

1. **Multicast** from the relay: one stream for the whole building, lowest latency
2. **Unicast SRT/RTSP** from the relay, if multicast isn't arriving
3. **Vimeo** (HLS), if the relay is down
4. **Photo slideshow**, if nothing is live
5. **Cached photos**, if the network is down

The control plane is central, but screens don't depend on it. If the server
goes down, every screen keeps doing what it was last told, even across reboots.

## Status

Early MVP. The core, server and agent are covered by automated tests, but it
**hasn't been run on real Pis and TVs yet**. That's next. See [ROADMAP.md](ROADMAP.md).

## Quick start

### 1. Server

You need a Linux box on the same network as the screens, with Docker.

```sh
git clone https://github.com/jumpinjet22/doxacast
cd doxacast/deploy/server
cp .env.example .env            # set the two tokens: openssl rand -hex 24
mkdir -p data photos && sudo chown 65532:65532 data
docker compose run --rm rclone config   # create a remote named "onedrive"
docker compose up -d
```

Open `http://<server>:8080` and sign in with `DOXACAST_ADMIN_TOKEN`.

### 2. OBS

Settings → Stream → Custom:

- **Server:** `srt://<server>:8890?streamid=publish:live&pkt_size=1316`

Settings → Output: H.264, 1080p30, ~6 Mbps CBR, keyframe interval 2s, AAC 48 kHz.
See [docs/obs.md](docs/obs.md).

### 3. Screens (Raspberry Pi)

Raspberry Pi OS Lite, wired Ethernet. Build or download the agent, then run:

```sh
sudo deploy/agent/install.sh ./doxacast-agent-linux-arm64 http://<server>:8080 <DOXACAST_ENROLLMENT_TOKEN>
```

The Pi appears in the admin console. Create a profile, assign it, and you're done.

## Build

```sh
go test ./...
make build        # linux/arm64, linux/arm (armv7) and linux/amd64 into ./dist
```

Go 1.26+. No cgo; everything cross-compiles.

## Repo layout

| Path | What |
|---|---|
| `core/` | Platform-independent brain: state, failover, engine. Also targeted by the Android app via gomobile. |
| `internal/server/` | Control plane: API, SQLite store, embedded admin console |
| `internal/agent/` | Linux implementations: mpv, probes, slideshow, metrics |
| `cmd/` | `doxacast-server` and `doxacast-agent` binaries |
| `deploy/server/` | Docker Compose: server, mediamtx relay, multicast, Vimeo, rclone |
| `deploy/agent/` | systemd unit, install script, example config |
| `docs/` | Architecture, network (multicast/IGMP, long runs), hardware, OBS |

## Docs

- [Architecture](docs/architecture.md)
- [Network: multicast, IGMP, long cable runs](docs/network.md)
- [Hardware: Pi models, power, composite, Android TV](docs/hardware.md)
- [OBS setup](docs/obs.md)

## License

[Apache-2.0](LICENSE)
