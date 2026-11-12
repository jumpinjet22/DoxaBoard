// Package rss reads RSS 2.0 and Atom 1.0 feeds (blogs, news, podcasts,
// YouTube channels) and turns their items into slides.
package rss

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jumpinjet22/doxacast/connectors/registry"
	"github.com/jumpinjet22/doxacast/slides"
)

func init() { registry.Register(Connector{}) }

// XML namespaces the connector understands.
const (
	nsAtom    = "http://www.w3.org/2005/Atom"
	nsMedia   = "http://search.yahoo.com/mrss/"
	nsContent = "http://purl.org/rss/1.0/modules/content/"
	nsITunes  = "http://www.itunes.com/dtds/podcast-1.0.dtd"
	nsDC      = "http://purl.org/dc/elements/1.1/"
)

// Limits on config values.
const (
	maxAgeDaysLimit = 3650
)

// Connector is the "rss" connector.
type Connector struct{}

// Info implements registry.Connector.
func (Connector) Info() registry.Info {
	return registry.Info{
		Name:        "rss",
		Title:       "RSS / Atom feed",
		Description: "Shows the latest items from an RSS 2.0 or Atom feed: a blog, news site, podcast or YouTube channel.",
		Status:      registry.StatusOfficial,
		Maintainer:  "@jumpinjet22",
		Fields: []registry.Field{
			{Key: "url", Label: "Feed URL", Type: registry.FieldURL, Required: true,
				Help: "The RSS or Atom feed, e.g. https://example.org/feed/"},
			{Key: "max_items", Label: "Maximum items", Type: registry.FieldNumber, Default: "20",
				Help: fmt.Sprintf("Show at most this many items (1-%d), newest first as the feed lists them.", slides.MaxSlides)},
			{Key: "max_age_days", Label: "Hide items older than (days)", Type: registry.FieldNumber, Default: "0",
				Help: "Items expire this many days after they were published. 0 shows items regardless of age."},
			{Key: "require_image", Label: "Only items with an image", Type: registry.FieldBool, Default: "false",
				Help: "Skip items that have no image."},
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
		return nil, fmt.Errorf("rss: feed URL: %v", err)
	}
	maxItems, _ := strconv.Atoi(cfg["max_items"])
	if maxItems < 1 || maxItems > slides.MaxSlides {
		return nil, fmt.Errorf("rss: maximum items must be between 1 and %d", slides.MaxSlides)
	}
	maxAge, _ := strconv.Atoi(cfg["max_age_days"])
	if maxAge < 0 || maxAge > maxAgeDaysLimit {
		return nil, fmt.Errorf("rss: max age must be between 0 and %d days", maxAgeDaysLimit)
	}
	requireImage, _ := strconv.ParseBool(cfg["require_image"])
	if deps.HTTP == nil {
		return nil, errors.New("rss: no HTTP client")
	}
	logf := deps.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Time{} } // never treat items as already expired
	}
	return &source{
		url: u, maxItems: maxItems, maxAge: maxAge, requireImage: requireImage,
		http: deps.HTTP, logf: logf, now: now,
	}, nil
}

type source struct {
	url          *url.URL
	maxItems     int
	maxAge       int
	requireImage bool
	http         *http.Client
	logf         func(string, ...any)
	now          func() time.Time
}

