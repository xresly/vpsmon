package main

import (
	"testing"
	"time"
)

func TestCycleStartFor(t *testing.T) {
	loc := time.FixedZone("T", 9*3600)
	at := func(s string) time.Time {
		tm, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	cases := []struct {
		now, day, rt, want string
	}{
		// after this month's reset moment -> this month
		{"2026-11-25 17:00", "3", "21:18", "2026-11-03 21:18"},
		// on the reset day but before the reset time -> last month
		{"2026-11-03 10:00", "3", "21:18", "2026-10-03 21:18"},
		// exactly at the reset moment -> this month
		{"2026-11-03 21:18", "3", "21:18", "2026-11-03 21:18"},
		// midnight default behaves like the old day-only logic
		{"2026-11-25 17:00", "24", "00:00", "2026-11-24 00:00"},
		{"2026-11-01 00:30", "1", "00:00", "2026-11-01 00:00"},
		// invalid time falls back to midnight; day clamps to 28
		{"2026-11-25 17:00", "31", "99:99", "2026-10-28 00:00"},
	}
	for _, c := range cases {
		day := 0
		for _, ch := range c.day {
			day = day*10 + int(ch-'0')
		}
		got := cycleStartFor(at(c.now), day, c.rt)
		if got.Format("2006-01-02 15:04") != c.want {
			t.Errorf("cycleStartFor(%s, %d, %q) = %s, want %s",
				c.now, day, c.rt, got.Format("2006-01-02 15:04"), c.want)
		}
	}
}
