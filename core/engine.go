package core

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"sync"
	"time"
)

const stateKey = "desired_state.json"

// Engine runs the display: syncs state, probes sources, fails over, reports.
type Engine struct {
	Client    *Client
	Store     Store
	Player    Player
	Slideshow Slideshow
	Prober    Prober
	Clock     Clock

	Platform string
	Version  string
	// Metrics, if set, adds platform numbers (link speed, temp...) to heartbeats.
	Metrics func() map[string]float64
	// Logf, if set, receives log lines.
	Logf func(format string, args ...any)
	// OnState, if set, is called whenever new desired state is applied
	// (including state loaded from cache at startup).
	OnState func(DesiredState)

	// TickInterval is how often sources are probed. Default 3s.
	TickInterval time.Duration
	// ProbeTimeout bounds each probe. Default 2.5s.
	ProbeTimeout time.Duration
	// HeartbeatInterval defaults to 30s.
	HeartbeatInterval time.Duration
	Failover          FailoverConfig

	sel       *Selector
	state     DesiredState
	hasState  bool
	applied   Mode
	slideSpec SlideshowSpec
	nextSync  time.Time
	nextBeat  time.Time
	started   time.Time
	lastErr   string
}

func (e *Engine) defaults() {
	if e.Clock == nil {
		e.Clock = SystemClock
	}
	if e.TickInterval == 0 {
		e.TickInterval = 3 * time.Second
	}
	if e.ProbeTimeout == 0 {
		e.ProbeTimeout = 2500 * time.Millisecond
	}
	if e.HeartbeatInterval == 0 {
		e.HeartbeatInterval = 30 * time.Second
	}
	if e.Failover == (FailoverConfig{}) {
		e.Failover = DefaultFailoverConfig
	}
	if e.Logf == nil {
		e.Logf = func(string, ...any) {}
	}
	if e.sel == nil {
		e.sel = NewSelector(e.Failover)
		e.applied = Mode{Kind: ModeIdle}
		e.started = e.Clock.Now()
	}
}

