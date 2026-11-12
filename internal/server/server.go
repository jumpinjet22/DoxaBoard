// Package server implements the DoxaBoard control plane: device enrollment,
// desired state, heartbeats, photo manifests and the admin API/console.
package server

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jumpinjet22/doxaboard/core"
	"github.com/jumpinjet22/doxaboard/internal/content"
	"github.com/jumpinjet22/doxaboard/internal/logsafe"
)

//go:embed web
var webFS embed.FS

// Config configures the server.
type Config struct {
	// AdminToken protects the admin API and console.
	AdminToken string
	// EnrollmentToken lets new devices register themselves.
	EnrollmentToken string
	// PhotoDir holds one subdirectory per album (e.g. synced from OneDrive by rclone).
	PhotoDir string
	// OnlineAfter is how recent a heartbeat must be to count as online.
	OnlineAfter time.Duration
	// Content fetches and caches connector sources. With Sealer, enables
	// the content API; nil disables it.
	Content *content.Manager
	// Sealer encrypts connector secrets at rest.
	Sealer *content.Sealer
	// RefreshEvery is how often each source is re-fetched (default 10 min).
	RefreshEvery time.Duration
}

// Server is the HTTP control plane.
type Server struct {
	cfg   Config
	store *Store
	log   *slog.Logger
}

// New creates a server.
func New(cfg Config, store *Store, log *slog.Logger) *Server {
	if cfg.OnlineAfter == 0 {
		cfg.OnlineAfter = 90 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &Server{cfg: cfg, store: store, log: log}
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

	// Device API.
	mux.HandleFunc("POST /api/v1/enroll", s.enroll)
	mux.HandleFunc("GET /api/v1/device/state", s.device(s.deviceState))
	mux.HandleFunc("POST /api/v1/device/heartbeat", s.device(s.deviceHeartbeat))

	// Photos: devices and admins.
	mux.HandleFunc("GET /api/v1/photos/{album}/manifest", s.deviceOrAdmin(s.photoManifest))
	mux.HandleFunc("GET /api/v1/photos/{album}/files/{name}", s.deviceOrAdmin(s.photoFile))

	// Admin API.
	mux.HandleFunc("GET /api/v1/admin/devices", s.admin(s.listDevices))
	mux.HandleFunc("PATCH /api/v1/admin/devices/{id}", s.admin(s.patchDevice))
	mux.HandleFunc("DELETE /api/v1/admin/devices/{id}", s.admin(s.deleteDevice))
	mux.HandleFunc("GET /api/v1/admin/profiles", s.admin(s.listProfiles))
	mux.HandleFunc("POST /api/v1/admin/profiles", s.admin(s.saveProfile))
	mux.HandleFunc("PUT /api/v1/admin/profiles/{id}", s.admin(s.saveProfile))
	mux.HandleFunc("DELETE /api/v1/admin/profiles/{id}", s.admin(s.deleteProfile))
	mux.HandleFunc("GET /api/v1/admin/albums", s.admin(s.listAlbums))

	// Content sources (connectors).
	mux.HandleFunc("GET /api/v1/admin/connectors", s.admin(s.listConnectors))
	mux.HandleFunc("GET /api/v1/admin/sources", s.admin(s.listSources))
	mux.HandleFunc("POST /api/v1/admin/sources", s.admin(s.saveSource))
	mux.HandleFunc("PUT /api/v1/admin/sources/{id}", s.admin(s.saveSource))
	mux.HandleFunc("DELETE /api/v1/admin/sources/{id}", s.admin(s.deleteSource))
	mux.HandleFunc("POST /api/v1/admin/sources/{id}/refresh", s.admin(s.refreshSourceHandler))
	mux.HandleFunc("GET /api/v1/admin/sources/{id}/slides", s.admin(s.sourceSlides))
	mux.HandleFunc("GET "+slideshowManifestPath, s.device(s.deviceSlideshow))
	mux.HandleFunc("GET /api/v1/content/{source}/files/{name}", s.deviceOrAdmin(s.contentFile))

	// Admin console (static, talks to the admin API).
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", http.FileServerFS(sub))

	return logRequests(s.log, mux)
}

// ---- auth ----

type ctxKey int

const deviceKey ctxKey = 0

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if t, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(t)
	}
	return ""
}

func eq(a, b string) bool {
	return a != "" && b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *Server) isAdmin(r *http.Request) bool { return eq(bearer(r), s.cfg.AdminToken) }

func (s *Server) admin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isAdmin(r) {
			httpError(w, http.StatusUnauthorized, "admin token required")
			return
		}
		h(w, r)
	}
}

func (s *Server) device(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := bearer(r)
		if tok == "" {
			httpError(w, http.StatusUnauthorized, "device token required")
			return
		}
		d, err := s.store.DeviceByToken(r.Context(), tok)
		if err != nil {
			httpError(w, http.StatusUnauthorized, "unknown device")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), deviceKey, d)))
	}
}

