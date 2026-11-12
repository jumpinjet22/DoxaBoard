// Package rock reads a Rock RMS content channel and turns its items into
// slides: title, image, start and expire dates, and order.
//
// It talks to Rock's v1 REST API with a REST key (the Authorization-Token
// header). The key is read-only in practice and is only ever sent to the
// configured Rock host. Images are loaded from Rock's public GetImage.ashx
// handler without the key.
package rock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jumpinjet22/doxaboard/connectors/registry"
	"github.com/jumpinjet22/doxaboard/slides"
)

func init() { registry.Register(Connector{}) }

// maxItems is how many channel items are requested, newest start date first.
const maxItems = 250

// Rock's ContentChannelItemStatus value for an approved item.
const statusApproved = 2

// Connector is the "rock" connector.
type Connector struct{}

// Info implements registry.Connector.
func (Connector) Info() registry.Info {
	return registry.Info{
		Name:  "rock",
		Title: "Rock RMS content channel",
		Description: "Shows the items of a Rock RMS content channel as slides, " +
			"with their image and their start and expire dates.",
		Status:     registry.StatusExperimental,
		Maintainer: "@jumpinjet22",
		Fields: []registry.Field{
			{Key: "url", Label: "Rock URL", Type: registry.FieldURL, Required: true,
				Help: "Your Rock server's address, e.g. https://rock.example.org"},
			{Key: "api_key", Label: "REST key", Type: registry.FieldSecret, Required: true,
				Help: "From Rock: Admin Tools > Security > REST Keys. It needs View access to the ContentChannelItems REST controller and to the channel."},
			{Key: "channel_id", Label: "Content channel ID", Type: registry.FieldNumber, Required: true,
				Help: "The number in the address bar when you open the channel in Rock (contentChannelId=...)."},
			{Key: "time_zone", Label: "Rock's time zone", Type: registry.FieldText, Required: true,
				Help: "Rock stores start and expire times in its own time zone. IANA name, e.g. America/Chicago."},
			{Key: "image_attribute", Label: "Image attribute key", Type: registry.FieldText, Default: "Image",
				Help: "The key of the item attribute that holds the graphic."},
			{Key: "approved_only", Label: "Only approved items", Type: registry.FieldBool, Default: "true",
				Help: "Skip items that are pending approval or denied."},
		},
	}
}

// New implements registry.Connector. It does no network I/O.
func (c Connector) New(cfg registry.Config, deps registry.Deps) (registry.Source, error) {
	cfg, err := registry.ValidateConfig(c.Info(), cfg)
	if err != nil {
		return nil, err
	}
	base, err := url.Parse(strings.TrimRight(cfg["url"], "/"))
	if err != nil {
		return nil, fmt.Errorf("rock: Rock URL: %w", err)
	}
	if base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("rock: Rock URL should be just the server address, e.g. https://rock.example.org")
	}
	key := cfg["api_key"]
	if strings.ContainsAny(key, "\r\n") {
		return nil, errors.New("rock: REST key must be a single line")
	}
	channel, err := strconv.Atoi(cfg["channel_id"])
	if err != nil || channel <= 0 {
		return nil, errors.New("rock: content channel ID must be a positive number")
	}
	loc, err := time.LoadLocation(cfg["time_zone"])
	if err != nil || cfg["time_zone"] == "Local" {
		return nil, fmt.Errorf("rock: time zone %q isn't an IANA name such as America/Chicago", cfg["time_zone"])
	}
	approvedOnly, _ := strconv.ParseBool(cfg["approved_only"])
	if deps.HTTP == nil {
		return nil, errors.New("rock: no HTTP client")
	}

	// The REST key rides in a custom header, which Go keeps on redirects.
	// Refuse any redirect that would carry it to another host or to plain http.
	hc := *deps.HTTP
	prev := hc.CheckRedirect
	hc.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if prev != nil {
			if err := prev(r, via); err != nil {
				return err
			}
		}
		if !strings.EqualFold(r.URL.Host, base.Host) || r.URL.Scheme != base.Scheme {
			return fmt.Errorf("rock: refusing redirect to %s://%s: the REST key is only sent to %s", r.URL.Scheme, r.URL.Host, base.Host)
		}
		return nil
	}

	logf := deps.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &source{base: base, key: key, channel: channel, loc: loc, imageKey: cfg["image_attribute"],
		approvedOnly: approvedOnly, http: &hc, logf: logf, now: now}, nil
}

