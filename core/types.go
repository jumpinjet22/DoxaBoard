// Package core is the platform-independent brain of a DoxaCast display.
//
// It knows how to fetch and cache desired state from the server, decide which
// source a screen should show (failover), and report health. It contains no
// OS-specific code: playback, slideshows, storage and probing are supplied by
// the platform through the interfaces in this package. That keeps it usable
// from the Linux agent and, via gomobile, from the Android TV app.
package core

import "time"

// SourceKind identifies how a live source is delivered.
type SourceKind string

const (
	// KindMulticast is MPEG-TS over UDP multicast, e.g. udp://239.10.10.1:5000.
	KindMulticast SourceKind = "multicast"
	// KindSRT is a unicast SRT stream from a relay.
	KindSRT SourceKind = "srt"
	// KindRTSP is a unicast RTSP stream from a relay.
	KindRTSP SourceKind = "rtsp"
	// KindHLS is an HTTP(S) HLS playlist (relay HLS output or Vimeo).
	KindHLS SourceKind = "hls"
)

// Source is one live video source, tried in priority order (lowest first).
type Source struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Kind     SourceKind `json:"kind"`
	URL      string     `json:"url"`
	Priority int        `json:"priority"`
}

// SlideshowSpec describes the photo fallback.
type SlideshowSpec struct {
	// ManifestURL lists the photos to show; the agent downloads and caches them.
	ManifestURL string `json:"manifest_url,omitempty"`
	// IntervalSeconds is how long each photo is shown.
	IntervalSeconds int `json:"interval_seconds"`
	// Shuffle randomizes order.
	Shuffle bool `json:"shuffle"`
}

// DisplaySpec holds per-screen output settings.
type DisplaySpec struct {
	// Output is "hdmi" or "composite".
	Output string `json:"output"`
	// TVStandard applies to composite output: "ntsc" or "pal".
	TVStandard string `json:"tv_standard,omitempty"`
	// SafeArea shrinks the slideshow to avoid overscan on old TVs (0-20 %).
	SafeAreaPercent int `json:"safe_area_percent,omitempty"`
}

// DesiredState is what the server wants a device to be doing.
type DesiredState struct {
	Revision  int64         `json:"revision"`
	DeviceID  string        `json:"device_id"`
	Name      string        `json:"name"`
	Sources   []Source      `json:"sources"`
	Slideshow SlideshowSpec `json:"slideshow"`
	Display   DisplaySpec   `json:"display"`
	// PollSeconds is how often the device should check for new state.
	PollSeconds int `json:"poll_seconds"`
}

// ModeKind is what the screen is currently doing.
type ModeKind string

const (
	ModeIdle      ModeKind = "idle"
	ModeLive      ModeKind = "live"
	ModeSlideshow ModeKind = "slideshow"
)

// Mode is the current output decision.
type Mode struct {
	Kind   ModeKind `json:"kind"`
	Source *Source  `json:"source,omitempty"`
}

// Equal reports whether two modes would produce the same output.
func (m Mode) Equal(o Mode) bool {
	if m.Kind != o.Kind {
		return false
	}
	if m.Source == nil || o.Source == nil {
		return m.Source == nil && o.Source == nil
	}
	return m.Source.ID == o.Source.ID && m.Source.URL == o.Source.URL
}

// Heartbeat is the status a device reports to the server.
type Heartbeat struct {
	Revision    int64             `json:"revision"`
	Mode        Mode              `json:"mode"`
	Platform    string            `json:"platform"`
	Version     string            `json:"version"`
	Uptime      time.Duration     `json:"uptime_ns"`
	SourceState map[string]string `json:"source_state,omitempty"`
	// Metrics carries platform-specific numbers: link speed, CPU temp, etc.
	Metrics map[string]float64 `json:"metrics,omitempty"`
	Error   string             `json:"error,omitempty"`
}
