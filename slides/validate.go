package slides

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Severity of a validation problem.
type Severity string

const (
	// Error: the server rejects the slide (or the whole feed).
	Error Severity = "error"
	// Warning: accepted, but probably not what the author wanted.
	Warning Severity = "warning"
)

// Problem is one validation finding.
type Problem struct {
	Severity Severity `json:"severity"`
	// Slide is the index in the feed, or -1 for feed-level problems.
	Slide   int    `json:"slide"`
	SlideID string `json:"slide_id,omitempty"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

func (p Problem) String() string {
	where := "feed"
	if p.Slide >= 0 {
		where = fmt.Sprintf("slides[%d]", p.Slide)
		if p.SlideID != "" {
			where += fmt.Sprintf(" (%s)", p.SlideID)
		}
	}
	if p.Field != "" {
		where += "." + p.Field
	}
	return fmt.Sprintf("%s: %s: %s", p.Severity, where, p.Message)
}

// Problems is a list of findings.
type Problems []Problem

// HasErrors reports whether any problem is an Error.
func (ps Problems) HasErrors() bool {
	for _, p := range ps {
		if p.Severity == Error {
			return true
		}
	}
	return false
}

// Validate checks a feed against the v1 rules. Feed-level errors (wrong
// schema, too many slides) make the whole feed unusable; slide-level errors
// make just that slide unusable.
func Validate(f Feed) Problems {
	var ps Problems
	feedErr := func(field, msg string, args ...any) {
		ps = append(ps, Problem{Severity: Error, Slide: -1, Field: field, Message: fmt.Sprintf(msg, args...)})
	}
	switch {
	case f.Schema == "":
		feedErr("schema", "missing; set it to %q", SchemaV1)
	case f.Schema != SchemaV1:
		feedErr("schema", "unsupported %q; this version understands %q", f.Schema, SchemaV1)
	}
	if len(f.Slides) > MaxSlides {
		feedErr("slides", "%d slides; the maximum is %d", len(f.Slides), MaxSlides)
	}
	seen := map[string]int{}
	for i, s := range f.Slides {
		ps = append(ps, validateSlide(i, s)...)
		if s.ID != "" {
			if j, dup := seen[s.ID]; dup {
				ps = append(ps, Problem{Severity: Error, Slide: i, SlideID: s.ID, Field: "id",
					Message: fmt.Sprintf("duplicate id (also slides[%d]); ids must be unique and stable", j)})
			} else {
				seen[s.ID] = i
			}
		}
	}
	return ps
}

func validateSlide(i int, s Slide) Problems {
	var ps Problems
	add := func(sev Severity, field, msg string, args ...any) {
		ps = append(ps, Problem{Severity: sev, Slide: i, SlideID: s.ID, Field: field, Message: fmt.Sprintf(msg, args...)})
	}
	text := func(field, v string, maxLen int) {
		if !utf8.ValidString(v) {
			add(Error, field, "not valid UTF-8")
			return
		}
		if n := utf8.RuneCountInString(v); n > maxLen {
			add(Error, field, "%d characters; the maximum is %d", n, maxLen)
		}
		if looksLikeHTML(v) {
			add(Warning, field, "looks like HTML; it will be shown as literal text (send plain text)")
		}
	}

	if strings.TrimSpace(s.ID) == "" {
		add(Error, "id", "required")
	} else {
		text("id", s.ID, MaxIDLen)
	}
	if strings.TrimSpace(s.Title) == "" && s.Image == nil {
		add(Error, "", "needs a title or an image")
	}
	text("title", s.Title, MaxTitleLen)
	text("body", s.Body, MaxBodyLen)

	if s.Image != nil {
		u, err := url.Parse(s.Image.URL)
		switch {
		case strings.TrimSpace(s.Image.URL) == "":
			add(Error, "image.url", "required when image is set")
		case err != nil:
			add(Error, "image.url", "not a valid URL: %v", err)
		case u.IsAbs() && u.Scheme != "http" && u.Scheme != "https":
			add(Error, "image.url", "scheme %q not allowed; use http(s) or a relative URL", u.Scheme)
		case u.Scheme == "http":
			add(Warning, "image.url", "plain http; prefer https")
		}
		text("image.alt", s.Image.Alt, MaxAltLen)
		if s.Image.Alt == "" && s.Title == "" {
			add(Warning, "image.alt", "image-only slide with no alt text")
		}
	}

	if s.StartsAt != nil && s.ExpiresAt != nil && !s.ExpiresAt.After(*s.StartsAt) {
		add(Error, "expires_at", "must be after starts_at")
	}
	if s.DurationSeconds != 0 && (s.DurationSeconds < MinDuration || s.DurationSeconds > MaxDuration) {
		add(Warning, "duration_seconds", "%d is outside %d-%d; it will be clamped", s.DurationSeconds, MinDuration, MaxDuration)
	}
	if len(s.Tags) > MaxTags {
		add(Error, "tags", "%d tags; the maximum is %d", len(s.Tags), MaxTags)
	}
	for _, t := range s.Tags {
		if t == "" || len(t) > MaxTagLen || strings.ContainsAny(t, " \t\n") {
			add(Error, "tags", "tag %q must be 1-%d characters with no spaces", t, MaxTagLen)
		}
	}
	return ps
}

func looksLikeHTML(s string) bool {
	l := strings.ToLower(s)
	for _, tag := range []string{"<p>", "<br", "<div", "<span", "<a ", "<img", "<b>", "<strong", "<em>", "<i>", "</", "&nbsp;"} {
		if strings.Contains(l, tag) {
			return true
		}
	}
	return false
}

// Usable returns the slides with no slide-level errors that are active at
// now, sorted by Order (stable). It returns nil if the feed itself is invalid.
func Usable(f Feed, now time.Time) []Slide {
	ps := Validate(f)
	bad := map[int]bool{}
	for _, p := range ps {
		if p.Severity != Error {
			continue
		}
		if p.Slide < 0 {
			return nil
		}
		bad[p.Slide] = true
	}
	var out []Slide
	for i, s := range f.Slides {
		if !bad[i] && s.Active(now) {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out
}
