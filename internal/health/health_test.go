package health

import (
	"encoding/json"
	"errors"
	"fmt"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The whole point of the package: four failures that look identical raw, and
// the four different people who fix them. Each case below is an error string
// this project has actually seen.
func TestEachFailureIsAttributedToWhoeverCanFixIt(t *testing.T) {
	cases := []struct {
		name    string
		message string
		want    string
	}{
		{
			"an expired SSO session",
			`Get "https://ABC.gr7.eu-west-3.eks.amazonaws.com/version": getting credentials: exec: Error when retrieving token from sso: Token has expired and refresh failed`,
			StateSSO,
		},
		{
			"an SSO session that was never started",
			"Error loading SSO Token: Token for sso-session my-sso does not exist",
			StateSSO,
		},
		{
			"a role this identity may not assume",
			"An error occurred (AccessDenied) when calling the AssumeRole operation: User is not authorized to perform: sts:AssumeRole",
			StateIAM,
		},
		{
			"a name that does not resolve",
			`Get "https://kubernetes.internal:6443/version": dial tcp: lookup kubernetes.internal on 127.0.0.11:53: no such host`,
			StateDNS,
		},
		{
			"the loopback trap",
			`Get "https://127.0.0.1:55594/version": dial tcp 127.0.0.1:55594: connect: connection refused`,
			StateRefused,
		},
		{
			"a black-holed route",
			`Get "https://10.0.0.1:6443/version": dial tcp 10.0.0.1:6443: i/o timeout`,
			StateTimeout,
		},
		{
			"a bare Unauthorized, which here is nearly always SSO",
			"You must be logged in to the server (Unauthorized)",
			StateSSO,
		},
		{
			"something genuinely unexpected",
			"the flux capacitor has come loose",
			StateKO,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			state, detail, _ := classify(errors.New(c.message))
			if state != c.want {
				t.Errorf("classified as %q, want %q\n  from: %s", state, c.want, c.message)
			}
			if detail == "" {
				t.Error("no detail; a state with nothing to read is a color, not an explanation")
			}
		})
	}
}

// Order dependence, stated as a test because it is the one thing about classify
// that a later edit could silently break: an expired session usually mentions a
// role too, and testing IAM first reports every lapsed session as a permissions
// problem the operator cannot fix.
func TestAnExpiredSessionThatAlsoMentionsARoleIsStillASessionProblem(t *testing.T) {
	message := "Error when retrieving token from sso: Token has expired; unable to AssumeRole arn:aws:iam::1234:role/admin"

	if state, _, _ := classify(errors.New(message)); state != StateSSO {
		t.Errorf("classified as %q, want %q — the operator can fix a session in ten seconds, "+
			"and cannot fix an IAM grant at all", state, StateSSO)
	}
}

func TestASuccessfulProbeIsNotClassifiedAsAFailure(t *testing.T) {
	// A cluster that answers must never be dressed up as a problem by a stray
	// substring: "not found" appears in plenty of healthy Kubernetes prose.
	state, _, _ := classify(errors.New(""))
	if state != StateKO {
		t.Errorf("an empty message gave %q; classify is only ever called on a real error", state)
	}
}

// Renewable is the state that stops the banner crying wolf.
func TestARenewableSessionIsNotReportedAsExpired(t *testing.T) {
	past := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	live := now.Add(30 * 24 * time.Hour).Format(time.RFC3339)

	if got := sessionState(past, "a-refresh-token", live, now); got != SSORenewable {
		t.Errorf("got %q, want %q — the AWS CLI renews this silently on the next call, "+
			"so the clusters keep answering and there is nothing for the operator to do",
			got, SSORenewable)
	}
	if got := sessionState(past, "", live, now); got != SSOExpired {
		t.Errorf("with no refresh token, got %q, want %q", got, SSOExpired)
	}
	future := now.Add(time.Hour)
	if got := sessionState(future, "", live, now); got != SSOValid {
		t.Errorf("a session in date gave %q, want %q", got, SSOValid)
	}
}

// A refresh token is only good while the registration behind it is in date.
// Reporting "renewable" past that leaves the operator waiting for a recovery
// that will never happen.
func TestARefreshTokenPastItsRegistrationCannotRenew(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	sessionExpired := now.Add(-3 * time.Hour)
	registrationExpired := now.Add(-24 * time.Hour).Format(time.RFC3339)

	if got := sessionState(sessionExpired, "a-refresh-token", registrationExpired, now); got != SSOExpired {
		t.Errorf("got %q, want %q — the registration behind the refresh token has itself lapsed, "+
			"so nothing will renew silently", got, SSOExpired)
	}
}

