package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jumpinjet22/doxaboard/core"
)

// Slideshow syncs photos from the server into a local cache and shows them
// with mpv. If the server is unreachable it shows whatever is cached, so a
// screen never goes black just because the network did.
type Slideshow struct {
	ServerURL string
	Token     string
	DataDir   string
	MpvPath   string
	MpvArgs   []string
	HTTP      *http.Client
	Logf      func(string, ...any)
	// Display returns current output settings (safe area for old TVs).
	Display func() core.DisplaySpec
	// ResyncEvery controls how often photos are re-synced while showing.
	ResyncEvery time.Duration
	// Now is the clock used for slide start and expiry times. Default time.Now.
	Now func() time.Time

	mu     sync.Mutex
	proc   *mpvProcess
	cancel context.CancelFunc
	files  []string
}

type manifest struct {
	Album  string          `json:"album"`
	Photos []manifestPhoto `json:"photos"`
}

type manifestPhoto struct {
	Name      string     `json:"name"`
	URL       string     `json:"url"`
	Size      int64      `json:"size"`
	StartsAt  *time.Time `json:"starts_at,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// window is when one cached photo may show. Either end may be nil.
type window struct {
	StartsAt  *time.Time `json:"starts_at,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// scheduleFile sits next to the cached photos and records their windows, so
// start and expiry times still apply when the server can't be reached.
const scheduleFile = ".schedule.json"

func (s *Slideshow) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Slideshow) logf(f string, a ...any) {
	if s.Logf != nil {
		s.Logf(f, a...)
	}
}

// Start begins showing photos.
func (s *Slideshow) Start(ctx context.Context, spec core.SlideshowSpec) error {
	_ = s.Stop()

	syncCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	files, err := s.sync(syncCtx, spec)
	cancel()
	if err != nil {
		s.logf("photo sync failed, using cache: %v", err)
	}
	if len(files) == 0 {
		return errors.New("no photos available")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.launch(spec, files); err != nil {
		return err
	}
	loopCtx, loopCancel := context.WithCancel(context.Background())
	s.cancel = loopCancel
	go s.resyncLoop(loopCtx, spec)
	return nil
}

// Stop stops the slideshow.
func (s *Slideshow) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.proc.stop()
	s.proc = nil
	return nil
}

