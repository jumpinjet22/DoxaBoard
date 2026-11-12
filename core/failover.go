package core

import (
	"sort"
	"time"
)

// FailoverConfig tunes how eagerly the selector switches sources.
type FailoverConfig struct {
	// RiseCount is how many consecutive good probes mark a source up.
	RiseCount int
	// FallCount is how many consecutive bad probes mark a source down.
	FallCount int
	// UpgradeHold is how long a higher-priority source must stay up before
	// we switch away from a working lower-priority one. Prevents flapping
	// when, say, multicast blips on and off.
	UpgradeHold time.Duration
	// Cooldown is how long a source that failed to play (or whose player
	// died) is ignored, even if it probes fine. Stops retry loops on a
	// source that answers probes but can't actually be played.
	Cooldown time.Duration
}

// DefaultFailoverConfig is a reasonable starting point.
var DefaultFailoverConfig = FailoverConfig{
	RiseCount:   2,
	FallCount:   2,
	UpgradeHold: 10 * time.Second,
	Cooldown:    30 * time.Second,
}

type sourceHealth struct {
	up        bool
	successes int
	failures  int
	upSince   time.Time
	downUntil time.Time
	lastErr   string
}

// Selector decides which source to show, given a stream of probe results.
// It is pure logic: no I/O, no goroutines, safe to unit test with a fake clock.
type Selector struct {
	cfg     FailoverConfig
	health  map[string]*sourceHealth
	current Mode
}

// NewSelector creates a selector.
func NewSelector(cfg FailoverConfig) *Selector {
	if cfg.RiseCount < 1 {
		cfg.RiseCount = 1
	}
	if cfg.FallCount < 1 {
		cfg.FallCount = 1
	}
	return &Selector{cfg: cfg, health: map[string]*sourceHealth{}, current: Mode{Kind: ModeIdle}}
}

// Observe records one probe result for a source.
func (s *Selector) Observe(id string, err error, now time.Time) {
	h := s.health[id]
	if h == nil {
		h = &sourceHealth{}
		s.health[id] = h
	}
	if err == nil && now.Before(h.downUntil) {
		return // still cooling down after a playback failure
	}
	if err == nil {
		h.successes++
		h.failures = 0
		h.lastErr = ""
		if !h.up && h.successes >= s.cfg.RiseCount {
			h.up = true
			h.upSince = now
		}
		return
	}
	h.failures++
	h.successes = 0
	h.lastErr = err.Error()
	if h.up && h.failures >= s.cfg.FallCount {
		h.up = false
	}
}

// MarkDown immediately marks a source down, e.g. when the player crashed on
// it, and keeps it down for the configured cooldown.
func (s *Selector) MarkDown(id string, reason string, now time.Time) {
	h := s.health[id]
	if h == nil {
		h = &sourceHealth{}
		s.health[id] = h
	}
	h.up = false
	h.successes = 0
	h.failures = s.cfg.FallCount
	h.lastErr = reason
	h.downUntil = now.Add(s.cfg.Cooldown)
}

// Up reports whether a source is currently considered up.
func (s *Selector) Up(id string) bool {
	h := s.health[id]
	return h != nil && h.up
}

// States returns a human-readable state per source, for heartbeats.
func (s *Selector) States() map[string]string {
	out := make(map[string]string, len(s.health))
	for id, h := range s.health {
		switch {
		case h.up:
			out[id] = "up"
		case h.lastErr != "":
			out[id] = "down: " + h.lastErr
		default:
			out[id] = "unknown"
		}
	}
	return out
}

// Forget drops health for sources no longer configured.
func (s *Selector) Forget(keep []Source) {
	ids := make(map[string]bool, len(keep))
	for _, src := range keep {
		ids[src.ID] = true
	}
	for id := range s.health {
		if !ids[id] {
			delete(s.health, id)
		}
	}
}

// Decide picks the mode for the given configuration and returns it.
// hasSlideshow says whether a photo fallback is available.
func (s *Selector) Decide(sources []Source, hasSlideshow bool, now time.Time) Mode {
	sorted := SortedSources(sources)

	var best *Source
	for i := range sorted {
		if s.Up(sorted[i].ID) {
			best = &sorted[i]
			break
		}
	}

	next := Mode{Kind: ModeIdle}
	switch {
	case best != nil:
		next = Mode{Kind: ModeLive, Source: best}
	case hasSlideshow:
		next = Mode{Kind: ModeSlideshow}
	}

	// Upgrade hold: if we're happily playing a lower-priority source and a
	// better one just came up, wait until it has been stable for a while.
	if s.current.Kind == ModeLive && next.Kind == ModeLive &&
		s.current.Source != nil && s.Up(s.current.Source.ID) &&
		next.Source.Priority < s.current.Source.Priority {
		h := s.health[next.Source.ID]
		if now.Sub(h.upSince) < s.cfg.UpgradeHold {
			// Stay on the current source, but refresh it from config in case
			// its URL changed.
			for i := range sorted {
				if sorted[i].ID == s.current.Source.ID {
					return s.set(Mode{Kind: ModeLive, Source: &sorted[i]})
				}
			}
		}
	}
	return s.set(next)
}

func (s *Selector) set(m Mode) Mode {
	s.current = m
	return m
}

// Current returns the last decided mode.
func (s *Selector) Current() Mode { return s.current }

// SortedSources returns a copy of sources ordered by priority, then ID.
func SortedSources(sources []Source) []Source {
	out := append([]Source(nil), sources...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].ID < out[j].ID
	})
	return out
}