// Run loops until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) error {
	e.defaults()
	e.loadCached()
	t := time.NewTicker(e.TickInterval)
	defer t.Stop()
	for {
		e.Step(ctx)
		select {
		case <-ctx.Done():
			_ = e.Player.Stop()
			_ = e.Slideshow.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Step performs one iteration. Exposed for tests.
func (e *Engine) Step(ctx context.Context) {
	e.defaults()
	now := e.Clock.Now()

	if e.Client != nil && !now.Before(e.nextSync) {
		e.sync(ctx)
		e.nextSync = now.Add(jitter(e.pollInterval()))
	}

	if e.hasState {
		e.probeAll(ctx, now)
	}

	if e.applied.Kind == ModeLive && e.applied.Source != nil && !e.Player.Healthy() {
		e.sel.MarkDown(e.applied.Source.ID, "player stopped", now)
	}

	// If playing the chosen source fails, it's marked down and we try the
	// next one right away instead of leaving the screen blank for a tick.
	for range len(e.state.Sources) + 1 {
		next := e.sel.Decide(e.state.Sources, e.state.Slideshow.ManifestURL != "", now)
		if e.apply(ctx, next, now) {
			break
		}
	}

	if e.Client != nil && !now.Before(e.nextBeat) {
		e.heartbeat(ctx)
		e.nextBeat = now.Add(e.HeartbeatInterval)
	}
}

// Mode returns what the engine is currently showing.
func (e *Engine) Mode() Mode { return e.applied }

// State returns the current desired state.
func (e *Engine) State() DesiredState { return e.state }

func (e *Engine) pollInterval() time.Duration {
	if e.state.PollSeconds > 0 {
		return time.Duration(e.state.PollSeconds) * time.Second
	}
	return 20 * time.Second
}

// jitter spreads requests ±20% so a building full of screens rebooting after
// a power cut doesn't hit the server in the same second.
func jitter(d time.Duration) time.Duration {
	f := 0.8 + rand.Float64()*0.4 //nolint:gosec // load-spreading jitter, not security
	return time.Duration(float64(d) * f)
}

func (e *Engine) loadCached() {
	if e.Store == nil || e.hasState {
		return
	}
	b, err := e.Store.Get(stateKey)
	if err != nil || len(b) == 0 {
		return
	}
	var st DesiredState
	if json.Unmarshal(b, &st) == nil {
		e.setState(st)
		e.Logf("loaded cached state revision %d", st.Revision)
	}
}

func (e *Engine) sync(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var rev int64
	if e.hasState {
		rev = e.state.Revision
	}
	st, err := e.Client.FetchState(ctx, rev)
	if errors.Is(err, ErrNotModified) {
		return
	}
	if err != nil {
		// Server down: keep running on cached state. That's the whole point.
		e.lastErr = err.Error()
		e.Logf("state sync failed (continuing with cached state): %v", err)
		return
	}
	e.lastErr = ""
	e.setState(st)
	if e.Store != nil {
		if b, err := json.Marshal(st); err == nil {
			if err := e.Store.Put(stateKey, b); err != nil {
				e.Logf("caching state: %v", err)
			}
		}
	}
	e.Logf("applied state revision %d (%d sources)", st.Revision, len(st.Sources))
}

func (e *Engine) setState(st DesiredState) {
	e.defaults()
	e.state = st
	e.hasState = true
	e.sel.Forget(st.Sources)
	if e.OnState != nil {
		e.OnState(st)
	}
}

func (e *Engine) probeAll(ctx context.Context, now time.Time) {
	type result struct {
		id  string
		err error
	}
	results := make(chan result, len(e.state.Sources))
	var wg sync.WaitGroup
	playing := ""
	if e.applied.Kind == ModeLive && e.applied.Source != nil && e.Player.Healthy() {
		playing = e.applied.Source.ID
	}
	for _, src := range e.state.Sources {
		// The source on screen is proven by the player itself; probing it
		// again would just open a second connection to the same stream.
		if src.ID == playing {
			results <- result{src.ID, nil}
			continue
		}
		wg.Add(1)
		go func(src Source) {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, e.ProbeTimeout)
			defer cancel()
			results <- result{src.ID, e.Prober.Probe(pctx, src)}
		}(src)
	}
	wg.Wait()
	close(results)
	for r := range results {
		e.sel.Observe(r.id, r.err, now)
	}
}

// apply switches output to next. It returns false if live playback failed
// and the caller should decide again.
func (e *Engine) apply(ctx context.Context, next Mode, now time.Time) bool {
	slideChanged := e.state.Slideshow != e.slideSpec
	if next.Equal(e.applied) && (next.Kind != ModeSlideshow || !slideChanged) {
		return true
	}
	e.Logf("switching %s -> %s", describe(e.applied), describe(next))

	switch next.Kind {
	case ModeLive:
		_ = e.Slideshow.Stop()
		if err := e.Player.Play(ctx, *next.Source); err != nil {
			e.Logf("play %s failed: %v", next.Source.ID, err)
			e.sel.MarkDown(next.Source.ID, "play failed: "+err.Error(), now)
			e.applied = Mode{Kind: ModeIdle}
			return false
		}
	case ModeSlideshow:
		_ = e.Player.Stop()
		if err := e.Slideshow.Start(ctx, e.state.Slideshow); err != nil {
			e.Logf("slideshow failed: %v", err)
		}
		e.slideSpec = e.state.Slideshow
	default:
		_ = e.Player.Stop()
		_ = e.Slideshow.Stop()
	}
	e.applied = next
	return true
}

func (e *Engine) heartbeat(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	hb := Heartbeat{
		Revision:    e.state.Revision,
		Mode:        e.applied,
		Platform:    e.Platform,
		Version:     e.Version,
		Uptime:      e.Clock.Now().Sub(e.started),
		SourceState: e.sel.States(),
		Error:       e.lastErr,
	}
	if e.Metrics != nil {
		hb.Metrics = e.Metrics()
	}
	if err := e.Client.SendHeartbeat(ctx, hb); err != nil {
		e.Logf("heartbeat failed: %v", err)
	}
}

func describe(m Mode) string {
	if m.Kind == ModeLive && m.Source != nil {
		return "live(" + m.Source.ID + ")"
	}
	return string(m.Kind)
}
