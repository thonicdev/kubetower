package web

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/health"
)

// The eight states the health page classifies each keep a reason code of their
// own.
//
// `clusterState` must not merge `StateRefused`, `StateIAM`, `StatePlugin` and
// `StateSSO` into one `unreadable` + `read_denied`, and a name that does not
// resolve (`StateDNS`) must not fall through to `unreachable` +
// `cluster_unreachable`. `stateWords` gives eight labels because each has a
// different person behind it, and `whoFixes` says who; merging them one layer
// down would leave a remedy column nothing to branch on. So this test is about
// the **code**, the stable branchable thing, and not about the message, which
// the contract says is prose a client must not branch on.
func TestEveryClassifiedStateKeepsAnAnswerOfItsOwn(t *testing.T) {
	for _, want := range []struct {
		state   string
		code    api.ReasonCode
		api     api.State
		retry   bool
		remedy  bool
		because string
	}{
		{health.StateOK, "", api.StateOK, false, false,
			"a cluster that answered has no failure to attribute"},
		{health.StateSSO, api.CodeCredentialsExpired, api.StateUnreadable, true, true,
			"the one row on this page the operator fixes themselves, in one command"},
		{health.StateIAM, api.CodeReadDenied, api.StateUnreadable, false, true,
			"the role cannot be assumed by this identity; nothing here can change it"},
		{health.StatePlugin, api.CodeCredentialsRefused, api.StateUnreadable, false, true,
			"the plugin ran and refused, which sends an operator somewhere else than an IAM grant"},
		{health.StateMissing, api.CodeNotInstalled, api.StateAbsent, false, true,
			"nothing refused anything; there was no program to ask"},
		{health.StateDNS, api.CodeNameNotResolved, api.StateUnreachable, true, true,
			"a VPN that is down is not a cluster that is gone"},
		{health.StateRefused, api.CodeConnectionRefused, api.StateUnreachable, true, true,
			"something answered the address and said no"},
		{health.StateTimeout, api.CodeTimeout, api.StateUnreachable, true, true,
			"a question that was put and not answered"},
	} {
		row := health.Cluster{Name: "prod", State: want.state, Detail: "the probe's own words"}
		state, reason := clusterState("prod", row, true)

		if state != want.api {
			t.Errorf("%s: state = %q, want %q — %s", want.state, state, want.api, want.because)
		}
		if want.code == "" {
			if reason != nil {
				t.Errorf("%s: carries the reason %+v", want.state, reason)
			}
			continue
		}
		if reason == nil {
			t.Errorf("%s: no reason at all", want.state)
			continue
		}
		if reason.Code != want.code {
			t.Errorf("%s: code = %q, want %q — %s", want.state, reason.Code, want.code, want.because)
		}
		// Retryable is advice, and it matters most on the one state an operator
		// can fix in ten seconds: a lapsed session is retryable after a login.
		if reason.Retryable == nil || *reason.Retryable != want.retry {
			t.Errorf("%s: retryable = %v, want %v", want.state, reason.Retryable, want.retry)
		}
		if got := clusterRemedy(row, true) != nil; got != want.remedy {
			t.Errorf("%s: remedy present = %v, want %v", want.state, got, want.remedy)
		}
	}
}

// And the codes are distinct, which is the property the merge destroyed and
// which no per-state assertion above can see on its own.
func TestNoTwoClassifiedStatesShareAReasonCode(t *testing.T) {
	seen := map[api.ReasonCode][]string{}
	for _, state := range []string{
		health.StateSSO, health.StateIAM, health.StatePlugin, health.StateMissing,
		health.StateDNS, health.StateRefused, health.StateTimeout,
	} {
		_, reason := clusterState("prod", health.Cluster{Name: "prod", State: state}, true)
		if reason == nil {
			t.Errorf("%s: no reason", state)
			continue
		}
		seen[reason.Code] = append(seen[reason.Code], state)
	}
	for code, states := range seen {
		if len(states) > 1 {
			t.Errorf("%d states share the code %q: %v — each of them has a different person "+
				"behind it, and this is the layer where that survives", len(states), code, states)
		}
	}
	if len(seen) != 7 {
		t.Errorf("%d distinct codes over 7 states", len(seen))
	}
}

