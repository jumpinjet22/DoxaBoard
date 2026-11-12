# jsonfeed connector

Reads a URL that serves a [`doxacast.slides/v1`](../../slides/slides.go) feed.

This is also how you plug in a connector written in **any language**: run it
as a small HTTP service that returns the feed JSON, and add a `jsonfeed`
source pointing at it. DoxaCast does the rest (scheduling, caching, images,
screens).

- **Status:** official
- **Maintainer:** @jumpinjet22

## Config

| Key     | Type   | Required | Default | Description |
|---------|--------|----------|---------|-------------|
| `url`   | URL    | yes      |         | Feed URL. Its host is automatically allowed. |
| `token` | secret | no       |         | Sent as `Authorization: Bearer <token>`. |
| `extra_hosts` | text | no |         | Comma-separated extra hosts (e.g. an image CDN), accepted by every connector. |

## Example

```json
{"url": "http://rock-sidecar:8090/slides.json", "token": "change-me"}
```

The sidecar returns:

```json
{
  "schema": "doxacast.slides/v1",
  "slides": [
    {
      "id": "event-4411",
      "title": "Fall Festival",
      "body": "Sunday after the 10:30 service.",
      "image": {"url": "/media/fall-festival.jpg", "alt": "Children on a hay ride"},
      "expires_at": "2026-02-01T00:00:00Z",
      "tags": ["lobby"]
    }
  ]
}
```

## Behavior

- Sends `GET` with `Accept: application/json` (and the bearer token, if set).
- Non-2xx responses, invalid or truncated JSON, and feeds over 4 MiB are
  errors, so screens keep the last good slides.
- Feed-level problems (missing or unknown `schema`, more than 500 slides)
  reject the whole feed; the error lists every problem.
- Slide-level problems (no `id`, duplicate `id`, no title and no image, text
  over the limits, bad image scheme, ...) drop just that slide. Each problem
  is logged, warnings too, so sidecar authors can see exactly what to fix.
- Relative image URLs are resolved against the feed URL (after redirects).
  An image that doesn't resolve to an http(s) URL with a host is dropped
  (and the slide too, if it has no title).
- Expired slides are returned as-is; the server filters by time.

## Limitations

- Text is not cleaned: send plain text. HTML in `title`/`body` is shown
  literally (the validator warns about it).
- Images on other hosts must be listed in `extra_hosts` for the server to
  download them.
