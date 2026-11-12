package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jumpinjet22/doxacast/core"
)

const (
	adminTok  = "admin-token-0123456789"
	enrollTok = "enroll-token-0123456789"
)

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	photos := filepath.Join(dir, "photos")
	if err := os.MkdirAll(filepath.Join(photos, "lobby"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(photos, "lobby", "a.jpg"), []byte("jpegdata"), 0o644)
	os.WriteFile(filepath.Join(photos, "lobby", "notes.txt"), []byte("nope"), 0o644)
	os.WriteFile(filepath.Join(dir, "secret.jpg"), []byte("secret"), 0o644)

	store, err := OpenStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	srv := httptest.NewServer(New(Config{AdminToken: adminTok, EnrollmentToken: enrollTok, PhotoDir: photos}, store, nil).Handler())
	t.Cleanup(srv.Close)
	return srv, photos
}

func call(t *testing.T, srv *httptest.Server, method, path, tok string, body, out any) int {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

func TestEndToEnd(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()

	// Bad enrollment token is rejected.
	c := core.NewClient(srv.URL, "")
	if _, err := c.Enroll(ctx, core.EnrollRequest{EnrollmentToken: "wrong", Hostname: "tv1"}); err == nil {
		t.Fatal("expected enrollment with bad token to fail")
	}

	// Enroll.
	enr, err := c.Enroll(ctx, core.EnrollRequest{EnrollmentToken: enrollTok, Hostname: "lobby-tv", Platform: "linux/arm64"})
	if err != nil {
		t.Fatal(err)
	}
	dc := core.NewClient(srv.URL, enr.DeviceToken)

	// Unassigned device gets an empty state.
	st, err := dc.FetchState(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if st.DeviceID != enr.DeviceID || len(st.Sources) != 0 {
		t.Fatalf("unexpected state %+v", st)
	}

	// Admin creates a profile and assigns it.
	var p Profile
	code := call(t, srv, "POST", "/api/v1/admin/profiles", adminTok, Profile{
		Name:  "Lobby",
		Album: "lobby",
		Sources: []core.Source{
			{Kind: core.KindHLS, URL: "https://example.invalid/live.m3u8", Priority: 30},
			{Kind: core.KindMulticast, URL: "udp://239.10.10.1:5000", Priority: 10},
		},
	}, &p)
	if code != 200 {
		t.Fatalf("create profile: %d", code)
	}
	pid := p.ID
	if code := call(t, srv, "PATCH", "/api/v1/admin/devices/"+enr.DeviceID, adminTok,
		DevicePatch{ProfileID: &pid}, nil); code != 200 {
		t.Fatalf("assign: %d", code)
	}

	// Device sees new state, sorted by priority, with photo manifest.
	st2, err := dc.FetchState(ctx, st.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if st2.Revision <= st.Revision || len(st2.Sources) != 2 || st2.Sources[0].Kind != core.KindMulticast {
		t.Fatalf("unexpected state %+v", st2)
	}
	if st2.Slideshow.ManifestURL != slideshowManifestPath {
		t.Fatalf("manifest url %q", st2.Slideshow.ManifestURL)
	}

	// Unchanged state returns 304.
	if _, err := dc.FetchState(ctx, st2.Revision); !errors.Is(err, core.ErrNotModified) {
		t.Fatalf("want not modified, got %v", err)
	}

	// Heartbeat shows up in the admin view.
	if err := dc.SendHeartbeat(ctx, core.Heartbeat{Revision: st2.Revision, Mode: core.Mode{Kind: core.ModeSlideshow}}); err != nil {
		t.Fatal(err)
	}
	var devs []DeviceView
	call(t, srv, "GET", "/api/v1/admin/devices", adminTok, nil, &devs)
	if len(devs) != 1 || !devs[0].Online || devs[0].Heartbeat == nil || devs[0].Heartbeat.Mode.Kind != core.ModeSlideshow {
		t.Fatalf("unexpected devices %+v", devs)
	}

	// Photo manifest lists only images.
	var m PhotoManifest
	if code := call(t, srv, "GET", "/api/v1/photos/lobby/manifest", enr.DeviceToken, nil, &m); code != 200 {
		t.Fatalf("manifest: %d", code)
	}
	if len(m.Photos) != 1 || m.Photos[0].Name != "a.jpg" {
		t.Fatalf("unexpected manifest %+v", m)
	}
	if code := call(t, srv, "GET", m.Photos[0].URL, enr.DeviceToken, nil, nil); code != 200 {
		t.Fatalf("photo: %d", code)
	}

	// Deleting the device revokes its token.
	call(t, srv, "DELETE", "/api/v1/admin/devices/"+enr.DeviceID, adminTok, nil, nil)
	if _, err := dc.FetchState(ctx, 0); err == nil {
		t.Fatal("deleted device should be rejected")
	}
}

func TestAuthRequired(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, path := range []string{"/api/v1/admin/devices", "/api/v1/device/state", "/api/v1/photos/lobby/manifest"} {
		if code := call(t, srv, "GET", path, "", nil, nil); code != http.StatusUnauthorized {
			t.Errorf("%s without token: got %d", path, code)
		}
		if code := call(t, srv, "GET", path, "bogus", nil, nil); code != http.StatusUnauthorized {
			t.Errorf("%s with bogus token: got %d", path, code)
		}
	}
}

func TestPhotoPathTraversal(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, path := range []string{
		"/api/v1/photos/lobby/files/..%2F..%2Fsecret.jpg",
		"/api/v1/photos/..%2F/files/secret.jpg",
		"/api/v1/photos/lobby/files/notes.txt",
		"/api/v1/photos/.hidden/manifest",
	} {
		if code := call(t, srv, "GET", path, adminTok, nil, nil); code == 200 {
			t.Errorf("%s: expected rejection, got 200", path)
		}
	}
}

func TestProfileValidation(t *testing.T) {
	srv, _ := newTestServer(t)
	bad := []Profile{
		{Name: ""},
		{Name: "x", Sources: []core.Source{{Kind: "carrier-pigeon", URL: "coo://"}}},
		{Name: "x", Sources: []core.Source{{Kind: core.KindMulticast, URL: "http://nope"}}},
		{Name: "x", Album: "../etc"},
		{Name: "x", Sources: []core.Source{{ID: "a", Kind: core.KindSRT, URL: "srt://a"}, {ID: "a", Kind: core.KindSRT, URL: "srt://b"}}},
	}
	for i, p := range bad {
		if code := call(t, srv, "POST", "/api/v1/admin/profiles", adminTok, p, nil); code != http.StatusBadRequest {
			t.Errorf("case %d: want 400, got %d", i, code)
		}
	}
}

func TestConsoleServed(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, path := range []string{"/", "/app.js", "/style.css"} {
		if code := call(t, srv, "GET", path, "", nil, nil); code != 200 {
			t.Errorf("%s: %d", path, code)
		}
	}
}