// A cluster nobody has probed yet is `pending`, and it is the ordinary state of
// every cluster at start-up.
func TestAClusterNobodyHasAskedIsPendingAndNotUnreadable(t *testing.T) {
	state, reason := clusterState("prod", health.Cluster{Name: "prod"}, false)
	if state != api.StatePending {
		t.Errorf("state = %q, want %q — `unreadable` asserts the cluster is there and could "+
			"not be read, when nothing was tried", state, api.StatePending)
	}
	if reason == nil || reason.Code != api.CodeNotProbedYet {
		t.Errorf("reason = %+v, want %q — and deliberately not `timeout`, because nothing "+
			"timed out", reason, api.CodeNotProbedYet)
	}
}

// --- the report ---------------------------------------------------------------

// probeFixture is a cold-ish process: two contexts answered out of fourteen, and
// the other twelve have no row at all.
//
// That asymmetry is the whole reason `HealthReport` grew counts. A client with
// only the rows writes "1 of 2 answering" over a kubeconfig declaring fourteen,
// which is a reassuring sentence about a page that has not finished looking.
type probeFixture struct {
	rows    []health.Cluster
	session health.Session
	// sessions is the whole SSO cache; nil derives it from session, so a test
	// about one session need not spell the list.
	sessions *health.Sessions
	cache    health.CredentialCache
	host     health.CredentialCache
}

func (p probeFixture) All(context.Context, bool) []health.Cluster { return p.rows }
func (p probeFixture) Check(_ context.Context, name string, _ bool) health.Cluster {
	for _, row := range p.rows {
		if row.Name == name {
			return row
		}
	}
	return health.Cluster{Name: name}
}
func (p probeFixture) Known() health.Snapshot {
	return health.Snapshot{Rows: p.rows, Unprobed: 12, Total: 14}
}
func (p probeFixture) SSO() health.Session { return p.session }
func (p probeFixture) SSOSessions() health.Sessions {
	if p.sessions != nil {
		return *p.sessions
	}
	list := health.Sessions{Checked: p.session.Checked, Dir: p.session.Dir, Problem: p.session.Problem}
	if p.session.Found {
		list.Sessions = []health.Session{p.session}
	}
	return list
}
func (p probeFixture) CredentialCache() health.CredentialCache     { return p.cache }
func (p probeFixture) HostCredentialCache() health.CredentialCache { return p.host }

func healthFixtureRows() []health.Cluster {
	checked := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	return []health.Cluster{
		{
			Name: "prod", State: health.StateOK, ReadOnly: true,
			Version: "v1.29.4", Metrics: "prometheus",
			CheckedAt: checked, Took: "184ms",
		},
		{
			Name: "staging", State: health.StateSSO,
			Detail: "exec: executable aws failed with exit code 255", CheckedAt: checked, Took: "1.2s",
		},
	}
}

func TestTheHealthPageIsFilledFromTheProbeThatHoldsIt(t *testing.T) {
	rows := healthFixtureRows()
	report := healthReport(nil, probeFixture{rows: rows}, rows, 12, 14, false)

	if len(report.Checks) != 2 {
		t.Fatalf("%d checks, want the two rows in the fixture", len(report.Checks))
	}

	// The five per-check facts. Every one of them is on `health.Cluster`, and the
	// payload must carry each.
	prod := report.Checks[0]
	if !prod.ReadOnly {
		t.Error("readOnly is not carried; the guardrail is per cluster and this is where it is")
	}
	if prod.Version != "v1.29.4" {
		t.Errorf("version = %q; a console several minors behind fails on APIs that moved "+
			"and the failure looks like a defect in the console", prod.Version)
	}
	if prod.MetricsSource != "prometheus" {
		t.Errorf("metricsSource = %q; without it an empty chart has no explanation", prod.MetricsSource)
	}
	if prod.CheckedAt == nil {
		t.Error("checkedAt is absent; a row from ten seconds ago and one from ten minutes " +
			"ago are read differently and look identical")
	}
	if prod.TookMs == nil || *prod.TookMs != 184 {
		t.Errorf("tookMs = %v, want 184", prod.TookMs)
	}

	// A duration the probe rendered as "1.2s", as a number. A client parsing
	// "1.2s" back out of prose is branching on display text.
	if staging := report.Checks[1]; staging.TookMs == nil || *staging.TookMs != 1200 {
		t.Errorf("tookMs for a second-scale probe = %v, want 1200", staging.TookMs)
	}
	// And who fixes it, which is the judgment the page is read for.
	if report.Checks[1].Remedy == nil || report.Checks[1].Remedy.Who == "" {
		t.Errorf("the SSO row names nobody: %+v", report.Checks[1].Remedy)
	}

	// The counts, and the one that is not a subtraction.
	if report.Total == nil || *report.Total != 14 {
		t.Errorf("total = %v, want 14 — `len(checks)` is 2 and the kubeconfig declares 14", report.Total)
	}
	if report.Pending == nil || *report.Pending != 12 {
		t.Errorf("pending = %v, want 12 — the figure that stops an unasked cluster reading "+
			"as a dead one", report.Pending)
	}
	if report.Probed == nil || *report.Probed != 2 {
		t.Errorf("probed = %v, want 2", report.Probed)
	}
	if report.Answered == nil || *report.Answered != 1 {
		t.Errorf("answered = %v, want 1", report.Answered)
	}
	if report.Refreshing == nil || !*report.Refreshing {
		t.Error("refreshing is not set while twelve contexts are unasked; a page that says " +
			"\"nothing yet\" and does not say it is still asking reads as one that gave up")
	}
	if report.Headline == "" || report.Tone == "" {
		t.Errorf("headline=%q tone=%q; the one line above the table is the server's because "+
			"it has to agree with the counts beside it", report.Headline, report.Tone)
	}
	if len(report.Counts) == 0 {
		t.Fatal("no count pills")
	}
	// The pill carries the branchable half beside the words.
	if report.Counts[0].Code == "" {
		t.Errorf("the pill %q carries no code; a client ordering or styling them has only "+
			"prose", report.Counts[0].Label)
	}
}

