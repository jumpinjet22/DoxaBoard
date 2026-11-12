package live

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRelay implements the slice of the mediamtx API the controller uses.
type fakeRelay struct {
	mu         sync.Mutex
	publishing bool
	forward    []string
	states     []map[string]any
	patches    int
}

func (f *fakeRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
		f.patches++
	case r.URL.Path == "/v3/config/paths/get/live":
		items := []map[string]string{}
		for _, d := range f.forward {
			items = append(items, map[string]string{"dest": d})
		}
		json.NewEncoder(w).Encode(map[string]any{"name": "live", "forward": items})
	case r.URL.Path == "/v3/paths/get/live":
		if !f.publishing {
			http.Error(w, `{"error":"path not found"}`, http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ready": true, "readyTime": "2026-11-15T14:40:00Z", "tracks": []string{"H264", "MPEG-4 Audio"}})
	case r.URL.Path == "/v3/paths/forward-dests/list":
		json.NewEncoder(w).Encode(map[string]any{"items": f.states})
	default:
		http.NotFound(w, r)
	}
}

type memBackend struct {
	state      State
	broadcasts map[string]Broadcast
	slots      []Slot
	specials   []Special
}

func (m *memBackend) LiveState(context.Context) (State, error)      { return m.state, nil }
func (m *memBackend) SetLiveState(_ context.Context, s State) error { m.state = s; return nil }
func (m *memBackend) Schedule(context.Context) ([]Slot, []Special, error) {
	return m.slots, m.specials, nil
}
func (m *memBackend) Broadcast(_ context.Context, id string) (Broadcast, error) {
	b, ok := m.broadcasts[id]
	if !ok {
		return b, errors.New("not found")
	}
	return b, nil
}

func setup(t *testing.T) (*Controller, *fakeRelay, *memBackend) {
	t.Helper()
	relay := &fakeRelay{publishing: true}
	srv := httptest.NewServer(relay)
	t.Cleanup(srv.Close)
	be := &memBackend{
		broadcasts: map[string]Broadcast{
			"trad": {ID: "trad", Name: "8:30 Traditional", Outputs: []Output{{Label: "Vimeo", URL: "rtmps://live.example.com:443/live", Key: "trad-secret-key"}}},
			"cont": {ID: "cont", Name: "11:00 Contemporary", Outputs: []Output{
				{Label: "YouTube", URL: "rtmp://a.example.com/live2", Key: "yt-key"},
				{Label: "Facebook", URL: "rtmps://fb.example.com:443/rtmp/", Key: "fb-key"},
			}},
			"empty": {ID: "empty", Name: "Nowhere"},
		},
		slots: gdlcSchedule(),
	}
	c := &Controller{Backend: be, Relay: &MediaMTX{BaseURL: srv.URL}, Path: "live", Loc: houston, Now: func() time.Time { return sun(9, 0) }}
	return c, relay, be
}

func TestStartAutoStopAndStatus(t *testing.T) {
	c, relay, be := setup(t)
	ctx := context.Background()

	if err := c.Start(ctx, "auto", false, "companion"); err != nil {
		t.Fatal(err)
	}
	if !be.state.Live || be.state.BroadcastID != "trad" || be.state.StartedBy != "companion" {
		t.Fatalf("state %+v", be.state)
	}
	if len(relay.forward) != 1 || relay.forward[0] != "rtmps://live.example.com:443/live#trad-secret-key" {
		t.Fatalf("forward %v", relay.forward)
	}

	relay.states = []map[string]any{{"state": "error", "lastError": "auth failed for rtmps://live.example.com/live#trad-secret-key", "outboundBytes": 0, "pos": 0}}
	st := c.Status(ctx)
	if !st.Live || st.Broadcast.Name != "8:30 Traditional" || !st.OBS.Ready {
		t.Fatalf("status %+v", st)
	}
	if len(st.Outputs) != 1 || st.Outputs[0].State != "error" || st.Outputs[0].Host != "live.example.com" {
		t.Fatalf("outputs %+v", st.Outputs)
	}
	if strings.Contains(st.Outputs[0].LastError, "trad-secret-key") {
		t.Fatalf("stream key leaked in status: %q", st.Outputs[0].LastError)
	}
	if st.Scheduled == nil || st.Scheduled.ID != "trad" {
		t.Fatalf("scheduled %+v", st.Scheduled)
	}
	// The window in progress is "scheduled", not "upcoming".
	if len(st.Upcoming) == 0 || st.Upcoming[0].ID != "cont" {
		t.Fatalf("upcoming %+v", st.Upcoming)
	}

	if err := c.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if be.state.Live || len(relay.forward) != 0 {
		t.Fatalf("after stop: state %+v forward %v", be.state, relay.forward)
	}
}

func TestMultipleOutputsGoTogether(t *testing.T) {
	c, relay, _ := setup(t)
	if err := c.Start(context.Background(), "cont", false, "console"); err != nil {
		t.Fatal(err)
	}
	if len(relay.forward) != 2 || relay.forward[1] != "rtmps://fb.example.com:443/rtmp#fb-key" {
		t.Fatalf("forward %v", relay.forward)
	}
}

func TestStartRefusals(t *testing.T) {
	ctx := context.Background()

	c, relay, _ := setup(t)
	relay.publishing = false
	if err := c.Start(ctx, "trad", false, ""); !errors.Is(err, ErrNoSignal) {
		t.Fatalf("no OBS: got %v", err)
	}
	if err := c.Start(ctx, "trad", true, ""); err != nil {
		t.Fatalf("force should go live without OBS: %v", err)
	}

	c, _, _ = setup(t)
	if err := c.Start(ctx, "trad", false, ""); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(ctx, "trad", false, ""); err != nil {
		t.Fatalf("starting the live broadcast again should be a no-op: %v", err)
	}
	if err := c.Start(ctx, "cont", false, ""); !errors.Is(err, ErrAlreadyLive) || !strings.Contains(err.Error(), "8:30 Traditional") {
		t.Fatalf("second broadcast: got %v", err)
	}

	c, _, _ = setup(t)
	if err := c.Start(ctx, "empty", false, ""); !errors.Is(err, ErrNoOutputs) {
		t.Fatalf("no outputs: got %v", err)
	}

	c, _, _ = setup(t)
	c.Now = func() time.Time { return sun(10, 15) } // between services
	if err := c.Start(ctx, "auto", false, ""); !errors.Is(err, ErrNoWindow) {
		t.Fatalf("auto outside the schedule: got %v", err)
	}
}

func TestReconcileRestoresAfterRelayRestart(t *testing.T) {
	c, relay, _ := setup(t)
	ctx := context.Background()
	if err := c.Start(ctx, "trad", false, ""); err != nil {
		t.Fatal(err)
	}
	relay.forward = nil // mediamtx restarted and reloaded its file config
	if err := c.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(relay.forward) != 1 {
		t.Fatalf("forward not restored: %v", relay.forward)
	}
	patches := relay.patches
	if err := c.Reconcile(ctx); err != nil || relay.patches != patches {
		t.Fatalf("reconcile should be a no-op when in sync (err %v, patches %d→%d)", err, patches, relay.patches)
	}
	// Someone added a forward by hand while nothing is live: cleared.
	c.Stop(ctx)
	relay.forward = []string{"rtmp://rogue.example.com/x"}
	c.Reconcile(ctx)
	if len(relay.forward) != 0 {
		t.Fatalf("stray forward not cleared: %v", relay.forward)
	}
}

func TestRelayUnreachable(t *testing.T) {
	c, _, _ := setup(t)
	c.Relay.BaseURL = "http://127.0.0.1:1" // nothing listens on port 1
	if err := c.Start(context.Background(), "trad", false, ""); !errors.Is(err, ErrRelayUnreachable) {
		t.Fatalf("got %v", err)
	}
	if st := c.Status(context.Background()); st.RelayErr == "" {
		t.Fatal("status should report the relay error")
	}
}

func TestValidateOutputURL(t *testing.T) {
	for _, ok := range []string{"rtmps://live-api-s.facebook.com:443/rtmp/", "rtmp://a.rtmp.youtube.com/live2", "srt://ingest.example.com:9000?streamid=abc"} {
		if err := ValidateOutputURL(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"https://vimeo.com/event/1", "rtmp://", "not a url"} {
		if ValidateOutputURL(bad) == nil {
			t.Errorf("%s should be rejected", bad)
		}
	}
}
