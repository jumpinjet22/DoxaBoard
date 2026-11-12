package live

import (
	"errors"
	"testing"
	"time"
)

var houston, _ = time.LoadLocation("America/Chicago")

func gdlcSchedule() []Slot {
	return []Slot{
		{ID: "a", BroadcastID: "trad", Weekday: time.Sunday, Start: "08:15", End: "09:45"},
		{ID: "b", BroadcastID: "cont", Weekday: time.Sunday, Start: "10:45", End: "12:15"},
		{ID: "c", BroadcastID: "esp", Weekday: time.Sunday, Start: "13:15", End: "14:45"},
	}
}

// Sunday 2026-11-15 in Houston.
func sun(h, m int) time.Time { return time.Date(2026, 11, 15, h, m, 0, 0, houston) }

func TestCurrentPicksTheRightService(t *testing.T) {
	cases := []struct {
		at   time.Time
		want string
	}{
		{sun(8, 15), "trad"}, // start is inclusive
		{sun(9, 0), "trad"},
		{sun(11, 0), "cont"},
		{sun(13, 30), "esp"},
	}
	for _, c := range cases {
		w, err := Current(c.at, houston, gdlcSchedule(), nil)
		if err != nil || w.BroadcastID != c.want {
			t.Errorf("%s: got %q %v, want %q", c.at.Format("15:04"), w.BroadcastID, err, c.want)
		}
	}
	for _, at := range []time.Time{sun(7, 0), sun(9, 45), sun(10, 0), sun(15, 0)} { // end is exclusive
		if _, err := Current(at, houston, gdlcSchedule(), nil); !errors.Is(err, ErrNoWindow) {
			t.Errorf("%s: want ErrNoWindow, got %v", at.Format("15:04"), err)
		}
	}
	// Monday at 9:00: nothing.
	if _, err := Current(sun(9, 0).AddDate(0, 0, 1), houston, gdlcSchedule(), nil); !errors.Is(err, ErrNoWindow) {
		t.Errorf("monday: %v", err)
	}
}

func TestCurrentUsesTheConfiguredTimeZone(t *testing.T) {
	// 15:00 UTC is 9:00 in Houston (CST, UTC-6) in November.
	utc := time.Date(2026, 11, 15, 15, 0, 0, 0, time.UTC)
	w, err := Current(utc, houston, gdlcSchedule(), nil)
	if err != nil || w.BroadcastID != "trad" {
		t.Fatalf("got %+v %v", w, err)
	}
}

func TestSpecialReplacesWeeklyScheduleThatDay(t *testing.T) {
	specials := []Special{{ID: "x", Name: "Combined Service", Date: "2026-11-15", Start: "09:45", End: "11:30", BroadcastID: "special"}}

	w, err := Current(sun(10, 0), houston, gdlcSchedule(), specials)
	if err != nil || w.BroadcastID != "special" || !w.Special || w.Name != "Combined Service" {
		t.Fatalf("got %+v %v", w, err)
	}
	// The regular 8:30 window does not apply on the special day.
	if _, err := Current(sun(9, 0), houston, gdlcSchedule(), specials); !errors.Is(err, ErrNoWindow) {
		t.Fatalf("regular service should be skipped on a special day, got %v", err)
	}
	// The next Sunday is back to normal.
	if w, err := Current(sun(9, 0).AddDate(0, 0, 7), houston, gdlcSchedule(), specials); err != nil || w.BroadcastID != "trad" {
		t.Fatalf("next week: %+v %v", w, err)
	}
}

func TestOverlappingWindowsAreRefused(t *testing.T) {
	slots := append(gdlcSchedule(), Slot{ID: "d", BroadcastID: "other", Weekday: time.Sunday, Start: "09:00", End: "10:00"})
	if _, err := Current(sun(9, 30), houston, slots, nil); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("want ErrAmbiguous, got %v", err)
	}
}

func TestDaylightSavingTransition(t *testing.T) {
	// 2026-11-01 is the Sunday DST ends in the US (2:00 → 1:00). 9:00 local
	// is still 9:00 local, which is 15:00 UTC (CST) that day.
	utc := time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC)
	if w, err := Current(utc, houston, gdlcSchedule(), nil); err != nil || w.BroadcastID != "trad" {
		t.Fatalf("DST end: %+v %v", w, err)
	}
	// 2026-03-08: DST starts. 9:00 local is 14:00 UTC (CDT).
	utc = time.Date(2026, 3, 8, 14, 0, 0, 0, time.UTC)
	if w, err := Current(utc, houston, gdlcSchedule(), nil); err != nil || w.BroadcastID != "trad" {
		t.Fatalf("DST start: %+v %v", w, err)
	}
}

func TestUpcoming(t *testing.T) {
	specials := []Special{{ID: "x", Name: "Christmas Eve", Date: "2026-11-18", Start: "18:00", End: "19:30", BroadcastID: "special"}}
	got := Upcoming(sun(11, 0), houston, 7, gdlcSchedule(), specials)
	var ids []string
	for _, w := range got {
		ids = append(ids, w.BroadcastID)
	}
	want := "cont,esp,special" // 8:30 already over; the 11:00 is in progress; Wed special
	if s := join(ids); s != want {
		t.Fatalf("upcoming = %s, want %s", s, want)
	}
}

func TestValidation(t *testing.T) {
	for _, c := range [][2]string{{"9:00", "10:00"}, {"25:00", "26:00"}, {"10:00", "09:00"}, {"10:00", "10:00"}, {"x", "y"}} {
		if c[0] == "9:00" {
			continue // "9:00" isn't HH:MM, covered below
		}
		if ValidateWindow(c[0], c[1]) == nil {
			t.Errorf("ValidateWindow(%q, %q) should fail", c[0], c[1])
		}
	}
	if ValidateWindow("08:15", "09:45") != nil {
		t.Error("valid window rejected")
	}
	if ValidateDate("2026-13-01") == nil || ValidateDate("2026-12-24") != nil {
		t.Error("date validation")
	}
}

func join(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ","
		}
		out += x
	}
	return out
}
