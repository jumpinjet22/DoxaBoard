package live

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MediaMTX talks to the relay's control API. It must only ever be reached
// on localhost: the API can reconfigure the relay.
type MediaMTX struct {
	BaseURL string // e.g. http://127.0.0.1:9997
	HTTP    *http.Client
}

// ErrRelayUnreachable means the relay's control API didn't answer.
var ErrRelayUnreachable = errors.New("can't reach the relay's control API (is mediamtx running with api: true?)")

func (m *MediaMTX) client() *http.Client {
	if m.HTTP != nil {
		return m.HTTP
	}
	return &http.Client{Timeout: 5 * time.Second}
}

func (m *MediaMTX) do(ctx context.Context, method, path string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(m.BaseURL, "/")+path, rd)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := m.client().Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrRelayUnreachable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound {
		return resp.StatusCode, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("relay API %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("relay API %s: %w", path, err)
		}
	}
	return resp.StatusCode, nil
}

type forwardConf struct {
	Dest string `json:"dest"`
}

// SetForward replaces the path's forward destinations. An empty list stops
// forwarding.
func (m *MediaMTX) SetForward(ctx context.Context, path string, dests []string) error {
	conf := make([]forwardConf, 0, len(dests))
	for _, d := range dests {
		conf = append(conf, forwardConf{Dest: d})
	}
	code, err := m.do(ctx, http.MethodPatch, "/v3/config/paths/patch/"+url.PathEscape(path),
		map[string]any{"forward": conf}, nil)
	if err == nil && code == http.StatusNotFound {
		return fmt.Errorf("relay has no path %q configured", path)
	}
	return err
}

// Forward returns the path's configured forward destinations.
func (m *MediaMTX) Forward(ctx context.Context, path string) ([]string, error) {
	var out struct {
		Forward []forwardConf `json:"forward"`
	}
	code, err := m.do(ctx, http.MethodGet, "/v3/config/paths/get/"+url.PathEscape(path), nil, &out)
	if err != nil {
		return nil, err
	}
	if code == http.StatusNotFound {
		return nil, fmt.Errorf("relay has no path %q configured", path)
	}
	dests := make([]string, 0, len(out.Forward))
	for _, f := range out.Forward {
		dests = append(dests, f.Dest)
	}
	return dests, nil
}

// PathStatus is whether a publisher (OBS) is sending.
type PathStatus struct {
	Ready     bool       `json:"ready"`
	ReadyTime *time.Time `json:"ready_time,omitempty"`
	Tracks    []string   `json:"tracks,omitempty"`
}

// Path returns whether OBS is publishing to the path.
func (m *MediaMTX) Path(ctx context.Context, path string) (PathStatus, error) {
	var out struct {
		Ready     bool       `json:"ready"`
		ReadyTime *time.Time `json:"readyTime"`
		Tracks    []string   `json:"tracks"`
	}
	code, err := m.do(ctx, http.MethodGet, "/v3/paths/get/"+url.PathEscape(path), nil, &out)
	if err != nil || code == http.StatusNotFound {
		return PathStatus{}, err // not found = nobody publishing
	}
	return PathStatus{Ready: out.Ready, ReadyTime: out.ReadyTime, Tracks: out.Tracks}, nil
}

// ForwardState is the runtime state of one forward destination.
type ForwardState struct {
	State         string `json:"state"` // idle, forwarding, error
	LastError     string `json:"last_error,omitempty"`
	OutboundBytes int64  `json:"outbound_bytes"`
	Pos           int    `json:"pos"`
}

// ForwardStates returns the runtime state of the path's forward destinations.
func (m *MediaMTX) ForwardStates(ctx context.Context, path string) ([]ForwardState, error) {
	var out struct {
		Items []struct {
			State         string `json:"state"`
			LastError     string `json:"lastError"`
			OutboundBytes int64  `json:"outboundBytes"`
			Pos           int    `json:"pos"`
		} `json:"items"`
	}
	code, err := m.do(ctx, http.MethodGet, "/v3/paths/forward-dests/list?path="+url.QueryEscape(path), nil, &out)
	if err != nil || code == http.StatusNotFound {
		return nil, err
	}
	states := make([]ForwardState, 0, len(out.Items))
	for _, it := range out.Items {
		states = append(states, ForwardState{State: it.State, LastError: it.LastError, OutboundBytes: it.OutboundBytes, Pos: it.Pos})
	}
	return states, nil
}
