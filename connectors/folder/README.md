# folder connector

Shows the photos in an album folder of the server's photo directory. Each
subfolder is an album, for example a OneDrive folder synced by rclone:

```
photos/
  Lobby/
    2025-12-24_christmas-eve.jpg
    baptism-sunday.png
  Kids/
    craft-table.jpg
```

- **Status:** official
- **Maintainer:** @jumpinjet22
- Reads files only (no network).

## Config

| Key     | Type | Required | Description |
|---------|------|----------|-------------|
| `album` | text | yes      | The album folder's name, directly inside the photo directory, e.g. `Lobby`. No `/`, `\`, `:`, `..`, or leading `.`. |

## Example

```json
{"album": "Lobby"}
```

## Behavior

- Lists `.jpg`, `.jpeg`, `.png`, `.webp` and `.gif` files (any case), sorted
  by file name. Hidden files (`.something`, including macOS `._` files),
  subfolders, symlinks and other file types are ignored.
- One image-only slide per file:
  - `id`: `folder:<album>/<file name>` (stable as long as the file keeps its name)
  - `image.url`: the URL-escaped file name, relative to the album
  - `image.alt`: the file name without its extension, `-` and `_` as spaces
    (`fall_festival-2025.webp` → "fall festival 2025")
  - `order`: position × 10; `updated_at`: the file's modification time
- A missing album folder is an error (screens keep the last good slides). An
  empty album is not: it gives no slides.
- Images are served by the source itself (`registry.Opener`): only plain file
  names with an image extension inside the album can be opened, never paths.

## Limitations

- At most 500 images per album (the first 500 by name).
- No titles or captions; ordering beyond file name and shuffling are up to
  the playlist profile. Rename files (e.g. with a number prefix) to reorder.
- Subfolders of an album are not included.
