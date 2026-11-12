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

	"github.com/jumpinjet22/doxacast/core"
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

	mu     sync.Mutex
	proc   *mpvProcess
	cancel context.CancelFunc
	files  []string
}

type manifest struct {
	Album  string `json:"album"`
	Photos []struct {
		Name string `json:"name"`
		URL  string `json:"url"`
		Size int64  `json:"size"`
	} `json:"photos"`
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
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		syncCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		files, err := s.sync(syncCtx, spec)
		cancel()
		if err != nil {
			s.logf("photo resync failed: %v", err)
		}
		s.mu.Lock()
		if ctx.Err() == nil && len(files) > 0 && (!equalStrings(files, s.files) || !s.proc.alive()) {
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

// sync downloads the manifest and any missing photos, removes photos no
// longer listed, and returns the local files. On error it still returns
// whatever is cached.
func (s *Slideshow) sync(ctx context.Context, spec core.SlideshowSpec) ([]string, error) {
	base, err := url.Parse(s.ServerURL + "/")
	if err != nil {
		return nil, err
	}
	murl, err := base.Parse(spec.ManifestURL)
	if err != nil {
		return nil, err
	}
	// The album name is the path segment before /manifest; use it to find the
	// cache even when the server is down.
	album := filepath.Base(filepath.Dir(murl.Path))
	dir, err := s.albumDir(album)
	if err != nil {
		return nil, err
	}

	var m manifest
	if err := s.getJSON(ctx, murl.String(), &m); err != nil {
		return cached(dir), err
	}

	keep := map[string]bool{}
	var firstErr error
	for _, ph := range m.Photos {
		name := filepath.Base(ph.Name)
		if name != ph.Name || strings.HasPrefix(name, ".") {
			continue
		}
		keep[name] = true
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
		if !e.IsDir() && !keep[e.Name()] && !strings.HasPrefix(e.Name(), ".tmp-") {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return cached(dir), firstErr
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
