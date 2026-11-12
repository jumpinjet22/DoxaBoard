package content

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	_ "github.com/jumpinjet22/doxacast/connectors/all"
	"github.com/jumpinjet22/doxacast/connectors/registry"
	"github.com/jumpinjet22/doxacast/slides"
	"golang.org/x/image/font"
)

func pngOf(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x += 7 {
		img.Set(x, x%h, color.RGBA{200, 50, 50, 255})
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func decodeSize(t *testing.T, jpg []byte) (int, int) {
	t.Helper()
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(jpg))
	if err != nil {
		t.Fatalf("output is not a JPEG: %v", err)
	}
	return cfg.Width, cfg.Height
}

func TestProcessImageScalesDownKeepsAspect(t *testing.T) {
	out, err := ProcessImage(bytes.NewReader(pngOf(2880, 2160)))
	if err != nil {
		t.Fatal(err)
	}
	if w, h := decodeSize(t, out); w != 1440 || h != 1080 {
		t.Fatalf("got %dx%d, want 1440x1080", w, h)
	}
	// Small images are not upscaled.
	out, err = ProcessImage(bytes.NewReader(pngOf(800, 600)))
	if err != nil {
		t.Fatal(err)
	}
	if w, h := decodeSize(t, out); w != 800 || h != 600 {
		t.Fatalf("got %dx%d, want 800x600", w, h)
	}
}

func TestProcessImageRejectsJunkAndBombs(t *testing.T) {
	if _, err := ProcessImage(strings.NewReader("<html>not an image</html>")); err == nil {
		t.Fatal("HTML should be rejected")
	}
	// A valid PNG header claiming 100000x100000 pixels: rejected before decoding.
	var b bytes.Buffer
	b.Write([]byte("\x89PNG\r\n\x1a\n"))
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 100000)
	binary.BigEndian.PutUint32(ihdr[4:], 100000)
	ihdr[8], ihdr[9] = 8, 2
	binary.Write(&b, binary.BigEndian, uint32(13))
	chunk := append([]byte("IHDR"), ihdr...)
	b.Write(chunk)
	binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	if _, err := ProcessImage(&b); err == nil || !strings.Contains(err.Error(), "megapixels") {
		t.Fatalf("decompression bomb should be rejected, got %v", err)
	}
}

func TestRenderText(t *testing.T) {
	long := strings.Repeat("Coffee and donuts after every service. ", 40)
	out, err := RenderText("Welcome Sunday: a very long title that keeps going and going past the edge", long, DefaultTextStyle)
	if err != nil {
		t.Fatal(err)
	}
	if w, h := decodeSize(t, out); w != ScreenW || h != ScreenH {
		t.Fatalf("got %dx%d", w, h)
	}
	if _, err := RenderText("", "", DefaultTextStyle); err != nil {
		t.Fatal(err)
	}
}

func TestWrapBreaksLongWords(t *testing.T) {
	url := "https://example.org/" + strings.Repeat("abcdefghij", 40)
	lines := wrap(bodyFace, "see "+url+" now", 600)
	for _, l := range lines {
		if font.MeasureString(bodyFace, l).Ceil() > 600 {
			t.Fatalf("line too wide: %q", l)
		}
	}
	if strings.Join(lines, "") != "see"+url+"now" && !strings.Contains(strings.Join(lines, ""), "abcdefghij") {
		t.Fatalf("text lost: %v", lines)
	}
}

func TestSealer(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "secret.key")
	key, err := LoadOrCreateKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(keyPath); fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode %v", fi.Mode().Perm())
	}
	again, err := LoadOrCreateKey(keyPath)
	if err != nil || !bytes.Equal(key, again) {
		t.Fatal("key not reloaded")
	}
	s, _ := NewSealer(key)
	sealed, err := s.Seal("rock-api-key")
	if err != nil || !strings.HasPrefix(sealed, sealedPrefix) || strings.Contains(sealed, "rock-api-key") {
		t.Fatalf("bad seal %q %v", sealed, err)
	}
	if plain, err := s.Open(sealed); err != nil || plain != "rock-api-key" {
		t.Fatalf("open: %q %v", plain, err)
	}
	other, _ := NewSealer(make([]byte, 32))
	if _, err := other.Open(sealed); err == nil {
		t.Fatal("wrong key should fail")
	}
	if v, _ := s.Seal(""); v != "" {
		t.Fatal("empty should stay empty")
	}
}

