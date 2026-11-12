# Connectors

Connectors bring content into DoxaCast: announcements from Rock RMS, posts from
WordPress, photos from a folder, anything with a feed. This page is for people
who want to **use** one, **write** one, or **get one listed**.

## Using a connector

In the admin console, open **Content → Add source**, pick a type (RSS / Atom,
JSON feed, Folder...) and fill in its settings. The server fetches it right
away and then every 10 minutes; **Preview** shows the slides it made, and
**Refresh now** fetches immediately. Then tick the source in a **profile**,
and screens using that profile show its slides whenever nothing is live.

- Slides with an image show the image (resized to 1080p on the server).
- Slides with only text are rendered as a clean text slide.
- Start and expire dates are respected; expired slides drop off on their own.
- API keys and tokens are encrypted at rest and never shown again after saving.
- If a fetch fails, screens keep the last good slides and the console shows the error.

## How content flows

```
CMS ──▶ connector ──▶ DoxaCast server (validates, caches images) ──▶ screens
```

Screens never talk to a CMS. The server pulls slides, checks them, downloads
and resizes the images, and hands screens a cached list. If a CMS goes down,
screens keep showing the last good slides.

## Three ways to connect a CMS

| | Who runs the code | Language | Approval |
|---|---|---|---|
| **Feed** (RSS/Atom, JSON feed, folder) | nobody: it's built in | none | none |
| **Sidecar** | you, next to DoxaCast | any | optional listing |
| **In-tree** | DoxaCast itself | Go | PR + maintainer review |

Start at the top. Most CMSes already publish an RSS feed, and if yours does,
you're done. If not, a sidecar is usually the fastest path: a small service in
whatever language you like that serves the slide feed. In-tree connectors are
for widely used systems that deserve first-class support.

---

## The slide feed

Everything speaks one format: `doxacast.slides/v1`.

```json
{
  "schema": "doxacast.slides/v1",
  "slides": [
    {
      "id": "rock-1234",
      "title": "Fall Festival",
      "body": "Oct 26 · 4-7pm · Family Life Center",
      "image": { "url": "https://example.org/fest.jpg", "alt": "Fall Festival" },
      "starts_at": "2026-10-01T00:00:00Z",
      "expires_at": "2026-10-27T00:00:00Z",
      "order": 10,
      "duration_seconds": 12,
      "tags": ["lobby", "kids"],
      "updated_at": "2026-09-20T14:02:00Z"
    }
  ]
}
```

| Field | Required | Rules |
|---|---|---|
| `id` | **yes** | ≤200 chars. **Stable**: the same content gets the same id every fetch. Unique in the feed. |
| `title` | title or image | ≤200 chars |
| `body` | | ≤1000 chars. **Plain text.** HTML is shown literally. |
| `image.url` | title or image | Absolute http(s), or relative to the feed URL |
| `image.alt` | | ≤300 chars. Strongly recommended for image-only slides. |
| `starts_at` / `expires_at` | | RFC 3339. `expires_at` must be after `starts_at`. |
| `order` | | Ascending; ties keep feed order |
| `duration_seconds` | | 3–300; overrides the profile's per-slide time |
| `tags` | | ≤20, no spaces. Target zones like `lobby`. |
| `updated_at` | | Lets the server skip unchanged images |

Feed limits: 4 MiB of JSON, 500 slides.

**Compatibility:** v1 may gain new optional fields, so readers must ignore
fields they don't know. Anything breaking becomes `doxacast.slides/v2`, and the
server will accept both for a transition period.

The JSON Schema is at
[`slides/schema/doxacast.slides.v1.schema.json`](../slides/schema/doxacast.slides.v1.schema.json)
(or `doxacast-connector schema`), for validating in any language. Some rules
(unique and stable ids, dates in order) can't be expressed in JSON Schema;
`check-feed` covers those.

---

## Writing a sidecar connector (any language)

A sidecar is any HTTP service that returns the slide feed. Copy
[`examples/sidecar-python`](../examples/sidecar-python): it's ~150 lines of
standard-library Python with caching, ETags and an optional bearer token.

Rules that matter:

1. **Stable ids.** If ids change every fetch, the server re-downloads every image forever.
2. **Fail loudly.** If your CMS is down, return a 5xx. Don't return an empty
   feed: an empty feed means "no slides," and screens will go blank.
3. **Plain text** in `body`. Strip HTML and decode entities.
4. **Respond in under 5 seconds.** Cache CMS results (the example does).
5. **16:9 images, at least 1280×720.** The server resizes, but it can't add pixels.