// This directory holds client registrations as
// well as sessions, both carry an `expiresAt`, and the registration's is months
// further out. Filtering on the expiry alone hands the banner the wrong file and
// it reports a healthy session while the real one lapses overnight.
func TestAClientRegistrationIsNotMistakenForASession(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	write := func(name string, payload map[string]any) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// The shape of a real ~/.aws/sso/cache: three registrations, one session,
	// and every registration outliving the session.
	for i, name := range []string{"reg-a.json", "reg-b.json", "reg-c.json"} {
		write(name, map[string]any{
			"clientId":     "client",
			"clientSecret": "shh",
			"scopes":       []string{"sso:account:access"},
			"expiresAt":    now.Add(time.Duration(60+i*10) * 24 * time.Hour).Format(time.RFC3339),
		})
	}
	write("session.json", map[string]any{
		"accessToken":           "a-token",
		"refreshToken":          "a-refresh-token",
		"registrationExpiresAt": now.Add(80 * 24 * time.Hour).Format(time.RFC3339),
		"startUrl":              "https://example.awsapps.com/start",
		"region":                "eu-west-3",
		"expiresAt":             now.Add(-2 * time.Hour).Format(time.RFC3339),
	})

	h := New(Options{AWSDir: dir})
	h.now = func() time.Time { return now }

	got := h.SSO()
	if !got.Found {
		t.Fatal("the session file was skipped entirely")
	}
	if got.Session == "" {
		t.Error("the session has no name; an empty name is the visible symptom of " +
			"having picked a registration file, which carries no startUrl")
	}
	if !got.ExpiresAt.Equal(now.Add(-2 * time.Hour)) {
		t.Errorf("expiry is %s, want the session's %s — a registration's expiry is months "+
			"out and would report a lapsed session as healthy",
			got.ExpiresAt.Format(time.RFC3339), now.Add(-2*time.Hour).Format(time.RFC3339))
	}
	if got.State != SSORenewable {
		t.Errorf("state is %q, want %q", got.State, SSORenewable)
	}
}

func TestExpiredMeansTheOperatorMustAct(t *testing.T) {
	if (Session{Found: true, State: SSORenewable}).Expired() {
		t.Error("a renewable session reported itself as expired")
	}
	if !(Session{Found: true, State: SSOExpired}).Expired() {
		t.Error("an expired session did not report itself as expired")
	}
	if (Session{}).Expired() {
		t.Error("an absent session reported itself as expired; there is nothing to renew")
	}
}

