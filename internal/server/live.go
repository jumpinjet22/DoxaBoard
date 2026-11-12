package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jumpinjet22/doxaboard/internal/live"
	"github.com/jumpinjet22/doxaboard/internal/logsafe"
)

func (s *Server) liveEnabled() bool { return s.cfg.Live != nil && s.cfg.Sealer != nil }

func (s *Server) requireLive(w http.ResponseWriter) bool {
	if !s.liveEnabled() {
		httpError(w, http.StatusServiceUnavailable, "live control is not configured on this server")
		return false
	}
	return true
}

// liveAuth accepts the admin token, or the live token (which can only
// start, stop and read status, so it's safe to put on a Stream Deck).
func (s *Server) liveAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.isAdmin(r) || eq(bearer(r), s.cfg.LiveToken) {
			h(w, r)
			return
		}
		httpError(w, http.StatusUnauthorized, "admin or live token required")
	}
}

// liveBackend adapts the store to live.Backend, unsealing stream keys.
type liveBackend struct{ s *Server }

func (b liveBackend) LiveState(ctx context.Context) (live.State, error) {
	return b.s.store.GetLiveState(ctx)
}

func (b liveBackend) SetLiveState(ctx context.Context, st live.State) error {
	return b.s.store.PutLiveState(ctx, st)
}

func (b liveBackend) Schedule(ctx context.Context) ([]live.Slot, []live.Special, error) {
	sched, err := b.s.store.LiveSchedule(ctx)
	return sched.Slots, sched.Specials, err
}

func (b liveBackend) Broadcast(ctx context.Context, id string) (live.Broadcast, error) {
	row, err := b.s.store.GetBroadcast(ctx, id)
	if err != nil {
		return live.Broadcast{}, err
	}
	out := live.Broadcast{ID: row.ID, Name: row.Name}
	for _, o := range row.Outputs {
		key, err := b.s.cfg.Sealer.Open(o.SealedKey)
		if err != nil {
			return out, fmt.Errorf("broadcast %s output %s: %w", row.Name, o.Label, err)
		}
		out.Outputs = append(out.Outputs, live.Output{Label: o.Label, URL: o.URL, Key: key})
	}
	return out, nil
}

// SetLive enables the live API with the given controller.
func (s *Server) SetLive(c *live.Controller) { s.cfg.Live = c }

// LiveBackend returns the store adapter for live.Controller.
func (s *Server) LiveBackend() live.Backend { return liveBackend{s} }

// ---- broadcasts (admin) ----

// OutputView is an output as the console sees it: the key is never sent.
type OutputView struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	URL    string `json:"url"`
	KeySet bool   `json:"key_set"`
}

// BroadcastView is a broadcast without secrets.
type BroadcastView struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Outputs []OutputView `json:"outputs"`
}

func broadcastView(b BroadcastRow) BroadcastView {
	v := BroadcastView{ID: b.ID, Name: b.Name, Outputs: []OutputView{}}
	for _, o := range b.Outputs {
		v.Outputs = append(v.Outputs, OutputView{ID: o.ID, Label: o.Label, URL: o.URL, KeySet: o.SealedKey != ""})
	}
	return v
}

func (s *Server) listBroadcasts(w http.ResponseWriter, r *http.Request) {
	if !s.requireLive(w) {
		return
	}
	rows, err := s.store.ListBroadcasts(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]BroadcastView, len(rows))
	for i, b := range rows {
		out[i] = broadcastView(b)
	}
	writeJSON(w, http.StatusOK, out)
}

type broadcastRequest struct {
	Name    string `json:"name"`
	Outputs []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		URL   string `json:"url"`
		Key   string `json:"key"`
	} `json:"outputs"`
}