Check it:

```sh
doxacast-connector check-feed http://localhost:8090/feed
```

`check-feed` validates the schema, fetches twice to make sure ids are stable,
downloads every image to confirm it's really an image and how big it is, and
times the response. It exits non-zero on errors, so you can use it in your own
CI. Add `-strict` to fail on warnings too, and `-json` for machine-readable output.

Then add a **JSON feed** source in the DoxaCast console pointing at your sidecar.

### Getting listed

Open a **"List my connector"** issue with your repo URL and a public sample
feed URL. A maintainer runs `check-feed` on it and adds you to
[`community-connectors.yaml`](../community-connectors.yaml).

*Coming soon:* a bot that runs `check-feed` on the issue and opens the listing
PR automatically, plus a nightly job that re-checks listed feeds and flags any
that break.

DoxaCast never runs sidecar code, so listing is quick. It's your code, your
credentials and your responsibility.

---

## Writing an in-tree connector (Go)

### 1. Propose it

Open a **"Connector proposal"** issue first: which CMS, API docs, auth type, and
who will maintain it. Sometimes the answer is "that CMS already has an RSS feed."

### 2. Scaffold it

```sh
go run ./cmd/doxacast-connector new planning-center
```

This creates `connectors/planningcenter/` with a working connector, a test that
runs the conformance kit, a sample fixture and a README. **It passes every check
before you change a line.** Search for `TODO`.

### 3. The interface

```go
type Connector interface {
    Info() registry.Info                                    // name, status, maintainer, config fields
    New(cfg registry.Config, deps registry.Deps) (registry.Source, error) // validate config; no I/O
}

type Source interface {
    Fetch(ctx context.Context) ([]slides.Slide, error)
}
```

- **`Info().Fields`** builds the console's "add source" form. `FieldSecret`
  values are write-only: they're never sent back to a browser or to screens.
  `FieldURL` hosts are automatically allowed for `deps.HTTP`.
- **`deps.HTTP`** is the only way to reach the network. It refuses any host
  that isn't one of the source's configured URLs (or listed in the built-in
  `extra_hosts` field), including after redirects.
- **`deps.Files`** is the only way to read files (folder-style connectors).
- **`deps.Now`** is the clock. Use it instead of `time.Now` so tests control time.
- **On upstream failure, return an error, never an empty list.**
- Image URLs must be absolute, unless your source implements `registry.Opener`.
- Register in `init()` and add a blank import to `connectors/all`.

### 4. Allowed imports

Connector code (not tests) **may not** import `os`, `os/exec`, `net` (but
`net/http` and `net/url` are fine), `unsafe`, `syscall`, `plugin`, cgo, or any
DoxaCast package except `connectors/registry` and `slides`. **Third-party
modules** need maintainer review (license, security, upkeep), so prefer the
standard library.

Why so strict? In-tree connectors run inside the server, next to every
source's API keys. The import rules make "quietly send a key somewhere" fail
CI instead of relying on a reviewer to notice.

### 5. Test it

```sh
make connector-check NAME=planningcenter
```

That's exactly what CI runs: gofmt, vet, `doxacast-connector lint` (imports and
required files), and your tests with the race detector. Your tests must call
`connectortest.Run`, which checks that your connector:

- has valid metadata and rejects configs missing required fields
- returns slides that pass the schema, with ids stable across fetches
- returns an error on HTTP 500, garbage and truncated responses
- gives up when the upstream hangs past the deadline
- can't reach hosts other than the configured ones

Fixtures are recorded API responses in `testdata/`. Tests never touch a real
CMS or real credentials. **Scrub fixtures**: no real names, emails, phone
numbers or keys. Church data is people data.

### 6. Open the PR

CI runs the checks above plus CodeQL and govulncheck. A maintainer reviews
every new connector and every new dependency. Once merged, you're listed as
the connector's maintainer in `CODEOWNERS`.

### Status and upkeep

| Status | Meaning |
|---|---|
| `official` | Maintained by DoxaCast maintainers |
| `community` | Maintained by its listed maintainer |
| `experimental` | New or incomplete; may change |

Connectors whose issues go unanswered for 90 days are marked
`needs-maintainer`. A maintainer may deprecate and eventually remove a
connector nobody looks after.

---

## Stability

DoxaCast is pre-1.0. The slide feed format (`doxacast.slides/v1`) is the
stable contract. The Go connector interface may still change before 1.0, and
any change will come with migration notes and updates to every in-tree connector.
