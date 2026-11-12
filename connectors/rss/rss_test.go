package rss

import (
	"context"
	"embed"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
			Name:   "wordpress",
			Config: registry.Config{"url": "{{server}}/feed/"},
			Routes: map[string]connectortest.Response{
				"/feed/": {ContentType: "application/rss+xml; charset=UTF-8", Body: mustRead(t, "testdata/wordpress.xml")},
			},
			WantIDs: []string{
				"https://gracecommunity.example.org/?p=2207",
				"https://gracecommunity.example.org/?p=2190",
				"https://gracecommunity.example.org/?p=2195",
			},
		},
		{
			Name:   "media_rss",
			Config: registry.Config{"url": "{{server}}/rss"},
			Routes: map[string]connectortest.Response{
				"/rss": {ContentType: "application/rss+xml", Body: mustRead(t, "testdata/media.xml")},
			},
			WantIDs: []string{
				"tag:photos.example.net,2025:album-881",
				"tag:photos.example.net,2025:album-880",
				"podcast-112",
				"tag:photos.example.net,2025:album-879",
				"https://photos.example.net/announcements",
				"https://photos.example.net/albums/sunset",
			},
		},
		{
			Name:   "atom",
			Config: registry.Config{"url": "{{server}}/feed.atom"},
			Routes: map[string]connectortest.Response{
				"/feed.atom": {ContentType: "application/atom+xml", Body: mustRead(t, "testdata/atom.xml")},
			},
			WantIDs: []string{
				"urn:uuid:9d1c0f53-0d7e-4b8e-8a7b-6f3e0b0d1a01",
				"tag:kids.example.com,2025-12-20:nursery",
				"tag:kids.example.com,2025-12-22:christmas-program",
			},
		},
		{
			Name:   "require_image",
			Config: registry.Config{"url": "{{server}}/rss", "require_image": "true", "max_items": "3"},
			Routes: map[string]connectortest.Response{
				"/rss": {Body: mustRead(t, "testdata/media.xml")},
			},
			WantIDs: []string{
				"tag:photos.example.net,2025:album-881",
				"tag:photos.example.net,2025:album-880",
				"podcast-112",
			},
		},
		{
			Name:    "html_error_page",
			Config:  registry.Config{"url": "{{server}}/feed"},
			Routes:  map[string]connectortest.Response{"/feed": {Body: []byte("<html><body><h1>Maintenance</h1></body></html>")}},
			WantErr: true,
		},
	})
}

