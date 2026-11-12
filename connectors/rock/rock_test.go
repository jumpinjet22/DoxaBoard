package rock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jumpinjet22/doxaboard/connectors/connectortest"
	"github.com/jumpinjet22/doxaboard/connectors/registry"
	"github.com/jumpinjet22/doxaboard/slides"
)

func items(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/items.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func config(server string) registry.Config {
	return registry.Config{"url": server, "api_key": "test-key", "channel_id": "12", "time_zone": "America/Chicago"}
}

// TestConformance runs the DoxaBoard conformance kit against recorded
// responses in testdata/. No network, no real credentials.
func TestConformance(t *testing.T) {
	connectortest.Run(t, Connector{}, []connectortest.Fixture{
		{
			Name:   "channel",
			Config: config("{{server}}"),
			Routes: map[string]connectortest.Response{
				"/api/ContentChannelItems": {ContentType: "application/json", Body: items(t)},
			},
			// 4414 expired, 4415 is pending approval, 4416 has no title or image.
			WantIDs: []string{"rock-4411", "rock-4412", "rock-4413"},
		},
		{
			Name:    "empty_channel",
			Config:  config("{{server}}"),
			Routes:  map[string]connectortest.Response{"/api/ContentChannelItems": {ContentType: "application/json", Body: []byte(`[]`)}},
			WantIDs: []string{},
		},
		{
			Name:   "bad_key",
			Config: config("{{server}}"),
			Routes: map[string]connectortest.Response{
				"/api/ContentChannelItems": {Status: 401, ContentType: "application/json", Body: []byte(`{"Message":"Authorization has been denied for this request."}`)},
			},
			WantErr: true,
		},
	})
}

func newSource(t *testing.T, cfg registry.Config) registry.Source {
	t.Helper()
	info := Connector{}.Info()
	cfg, err := registry.ValidateConfig(info, cfg)
	if err != nil {
		t.Fatal(err)
	}
	src, err := Connector{}.New(cfg, registry.Deps{
		HTTP: registry.NewHTTPClient(registry.AllowedHosts(info, cfg), 5*time.Second, nil),
		Now:  func() time.Time { return connectortest.DefaultNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func TestRequestAndMapping(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Write(items(t))
	}))
	defer srv.Close()

	out, err := newSource(t, config(srv.URL+"/")).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.URL.Path != "/api/ContentChannelItems" || got.Header.Get("Authorization-Token") != "test-key" {
		t.Errorf("request %s with key %q", got.URL.Path, got.Header.Get("Authorization-Token"))
	}
	q := got.URL.Query()
	if q.Get("$filter") != "ContentChannelId eq 12" || q.Get("loadAttributes") != "simple" || q.Get("$top") == "" {
		t.Errorf("query %q", got.URL.RawQuery)
	}
	if strings.Contains(got.URL.RawQuery, "+") {
		t.Errorf("spaces should be sent as %%20, got %q", got.URL.RawQuery)
	}
	if len(out) != 3 {
		t.Fatalf("got %d slides: %+v", len(out), out)
	}

	// Rock's Order first (0 before 1), then higher Priority.
	if out[0].ID != "rock-4412" || out[1].ID != "rock-4413" || out[2].ID != "rock-4411" {
		t.Errorf("order: %s, %s, %s", out[0].ID, out[1].ID, out[2].ID)
	}
	for i, sl := range out {
		if sl.Order != i+1 {
			t.Errorf("%s: Order %d, want %d", sl.ID, sl.Order, i+1)
		}
	}

	groups, christmas, festival := out[0], out[1], out[2]
	if groups.Image != nil || groups.Body != "Find one that fits Sign up in the lobby." {
		t.Errorf("text item: image %v, body %q", groups.Image, groups.Body)
	}
	if groups.ExpiresAt != nil || groups.StartsAt == nil {
		t.Errorf("text item dates: %v - %v", groups.StartsAt, groups.ExpiresAt)
	}
	if want := srv.URL + "/Content/Graphics/christmas-eve.jpg"; christmas.Image == nil || christmas.Image.URL != want {
		t.Errorf("path image: %+v, want %s", christmas.Image, want)
	}
	if want := srv.URL + "/GetImage.ashx?guid=a1b2c3d4-0000-4000-8000-00000000ab01"; festival.Image == nil || festival.Image.URL != want {
		t.Errorf("guid image: %+v, want %s", festival.Image, want)
	}
	if festival.Image.Alt != "Fall Festival" || festival.Body != "Hay rides, chili & games for the whole family. Sunday at 4 PM" {
		t.Errorf("festival: alt %q, body %q", festival.Image.Alt, festival.Body)
	}
	// Rock's wall-clock times are read in Rock's zone: midnight in Chicago is 06:00 UTC in winter.
	if want := time.Date(2026, 2, 1, 6, 0, 0, 0, time.UTC); festival.ExpiresAt == nil || !festival.ExpiresAt.Equal(want) {
		t.Errorf("expires %v, want %v", festival.ExpiresAt, want)
	}
	if festival.UpdatedAt == nil || festival.UpdatedAt.UTC().Hour() != 20 {
		t.Errorf("updated_at with fractional seconds: %v", festival.UpdatedAt)
	}
	for _, p := range slides.Validate(slides.Feed{Schema: slides.SchemaV1, Slides: out}) {
		if p.Severity == slides.Error {
			t.Errorf("invalid slide: %s", p)
		}
	}
}

func TestApprovedOnlyOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(items(t)) }))
	defer srv.Close()
	cfg := config(srv.URL)
	cfg["approved_only"] = "false"
	out, err := newSource(t, cfg).Fetch(context.Background())
	if err != nil || len(out) != 4 {
		t.Fatalf("want the pending item too: %d slides, %v", len(out), err)
	}
}

