# Live broadcast

DoxaBoard separates two things that used to be one button:

| | Who sees it | Controlled by |
|---|---|---|
| **OBS → relay** | Screens in the building | Starting/stopping the stream in OBS |
| **Relay → Vimeo / YouTube / Facebook…** | The public | DoxaBoard's **Go live** / **End** |

So you can run OBS from the pre-service countdown onward, check it on the
screens, and only go public when you're ready. **End** stops the public
broadcast; screens in the building keep playing.

## Setup

### 1. OBS: send to the relay only

OBS streams to the relay and nowhere else (see [obs.md](obs.md)):

```
srt://<server>:8890?streamid=publish:live&pkt_size=1316
```

Remove any Vimeo/YouTube/Facebook stream keys from OBS. DoxaBoard holds them now.

### 2. Broadcasts

In the console, open **Live → Add broadcast**. A broadcast is where one
service goes: one or more **outputs**, each with the stream URL and key your
platform shows you.

| Platform | Stream URL | Stream key |
|---|---|---|
| Vimeo | shown in the event's stream settings (RTMPS) | from the same page |
| YouTube | `rtmp://a.rtmp.youtube.com/live2` | YouTube Studio → Stream key |
| Facebook | `rtmps://live-api-s.facebook.com:443/rtmp/` | Live Producer → Stream key |
| Anything else | any `rtmp://`, `rtmps://` or `srt://` ingest | if it has one |

Add several outputs to one broadcast to stream to several platforms at once
(e.g. YouTube **and** Facebook). Keys are encrypted at rest and never shown
again; leave the key blank when editing to keep it.

**Tip:** if your platform has a "go live manually from a preview" mode, switch
the event to start **automatically** when it receives video. DoxaBoard's Go
live is your go-live button now, and the screens in the building are your preview.

Recurring events with a fixed stream key (one per service) work best: set
them up once and you're done.

### 3. Schedule (optional)

With a schedule, the **Go live** button picks the right broadcast by itself.

- **Every week:** day, start, end, broadcast. E.g. Sunday 08:15-09:45 → *8:30 service*.
  Give each window some slack before and after the service.
- **Special dates:** a date, a time and a broadcast, e.g. a combined Christmas
  service. **On a date with special entries, the weekly schedule is ignored**
  for that whole day, so the smart button can't pick a regular service by mistake.
- Times use the server's time zone. Set `DOXABOARD_TIMEZONE` (e.g.
  `America/Chicago`); the Live tab shows which zone is in effect.

Without a schedule, pick the broadcast by hand: the Live tab shows a
**Go live** button for each one.

## Safety rules

- **One broadcast at a time.** Going live on a different broadcast while one
  is live is refused: end it first.
- **No signal, no broadcast.** Go live is refused until OBS is actually
  sending to the relay.
- **Ambiguous or empty schedule:** the smart button refuses instead of guessing.
- **Self-healing:** if the relay restarts mid-service, DoxaBoard restores the
  outputs within about 15 seconds.

## Buttons: Companion, Stream Deck, anything that speaks HTTP

Set `DOXABOARD_LIVE_TOKEN` in `.env` (generate with `openssl rand -hex 24`).
This token can **only** go live, end, and read status: it can't change
screens, content or settings, so it's safe to put on a button.

| Action | Request |
|---|---|
| Go live (scheduled broadcast) | `POST /api/v1/live/start` |
| Go live (a specific broadcast) | `POST /api/v1/live/start?broadcast=<id>` |
| End | `POST /api/v1/live/stop` |
| Status (JSON) | `GET /api/v1/live/status` |
| Status (one line, for button text) | `GET /api/v1/live/status.txt` → `LIVE 11:00 service 0:42`, `READY 11:00 service`, `OBS ON` or `OFF` |

Every request needs the header `Authorization: Bearer <DOXABOARD_LIVE_TOKEN>`.
A broadcast's `<id>` is shown on its card in the Live tab ("ID for buttons").

Responses: `200` with the new status; `409` with a readable reason when it's
refused (already live, OBS not sending, nothing scheduled); `401` for a wrong
token; `502` if the relay isn't reachable.

### Bitfocus Companion

Use the **Generic: HTTP Requests** module. Point it at `http://<server>:8080`
and add the header `Authorization: Bearer <DOXABOARD_LIVE_TOKEN>`.

- **GO LIVE** button: your existing OBS module action *Start streaming*
  (if OBS isn't already running), then an HTTP **POST** to `/api/v1/live/start`.
- **END** button: HTTP **POST** to `/api/v1/live/stop`. Keep OBS running if
  the screens should stay on.
- Per-service override buttons: POST `/api/v1/live/start?broadcast=<id>`.
- Button text: if your Companion version can poll a URL into a variable,
  use `/api/v1/live/status.txt`.

Going public should be deliberate. Consider a **press-and-hold** (or
two-step) button for Go live and End, so a bumped Stream Deck mid-sermon
doesn't end the broadcast.

### Other tools

Anything that can send an HTTP request with a header works: Stream Deck HTTP
plugins, Home Assistant, an iOS Shortcut, `curl`:

```sh
curl -X POST -H "Authorization: Bearer $DOXABOARD_LIVE_TOKEN" http://server:8080/api/v1/live/start
```

## How it works

The relay (mediamtx) forwards the incoming OBS stream to each output itself,
with no re-encoding. DoxaBoard switches that forwarding on and off through
mediamtx's control API, which listens on `127.0.0.1` only and is never
exposed to the network. Stream keys are passed only to that local API and are
redacted from anything DoxaBoard shows or logs.