type source struct {
	base         *url.URL
	key          string
	channel      int
	loc          *time.Location
	imageKey     string
	approvedOnly bool
	http         *http.Client
	logf         func(string, ...any)
	now          func() time.Time
}

// item is the part of Rock's ContentChannelItem this connector reads.
type item struct {
	ID               int                        `json:"Id"`
	Title            string                     `json:"Title"`
	Content          string                     `json:"Content"`
	Status           json.RawMessage            `json:"Status"`
	Priority         int                        `json:"Priority"`
	Order            int                        `json:"Order"`
	StartDateTime    *string                    `json:"StartDateTime"`
	ExpireDateTime   *string                    `json:"ExpireDateTime"`
	ModifiedDateTime *string                    `json:"ModifiedDateTime"`
	AttributeValues  map[string]json.RawMessage `json:"AttributeValues"`
}

// Fetch implements registry.Source.
func (s *source) Fetch(ctx context.Context) ([]slides.Slide, error) {
	q := url.Values{}
	q.Set("$filter", fmt.Sprintf("ContentChannelId eq %d", s.channel))
	q.Set("$orderby", "StartDateTime desc")
	q.Set("$top", strconv.Itoa(maxItems))
	q.Set("loadAttributes", "simple")
	u := *s.base
	u.Path = strings.TrimRight(u.Path, "/") + "/api/ContentChannelItems"
	u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization-Token", s.key)
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rock: fetch content channel items: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("rock: Rock refused the REST key (%s). Check the key, and that it has View access to the ContentChannelItems REST controller and to channel %d", resp.Status, s.channel)
	}
	if err := registry.CheckStatus(resp); err != nil {
		return nil, fmt.Errorf("rock: %w", err)
	}
	body, err := registry.ReadLimited(resp.Body, 0)
	if err != nil {
		return nil, fmt.Errorf("rock: read content channel items: %w", err)
	}
	var items []item
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("rock: the response isn't a list of content channel items (is the Rock URL right?): %w", err)
	}

	now := s.now()
	var skippedStatus, skippedExpired int
	type entry struct {
		slide    slides.Slide
		order    int
		priority int
		id       int
	}
	entries := make([]entry, 0, len(items))
	for _, it := range items {
		if it.ID <= 0 {
			continue
		}
		if s.approvedOnly && !approved(it.Status) {
			skippedStatus++
			continue
		}
		sl := slides.Slide{
			ID:    "rock-" + strconv.Itoa(it.ID),
			Title: truncate(htmlToText(it.Title), slides.MaxTitleLen),
			Body:  truncate(htmlToText(it.Content), slides.MaxBodyLen),
		}
		sl.StartsAt = s.parseTime(it.StartDateTime)
		sl.ExpiresAt = s.parseTime(it.ExpireDateTime)
		sl.UpdatedAt = s.parseTime(it.ModifiedDateTime)
		if sl.ExpiresAt != nil && !sl.ExpiresAt.After(now) {
			skippedExpired++ // a channel can hold years of old items
			continue
		}
		if sl.StartsAt != nil && sl.ExpiresAt != nil && !sl.ExpiresAt.After(*sl.StartsAt) {
			s.logf("rock: skipping item %d (%s): it expires before it starts", it.ID, sl.Title)
			continue
		}
		if img := s.imageURL(it.AttributeValues[s.imageKey]); img != "" {
			sl.Image = &slides.Image{URL: img, Alt: truncate(sl.Title, slides.MaxAltLen)}
		}
		if sl.Title == "" && sl.Image == nil {
			s.logf("rock: skipping item %d: no title and no image in attribute %q", it.ID, s.imageKey)
			continue
		}
		entries = append(entries, entry{slide: sl, order: it.Order, priority: it.Priority, id: it.ID})
	}
	if skippedStatus > 0 {
		s.logf("rock: skipped %d item(s) that aren't approved", skippedStatus)
	}
	if skippedExpired > 0 {
		s.logf("rock: skipped %d expired item(s)", skippedExpired)
	}

	// Rock's manual order first, then higher priority, then earlier start.
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.order != b.order {
			return a.order < b.order
		}
		if a.priority != b.priority {
			return a.priority > b.priority
		}
		as, bs := a.slide.StartsAt, b.slide.StartsAt
		if (as == nil) != (bs == nil) {
			return as == nil
		}
		if as != nil && !as.Equal(*bs) {
			return as.Before(*bs)
		}
		return a.id < b.id
	})
	if len(entries) > slides.MaxSlides {
		entries = entries[:slides.MaxSlides]
	}
	out := make([]slides.Slide, len(entries))
	for i, e := range entries {
		e.slide.Order = i + 1
		out[i] = e.slide
	}
	return out, nil
}