func (s *Slideshow) launch(spec core.SlideshowSpec, files []string) error {
	s.proc.stop()
	playlist := filepath.Join(s.DataDir, "slideshow.m3u")
	if err := writeFileAtomic(playlist, []byte(strings.Join(files, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	interval := max(spec.IntervalSeconds, 3)
	args := append(baseMpvArgs(),
		"--image-display-duration="+strconv.Itoa(interval),
		"--loop-playlist=inf",
		"--keep-open=no",
		"--playlist="+playlist,
	)
	if spec.Shuffle {
		args = append(args, "--shuffle")
	}
	if s.Display != nil {
		if pct := s.Display().SafeAreaPercent; pct > 0 {
			r := strconv.FormatFloat(float64(pct)/100, 'f', 3, 64)
			for _, side := range []string{"left", "right", "top", "bottom"} {
				args = append(args, "--video-margin-ratio-"+side+"="+r)
			}
		}
	}
	args = append(args, s.MpvArgs...)
	proc, err := startMpv(s.MpvPath, args, s.Logf)
	if err != nil {
		return err
	}
	s.proc = proc
	s.files = files
	return nil
}

func (s *Slideshow) resyncLoop(ctx context.Context, spec core.SlideshowSpec) {
	every := s.ResyncEvery
	if every == 0 {
		every = 10 * time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		// Wake at the next start or expiry time as well as on the resync
		// tick, so a slide changes on time rather than up to a tick late.
		var boundary <-chan time.Time
		var timer *time.Timer
		if dir, err := s.specDir(spec); err == nil {
			if _, next := activeFiles(dir, s.now()); !next.IsZero() {
				timer = time.NewTimer(next.Sub(s.now()) + 50*time.Millisecond)
				boundary = timer.C
			}
		}
		var files []string
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-boundary:
			// No network needed: the windows are already on disk.
			if dir, err := s.specDir(spec); err == nil {
				files, _ = activeFiles(dir, s.now())
			}
		case <-t.C:
			if timer != nil {
				timer.Stop()
			}
			syncCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			var err error
			files, err = s.sync(syncCtx, spec)
			cancel()
			if err != nil {
				s.logf("photo resync failed: %v", err)
			}
		}
		s.mu.Lock()
		switch {
		case ctx.Err() != nil:
		case len(files) == 0:
			// A stale slide beats a black screen: keep what's showing.
			s.logf("no photos are current; keeping the last slideshow on screen")
		case !equalStrings(files, s.files) || !s.proc.alive():
			s.logf("photos changed (%d), restarting slideshow", len(files))
			if err := s.launch(spec, files); err != nil {
				s.logf("restart slideshow: %v", err)
			}
		}
		s.mu.Unlock()
	}
}

func (s *Slideshow) albumDir(album string) (string, error) {
	if album == "" || album != filepath.Base(album) || strings.HasPrefix(album, ".") {
		return "", fmt.Errorf("invalid album %q", album)
	}
	dir := filepath.Join(s.DataDir, "photos", album)
	return dir, os.MkdirAll(dir, 0o700)
}

// manifestURL resolves the spec's manifest against the server.
func (s *Slideshow) manifestURL(spec core.SlideshowSpec) (base, murl *url.URL, err error) {
	if base, err = url.Parse(s.ServerURL + "/"); err != nil {
		return nil, nil, err
	}
	murl, err = base.Parse(spec.ManifestURL)
	return base, murl, err
}

// specDir is the local cache for the spec's photos. The album name is the
// path segment before /manifest, so the cache can be found even when the
// server is down.
func (s *Slideshow) specDir(spec core.SlideshowSpec) (string, error) {
	_, murl, err := s.manifestURL(spec)
	if err != nil {
		return "", err
	}
	return s.albumDir(filepath.Base(filepath.Dir(murl.Path)))
}

// sync downloads the manifest and any missing photos, removes photos no
// longer listed, and returns the local files that should show right now.
// Photos that start later are downloaded too, ready for their start time.
// On error it still returns whatever is cached and current.
func (s *Slideshow) sync(ctx context.Context, spec core.SlideshowSpec) ([]string, error) {
	base, murl, err := s.manifestURL(spec)
	if err != nil {
		return nil, err
	}
	dir, err := s.specDir(spec)
	if err != nil {
		return nil, err
	}
	// Ask for upcoming slides as well, with their start and expiry times.
	q := murl.Query()
	q.Set("schedule", "1")
	murl.RawQuery = q.Encode()

	var m manifest
	if err := s.getJSON(ctx, murl.String(), &m); err != nil {
		files, _ := activeFiles(dir, s.now())
		return files, err
	}

	keep := map[string]bool{}
	windows := map[string]window{}
	var firstErr error
	for _, ph := range m.Photos {
		name := filepath.Base(ph.Name)
		if name != ph.Name || strings.HasPrefix(name, ".") {
			continue
		}
		keep[name] = true
		if ph.StartsAt != nil || ph.ExpiresAt != nil {
			windows[name] = window{StartsAt: ph.StartsAt, ExpiresAt: ph.ExpiresAt}
		}
		local := filepath.Join(dir, name)
		if st, err := os.Stat(local); err == nil && st.Size() == ph.Size {
			continue
		}
		purl, err := base.Parse(ph.URL)
		if err != nil {
			continue
		}
		if err := s.download(ctx, purl.String(), local); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() && !keep[e.Name()] && !strings.HasPrefix(e.Name(), ".") {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	if b, err := json.Marshal(windows); err == nil {
		if err := writeFileAtomic(filepath.Join(dir, scheduleFile), b, 0o600); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	files, _ := activeFiles(dir, s.now())
	return files, firstErr
}

// activeFiles returns the cached photos that should show at now, and the
// next time that answer changes (zero if it never does). It reads only the
// local cache, so it works with the server down.
func activeFiles(dir string, now time.Time) ([]string, time.Time) {
	windows := map[string]window{}
	if b, err := os.ReadFile(filepath.Join(dir, scheduleFile)); err == nil {
		_ = json.Unmarshal(b, &windows)
	}
	var next time.Time
	note := func(t *time.Time) {
		if t != nil && t.After(now) && (next.IsZero() || t.Before(next)) {
			next = *t
		}
	}
	var out []string
	for _, f := range cached(dir) {
		w := windows[filepath.Base(f)]
		note(w.StartsAt)
		note(w.ExpiresAt)
		if w.StartsAt != nil && now.Before(*w.StartsAt) {
			continue
		}
		if w.ExpiresAt != nil && !now.Before(*w.ExpiresAt) {
			continue
		}
		out = append(out, f)
	}
	return out, next
}

func cached(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

func (s *Slideshow) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return http.DefaultClient
}

func (s *Slideshow) get(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return resp, nil
}

func (s *Slideshow) getJSON(ctx context.Context, u string, v any) error {
	resp, err := s.get(ctx, u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v)
}

func (s *Slideshow) download(ctx context.Context, u, dest string) error {
	resp, err := s.get(ctx, u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, io.LimitReader(resp.Body, 200<<20)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
