// Package connectortest is the conformance kit every in-tree connector must
// pass. Use it from your connector's tests:
//
//	func TestConformance(t *testing.T) {
//		connectortest.Run(t, myConnector{}, []connectortest.Fixture{{
//			Name:   "basic",
//			Config: registry.Config{"base_url": "{{server}}"},
//			Routes: map[string]connectortest.Response{
//				"/api/items": {Body: mustRead("testdata/items.json")},
//			},
//			WantIDs: []string{"item-1", "item-2"},
//		}})
//	}
//
// Fixtures are recorded responses served from a local test server, so tests
// never touch a real CMS or need real credentials. "{{server}}" in any config
// value is replaced with the test server's URL.
//
// On top of each fixture, Run checks that the connector:
//   - has valid metadata and rejects configs missing required fields
//   - returns slides that pass the slide schema, with stable IDs
//   - returns an error (not an empty list) on 5xx, garbage and truncated responses
//   - respects context deadlines
//   - can't reach hosts other than the configured ones
package connectortest

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jumpinjet22/doxaboard/connectors/registry"
	"github.com/jumpinjet22/doxaboard/slides"
)

// Response is a canned HTTP response.
type Response struct {
	Status      int // default 200
	ContentType string
	Body        []byte
	Headers     map[string]string
}

// Fixture is one recorded scenario.
type Fixture struct {
	Name string
	// Config for the source; "{{server}}" is replaced with the test server URL.
	Config registry.Config
	// Routes maps "path" or "path?rawquery" to a response. Unmatched requests 404.
	Routes map[string]Response
	// Files is served as Deps.Files, for folder-style connectors.
	Files fs.FS
	// Now is the clock given to the connector. Default: 2026-01-01T12:00:00Z.
	Now time.Time
	// WantIDs, if set, must equal the returned slide IDs (any order).
	WantIDs []string
	// WantErr expects Fetch to fail (e.g. a fixture of an API error page).
	WantErr bool
}

// DefaultNow is the fixture clock unless Fixture.Now is set.
var DefaultNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

type mode int32

const (
	modeNormal mode = iota
	mode500
	modeGarbage
	modeTruncated
	modeHang
)

