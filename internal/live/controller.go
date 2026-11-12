package live

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// Output is one place a broadcast is sent: a Vimeo event, a YouTube or
// Facebook stream, any RTMP/RTMPS/SRT ingest.
type Output struct {
	Label string // e.g. "Vimeo", "YouTube"
	URL   string // ingest URL as the platform shows it
	Key   string // stream key (secret); empty for SRT, where it's in the URL
}

// Dest returns the destination string mediamtx expects: RTMP stream keys go
// after a '#'.
func (o Output) Dest() string {
	if o.Key == "" {
		return o.URL
	}
	return strings.TrimRight(o.URL, "/") + "#" + o.Key
}

// ValidateOutputURL accepts rtmp, rtmps and srt ingest URLs.
func ValidateOutputURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return fmt.Errorf("%q is not a valid stream URL", raw)
	}
	switch u.Scheme {
	case "rtmp", "rtmps", "srt":
		return nil
	default:
		return fmt.Errorf("stream URL must start with rtmp://, rtmps:// or srt:// (got %q)", u.Scheme+"://")
	}
}

// Broadcast is a named group of outputs, resolved with secrets.
type Broadcast struct {
	ID      string
	Name    string
	Outputs []Output
}

// State is what's live, persisted so it survives restarts.
type State struct {
	Live        bool      `json:"live"`
	BroadcastID string    `json:"broadcast_id,omitempty"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	StartedBy   string    `json:"started_by,omitempty"`
}

// Backend is the storage the controller needs.
type Backend interface {
	LiveState(ctx context.Context) (State, error)
	SetLiveState(ctx context.Context, s State) error
	Broadcast(ctx context.Context, id string) (Broadcast, error)
	Schedule(ctx context.Context) ([]Slot, []Special, error)
}

var (
	// ErrAlreadyLive means another broadcast is live; end it first.
	ErrAlreadyLive = errors.New("another broadcast is already live")
	// ErrNoSignal means OBS isn't sending to the relay.
	ErrNoSignal = errors.New("OBS isn't sending to the relay yet; start streaming in OBS first")
	// ErrNoOutputs means a broadcast has nowhere to go.
	ErrNoOutputs = errors.New("this broadcast has no outputs; add a stream URL and key first")
)

// Controller starts and stops the public broadcast.
type Controller struct {
	Backend Backend
	Relay   *MediaMTX
	Path    string // relay path OBS publishes to, e.g. "live"
	Loc     *time.Location
	Now     func() time.Time

	mu sync.Mutex
}

func (c *Controller) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Controller) loc() *time.Location {
	if c.Loc != nil {
		return c.Loc
	}
	return time.Local
}

// Resolve turns "auto" (or "") into the scheduled broadcast; any other
// value is a broadcast ID.
func (c *Controller) Resolve(ctx context.Context, target string) (string, error) {
	if target != "" && target != "auto" {
		return target, nil
	}
	slots, specials, err := c.Backend.Schedule(ctx)
	if err != nil {
		return "", err
	}
	w, err := Current(c.now(), c.loc(), slots, specials)
	if err != nil {
		return "", err
	}
	return w.BroadcastID, nil
}

// Start begins forwarding the chosen broadcast. Starting the broadcast
// that's already live is a no-op; starting a different one is refused.
// Unless force is set, it refuses when OBS isn't sending.
func (c *Controller) Start(ctx context.Context, target string, force bool, by string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, err := c.Resolve(ctx, target)
	if err != nil {
		return err
	}
	b, err := c.Backend.Broadcast(ctx, id)
	if err != nil {
		return err
	}
	cur, err := c.Backend.LiveState(ctx)
	if err != nil {
		return err
	}
	if cur.Live && cur.BroadcastID != id {
		name := cur.BroadcastID
		if other, err := c.Backend.Broadcast(ctx, cur.BroadcastID); err == nil {
			name = other.Name
		}
		return fmt.Errorf("%w (%s); end it first", ErrAlreadyLive, name)
	}
	if len(b.Outputs) == 0 {
		return ErrNoOutputs
	}
	if !force {
		ps, err := c.Relay.Path(ctx, c.Path)
		if err != nil {
			return err
		}
		if !ps.Ready {
			return ErrNoSignal
		}
	}
	if err := c.Relay.SetForward(ctx, c.Path, dests(b)); err != nil {
		return err
	}
	if cur.Live && cur.BroadcastID == id {
		return nil // already live; outputs re-applied in case they were lost
	}
	return c.Backend.SetLiveState(ctx, State{Live: true, BroadcastID: id, StartedAt: c.now().UTC(), StartedBy: by})
}

// Stop ends the public broadcast. Screens on the local network are not
// affected: they read from the relay, not from the outputs.
func (c *Controller) Stop(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.Relay.SetForward(ctx, c.Path, nil); err != nil {
		return err
	}
	return c.Backend.SetLiveState(ctx, State{})
}

func dests(b Broadcast) []string {
	out := make([]string, 0, len(b.Outputs))
	for _, o := range b.Outputs {
		out = append(out, o.Dest())
	}
	return out
}

// Reconcile makes the relay match the saved state: after a relay restart
// its forward list is empty again, and this puts it back mid-service.
func (c *Controller) Reconcile(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, err := c.Backend.LiveState(ctx)
	if err != nil {
		return err
	}
	want := []string{}
	if st.Live {
		b, err := c.Backend.Broadcast(ctx, st.BroadcastID)
		if err != nil {
			return fmt.Errorf("live broadcast %s: %w", st.BroadcastID, err)
		}
		want = dests(b)
	}
	have, err := c.Relay.Forward(ctx, c.Path)
	if err != nil {
		return err
	}
	if slices.Equal(have, want) {
		return nil
	}
	return c.Relay.SetForward(ctx, c.Path, want)
}

// Run reconciles every interval until ctx is cancelled.
func (c *Controller) Run(ctx context.Context, every time.Duration, logf func(string, ...any)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := c.Reconcile(ctx); err != nil && ctx.Err() == nil && logf != nil {
			logf("live: reconcile: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// OutputStatus is one output's live state, with secrets removed.
type OutputStatus struct {
	Label         string `json:"label"`
	Host          string `json:"host"`
	State         string `json:"state"` // idle, forwarding, error, or "" if unknown
	LastError     string `json:"last_error,omitempty"`
	OutboundBytes int64  `json:"outbound_bytes"`
}

// Status is everything the console and a Companion button need.
type Status struct {
	OBS       PathStatus     `json:"obs"`
	Live      bool           `json:"live"`
	Broadcast *BroadcastRef  `json:"broadcast,omitempty"`
	StartedAt *time.Time     `json:"started_at,omitempty"`
	Outputs   []OutputStatus `json:"outputs"`
	Scheduled *ScheduledRef  `json:"scheduled,omitempty"`
	Upcoming  []ScheduledRef `json:"upcoming"`
	TimeZone  string         `json:"time_zone"`
	RelayErr  string         `json:"relay_error,omitempty"`
}

// BroadcastRef names a broadcast.
type BroadcastRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ScheduledRef is a schedule window with its broadcast's name.
type ScheduledRef struct {
	BroadcastRef
	Special bool      `json:"special"`
	Label   string    `json:"label,omitempty"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
}