// TestRefreshJSONFeed runs a real connector end to end: image slides, a
// text-only slide, an image that fails (falls back to text), a bad slide,
// then an upstream outage that must keep the last good slides.
func TestRefreshJSONFeed(t *testing.T) {
	var down atomic.Bool
	img := pngOf(1200, 675)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		switch r.URL.Path {
		case "/feed":
			w.Write([]byte(`{"schema":"doxacast.slides/v1","slides":[
			  {"id":"img","title":"Fall Festival","image":{"url":"/img.png","alt":"x"},"order":20},
			  {"id":"text","title":"Small Groups","body":"Find one that fits.","order":10},
			  {"id":"broken","title":"Broken image","image":{"url":"/missing.png","alt":"x"},"order":30},
			  {"id":"","title":"no id"}
			]}`))
		case "/img.png":
			w.Write(img)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	m := &Manager{Dir: t.TempDir(), Style: DefaultTextStyle}
	src := Source{ID: "s_test", Name: "Feed", Connector: "jsonfeed", Config: registry.Config{"url": srv.URL + "/feed"}}

	st := m.Refresh(context.Background(), src)
	// The slide with no id is dropped by the jsonfeed connector itself.
	if !st.OK || st.Slides != 3 {
		t.Fatalf("status %+v", st)
	}
	got, err := m.Slides(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != "text" || got[1].ID != "img" || got[2].ID != "broken" {
		t.Fatalf("slides %+v", got)
	}
	for _, s := range got {
		f, err := m.OpenFile(src.ID, s.File)
		if err != nil {
			t.Fatalf("%s: %v", s.ID, err)
		}
		b, _ := os.ReadFile(f.Name())
		f.Close()
		if w, _ := decodeSize(t, b); w == 0 {
			t.Fatal("empty image")
		}
	}

	// Upstream down: error status, but the slides stay.
	down.Store(true)
	st = m.Refresh(context.Background(), src)
	if st.OK || st.Error == "" || st.Slides != 3 {
		t.Fatalf("outage status %+v", st)
	}
	if again, _ := m.Slides(src.ID); len(again) != 3 {
		t.Fatalf("outage lost slides: %d", len(again))
	}
	if m.Status(src.ID).OK {
		t.Fatal("persisted status should show the failure")
	}
}

func TestRefreshFolderUsesOpener(t *testing.T) {
	files := fstest.MapFS{
		"lobby/sunrise.png": {Data: pngOf(2000, 1125), ModTime: time.Unix(1, 0)},
		"lobby/notes.txt":   {Data: []byte("x")},
	}
	m := &Manager{Dir: t.TempDir(), Files: files}
	src := Source{ID: "s_f", Connector: "folder", Config: registry.Config{"album": "lobby"}}
	st := m.Refresh(context.Background(), src)
	if !st.OK || st.Slides != 1 {
		t.Fatalf("status %+v", st)
	}
	got, _ := m.Slides(src.ID)
	f, err := m.OpenFile(src.ID, got[0].File)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(f.Name())
	f.Close()
	if w, h := decodeSize(t, b); w != 1920 || h != 1080 {
		t.Fatalf("got %dx%d", w, h)
	}
}

func TestOpenFileRejectsTraversal(t *testing.T) {
	m := &Manager{Dir: t.TempDir()}
	for _, n := range []string{"../x.jpg", "a/b.jpg", ".hidden.jpg", "slides.json", "status.json"} {
		if _, err := m.OpenFile("s", n); err == nil {
			t.Errorf("%q should be rejected", n)
		}
	}
}

func TestRenderTextZeroStyleUsesDefaults(t *testing.T) {
	if _, err := RenderText("Title", "Body", TextStyle{}); err != nil {
		t.Fatal(err)
	}
}

type panicConnector struct{}

func (panicConnector) Info() registry.Info {
	return registry.Info{Name: "zz-panic", Title: "Panic", Description: "panics", Status: registry.StatusExperimental, Maintainer: "@x"}
}
func (panicConnector) New(registry.Config, registry.Deps) (registry.Source, error) {
	return panicSource{}, nil
}

type panicSource struct{}

func (panicSource) Fetch(context.Context) ([]slides.Slide, error) { panic("boom") }

func TestRefreshRecoversFromConnectorPanic(t *testing.T) {
	registry.Register(panicConnector{})
	m := &Manager{Dir: t.TempDir()}
	st := m.Refresh(context.Background(), Source{ID: "s_p", Connector: "zz-panic"})
	if st.OK || !strings.Contains(st.Error, "boom") {
		t.Fatalf("status %+v", st)
	}
	if m.Status("s_p").OK {
		t.Fatal("failure should be persisted")
	}
}
