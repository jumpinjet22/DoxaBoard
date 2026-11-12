package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	_ "image/gif"  // register decoders for DecodeConfig
	_ "image/jpeg" //
	_ "image/png"  //
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jumpinjet22/doxacast/slides"
)

const (
	maxImageBytes   = 20 << 20
	warnImageBytes  = 5 << 20
	slowFeed        = 5 * time.Second
	fetchTimeout    = 30 * time.Second
	imageCheckLimit = 50
)

// Report is the machine-readable result (-json).
type Report struct {
	Source      string          `json:"source"`
	OK          bool            `json:"ok"`
	Errors      int             `json:"errors"`
	Warnings    int             `json:"warnings"`
	Slides      int             `json:"slides"`
	ActiveNow   int             `json:"active_now"`
	FetchMillis int64           `json:"fetch_ms,omitempty"`
	Bytes       int             `json:"bytes"`
	Problems    slides.Problems `json:"problems"`
}

type checker struct {
	source string
	token  string
	client *http.Client
	images bool
	now    time.Time
	rep    Report
}

func checkFeedCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check-feed", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print a JSON report")
	strict := fs.Bool("strict", false, "treat warnings as errors")
	noImages := fs.Bool("no-images", false, "skip downloading and checking images")
	token := fs.String("token", os.Getenv("DOXACAST_FEED_TOKEN"), "bearer token for the feed (or DOXACAST_FEED_TOKEN)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: doxacast-connector check-feed [flags] <url | file | ->")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	c := &checker{
		source: fs.Arg(0),
		token:  *token,
		client: &http.Client{Timeout: fetchTimeout},
		images: !*noImages,
		now:    time.Now(),
	}
	c.run(context.Background(), os.Stdin)
	c.rep.OK = c.rep.Errors == 0 && (!*strict || c.rep.Warnings == 0)

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.Encode(c.rep)
	} else {
		c.print(stdout)
	}
	if !c.rep.OK {
		return 1
	}
	return 0
}

func (c *checker) problem(sev slides.Severity, slide int, id, field, msg string, args ...any) {
	c.rep.Problems = append(c.rep.Problems, slides.Problem{
		Severity: sev, Slide: slide, SlideID: id, Field: field, Message: fmt.Sprintf(msg, args...),
	})
}

func (c *checker) isURL() bool {
	return strings.HasPrefix(c.source, "http://") || strings.HasPrefix(c.source, "https://")
}

// load returns the raw feed.
func (c *checker) load(ctx context.Context, stdin io.Reader) ([]byte, http.Header, error) {
	if !c.isURL() {
		r := stdin
		if c.source != "-" {
			f, err := os.Open(c.source)
			if err != nil {
				return nil, nil, err
			}
			defer f.Close()
			r = f
		}
		b, err := io.ReadAll(io.LimitReader(r, slides.MaxFeedBytes+1))
		return b, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.source, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "DoxaCast-check-feed/"+version)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return nil, nil, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, slides.MaxFeedBytes+1))
	return b, resp.Header, err
}

func (c *checker) run(ctx context.Context, stdin io.Reader) {
	start := time.Now()
	raw, hdr, err := c.load(ctx, stdin)
	if err != nil {
		c.problem(slides.Error, -1, "", "", "could not load feed: %v", err)
		c.tally()
		return
	}
	if c.isURL() {
		d := time.Since(start)
		c.rep.FetchMillis = d.Milliseconds()
		if d > slowFeed {
			c.problem(slides.Warning, -1, "", "", "feed took %v to respond; the server gives up at 30s", d.Round(time.Millisecond))
		}
		if ct := hdr.Get("Content-Type"); ct != "" && !strings.Contains(ct, "json") {
			c.problem(slides.Warning, -1, "", "", "Content-Type is %q; use application/json", ct)
		}
	}
	c.rep.Bytes = len(raw)
	feed, err := slides.Parse(bytes.NewReader(raw))
	if err != nil {
		c.problem(slides.Error, -1, "", "", "%v", err)
		c.tally()
		return
	}
	c.rep.Problems = append(c.rep.Problems, slides.Validate(feed)...)
	c.rep.Slides = len(feed.Slides)
	for _, s := range feed.Slides {
		if s.Active(c.now) {
			c.rep.ActiveNow++
		}
	}
	if len(feed.Slides) == 0 {
		c.problem(slides.Warning, -1, "", "slides", "feed has no slides")
	}
	if c.isURL() {
		c.checkStableIDs(ctx, feed)
	}
	if c.images {
		c.checkImages(ctx, feed)
	}
	c.tally()
}