// Status reports OBS, the live broadcast and its outputs, and the schedule.
func (c *Controller) Status(ctx context.Context) Status {
	st := Status{Outputs: []OutputStatus{}, Upcoming: []ScheduledRef{}, TimeZone: c.loc().String()}
	var err error
	if st.OBS, err = c.Relay.Path(ctx, c.Path); err != nil {
		st.RelayErr = err.Error()
	}
	names := map[string]string{}
	name := func(id string) string {
		if n, ok := names[id]; ok {
			return n
		}
		n := id
		if b, err := c.Backend.Broadcast(ctx, id); err == nil {
			n = b.Name
		}
		names[id] = n
		return n
	}

	if ls, err := c.Backend.LiveState(ctx); err == nil && ls.Live {
		st.Live = true
		st.Broadcast = &BroadcastRef{ID: ls.BroadcastID, Name: name(ls.BroadcastID)}
		started := ls.StartedAt
		st.StartedAt = &started
		if b, err := c.Backend.Broadcast(ctx, ls.BroadcastID); err == nil {
			states, _ := c.Relay.ForwardStates(ctx, c.Path)
			for i, o := range b.Outputs {
				os := OutputStatus{Label: o.Label, Host: hostOf(o.URL)}
				for _, fs := range states {
					if fs.Pos == i {
						os.State, os.OutboundBytes = fs.State, fs.OutboundBytes
						os.LastError = redact(fs.LastError, o.Key)
					}
				}
				st.Outputs = append(st.Outputs, os)
			}
		}
	}

	if slots, specials, err := c.Backend.Schedule(ctx); err == nil {
		ref := func(w Window) ScheduledRef {
			return ScheduledRef{BroadcastRef: BroadcastRef{ID: w.BroadcastID, Name: name(w.BroadcastID)},
				Special: w.Special, Label: w.Name, Start: w.Start, End: w.End}
		}
		if w, err := Current(c.now(), c.loc(), slots, specials); err == nil {
			r := ref(w)
			st.Scheduled = &r
		}
		// "Upcoming" means not started yet; the current window is Scheduled.
		for _, w := range Upcoming(c.now(), c.loc(), 8, slots, specials) {
			if !w.Start.After(c.now()) {
				continue
			}
			if st.Upcoming = append(st.Upcoming, ref(w)); len(st.Upcoming) == 5 {
				break
			}
		}
	}
	return st
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Hostname()
	}
	return ""
}

// redact removes a stream key from an error message, in case the relay
// echoes the destination.
func redact(msg, key string) string {
	if key != "" {
		msg = strings.ReplaceAll(msg, key, "***")
	}
	return msg
}