// A forced round is a round, and it says so.
func TestAForcedRoundReportsThatItIsOne(t *testing.T) {
	rows := healthFixtureRows()
	report := healthReport(nil, probeFixture{rows: rows}, rows, 0, 2, true)
	if report.Refreshing == nil || !*report.Refreshing {
		t.Error("a forced round did not report itself as one")
	}
	if report.Pending == nil || *report.Pending != 0 {
		t.Errorf("pending = %v after a forced round over every context", report.Pending)
	}
}

// The three banners, as notices, and the third arm.
func TestACacheThatCouldNotBeListedIsNeitherFineNorEmpty(t *testing.T) {
	// `~/.aws` bind-mounted with a uid mismatch is a
	// directory that exists and refuses to be listed. Answered as "there is no
	// session", which removes the warning naming the command, on the page whose
	// entire product is being believed.
	probe := probeFixture{
		// Checked, so the SSO session is a fact rather than the third arm. A zero
		// Session is `Unknown` — `!Checked` — and would produce a notice of its
		// own, which is correct behavior and not what this test is about.
		session: health.Session{Checked: true},
		cache: health.CredentialCache{
			Checked: false, Dir: "/root/.aws/cli/cache", Problem: "permission denied",
		},
	}
	notices := healthNotices(probe, nil)
	if len(notices) == 0 {
		t.Fatal("a cache that could not be listed produced no notice at all; the failure " +
			"that makes the banner necessary was the failure that removed it")
	}

	var found *api.HealthNotice
	for i := range notices {
		if notices[i].Key == "cache/container" {
			found = &notices[i]
		}
	}
	if found == nil {
		t.Fatalf("no container-cache notice among %+v", notices)
	}
	if found.Measured == nil || *found.Measured {
		t.Errorf("measured = %v; `false` is the third arm and it is neither fine nor broken",
			found.Measured)
	}
	// Named rather than summarized: "the cache could not be read" is not
	// something an operator can act on.
	if len(found.Paths) == 0 {
		t.Error("the notice names no path")
	}
	// **No command.** One offered for a problem it does not solve is how a
	// warning stops being read, and the fix here may be a mount rather than a
	// sign-in.
	if found.Command != "" {
		t.Errorf("a command was offered for a problem it may not solve: %q", found.Command)
	}
}

// The damaged-entry case, which is the one that carries a command.
func TestADamagedCacheEntryIsNamedAndNotCounted(t *testing.T) {
	probe := probeFixture{
		session: health.Session{Checked: true},
		cache: health.CredentialCache{
			Checked: true, Dir: "/root/.aws/cli/cache", Total: 14,
			Corrupt: []string{"a.json", "b.json"},
		},
	}
	notices := healthNotices(probe, nil)
	if len(notices) == 0 {
		t.Fatal("no notice for a cache with two unreadable entries")
	}
	notice := notices[0]
	// A count tells the operator there is a problem; the names are what they
	// paste into `rm`.
	if len(notice.Paths) != 2 {
		t.Errorf("paths = %v, want the two entries by name", notice.Paths)
	}
	if notice.Command == "" {
		t.Error("no command on the one arm where there is a command to give")
	}
	if notice.Measured == nil || !*notice.Measured {
		t.Errorf("measured = %v on a directory that was read", notice.Measured)
	}
}