// approved reports whether a Status value means Approved. Rock sends the
// enum as a number; a name is accepted too. A missing status counts as
// approved, since channels that don't use approval may omit it.
func approved(raw json.RawMessage) bool {
	v := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if v == "" || v == "null" {
		return true
	}
	if n, err := strconv.Atoi(v); err == nil {
		return n == statusApproved
	}
	return strings.EqualFold(v, "Approved")
}

var timeLayouts = []string{"2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"}

// parseTime reads a Rock date. Rock sends wall-clock times in its own time
// zone with no offset; a value that does carry an offset is honored.
func (s *source) parseTime(v *string) *time.Time {
	if v == nil || strings.TrimSpace(*v) == "" {
		return nil
	}
	str := strings.TrimSpace(*v)
	if t, err := time.Parse(time.RFC3339Nano, str); err == nil {
		return &t
	}
	for _, layout := range timeLayouts {
		if t, err := time.ParseInLocation(layout, str, s.loc); err == nil {
			return &t
		}
	}
	s.logf("rock: ignoring a date Rock sent in an unknown format: %q", str)
	return nil
}

var guidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-(?:[0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12}$`)

// imageURL turns an image attribute value into an absolute URL. The value
// is normally a binary file GUID; a URL or a site-relative path also works.
// With loadAttributes=simple Rock wraps the value as {"Value": "..."}.
func (s *source) imageURL(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var val string
	var wrapped struct {
		Value string `json:"Value"`
	}
	if json.Unmarshal(raw, &val) != nil {
		if json.Unmarshal(raw, &wrapped) != nil {
			return ""
		}
		val = wrapped.Value
	}
	val = strings.TrimSpace(val)
	switch {
	case val == "":
		return ""
	case guidRE.MatchString(val):
		u := *s.base
		u.Path = strings.TrimRight(u.Path, "/") + "/GetImage.ashx"
		u.RawQuery = "guid=" + strings.ToLower(val)
		return u.String()
	}
	ref, err := url.Parse(val)
	if err != nil {
		return ""
	}
	abs := s.base.ResolveReference(ref)
	if (abs.Scheme != "http" && abs.Scheme != "https") || abs.Host == "" {
		return ""
	}
	return abs.String()
}

var (
	rawBlockRE = regexp.MustCompile(`(?is)<(script|style)\b.*?</(script|style)\s*>`)
	commentRE  = regexp.MustCompile(`(?s)<!--.*?-->`)
	tagRE      = regexp.MustCompile(`(?s)</?[a-zA-Z!?][^>]*>`)
)

// htmlToText turns Rock's HTML content into one line of plain text. Slides
// show the body literally, so no markup may survive.
func htmlToText(s string) string {
	if strings.ContainsAny(s, "<&") {
		s = rawBlockRE.ReplaceAllString(s, " ")
		s = commentRE.ReplaceAllString(s, " ")
		s = tagRE.ReplaceAllString(s, " ")
		s = html.UnescapeString(s)
		s = strings.NewReplacer("<", " ", ">", " ").Replace(s)
	}
	return strings.Join(strings.Fields(s), " ")
}

// truncate shortens s to at most max bytes on a rune boundary, with an ellipsis.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	const ell = "…"
	cut := max - len(ell)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + ell
}
