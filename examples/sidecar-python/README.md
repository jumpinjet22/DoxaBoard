# Python sidecar connector example

A complete DoxaCast connector in ~150 lines of standard-library Python. Use it
as a template for connecting any CMS DoxaCast doesn't support yet, in any
language: all a sidecar has to do is serve the
[slide feed](../../docs/connectors.md#the-slide-feed) over HTTP.

## Run it

```sh
python3 sidecar.py                     # or: docker build -t my-sidecar . && docker run -p 8090:8090 my-sidecar
doxacast-connector check-feed http://localhost:8090/feed
```

Then in the DoxaCast console add a **JSON feed** source with URL
`http://<sidecar-host>:8090/feed`.

## Make it yours

1. Replace `fetch_items()` with calls to your CMS.
2. Keep IDs **stable**: same content, same `id`, every time.
3. Send **plain text** in `body` (strip HTML).
4. If your CMS fails, **raise**. The sidecar returns 502 and DoxaCast keeps the
   last good slides instead of blanking every screen.
5. Optional: set `FEED_TOKEN` and give the same token to the DoxaCast source.

## Settings

| Env var | Default | |
|---|---|---|
| `PORT` | `8090` | |
| `FEED_TOKEN` | *(none)* | Require `Authorization: Bearer <token>` |
| `CACHE_SECONDS` | `60` | How long to reuse CMS results |

## Get listed

Once `check-feed` passes, open a **"List my connector"** issue on DoxaCast
with your repo and a sample feed URL.
