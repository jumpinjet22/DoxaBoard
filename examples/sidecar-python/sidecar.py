#!/usr/bin/env python3
"""A DoxaBoard sidecar connector in plain Python (standard library only).

It serves a doxaboard.slides/v1 feed at http://<host>:8090/feed. In DoxaBoard,
add a "JSON feed" source pointing at that URL.

Copy this file, replace fetch_items() with calls to your CMS, and keep
to_slide() returning the same shape. Check your work with:

    doxaboard-connector check-feed http://localhost:8090/feed
"""

import hashlib
import json
import os
import time
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(os.environ.get("PORT", "8090"))
# Optional: require DoxaBoard to send "Authorization: Bearer <token>".
TOKEN = os.environ.get("FEED_TOKEN", "")
# Cache upstream results so DoxaBoard polling doesn't hammer your CMS.
CACHE_SECONDS = int(os.environ.get("CACHE_SECONDS", "60"))


def fetch_items():
    """Replace this with your CMS. Raise an exception if the CMS fails:
    the sidecar then returns HTTP 502 and DoxaBoard keeps its last good slides
    instead of blanking every screen."""
    return [
        {
            "key": "welcome",
            "title": "Welcome!",
            "summary": "Coffee and donuts after every service.",
            "image": "https://picsum.photos/id/1060/1920/1080.jpg",
            "start": None,
            "end": None,
        },
        {
            "key": "fall-festival-2026",
            "title": "Fall Festival",
            "summary": "Oct 26 · 4-7pm · Family Life Center",
            "image": None,
            "start": "2026-10-01T00:00:00Z",
            "end": "2026-10-27T00:00:00Z",
        },
    ]


def to_slide(item, index):
    slide = {
        # IDs must be stable: the same content gets the same id on every fetch.
        "id": "mycms:" + str(item["key"]),
        "title": (item.get("title") or "")[:200],
        # Plain text only. Strip HTML from your CMS before it gets here.
        "body": (item.get("summary") or "")[:1000],
        "order": index * 10,
    }
    if item.get("image"):
        slide["image"] = {"url": item["image"], "alt": slide["title"]}
    if item.get("start"):
        slide["starts_at"] = item["start"]
    if item.get("end"):
        slide["expires_at"] = item["end"]
    return slide


_cache = {"at": 0.0, "body": None}


def build_feed():
    now = time.time()
    if _cache["body"] is not None and now - _cache["at"] < CACHE_SECONDS:
        return _cache["body"]
    items = fetch_items()
    feed = {
        "schema": "doxaboard.slides/v1",
        "slides": [to_slide(it, i) for i, it in enumerate(items)],
    }
    body = json.dumps(feed).encode("utf-8")
    _cache.update(at=now, body=body)
    return body


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/healthz":
            return self._send(200, b"ok", "text/plain")
        if self.path != "/feed":
            return self._send(404, b'{"error":"not found"}')
        if TOKEN and self.headers.get("Authorization") != "Bearer " + TOKEN:
            return self._send(401, b'{"error":"unauthorized"}')
        try:
            body = build_feed()
        except Exception as e:  # upstream CMS failed
            self.log_error("fetch failed: %s", e)
            return self._send(502, json.dumps({"error": "upstream failed"}).encode())
        etag = '"' + hashlib.sha256(body).hexdigest()[:16] + '"'
        if self.headers.get("If-None-Match") == etag:
            return self._send(304, b"", extra={"ETag": etag})
        self._send(200, body, extra={"ETag": etag, "Cache-Control": "no-cache"})

    def _send(self, code, body, ctype="application/json", extra=None):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        for k, v in (extra or {}).items():
            self.send_header(k, v)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if body:
            self.wfile.write(body)

    def log_message(self, fmt, *args):
        stamp = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
        print(f"{stamp} {self.address_string()} {fmt % args}", flush=True)


if __name__ == "__main__":
    print(f"DoxaBoard sidecar on :{PORT} (GET /feed)", flush=True)
    ThreadingHTTPServer(("0.0.0.0", PORT), Handler).serve_forever()
