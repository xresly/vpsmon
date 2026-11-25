package main

import (
	"testing"
	"time"
)

func TestParseDateTime(t *testing.T) {
	if _, ok := parseDateTime("2026-12-03 21:18"); !ok {
		t.Error("datetime should parse")
	}
	if tm, ok := parseDateTime("2027-12-03"); !ok || tm.Hour() != 0 || tm.Minute() != 0 {
		t.Error("legacy date-only should parse as midnight")
	}
	if _, ok := parseDateTime(""); ok {
		t.Error("empty should not parse")
	}
	if _, ok := parseDateTime("2026-13-40 99:99"); ok {
		t.Error("garbage should not parse")
	}
	if tm, ok := parseDateTime("2026-12-03 21:18"); !ok || tm.Format(dtLayout) != "2026-12-03 21:18" {
		t.Error("round trip failed")
	}
}

func TestAdvanceReset(t *testing.T) {
	at := func(s string) time.Time {
		tm, ok := parseDateTime(s)
		if !ok {
			t.Fatalf("bad test time %q", s)
		}
		return tm
	}
	// future moment: untouched, no rollover
	if nt, rolled := advanceReset(at("2026-12-03 21:18"), at("2026-11-25 18:00")); rolled || nt.Format(dtLayout) != "2026-12-03 21:18" {
		t.Error("future reset must not roll")
	}
	// exactly at the moment: rolls once, +30 days
	if nt, rolled := advanceReset(at("2026-12-03 21:18"), at("2026-12-03 21:18")); !rolled || nt.Format(dtLayout) != "2027-01-02 21:18" {
		t.Errorf("at-moment roll wrong: %v %s", rolled, nt.Format(dtLayout))
	}
	// long downtime: catches up in 30-day steps (Dec 3 -> Jan 2 -> Feb 1)
	if nt, rolled := advanceReset(at("2026-12-03 21:18"), at("2027-01-20 00:00")); !rolled || nt.Format(dtLayout) != "2027-02-01 21:18" {
		t.Errorf("catch-up wrong: %v %s", rolled, nt.Format(dtLayout))
	}
}
