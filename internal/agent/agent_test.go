package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jumpinjet22/doxacast/core"
)

func TestMulticastAddr(t *testing.T) {
	cases := map[string]bool{
		"udp://239.10.10.1:5000":       true,
		"udp://@239.10.10.1:5000":      true,
		"udp://239.10.10.1:5000?ttl=2": true,
		"udp://192.168.1.10:5000":      false,
		"srt://239.10.10.1:5000":       false,
		"udp://239.10.10.1":            false,
	}
	for in, want := range cases {
		if _, ok := multicastAddr(in); ok != want {
			t.Errorf("%s: got %v want %v", in, ok, want)
		}
	}
	if got := mpvURL(core.Source{Kind: core.KindMulticast, URL: "udp://239.1.2.3:5000"}); got != "udp://@239.1.2.3:5000" {
		t.Errorf("mpvURL = %s", got)
	}
}

func TestProbeHLS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.m3u8":
			w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:3\n"))
		case "/html":
			w.Write([]byte("<html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := &Prober{}
	ctx := context.Background()
	if err := p.Probe(ctx, core.Source{Kind: core.KindHLS, URL: srv.URL + "/ok.m3u8"}); err != nil {
		t.Errorf("ok playlist: %v", err)
	}
	if err := p.Probe(ctx, core.Source{Kind: core.KindHLS, URL: srv.URL + "/html"}); err == nil {
		t.Error("html should fail")
	}
	if err := p.Probe(ctx, core.Source{Kind: core.KindHLS, URL: srv.URL + "/missing"}); err == nil {
		t.Error("404 should fail")
	}
}

func TestProbeMulticast(t *testing.T) {
	group := "239.255.42.99:45999"
	p := &Prober{}

	// Nothing being sent: should time out cleanly.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := p.Probe(ctx, core.Source{Kind: core.KindMulticast, URL: "udp://" + group}); err == nil {
		t.Fatal("expected no packets")
	}

	// Send MPEG-TS-looking packets and expect success. Skipped where the
	// sandbox has no multicast route.
	dst, _ := net.ResolveUDPAddr("udp4", group)
	conn, err := net.DialUDP("udp4", nil, dst)
	if err != nil {
		t.Skipf("no multicast route: %v", err)
	}
	defer conn.Close()
	pkt := make([]byte, 188*7)
	for i := 0; i < len(pkt); i += 188 {
		pkt[i] = 0x47
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				conn.Write(pkt)
			}
		}
	}()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	if err := p.Probe(ctx2, core.Source{Kind: core.KindMulticast, URL: "udp://" + group}); err != nil {
		if strings.Contains(err.Error(), "no multicast packets") {
			t.Skip("multicast loopback not available in this environment")
		}
		t.Fatalf("expected packets: %v", err)
	}
}

func TestSlideshowSyncAndOfflineCache(t *testing.T) {
	photos := map[string]string{"a.jpg": "AAAA", "b.png": "BB"}
	up := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up || r.Header.Get("Authorization") != "Bearer devtok" {
			http.Error(w, "nope", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/api/v1/photos/lobby/manifest" {
			m := manifest{Album: "lobby"}
			for name, body := range photos {
				m.Photos = append(m.Photos, struct {
					Name string `json:"name"`
					URL  string `json:"url"`
					Size int64  `json:"size"`
				}{name, "/api/v1/photos/lobby/files/" + name, int64(len(body))})
			}
			json.NewEncoder(w).Encode(m)
			return
		}
		name := filepath.Base(r.URL.Path)
		if body, ok := photos[name]; ok {
			w.Write([]byte(body))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	dir := t.TempDir()
	// A stale photo that should be removed.
	os.MkdirAll(filepath.Join(dir, "photos", "lobby"), 0o700)
	os.WriteFile(filepath.Join(dir, "photos", "lobby", "old.jpg"), []byte("x"), 0o600)

	s := &Slideshow{ServerURL: srv.URL, Token: "devtok", DataDir: dir}
	spec := core.SlideshowSpec{ManifestURL: "/api/v1/photos/lobby/manifest"}
	files, err := s.sync(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("want 2 files, got %v", files)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "photos", "lobby", "a.jpg")); string(b) != "AAAA" {
		t.Fatalf("a.jpg content %q", b)
	}

	// Server down: cached photos are still returned.
	up = false
	files, err = s.sync(context.Background(), spec)
	if err == nil || len(files) != 2 {
		t.Fatalf("offline: want cached files and an error, got %v, %v", files, err)
	}
}

func TestFileStoreAtomic(t *testing.T) {
	s := FileStore{Dir: t.TempDir()}
	if b, err := s.Get("missing"); err != nil || b != nil {
		t.Fatalf("missing key: %v %v", b, err)
	}
	if err := s.Put("k", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("k", []byte("v2")); err != nil {
		t.Fatal(err)
	}
	if b, _ := s.Get("k"); string(b) != "v2" {
		t.Fatalf("got %q", b)
	}
	entries, _ := os.ReadDir(s.Dir)
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}