// fetch serves body and returns the slides for cfg (url is filled in).
func fetch(t *testing.T, body string, cfg registry.Config) ([]slides.Slide, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	if cfg == nil {
		cfg = registry.Config{}
	}
	if cfg["url"] == "" {
		cfg["url"] = srv.URL + "/blog/feed/"
	} else {
		cfg["url"] = strings.ReplaceAll(cfg["url"], "{{server}}", srv.URL)
	}
	info := Connector{}.Info()
	cfg, err := registry.ValidateConfig(info, cfg)
	if err != nil {
		t.Fatal(err)
	}
	src, err := Connector{}.New(cfg, registry.Deps{
		HTTP: registry.NewHTTPClient(registry.AllowedHosts(info, cfg), 5*time.Second, nil),
		Logf: t.Logf,
		Now:  func() time.Time { return connectortest.DefaultNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return got, srv.URL
}

func byID(ss []slides.Slide) map[string]slides.Slide {
	m := map[string]slides.Slide{}
	for _, s := range ss {
		m[s.ID] = s
	}
	return m
}

func TestWordPressMapping(t *testing.T) {
	got, _ := fetch(t, string(mustRead(t, "testdata/wordpress.xml")), nil)
	if len(got) != 3 {
		t.Fatalf("got %d slides, want 3", len(got))
	}
	first := got[0]
	if want := "Christmas Eve Candlelight Services – 4pm & 6pm"; first.Title != want {
		t.Errorf("title = %q, want %q", first.Title, want)
	}
	if want := "Join us for one of our two candlelight services on Christmas Eve. Childcare is available for ages 0–4 at the 4pm service. Bring a friend! The post Christmas Eve Candlelight Services – 4pm & 6pm appeared first on Grace Community Church."; first.Body != want {
		t.Errorf("body =\n%q\nwant\n%q", first.Body, want)
	}
	if first.Image == nil || first.Image.URL != "https://gracecommunity.example.org/wp-content/uploads/2025/12/candles-1024x576.jpg" {
		t.Errorf("image = %+v", first.Image)
	}
	if first.Image.Alt != first.Title {
		t.Errorf("alt = %q, want the title", first.Image.Alt)
	}
	if want := time.Date(2025, 12, 15, 14, 3, 12, 0, time.UTC); first.UpdatedAt == nil || !first.UpdatedAt.Equal(want) {
		t.Errorf("updated_at = %v, want %v", first.UpdatedAt, want)
	}
	if first.ExpiresAt != nil {
		t.Errorf("expires_at set without max_age_days: %v", first.ExpiresAt)
	}

	retreat := got[1]
	if retreat.Image == nil || retreat.Image.URL != "https://gracecommunity.example.org/wp-content/uploads/2025/12/retreat.png" {
		t.Errorf("relative <img> should resolve against the item link: %+v", retreat.Image)
	}
	if want := time.Date(2025, 12, 3, 14, 30, 0, 0, time.UTC); retreat.UpdatedAt == nil || !retreat.UpdatedAt.Equal(want) {
		t.Errorf("EST date parsed as %v, want %v", retreat.UpdatedAt, want)
	}
	if want := "Grades 6–12 head to Camp Pinewood January 16–18. Early-bird price ends December 20 …"; retreat.Body != want {
		t.Errorf("body = %q, want %q", retreat.Body, want)
	}

	// Entity-escaped HTML (not CDATA), with double-escaped entities inside.
	notes := got[2]
	if want := "Isaiah 40:1–11. “Comfort, comfort my people.”"; notes.Body != want {
		t.Errorf("body = %q, want %q", notes.Body, want)
	}
	if notes.Image != nil {
		t.Errorf("unexpected image %+v", notes.Image)
	}
	for i, s := range got {
		if s.Order != i*10 {
			t.Errorf("slide %d order = %d", i, s.Order)
		}
	}
}

func TestMediaImagePriority(t *testing.T) {
	got, _ := fetch(t, string(mustRead(t, "testdata/media.xml")), nil)
	m := byID(got)
	want := map[string]string{
		"tag:photos.example.net,2025:album-881":    "https://cdn.photos.example.net/881/cover.jpg", // image media:content beats a video's thumbnail
		"tag:photos.example.net,2025:album-880":    "https://cdn.photos.example.net/880/food.webp", // inside media:group
		"podcast-112":                              "https://cdn.photos.example.net/podcast/112.jpg",
		"tag:photos.example.net,2025:album-879":    "https://cdn.photos.example.net/879/volunteers.png",
		"https://photos.example.net/albums/sunset": "https://photos.example.net/albums/thumbs/sunset.jpg",
	}
	for id, url := range want {
		s, ok := m[id]
		if !ok || s.Image == nil || s.Image.URL != url {
			t.Errorf("%s: image %+v, want %s", id, s.Image, url)
		}
	}
	if s := m["https://photos.example.net/announcements"]; s.Image != nil {
		t.Errorf("announcements should have no image, got %+v", s.Image)
	}
	if want := time.Date(2025, 12, 28, 23, 45, 0, 0, time.UTC); m["tag:photos.example.net,2025:album-881"].UpdatedAt == nil ||
		!m["tag:photos.example.net,2025:album-881"].UpdatedAt.Equal(want) {
		t.Errorf("updated_at = %v", m["tag:photos.example.net,2025:album-881"].UpdatedAt)
	}
}

func TestAtomMapping(t *testing.T) {
	got, srv := fetch(t, string(mustRead(t, "testdata/atom.xml")), nil)
	if len(got) != 3 {
		t.Fatalf("got %d slides", len(got))
	}
	vbs, nursery, xmas := got[0], got[1], got[2]
	if vbs.Title != "VBS 2026 volunteers needed" {
		t.Errorf("html title = %q", vbs.Title)
	}
	if vbs.Body != "We need 40 volunteers for Vacation Bible School this summer. Sign up at the kids desk." {
		t.Errorf("body should prefer summary, got %q", vbs.Body)
	}
	if vbs.Image == nil || vbs.Image.URL != "https://kids.example.com/news/images/vbs.jpg" {
		t.Errorf("image = %+v", vbs.Image)
	}
	if want := time.Date(2025, 12, 29, 21, 0, 0, 0, time.UTC); vbs.UpdatedAt == nil || !vbs.UpdatedAt.Equal(want) {
		t.Errorf("updated_at = %v, want %v", vbs.UpdatedAt, want)
	}
	if nursery.Body != "Starting January 4, nursery check-in moves to the east lobby." {
		t.Errorf("xhtml body = %q", nursery.Body)
	}
	// Relative link resolves against the feed URL; the image against the link.
	if nursery.Image == nil || nursery.Image.URL != srv+"/img/east-lobby.jpg" {
		t.Errorf("image = %+v", nursery.Image)
	}
	if xmas.Body != "Thank you to every kid who sang, and every parent who sewed a shepherd costume." {
		t.Errorf("text body = %q", xmas.Body)
	}
	if xmas.Image == nil || xmas.Image.URL != "https://media.example.com/kids/christmas-program-thumb.jpg" {
		t.Errorf("media:thumbnail should beat the enclosure link: %+v", xmas.Image)
	}
}

const itemTmpl = `<item><title>Item %d</title><guid>id-%d</guid><pubDate>%s</pubDate><description>Body %d</description></item>`

func feedOf(items ...string) string {
	return `<?xml version="1.0"?><rss version="2.0"><channel><title>T</title>` + strings.Join(items, "") + `</channel></rss>`
}

func TestMaxItems(t *testing.T) {
	var items []string
	for i := range 30 {
		items = append(items, fmt.Sprintf(itemTmpl, i, i, "Mon, 01 Dec 2025 10:00:00 +0000", i))
	}
	got, _ := fetch(t, feedOf(items...), nil)
	if len(got) != 20 {
		t.Errorf("default max_items: got %d, want 20", len(got))
	}
	got, _ = fetch(t, feedOf(items...), registry.Config{"max_items": "5"})
	if len(got) != 5 || got[0].ID != "id-0" || got[4].ID != "id-4" || got[4].Order != 40 {
		t.Errorf("max_items=5: got %d slides starting %v", len(got), got)
	}
}

func TestMaxAgeDays(t *testing.T) {
	// Now is 2026-01-01T12:00:00Z.
	body := feedOf(
		fmt.Sprintf(itemTmpl, 1, 1, "Tue, 30 Dec 2025 12:00:00 +0000", 1), // 2 days old
		fmt.Sprintf(itemTmpl, 2, 2, "Mon, 01 Dec 2025 12:00:00 +0000", 2), // 31 days old
		fmt.Sprintf(itemTmpl, 3, 3, "not a date", 3),
	)
	got, _ := fetch(t, body, registry.Config{"max_age_days": "7"})
	m := byID(got)
	if s, ok := m["id-1"]; !ok || s.ExpiresAt == nil || !s.ExpiresAt.Equal(time.Date(2026, 1, 6, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("id-1: %+v", s)
	}
	if _, ok := m["id-2"]; ok {
		t.Error("id-2 is older than max_age_days and should be skipped")
	}
	if s, ok := m["id-3"]; !ok || s.ExpiresAt != nil || s.UpdatedAt != nil {
		t.Errorf("id-3 (unparseable date) should be kept without dates: %+v", s)
	}

	got, _ = fetch(t, body, nil)
	if len(got) != 3 || got[0].ExpiresAt != nil {
		t.Errorf("max_age_days=0 should keep everything without expiry: %+v", got)
	}
}

func TestRequireImage(t *testing.T) {
	body := feedOf(
		`<item><title>No image</title><guid>a</guid></item>`,
		`<item><title>Has image</title><guid>b</guid><enclosure url="https://cdn.example.org/b.jpg" type="image/jpeg" length="1"/></item>`,
		`<item><title>Has audio</title><guid>c</guid><enclosure url="https://cdn.example.org/c.mp3" type="audio/mpeg" length="1"/></item>`,
	)
	got, _ := fetch(t, body, registry.Config{"require_image": "true"})
	if len(got) != 1 || got[0].ID != "b" {
		t.Errorf("got %+v", got)
	}
	got, _ = fetch(t, body, nil)
	if len(got) != 3 {
		t.Errorf("without require_image: got %d", len(got))
	}
}

func TestIDFallbacks(t *testing.T) {
	body := feedOf(
		`<item><title>Has guid</title><guid>guid-1</guid><link>https://example.org/a</link></item>`,
		`<item><title>Link only</title><link>https://example.org/b</link></item>`,
		`<item><title>Relative link</title><link>/c</link></item>`,
		`<item><title>Nothing but a title</title><pubDate>Mon, 01 Dec 2025 10:00:00 +0000</pubDate></item>`,
		`<item><title>Duplicate</title><guid>guid-1</guid></item>`,
		`<item><title>Long guid</title><guid>`+strings.Repeat("x", 300)+`</guid></item>`,
	)
	got, srv := fetch(t, body, nil)
	ids := make([]string, len(got))
	for i, s := range got {
		ids[i] = s.ID
	}
	want := []string{
		"guid-1",
		"https://example.org/b",
		srv + "/c",
		shortHash("Nothing but a title" + "Mon, 01 Dec 2025 10:00:00 +0000"),
		shortHash(strings.Repeat("x", 300)),
	}
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Errorf("ids\n got %q\nwant %q", ids, want)
	}
	if len(want[3]) != 16 {
		t.Errorf("hash id %q should be 16 hex chars", want[3])
	}
	if got[0].Title != "Has guid" {
		t.Error("duplicate guid should keep the first occurrence")
	}
	// Same input, same IDs.
	again, _ := fetch(t, body, nil)
	if again[3].ID != got[3].ID {
		t.Error("hash IDs must be stable")
	}
}

func TestSkipsItemsWithoutTitleOrImage(t *testing.T) {
	body := feedOf(
		`<item><guid>a</guid><description>Only a body</description></item>`,
		`<item><guid>b</guid><description><![CDATA[<img src="https://cdn.example.org/b.jpg">]]></description></item>`,
	)
	got, _ := fetch(t, body, nil)
	if len(got) != 1 || got[0].ID != "b" || got[0].Image == nil {
		t.Errorf("got %+v", got)
	}
}

func TestImageEdgeCases(t *testing.T) {
	body := feedOf(
		// Tracking pixel and data: URI placeholder are skipped; lazy-load src is used.
		`<item><title>Lazy</title><guid>a</guid><link>https://blog.example.org/posts/a/</link><description><![CDATA[
			<img src="https://feeds.example.org/pixel.gif" width="1" height="1">
			<img src="data:image/gif;base64,R0lGOD" data-src="../img/a.jpg?w=800&amp;h=600">
		]]></description></item>`,
		// javascript: URLs are never used.
		`<item><title>Hostile</title><guid>b</guid><enclosure url="javascript:alert(1)" type="image/png" length="1"/></item>`,
		// Protocol-relative.
		`<item><title>Proto</title><guid>c</guid><media:thumbnail xmlns:media="http://search.yahoo.com/mrss/" url="//cdn.example.org/c.jpg"/></item>`,
	)
	got, srv := fetch(t, body, nil)
	m := byID(got)
	if s := m["a"]; s.Image == nil || s.Image.URL != "https://blog.example.org/posts/img/a.jpg?w=800&h=600" {
		t.Errorf("a: %+v", s.Image)
	}
	if s := m["b"]; s.Image != nil {
		t.Errorf("b: javascript: image should be dropped, got %+v", s.Image)
	}
	if s := m["c"]; s.Image == nil || !strings.HasPrefix(srv, "http:") || s.Image.URL != "http://cdn.example.org/c.jpg" {
		t.Errorf("c: %+v", s.Image)
	}
}

func TestTruncation(t *testing.T) {
	long := strings.Repeat("Grace and peace to you. ", 100)
	body := feedOf(`<item><guid>a</guid><title>` + strings.Repeat("Ü", 300) + `</title><description>` + long + `</description></item>`)
	got, _ := fetch(t, body, nil)
	s := got[0]
	if n := utf8.RuneCountInString(s.Title); n != slides.MaxTitleLen || !strings.HasSuffix(s.Title, "…") {
		t.Errorf("title: %d runes, %q", n, s.Title[len(s.Title)-10:])
	}
	if n := utf8.RuneCountInString(s.Body); n > slides.MaxBodyLen || !strings.HasSuffix(s.Body, "…") {
		t.Errorf("body: %d runes", n)
	}
	if !utf8.ValidString(s.Title) || !utf8.ValidString(s.Body) {
		t.Error("truncation produced invalid UTF-8")
	}
}

func TestHTMLToText(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain   text\n\twith  spaces", "plain text with spaces"},
		{"<p>One</p><p>Two</p>", "One Two"},
		{"Line<br>break<br/>again", "Line break again"},
		{"<b>Bold</b>face and <a href='x'>link</a>s", "Boldface and links"},
		{"Fish &amp; chips &#8211; &ldquo;yum&rdquo;&nbsp;!", "Fish & chips – “yum” !"},
		{"<script>var a = '<p>';</script>Visible<style>p{}</style>", "Visible"},
		{"<!-- comment <p> -->Shown", "Shown"},
		{"1 < 2 and 3 > 2", "1 < 2 and 3 > 2"},
		{"a <b c", "a <b c"},
		{`<img alt="x > y" src="a.jpg">Caption`, "Caption"},
		{"&lt;p&gt; is a tag", "<p> is a tag"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := htmlToText(tt.in); got != tt.want {
			t.Errorf("htmlToText(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseDate(t *testing.T) {
	want := time.Date(2025, 12, 1, 15, 4, 5, 0, time.UTC)
	for _, s := range []string{
		"Mon, 01 Dec 2025 15:04:05 +0000",
		"Mon, 1 Dec 2025 15:04:05 +0000",
		"Mon, 01 Dec 2025 15:04:05 GMT",
		"Mon, 01 Dec 2025 10:04:05 EST",
		"Mon, 01 Dec 2025 07:04:05 PST",
		"01 Dec 2025 15:04:05 +0000",
		"2025-12-01T15:04:05Z",
		"2025-12-01T10:04:05-05:00",
		"2025-12-01T15:04:05.000Z",
		"  Mon, 01 Dec 2025 15:04:05 +0000 \n",
	} {
		got, ok := parseDate(s)
		if !ok || !got.Equal(want) {
			t.Errorf("parseDate(%q) = %v, %v", s, got, ok)
		}
	}
	for _, s := range []string{"", "yesterday", "13/45/2025"} {
		if _, ok := parseDate(s); ok {
			t.Errorf("parseDate(%q) should fail", s)
		}
	}
}

func TestCharsets(t *testing.T) {
	// "Café" in ISO-8859-1, and a windows-1252 curly apostrophe.
	latin1 := "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><rss version=\"2.0\"><channel><item><guid>a</guid><title>Caf\xe9</title></item></channel></rss>"
	cp := "<?xml version=\"1.0\" encoding=\"windows-1252\"?><rss version=\"2.0\"><channel><item><guid>a</guid><title>It\x92s here</title></item></channel></rss>"
	if got, _ := fetch(t, latin1, nil); got[0].Title != "Café" {
		t.Errorf("latin1 title %q", got[0].Title)
	}
	if got, _ := fetch(t, cp, nil); got[0].Title != "It’s here" {
		t.Errorf("cp1252 title %q", got[0].Title)
	}
}

func TestParseErrors(t *testing.T) {
	for name, body := range map[string]string{
		"empty":       "",
		"html":        "<!doctype html><html><body>Not a feed</body></html>",
		"rdf":         `<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"></rdf:RDF>`,
		"no channel":  `<rss version="2.0"></rss>`,
		"truncated":   `<rss version="2.0"><channel><item><title>Half`,
		"bad charset": `<?xml version="1.0" encoding="EBCDIC"?><rss version="2.0"><channel></channel></rss>`,
	} {
		if _, err := parseFeed([]byte(body)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if es, err := parseFeed([]byte(`<rss version="2.0"><channel><title>Empty</title></channel></rss>`)); err != nil || len(es) != 0 {
		t.Errorf("an empty but valid feed is not an error: %v %v", es, err)
	}
}

func TestNewValidates(t *testing.T) {
	deps := registry.Deps{HTTP: registry.NewHTTPClient(nil, time.Second, nil)}
	for _, cfg := range []registry.Config{
		{},
		{"url": "file:///etc/passwd"},
		{"url": "https://example.org/feed", "max_items": "0"},
		{"url": "https://example.org/feed", "max_items": "501"},
		{"url": "https://example.org/feed", "max_items": "ten"},
		{"url": "https://example.org/feed", "max_age_days": "-1"},
		{"url": "https://example.org/feed", "require_image": "maybe"},
	} {
		if _, err := (Connector{}).New(cfg, deps); err == nil {
			t.Errorf("New(%v) should fail", cfg)
		}
	}
	if _, err := (Connector{}).New(registry.Config{"url": "https://example.org/feed"}, deps); err != nil {
		t.Errorf("defaults should be valid: %v", err)
	}
}