func (s *Server) saveBroadcast(w http.ResponseWriter, r *http.Request) {
	if !s.requireLive(w) {
		return
	}
	var req broadcastRequest
	if !decode(w, r, &req) {
		return
	}
	row := &BroadcastRow{Name: strings.TrimSpace(req.Name), Outputs: []StoredOutput{}}
	existing := map[string]StoredOutput{}
	if id := r.PathValue("id"); id != "" {
		old, err := s.store.GetBroadcast(r.Context(), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		row.ID, row.CreatedAt = old.ID, old.CreatedAt
		for _, o := range old.Outputs {
			existing[o.ID] = o
		}
	}
	if row.Name == "" {
		httpError(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(req.Outputs) > 10 {
		httpError(w, http.StatusBadRequest, "at most 10 outputs per broadcast")
		return
	}
	for i, o := range req.Outputs {
		o.URL = strings.TrimSpace(o.URL)
		if err := live.ValidateOutputURL(o.URL); err != nil {
			httpError(w, http.StatusBadRequest, fmt.Sprintf("output %d: %v", i+1, err))
			return
		}
		label := strings.TrimSpace(o.Label)
		if label == "" {
			label = hostLabel(o.URL)
		}
		so := StoredOutput{ID: o.ID, Label: label, URL: o.URL}
		key := strings.TrimSpace(o.Key)
		if prev, ok := existing[o.ID]; ok && key == "" {
			so.SealedKey = prev.SealedKey // blank on edit keeps the saved key
		} else {
			sealed, err := s.cfg.Sealer.Seal(key)
			if err != nil {
				s.fail(w, err)
				return
			}
			so.SealedKey = sealed
		}
		if so.ID == "" || existing[so.ID].ID == "" {
			so.ID = newID("o")
		}
		row.Outputs = append(row.Outputs, so)
	}
	if err := s.store.SaveBroadcast(r.Context(), row); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, broadcastView(*row))
}

func hostLabel(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return "Output"
}

func (s *Server) deleteBroadcast(w http.ResponseWriter, r *http.Request) {
	if !s.requireLive(w) {
		return
	}
	id := r.PathValue("id")
	if st, err := s.store.GetLiveState(r.Context()); err == nil && st.Live && st.BroadcastID == id {
		httpError(w, http.StatusConflict, "this broadcast is live; end it first")
		return
	}
	if err := s.store.DeleteBroadcast(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- schedule (admin) ----

func (s *Server) getLiveSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.requireLive(w) {
		return
	}
	sched, err := s.store.LiveSchedule(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sched)
}

func (s *Server) putLiveSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.requireLive(w) {
		return
	}
	var sched LiveSchedule
	if !decode(w, r, &sched) {
		return
	}
	exists := func(id string) bool {
		_, err := s.store.GetBroadcast(r.Context(), id)
		return err == nil
	}
	if len(sched.Slots) > 200 || len(sched.Specials) > 500 {
		httpError(w, http.StatusBadRequest, "schedule is too large")
		return
	}
	if sched.Slots == nil {
		sched.Slots = []live.Slot{}
	}
	if sched.Specials == nil {
		sched.Specials = []live.Special{}
	}
	for i := range sched.Slots {
		sl := &sched.Slots[i]
		if sl.Weekday < time.Sunday || sl.Weekday > time.Saturday {
			httpError(w, http.StatusBadRequest, fmt.Sprintf("weekly entry %d: invalid day", i+1))
			return
		}
		if err := live.ValidateWindow(sl.Start, sl.End); err != nil {
			httpError(w, http.StatusBadRequest, fmt.Sprintf("weekly entry %d: %v", i+1, err))
			return
		}
		if !exists(sl.BroadcastID) {
			httpError(w, http.StatusBadRequest, fmt.Sprintf("weekly entry %d: unknown broadcast", i+1))
			return
		}
		if sl.ID == "" {
			sl.ID = newID("w")
		}
	}
	for i := range sched.Specials {
		sp := &sched.Specials[i]
		sp.Name = strings.TrimSpace(sp.Name)
		if err := live.ValidateDate(sp.Date); err != nil {
			httpError(w, http.StatusBadRequest, fmt.Sprintf("special %d: %v", i+1, err))
			return
		}
		if err := live.ValidateWindow(sp.Start, sp.End); err != nil {
			httpError(w, http.StatusBadRequest, fmt.Sprintf("special %d: %v", i+1, err))
			return
		}
		if !exists(sp.BroadcastID) {
			httpError(w, http.StatusBadRequest, fmt.Sprintf("special %d: unknown broadcast", i+1))
			return
		}
		if sp.ID == "" {
			sp.ID = newID("x")
		}
	}
	if err := s.store.SaveLiveSchedule(r.Context(), sched); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sched)
}

// ---- live control (admin or live token) ----

func (s *Server) liveStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireLive(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.cfg.Live.Status(r.Context()))
}

// liveStatusText is a one-line status for button labels (e.g. a Companion
// variable): "LIVE 11:00 Contemporary 0:42", "READY 11:00 Contemporary", "OFF".
func (s *Server) liveStatusText(w http.ResponseWriter, r *http.Request) {
	if !s.requireLive(w) {
		return
	}
	st := s.cfg.Live.Status(r.Context())
	var line string
	switch {
	case st.Live && st.Broadcast != nil:
		d := time.Since(*st.StartedAt).Round(time.Minute)
		line = fmt.Sprintf("LIVE %s %d:%02d", st.Broadcast.Name, int(d.Hours()), int(d.Minutes())%60)
	case st.OBS.Ready && st.Scheduled != nil:
		line = "READY " + st.Scheduled.Name
	case st.OBS.Ready:
		line = "OBS ON"
	default:
		line = "OFF"
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(line + "\n"))
}

func (s *Server) liveStart(w http.ResponseWriter, r *http.Request) {
	if !s.requireLive(w) {
		return
	}
	target := r.URL.Query().Get("broadcast")
	force, _ := strconv.ParseBool(r.URL.Query().Get("force"))
	by := "live token"
	if s.isAdmin(r) {
		by = "admin"
	}
	if err := s.cfg.Live.Start(r.Context(), target, force, by); err != nil {
		s.liveFail(w, err)
		return
	}
	s.log.Info("broadcast started", "broadcast", logsafe.Clean(target), "by", by)
	writeJSON(w, http.StatusOK, s.cfg.Live.Status(r.Context()))
}

func (s *Server) liveStop(w http.ResponseWriter, r *http.Request) {
	if !s.requireLive(w) {
		return
	}
	if err := s.cfg.Live.Stop(r.Context()); err != nil {
		s.liveFail(w, err)
		return
	}
	s.log.Info("broadcast ended")
	writeJSON(w, http.StatusOK, s.cfg.Live.Status(r.Context()))
}

func (s *Server) liveFail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpError(w, http.StatusNotFound, "broadcast not found")
	case errors.Is(err, live.ErrAlreadyLive), errors.Is(err, live.ErrNoSignal),
		errors.Is(err, live.ErrNoWindow), errors.Is(err, live.ErrAmbiguous):
		httpError(w, http.StatusConflict, err.Error())
	case errors.Is(err, live.ErrNoOutputs):
		httpError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, live.ErrRelayUnreachable):
		s.log.Error("relay unreachable", "err", logsafe.Clean(err.Error()))
		httpError(w, http.StatusBadGateway, live.ErrRelayUnreachable.Error())
	default:
		s.fail(w, err)
	}
}
