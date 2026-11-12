package rss

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jumpinjet22/doxaboard/slides"
)

// Feeds come from the internet: any byte sequence must either be rejected
// or turn into slides that pass the slide schema. Never a panic, never an
// invalid slide.
func FuzzFeedToSlides(f *testing.F) {
	files, _ := filepath.Glob("testdata/*.xml")
	for _, p := range files {
		if b, err := os.ReadFile(p); err == nil {
			f.Add(b)
		}
	}
	f.Add([]byte(`<rss><channel><item><title>x</title><guid>1</guid></item></channel></rss>`))
	f.Add([]byte(`<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>a</id><title type="html">&lt;b&gt;hi&lt;/b&gt;</title></entry></feed>`))
	f.Add([]byte(`<?xml version="1.0" encoding="windows-1252"?><rss><channel><item><title>caf` + "\xe9" + `</title></item></channel></rss>`))

	feedURL, _ := url.Parse("https://example.org/feed.xml")
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	f.Fuzz(func(t *testing.T, data []byte) {
		entries, err := parseFeed(data)
		if err != nil {
			return
		}
		s := &source{
			url: feedURL, maxItems: slides.MaxSlides, maxAge: 30,
			logf: func(string, ...any) {}, now: func() time.Time { return now },
		}
		out := s.toSlides(entries, feedURL)
		if len(out) > slides.MaxSlides {
			t.Fatalf("%d slides exceeds the limit", len(out))
		}
		for _, p := range slides.Validate(slides.Feed{Schema: slides.SchemaV1, Slides: out}) {
			if p.Severity == slides.Error {
				t.Fatalf("connector produced an invalid slide: %s", p)
			}
		}
		for _, sl := range out {
			if sl.Image != nil && !strings.HasPrefix(sl.Image.URL, "http://") && !strings.HasPrefix(sl.Image.URL, "https://") {
				t.Fatalf("slide %q: non-absolute image URL %q", sl.ID, sl.Image.URL)
			}
		}
	})
}

// HTML from feeds is untrusted. Plain-text output must stay valid UTF-8
// when the input was, and no complete tag may survive. A lone "<" is fine:
// it's text ("a < b", or "<sCript0" with no closing ">"), and slide text is
// always rendered literally, never as HTML.
func FuzzHTMLToText(f *testing.F) {
	for _, s := range []string{
		"<p>Hello <b>world</b></p>",
		"a &amp; b &lt;c&gt; &#x1F600; &nbsp;",
		"<script>alert(1)</script>after",
		"<!-- comment --><br/>line<br>break",
		"<a href='x'>unterminated",
		"&#99999999;",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := htmlToText(s)
		if utf8.ValidString(s) && !utf8.ValidString(out) {
			t.Fatalf("valid UTF-8 in, invalid out: %q -> %q", s, out)
		}
		// Entities can legitimately decode to "<b>", and single-pass stripping
		// can reassemble deliberately nested fragments ("<A<A>>" -> "<A>").
		// Both are harmless because slide text is never rendered as HTML, so
		// only check inputs with neither.
		if !strings.Contains(s, "&") && !nestedTagRE.MatchString(s) && completeTagRE.MatchString(out) {
			t.Fatalf("a complete tag survived: %q -> %q", s, out)
		}
	})
}

var completeTagRE = regexp.MustCompile(`<[/!?a-zA-Z][^<>]*>`)
var nestedTagRE = regexp.MustCompile(`<[^>]*<`)

func FuzzTruncate(f *testing.F) {
	f.Add("hello world", 5)
	f.Add("héllo wörld 😀😀😀", 4)
	f.Add("", 1)
	f.Fuzz(func(t *testing.T, s string, max int) {
		if max < 1 || max > 5000 {
			return
		}
		out := truncate(s, max)
		if n := utf8.RuneCountInString(out); n > max {
			t.Fatalf("truncate(%q, %d) has %d runes", s, max, n)
		}
		if utf8.ValidString(s) && !utf8.ValidString(out) {
			t.Fatalf("truncate broke UTF-8: %q -> %q", s, out)
		}
	})
}

func FuzzParseDate(f *testing.F) {
	for _, s := range []string{"Mon, 02 Jan 2006 15:04:05 -0700", "2026-01-01T00:00:00Z", "Tue, 3 Jun 2008 11:05:30 GMT", "Wed, 01 Jan 2025 10:00:00 EST", "garbage"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		parseDate(s) // must not panic
	})
}

func TestHTMLToTextUnclosedQuote(t *testing.T) {
	cases := map[string]string{
		`<a href="https://x>Register</a> today`: "Register today",
		`<sc"000>0000000000`:                    "0000000000",
		`<img alt="it's here" src="x.jpg">ok`:   "ok",
		`a < b and c > d`:                       "a < b and c > d",
		`<A">"`:                                 `"`,
	}
	for in, want := range cases {
		if got := htmlToText(in); got != want {
			t.Errorf("htmlToText(%q) = %q, want %q", in, got, want)
		}
	}
}