// Run runs the conformance suite.
func Run(t *testing.T, c registry.Connector, fixtures []Fixture) {
	t.Helper()
	info := c.Info()
	t.Run("info", func(t *testing.T) {
		if err := registry.CheckInfo(info); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("required_fields", func(t *testing.T) {
		for _, f := range info.Fields {
			if f.Required && f.Default == "" {
				if _, err := c.New(registry.Config{}, deps(nil, nil, DefaultNow)); err == nil {
					t.Errorf("New with empty config should fail: field %q is required", f.Key)
				}
				return
			}
		}
	})
	if len(fixtures) == 0 {
		t.Fatal("at least one fixture is required")
	}
	for _, fx := range fixtures {
		t.Run(fx.Name, func(t *testing.T) { runFixture(t, c, info, fx) })
	}
}

func runFixture(t *testing.T, c registry.Connector, info registry.Info, fx Fixture) {
	var m atomic.Int32
	hang := make(chan struct{})
	defer close(hang)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode(m.Load()) {
		case mode500:
			http.Error(w, "upstream exploded", http.StatusInternalServerError)
			return
		case modeHang:
			select {
			case <-hang:
			case <-r.Context().Done():
			}
			return
		}
		resp, ok := fx.Routes[r.URL.Path+"?"+r.URL.RawQuery]
		if !ok {
			resp, ok = fx.Routes[r.URL.Path]
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		body := resp.Body
		switch mode(m.Load()) {
		case modeGarbage:
			body = []byte("\x00\xffthis is not what you expected<<<{{{")
		case modeTruncated:
			body = body[:len(body)/2]
		}
		for k, v := range resp.Headers {
			w.Header().Set(k, v)
		}
		if resp.ContentType != "" {
			w.Header().Set("Content-Type", resp.ContentType)
		}
		if resp.Status != 0 {
			w.WriteHeader(resp.Status)
		}
		w.Write(body)
	}))
	defer srv.Close()

	cfg := registry.Config{}
	for k, v := range fx.Config {
		cfg[k] = strings.ReplaceAll(v, "{{server}}", srv.URL)
	}
	cfg, err := registry.ValidateConfig(info, cfg)
	if err != nil {
		t.Fatalf("fixture config invalid: %v", err)
	}
	now := fx.Now
	if now.IsZero() {
		now = DefaultNow
	}
	hosts := registry.AllowedHosts(info, cfg)
	src, err := c.New(cfg, deps(hosts, fx.Files, now))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fetch := func(timeout time.Duration) ([]slides.Slide, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		return src.Fetch(ctx)
	}

	got, err := fetch(10 * time.Second)
	if fx.WantErr {
		if err == nil {
			t.Fatalf("expected an error, got %d slides", len(got))
		}
		return
	}
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	t.Run("valid_slides", func(t *testing.T) {
		ps := slides.Validate(slides.Feed{Schema: slides.SchemaV1, Slides: got})
		for _, p := range ps {
			if p.Severity == slides.Error {
				t.Errorf("%s", p)
			}
		}
		_, opener := src.(registry.Opener)
		for _, s := range got {
			if s.Image != nil && !opener && !strings.HasPrefix(s.Image.URL, "http://") && !strings.HasPrefix(s.Image.URL, "https://") {
				t.Errorf("slide %s: image URL %q must be absolute (or implement registry.Opener)", s.ID, s.Image.URL)
			}
		}
	})
	if fx.WantIDs != nil {
		t.Run("want_ids", func(t *testing.T) {
			if g, w := ids(got), sorted(fx.WantIDs); g != w {
				t.Errorf("slide IDs\n got: %s\nwant: %s", g, w)
			}
		})
	}
	t.Run("stable_ids", func(t *testing.T) {
		again, err := fetch(10 * time.Second)
		if err != nil {
			t.Fatalf("second Fetch: %v", err)
		}
		if a, b := ids(got), ids(again); a != b {
			t.Errorf("IDs changed between fetches\nfirst:  %s\nsecond: %s", a, b)
		}
	})
	if opener, ok := src.(registry.Opener); ok {
		t.Run("open_images", func(t *testing.T) {
			for _, s := range got {
				if s.Image == nil {
					continue
				}
				rc, err := opener.Open(context.Background(), s.Image.URL)
				if err != nil {
					t.Errorf("slide %s: Open(%q): %v", s.ID, s.Image.URL, err)
					continue
				}
				rc.Close()
			}
		})
	}

	if len(fx.Routes) == 0 {
		return // not a network connector; the failure modes below don't apply
	}
	failures := []struct {
		name string
		mode mode
	}{
		{"upstream_500", mode500},
		{"garbage_response", modeGarbage},
		{"truncated_response", modeTruncated},
	}
	for _, f := range failures {
		t.Run(f.name, func(t *testing.T) {
			m.Store(int32(f.mode))
			defer m.Store(int32(modeNormal))
			got, err := fetch(10 * time.Second)
			if err == nil {
				t.Errorf("Fetch should fail so the server keeps the last good slides; got %d slides and no error", len(got))
			}
		})
	}
	t.Run("respects_deadline", func(t *testing.T) {
		m.Store(int32(modeHang))
		defer m.Store(int32(modeNormal))
		start := time.Now()
		_, err := fetch(500 * time.Millisecond)
		if err == nil {
			t.Error("Fetch should fail when upstream hangs")
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("Fetch took %v after a 500ms deadline", d)
		}
	})
	t.Run("host_guard", func(t *testing.T) {
		// Same server, reached by a name the source wasn't configured with.
		// The connector must use Deps.HTTP, which refuses it.
		blocked := registry.Config{}
		for k, v := range cfg {
			blocked[k] = strings.ReplaceAll(v, "127.0.0.1", "localhost")
		}
		bsrc, err := c.New(blocked, deps(hosts, fx.Files, now))
		if err != nil {
			t.Skipf("connector rejected rewritten config: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err = bsrc.Fetch(ctx)
		if err == nil || !errors.Is(err, registry.ErrHostNotAllowed) {
			t.Errorf("Fetch to an unconfigured host must fail with registry.ErrHostNotAllowed "+
				"(are you using your own http.Client instead of Deps.HTTP?), got %v", err)
		}
	})
}

func deps(hosts []string, files fs.FS, now time.Time) registry.Deps {
	return registry.Deps{
		HTTP:  registry.NewHTTPClient(hosts, 5*time.Second, nil),
		Files: files,
		Logf:  func(string, ...any) {},
		Now:   func() time.Time { return now },
	}
}

func ids(ss []slides.Slide) string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.ID
	}
	return sorted(out)
}

func sorted(in []string) string {
	c := append([]string(nil), in...)
	sort.Strings(c)
	return fmt.Sprint(c)
}
