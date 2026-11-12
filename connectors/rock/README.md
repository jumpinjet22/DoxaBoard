# Rock RMS connector

Slides from a [Rock RMS](https://www.rockrms.com) content channel for
DoxaBoard screens: each channel item becomes a slide, with its graphic and its
start and expire dates.

**Status:** experimental · **Maintainer:** @jumpinjet22

Experimental means it passes the conformance kit against recorded responses
but hasn't been run against a live Rock server yet. Please report what you find.

## Setup in Rock

1. **Pick or create a content channel** for screen graphics. Give its channel
   type an **Image** item attribute (field type *Image*) if it doesn't have
   one, and a date range if you want items to start and expire by themselves.
2. **Create a REST key:** Admin Tools → Security → REST Keys. The key acts as
   its own user.
3. **Grant that user View access** to the *ContentChannelItems* REST controller
   (Admin Tools → Security → REST Controllers) and to the content channel.
   It needs nothing else, and never needs Edit.
4. **Make sure the images are public.** DoxaBoard downloads graphics from
   `GetImage.ashx` without the key, the same way your website shows them. The
   binary file type used by the Image attribute must not require security.
5. **Find the channel ID:** open the channel in Rock; it's the
   `contentChannelId=` number in the address bar.

## Configuration

| Field | Required | Notes |
|---|---|---|
| Rock URL (`url`) | yes | e.g. `https://rock.example.org` |
| REST key (`api_key`) | yes | Stored encrypted; never shown again. Only ever sent to the Rock URL's host. |
| Content channel ID (`channel_id`) | yes | A number. |
| Rock's time zone (`time_zone`) | yes | IANA name, e.g. `America/Chicago`. Rock sends start and expire times in its own zone. |
| Image attribute key (`image_attribute`) | no | Default `Image`. |
| Only approved items (`approved_only`) | no | Default `true`. |
| Extra hosts (`extra_hosts`) | no | Comma-separated, if the image attribute holds URLs on another host (a CDN). |

## How content maps to slides

| Rock content channel item | Slide |
|---|---|
| `Id` | `id` (`rock-<Id>`) |
| `Title` | `title` |
| `Content` (HTML) | `body`, as plain text |
| Image attribute (file GUID, URL or path) | `image` |
| `StartDateTime` / `ExpireDateTime` | `starts_at` / `expires_at` |
| `Order`, then `Priority` (higher first), then start date | `order` |
| `ModifiedDateTime` | `updated_at` |

Items with an image show the image full screen. Items with only text are
rendered as a text slide. Screens add and drop slides at the start and expire
times by themselves, even if they can't reach the server at that moment.

## Limitations

- One content channel per source. Add a source per channel.
- Reads the 250 items with the newest start dates; already-expired items are
  skipped.
- Calendar events and service schedules aren't read yet (see ROADMAP.md).
- Secured image files aren't supported: graphics must be publicly readable.

## Development

```sh
go test ./connectors/rock/
make connector-check NAME=rock
```

Fixtures in `testdata/` are made-up API responses in Rock's shape. No real
names, emails, phone numbers or keys.
