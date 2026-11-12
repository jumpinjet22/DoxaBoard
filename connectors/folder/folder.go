// Package folder shows the images in an album folder of the server's
// content directory (for example, a OneDrive folder synced by rclone).
//
// Each subfolder of the content directory is an album. Images are served by
// the source itself through registry.Opener, so slide image URLs are plain
// file names relative to the album.
package folder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jumpinjet22/doxacast/connectors/registry"
	"github.com/jumpinjet22/doxacast/slides"
)

func init() { registry.Register(Connector{}) }

// imageExts are the file types shown, matched case-insensitively.
var imageExts = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true}

// Connector is the "folder" connector.
type Connector struct{}

// Info implements registry.Connector.
func (Connector) Info() registry.Info {
	return registry.Info{
		Name:  "folder",
		Title: "Photo folder",
		Description: "Shows the photos in an album folder of the server's photo directory " +
			"(for example, a OneDrive folder synced by rclone).",
		Status:     registry.StatusOfficial,
		Maintainer: "@jumpinjet22",
		UsesFiles:  true,
		Fields: []registry.Field{
			{Key: "album", Label: "Album folder", Type: registry.FieldText, Required: true,
				Help: "The name of a folder directly inside the photo directory, e.g. \"Lobby\"."},
		},
	}
}

// New implements registry.Connector. It does no I/O.
func (c Connector) New(cfg registry.Config, deps registry.Deps) (registry.Source, error) {
	cfg, err := registry.ValidateConfig(c.Info(), cfg)
	if err != nil {
		return nil, err
	}
	album := cfg["album"]
	if err := checkName(album); err != nil {
		return nil, fmt.Errorf("Album folder %w", err)
	}
	logf := deps.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &source{album: album, files: deps.Files, logf: logf}, nil
}

// checkName accepts a single, plain path element: no separators, no
// traversal, no hidden names.
func checkName(name string) error {
	switch {
	case name == "":
		return errors.New("must not be empty")
	case strings.ContainsAny(name, `/\:`):
		return errors.New(`must be a single folder name (no "/", "\" or ":")`)
	case strings.Contains(name, ".."):
		return errors.New(`must not contain ".."`)
	case strings.HasPrefix(name, "."):
		return errors.New(`must not start with "."`)
	case !utf8.ValidString(name) || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return errors.New("contains invalid characters")
	case !fs.ValidPath(name):
		return errors.New("is not a valid name")
	}
	return nil
}

type source struct {
	album string
	files fs.FS
	logf  func(string, ...any)
}

// Fetch implements registry.Source.
func (s *source) Fetch(ctx context.Context) ([]slides.Slide, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.files == nil {
		return nil, errors.New("folder: the server has no photo directory configured")
	}
	entries, err := fs.ReadDir(s.files, s.album)
	if err != nil {
		return nil, fmt.Errorf("folder: album %q: %w", s.album, err)
	}
	var names []string
	modTimes := map[string]fs.FileInfo{}
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !isImageName(name) {
			continue // directories, symlinks, dotfiles, other file types
		}
		names = append(names, name)
		if info, err := e.Info(); err == nil {
			modTimes[name] = info
		}
	}
	sort.Strings(names)
	if len(names) > slides.MaxSlides {
		s.logf("folder: album %q has %d images; showing the first %d", s.album, len(names), slides.MaxSlides)
		names = names[:slides.MaxSlides]
	}

	out := make([]slides.Slide, 0, len(names))
	for i, name := range names {
		sl := slides.Slide{
			ID:    slideID(s.album, name),
			Image: &slides.Image{URL: url.PathEscape(name), Alt: altText(name)},
			Order: i * 10,
		}
		if info := modTimes[name]; info != nil && !info.ModTime().IsZero() {
			t := info.ModTime().UTC()
			sl.UpdatedAt = &t
		}
		out = append(out, sl)
	}
	return out, nil
}

// Open implements registry.Opener. imageURL is an Image.URL from Fetch: a
// URL-escaped file name inside the album.
func (s *source) Open(ctx context.Context, imageURL string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.files == nil {
		return nil, errors.New("folder: the server has no photo directory configured")
	}
	name, err := url.PathUnescape(imageURL)
	if err != nil {
		return nil, fmt.Errorf("folder: invalid image name %q: %w", imageURL, err)
	}
	if !isImageName(name) {
		return nil, fmt.Errorf("folder: %w: image name %q", fs.ErrPermission, imageURL)
	}
	f, err := s.files.Open(path.Join(s.album, name))
	if err != nil {
		return nil, fmt.Errorf("folder: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("folder: %w", err)
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("folder: %q is not a regular file", name)
	}
	return f, nil
}

// isImageName reports whether name is a single, visible path element with
// an image extension. With no separators, a name can't escape the album.
func isImageName(name string) bool {
	return name != "" &&
		!strings.HasPrefix(name, ".") &&
		!strings.ContainsAny(name, `/\:`) &&
		!strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) &&
		fs.ValidPath(name) &&
		imageExts[strings.ToLower(path.Ext(name))]
}

// slideID is stable for a file in an album. Overlong names are hashed to
// keep IDs within slides.MaxIDLen.
func slideID(album, name string) string {
	id := "folder:" + album + "/" + name
	if utf8.RuneCountInString(id) <= slides.MaxIDLen {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return "folder:" + hex.EncodeToString(sum[:])[:32]
}

// altText turns "easter-sunday_2025.jpg" into "easter sunday 2025".
func altText(name string) string {
	base := strings.TrimSuffix(name, path.Ext(name))
	base = strings.NewReplacer("-", " ", "_", " ").Replace(base)
	alt := strings.Join(strings.Fields(base), " ")
	if alt == "" {
		alt = name
	}
	if utf8.RuneCountInString(alt) > slides.MaxAltLen {
		r := []rune(alt)
		alt = string(r[:slides.MaxAltLen-1]) + "…"
	}
	return alt
}