func TestTheSessionCacheIsReadAndTheFurthestExpiryWins(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	write := func(name string, payload map[string]any) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// A client registration: no access token, must be skipped rather than crash.
	write("registration.json", map[string]any{"clientId": "abc", "clientSecret": "shh"})
	write("old.json", map[string]any{
		"expiresAt":   now.Add(-2 * time.Hour).Format(time.RFC3339),
		"accessToken": "stale-token",
		"sessionName": "stale",
	})
	write("current.json", map[string]any{
		"expiresAt":   now.Add(6 * time.Hour).Format(time.RFC3339),
		"accessToken": "live-token",
		"sessionName": "the-one-that-matters",
	})
	// Not JSON at all. The AWS CLI has been known to leave partial writes here.
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{{{"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := New(Options{AWSDir: dir})
	h.now = func() time.Time { return now }

	got := h.SSO()
	if !got.Found {
		t.Fatal("no session found in a directory holding two")
	}
	if got.Session != "the-one-that-matters" {
		t.Errorf("picked %q; the session in date is the one the clusters are using", got.Session)
	}
	if got.State != SSOValid {
		t.Errorf("state is %q, want %q", got.State, SSOValid)
	}
}

func TestAnAbsentCacheIsNotAnExpiredSession(t *testing.T) {
	h := New(Options{AWSDir: filepath.Join(t.TempDir(), "nothing-here")})

	got := h.SSO()
	if got.Found {
		t.Error("found a session in a directory that does not exist")
	}
	if got.Expired() {
		t.Error("an absent cache reported an expired session — that sends the operator " +
			"to run `aws sso login` for a tool that may have no EKS clusters at all")
	}
}

// A malformed expiry must not be read as "expired". Guessing wrong here costs
// the operator a pointless login and teaches them to distrust the banner.
func TestAnUnreadableExpiryIsIgnoredRatherThanGuessed(t *testing.T) {
	dir := t.TempDir()
	raw, _ := json.Marshal(map[string]any{
		"expiresAt": "yesterday", "accessToken": "a-token", "sessionName": "x",
	})
	if err := os.WriteFile(filepath.Join(dir, "s.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	if got := New(Options{AWSDir: dir}).SSO(); got.Found {
		t.Errorf("an unparseable expiry produced a session in state %q", got.State)
	}
}

func TestRowsReadWorstFirstAndDoNotReshuffle(t *testing.T) {
	rows := []Cluster{
		{Name: "b-ok", State: StateOK},
		{Name: "iam", State: StateIAM},
		{Name: "a-ok", State: StateOK},
		{Name: "sso", State: StateSSO},
		{Name: "dns", State: StateDNS},
	}
	Sort(rows)

	want := []string{"sso", "dns", "iam", "a-ok", "b-ok"}
	for i, name := range want {
		if rows[i].Name != name {
			t.Fatalf("row %d is %q, want %q — order was %v", i, rows[i].Name, name, names(rows))
		}
	}
}

// The operator can fix a lapsed session themselves and cannot fix an IAM grant,
// so the actionable row must come first even though both are failures.
func TestTheRowTheOperatorCanFixOutranksTheOneTheyCannot(t *testing.T) {
	if rank(StateSSO) >= rank(StateIAM) {
		t.Error("an IAM refusal was ranked at or above a lapsed SSO session")
	}
	if !(Cluster{State: StateSSO}).Actionable() {
		t.Error("a lapsed SSO session is not marked actionable")
	}
	if (Cluster{State: StateIAM}).Actionable() {
		t.Error("an IAM refusal was marked actionable; it fails from the host too")
	}
}

func TestSummaryCountsEveryRow(t *testing.T) {
	got := Summary([]Cluster{
		{State: StateOK}, {State: StateOK}, {State: StateSSO}, {State: StateIAM},
	})
	if got[StateOK] != 2 || got[StateSSO] != 1 || got[StateIAM] != 1 {
		t.Errorf("counted %v", got)
	}
}

// The Detail column has a ceiling, and it is checked through classify because
// classify is the only way a message reaches that column.
//
// The number is written out here rather than compared against `detailLine`.
// `len(detail) <= detailLine` is a tautology: raise the constant to 2 000 and
// the assertion raises with it, so the test would pass over exactly the change
// it exists to stop. The pattern is bulk_test.go's
// TestABulkRunNeverHasMoreThanItsShareInFlight.
//
// 200, the same as a log panel's refusal: cut at two lengths, the same cluster
// error would be quoted differently on two pages and the shorter one would read
// as a different failure.
func TestALongErrorIsCutToSomethingAPageCanShow(t *testing.T) {
	const ceiling = 200
	if detailLine != ceiling {
		t.Fatalf("the Detail column's ceiling is %d and not %d. internal/logs and "+
			"internal/forward cut the same cluster error at %d; a column that disagrees "+
			"with them makes one page quote the API server differently from the next",
			detailLine, ceiling, ceiling)
	}

	for name, message := range map[string]string{
		"unix":          "first line, which names the problem\nsecond line, which does not",
		"windows":       "first line, which names the problem\r\nsecond line, which does not",
		"carriage only": "first line, which names the problem\rsecond line, which does not",
	} {
		if _, detail, _ := classify(errors.New(message)); detail != "first line, which names the problem" {
			t.Errorf("%s: classify kept %q; the rest of a client-go error is provenance, and on "+
				"an EKS context it names the caller's IAM ARN", name, detail)
		}
	}

	huge := ""
	for range 40 {
		huge += "0123456789"
	}
	_, detail, _ := classify(errors.New(huge))
	if n := len([]rune(detail)); n > ceiling {
		t.Errorf("a %d-character error reached the Detail column as %d runes; a table cell "+
			"cannot show that", len(huge), n)
	}
	if n := len([]rune(detail)); n < 20 {
		t.Errorf("the Detail column was handed %d runes of a %d-character error. This test "+
			"would pass over an implementation that returned the empty string, and an empty "+
			"answer is not the same answer as a short one", n, len(huge))
	}
}

func names(rows []Cluster) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Name
	}
	return out
}

// The half of this classifier that is *not* text.
//
// Three of the states this function returns exist for failures that never reach
// the API server at all — an expired SSO session, an `sts:AssumeRole` refusal, a
// name that does not resolve — and those can only be read out of the error's
// text. The table above covers them and they are why this classifier keeps
// sniffing.
//
// What that table does not cover is the other half: a typed Kubernetes error whose
// sentence happens to say none of the words. The API server's own message for a
// 403 is "... is forbidden: ..." and the text arm catches it; a 403 built by a
// webhook, a proxy or a client-go wrapper need not say the word at all, and must
// not arrive as StateKO with the raw error as its only explanation.
func TestATypedKubernetesErrorIsClassifiedByItsStatusAndNotItsProse(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		want string
	}{
		"a 401 whose sentence says nothing": {
			apierrors.NewUnauthorized("request rejected upstream"), StateSSO},
		"a 403 whose sentence says nothing": {
			&apierrors.StatusError{ErrStatus: metav1.Status{
				Code: 403, Reason: metav1.StatusReasonForbidden, Message: "rejected by policy 7"}},
			StateKO},
		"a 503 whose sentence says nothing": {
			apierrors.NewServiceUnavailable("upstream closed"), StateTimeout},
		"still nothing recognisable at all": {
			errors.New("the flux capacitor has come loose"), StateKO},
	} {
		t.Run(name, func(t *testing.T) {
			state, detail, _ := classify(c.err)
			if state != c.want {
				t.Errorf("classified as %q, want %q\n  from: %v", state, c.want, c.err)
			}
			if detail == "" {
				t.Error("no detail; a state with nothing to read is a color, not an explanation")
			}
		})
	}
}

// And the ordering the typed predicates must not disturb.
//
// They are at the bottom of classify rather than the top, which is the opposite
// of the two statusFor functions. An expired SSO session can arrive wrapped as
// an Unauthorized, and a typed check running first would answer StateKO — "the
// credentials are valid but may not read this", a sentence about RBAC — for a
// session the operator renews in ten seconds. This is the same rule the IAM case
// above is protected by, in the other direction.
func TestASessionExpiryWrappedAsUnauthorizedIsStillASessionProblem(t *testing.T) {
	err := fmt.Errorf("%w: Token has expired and refresh failed",
		apierrors.NewUnauthorized("Unauthorized"))
	if state, detail, _ := classify(err); state != StateSSO {
		t.Errorf("classified as %q with %q, want %q — the text arms are the specific "+
			"diagnosis and the typed predicates are the fallback, not the other way round",
			state, detail, StateSSO)
	}
}

// A credential plugin that ran and refused is not one that could not be run.
//
// Client-go's `exec: executable aws failed with exit code 255` would match the
// `"executable"` arm and come out as "the credential plugin could not be run",
// which sends the reader hunting for a missing binary that is demonstrably in the
// image. The plugin is there, it ran, and it refused — and its own message never
// reaches this function, because client-go points the plugin's stderr at the
// process's own and returns only the exit code.
//
// So the two cases need two answers, and the ordering is what makes it work: the
// exit-code arm has to be tested *before* the arm matching "executable", since the
// message contains both words.
func TestAPluginThatRanAndRefusedIsNotAMissingPlugin(t *testing.T) {
	ran, detail, _ := classify(errors.New("exec: executable aws failed with exit code 255"))
	if strings.Contains(detail, "could not be run") {
		t.Errorf("a plugin that exited 255 is reported as %q. It ran; saying otherwise sends "+
			"the reader looking for a binary that is present", detail)
	}
	// It says where the real message is, because that is all this function can
	// honestly offer — and it is genuinely useful, since the answer is one
	// `docker compose logs` away.
	if !strings.Contains(detail, "logs") {
		t.Errorf("detail = %q and does not say where the plugin's own message went, which "+
			"leaves the operator with an exit code and nothing else", detail)
	}
	// And it names the cause they cannot otherwise find. A corrupt entry in the
	// AWS credential cache lands here and is invisible from the error; the health
	// page's own scan is what diagnoses it, and this sentence is the pointer to it.
	if !strings.Contains(strings.ToLower(detail), "cache") {
		t.Errorf("detail = %q and never mentions the credential cache, which is one of the "+
			"two things this failure usually is and the one nobody guesses", detail)
	}

	// The genuinely missing plugin still says so — in its own words rather than
	// in `could not be run`, which is true and says nothing a reader can act on.
	// It names the binary and the PATH that was searched; see
	// TestAMissingPluginNamesTheBinaryAndWhereItLooked.
	missingState, missing, _ := classify(errors.New(`exec: executable aws not found`))
	if missingState != StateMissing {
		t.Errorf("state = %q for a plugin that is not installed, want %q — the two failures "+
			"share an arm and must not share an answer", missingState, StateMissing)
	}
	if strings.Contains(missing, "ran and refused") {
		t.Errorf("a missing plugin is reported as %q, which claims a program ran that "+
			"never existed", missing)
	}
	// It has its own state because **a fixer is named for it**: `whoFixes`
	// answers "You first, then your cloud administrator" and spells the order to
	// check in. Under StateKO it would fall through to `whoFixes`'s default —
	// "Nobody yet". See TestARequestURLCannotDecideWhatWentWrong for the other
	// way to get the same event wrong.
	if ran != StatePlugin {
		t.Errorf("state = %q for a refusing plugin, which puts it back among the failures "+
			"nobody is named for", ran)
	}
}
