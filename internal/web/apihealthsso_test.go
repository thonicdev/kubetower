package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/health"
)

// The home's AWS SSO card reads `HealthReport.sso`: every
// session with its name, state and expiry, the command where one is expired,
// an empty list for a cache holding none, and `measured: false` for a cache
// nobody could read - which a client must not draw as the ghost card.

func TestTheSSOCardIsFilledFromEverySessionInTheCache(t *testing.T) {
	rows := healthFixtureRows()
	valid := time.Date(2026, 10, 30, 18, 0, 0, 0, time.UTC)
	lapsed := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	sessions := &health.Sessions{Checked: true, Dir: "/home/someone/.aws/sso/cache", Sessions: []health.Session{
		{Checked: true, Found: true, Session: "corp", State: health.SSOValid, ExpiresAt: valid,
			Dir: "/home/someone/.aws/sso/cache"},
		{Checked: true, Found: true, Session: "lab", State: health.SSOExpired, ExpiresAt: lapsed},
		{Checked: true, Found: true, Session: "https://old.awsapps.com/start", State: health.SSOExpired,
			ExpiresAt: lapsed},
	}}

	report := healthReport(serverFor(t, false), probeFixture{rows: rows, sessions: sessions}, rows, 0, 2, false)
	if report.SSO == nil {
		t.Fatal("the desktop report carries no SSO sessions")
	}
	sso := report.SSO
	if !sso.Measured || sso.Problem != "" || len(sso.Sessions) != 3 {
		t.Fatalf("%+v, want three measured sessions", sso)
	}
	first := sso.Sessions[0]
	if first.Name != "corp" || first.State != health.SSOValid || first.ExpiresAt == nil ||
		!first.ExpiresAt.Equal(valid) || first.Command != "" {
		t.Errorf("valid session = %+v, want corp until %v and no command", first, valid)
	}
	if got := sso.Sessions[1]; got.State != health.SSOExpired || got.Command != "aws sso login --sso-session lab" {
		t.Errorf("expired session = %+v, want the command naming it", got)
	}
	// A start URL cannot go on a command line: the bare command, not a guess.
	if got := sso.Sessions[2].Command; got != "aws sso login" {
		t.Errorf("command for a start-URL session = %q, want the bare command", got)
	}

	raw, err := json.Marshal(sso)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "/home/someone") {
		t.Errorf("the SSO card's data carries a host path: %s", raw)
	}
}

func TestAnSSOCacheHoldingNothingIsMeasuredAndEmpty(t *testing.T) {
	rows := healthFixtureRows()
	none := probeFixture{rows: rows, session: health.Session{State: health.SSOAbsent, Checked: true}}

	report := healthReport(serverFor(t, false), none, rows, 0, 2, false)
	if report.SSO == nil || !report.SSO.Measured || len(report.SSO.Sessions) != 0 {
		t.Fatalf("%+v, want measured with no session: the ghost card's answer", report.SSO)
	}
	raw, err := json.Marshal(report.SSO)
	if err != nil {
		t.Fatal(err)
	}
	// `sessions` is required by the contract: an empty list, never null.
	if !strings.Contains(string(raw), `"sessions":[]`) {
		t.Errorf("an empty cache serialized as %s, want an empty sessions list", raw)
	}
}

func TestAnSSOCacheNobodyCouldReadIsNotACacheHoldingNothing(t *testing.T) {
	rows := healthFixtureRows()
	unread := probeFixture{rows: rows, session: health.Session{
		State: health.SSOAbsent, Dir: "/home/someone/.aws/sso/cache", Problem: "permission denied",
	}}

	report := healthReport(serverFor(t, false), unread, rows, 0, 2, false)
	if report.SSO == nil {
		t.Fatal("an unread cache left no SSO answer, which a client reads as nothing to say")
	}
	if report.SSO.Measured {
		t.Error("an unread cache reported measured: the card would draw the ghost, 'there is no " +
			"session', where nobody could look")
	}
	if report.SSO.Problem != "permission denied" || len(report.SSO.Sessions) != 0 {
		t.Errorf("%+v, want the reason and no session", report.SSO)
	}
	raw, _ := json.Marshal(report.SSO)
	if strings.Contains(string(raw), "/home/someone") {
		t.Errorf("the unread answer carries the host path: %s", raw)
	}
}

func TestTheServerProfileCarriesNoSSOSessions(t *testing.T) {
	rows := healthFixtureRows()
	valid := probeFixture{rows: rows, session: health.Session{
		Checked: true, Found: true, Session: "corp", State: health.SSOValid, ExpiresAt: time.Now().Add(time.Hour),
	}}
	if report := healthReport(serverFor(t, true), valid, rows, 0, 2, false); report.SSO != nil {
		t.Errorf("the server profile reports SSO sessions: %+v", report.SSO)
	}
}
