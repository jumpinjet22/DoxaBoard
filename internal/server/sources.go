package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/jumpinjet22/doxaboard/connectors/registry"
	"github.com/jumpinjet22/doxaboard/internal/content"
)

// slideshowManifestPath is where screens get their merged slideshow.
const slideshowManifestPath = "/api/v1/device/slideshow/manifest"

func (s *Server) contentEnabled() bool { return s.cfg.Content != nil && s.cfg.Sealer != nil }

func (s *Server) requireContent(w http.ResponseWriter) bool {
	if !s.contentEnabled() {
		httpError(w, http.StatusServiceUnavailable, "content sources are not configured on this server")
		return false
	}
	return true
}

// connectorFields returns a connector's fields plus the extra_hosts field
// every connector accepts.
func connectorFields(in registry.Info) []registry.Field {
	return append(append([]registry.Field(nil), in.Fields...), registry.Field{
		Key: registry.ExtraHostsKey, Label: "Extra image hosts", Type: registry.FieldText,
		Help: "Comma-separated hosts this source may also download from, e.g. an image CDN.",
	})
}

func (s *Server) listConnectors(w http.ResponseWriter, _ *http.Request) {
	out := []registry.Info{}
	for _, in := range registry.List() {
		in.Fields = connectorFields(in)
		out = append(out, in)
	}
	writeJSON(w, http.StatusOK, out)
}

