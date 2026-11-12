package server

import (
	"io"
	"mime"
	"net/http"
	"path"
)

// themeCSS serves the instance's console styling. It is empty for the
// built-in look, so the console can always link it.
func (s *Server) themeCSS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.WriteString(w, s.cfg.Theme.CSS())
}

// themeAsset serves a font or logo named in theme.json, and nothing else.
func (s *Server) themeAsset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	f, err := s.cfg.Theme.Asset(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	ct := mime.TypeByExtension(path.Ext(name))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// An SVG logo is only ever shown through <img>; this stops it running
	// scripts if someone opens the file directly.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.Copy(w, f)
}

// themeInfo tells the console what to call itself.
func (s *Server) themeInfo(w http.ResponseWriter, _ *http.Request) {
	out := struct {
		Name string `json:"name,omitempty"`
		Logo string `json:"logo,omitempty"`
	}{}
	if t := s.cfg.Theme; t != nil {
		out.Name = t.Name
		if t.Logo != "" {
			out.Logo = "theme/" + t.Logo
		}
	}
	writeJSON(w, http.StatusOK, out)
}
