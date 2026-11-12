package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jumpinjet22/doxaboard/internal/content"
	"github.com/jumpinjet22/doxaboard/internal/live"
)

const liveTok = "live-token-0123456789"

type testRelay struct {
	mu         sync.Mutex
	publishing bool
	forward    []string
}

func (f *testRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPatch && r.URL.Path == "/v3/config/paths/patch/live":
		var body struct {
			Forward []struct{ Dest string } `json:"forward"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.forward = nil
		for _, d := range body.Forward {
			f.forward = append(f.forward, d.Dest)
		}
	case r.URL.Path == "/v3/config/paths/get/live":
		items := []map[string]string{}
		for _, d := range f.forward {
			items = append(items, map[string]string{"dest": d})
		}
		json.NewEncoder(w).Encode(map[string]any{"forward": items})
	case r.URL.Path == "/v3/paths/get/live":
		if !f.publishing {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ready": true})
	case r.URL.Path == "/v3/paths/forward-dests/list":
		json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
	default:
		http.NotFound(w, r)
	}
}

func newLiveServer(t *testing.T, now time.Time) (*httptest.Server, *Store, *testRelay) {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	key, _ := content.LoadOrCreateKey(filepath.Join(dir, "secret.key"))
	sealer, _ := content.NewSealer(key)
	relay := &testRelay{publishing: true}
	relaySrv := httptest.NewServer(relay)
	t.Cleanup(relaySrv.Close)

	cfg := Config{AdminToken: adminTok, EnrollmentToken: enrollTok, Sealer: sealer, LiveToken: liveTok,
		Content: &content.Manager{Dir: filepath.Join(dir, "content"), Files: os.DirFS(dir)}}
	app := New(cfg, store, nil)
	loc, _ := time.LoadLocation("America/Chicago")
	app.cfg.Live = &live.Controller{Backend: app.LiveBackend(), Relay: &live.MediaMTX{BaseURL: relaySrv.URL},
		Path: "live", Loc: loc, Now: func() time.Time { return now }}
	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)
	return srv, store, relay
}

func TestLiveEndToEnd(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	sunday9 := time.Date(2026, 11, 15, 9, 0, 0, 0, loc)
	srv, store, relay := newLiveServer(t, sunday9)
	ctx := context.Background()

	// Bad stream URL rejected.
	bad := map[string]any{"name": "x", "outputs": []map[string]string{{"url": "https://vimeo.com/event/1", "key": "k"}}}
	if code := call(t, srv, "POST", "/api/v1/admin/broadcasts", adminTok, bad, nil); code != 400 {
		t.Fatalf("bad URL: %d", code)
	}

	// Create two broadcasts; keys are never returned.
	var trad, cont BroadcastView
	call(t, srv, "POST", "/api/v1/admin/broadcasts", adminTok, map[string]any{"name": "8:30 Traditional",
		"outputs": []map[string]string{{"label": "Vimeo", "url": "rtmps://live.example.com:443/live", "key": "trad-secret"}}}, &trad)
	call(t, srv, "POST", "/api/v1/admin/broadcasts", adminTok, map[string]any{"name": "11:00 Contemporary",
		"outputs": []map[string]string{{"url": "rtmp://yt.example.com/live2", "key": "yt"}, {"url": "rtmps://fb.example.com:443/rtmp/", "key": "fb"}}}, &cont)
	if len(trad.Outputs) != 1 || !trad.Outputs[0].KeySet || len(cont.Outputs) != 2 || cont.Outputs[0].Label != "yt.example.com" {
		t.Fatalf("views: %+v %+v", trad, cont)
	}
	raw, _ := io.ReadAll(mustGet(t, srv, "/api/v1/admin/broadcasts", adminTok).Body)
	if strings.Contains(string(raw), "trad-secret") || strings.Contains(string(raw), "enc:v1:") {
		t.Fatalf("stream key exposed: %s", raw)
	}
	row, _ := store.GetBroadcast(ctx, trad.ID)
	if !strings.HasPrefix(row.Outputs[0].SealedKey, "enc:v1:") {
		t.Fatal("stream key not sealed at rest")
	}

	// Rename with a blank key keeps the key.
	call(t, srv, "PUT", "/api/v1/admin/broadcasts/"+trad.ID, adminTok, map[string]any{"name": "8:30 Traditional Service",
		"outputs": []map[string]string{{"id": trad.Outputs[0].ID, "label": "Vimeo", "url": "rtmps://live.example.com:443/live"}}}, &trad)

	// Schedule: validated, then saved.
	badSched := LiveSchedule{Slots: []live.Slot{{BroadcastID: "b_nope", Weekday: time.Sunday, Start: "08:15", End: "09:45"}}}
	if code := call(t, srv, "PUT", "/api/v1/admin/live/schedule", adminTok, badSched, nil); code != 400 {
		t.Fatalf("unknown broadcast in schedule: %d", code)
	}
	sched := LiveSchedule{Slots: []live.Slot{
		{BroadcastID: trad.ID, Weekday: time.Sunday, Start: "08:15", End: "09:45"},
		{BroadcastID: cont.ID, Weekday: time.Sunday, Start: "10:45", End: "12:15"},
	}}
	if code := call(t, srv, "PUT", "/api/v1/admin/live/schedule", adminTok, sched, nil); code != 200 {
		t.Fatalf("save schedule: %d", code)
	}

	// The live token can't touch setup.
	for _, path := range []string{"/api/v1/admin/broadcasts", "/api/v1/admin/live/schedule", "/api/v1/admin/devices"} {
		if code := call(t, srv, "GET", path, liveTok, nil, nil); code != 401 {
			t.Errorf("live token on %s: %d", path, code)
		}
	}
	if code := call(t, srv, "POST", "/api/v1/live/start", "nope-not-a-token", nil, nil); code != 401 {
		t.Fatalf("bogus token: %d", code)
	}

	// Smart Go Live with the live token: Sunday 9:00 → Traditional.
	var st live.Status
	if code := call(t, srv, "POST", "/api/v1/live/start", liveTok, nil, &st); code != 200 {
		t.Fatalf("start: %d", code)
	}
	if !st.Live || st.Broadcast.ID != trad.ID {
		t.Fatalf("status %+v", st)
	}
	if len(relay.forward) != 1 || relay.forward[0] != "rtmps://live.example.com:443/live#trad-secret" {
		t.Fatalf("relay forward %v (blank key on edit must keep the saved key)", relay.forward)
	}
	resp := mustGet(t, srv, "/api/v1/live/status.txt", liveTok)
	line, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(line), "LIVE 8:30 Traditional Service") {
		t.Fatalf("status.txt %q", line)
	}

	// Going live on another broadcast is refused; deleting the live one too.
	if code := call(t, srv, "POST", "/api/v1/live/start?broadcast="+cont.ID, liveTok, nil, nil); code != 409 {
		t.Fatalf("second broadcast: %d", code)
	}
	if code := call(t, srv, "DELETE", "/api/v1/admin/broadcasts/"+trad.ID, adminTok, nil, nil); code != 409 {
		t.Fatalf("delete live broadcast: %d", code)
	}

	// End: relay stops forwarding.
	if code := call(t, srv, "POST", "/api/v1/live/stop", liveTok, nil, &st); code != 200 || st.Live || len(relay.forward) != 0 {
		t.Fatalf("stop: %d %+v %v", code, st, relay.forward)
	}

	// No OBS → refused with 409.
	relay.publishing = false
	if code := call(t, srv, "POST", "/api/v1/live/start?broadcast="+cont.ID, adminTok, nil, nil); code != 409 {
		t.Fatalf("no OBS: %d", code)
	}

	// Deleting a broadcast removes its schedule entries.
	call(t, srv, "DELETE", "/api/v1/admin/broadcasts/"+trad.ID, adminTok, nil, nil)
	got, _ := store.LiveSchedule(ctx)
	if len(got.Slots) != 1 || got.Slots[0].BroadcastID != cont.ID {
		t.Fatalf("schedule after delete: %+v", got.Slots)
	}
}

func TestLiveTokenOptional(t *testing.T) {
	srv, _ := newTestServer(t) // no live controller, no live token
	if code := call(t, srv, "GET", "/api/v1/live/status", "", nil, nil); code != 401 {
		t.Fatalf("empty token: %d", code)
	}
	if code := call(t, srv, "GET", "/api/v1/live/status", adminTok, nil, nil); code != 503 {
		t.Fatalf("live disabled: %d", code)
	}
}
