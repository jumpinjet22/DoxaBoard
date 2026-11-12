package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }

type fakePlayer struct {
	playing *Source
	healthy bool
	fail    map[string]bool
}

func (p *fakePlayer) Play(_ context.Context, s Source) error {
	if p.fail[s.ID] {
		return errors.New("boom")
	}
	p.playing, p.healthy = &s, true
	return nil
}
func (p *fakePlayer) Stop() error   { p.playing, p.healthy = nil, false; return nil }
func (p *fakePlayer) Healthy() bool { return p.healthy }

type fakeSlides struct{ running bool }

func (s *fakeSlides) Start(context.Context, SlideshowSpec) error { s.running = true; return nil }
func (s *fakeSlides) Stop() error                                { s.running = false; return nil }

type fakeProber struct {
	mu sync.Mutex
	up map[string]bool
}

func (p *fakeProber) set(id string, up bool) { p.mu.Lock(); p.up[id] = up; p.mu.Unlock() }
func (p *fakeProber) Probe(_ context.Context, s Source) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.up[s.ID] {
		return nil
	}
	return errors.New("down")
}

type memStore map[string][]byte

func (m memStore) Get(k string) ([]byte, error) { return m[k], nil }
func (m memStore) Put(k string, v []byte) error { m[k] = v; return nil }

func testState() DesiredState {
	return DesiredState{
		Revision: 1, DeviceID: "d1", PollSeconds: 1,
		Sources:   srcs(),
		Slideshow: SlideshowSpec{ManifestURL: "http://server/photos.json", IntervalSeconds: 8},
	}
}

func newEngine(t *testing.T, srvURL string) (*Engine, *fakePlayer, *fakeSlides, *fakeProber, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	pl := &fakePlayer{fail: map[string]bool{}}
	sl := &fakeSlides{}
	pr := &fakeProber{up: map[string]bool{}}
	e := &Engine{
		Store: memStore{}, Player: pl, Slideshow: sl, Prober: pr, Clock: clk,
		Failover: FailoverConfig{RiseCount: 1, FallCount: 1, UpgradeHold: 5 * time.Second, Cooldown: time.Minute},
	}
	if srvURL != "" {
		e.Client = NewClient(srvURL, "tok")
	}
	return e, pl, sl, pr, clk
}

func TestEngineFailoverChain(t *testing.T) {
	st := testState()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/device/state" {
			_ = json.NewEncoder(w).Encode(st)
		}
	}))
	defer srv.Close()

	e, pl, sl, pr, clk := newEngine(t, srv.URL)
	ctx := context.Background()

	// Nothing live: slideshow.
	e.Step(ctx)
	if !sl.running || pl.playing != nil {
		t.Fatalf("want slideshow, mode=%+v", e.Mode())
	}

	// Vimeo comes up: play it.
	pr.set("vimeo", true)
	clk.t = clk.t.Add(3 * time.Second)
	e.Step(ctx)
	if pl.playing == nil || pl.playing.ID != "vimeo" || sl.running {
		t.Fatalf("want vimeo, mode=%+v", e.Mode())
	}

	// Multicast comes up: held, then upgraded.
	pr.set("mcast", true)
	clk.t = clk.t.Add(3 * time.Second)
	e.Step(ctx)
	if pl.playing.ID != "vimeo" {
		t.Fatalf("upgrade should be held, playing %s", pl.playing.ID)
	}
	clk.t = clk.t.Add(6 * time.Second)
	e.Step(ctx)
	if pl.playing.ID != "mcast" {
		t.Fatalf("want mcast after hold, playing %s", pl.playing.ID)
	}

	// Player dies on multicast: drop to next best immediately.
	pl.healthy = false
	pr.set("mcast", false)
	clk.t = clk.t.Add(3 * time.Second)
	e.Step(ctx)
	if pl.playing.ID != "vimeo" {
		t.Fatalf("want vimeo after mcast died, playing %s", pl.playing.ID)
	}
}

func TestEngineSurvivesServerOutageWithCachedState(t *testing.T) {
	st := testState()
	up := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/api/v1/device/state" {
			_ = json.NewEncoder(w).Encode(st)
		}
	}))
	defer srv.Close()

	e, pl, _, pr, clk := newEngine(t, srv.URL)
	pr.set("srt", true)
	e.Step(context.Background())
	if pl.playing == nil || pl.playing.ID != "srt" {
		t.Fatalf("want srt, mode=%+v", e.Mode())
	}

	// Server goes away. A fresh engine (simulating a reboot) using the same
	// store must keep playing from cache.
	up = false
	e2, pl2, _, pr2, _ := newEngine(t, srv.URL)
	e2.Store = e.Store
	e2.Clock = clk
	pr2.set("srt", true)
	e2.loadCached()
	e2.Step(context.Background())
	if pl2.playing == nil || pl2.playing.ID != "srt" {
		t.Fatalf("want srt from cached state after reboot, mode=%+v", e2.Mode())
	}
}

func TestEnginePlayFailureFallsThrough(t *testing.T) {
	e, pl, _, pr, clk := newEngine(t, "")
	e.setState(testState())
	pl.fail["mcast"] = true
	pr.set("mcast", true)
	pr.set("srt", true)

	e.Step(context.Background())
	clk.t = clk.t.Add(3 * time.Second)
	e.Step(context.Background())
	if pl.playing == nil || pl.playing.ID != "srt" {
		t.Fatalf("want srt after mcast play failure, mode=%+v", e.Mode())
	}
}