// Fetch implements registry.Source.
func (s *source) Fetch(ctx context.Context) ([]slides.Slide, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml;q=0.9, text/xml;q=0.8, */*;q=0.1")
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch feed: %w", err)
	}
	defer resp.Body.Close()
	if err := registry.CheckStatus(resp); err != nil {
		return nil, err
	}
	body, err := registry.ReadLimited(resp.Body, 0)
	if err != nil {
		return nil, fmt.Errorf("read feed: %w", err)
	}
	base := s.url
	if resp.Request != nil && resp.Request.URL != nil {
		base = resp.Request.URL // the document's real URL, after redirects
	}
	entries, err := parseFeed(body)
	if err != nil {
		return nil, fmt.Errorf("feed %s: %w", s.url.Redacted(), err)
	}
	return s.toSlides(entries, base), nil
}

// entry is a feed item, format-independent.
type entry struct {
	id, link, title    string
	summary, content   string // HTML
	published, updated string
	images             []string // explicit image URLs, in preference order
}

func parseFeed(b []byte) ([]entry, error) {
	root, err := parseXML(b)
	if err != nil {
		return nil, err
	}
	switch {
	case root.name.Local == "rss":
		ch := root.first("", "channel")
		if ch == nil {
			return nil, errors.New("RSS feed has no <channel>")
		}
		var out []entry
		for _, it := range ch.all("", "item") {
			out = append(out, rssEntry(it))
		}
		return out, nil
	case root.name.Local == "feed" && (root.name.Space == nsAtom || root.name.Space == ""):
		ns := root.name.Space
		var out []entry
		for _, it := range root.all(ns, "entry") {
			out = append(out, atomEntry(it, ns))
		}
		return out, nil
	}
	return nil, fmt.Errorf("not an RSS 2.0 or Atom feed (root element <%s>)", root.name.Local)
}

func rssEntry(it *node) entry {
	e := entry{
		id:      it.first("", "guid").value(),
		link:    it.first("", "link").value(),
		title:   htmlToText(it.first("", "title").content()),
		summary: it.first("", "description").content(),
		content: it.first(nsContent, "encoded").content(),
	}
	if e.link == "" {
		e.link = atomLink(it, nsAtom)
	}
	e.published = firstNonEmpty(it.first("", "pubDate").value(), it.first(nsDC, "date").value(),
		it.first(nsAtom, "published").value())
	e.updated = firstNonEmpty(it.first(nsAtom, "updated").value(), e.published)
	e.images = mediaImages(it)
	for _, enc := range it.all("", "enclosure") {
		if isImageType(enc.get("type")) {
			e.images = append(e.images, enc.get("url"))
		}
	}
	if img := it.first(nsITunes, "image"); img != nil {
		e.images = append(e.images, firstNonEmpty(img.get("href"), img.value()))
	}
	return e
}

func atomEntry(it *node, ns string) entry {
	e := entry{
		id:   it.first(ns, "id").value(),
		link: atomLink(it, ns),
	}
	e.title, _ = atomText(it.first(ns, "title"))
	_, e.summary = atomText(it.first(ns, "summary"))
	if c := it.first(ns, "content"); c != nil && c.get("src") == "" {
		_, e.content = atomText(c)
	}
	e.published = firstNonEmpty(it.first(ns, "published").value(), it.first(ns, "updated").value())
	e.updated = firstNonEmpty(it.first(ns, "updated").value(), e.published)
	e.images = mediaImages(it)
	for _, l := range it.all(ns, "link") {
		if l.get("rel") == "enclosure" && isImageType(l.get("type")) {
			e.images = append(e.images, l.get("href"))
		}
	}
	if img := it.first(nsITunes, "image"); img != nil {
		e.images = append(e.images, img.get("href"))
	}
	return e
}

// atomText reads an Atom text construct, returning it as plain text and as
// HTML.
func atomText(n *node) (text, htmlSrc string) {
	if n == nil {
		return "", ""
	}
	switch n.get("type") {
	case "html", "text/html":
		h := n.content()
		return htmlToText(h), h
	case "xhtml", "application/xhtml+xml":
		h := n.innerHTML()
		return htmlToText(h), h
	default: // "text": escape it so it survives being treated as HTML
		t := n.content()
		return collapse(t), html.EscapeString(t)
	}
}

// atomLink returns the alternate link of an Atom entry (or an atom:link in
// an RSS item).
func atomLink(it *node, ns string) string {
	var fallback string
	for _, l := range it.all(ns, "link") {
		switch l.get("rel") {
		case "", "alternate":
			if t := l.get("type"); t == "" || strings.Contains(t, "html") {
				return l.get("href")
			}
			if fallback == "" {
				fallback = l.get("href")
			}
		}
	}
	return fallback
}

// mediaImages returns Media RSS images: media:content that is an image,
// then thumbnails. Both may be wrapped in media:group.
func mediaImages(it *node) []string {
	scopes := append([]*node{it}, it.all(nsMedia, "group")...)
	var contents, thumbs []string
	for _, sc := range scopes {
		for _, mc := range sc.all(nsMedia, "content") {
			if mc.get("medium") == "image" || isImageType(mc.get("type")) ||
				(mc.get("medium") == "" && mc.get("type") == "" && looksLikeImageURL(mc.get("url"))) {
				contents = append(contents, mc.get("url"))
			}
			for _, th := range mc.all(nsMedia, "thumbnail") {
				thumbs = append(thumbs, th.get("url"))
			}
		}
		for _, th := range sc.all(nsMedia, "thumbnail") {
			thumbs = append(thumbs, th.get("url"))
		}
	}
	return append(contents, thumbs...)
}

func isImageType(t string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(t)), "image/")
}

func looksLikeImageURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	p := strings.ToLower(u.Path)
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp", ".gif", ".avif"} {
		if strings.HasSuffix(p, ext) {
			return true
		}
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// resolveHTTP resolves ref against base and returns it only if the result
// is an absolute http(s) URL with a host.
func resolveHTTP(base *url.URL, ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", false
	}
	u, err := url.Parse(ref)
	if err != nil {
		return "", false
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", false
	}
	return u.String(), true
}

// toSlides maps entries to slides in feed order, applying the source's
// options. feedURL is the feed's real URL, for resolving relative links.
func (s *source) toSlides(entries []entry, feedURL *url.URL) []slides.Slide {
	now := s.now()
	out := []slides.Slide{}
	seen := map[string]bool{}
	for _, e := range entries {
		if len(out) >= s.maxItems {
			break
		}
		// Relative URLs resolve against the item's link, then the feed URL.
		base := feedURL
		link, linkOK := resolveHTTP(feedURL, e.link)
		if linkOK {
			base, _ = url.Parse(link)
		}

		title := truncate(e.title, slides.MaxTitleLen)
		image := ""
		candidates := append([]string(nil), e.images...)
		candidates = append(candidates, imgSources(e.content)...)
		candidates = append(candidates, imgSources(e.summary)...)
		for _, c := range candidates {
			if u, ok := resolveHTTP(base, c); ok {
				image = u
				break
			}
		}
		if title == "" && image == "" {
			continue
		}
		if s.requireImage && image == "" {
			continue
		}

		id := e.id
		if id == "" && linkOK {
			id = link
		}
		if id == "" {
			id = firstNonEmpty(e.link)
		}
		if id == "" {
			id = shortHash(e.title + e.published)
		}
		if len([]rune(id)) > slides.MaxIDLen {
			id = shortHash(id)
		}
		if seen[id] {
			s.logf("rss: skipping item with duplicate id %q", id)
			continue
		}
		seen[id] = true

		body := htmlToText(e.summary)
		if body == "" {
			body = htmlToText(e.content)
		}
		if body == title {
			body = ""
		}

		sl := slides.Slide{
			ID:    id,
			Title: title,
			Body:  truncate(body, slides.MaxBodyLen),
			Order: len(out) * 10,
		}
		if image != "" {
			alt := title
			if alt == "" {
				alt = body // untitled item: describe the image with its text
			}
			sl.Image = &slides.Image{URL: image, Alt: truncate(alt, slides.MaxAltLen)}
		}
		published, pubOK := parseDate(e.published)
		if !pubOK {
			published, pubOK = parseDate(e.updated)
		}
		if t, ok := parseDate(e.updated); ok {
			sl.UpdatedAt = &t
		} else if pubOK {
			t := published
			sl.UpdatedAt = &t
		}
		if s.maxAge > 0 && pubOK {
			exp := published.AddDate(0, 0, s.maxAge)
			if !now.Before(exp) {
				// Already too old: skip it so it doesn't use up max_items.
				continue
			}
			sl.ExpiresAt = &exp
		}
		out = append(out, sl)
	}
	return out
}
