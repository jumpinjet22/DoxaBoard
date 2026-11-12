package server

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/jumpinjet22/doxaboard/connectors/all"
	"github.com/jumpinjet22/doxaboard/connectors/registry"
	"github.com/jumpinjet22/doxaboard/core"
	"github.com/jumpinjet22/doxaboard/internal/content"
	"github.com/jumpinjet22/doxaboard/internal/theme"
)

func newContentServer(t *testing.T) (*httptest.Server, *Store) {
	t.Helper()
	dir := t.TempDir()
	photos := filepath.Join(dir, "photos")
	os.MkdirAll(filepath.Join(photos, "lobby"), 0o755)
	os.WriteFile(filepath.Join(photos, "lobby", "a.jpg"), []byte("jpegdata"), 0o644)
	store, err := OpenStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	key, err := content.LoadOrCreateKey(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	sealer, _ := content.NewSealer(key)
	cfg := Config{
		AdminToken: adminTok, EnrollmentToken: enrollTok, PhotoDir: photos,
		Content: &content.Manager{Dir: filepath.Join(dir, "content"), Files: os.DirFS(photos)},
		Sealer:  sealer,
	}
	srv := httptest.NewServer(New(cfg, store, nil).Handler())
	t.Cleanup(srv.Close)
	return srv, store
}

func pngBytes() []byte {
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1600, 900)))
	return b.Bytes()
}

func TestContentSourcesEndToEnd(t *testing.T) {
	var gotAuth string
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/feed":
			gotAuth = r.Header.Get("Authorization")
			w.Write([]byte(`{"schema":"doxaboard.slides/v1","slides":[
				{"id":"welcome","title":"Welcome","image":{"url":"/hero.png","alt":"hero"},"order":1},
				{"id":"groups","title":"Small Groups","body":"Find one that fits.","order":2},
				{"id":"old","title":"Past event","expires_at":"2020-01-01T00:00:00Z"},
				{"id":"soon","title":"Next month","starts_at":"2999-01-01T00:00:00Z","expires_at":"2999-02-01T00:00:00Z"}]}`))
		case "/hero.png":
			w.Write(pngBytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer feed.Close()

	srv, store := newContentServer(t)
	ctx := context.Background()

	// Connectors are listed with their fields plus extra_hosts.
	var conns []registry.Info
	call(t, srv, "GET", "/api/v1/admin/connectors", adminTok, nil, &conns)
	names := map[string]bool{}
	for _, c := range conns {
		names[c.Name] = true
		if c.Fields[len(c.Fields)-1].Key != registry.ExtraHostsKey {
			t.Errorf("%s: missing extra_hosts field", c.Name)
		}
	}
	for _, n := range []string{"jsonfeed", "rss", "folder"} {
		if !names[n] {
			t.Fatalf("connector %s not listed", n)
		}
	}

	// Invalid configs are rejected with a reason.
	if code := call(t, srv, "POST", "/api/v1/admin/sources", adminTok,
		sourceRequest{Name: "x", Connector: "jsonfeed", Config: map[string]string{"url": "ftp://nope"}}, nil); code != 400 {
		t.Fatalf("bad URL: got %d", code)
	}
	if code := call(t, srv, "POST", "/api/v1/admin/sources", adminTok,
		sourceRequest{Name: "x", Connector: "carrier-pigeon"}, nil); code != 400 {
		t.Fatalf("unknown connector: got %d", code)
	}

	// Create a source with a secret.
	var v SourceView
	code := call(t, srv, "POST", "/api/v1/admin/sources", adminTok, sourceRequest{
		Name: "Announcements", Connector: "jsonfeed",
		Config: map[string]string{"url": feed.URL + "/feed", "token": "super-secret-token"},
	}, &v)
	if code != 200 {
		t.Fatalf("create source: %d", code)
	}
	if _, leaked := v.Config["token"]; leaked || len(v.SecretsSet) != 1 || v.SecretsSet[0] != "token" {
		t.Fatalf("secret leaked or not marked set: %+v", v)
	}

	// Stored encrypted, never in plaintext.
	row, err := store.GetSource(ctx, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(row.Config["token"], "enc:v1:") || strings.Contains(row.Config["token"], "super-secret") {
		t.Fatalf("secret not sealed in the database: %q", row.Config["token"])
	}
	raw, _ := io.ReadAll(mustGet(t, srv, "/api/v1/admin/sources", adminTok).Body)
	if strings.Contains(string(raw), "super-secret") || strings.Contains(string(raw), "enc:v1:") {
		t.Fatal("source list exposes the secret")
	}

	// Refresh: two current slides (one rendered from text); the expired and upcoming ones are filtered later.
	var st content.Status
	call(t, srv, "POST", "/api/v1/admin/sources/"+v.ID+"/refresh", adminTok, nil, &st)
	if !st.OK || st.Slides != 4 {
		t.Fatalf("refresh status %+v", st)
	}
	if gotAuth != "Bearer super-secret-token" {
		t.Fatalf("connector got Authorization %q", gotAuth)
	}

	// Updating with an empty secret keeps the stored one.
	call(t, srv, "PUT", "/api/v1/admin/sources/"+v.ID, adminTok, sourceRequest{
		Name: "Announcements (renamed)", Config: map[string]string{"url": feed.URL + "/feed"},
	}, &v)
	call(t, srv, "POST", "/api/v1/admin/sources/"+v.ID+"/refresh", adminTok, nil, &st)
	if gotAuth != "Bearer super-secret-token" || v.Name != "Announcements (renamed)" {
		t.Fatalf("secret lost on update (auth %q) or rename failed (%q)", gotAuth, v.Name)
	}

	// Profile with the source and an album; a screen gets the merged manifest.
	var p Profile
	call(t, srv, "POST", "/api/v1/admin/profiles", adminTok, Profile{Name: "Lobby", Album: "lobby", SourceIDs: []string{v.ID, v.ID}}, &p)
	if len(p.SourceIDs) != 1 {
		t.Fatalf("duplicate source ids not removed: %v", p.SourceIDs)
	}
	if code := call(t, srv, "POST", "/api/v1/admin/profiles", adminTok, Profile{Name: "Bad", SourceIDs: []string{"s_nope"}}, nil); code != 400 {
		t.Fatalf("unknown source in profile: got %d", code)
	}
	enr, err := core.NewClient(srv.URL, "").Enroll(ctx, core.EnrollRequest{EnrollmentToken: enrollTok, Hostname: "tv"})
	if err != nil {
		t.Fatal(err)
	}
	pid := p.ID
	call(t, srv, "PATCH", "/api/v1/admin/devices/"+enr.DeviceID, adminTok, DevicePatch{ProfileID: &pid}, nil)

	dstate, err := core.NewClient(srv.URL, enr.DeviceToken).FetchState(ctx, 0)
	if err != nil || dstate.Slideshow.ManifestURL != slideshowManifestPath {
		t.Fatalf("state %+v %v", dstate.Slideshow, err)
	}
	var m PhotoManifest
	call(t, srv, "GET", slideshowManifestPath, enr.DeviceToken, nil, &m)
	if len(m.Photos) != 3 { // 2 active source slides + 1 album photo
		t.Fatalf("manifest has %d photos: %+v", len(m.Photos), m.Photos)
	}
	// Agents that apply the times themselves also get upcoming slides, with
	// their windows; expired ones are never sent.
	var sched PhotoManifest
	call(t, srv, "GET", slideshowManifestPath+"?schedule=1", enr.DeviceToken, nil, &sched)
	var upcoming int
	for _, ph := range sched.Photos {
		if ph.StartsAt != nil && ph.ExpiresAt != nil && ph.StartsAt.Year() == 2999 {
			upcoming++
		}
	}
	if len(sched.Photos) != 4 || upcoming != 1 {
		t.Fatalf("schedule manifest: %d photos, %d upcoming: %+v", len(sched.Photos), upcoming, sched.Photos)
	}
	resp := mustGet(t, srv, m.Photos[0].URL, enr.DeviceToken)
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("content type %q", ct)
	}
	if code := call(t, srv, "GET", "/api/v1/content/"+v.ID+"/files/..%2Fslides.json", enr.DeviceToken, nil, nil); code == 200 {
		t.Fatal("traversal into the cache dir should fail")
	}

	// Deleting the source removes it from the profile.
	call(t, srv, "DELETE", "/api/v1/admin/sources/"+v.ID, adminTok, nil, nil)
	var after Profile
	call(t, srv, "PUT", "/api/v1/admin/profiles/"+p.ID, adminTok, Profile{Name: "Lobby", Album: "lobby"}, &after)
	got, _ := store.GetProfile(ctx, p.ID)
	if len(got.SourceIDs) != 0 {
		t.Fatalf("deleted source still in profile: %v", got.SourceIDs)
	}
}

func TestContentDisabledWithoutConfig(t *testing.T) {
	srv, _ := newTestServer(t)
	if code := call(t, srv, "GET", "/api/v1/admin/sources", adminTok, nil, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("got %d", code)
	}
}

func mustGet(t *testing.T, srv *httptest.Server, path, tok string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: %d", path, resp.StatusCode)
	}
	return resp
}