func (s *Server) deviceOrAdmin(h http.HandlerFunc) http.HandlerFunc {
	dev := s.device(h)
	return func(w http.ResponseWriter, r *http.Request) {
		if s.isAdmin(r) {
			h(w, r)
			return
		}
		dev(w, r)
	}
}

func deviceFrom(r *http.Request) *Device { d, _ := r.Context().Value(deviceKey).(*Device); return d }

// ---- device API ----

func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	var req core.EnrollRequest
	if !decode(w, r, &req) {
		return
	}
	if !eq(req.EnrollmentToken, s.cfg.EnrollmentToken) {
		httpError(w, http.StatusForbidden, "invalid enrollment token")
		return
	}
	host := strings.TrimSpace(req.Hostname)
	if host == "" {
		host = "unnamed"
	}
	d, token, err := s.store.CreateDevice(r.Context(), host, req.Platform)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.log.Info("device enrolled", "id", d.ID, "hostname", logsafe.Clean(host), "platform", logsafe.Clean(req.Platform))
	writeJSON(w, http.StatusCreated, core.EnrollResponse{DeviceID: d.ID, DeviceToken: token})
}

func (s *Server) deviceState(w http.ResponseWriter, r *http.Request) {
	d := deviceFrom(r)
	rev, err := s.store.Revision(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	etag := fmt.Sprintf(`"%d"`, rev)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	st, err := s.desiredState(r.Context(), d, rev)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("ETag", etag)
	writeJSON(w, http.StatusOK, st)
}

// desiredState builds what a device should be doing from its profile.
func (s *Server) desiredState(ctx context.Context, d *Device, rev int64) (core.DesiredState, error) {
	st := core.DesiredState{
		Revision:    rev,
		DeviceID:    d.ID,
		Name:        d.Name,
		Sources:     []core.Source{},
		Display:     d.Display,
		PollSeconds: 20,
	}
	if d.ProfileID == "" {
		return st, nil
	}
	p, err := s.store.GetProfile(ctx, d.ProfileID)
	if errors.Is(err, ErrNotFound) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	st.Sources = core.SortedSources(p.Sources)
	if p.PollSeconds > 0 {
		st.PollSeconds = p.PollSeconds
	}
	if p.Album != "" || len(p.SourceIDs) > 0 {
		st.Slideshow = core.SlideshowSpec{
			// Relative to the server URL; the device resolves it. Merges the
			// profile's content sources and photo album.
			ManifestURL:     slideshowManifestPath,
			IntervalSeconds: max(p.Interval, 3),
			Shuffle:         p.Shuffle,
		}
	}
	return st, nil
}

func (s *Server) deviceHeartbeat(w http.ResponseWriter, r *http.Request) {
	var hb core.Heartbeat
	if !decode(w, r, &hb) {
		return
	}
	if err := s.store.RecordHeartbeat(r.Context(), deviceFrom(r).ID, hb); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- photos ----

// PhotoManifest lists the photos in an album.
type PhotoManifest struct {
	Album  string  `json:"album"`
	Photos []Photo `json:"photos"`
}

// Photo is one image in a manifest.
type Photo struct {
	Name    string    `json:"name"`
	URL     string    `json:"url"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

var photoExts = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true}

// safeName rejects anything that could escape the photo directory.
func safeName(n string) bool {
	return n != "" && n != "." && n != ".." && !strings.HasPrefix(n, ".") &&
		!strings.ContainsAny(n, `/\`) && path.Base(n) == n
}

func (s *Server) photoRoot(album string) (*os.Root, error) {
	if s.cfg.PhotoDir == "" || !safeName(album) {
		return nil, ErrNotFound
	}
	base, err := os.OpenRoot(s.cfg.PhotoDir)
	if err != nil {
		return nil, err
	}
	defer base.Close()
	return base.OpenRoot(album)
}

func (s *Server) photoManifest(w http.ResponseWriter, r *http.Request) {
	album := r.PathValue("album")
	root, err := s.photoRoot(album)
	if err != nil {
		httpError(w, http.StatusNotFound, "album not found")
		return
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		s.fail(w, err)
		return
	}
	m := PhotoManifest{Album: album, Photos: []Photo{}}
	for _, e := range entries {
		if e.IsDir() || !safeName(e.Name()) || !photoExts[strings.ToLower(path.Ext(e.Name()))] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		m.Photos = append(m.Photos, Photo{
			Name:    e.Name(),
			URL:     "/api/v1/photos/" + album + "/files/" + e.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime().UTC(),
		})
	}
	sort.Slice(m.Photos, func(i, j int) bool { return m.Photos[i].Name < m.Photos[j].Name })
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) photoFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !safeName(name) || !photoExts[strings.ToLower(path.Ext(name))] {
		httpError(w, http.StatusNotFound, "not found")
		return
	}
	root, err := s.photoRoot(r.PathValue("album"))
	if err != nil {
		httpError(w, http.StatusNotFound, "album not found")
		return
	}
	defer root.Close()
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFileFS(w, r, root.FS(), name)
}

func (s *Server) listAlbums(w http.ResponseWriter, _ *http.Request) {
	albums := []string{}
	if s.cfg.PhotoDir != "" {
		entries, err := os.ReadDir(s.cfg.PhotoDir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.fail(w, err)
			return
		}
		for _, e := range entries {
			if e.IsDir() && safeName(e.Name()) {
				albums = append(albums, e.Name())
			}
		}
	}
	writeJSON(w, http.StatusOK, albums)
}

// ---- admin API ----

// DeviceView adds computed fields for the console.
type DeviceView struct {
	Device
	Online bool `json:"online"`
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	ds, err := s.store.ListDevices(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]DeviceView, len(ds))
	for i, d := range ds {
		out[i] = DeviceView{Device: d, Online: d.LastSeen != nil && time.Since(*d.LastSeen) < s.cfg.OnlineAfter}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) patchDevice(w http.ResponseWriter, r *http.Request) {
	var p DevicePatch
	if !decode(w, r, &p) {
		return
	}
	if p.Display != nil {
		if err := validateDisplay(*p.Display); err != nil {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	d, err := s.store.UpdateDevice(r.Context(), r.PathValue("id"), p)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteDevice(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listProfiles(w http.ResponseWriter, r *http.Request) {
	ps, err := s.store.ListProfiles(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ps)
}

func (s *Server) saveProfile(w http.ResponseWriter, r *http.Request) {
	var p Profile
	if !decode(w, r, &p) {
		return
	}
	if id := r.PathValue("id"); id != "" {
		if _, err := s.store.GetProfile(r.Context(), id); err != nil {
			s.fail(w, err)
			return
		}
		p.ID = id
	}
	if err := validateProfile(&p); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	for _, id := range p.SourceIDs {
		if _, err := s.store.GetSource(r.Context(), id); err != nil {
			httpError(w, http.StatusBadRequest, fmt.Sprintf("unknown content source %q", id))
			return
		}
	}
	if err := s.store.SaveProfile(r.Context(), &p); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) deleteProfile(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteProfile(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var validKinds = map[core.SourceKind][]string{
	core.KindMulticast: {"udp://"},
	core.KindSRT:       {"srt://"},
	core.KindRTSP:      {"rtsp://", "rtsps://"},
	core.KindHLS:       {"http://", "https://"},
}

func validateProfile(p *Profile) error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return errors.New("name is required")
	}
	if p.Album != "" && !safeName(p.Album) {
		return errors.New("invalid album name")
	}
	seenSrc := map[string]bool{}
	ids := p.SourceIDs[:0]
	for _, id := range p.SourceIDs {
		if !seenSrc[id] {
			seenSrc[id] = true
			ids = append(ids, id)
		}
	}
	p.SourceIDs = ids
	seen := map[string]bool{}
	for i := range p.Sources {
		src := &p.Sources[i]
		if src.ID == "" {
			src.ID = "s" + strconv.Itoa(i+1)
		}
		if seen[src.ID] {
			return fmt.Errorf("duplicate source id %q", src.ID)
		}
		seen[src.ID] = true
		prefixes, ok := validKinds[src.Kind]
		if !ok {
			return fmt.Errorf("source %q: unknown kind %q", src.ID, src.Kind)
		}
		okURL := false
		for _, pre := range prefixes {
			okURL = okURL || strings.HasPrefix(src.URL, pre)
		}
		if !okURL {
			return fmt.Errorf("source %q: %s URL must start with %s", src.ID, src.Kind, strings.Join(prefixes, " or "))
		}
	}
	return nil
}

func validateDisplay(d core.DisplaySpec) error {
	switch d.Output {
	case "", "hdmi", "composite":
	default:
		return fmt.Errorf("output must be hdmi or composite")
	}
	switch d.TVStandard {
	case "", "ntsc", "pal":
	default:
		return fmt.Errorf("tv_standard must be ntsc or pal")
	}
	if d.SafeAreaPercent < 0 || d.SafeAreaPercent > 20 {
		return fmt.Errorf("safe_area_percent must be 0-20")
	}
	return nil
}

// ---- helpers ----

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		httpError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotFound) {
		httpError(w, http.StatusNotFound, err.Error())
		return
	}
	s.log.Error("request failed", "err", logsafe.Clean(err.Error()))
	httpError(w, http.StatusInternalServerError, "internal error")
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(c int) { r.code = c; r.ResponseWriter.WriteHeader(c) }

func logRequests(log *slog.Logger, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: 200}
		h.ServeHTTP(rec, r)
		// Heartbeats and state polls are noisy; only log them when they fail.
		if rec.code >= 400 || !strings.HasPrefix(r.URL.Path, "/api/v1/device/") {
			log.Debug("http", "method", logsafe.Clean(r.Method), "path", logsafe.Clean(r.URL.Path), "status", rec.code, "dur", time.Since(start))
		}
	})
}
