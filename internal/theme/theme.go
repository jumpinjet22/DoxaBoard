// Package theme loads an instance's branding: colors and fonts for rendered
// text slides and for the admin console, plus a name and logo.
//
// A theme is a directory holding theme.json and the files it names (fonts,
// a logo). It belongs to one installation, not to DoxaBoard: fonts are
// usually licensed to the organization, so they stay out of the repository.
package theme

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jumpinjet22/doxaboard/internal/content"
)

// File is the theme's manifest inside the theme directory.
const File = "theme.json"

// maxAsset caps a font or logo file.
const maxAsset = 8 << 20

// Spec is the contents of theme.json. Every field is optional.
type Spec struct {
	// Name replaces "DoxaBoard" in the console header.
	Name string `json:"name"`
	// Logo is an image shown beside the name (png, jpg, webp or svg).
	Logo   string      `json:"logo"`
	Slides SlideSpec   `json:"slides"`
	Admin  ConsoleSpec `json:"console"`
}

// SlideSpec styles text slides rendered for screens. Colors are hex (#rrggbb).
type SlideSpec struct {
	Background string `json:"background"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	Accent     string `json:"accent"`
	// TitleFont and BodyFont are .ttf or .otf files in the theme directory.
	TitleFont string `json:"title_font"`
	BodyFont  string `json:"body_font"`
}

// ConsoleSpec styles the admin console. Light and Dark map the console's
// color names (bg, surface, text, muted, border, accent, accent-text, ok,
// warn, bad) to hex colors.
type ConsoleSpec struct {
	Light map[string]string `json:"light"`
	Dark  map[string]string `json:"dark"`
	// HeadingFont and BodyFont are .ttf, .otf, .woff or .woff2 files.
	HeadingFont string `json:"heading_font"`
	BodyFont    string `json:"body_font"`
}

// Theme is a loaded theme. The zero value (and nil) is the built-in look.
type Theme struct {
	Spec
	// Warnings are problems that were worked around, e.g. a missing font.
	Warnings []string

	dir    string
	assets map[string]bool // files the console may fetch
	style  content.TextStyle
	key    string
}

var (
	hexRE   = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)
	fileRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]*(?:/[A-Za-z0-9][A-Za-z0-9 ._-]*)*$`)
	cssVars = map[string]bool{"bg": true, "surface": true, "text": true, "muted": true, "border": true,
		"accent": true, "accent-text": true, "ok": true, "warn": true, "bad": true}
	fontExts = map[string]string{".ttf": "truetype", ".otf": "opentype", ".woff": "woff", ".woff2": "woff2"}
	logoExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".svg": true}
)

// Load reads the theme in dir. An empty dir, or one without theme.json,
// gives the built-in look. Anything wrong with a single setting (a bad
// color, a missing font) is skipped and reported in Warnings, so a typo in
// a theme never stops the server that every screen depends on.
func Load(dir string) (*Theme, error) {
	t := &Theme{dir: dir, assets: map[string]bool{}}
	if dir == "" {
		return t, nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, File))
	if errors.Is(err, fs.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return t, err
	}
	if err := json.Unmarshal(raw, &t.Spec); err != nil {
		t.Spec = Spec{}
		return t, fmt.Errorf("%s: %w", File, err)
	}
	t.Name = strings.TrimSpace(t.Name)
	if len(t.Name) > 80 {
		t.warn("name is longer than 80 characters; ignoring it")
		t.Name = ""
	}
	sum := sha256.New()
	sum.Write(raw)

	// Text slides.
	set := func(dst *color.Color, field, v string) {
		if v == "" {
			return
		}
		c, ok := parseHex(v)
		if !ok {
			t.warn("slides.%s: %q isn't a hex color like #758d87", field, v)
			return
		}
		*dst = c
	}
	set(&t.style.Background, "background", t.Slides.Background)
	set(&t.style.Title, "title", t.Slides.Title)
	set(&t.style.Body, "body", t.Slides.Body)
	set(&t.style.Accent, "accent", t.Slides.Accent)
	titleTTF := t.readFont("slides.title_font", t.Slides.TitleFont, false)
	bodyTTF := t.readFont("slides.body_font", t.Slides.BodyFont, false)
	sum.Write(titleTTF)
	sum.Write(bodyTTF)
	if titleTTF != nil || bodyTTF != nil {
		tf, bf, err := content.TextFaces(titleTTF, bodyTTF)
		if err != nil {
			t.warn("slide fonts can't be used, keeping the built-in ones: %v", err)
		} else {
			t.style.TitleFace, t.style.BodyFace = tf, bf
		}
	}

	// Console.
	for _, mode := range []map[string]string{t.Admin.Light, t.Admin.Dark} {
		for k, v := range mode {
			if !cssVars[k] || !hexRE.MatchString(v) {
				t.warn("console color %q: %q isn't a known name with a hex color", k, v)
				delete(mode, k)
			}
		}
	}
	if t.readFont("console.heading_font", t.Admin.HeadingFont, true) == nil {
		t.Admin.HeadingFont = ""
	}
	if t.readFont("console.body_font", t.Admin.BodyFont, true) == nil {
		t.Admin.BodyFont = ""
	}
	if t.Logo != "" {
		if !logoExts[strings.ToLower(path.Ext(t.Logo))] {
			t.warn("logo %q must be a png, jpg, webp or svg", t.Logo)
			t.Logo = ""
		} else if t.read("logo", t.Logo) == nil {
			t.Logo = ""
		} else {
			t.assets[t.Logo] = true
		}
	}
	t.key = hex.EncodeToString(sum.Sum(nil)[:6])
	return t, nil
}

