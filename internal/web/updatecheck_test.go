package web

import (
	"context"
	"testing"
	"time"
)

// Check now is offered only where something checks: a Server with no
// CheckUpdates gives the router a nil interface, not a non-nil apiUpdates over
// a nil func that would panic on the first press.
func TestCheckNowIsNilWhereNothingChecks(t *testing.T) {
	if got := (&Server{}).updateCheck(); got != nil {
		t.Errorf("updateCheck() = %#v with no CheckUpdates, want nil", got)
	}
}

// The answer carries the in-flight flag and dates only a request that was made.
func TestCheckNowCarriesTheSettledAnswer(t *testing.T) {
	at := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	s := &Server{setup: SetupInfo{CheckUpdates: func(context.Context) UpdateReport {
		return UpdateReport{State: "newer", Latest: "0.9.0", CheckedAt: at, Note: "n", Checking: true}
	}}}
	got := s.updateCheck().CheckUpdates(context.Background())
	if got.State != "newer" || got.Latest != "0.9.0" || !got.Checking {
		t.Errorf("answer %+v, want newer 0.9.0 still checking", got)
	}
	if got.CheckedAt == nil || !got.CheckedAt.Equal(at) {
		t.Errorf("checkedAt %v, want %v", got.CheckedAt, at)
	}

	off := &Server{setup: SetupInfo{CheckUpdates: func(context.Context) UpdateReport {
		return UpdateReport{State: "off"}
	}}}
	if got := off.updateCheck().CheckUpdates(context.Background()); got.CheckedAt != nil {
		t.Errorf("an off answer is dated %v; no request was made", got.CheckedAt)
	}
}
