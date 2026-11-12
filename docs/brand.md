# DoxaBoard brand

How DoxaBoard itself looks. A church's own colors, fonts and logo go in a
[theme](theming.md) instead and never change these files.

## The mark

<img src="assets/doxaboard-mark.svg" width="96" alt="DoxaBoard mark">

A screen with light rising inside it. *Doxa* is Greek for glory, so the
light is the point: the screen is only the frame.

- Source: [`docs/assets/doxaboard-mark.svg`](assets/doxaboard-mark.svg). The
  same paths are inlined in the console (`internal/server/web/index.html`)
  and used in `favicon.svg`; change all three together.
- It is drawn in one color on a 32 unit grid with 2.4 unit strokes, so it
  stays legible at 16 px. Don't add gradients, outlines or a second color.
- In the console it takes the accent color. On its own (README, slides,
  print) use brand blue, or white or near-black when blue won't read.
- Keep clear space around it of at least a quarter of its width.

## The name

Written **Doxa**Board: one word, capital D and B, "Doxa" bold and "Board"
regular weight. Not "Doxa Board", "Doxaboard" or "DOXABOARD".

Tagline: *One live stream, every screen in the building.*

## Color

| Name | Light | Dark | Used for |
|---|---|---|---|
| Blue (accent) | `#1f5fbf` | `#6aa8ff` | The mark, buttons, the active tab, links |
| On blue | `#ffffff` | `#0c1420` | Text on a blue button |
| Background | `#f4f6f9` | `#10141a` | Page |
| Surface | `#ffffff` | `#181d25` | Cards, header |
| Ink (text) | `#171c24` | `#e6eaf0` | Body text |
| Muted | `#5f6875` | `#939cab` | Secondary text |
| Line | `#d8dee6` | `#2c3440` | Borders |
| OK / Warn / Bad | `#2f7d4a` `#b26a00` `#b3261e` | `#5cbf7d` `#e0a040` `#ef6b62` | Status only, never decoration |

Standalone blue, for the mark outside the console: `#2f74d6`. The favicon is
a `#2563c9` tile with a white mark.

The neutrals lean slightly blue on purpose, so they sit with the accent
instead of fighting it. Red is reserved for "live" and for errors.

Text slides rendered for screens use the dark palette: background `#10141a`,
title `#f2f5f9`, body `#c3cad6`, blue accent bar `#6aa8ff`.

## Type

The console uses the system UI font; text slides use the Go fonts built into
the server. Nothing is downloaded and nothing needs a license.