// The whole report survives marshalling, which is the only thing that decides
// what a client sees.
func TestTheHealthReportMarshalsItsCountsAndItsRemedies(t *testing.T) {
	rows := healthFixtureRows()
	report := healthReport(nil, probeFixture{rows: rows}, rows, 12, 14, false)

	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	var payload struct {
		Total  *int `json:"total"`
		Checks []struct {
			Remedy *api.Remedy `json:"remedy"`
			TookMs *int        `json:"tookMs"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if payload.Total == nil || *payload.Total != 14 {
		t.Errorf("total did not survive: %v", payload.Total)
	}
	if len(payload.Checks) != 2 || payload.Checks[1].Remedy == nil {
		t.Errorf("the remedy did not survive: %+v", payload.Checks)
	}
}

// A desktop with no AWS SSO session is not `absent` with `not_installed`,
// which the page would draw as "not installed". A session is not something
// one installs, and its absence is not a failure: it is `empty`, with its own
// code, its sentence and the retry hint all from the server, so no client has
// to invent any of them.
func TestAMissingSSOSessionIsNoSessionAndNotNotInstalled(t *testing.T) {
	check := ssoCheck(health.Session{Checked: true, Found: false}, nil)
	if check.State != api.StateEmpty {
		t.Errorf("state = %q, want %q", check.State, api.StateEmpty)
	}
	if check.Reason == nil {
		t.Fatal("no reason: the sentence would be the client's")
	}
	if check.Reason.Code != api.CodeNoSession {
		t.Errorf("code = %q, want %q", check.Reason.Code, api.CodeNoSession)
	}
	if strings.Contains(check.Reason.Message, "install") || check.Reason.Message == "" {
		t.Errorf("message = %q", check.Reason.Message)
	}
	if check.Reason.Retryable == nil || !*check.Reason.Retryable {
		t.Errorf("retryable = %v, want true: an `aws sso login` makes one", check.Reason.Retryable)
	}
	if check.Remedy != nil {
		t.Errorf("remedy = %+v, want none", *check.Remedy)
	}
}

// An in-cluster console showed the AWS credential cache's
// notice on its health page. Its pod has no `~/.aws` and signs as its
// ServiceAccount, so neither the SSO row nor any of the three AWS notices
// belong in the response; the desktop keeps all of them.
func TestTheServerProfileReportsNothingAboutAWS(t *testing.T) {
	probe := probeFixture{
		rows:    healthFixtureRows(),
		session: health.Session{Problem: "there is no home directory to find the AWS CLI's SSO cache in"},
		cache:   health.CredentialCache{Problem: "no home directory"},
		host:    health.CredentialCache{Problem: "no home directory"},
	}

	server := healthReport(&Server{inCluster: true}, probe, probe.rows, 0, len(probe.rows), false)
	for _, check := range server.Checks {
		if check.Key == "sso" {
			t.Errorf("server profile: SSO row %+v", check)
		}
	}
	if len(server.Notices) != 0 {
		t.Errorf("server profile: notices %+v, want none", server.Notices)
	}

	desktop := healthReport(&Server{}, probe, probe.rows, 0, len(probe.rows), false)
	keys := map[string]bool{}
	for _, notice := range desktop.Notices {
		keys[notice.Key] = true
	}
	for _, key := range []string{"sso", "cache/container", "cache/host"} {
		if !keys[key] {
			t.Errorf("desktop: notice %q missing from %v", key, keys)
		}
	}
}

// A session's state is one of `health.SSOValid`, `SSORenewable`, `SSOExpired`
// and `SSOAbsent`, never the cluster probe's "ok", so a valid session must not
// become an expired check telling the operator to log in. A renewable session
// is not expired either: the AWS CLI renews it on the next call. And a cache
// that could not be read is not "no session": it is `unreadable`, with the
// reason it could not be read.
//
// A renewable session is fine only while the clusters do
// not contradict it. When they do - the condition that raises the notice "This
// SSO session is renewable and the clusters disagree" - the row says what the
// notice says, with the same `aws sso login` remedy, and not `ok` beside it.
func TestTheSSOCheckReadsEachSessionStateAsItsOwnAnswer(t *testing.T) {
	expires := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	answering := []health.Cluster{{Name: "prod", State: health.StateOK}}
	failing := []health.Cluster{
		{Name: "prod", State: health.StateOK},
		{Name: "staging", State: health.StateSSO},
	}
	cases := []struct {
		name    string
		session health.Session
		rows    []health.Cluster
		state   api.State
		code    api.ReasonCode // empty: no reason at all
		remedy  string         // a substring the remedy's sentence must carry; empty: no remedy
		retry   bool           // the reason says "try again"; false on no reason, or one without the hint
		message string         // a substring the reason must carry, when set
	}{
		{
			name:    "valid",
			session: health.Session{Checked: true, Found: true, State: health.SSOValid, Session: "work"},
			state:   api.StateOK,
		},
		{
			name:    "renewable",
			session: health.Session{Checked: true, Found: true, State: health.SSORenewable, Session: "work"},
			state:   api.StateOK,
		},
		{
			name: "renewable, every cluster answering",
			session: health.Session{
				Checked: true, Found: true, State: health.SSORenewable, Session: "work", ExpiresAt: expires,
			},
			rows:  answering,
			state: api.StateOK,
		},
		{
			name: "renewable, the clusters disagree",
			session: health.Session{
				Checked: true, Found: true, State: health.SSORenewable, Session: "work", ExpiresAt: expires,
			},
			rows:    failing,
			state:   api.StateUnreadable,
			code:    api.CodeCredentialsExpired,
			remedy:  "`aws sso login`",
			retry:   true,
			message: "renewable",
		},
		{
			name:    "expired",
			session: health.Session{Checked: true, Found: true, State: health.SSOExpired, Session: "work"},
			state:   api.StateUnreadable,
			code:    api.CodeCredentialsExpired,
			remedy:  "`aws sso login`",
			retry:   true,
		},
		{
			name:    "absent and checked",
			session: health.Session{Checked: true, Found: false, State: health.SSOAbsent},
			state:   api.StateEmpty,
			code:    api.CodeNoSession,
			retry:   true,
		},
		{
			name: "not checked",
			session: health.Session{
				Checked: false, Found: false, State: health.SSOAbsent,
				Dir: "/home/op/.aws/sso/cache", Problem: "permission denied",
			},
			state:   api.StateUnreadable,
			code:    api.CodeReadDenied,
			retry:   true,
			message: "permission denied",
		},
		{
			// A session found in a state this build does not know - an empty one,
			// or one a later `health` adds - is not a session known to be fine.
			name:    "found in an unknown state",
			session: health.Session{Checked: true, Found: true, State: "", Session: "work"},
			state:   api.StateUnreadable,
			code:    api.CodeUndecodable,
		},
		{
			name:    "found in a state added later",
			session: health.Session{Checked: true, Found: true, State: "suspended", Session: "work"},
			state:   api.StateUnreadable,
			code:    api.CodeUndecodable,
			message: "suspended",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			check := ssoCheck(tc.session, tc.rows)
			if check.Key != "sso" {
				t.Errorf("key = %q, want sso", check.Key)
			}
			if check.State != tc.state {
				t.Errorf("state = %q, want %q", check.State, tc.state)
			}
			switch {
			case tc.code == "" && check.Reason != nil:
				t.Errorf("reason = %+v, want none on an answer that is fine", *check.Reason)
			case tc.code != "" && check.Reason == nil:
				t.Fatalf("no reason, want %q", tc.code)
			case tc.code != "":
				if check.Reason.Code != tc.code {
					t.Errorf("code = %q, want %q", check.Reason.Code, tc.code)
				}
				if check.Reason.Message == "" {
					t.Error("empty message: the sentence would be the client's")
				}
				if tc.message != "" && !strings.Contains(check.Reason.Message, tc.message) {
					t.Errorf("message = %q, want it to carry %q", check.Reason.Message, tc.message)
				}
			}
			retry := check.Reason != nil && check.Reason.Retryable != nil && *check.Reason.Retryable
			if retry != tc.retry {
				t.Errorf("retryable = %v, want %v", retry, tc.retry)
			}
			switch {
			case tc.remedy == "" && check.Remedy != nil:
				t.Errorf("remedy = %+v, want none", *check.Remedy)
			case tc.remedy != "" && check.Remedy == nil:
				t.Fatalf("no remedy, want one carrying %q", tc.remedy)
			case tc.remedy != "":
				if check.Remedy.Who != "You" || !check.Remedy.Actionable {
					t.Errorf("remedy = %+v, want the operator's own, actionable", *check.Remedy)
				}
				if !strings.Contains(check.Remedy.What, tc.remedy) {
					t.Errorf("remedy = %q, want it to carry %q", check.Remedy.What, tc.remedy)
				}
			}
		})
	}
}
