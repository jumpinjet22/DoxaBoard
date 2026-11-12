package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/jumpinjet22/doxaboard/internal/cloud"
	"github.com/jumpinjet22/doxaboard/internal/logsafe"
)

// cloudStatus is everything the console's cloud folders section shows.
type cloudStatus struct {
	// Available is false when the rclone service isn't configured or running.
	Available bool             `json:"available"`
	Problem   string           `json:"problem,omitempty"`
	Providers []cloud.Provider `json:"providers"`
	Drives    []cloud.Drive    `json:"drives"`
	Folders   []cloud.Folder   `json:"folders"`
	// SignIn is set while a sign-in is waiting for the admin.
	SignIn *cloudSignIn `json:"sign_in,omitempty"`
}

type cloudSignIn struct {
	Type    string `json:"type"`
	AuthURL string `json:"auth_url"`
}

func (s *Server) cloudFail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, cloud.ErrNotFound):
		httpError(w, http.StatusNotFound, "not found")
	case errors.Is(err, cloud.ErrUnavailable):
		httpError(w, http.StatusServiceUnavailable, cloud.ErrUnavailable.Error())
	default:
		// These are messages for the admin (a bad paste, a name clash, what
		// the provider said), not internal failures.
		httpError(w, http.StatusBadRequest, logsafe.Clean(err.Error()))
	}
}

func (s *Server) requireCloud(w http.ResponseWriter) bool {
	if s.cfg.Cloud == nil {
		httpError(w, http.StatusServiceUnavailable, "cloud drives aren't set up on this server (see docs/cloud-drives.md)")
		return false
	}
	return true
}

func (s *Server) getCloud(w http.ResponseWriter, r *http.Request) {
	st := cloudStatus{Providers: cloud.Providers, Drives: []cloud.Drive{}, Folders: []cloud.Folder{}}
	c := s.cfg.Cloud
	if c == nil {
		st.Problem = "Cloud drives aren't set up on this server."
		writeJSON(w, http.StatusOK, st)
		return
	}
	st.Folders = c.Folders()
	if err := c.Available(r.Context()); err != nil {
		st.Problem = cloud.ErrUnavailable.Error()
		writeJSON(w, http.StatusOK, st)
		return
	}
	drives, err := c.Drives(r.Context())
	if err != nil {
		s.cloudFail(w, err)
		return
	}
	st.Available, st.Drives = true, drives
	if typ, authURL := c.Pending(); authURL != "" {
		st.SignIn = &cloudSignIn{Type: typ, AuthURL: authURL}
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) cloudConnect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type string `json:"type"`
	}
	if !s.requireCloud(w) || !decode(w, r, &req) {
		return
	}
	authURL, err := s.cfg.Cloud.Connect(r.Context(), req.Type)
	if err != nil {
		s.cloudFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cloudSignIn{Type: req.Type, AuthURL: authURL})
}

func (s *Server) cloudFinish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if !s.requireCloud(w) || !decode(w, r, &req) {
		return
	}
	drive, err := s.cfg.Cloud.Finish(r.Context(), req.URL)
	if err != nil {
		s.cloudFail(w, err)
		return
	}
	s.log.Info("cloud drive connected", "drive", drive.Name, "type", drive.Type)
	writeJSON(w, http.StatusOK, drive)
}

func (s *Server) cloudCancel(w http.ResponseWriter, r *http.Request) {
	if !s.requireCloud(w) {
		return
	}
	s.cfg.Cloud.Cancel(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) cloudRemoveDrive(w http.ResponseWriter, r *http.Request) {
	if !s.requireCloud(w) {
		return
	}
	if err := s.cfg.Cloud.RemoveDrive(r.Context(), r.PathValue("name")); err != nil {
		s.cloudFail(w, err)
		return
	}
	s.log.Info("cloud drive disconnected", "drive", logsafe.Clean(r.PathValue("name")))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) cloudBrowse(w http.ResponseWriter, r *http.Request) {
	if !s.requireCloud(w) {
		return
	}
	names, err := s.cfg.Cloud.Browse(r.Context(), r.PathValue("name"), r.URL.Query().Get("path"))
	if err != nil {
		s.cloudFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"folders": names})
}

func (s *Server) cloudAddFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Drive string `json:"drive"`
		Path  string `json:"path"`
		Album string `json:"album"`
	}
	if !s.requireCloud(w) || !decode(w, r, &req) {
		return
	}
	f, err := s.cfg.Cloud.AddFolder(r.Context(), req.Drive, req.Path, req.Album)
	if err != nil {
		s.cloudFail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

func (s *Server) cloudRemoveFolder(w http.ResponseWriter, r *http.Request) {
	if !s.requireCloud(w) {
		return
	}
	if err := s.cfg.Cloud.RemoveFolder(r.PathValue("id")); err != nil {
		s.cloudFail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) cloudSyncFolder(w http.ResponseWriter, r *http.Request) {
	if !s.requireCloud(w) {
		return
	}
	// The sync outlives this request: a first sync can take a while.
	go func(id string) { _ = s.cfg.Cloud.Sync(context.Background(), id) }(r.PathValue("id"))
	w.WriteHeader(http.StatusAccepted)
}
