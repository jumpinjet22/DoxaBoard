// Package agent contains the Linux (Raspberry Pi) implementations of the
// core interfaces: mpv playback, photo slideshow, source probes, file store.
package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Config is /etc/doxacast/agent.json.
type Config struct {
	// ServerURL is the control plane, e.g. http://doxacast.local:8080.
	ServerURL string `json:"server_url"`
	// EnrollmentToken is used once to register; credentials are then saved
	// in DataDir and the token can be removed from this file.
	EnrollmentToken string `json:"enrollment_token,omitempty"`
	// DataDir holds credentials, cached state and cached photos.
	DataDir string `json:"data_dir"`
	// MpvPath defaults to "mpv" on PATH.
	MpvPath string `json:"mpv_path,omitempty"`
	// MpvArgs are appended to every mpv invocation (e.g. ["--vo=gpu", "--gpu-context=drm"]).
	MpvArgs []string `json:"mpv_args,omitempty"`
	// FFprobePath defaults to "ffprobe" on PATH. Used to probe SRT/RTSP.
	FFprobePath string `json:"ffprobe_path,omitempty"`
	// Interface is the network interface for multicast and link metrics. Default eth0.
	Interface string `json:"interface,omitempty"`
}

// LoadConfig reads and validates the config file.
func LoadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	c.ServerURL = strings.TrimRight(c.ServerURL, "/")
	if c.ServerURL == "" {
		return c, errors.New("server_url is required")
	}
	if c.DataDir == "" {
		c.DataDir = "/var/lib/doxacast"
	}
	if c.MpvPath == "" {
		c.MpvPath = "mpv"
	}
	if c.FFprobePath == "" {
		c.FFprobePath = "ffprobe"
	}
	if c.Interface == "" {
		c.Interface = "eth0"
	}
	return c, os.MkdirAll(c.DataDir, 0o700)
}

// Credentials are issued by the server at enrollment.
type Credentials struct {
	DeviceID    string `json:"device_id"`
	DeviceToken string `json:"device_token"`
}

func credPath(dataDir string) string { return filepath.Join(dataDir, "credentials.json") }

// LoadCredentials returns saved credentials, or fs.ErrNotExist.
func LoadCredentials(dataDir string) (Credentials, error) {
	var c Credentials
	b, err := os.ReadFile(credPath(dataDir))
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil || c.DeviceToken == "" {
		return c, fs.ErrNotExist
	}
	return c, nil
}

// SaveCredentials writes credentials readable only by the agent user.
func SaveCredentials(dataDir string, c Credentials) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(credPath(dataDir), b, 0o600)
}

// FileStore implements core.Store on disk with atomic writes, so a power cut
// mid-write never leaves a half-written state file.
type FileStore struct{ Dir string }

// Get reads a key; a missing key returns nil, nil.
func (s FileStore) Get(key string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir, filepath.Base(key)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// Put writes a key atomically.
func (s FileStore) Put(key string, value []byte) error {
	return writeFileAtomic(filepath.Join(s.Dir, filepath.Base(key)), value, 0o600)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path))
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
