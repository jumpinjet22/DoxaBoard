package folder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jumpinjet22/doxaboard/connectors/connectortest"
	"github.com/jumpinjet22/doxaboard/connectors/registry"
	"github.com/jumpinjet22/doxaboard/slides"
)

var modTime = time.Date(2025, 12, 20, 18, 30, 0, 0, time.UTC)

// photos is a content directory the way rclone leaves it: albums as
// subfolders, with the odd non-image and hidden file mixed in.
func photos() fstest.MapFS {
	img := func(data string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte(data), Mode: 0o644, ModTime: modTime}
	}
	return fstest.MapFS{
		"Lobby/2025-12-24_christmas-eve.JPG": img("jpeg"),
		"Lobby/baptism sunday.jpeg":          img("jpeg"),
		"Lobby/choir-rehearsal.png":          img("png"),
		"Lobby/fall_festival-2025.webp":      img("webp"),
		"Lobby/welcome.gif":                  &fstest.MapFile{Data: []byte("gif")}, // no mod time
		"Lobby/100% joy.jpg":                 img("jpeg"),
		"Lobby/.hidden.jpg":                  img("jpeg"),
		"Lobby/._choir-rehearsal.png":        img("macOS resource fork"),
		"Lobby/notes.txt":                    img("not an image"),
		"Lobby/video.mp4":                    img("not an image"),
		"Lobby/Thumbs.db":                    img("windows junk"),
		"Lobby/nested/inside.jpg":            img("in a subfolder"),
		"Lobby/folder.jpg/real.jpg":          img("a directory named like an image"),
		"Kids/craft-table.jpg":               img("jpeg"),
		"Empty/.keep":                        img(""),
		"secret.jpg":                         img("outside every album"),
		"Lobby2/escape.jpg":                  img("sibling album"),
	}
}

var lobbyIDs = []string{
	"folder:Lobby/2025-12-24_christmas-eve.JPG",
	"folder:Lobby/baptism sunday.jpeg",
	"folder:Lobby/choir-rehearsal.png",
	"folder:Lobby/fall_festival-2025.webp",
	"folder:Lobby/welcome.gif",
	"folder:Lobby/100% joy.jpg",
}

func TestConformance(t *testing.T) {
	connectortest.Run(t, Connector{}, []connectortest.Fixture{
		{Name: "lobby", Config: registry.Config{"album": "Lobby"}, Files: photos(), WantIDs: lobbyIDs},
		{Name: "kids", Config: registry.Config{"album": "Kids"}, Files: photos(), WantIDs: []string{"folder:Kids/craft-table.jpg"}},
		{Name: "empty_album", Config: registry.Config{"album": "Empty"}, Files: photos(), WantIDs: []string{}},
		{Name: "missing_album", Config: registry.Config{"album": "Gone"}, Files: photos(), WantErr: true},
	})
}

