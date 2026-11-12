package rss

import (
	"crypto/sha256"
	"encoding/hex"
	"html"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// htmlToText turns an HTML fragment into one line of plain text: tags are
// removed (script/style contents too), block-level tags become spaces,
// entities are decoded and whitespace is collapsed.
func htmlToText(s string) string {
	if !strings.ContainsAny(s, "<&") {
		return collapse(s)
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if c != '<' || i+1 >= len(s) {
			b.WriteByte(c)
			i++
			continue
		}
		next := s[i+1]
		switch {
		case strings.HasPrefix(s[i:], "<!--"):
			end := strings.Index(s[i+4:], "-->")
			if end < 0 {
				i = len(s)
			} else {
				i += 4 + end + 3
			}
			continue
		case next == '!' || next == '?' || next == '/' || isASCIILetter(next):
			end := tagEnd(s, i+1)
			if end < 0 { // not a tag after all
				b.WriteByte(c)
				i++
				continue
			}
			tag := s[i+1 : end]
			name, closing := tagName(tag)
			i = end + 1
			if !closing && rawTextTags[name] && !strings.HasSuffix(tag, "/") {
				// Skip everything up to the matching close tag.
				if j := indexFold(s[i:], "</"+name); j >= 0 {
					i += j
				} else {
					i = len(s)
				}
				continue
			}
			if blockTags[name] {
				b.WriteByte(' ')
			}
		default:
			b.WriteByte(c)
			i++
		}
	}
	return collapse(html.UnescapeString(b.String()))
}

// tagEnd returns the index of the '>' that ends the tag starting at s[i],
// skipping quoted attribute values, or -1.
func tagEnd(s string, i int) int {
	var quote byte
	for ; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return i
		case c == '<':
			return -1
		}
	}
	return -1
}

func tagName(tag string) (name string, closing bool) {
	if strings.HasPrefix(tag, "/") {
		closing = true
		tag = tag[1:]
	}
	end := strings.IndexFunc(tag, func(r rune) bool {
		return !(r < utf8.RuneSelf && (isASCIILetter(byte(r)) || r >= '0' && r <= '9'))
	})
	if end >= 0 {
		tag = tag[:end]
	}
	return strings.ToLower(tag), closing
}

func isASCIILetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func indexFold(s, sub string) int {
	return strings.Index(strings.ToLower(s), sub)
}

var rawTextTags = map[string]bool{"script": true, "style": true, "noscript": true, "template": true, "iframe": true}

var blockTags = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true, "br": true, "dd": true,
	"div": true, "dl": true, "dt": true, "figcaption": true, "figure": true, "footer": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "header": true,
	"hr": true, "img": true, "li": true, "ol": true, "p": true, "pre": true, "section": true,
	"table": true, "td": true, "th": true, "tr": true, "ul": true,
}

// collapse trims s and squeezes all whitespace runs to one space.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// truncate shortens s to at most max runes, ending in "…" when cut.
func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	n := 0
	for i := range s {
		if n == max-1 {
			return strings.TrimRight(s[:i], " \t\n,;:-") + "…"
		}
		n++
	}
	return s
}

var (
	imgTagRE = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	attrRE   = regexp.MustCompile(`(?s)([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)
)

// imgSources returns candidate image URLs from <img> tags in an HTML
// fragment, in document order. Lazy-loading attributes are tried after src,
// and 1x1 tracking pixels are skipped.
func imgSources(h string) []string {
	var out []string
	for _, tag := range imgTagRE.FindAllString(h, -1) {
		attrs := map[string]string{}
		for _, m := range attrRE.FindAllStringSubmatch(tag[len("<img"):], -1) {
			k := strings.ToLower(m[1])
			if _, dup := attrs[k]; !dup {
				attrs[k] = html.UnescapeString(m[2] + m[3] + m[4])
			}
		}
		if w, h := strings.TrimSpace(attrs["width"]), strings.TrimSpace(attrs["height"]); w == "1" || h == "1" || w == "0" || h == "0" {
			continue
		}
		for _, k := range []string{"src", "data-src", "data-lazy-src"} {
			if v := strings.TrimSpace(attrs[k]); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

// shortHash is the first 16 hex characters of sha256(s).
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

var dateLayouts = []string{
	time.RFC1123Z,
	time.RFC1123,
	time.RFC3339Nano,
	time.RFC3339,
	time.RFC822Z,
	time.RFC822,
	time.RFC850,
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"Mon, 2 Jan 2006 15:04:05 MST",
	"Mon, 2 Jan 2006 15:04 -0700",
	"Mon, 2 Jan 2006 15:04 MST",
	"Mon, 02 Jan 2006 15:04 -0700",
	"Mon, 2 Jan 06 15:04:05 -0700",
	"Mon, 2 Jan 06 15:04:05 MST",
	"Mon, 2 January 2006 15:04:05 -0700",
	"2 Jan 2006 15:04:05 -0700",
	"2 Jan 2006 15:04:05 MST",
	"02 Jan 2006 15:04 -0700",
	"Mon Jan 2 15:04:05 -0700 2006",
	"Mon, 2 Jan 2006",
	"2006-01-02T15:04:05Z0700",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05 -0700",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// zoneOffsets maps zone abbreviations that RSS feeds use (RFC 822 allows
// them) to numeric offsets, since time.Parse can't know them.
var zoneOffsets = map[string]string{
	"UT": "+0000", "UTC": "+0000", "GMT": "+0000", "Z": "+0000",
	"EST": "-0500", "EDT": "-0400", "CST": "-0600", "CDT": "-0500",
	"MST": "-0700", "MDT": "-0600", "PST": "-0800", "PDT": "-0700",
	"AKST": "-0900", "AKDT": "-0800", "HST": "-1000",
	"BST": "+0100", "CET": "+0100", "CEST": "+0200", "AEST": "+1000", "AEDT": "+1100",
}

// parseDate parses the date formats seen in real feeds. ok is false for
// anything it doesn't understand; such dates are ignored.
func parseDate(s string) (time.Time, bool) {
	s = collapse(s)
	if s == "" {
		return time.Time{}, false
	}
	if i := strings.LastIndexByte(s, ' '); i > 0 {
		if off, ok := zoneOffsets[strings.ToUpper(s[i+1:])]; ok {
			s = s[:i+1] + off
		}
	}
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
