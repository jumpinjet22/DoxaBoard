package jsonfeed

import (
	"context"
	"embed"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jumpinjet22/doxaboard/connectors/connectortest"
	"github.com/jumpinjet22/doxaboard/connectors/registry"
	"github.com/jumpinjet22/doxaboard/slides"
)

//go:embed testdata
var testdata embed.FS

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := testdata.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestConformance(t *testing.T) {
	connectortest.Run(t, Connector{}, []connectortest.Fixture{
		{
			Name:   "sidecar_feed",
			Config: registry.Config{"url": "{{server}}/feed.json"},
			Routes: map[string]connectortest.Response{
				"/feed.json": {ContentType: "application/json", Body: mustRead(t, "testdata/announcements.json")},
			},
			// broken-no-content, the duplicate and bad-scheme are dropped;
			// expired-1 is kept (the server filters by time).
			WantIDs: []string{"event-4411", "event-4412", "photo-77", "expired-1"},
		},
		{
			Name:    "empty_feed",
			Config:  registry.Config{"url": "{{server}}/empty"},
			Routes:  map[string]connectortest.Response{"/empty": {Body: []byte(`{"schema":"doxaboard.slides/v1","slides":[]}`)}},
			WantIDs: []string{},
		},
		{
			Name:    "wrong_schema",
			Config:  registry.Config{"url": "{{server}}/v2"},
			Routes:  map[string]connectortest.Response{"/v2": {Body: []byte(`{"schema":"doxaboard.slides/v2","slides":[]}`)}},
			WantErr: true,
		},
	})
}

type logRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (l *logRecorder) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logRecorder) String() string { return strings.Join(l.lines, "\n") }

func newSource(t *testing.T, srv *httptest.Server, cfg registry.Config, log *logRecorder) registry.Source {
	t.Helper()
	info := Connector{}.Info()
	cfg, err := registry.ValidateConfig(info, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if log == nil {
		log = &logRecorder{}
	}
	src, err := Connector{}.New(cfg, registry.Deps{
		HTTP: registry.NewHTTPClient(registry.AllowedHosts(info, cfg), 5*time.Second, nil),
		Logf: log.logf,
		Now:  func() time.Time { return connectortest.DefaultNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func TestFetchDetails(t *testing.T) {
	var gotAuth, gotAccept string
	feed := mustRead(t, "testdata/announcements.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotAccept = r.Header.Get("Authorization"), r.Header.Get("Accept")
		w.Write(feed)
	}))
	defer srv.Close()
	log := &logRecorder{}
	src := newSource(t, srv, registry.Config{"url": srv.URL + "/sidecar/feed.json", "token": "s3cret"}, log)
	got, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer s3cret" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q", gotAccept)
	}
	if len(got) != 4 {
		t.Fatalf("got %d slides, want 4", len(got))
	}
	if want := srv.URL + "/media/fall-festival.jpg"; got[0].Image.URL != want {
		t.Errorf("relative image resolved to %q, want %q", got[0].Image.URL, want)
	}
	if got[0].Image.Alt != "Children on a hay ride" || got[0].StartsAt == nil || len(got[0].Tags) != 2 {
		t.Errorf("fields not preserved: %+v", got[0])
	}
	if got[2].Image.URL != "https://cdn.example.org/photos/choir.jpg" {
		t.Errorf("absolute image changed: %q", got[2].Image.URL)
	}
	logs := log.String()
	for _, want := range []string{"broken-no-content", "duplicate id", "bad-scheme"} {
		if !strings.Contains(logs, want) {
			t.Errorf("log should mention %q; got:\n%s", want, logs)
		}
	}
}

func TestNoTokenNoAuthHeader(t *testing.T) {
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawAuth = r.Header["Authorization"]
		w.Write([]byte(`{"schema":"doxaboard.slides/v1","slides":[]}`))
	}))
	defer srv.Close()
	if _, err := newSource(t, srv, registry.Config{"url": srv.URL}, nil).Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sawAuth {
		t.Error("Authorization sent without a token")
	}
}