func newSource(t *testing.T, album string, files fs.FS) *source {
	t.Helper()
	src, err := Connector{}.New(registry.Config{"album": album}, registry.Deps{
		Files: files,
		Logf:  t.Logf,
		Now:   func() time.Time { return connectortest.DefaultNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	return src.(*source)
}

func TestFetchMapping(t *testing.T) {
	got, err := newSource(t, "Lobby", photos()).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(lobbyIDs) {
		t.Fatalf("got %d slides, want %d", len(got), len(lobbyIDs))
	}
	// Sorted by file name (byte order).
	wantNames := []string{"100%25%20joy.jpg", "2025-12-24_christmas-eve.JPG", "baptism%20sunday.jpeg",
		"choir-rehearsal.png", "fall_festival-2025.webp", "welcome.gif"}
	wantAlts := []string{"100% joy", "2025 12 24 christmas eve", "baptism sunday",
		"choir rehearsal", "fall festival 2025", "welcome"}
	for i, s := range got {
		if s.Image == nil || s.Image.URL != wantNames[i] || s.Image.Alt != wantAlts[i] {
			t.Errorf("slide %d: %+v", i, s.Image)
		}
		if s.Order != i*10 || s.Title != "" || s.Body != "" {
			t.Errorf("slide %d: order %d title %q body %q", i, s.Order, s.Title, s.Body)
		}
	}
	if got[0].UpdatedAt == nil || !got[0].UpdatedAt.Equal(modTime) {
		t.Errorf("updated_at = %v, want %v", got[0].UpdatedAt, modTime)
	}
	if got[5].UpdatedAt != nil {
		t.Errorf("zero mod time should leave updated_at nil, got %v", got[5].UpdatedAt)
	}
	if ps := slides.Validate(slides.Feed{Schema: slides.SchemaV1, Slides: got}); ps.HasErrors() || len(ps) > 0 {
		t.Errorf("problems: %v", ps)
	}
}

func TestOpen(t *testing.T) {
	src := newSource(t, "Lobby", photos())
	ctx := context.Background()
	for url, want := range map[string]string{
		"choir-rehearsal.png":   "png",
		"baptism%20sunday.jpeg": "jpeg",
		"baptism sunday.jpeg":   "jpeg", // unescaped names work too
		"100%25%20joy.jpg":      "jpeg",
	} {
		rc, err := src.Open(ctx, url)
		if err != nil {
			t.Errorf("Open(%q): %v", url, err)
			continue
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		if string(b) != want {
			t.Errorf("Open(%q) = %q, want %q", url, b, want)
		}
	}
}

func TestOpenRejectsTraversal(t *testing.T) {
	src := newSource(t, "Lobby", photos())
	ctx := context.Background()
	for _, url := range []string{
		"../secret.jpg",
		"..%2Fsecret.jpg",
		"%2E%2E/secret.jpg",
		"../Lobby2/escape.jpg",
		"/secret.jpg",
		"nested/inside.jpg",
		`..\secret.jpg`,
		"..",
		".",
		"",
		".hidden.jpg",
		"notes.txt",
		"video.mp4",
		"Thumbs.db",
		"folder.jpg", // a directory
		"C:secret.jpg",
		"https://example.org/x.jpg",
		"choir-rehearsal.png\x00.jpg",
		"%zz.jpg",
	} {
		rc, err := src.Open(ctx, url)
		if err == nil {
			rc.Close()
			t.Errorf("Open(%q) should be rejected", url)
		}
	}
}

func TestOpenMissingFile(t *testing.T) {
	_, err := newSource(t, "Lobby", photos()).Open(context.Background(), "nope.jpg")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("want fs.ErrNotExist, got %v", err)
	}
}

func TestAlbumValidation(t *testing.T) {
	for _, album := range []string{"", "  ", "a/b", `a\b`, "..", "../photos", "a..b", ".hidden", ".", "/abs", "C:", "bad\x00name"} {
		if _, err := (Connector{}).New(registry.Config{"album": album}, registry.Deps{Files: photos()}); err == nil {
			t.Errorf("album %q should be rejected", album)
		}
	}
	for _, album := range []string{"Lobby", "Kids Ministry", "2025 Christmas", "Café"} {
		if _, err := (Connector{}).New(registry.Config{"album": album}, registry.Deps{Files: photos()}); err != nil {
			t.Errorf("album %q should be accepted: %v", album, err)
		}
	}
}

func TestMissingAlbumAndNoFiles(t *testing.T) {
	if _, err := newSource(t, "Gone", photos()).Fetch(context.Background()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing album: want fs.ErrNotExist, got %v", err)
	}
	if _, err := newSource(t, "Lobby", nil).Fetch(context.Background()); err == nil {
		t.Error("nil Files should be an error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newSource(t, "Lobby", photos()).Fetch(ctx); err == nil {
		t.Error("cancelled context should be an error")
	}
}

func TestCapsAtMaxSlides(t *testing.T) {
	files := fstest.MapFS{}
	for i := range slides.MaxSlides + 20 {
		files[fmt.Sprintf("Big/%04d.jpg", i)] = &fstest.MapFile{Data: []byte("x")}
	}
	got, err := newSource(t, "Big", files).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != slides.MaxSlides {
		t.Errorf("got %d slides, want %d", len(got), slides.MaxSlides)
	}
}

func TestLongNames(t *testing.T) {
	name := strings.Repeat("very-long-name-", 30) + ".jpg"
	files := fstest.MapFS{"A/" + name: &fstest.MapFile{Data: []byte("x")}}
	src := newSource(t, "A", files)
	got, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ps := slides.Validate(slides.Feed{Schema: slides.SchemaV1, Slides: got}); ps.HasErrors() {
		t.Errorf("problems: %v", ps)
	}
	if again, _ := src.Fetch(context.Background()); again[0].ID != got[0].ID {
		t.Error("hashed ID not stable")
	}
	rc, err := src.Open(context.Background(), got[0].Image.URL)
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
}
