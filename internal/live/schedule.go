// Package live controls the public broadcast: which outputs (Vimeo,
// YouTube, Facebook, any RTMP/RTMPS/SRT ingest) the relay forwards OBS to.
// A broadcast groups one or more outputs (e.g. "11:00 service" → YouTube +
// Facebook). An optional weekly schedule, with dated special events, picks
// the broadcast automatically; it can also be chosen by hand from the
// console or anything that can make an HTTP request (Bitfocus Companion,
// a Stream Deck, a phone shortcut).
package live

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Slot is a weekly window, e.g. Sunday 08:15-09:45 → "8:30 service".
type Slot struct {
	ID          string       `json:"id"`
	BroadcastID string       `json:"broadcast_id"`
	Weekday     time.Weekday `json:"weekday"`
	Start       string       `json:"start"` // "HH:MM", local time
	End         string       `json:"end"`   // "HH:MM", exclusive
}

// Special is a dated event. On its date, specials replace the weekly
// schedule entirely (e.g. one combined service instead of three).
type Special struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Date        string `json:"date"` // "YYYY-MM-DD", local date
	Start       string `json:"start"`
	End         string `json:"end"`
	BroadcastID string `json:"broadcast_id"`
}

// Window is a resolved, concrete time window for a destination.
type Window struct {
	BroadcastID string    `json:"broadcast_id"`
	Name        string    `json:"name,omitempty"` // special event name, if any
	Special     bool      `json:"special"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
}

func parseHM(s string) (int, int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, 0, fmt.Errorf("time %q must be HH:MM (24-hour)", s)
	}
	return t.Hour(), t.Minute(), nil
}

// ValidateWindow checks a start/end pair.
func ValidateWindow(start, end string) error {
	sh, sm, err := parseHM(start)
	if err != nil {
		return err
	}
	eh, em, err := parseHM(end)
	if err != nil {
		return err
	}
	if eh*60+em <= sh*60+sm {
		return errors.New("end time must be after start time (windows can't cross midnight)")
	}
	return nil
}

// ValidateDate checks a "YYYY-MM-DD" date.
func ValidateDate(s string) error {
	if _, err := time.Parse(time.DateOnly, s); err != nil {
		return fmt.Errorf("date %q must be YYYY-MM-DD", s)
	}
	return nil
}

func at(day time.Time, hm string) time.Time {
	h, m, _ := parseHM(hm)
	y, mo, d := day.Date()
	// time.Date normalizes DST gaps; windows are wall-clock times.
	return time.Date(y, mo, d, h, m, 0, 0, day.Location())
}

// windowsOn returns the windows that apply on the local date of day.
func windowsOn(day time.Time, slots []Slot, specials []Special) []Window {
	date := day.Format(time.DateOnly)
	var out []Window
	for _, sp := range specials {
		if sp.Date != date || ValidateWindow(sp.Start, sp.End) != nil {
			continue
		}
		out = append(out, Window{BroadcastID: sp.BroadcastID, Name: sp.Name, Special: true,
			Start: at(day, sp.Start), End: at(day, sp.End)})
	}
	if len(out) > 0 {
		return sortWindows(out) // specials replace the weekly schedule that day
	}
	for _, sl := range slots {
		if sl.Weekday != day.Weekday() || ValidateWindow(sl.Start, sl.End) != nil {
			continue
		}
		out = append(out, Window{BroadcastID: sl.BroadcastID, Start: at(day, sl.Start), End: at(day, sl.End)})
	}
	return sortWindows(out)
}

func sortWindows(w []Window) []Window {
	sort.SliceStable(w, func(i, j int) bool { return w[i].Start.Before(w[j].Start) })
	return w
}

// ErrNoWindow means nothing is scheduled at the given time.
var ErrNoWindow = errors.New("no service is scheduled right now; pick a broadcast")

// ErrAmbiguous means more than one window covers the given time.
var ErrAmbiguous = errors.New("more than one service is scheduled right now; pick a broadcast")

// Current returns the window covering now (in loc). Overlapping windows are
// refused rather than guessed.
func Current(now time.Time, loc *time.Location, slots []Slot, specials []Special) (Window, error) {
	now = now.In(loc)
	var hits []Window
	for _, w := range windowsOn(now, slots, specials) {
		if !now.Before(w.Start) && now.Before(w.End) {
			hits = append(hits, w)
		}
	}
	switch len(hits) {
	case 0:
		return Window{}, ErrNoWindow
	case 1:
		return hits[0], nil
	default:
		return Window{}, ErrAmbiguous
	}
}

// Upcoming lists windows that end after now, over the next days days
// (including today), in order.
func Upcoming(now time.Time, loc *time.Location, days int, slots []Slot, specials []Special) []Window {
	now = now.In(loc)
	var out []Window
	y, m, d := now.Date()
	for i := 0; i < days; i++ {
		day := time.Date(y, m, d+i, 12, 0, 0, 0, loc) // noon avoids DST edge cases
		for _, w := range windowsOn(day, slots, specials) {
			if w.End.After(now) {
				out = append(out, w)
			}
		}
	}
	return out
}
