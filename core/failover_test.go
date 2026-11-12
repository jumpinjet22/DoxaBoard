package core

import (
	"errors"
	"testing"
	"time"
)

var errDown = errors.New("no packets")

func srcs() []Source {
	return []Source{
		{ID: "vimeo", Kind: KindHLS, URL: "https://example.invalid/live.m3u8", Priority: 30},
		{ID: "mcast", Kind: KindMulticast, URL: "udp://239.10.10.1:5000", Priority: 10},
		{ID: "srt", Kind: KindSRT, URL: "srt://relay:8890", Priority: 20},
	}
}

func TestPrefersHighestPriorityUpSource(t *testing.T) {
	s := NewSelector(FailoverConfig{RiseCount: 1, FallCount: 1})
	now := time.Unix(0, 0)
	s.Observe("vimeo", nil, now)
	s.Observe("srt", nil, now)
	s.Observe("mcast", errDown, now)

	m := s.Decide(srcs(), true, now)
	if m.Kind != ModeLive || m.Source.ID != "srt" {
		t.Fatalf("want live srt, got %+v", m)
	}
}

func TestFallsBackToSlideshowThenIdle(t *testing.T) {
	s := NewSelector(FailoverConfig{RiseCount: 1, FallCount: 1})
	now := time.Unix(0, 0)
	for _, src := range srcs() {
		s.Observe(src.ID, errDown, now)
	}
	if m := s.Decide(srcs(), true, now); m.Kind != ModeSlideshow {
		t.Fatalf("want slideshow, got %+v", m)
	}
	if m := s.Decide(srcs(), false, now); m.Kind != ModeIdle {
		t.Fatalf("want idle, got %+v", m)
	}
}

func TestHysteresis(t *testing.T) {
	s := NewSelector(FailoverConfig{RiseCount: 2, FallCount: 3})
	now := time.Unix(0, 0)

	s.Observe("mcast", nil, now)
	if s.Up("mcast") {
		t.Fatal("one success should not mark up with RiseCount=2")
	}
	s.Observe("mcast", nil, now)
	if !s.Up("mcast") {
		t.Fatal("two successes should mark up")
	}
	s.Observe("mcast", errDown, now)
	s.Observe("mcast", errDown, now)
	if !s.Up("mcast") {
		t.Fatal("two failures should not mark down with FallCount=3")
	}
	s.Observe("mcast", errDown, now)
	if s.Up("mcast") {
		t.Fatal("three failures should mark down")
	}
}

func TestUpgradeHoldPreventsFlapping(t *testing.T) {
	s := NewSelector(FailoverConfig{RiseCount: 1, FallCount: 1, UpgradeHold: 10 * time.Second})
	t0 := time.Unix(1000, 0)

	s.Observe("srt", nil, t0)
	s.Observe("mcast", errDown, t0)
	if m := s.Decide(srcs(), true, t0); m.Source.ID != "srt" {
		t.Fatalf("want srt, got %+v", m)
	}

	// Multicast comes back; we should not jump to it immediately.
	t1 := t0.Add(2 * time.Second)
	s.Observe("mcast", nil, t1)
	if m := s.Decide(srcs(), true, t1); m.Source.ID != "srt" {
		t.Fatalf("upgrade should be held, got %+v", m)
	}

	// After the hold, upgrade.
	t2 := t1.Add(11 * time.Second)
	s.Observe("mcast", nil, t2)
	if m := s.Decide(srcs(), true, t2); m.Source.ID != "mcast" {
		t.Fatalf("want upgrade to mcast, got %+v", m)
	}
}

func TestDowngradeIsImmediate(t *testing.T) {
	s := NewSelector(FailoverConfig{RiseCount: 1, FallCount: 1, UpgradeHold: time.Hour})
	now := time.Unix(0, 0)
	s.Observe("mcast", nil, now)
	s.Observe("srt", nil, now)
	if m := s.Decide(srcs(), true, now); m.Source.ID != "mcast" {
		t.Fatalf("want mcast, got %+v", m)
	}
	s.MarkDown("mcast", "player exited", now)
	if m := s.Decide(srcs(), true, now); m.Source.ID != "srt" {
		t.Fatalf("want immediate downgrade to srt, got %+v", m)
	}
}

func TestForgetDropsRemovedSources(t *testing.T) {
	s := NewSelector(DefaultFailoverConfig)
	s.Observe("old", nil, time.Unix(0, 0))
	s.Forget(srcs())
	if _, ok := s.States()["old"]; ok {
		t.Fatal("expected old source to be forgotten")
	}
}

func TestCooldownIgnoresProbesAfterPlaybackFailure(t *testing.T) {
	s := NewSelector(FailoverConfig{RiseCount: 1, FallCount: 1, Cooldown: 30 * time.Second})
	t0 := time.Unix(0, 0)
	s.Observe("mcast", nil, t0)
	s.MarkDown("mcast", "play failed", t0)
	s.Observe("mcast", nil, t0.Add(10*time.Second))
	if s.Up("mcast") {
		t.Fatal("source should stay down during cooldown")
	}
	s.Observe("mcast", nil, t0.Add(31*time.Second))
	if !s.Up("mcast") {
		t.Fatal("source should come back after cooldown")
	}
}