func (t *Theme) warn(format string, args ...any) {
	t.Warnings = append(t.Warnings, fmt.Sprintf(format, args...))
}

// read returns a file named in theme.json, or nil with a warning.
func (t *Theme) read(field, name string) []byte {
	if name == "" {
		return nil
	}
	if !fileRE.MatchString(name) || strings.Contains(name, "..") {
		t.warn("%s: %q must be a plain file name inside the theme folder", field, name)
		return nil
	}
	root, err := os.OpenRoot(t.dir)
	if err != nil {
		t.warn("%s: %v", field, err)
		return nil
	}
	defer root.Close()
	f, err := root.Open(name)
	if err != nil {
		t.warn("%s: %s isn't in the theme folder yet; using the built-in look for it", field, name)
		return nil
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxAsset+1))
	if err != nil || len(b) == 0 || len(b) > maxAsset {
		t.warn("%s: %s is empty, unreadable or over %d MB", field, name, maxAsset>>20)
		return nil
	}
	return b
}

func (t *Theme) readFont(field, name string, web bool) []byte {
	if name == "" {
		return nil
	}
	ext := strings.ToLower(path.Ext(name))
	if _, ok := fontExts[ext]; !ok || (!web && ext != ".ttf" && ext != ".otf") {
		t.warn("%s: %q must be a .ttf or .otf font%s", field, name, map[bool]string{true: " (or .woff/.woff2)", false: ""}[web])
		return nil
	}
	b := t.read(field, name)
	if b != nil && web {
		t.assets[name] = true
	}
	return b
}

func parseHex(v string) (color.Color, bool) {
	if !hexRE.MatchString(v) {
		return nil, false
	}
	v = v[1:]
	if len(v) == 3 {
		v = string([]byte{v[0], v[0], v[1], v[1], v[2], v[2]})
	}
	b, err := hex.DecodeString(v)
	if err != nil || len(b) != 3 {
		return nil, false
	}
	return color.RGBA{R: b[0], G: b[1], B: b[2], A: 0xff}, true
}

// TextStyle is the style for rendered text slides. Unset parts fall back
// to DoxaBoard's defaults.
func (t *Theme) TextStyle() content.TextStyle {
	if t == nil {
		return content.TextStyle{}
	}
	return t.style
}

// Key changes whenever the theme does, so text slides rendered with an
// older theme are drawn again. It is empty for the built-in look.
func (t *Theme) Key() string {
	if t == nil {
		return ""
	}
	return t.key
}

// Asset opens a font or logo for the console. Only files named in
// theme.json can be fetched.
func (t *Theme) Asset(name string) (fs.File, error) {
	if t == nil || !t.assets[name] {
		return nil, fs.ErrNotExist
	}
	root, err := os.OpenRoot(t.dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Open(name)
}

// CSS is a stylesheet layered over the console's own. It is empty for the
// built-in look. Every value in it has been validated, so nothing from
// theme.json can inject markup or other rules.
func (t *Theme) CSS() string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	vars := func(m map[string]string) string {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var out strings.Builder
		for _, k := range keys {
			fmt.Fprintf(&out, "--%s:%s;", k, m[k])
		}
		return out.String()
	}
	face := func(family, file string) {
		if file == "" {
			return
		}
		fmt.Fprintf(&b, "@font-face{font-family:%q;src:url(%q) format(%q);font-display:swap}\n",
			family, "theme/"+file, fontExts[strings.ToLower(path.Ext(file))])
	}
	face("Theme Heading", t.Admin.HeadingFont)
	face("Theme Body", t.Admin.BodyFont)
	if len(t.Admin.Light) > 0 {
		fmt.Fprintf(&b, ":root{%s}\n", vars(t.Admin.Light))
	}
	if len(t.Admin.Dark) > 0 {
		// Mirrors the console's own rules: dark follows the system setting
		// unless the Appearance button forced a mode (data-mode on <html>).
		d := vars(t.Admin.Dark)
		fmt.Fprintf(&b, "@media (prefers-color-scheme: dark){:root:not([data-mode=light]){%s}}\n:root[data-mode=dark]{%s}\n", d, d)
	}
	const fallback = `system-ui,-apple-system,"Segoe UI",sans-serif`
	if t.Admin.BodyFont != "" {
		fmt.Fprintf(&b, "body,button,input,select{font-family:\"Theme Body\",%s}\n", fallback)
	}
	if t.Admin.HeadingFont != "" {
		fmt.Fprintf(&b, "h1,h2,h3,h4{font-family:\"Theme Heading\",%s}\n", fallback)
	}
	return b.String()
}
