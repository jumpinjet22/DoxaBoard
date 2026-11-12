# rss connector

Shows the latest items of an **RSS 2.0** or **Atom 1.0** feed: a WordPress
blog, a news site, a podcast, a YouTube channel.

- **Status:** official
- **Maintainer:** @jumpinjet22

## Config

| Key             | Type   | Required | Default | Description |
|-----------------|--------|----------|---------|-------------|
| `url`           | URL    | yes      |         | Feed URL, e.g. `https://example.org/feed/`. Its host is automatically allowed. |
| `max_items`     | number | no       | `20`    | Show at most this many items (1-500), in feed order. |
| `max_age_days`  | number | no       | `0`     | When > 0, each slide expires this many days after the item was published; older items are skipped. `0` = no limit. |
| `require_image` | bool   | no       | `false` | Skip items without an image. |
| `extra_hosts`   | text   | no       |         | Comma-separated extra hosts, e.g. the CDN your images are served from. |

## Example

```json
{"url": "https://gracecommunity.example.org/feed/", "max_items": "10", "max_age_days": "30", "require_image": "true",
 "extra_hosts": "i0.wp.com"}
```

## Mapping

| Slide        | RSS 2.0 | Atom 1.0 |
|--------------|---------|----------|
| `id`         | `guid`, else `link`, else a hash of title + date | `id`, else `link`, else a hash |
| `title`      | `title` | `title` (text, html or xhtml) |
| `body`       | `description`, else `content:encoded` | `summary`, else `content` |
| `image`      | see below | see below |
| `image.alt`  | the title (the body for untitled items) | same |
| `updated_at` | `atom:updated`, else `pubDate`/`dc:date` | `updated`, else `published` |
| `expires_at` | published + `max_age_days` | same |
| `order`      | feed position × 10 | same |

The image is the first usable one of: `media:content` that is an image
(`medium="image"` or an `image/*` type, also inside `media:group`),
`media:thumbnail`, an `enclosure` (or Atom `link rel="enclosure"`) with an
`image/*` type, `itunes:image`, then the first `<img>` in the HTML content and
then the description. Relative URLs resolve against the item's link, then the
feed URL. Only http(s) images are used; 1×1 tracking pixels are skipped and
lazy-loading `data-src` is understood.

Text is made plain: tags are stripped (and `<script>`/`<style>` contents),
entities decoded, whitespace collapsed, and titles/bodies are cut to 200/1000
characters with "…". Items with neither a title nor an image are skipped, and
duplicate IDs keep their first occurrence.

Feeds in UTF-8, ISO-8859-1 and windows-1252 are supported. Upstream errors,
malformed or truncated XML, and anything that isn't an `<rss>` or `<feed>`
document (an HTML error page, say) fail the fetch, so screens keep the last
good slides. A valid feed with no items gives no slides.

## Limitations

- RSS 1.0 (RDF) and JSON Feed are not supported.
- `xml:base` is ignored.
- Dates in unusual formats are ignored (the item is still shown, without
  `updated_at` or `expires_at`).
- Images on hosts other than the feed's need `extra_hosts` for the server to
  download them.
