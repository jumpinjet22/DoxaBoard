package registry

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// MaxResponseBytes is the default cap for ReadLimited (feeds and API pages).
const MaxResponseBytes = 10 << 20

// ErrHostNotAllowed is returned when a connector tries to reach a host that
// isn't configured for its source.
var ErrHostNotAllowed = errors.New("host not allowed for this source")

// ErrTooLarge is returned by ReadLimited when a body exceeds the limit.
var ErrTooLarge = errors.New("response too large")

// NewHTTPClient returns the client handed to connectors as Deps.HTTP. It can
// only reach allowedHosts (case-insensitive, exact match), including after
// redirects, and gives up after timeout.
func NewHTTPClient(allowedHosts []string, timeout time.Duration, base http.RoundTripper) *http.Client {
	if base == nil {
		base = http.DefaultTransport
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	allowed := map[string]bool{}
	for _, h := range allowedHosts {
		allowed[strings.ToLower(h)] = true
	}
	check := func(r *http.Request) error {
		if r.URL.Scheme != "http" && r.URL.Scheme != "https" {
			return fmt.Errorf("%w: scheme %q", ErrHostNotAllowed, r.URL.Scheme)
		}
		if !allowed[strings.ToLower(r.URL.Hostname())] {
			return fmt.Errorf("%w: %s", ErrHostNotAllowed, r.URL.Hostname())
		}
		return nil
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: guardTransport{base: base, check: check},
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return check(r)
		},
	}
}

type guardTransport struct {
	base  http.RoundTripper
	check func(*http.Request) error
}

func (g guardTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := g.check(r); err != nil {
		return nil, err
	}
	if r.Header.Get("User-Agent") == "" {
		r = r.Clone(r.Context())
		r.Header.Set("User-Agent", "DoxaCast")
	}
	return g.base.RoundTrip(r)
}

// ReadLimited reads at most max bytes (MaxResponseBytes if max <= 0) and
// returns ErrTooLarge if there was more.
func ReadLimited(r io.Reader, max int64) ([]byte, error) {
	if max <= 0 {
		max = MaxResponseBytes
	}
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%w (over %d bytes)", ErrTooLarge, max)
	}
	return b, nil
}

// CheckStatus returns an error for non-2xx responses, including a short
// snippet of the body to make upstream errors debuggable.
func CheckStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	return fmt.Errorf("%s %s: %s: %s", resp.Request.Method, resp.Request.URL.Redacted(), resp.Status,
		strings.TrimSpace(string(snippet)))
}