func TestFeedLevelErrors(t *testing.T) {
	tests := map[string]string{
		"missing schema": `{"slides":[{"id":"a","title":"A"}]}`,
		"wrong schema":   `{"schema":"something/else","slides":[]}`,
		"html page":      `<!doctype html><html><body>Sign in</body></html>`,
		"too many slides": func() string {
			var b strings.Builder
			b.WriteString(`{"schema":"doxaboard.slides/v1","slides":[`)
			for i := range slides.MaxSlides + 1 {
				if i > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, `{"id":"s%d","title":"T"}`, i)
			}
			b.WriteString("]}")
			return b.String()
		}(),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(body))
			}))
			defer srv.Close()
			got, err := newSource(t, srv, registry.Config{"url": srv.URL}, nil).Fetch(context.Background())
			if err == nil {
				t.Fatalf("want error, got %d slides", len(got))
			}
			t.Log(err)
		})
	}
}

func TestSchemaErrorMentionsProblem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"slides":[]}`))
	}))
	defer srv.Close()
	_, err := newSource(t, srv, registry.Config{"url": srv.URL}, nil).Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "schema") || !strings.Contains(err.Error(), slides.SchemaV1) {
		t.Errorf("error should explain the schema problem, got %v", err)
	}
}

func TestOversizedFeed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"schema":"doxaboard.slides/v1","slides":[],"pad":"`))
		w.Write([]byte(strings.Repeat("x", slides.MaxFeedBytes)))
		w.Write([]byte(`"}`))
	}))
	defer srv.Close()
	if _, err := newSource(t, srv, registry.Config{"url": srv.URL}, nil).Fetch(context.Background()); err == nil {
		t.Error("want error for a feed over MaxFeedBytes")
	}
}

func TestImageResolution(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"schema":"doxaboard.slides/v1","slides":[
			{"id":"rel","title":"Relative","image":{"url":"img/a.jpg"}},
			{"id":"proto","title":"Protocol-relative","image":{"url":"//cdn.example.org/b.jpg"}},
			{"id":"nohost-titled","title":"Kept without image","image":{"url":"http:c.jpg"}},
			{"id":"nohost-untitled","image":{"url":"http:d.jpg","alt":"d"}}
		]}`))
	}))
	defer srv.Close()
	log := &logRecorder{}
	got, err := newSource(t, srv, registry.Config{"url": srv.URL + "/feeds/main.json"}, log).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]slides.Slide{}
	for _, s := range got {
		byID[s.ID] = s
	}
	if s := byID["rel"]; s.Image == nil || s.Image.URL != srv.URL+"/feeds/img/a.jpg" {
		t.Errorf("rel: %+v", s.Image)
	}
	if s := byID["proto"]; s.Image == nil || !strings.HasPrefix(s.Image.URL, "http://cdn.example.org/b.jpg") {
		t.Errorf("proto: %+v", s.Image)
	}
	if s, ok := byID["nohost-titled"]; !ok || s.Image != nil {
		t.Errorf("nohost-titled should be kept with its image dropped: %+v", s)
	}
	if _, ok := byID["nohost-untitled"]; ok {
		t.Error("nohost-untitled should be dropped: no title and no usable image")
	}
	if !strings.Contains(log.String(), "dropping image") {
		t.Errorf("expected a log line about the dropped image:\n%s", log)
	}
}

func TestNewValidates(t *testing.T) {
	deps := registry.Deps{HTTP: registry.NewHTTPClient(nil, time.Second, nil)}
	for _, cfg := range []registry.Config{
		{},
		{"url": "ftp://example.org/feed.json"},
		{"url": "not a url"},
		{"url": "https://example.org/feed.json", "token": "a\nb"},
	} {
		if _, err := (Connector{}).New(cfg, deps); err == nil {
			t.Errorf("New(%v) should fail", cfg)
		}
	}
}

func TestRegistered(t *testing.T) {
	if _, ok := registry.Get("jsonfeed"); !ok {
		t.Error("jsonfeed not registered")
	}
}
