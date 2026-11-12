package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrNotModified is returned by FetchState when the server state is unchanged.
var ErrNotModified = errors.New("state not modified")

// Client talks to the DoxaCast server's device API.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewClient creates a client with sensible timeouts.
func NewClient(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
}

// EnrollRequest is sent once by a new device.
type EnrollRequest struct {
	EnrollmentToken string `json:"enrollment_token"`
	Hostname        string `json:"hostname"`
	Platform        string `json:"platform"`
}

// EnrollResponse carries the device's long-lived credentials.
type EnrollResponse struct {
	DeviceID    string `json:"device_id"`
	DeviceToken string `json:"device_token"`
}

// Enroll registers a new device and returns its credentials.
func (c *Client) Enroll(ctx context.Context, req EnrollRequest) (EnrollResponse, error) {
	var out EnrollResponse
	err := c.do(ctx, http.MethodPost, "/api/v1/enroll", req, &out, nil)
	return out, err
}

// FetchState gets desired state. If ifRevision > 0 and the server's revision
// matches, it returns ErrNotModified.
func (c *Client) FetchState(ctx context.Context, ifRevision int64) (DesiredState, error) {
	var out DesiredState
	hdr := http.Header{}
	if ifRevision > 0 {
		hdr.Set("If-None-Match", fmt.Sprintf(`"%d"`, ifRevision))
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/device/state", nil, &out, hdr)
	return out, err
}

// SendHeartbeat reports status.
func (c *Client) SendHeartbeat(ctx context.Context, hb Heartbeat) error {
	return c.do(ctx, http.MethodPost, "/api/v1/device/heartbeat", hb, nil, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any, hdr http.Header) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return ErrNotModified
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
