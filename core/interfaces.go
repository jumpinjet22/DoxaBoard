package core

import (
	"context"
	"time"
)

// Player shows a live source full screen.
type Player interface {
	// Play starts (or switches to) the given source.
	Play(ctx context.Context, src Source) error
	// Stop stops live playback.
	Stop() error
	// Healthy reports whether playback is still running.
	Healthy() bool
}

// Slideshow shows the photo fallback.
type Slideshow interface {
	Start(ctx context.Context, spec SlideshowSpec) error
	Stop() error
}

// Prober checks whether a source is currently delivering media.
// It should return within the context deadline.
type Prober interface {
	Probe(ctx context.Context, src Source) error
}

// Store persists small blobs (cached desired state, etc.) across restarts.
type Store interface {
	Get(key string) ([]byte, error)
	Put(key string, value []byte) error
}

// Clock is injectable for tests.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// SystemClock is the real wall clock.
var SystemClock Clock = systemClock{}
