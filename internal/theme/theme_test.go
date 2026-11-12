package theme

import (
	"image/color"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/font/gofont/gobold"

	"github.com/jumpinjet22/doxaboard/internal/content"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNoThemeIsBuiltInLook(t *testing.T) {
	for _, dir := range []string{"", t.TempDir()} {
		th, err := Load(dir)
		if err != nil || th.Key() != "" || th.CSS() != "" || len(th.Warnings) != 0 {
			t.Errorf("dir %q: key %q css %q warnings %v err %v", dir, th.Key(), th.CSS(), th.Warnings, err)
		}
		if _, err := content.RenderText("Title", "Body", th.TextStyle()); err != nil {
			t.Error(err)
		}
	}
	var nilTheme *Theme
	if nilTheme.CSS() != "" || nilTheme.Key() != "" {
		t.Error("a nil theme should be the built-in look")
	}
	if _, err := nilTheme.Asset("x"); err == nil {
		t.Error("a nil theme has no assets")
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "fonts/title.ttf", string(gobold.TTF))
	write(t, dir, "logo.svg", `<svg xmlns="http://www.w3.org/2000/svg"/>`)
	write(t, dir, "secret.txt", "not part of the theme")
	write(t, dir, File, `{
		"name": " Gloria Dei ",
		"logo": "logo.svg",
		"slides": {"background": "#242c2a", "title": "#fff", "accent": "sage",
			"title_font": "fonts/title.ttf", "body_font": "fonts/missing.ttf"},
		"console": {
			"light": {"accent": "#52635f", "bg": "red; } body { display:none", "nope": "#000000"},
			"dark": {"accent": "#91a49f"},
			"heading_font": "fonts/title.ttf", "body_font": "../etc/passwd"}
	}`)
	th, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if th.Name != "Gloria Dei" || th.Key() == "" {
		t.Errorf("name %q key %q", th.Name, th.Key())
	}

	st := th.TextStyle()
	if st.Background != (color.RGBA{0x24, 0x2c, 0x2a, 0xff}) || st.Title != (color.RGBA{0xff, 0xff, 0xff, 0xff}) {
		t.Errorf("colors: %v %v", st.Background, st.Title)
	}
	if st.Accent != nil || st.Body != nil {
		t.Errorf("bad and unset colors should fall back: %v %v", st.Accent, st.Body)
	}
	if st.TitleFace == nil || st.BodyFace == nil {
		t.Error("the title font should load, with the built-in body font beside it")
	}
	if _, err := content.RenderText("Title", "Body", st); err != nil {
		t.Error(err)
	}

	css := th.CSS()
	for _, want := range []string{`--accent:#52635f;`, `prefers-color-scheme: dark){:root:not([data-mode=light]){--accent:#91a49f;}`, `:root[data-mode=dark]{--accent:#91a49f;}`,
		`url("theme/fonts/title.ttf") format("truetype")`, `h1,h2,h3,h4{font-family:"Theme Heading"`} {
		if !strings.Contains(css, want) {
			t.Errorf("CSS is missing %q:\n%s", want, css)
		}
	}
	for _, bad := range []string{"display:none", "nope", "passwd", "Theme Body"} {
		if strings.Contains(css, bad) {
			t.Errorf("CSS should not contain %q:\n%s", bad, css)
		}
	}

	warnings := strings.Join(th.Warnings, "\n")
	for _, want := range []string{"slides.accent", "missing.ttf", `"bg"`, `"nope"`, "console.body_font"} {
		if !strings.Contains(warnings, want) {
			t.Errorf("warnings should mention %s:\n%s", want, warnings)
		}
	}

	// Only files named in theme.json can be fetched.
	for name, ok := range map[string]bool{"logo.svg": true, "fonts/title.ttf": true,
		"secret.txt": false, File: false, "../theme.json": false, "fonts/missing.ttf": false} {
		f, err := th.Asset(name)
		if (err == nil) != ok {
			t.Errorf("Asset(%q): err %v, want ok=%v", name, err, ok)
		}
		if err == nil {
			if b, _ := io.ReadAll(f); len(b) == 0 {
				t.Errorf("Asset(%q) is empty", name)
			}
			f.Close()
		}
	}

	// Editing the theme changes the key, so text slides are drawn again.
	write(t, dir, File, `{"slides": {"background": "#000000"}}`)
	if again, _ := Load(dir); again.Key() == th.Key() || again.Key() == "" {
		t.Errorf("key should change with the theme: %q then %q", th.Key(), again.Key())
	}
}

func TestBrokenJSON(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, File, `{"name": `)
	th, err := Load(dir)
	if err == nil || th == nil || th.CSS() != "" || th.Key() != "" {
		t.Errorf("broken theme.json should give an error and the built-in look: %v", err)
	}
}
