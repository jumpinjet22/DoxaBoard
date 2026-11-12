package slides

import (
	"bytes"
	"testing"
	"time"
)

// Sidecar feeds come from code we don't control. Parse/Validate/Usable must
// never panic, and every slide Usable returns must pass validation on its
// own, sorted by Order.
func FuzzParseValidate(f *testing.F) {
	f.Add([]byte(`{"schema":"doxacast.slides/v1","slides":[{"id":"a","title":"Hi"}]}`))
	f.Add([]byte(`{"schema":"doxacast.slides/v1","slides":[{"id":"a","image":{"url":"/x.jpg","alt":"x"},"order":5},{"id":"a","title":"dup","order":1}]}`))
	f.Add([]byte(`{"schema":"doxacast.slides/v1","slides":[{"id":"b","title":"t","starts_at":"2026-01-02T00:00:00Z","expires_at":"2026-01-01T00:00:00Z"}]}`))
	f.Add([]byte(`{"schema":"doxacast.slides/v2"}`))
	f.Add([]byte(`{"slides":[{"id":"x","tags":["a b"],"duration_seconds":-4}]}`))
	f.Add([]byte(`not json`))

	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	f.Fuzz(func(t *testing.T, data []byte) {
		feed, err := Parse(bytes.NewReader(data))
		if err != nil {
			return
		}
		Validate(feed)
		usable := Usable(feed, now)
		for i, s := range usable {
			for _, p := range Validate(Feed{Schema: SchemaV1, Slides: []Slide{s}}) {
				if p.Severity == Error {
					t.Fatalf("Usable returned an invalid slide: %s", p)
				}
			}
			if !s.Active(now) {
				t.Fatalf("Usable returned inactive slide %q", s.ID)
			}
			if i > 0 && usable[i-1].Order > s.Order {
				t.Fatalf("Usable not sorted by order")
			}
			if d := s.Duration(0); d < MinDuration*time.Second || d > MaxDuration*time.Second {
				t.Fatalf("Duration %v out of range", d)
			}
		}
	})
}
