package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jumpinjet22/doxaboard/connectors/registry"
	"github.com/jumpinjet22/doxaboard/slides"
)

// Source is a configured content source with its secrets already decrypted.
type Source struct {
	ID        string
	Name      string
	Connector string
	Config    registry.Config
}

// CachedSlide is a slide that's been turned into a screen-ready JPEG.
type CachedSlide struct {
	ID              string     `json:"id"`
	Title           string     `json:"title,omitempty"`
	File            string     `json:"file"`
	Size            int64      `json:"size"`
	StartsAt        *time.Time `json:"starts_at,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	Order           int        `json:"order"`
	DurationSeconds int        `json:"duration_seconds,omitempty"`
	Tags            []string   `json:"tags,omitempty"`
}

// Active reports whether the slide should show at now.
func (c CachedSlide) Active(now time.Time) bool {
	return slides.Slide{StartsAt: c.StartsAt, ExpiresAt: c.ExpiresAt}.Active(now)
}

// Status is the outcome of the last fetch.
type Status struct {
	FetchedAt  time.Time `json:"fetched_at"`
	OK         bool      `json:"ok"`
	Error      string    `json:"error,omitempty"`
	Slides     int       `json:"slides"`
	Dropped    int       `json:"dropped"`
	DurationMS int64     `json:"duration_ms"`
}

// Manager fetches sources and owns the on-disk cache under Dir/<source id>/.
type Manager struct {
	Dir string
	// Files is handed to folder-style connectors (the server's photo dir).
	Files fs.FS
	Style TextStyle
	Logf  func(format string, args ...any)
	Now   func() time.Time
	// Transport is the base HTTP transport (tests swap it); nil = default.
	Transport http.RoundTripper

	locks sync.Map // source id -> *sync.Mutex
}

func (m *Manager) logf(f string, a ...any) {
	if m.Logf != nil {
		m.Logf(f, a...)
	}
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manager) dir(id string) string { return filepath.Join(m.Dir, filepath.Base(id)) }

func (m *Manager) lock(id string) func() {
	v, _ := m.locks.LoadOrStore(id, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// Refresh fetches a source and rebuilds its cache. On failure the previous
// slides are kept and only the status changes, so screens never go blank
// because a CMS had a bad moment.
func (m *Manager) Refresh(ctx context.Context, src Source) (result Status) {
	defer m.lock(src.ID)()
	start := m.now()
	// A panicking connector or image decoder must fail this one fetch, not
	// take down the server every screen depends on.
	defer func() {
		if r := recover(); r != nil {
			result = Status{FetchedAt: start, Error: fmt.Sprintf("internal error: %v", r)}
			m.logf("content source %s: panic during refresh: %v", src.ID, r)
			if prev, err := m.readStatus(src.ID); err == nil {
				result.Slides = prev.Slides
			}
			_ = m.writeJSON(src.ID, "status.json", result)
		}
	}()
	st := Status{FetchedAt: start}
	finish := func(err error) Status {
		st.DurationMS = time.Since(start).Milliseconds()
		if err != nil {
			st.OK, st.Error = false, err.Error()
			if prev, perr := m.readStatus(src.ID); perr == nil {
				st.Slides = prev.Slides // still serving the last good slides
			}
			m.logf("content source %s (%s): %v", src.ID, src.Name, err)
		} else {
			st.OK = true
		}
		if werr := m.writeJSON(src.ID, "status.json", st); werr != nil {
			m.logf("content source %s: saving status: %v", src.ID, werr)
		}
		return st
	}

	if err := os.MkdirAll(m.dir(src.ID), 0o750); err != nil {
		return finish(err)
	}
	conn, ok := registry.Get(src.Connector)
	if !ok {
		return finish(fmt.Errorf("unknown connector %q", src.Connector))
	}
	info := conn.Info()
	cfg, err := registry.ValidateConfig(info, src.Config)
	if err != nil {
		return finish(err)
	}
	client := registry.NewHTTPClient(registry.AllowedHosts(info, cfg), 30*time.Second, m.Transport)
	deps := registry.Deps{
		HTTP:  client,
		Files: m.Files,
		Logf:  func(f string, a ...any) { m.logf("content source "+src.ID+": "+f, a...) },
		Now:   m.now,
	}
	source, err := conn.New(cfg, deps)
	if err != nil {
		return finish(err)
	}
	fetched, err := source.Fetch(ctx)
	if err != nil {
		return finish(err)
	}

	// Drop anything the schema rejects; the feed itself is assembled by us,
	// so feed-level errors only mean "too many slides".
	if len(fetched) > slides.MaxSlides {
		st.Dropped += len(fetched) - slides.MaxSlides
		fetched = fetched[:slides.MaxSlides]
	}
	bad := map[int]bool{}
	for _, p := range slides.Validate(slides.Feed{Schema: slides.SchemaV1, Slides: fetched}) {
		if p.Severity == slides.Error && p.Slide >= 0 {
			bad[p.Slide] = true
			deps.Logf("dropping slide: %s", p)
		}
	}

	opener, _ := source.(registry.Opener)
	var cached []CachedSlide
	keep := map[string]bool{}
	for i, s := range fetched {
		if bad[i] {
			st.Dropped++
			continue
		}
		if ctx.Err() != nil {
			return finish(ctx.Err())
		}
		cs, err := m.prepare(ctx, src.ID, s, client, opener)
		if err != nil {
			st.Dropped++
			deps.Logf("dropping slide %s: %v", s.ID, err)
			continue
		}
		keep[cs.File] = true
		cached = append(cached, cs)
	}
	sort.SliceStable(cached, func(i, j int) bool { return cached[i].Order < cached[j].Order })
	if err := m.writeJSON(src.ID, "slides.json", cached); err != nil {
		return finish(err)
	}
	m.prune(src.ID, keep)
	st.Slides = len(cached)
	return finish(nil)
}

// prepare turns one slide into a cached JPEG, reusing an existing file when
// the slide hasn't changed.
func (m *Manager) prepare(ctx context.Context, sourceID string, s slides.Slide, client *http.Client, opener registry.Opener) (CachedSlide, error) {
	imgURL := ""
	if s.Image != nil {
		imgURL = s.Image.URL
	}
	updated := ""
	if s.UpdatedAt != nil {
		updated = s.UpdatedAt.UTC().Format(time.RFC3339)
	}
	h := sha256.Sum256([]byte(s.ID + "\x00" + imgURL + "\x00" + updated + "\x00" + s.Title + "\x00" + s.Body))
	name := hex.EncodeToString(h[:12]) + ".jpg"
	path := filepath.Join(m.dir(sourceID), name)

	cs := CachedSlide{
		ID: s.ID, Title: s.Title, File: name, StartsAt: s.StartsAt, ExpiresAt: s.ExpiresAt,
		Order: s.Order, DurationSeconds: s.DurationSeconds, Tags: s.Tags,
	}
	if fi, err := os.Stat(path); err == nil {
		cs.Size = fi.Size()
		return cs, nil
	}

	var jpg []byte
	var imgErr error
	if imgURL != "" {
		jpg, imgErr = m.fetchImage(ctx, imgURL, client, opener)
	}
	if jpg == nil {
		if strings.TrimSpace(s.Title) == "" && strings.TrimSpace(s.Body) == "" {
			if imgErr == nil {
				imgErr = errors.New("no image and no text")
			}
			return cs, imgErr
		}
		if imgErr != nil {
			m.logf("content source %s: slide %s: image failed (%v), showing text instead", sourceID, s.ID, imgErr)
		}
		var err error
		if jpg, err = RenderText(s.Title, s.Body, m.Style); err != nil {
			return cs, err
		}
	}
	if err := writeAtomic(path, jpg); err != nil {
		return cs, err
	}
	cs.Size = int64(len(jpg))
	return cs, nil
}

func (m *Manager) fetchImage(ctx context.Context, raw string, client *http.Client, opener registry.Opener) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if !u.IsAbs() {
		if opener == nil {
			return nil, fmt.Errorf("relative image URL %q from a source that can't open it", raw)
		}
		rc, err := opener.Open(ctx, raw)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return ProcessImage(rc)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := registry.CheckStatus(resp); err != nil {
		return nil, err
	}
	return ProcessImage(resp.Body)
}

// Slides returns the cached slides for a source (empty if never fetched).
func (m *Manager) Slides(id string) ([]CachedSlide, error) {
	b, err := os.ReadFile(filepath.Join(m.dir(id), "slides.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []CachedSlide
	return out, json.Unmarshal(b, &out)
}

// Status returns the last fetch status, or a zero Status if never fetched.
func (m *Manager) Status(id string) Status {
	st, _ := m.readStatus(id)
	return st
}

func (m *Manager) readStatus(id string) (Status, error) {
	var st Status
	b, err := os.ReadFile(filepath.Join(m.dir(id), "status.json"))
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(b, &st)
}

// OpenFile opens a cached slide image. name must be a plain file name.
func (m *Manager) OpenFile(id, name string) (*os.File, error) {
	if name != filepath.Base(name) || !strings.HasSuffix(name, ".jpg") || strings.HasPrefix(name, ".") {
		return nil, fs.ErrNotExist
	}
	return os.Open(filepath.Join(m.dir(id), name))
}

// Remove deletes a source's cache.
func (m *Manager) Remove(id string) error {
	defer m.lock(id)()
	return os.RemoveAll(m.dir(id))
}

func (m *Manager) prune(id string, keep map[string]bool) {
	entries, _ := os.ReadDir(m.dir(id))
	for _, e := range entries {
		n := e.Name()
		if strings.HasSuffix(n, ".jpg") && !keep[n] {
			_ = os.Remove(filepath.Join(m.dir(id), n))
		}
	}
}

func (m *Manager) writeJSON(id, name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(m.dir(id), name), b)
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