// checkStableIDs fetches again: IDs that change on every fetch make the
// server re-download everything forever.
func (c *checker) checkStableIDs(ctx context.Context, first slides.Feed) {
	raw, _, err := c.load(ctx, nil)
	if err != nil {
		c.problem(slides.Warning, -1, "", "", "second fetch (stable ID check) failed: %v", err)
		return
	}
	second, err := slides.Parse(bytes.NewReader(raw))
	if err != nil {
		c.problem(slides.Error, -1, "", "", "second fetch returned an invalid feed: %v", err)
		return
	}
	a, b := idSet(first), idSet(second)
	var gone []string
	for id := range a {
		if !b[id] {
			gone = append(gone, id)
		}
	}
	if len(gone) > 0 && len(gone) >= len(a)/2 {
		sort.Strings(gone)
		c.problem(slides.Error, -1, "", "id",
			"%d of %d slide IDs changed between two fetches (e.g. %q); IDs must be stable for the same content",
			len(gone), len(a), gone[0])
	}
}

func idSet(f slides.Feed) map[string]bool {
	m := map[string]bool{}
	for _, s := range f.Slides {
		m[s.ID] = true
	}
	return m
}

func (c *checker) checkImages(ctx context.Context, feed slides.Feed) {
	var base *url.URL
	if c.isURL() {
		base, _ = url.Parse(c.source)
	}
	checked := map[string]bool{}
	n := 0
	for i, s := range feed.Slides {
		if s.Image == nil || s.Image.URL == "" {
			continue
		}
		u, err := url.Parse(s.Image.URL)
		if err != nil {
			continue // Validate already reported it
		}
		if !u.IsAbs() {
			if base == nil {
				c.problem(slides.Warning, i, s.ID, "image.url", "relative URL can't be checked when the feed isn't loaded from a URL")
				continue
			}
			u = base.ResolveReference(u)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			continue
		}
		if checked[u.String()] {
			continue
		}
		checked[u.String()] = true
		if n++; n > imageCheckLimit {
			c.problem(slides.Warning, -1, "", "", "checked the first %d images only", imageCheckLimit)
			return
		}
		c.checkImage(ctx, i, s.ID, u.String())
	}
}

func (c *checker) checkImage(ctx context.Context, i int, id, u string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		c.problem(slides.Error, i, id, "image.url", "%v", err)
		return
	}
	req.Header.Set("User-Agent", "DoxaCast-check-feed/"+version)
	resp, err := c.client.Do(req)
	if err != nil {
		c.problem(slides.Error, i, id, "image.url", "could not download: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.problem(slides.Error, i, id, "image.url", "HTTP %s", resp.Status)
		return
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		c.problem(slides.Error, i, id, "image.url", "download failed: %v", err)
		return
	}
	if len(b) > maxImageBytes {
		c.problem(slides.Error, i, id, "image.url", "image is over %d MB", maxImageBytes>>20)
		return
	}
	ct := http.DetectContentType(b)
	if !strings.HasPrefix(ct, "image/") {
		c.problem(slides.Error, i, id, "image.url", "not an image (looks like %s)", ct)
		return
	}
	if len(b) > warnImageBytes {
		c.problem(slides.Warning, i, id, "image.url", "image is %.1f MB; the server resizes it, but smaller downloads faster",
			float64(len(b))/(1<<20))
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return // webp and friends: content type was fine, dimensions unknown
	}
	if cfg.Width < 1280 || cfg.Height < 720 {
		c.problem(slides.Warning, i, id, "image.url", "image is %dx%d; it will look soft on a 1080p TV", cfg.Width, cfg.Height)
	}
	if ratio := float64(cfg.Width) / float64(cfg.Height); math.Abs(ratio-16.0/9.0) > 0.15 {
		c.problem(slides.Warning, i, id, "image.url", "image is %dx%d (%.2f:1); TVs are 16:9 (1.78:1), so expect bars or cropping",
			cfg.Width, cfg.Height, ratio)
	}
}

func (c *checker) tally() {
	c.rep.Source = c.source
	c.rep.Errors, c.rep.Warnings = 0, 0
	for _, p := range c.rep.Problems {
		if p.Severity == slides.Error {
			c.rep.Errors++
		} else {
			c.rep.Warnings++
		}
	}
	if c.rep.Problems == nil {
		c.rep.Problems = slides.Problems{}
	}
}

func (c *checker) print(w io.Writer) {
	fmt.Fprintf(w, "Checking %s\n\n", c.source)
	for _, p := range c.rep.Problems {
		mark := "⚠"
		if p.Severity == slides.Error {
			mark = "✗"
		}
		fmt.Fprintf(w, "  %s %s\n", mark, p)
	}
	if len(c.rep.Problems) > 0 {
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "  %d slides, %d showing right now", c.rep.Slides, c.rep.ActiveNow)
	if c.rep.FetchMillis > 0 {
		fmt.Fprintf(w, ", fetched in %dms", c.rep.FetchMillis)
	}
	fmt.Fprintf(w, "\n  %d errors, %d warnings\n\n", c.rep.Errors, c.rep.Warnings)
	if c.rep.OK {
		fmt.Fprintln(w, "✓ Feed works with DoxaCast "+slides.SchemaV1)
	} else {
		fmt.Fprintln(w, "✗ Fix the errors above, then run this again.")
	}
}