// SourceView is a source as the console sees it: secrets are never sent,
// only whether they're set.
type SourceView struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Connector      string            `json:"connector"`
	ConnectorTitle string            `json:"connector_title"`
	Config         map[string]string `json:"config"`
	SecretsSet     []string          `json:"secrets_set"`
	Status         content.Status    `json:"status"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

func (s *Server) view(src SourceRow) SourceView {
	v := SourceView{
		ID: src.ID, Name: src.Name, Connector: src.Connector, Config: map[string]string{},
		SecretsSet: []string{}, Status: s.cfg.Content.Status(src.ID), UpdatedAt: src.UpdatedAt,
	}
	conn, ok := registry.Get(src.Connector)
	secret := map[string]bool{}
	if ok {
		v.ConnectorTitle = conn.Info().Title
		for _, f := range conn.Info().Fields {
			secret[f.Key] = f.Type == registry.FieldSecret
		}
	}
	for k, val := range src.Config {
		if secret[k] {
			if val != "" {
				v.SecretsSet = append(v.SecretsSet, k)
			}
			continue
		}
		v.Config[k] = val
	}
	sort.Strings(v.SecretsSet)
	return v
}

func (s *Server) listSources(w http.ResponseWriter, r *http.Request) {
	if !s.requireContent(w) {
		return
	}
	rows, err := s.store.ListSources(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]SourceView, len(rows))
	for i, row := range rows {
		out[i] = s.view(row)
	}
	writeJSON(w, http.StatusOK, out)
}

type sourceRequest struct {
	Name      string            `json:"name"`
	Connector string            `json:"connector"`
	Config    map[string]string `json:"config"`
}

func (s *Server) saveSource(w http.ResponseWriter, r *http.Request) {
	if !s.requireContent(w) {
		return
	}
	var req sourceRequest
	if !decode(w, r, &req) {
		return
	}
	var existing *SourceRow
	if id := r.PathValue("id"); id != "" {
		row, err := s.store.GetSource(r.Context(), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		existing = row
		if req.Connector == "" {
			req.Connector = row.Connector
		}
		if req.Connector != row.Connector {
			httpError(w, http.StatusBadRequest, "a source's connector can't be changed; create a new source")
			return
		}
	}
	row, err := s.buildSource(req, existing)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.SaveSource(r.Context(), row); err != nil {
		s.fail(w, err)
		return
	}
	// Fetch right away so the admin sees results without waiting for the loop.
	// The fetch outlives this request, but keeps its values (not its cancellation).
	bg := context.WithoutCancel(r.Context())
	go func() {
		if _, err := s.refreshSource(bg, row.ID); err != nil {
			s.log.Error("initial fetch", "source", row.ID, "err", err)
		}
	}()
	writeJSON(w, http.StatusOK, s.view(*row))
}

// buildSource validates a request and returns the row to store, with
// secrets sealed. An empty secret on update keeps the stored value.
func (s *Server) buildSource(req sourceRequest, existing *SourceRow) (*SourceRow, error) {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return nil, errors.New("name is required")
	}
	conn, ok := registry.Get(req.Connector)
	if !ok {
		return nil, fmt.Errorf("unknown connector %q", req.Connector)
	}
	info := conn.Info()
	plain := registry.Config{}
	stored := map[string]string{}
	for _, f := range connectorFields(info) {
		val := strings.TrimSpace(req.Config[f.Key])
		if f.Type == registry.FieldSecret {
			if val == "" && existing != nil && existing.Config[f.Key] != "" {
				stored[f.Key] = existing.Config[f.Key]
				opened, err := s.cfg.Sealer.Open(existing.Config[f.Key])
				if err != nil {
					return nil, fmt.Errorf("%s: %w", f.Label, err)
				}
				plain[f.Key] = opened
				continue
			}
			sealed, err := s.cfg.Sealer.Seal(val)
			if err != nil {
				return nil, err
			}
			stored[f.Key] = sealed
			plain[f.Key] = val
			continue
		}
		if val != "" {
			stored[f.Key] = val
			plain[f.Key] = val
		}
	}
	validated, err := registry.ValidateConfig(info, plain)
	if err != nil {
		return nil, err
	}
	// New() checks connector-specific rules (ranges, names) without I/O.
	deps := registry.Deps{HTTP: registry.NewHTTPClient(nil, time.Second, nil), Logf: func(string, ...any) {}, Now: time.Now}
	if _, err := conn.New(validated, deps); err != nil {
		return nil, err
	}
	row := &SourceRow{Name: req.Name, Connector: req.Connector, Config: stored}
	if existing != nil {
		row.ID, row.CreatedAt = existing.ID, existing.CreatedAt
	}
	return row, nil
}

func (s *Server) deleteSource(w http.ResponseWriter, r *http.Request) {
	if !s.requireContent(w) {
		return
	}
	id := r.PathValue("id")
	if err := s.store.DeleteSource(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.cfg.Content.Remove(id); err != nil {
		s.log.Warn("removing source cache", "id", id, "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) refreshSourceHandler(w http.ResponseWriter, r *http.Request) {
	if !s.requireContent(w) {
		return
	}
	st, err := s.refreshSource(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// SlidePreview is a cached slide as the console shows it.
type SlidePreview struct {
	content.CachedSlide
	URL    string `json:"url"`
	Active bool   `json:"active"`
}

func (s *Server) sourceSlides(w http.ResponseWriter, r *http.Request) {
	if !s.requireContent(w) {
		return
	}
	id := r.PathValue("id")
	if _, err := s.store.GetSource(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	cached, err := s.cfg.Content.Slides(id)
	if err != nil {
		s.fail(w, err)
		return
	}
	now := time.Now()
	out := make([]SlidePreview, len(cached))
	for i, c := range cached {
		out[i] = SlidePreview{CachedSlide: c, URL: contentURL(id, c.File), Active: c.Active(now)}
	}
	writeJSON(w, http.StatusOK, out)
}

func contentURL(sourceID, file string) string {
	return "/api/v1/content/" + sourceID + "/files/" + file
}

func (s *Server) contentFile(w http.ResponseWriter, r *http.Request) {
	if !s.requireContent(w) {
		return
	}
	id, name := r.PathValue("source"), r.PathValue("name")
	if !safeName(id) {
		httpError(w, http.StatusNotFound, "not found")
		return
	}
	f, err := s.cfg.Content.OpenFile(id, name)
	if err != nil {
		httpError(w, http.StatusNotFound, "not found")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	// File names are content hashes, so they never change.
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

func (s *Server) refreshSource(ctx context.Context, id string) (content.Status, error) {
	row, err := s.store.GetSource(ctx, id)
	if err != nil {
		return content.Status{}, err
	}
	src, err := s.decrypt(*row)
	if err != nil {
		return content.Status{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	return s.cfg.Content.Refresh(ctx, src), nil
}

func (s *Server) decrypt(row SourceRow) (content.Source, error) {
	src := content.Source{ID: row.ID, Name: row.Name, Connector: row.Connector, Config: registry.Config{}}
	conn, ok := registry.Get(row.Connector)
	secret := map[string]bool{}
	if ok {
		for _, f := range conn.Info().Fields {
			secret[f.Key] = f.Type == registry.FieldSecret
		}
	}
	for k, v := range row.Config {
		if secret[k] {
			opened, err := s.cfg.Sealer.Open(v)
			if err != nil {
				return src, fmt.Errorf("source %s: %s: %w", row.ID, k, err)
			}
			v = opened
		}
		src.Config[k] = v
	}
	return src, nil
}

// RunContent refreshes sources that are due, until ctx is cancelled.
func (s *Server) RunContent(ctx context.Context) {
	if !s.contentEnabled() {
		return
	}
	every := s.cfg.RefreshEvery
	if every <= 0 {
		every = 10 * time.Minute
	}
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		rows, err := s.store.ListSources(ctx)
		if err != nil && ctx.Err() == nil {
			s.log.Error("listing sources", "err", err)
		}
		for _, row := range rows {
			if ctx.Err() != nil {
				return
			}
			if time.Since(s.cfg.Content.Status(row.ID).FetchedAt) < every {
				continue
			}
			if _, err := s.refreshSource(ctx, row.ID); err != nil {
				s.log.Error("refreshing source", "id", row.ID, "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// deviceSlideshow merges the device's profile sources and album into one
// manifest in the format the agent already understands.
func (s *Server) deviceSlideshow(w http.ResponseWriter, r *http.Request) {
	d := deviceFrom(r)
	m := PhotoManifest{Album: "slideshow", Photos: []Photo{}}
	if d.ProfileID == "" {
		writeJSON(w, http.StatusOK, m)
		return
	}
	p, err := s.store.GetProfile(r.Context(), d.ProfileID)
	if errors.Is(err, ErrNotFound) {
		writeJSON(w, http.StatusOK, m)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	now := time.Now()
	if s.contentEnabled() {
		for _, id := range p.SourceIDs {
			cached, err := s.cfg.Content.Slides(id)
			if err != nil {
				s.log.Warn("reading cached slides", "source", id, "err", err)
				continue
			}
			for _, c := range cached {
				if !c.Active(now) {
					continue
				}
				m.Photos = append(m.Photos, Photo{Name: id + "-" + c.File, URL: contentURL(id, c.File), Size: c.Size})
			}
		}
	}
	if p.Album != "" {
		photos, err := s.albumPhotos(p.Album)
		if err != nil && !errors.Is(err, ErrNotFound) {
			s.fail(w, err)
			return
		}
		m.Photos = append(m.Photos, photos...)
	}
	writeJSON(w, http.StatusOK, m)
}

// albumPhotos lists the images in a photo album.
func (s *Server) albumPhotos(album string) ([]Photo, error) {
	root, err := s.photoRoot(album)
	if err != nil {
		return nil, ErrNotFound
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, err
	}
	var out []Photo
	for _, e := range entries {
		if e.IsDir() || !safeName(e.Name()) || !photoExts[strings.ToLower(path.Ext(e.Name()))] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Photo{
			Name:    e.Name(),
			URL:     "/api/v1/photos/" + album + "/files/" + e.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime().UTC(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
