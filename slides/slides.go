// Package slides defines the DoxaCast slide feed: the contract between
// content sources (Rock, WordPress, RSS, folders, sidecar connectors in any
// language) and the DoxaCast server.
//
// The wire format is JSON:
//
//	{"schema": "doxacast.slides/v1", "slides": [{"id": "...", "title": "..."}]}
//
// Compatibility rules for v1: new optional fields may be added; readers must
// ignore fields they don't know. Anything that would break an existing feed
// or reader requires doxacast.slides/v2.
package slides

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// SchemaV1 identifies version 1 of the feed format.
const SchemaV1 = "doxacast.slides/v1"

// Limits enforced by Validate. Generous for real content, tight enough that
// a broken source can't flood screens or the server.
const (
	MaxFeedBytes    = 4 << 20 // 4 MiB of JSON
	MaxSlides       = 500
	MaxIDLen        = 200
	MaxTitleLen     = 200
	MaxBodyLen      = 1000
	MaxAltLen       = 300
	MaxTags         = 20
	MaxTagLen       = 50
	MinDuration     = 3
	MaxDuration     = 300
	DefaultDuration = 10
)

// Feed is a document of slides.
type Feed struct {
	Schema string  `json:"schema"`
	Slides []Slide `json:"slides"`
}

// Slide is one piece of content shown on screens.
type Slide struct {
	// ID is stable for the same piece of content across fetches. Required.
	ID string `json:"id"`
	// Title is a short heading. Title or Image is required.
	Title string `json:"title,omitempty"`
	// Body is plain text. Never HTML: screens render it literally.
	Body string `json:"body,omitempty"`
	// Image is shown full screen (or behind the text, depending on theme).
	Image *Image `json:"image,omitempty"`
	// StartsAt and ExpiresAt bound when the slide is shown. Either may be nil.
	StartsAt  *time.Time `json:"starts_at,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// Order sorts slides ascending; ties keep feed order.
	Order int `json:"order,omitempty"`
	// DurationSeconds overrides the profile's per-slide time (3-300).
	DurationSeconds int `json:"duration_seconds,omitempty"`
	// Tags target slides at zones, e.g. "lobby" or "kids".
	Tags []string `json:"tags,omitempty"`
	// UpdatedAt lets the server skip re-downloading unchanged images.
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// Image is a slide's picture.
type Image struct {
	// URL is absolute http(s), or relative to the feed URL.
	URL string `json:"url"`
	// Alt describes the image (accessibility, and the console's preview).
	Alt string `json:"alt,omitempty"`
}

// Active reports whether the slide should be shown at now.
func (s Slide) Active(now time.Time) bool {
	if s.StartsAt != nil && now.Before(*s.StartsAt) {
		return false
	}
	if s.ExpiresAt != nil && !now.Before(*s.ExpiresAt) {
		return false
	}
	return true
}

// Duration returns the slide's display time, falling back to def (and then
// DefaultDuration), clamped to the allowed range.
func (s Slide) Duration(def int) time.Duration {
	d := s.DurationSeconds
	if d == 0 {
		d = def
	}
	if d == 0 {
		d = DefaultDuration
	}
	d = min(max(d, MinDuration), MaxDuration)
	return time.Duration(d) * time.Second
}

// Parse reads a feed, enforcing MaxFeedBytes. Unknown fields are ignored
// (forward compatibility). It does not validate; call Validate.
func Parse(r io.Reader) (Feed, error) {
	var f Feed
	lr := &io.LimitedReader{R: r, N: MaxFeedBytes + 1}
	if err := json.NewDecoder(lr).Decode(&f); err != nil {
		if lr.N <= 0 {
			return f, fmt.Errorf("feed is larger than %d bytes", MaxFeedBytes)
		}
		return f, fmt.Errorf("invalid feed JSON: %w", err)
	}
	if lr.N <= 0 {
		return f, fmt.Errorf("feed is larger than %d bytes", MaxFeedBytes)
	}
	return f, nil
}

// ErrInvalid is wrapped by errors from Feed.Err.
var ErrInvalid = errors.New("invalid feed")
