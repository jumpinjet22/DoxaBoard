# Theming

A theme gives one installation its own look: brand colors and fonts on the
text slides screens show, and the same colors, fonts, name and logo in the
admin console. Slides that are images (your own graphics) are never changed.

A theme is a folder with a `theme.json` and the files it names. It belongs to
your installation, not to DoxaBoard, so it isn't in the repository. Brand
fonts are usually licensed to your organization: keep them in your theme
folder only.

## Setting one up

With Docker Compose, the folder is `deploy/server/theme/`:

```
deploy/server/theme/
  theme.json
  logo.png
  fonts/Heading-Bold.otf
  fonts/Body-Regular.ttf
```

Then `docker compose up -d` (or `docker compose restart server` after a
change). Without Docker, pass `-theme <folder>` or set `DOXABOARD_THEME_DIR`.

## theme.json

Everything is optional. Leave something out and DoxaBoard's built-in look is
used for it.

```json
{
  "name": "Example Church",
  "logo": "logo.png",
  "slides": {
    "background": "#242c2a",
    "title": "#f9fefe",
    "body": "#e5e9e8",
    "accent": "#91a49f",
    "title_font": "fonts/Heading-Bold.otf",
    "body_font": "fonts/Body-Regular.ttf"
  },
  "console": {
    "light": { "accent": "#52635f", "accent-text": "#f9fefe" },
    "dark": { "accent": "#91a49f", "accent-text": "#242c2a" },
    "heading_font": "fonts/Heading-Bold.otf",
    "body_font": "fonts/Body-Regular.ttf"
  }
}
```

| Setting | What it changes |
|---|---|
| `name` | The name in the console header and browser tab. |
| `logo` | An image beside the name: png, jpg, webp or svg. |
| `slides.background`, `title`, `body`, `accent` | Colors of text slides (slides without an image). Hex, e.g. `#758d87`. |
| `slides.title_font`, `body_font` | Fonts for text slides: `.ttf` or `.otf`. |
| `console.light`, `console.dark` | Console colors for light and dark mode. Names: `bg`, `surface`, `text`, `muted`, `border`, `accent`, `accent-text`, `ok`, `warn`, `bad`. |
| `console.heading_font`, `body_font` | Console fonts: `.ttf`, `.otf`, `.woff` or `.woff2`. |

Pick an `accent` that reads well with its `accent-text` on top: buttons use
the pair.

## What happens when something is wrong

A theme can't take the server down. A bad color, a font that isn't there or
a file that can't be read is skipped, the built-in look is used for that one
thing, and the server log says which setting it was (`theme: ...`). If
`theme.json` itself can't be parsed, the whole theme is ignored.

Changing the theme redraws existing text slides the next time each content
source refreshes (within 10 minutes, or press **Refresh now**).

Only the files named in `theme.json` are served to browsers; nothing else in
the folder is reachable.
