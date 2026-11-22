package kubecron

import (
	"testing"
	"time"
)

// Failing is now, not the window: the last three finished runs failed.

func runs(statuses ...string) RunPage {
	page := RunPage{}
	for _, s := range statuses {
		page.Runs = append(page.Runs, RunRecord{Status: s})
	}
	return page
}

func TestFailingIsTheLastThreeFinishedRuns(t *testing.T) {
	cases := []struct {
		name string
		page RunPage
		want bool
	}{
		{"three failures", runs("failed", "failed", "failed", "succeeded"), true},
		{"a running run is skipped", runs("running", "failed", "failed", "failed"), true},
		{"one success clears it", runs("failed", "succeeded", "failed", "failed"), false},
		{"fewer than three finished runs", runs("failed", "failed"), false},
		{"the newest succeeded", runs("succeeded", "failed", "failed", "failed"), false},
		{"nothing", runs(), false},
	}
	for _, c := range cases {
		if got := FailingNow(c.page); got != c.want {
			t.Errorf("%s: FailingNow = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestADayKeepsItsOwnRunsAndStopsAtItsStart(t *testing.T) {
	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	page := RunPage{NextCursor: "more", Runs: []RunRecord{
		{ID: "a", StartedAt: at("2026-10-20T23:00:00Z")},
		{ID: "b", StartedAt: at("2026-10-20T01:00:00Z")},
		{ID: "c", StartedAt: at("2026-10-19T23:00:00Z")},
	}}
	day, ok := parseDay("2026-10-20")
	if !ok {
		t.Fatal("2026-10-20 is a day")
	}
	got := onDay(page, day)
	if len(got.Runs) != 2 || got.Runs[0].ID != "a" || got.Runs[1].ID != "b" || got.NextCursor != "" {
		t.Errorf("got %+v, want a and b and no cursor: the day is read to its start", got)
	}
	if _, ok := parseDay("yesterday"); ok {
		t.Error("yesterday is not a day the calendar draws")
	}
	full := onDay(RunPage{NextCursor: "more", Runs: page.Runs[:2]}, day)
	if full.NextCursor != "more" {
		t.Errorf("cursor %q, want kept: the day goes on past this page", full.NextCursor)
	}
}
