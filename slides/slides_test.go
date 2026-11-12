package slides

import (
	"strings"
	"testing"
	"time"
)

func tp(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &t
}

func TestParseAndValidateGoodFeed(t *testing.T) {
	raw := `{
	  "schema": "doxaboard.slides/v1",
	  "future_field": {"ignored": true},
	  "slides": [
	    {"id": "a", "title": "Fall Festival", "body": "Oct 26", "order": 20,
	     "image": {"url": "https://example.org/a.jpg", "alt": "pumpkins"},
	     "starts_at": "2026-10-01T00:00:00Z", "expires_at": "2026-10-27T00:00:00Z",
	     "tags": ["lobby"], "duration_seconds": 12},
	    {"id": "b", "image": {"url": "/img/b.png", "alt": "sunrise"}, "order": 10}
	  ]
	}`
	f, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if ps := Validate(f); ps.HasErrors() {
		t.Fatalf("unexpected errors: %v", ps)
	}
	got := Usable(f, *tp("2026-10-15T12:00:00Z"))
	if len(got) != 2 || got[0].ID != "b" || got[1].ID != "a" {
		t.Fatalf("want [b a] sorted by order, got %+v", got)
	}
	// After expiry only b remains.
	if got := Usable(f, *tp("2026-10-28T00:00:00Z")); len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("want [b] after expiry, got %+v", got)
	}
}

func TestValidateProblems(t *testing.T) {
	long := strings.Repeat("x", MaxTitleLen+1)
	f := Feed{Schema: SchemaV1, Slides: []Slide{
		{ID: "", Title: "no id"},
		{ID: "dup", Title: "one"},
		{ID: "dup", Title: "two"},
		{ID: "empty"},
		{ID: "long", Title: long},
		{ID: "html", Title: "ok", Body: "<p>hello</p>"},
		{ID: "dates", Title: "x", StartsAt: tp("2026-01-02T00:00:00Z"), ExpiresAt: tp("2026-01-01T00:00:00Z")},
		{ID: "scheme", Image: &Image{URL: "javascript:alert(1)", Alt: "x"}},
		{ID: "tags", Title: "x", Tags: []string{"has space"}},
		{ID: "dur", Title: "x", DurationSeconds: 1000},
	}}
	ps := Validate(f)
	want := map[string]Severity{
		"slides[0].id":                     Error,
		"slides[2] (dup).id":               Error,
		"slides[3] (empty)":                Error,
		"slides[4] (long).title":           Error,
		"slides[5] (html).body":            Warning,
		"slides[6] (dates).expires_at":     Error,
		"slides[7] (scheme).image.url":     Error,
		"slides[8] (tags).tags":            Error,
		"slides[9] (dur).duration_seconds": Warning,
	}
	found := map[string]Severity{}
	for _, p := range ps {
		where := strings.SplitN(p.String(), ": ", 3)[1]
		found[where] = p.Severity
	}
	for where, sev := range want {
		if found[where] != sev {
			t.Errorf("%s: want %s, got %q\nall: %v", where, sev, found[where], ps)
		}
	}
	// Bad slides are dropped, good ones kept.
	got := Usable(f, time.Now())
	ids := []string{}
	for _, s := range got {
		ids = append(ids, s.ID)
	}
	if strings.Join(ids, ",") != "dup,html,dur" {
		t.Fatalf("usable = %v", ids)
	}
}

func TestFeedLevelErrors(t *testing.T) {
	for _, f := range []Feed{
		{Slides: []Slide{{ID: "a", Title: "x"}}},
		{Schema: "doxaboard.slides/v9", Slides: []Slide{{ID: "a", Title: "x"}}},
		{Schema: SchemaV1, Slides: make([]Slide, MaxSlides+1)},
	} {
		if !Validate(f).HasErrors() {
			t.Errorf("expected feed error for %+v", f.Schema)
		}
		if Usable(f, time.Now()) != nil {
			t.Error("invalid feed should yield no slides")
		}
	}
}

func TestParseLimits(t *testing.T) {
	big := `{"schema":"doxaboard.slides/v1","slides":[{"id":"a","title":"` + strings.Repeat("x", MaxFeedBytes) + `"}]}`
	if _, err := Parse(strings.NewReader(big)); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("want size error, got %v", err)
	}
	if _, err := Parse(strings.NewReader(`{not json`)); err == nil {
		t.Fatal("want JSON error")
	}
}

func TestDuration(t *testing.T) {
	cases := []struct {
		slide, def int
		want       time.Duration
	}{
		{0, 0, DefaultDuration * time.Second},
		{0, 8, 8 * time.Second},
		{12, 8, 12 * time.Second},
		{1, 8, MinDuration * time.Second},
		{9999, 8, MaxDuration * time.Second},
	}
	for _, c := range cases {
		if got := (Slide{DurationSeconds: c.slide}).Duration(c.def); got != c.want {
			t.Errorf("Duration(%d, def %d) = %v, want %v", c.slide, c.def, got, c.want)
		}
	}
}
