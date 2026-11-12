// Package jsonfeed reads a URL that serves a doxaboard.slides/v1 feed.
//
// It is also how connectors written in other languages plug in: run them as
// a small service that serves the feed JSON, and point a jsonfeed source at
// it. The connector is strict (it enforces the schema and its limits) and
// helpful (it logs exactly which slides were dropped and why).
package jsonfeed

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/jumpinjet22/doxaboard/connectors/registry"
	"github.com/jumpinjet22/doxaboard/slides"
)

func init() { registry.Register(Connector{}) }

// Connector is the "jsonfeed" connector.
type Connector struct{}

// Info implements registry.Connector.
func (Connector) Info() registry.Info {
	return registry.Info{
		Name:  "jsonfeed",
		Title: "Slide feed (JSON)",
		Description: "Reads a URL that serves a doxaboard.slides/v1 JSON feed. " +
			"Use it for sidecar connectors written in any language.",
		Status:     registry.StatusOfficial,
		Maintainer: "@jumpinjet22",
		Fields: []registry.Field{
			{Key: "url", Label: "Feed URL", Type: registry.FieldURL, Required: true,
				Help: "An http(s) URL that returns a doxaboard.slides/v1 feed."},
			{Key: "token", Label: "Bearer token", Type: registry.FieldSecret,
				Help: `Optional. Sent as "Authorization: Bearer <token>".`},
		},
	}
}

// New implements registry.Connector. It does no I/O.
func (c Connector) New(cfg registry.Config, deps registry.Deps) (registry.Source, error) {
	cfg, err := registry.ValidateConfig(c.Info(), cfg)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(cfg["url"])
	if err != nil {
		return nil, fmt.Errorf("jsonfeed: feed URL: %w", err)
	}
	if deps.HTTP == nil {
		return nil, errors.New("jsonfeed: no HTTP client")
	}
	if strings.ContainsAny(cfg["token"], "\r\n") {
		return nil, errors.New("jsonfeed: bearer token must be a single line")
	}
	logf := deps.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &source{url: u, token: cfg["token"], http: deps.HTTP, logf: logf}, nil
}

type source struct {
	url   *url.URL
	token string
	http  *http.Client
	logf  func(string, ...any)
}

// Fetch implements registry.Source.
func (s *source) Fetch(ctx context.Context) ([]slides.Slide, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch slide feed: %w", err)
	}
	defer resp.Body.Close()
	if err := registry.CheckStatus(resp); err != nil {
		return nil, err
	}
	body, err := registry.ReadLimited(resp.Body, slides.MaxFeedBytes)
	if err != nil {
		return nil, fmt.Errorf("read slide feed: %w", err)
	}
	feed, err := slides.Parse(bytes.NewReader(body))
	if err != nil {
		if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "json") {
			return nil, fmt.Errorf("slide feed (Content-Type %q): %w", ct, err)
		}
		return nil, fmt.Errorf("slide feed: %w", err)
	}

	// Feed-level errors make the whole feed unusable; slide-level errors only
	// drop that slide.
	problems := slides.Validate(feed)
	var feedErrs []string
	bad := map[int]bool{}
	for _, p := range problems {
		switch {
		case p.Severity == slides.Error && p.Slide < 0:
			feedErrs = append(feedErrs, p.String())
		case p.Severity == slides.Error:
			bad[p.Slide] = true
			s.logf("jsonfeed: dropping slide: %s", p)
		default:
			s.logf("jsonfeed: %s", p)
		}
	}
	if len(feedErrs) > 0 {
		return nil, fmt.Errorf("%w: %s", slides.ErrInvalid, strings.Join(feedErrs, "; "))
	}

	base := s.url
	if resp.Request != nil && resp.Request.URL != nil {
		base = resp.Request.URL // the document's real URL, after redirects
	}
	out := make([]slides.Slide, 0, len(feed.Slides))
	for i, sl := range feed.Slides {
		if bad[i] {
			continue
		}
		if sl.Image != nil {
			img := *sl.Image
			abs, err := resolve(base, img.URL)
			if err != nil {
				s.logf("jsonfeed: slides[%d] (%s): dropping image %q: %v", i, sl.ID, img.URL, err)
				if strings.TrimSpace(sl.Title) == "" {
					s.logf("jsonfeed: dropping slide: slides[%d] (%s): no title and no usable image", i, sl.ID)
					continue
				}
				sl.Image = nil
			} else {
				img.URL = abs
				sl.Image = &img
			}
		}
		out = append(out, sl)
	}
	return out, nil
}

// resolve makes ref absolute against base and insists on http(s) with a host.
func resolve(base *url.URL, ref string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(ref))
	if err != nil {
		return "", err
	}
	u = base.ResolveReference(u)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("scheme %q not allowed", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("no host")
	}
	return u.String(), nil
}