// The REST key rides in a custom header, so a redirect must not carry it
// to another host, even one the source is otherwise allowed to reach.
func TestKeyNotSentAcrossRedirect(t *testing.T) {
	var leaked string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("Authorization-Token")
		w.Write([]byte(`[]`))
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, strings.Replace(other.URL, "127.0.0.1", "localhost", 1)+"/api/ContentChannelItems", http.StatusFound)
	}))
	defer srv.Close()

	cfg := config(srv.URL)
	cfg[registry.ExtraHostsKey] = "localhost"
	if _, err := newSource(t, cfg).Fetch(context.Background()); err == nil {
		t.Error("a redirect to another host should fail")
	}
	if leaked != "" {
		t.Errorf("REST key was sent to another host: %q", leaked)
	}
}

func TestNewValidates(t *testing.T) {
	deps := registry.Deps{HTTP: registry.NewHTTPClient(nil, time.Second, nil)}
	base := func() registry.Config { return config("https://rock.example.org") }
	bad := map[string]func(registry.Config){
		"no url":        func(c registry.Config) { delete(c, "url") },
		"no key":        func(c registry.Config) { delete(c, "api_key") },
		"multiline key": func(c registry.Config) { c["api_key"] = "a\nb" },
		"no channel":    func(c registry.Config) { delete(c, "channel_id") },
		"zero channel":  func(c registry.Config) { c["channel_id"] = "0" },
		"no zone":       func(c registry.Config) { delete(c, "time_zone") },
		"bad zone":      func(c registry.Config) { c["time_zone"] = "Central" },
		"url query":     func(c registry.Config) { c["url"] = "https://rock.example.org/?x=1" },
	}
	for name, change := range bad {
		cfg := base()
		change(cfg)
		if _, err := (Connector{}).New(cfg, deps); err == nil {
			t.Errorf("%s: New should fail", name)
		}
	}
	if _, err := (Connector{}).New(base(), deps); err != nil {
		t.Errorf("valid config: %v", err)
	}
}

func TestHTMLToTextAndTruncate(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                                  "plain",
		"<p>a</p><p>b</p>":                       "a b",
		"<style>p{}</style>x<br/>y":              "x y",
		"Tom &amp; Jerry &lt;b&gt;":              "Tom & Jerry b",
		"<a href=\"x\">link</a> <!-- c --> done": "link done",
	} {
		if got := htmlToText(in); got != want {
			t.Errorf("htmlToText(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("é", 300)
	if got := truncate(long, 200); len(got) > 200 || !strings.HasSuffix(got, "…") || !strings.HasPrefix(got, "é") {
		t.Errorf("truncate: %d bytes, %q...", len(got), got[:6])
	}
}

func TestRegistered(t *testing.T) {
	if _, ok := registry.Get("rock"); !ok {
		t.Error("rock not registered")
	}
}