func TestThemeEndpoints(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "logo.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0o644)
	os.WriteFile(filepath.Join(dir, theme.File), []byte(`{"name":"Example Church","logo":"logo.svg","console":{"light":{"accent":"#52635f"}}}`), 0o644)
	th, err := theme.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	body := func(h http.Handler, path string) (int, string, http.Header) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec.Code, rec.Body.String(), rec.Header()
	}

	themed := New(Config{AdminToken: adminTok, EnrollmentToken: enrollTok, Theme: th}, store, nil).Handler()
	if code, css, _ := body(themed, "/theme.css"); code != 200 || !strings.Contains(css, "--accent:#52635f;") {
		t.Errorf("theme.css: %d %q", code, css)
	}
	if code, info, _ := body(themed, "/api/v1/theme"); code != 200 || !strings.Contains(info, `"name":"Example Church"`) || !strings.Contains(info, `"logo":"theme/logo.svg"`) {
		t.Errorf("theme info: %d %s", code, info)
	}
	if code, _, hdr := body(themed, "/theme/logo.svg"); code != 200 || hdr.Get("Content-Security-Policy") == "" {
		t.Errorf("logo: %d, CSP %q", code, hdr.Get("Content-Security-Policy"))
	}
	if code, _, _ := body(themed, "/theme/theme.json"); code != 404 {
		t.Errorf("theme.json itself must not be served: %d", code)
	}

	plain := New(Config{AdminToken: adminTok, EnrollmentToken: enrollTok}, store, nil).Handler()
	if code, css, _ := body(plain, "/theme.css"); code != 200 || css != "" {
		t.Errorf("no theme: %d %q", code, css)
	}
	if code, info, _ := body(plain, "/api/v1/theme"); code != 200 || strings.TrimSpace(info) != "{}" {
		t.Errorf("no theme info: %d %s", code, info)
	}
}
