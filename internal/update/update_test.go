package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// refuse is a transport that fails the test if anything reaches it.
//
// **This is the whole of the "it can be turned off" guarantee.** Reading the
// code and seeing an early return proves that the code said so once; a transport
// that cannot be reached proves it on every run, including the one after somebody
// moves the guard below the cache read. `rules/DONE.md` §9 — it is not done until
// it has run.
type refuse struct{ t *testing.T }

func (r refuse) RoundTrip(request *http.Request) (*http.Response, error) {
	r.t.Helper()
	r.t.Fatalf("a request was made to %s, and nothing should have been asked", request.URL)
	return nil, nil
}

func refusing(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{Transport: refuse{t}}
}

// release is a stand-in for api.github.com answering.
func release(t *testing.T, tag string) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		// No query string ever, and nothing identifying in the headers. This is
		// the no-telemetry property, asserted where a request actually arrives
		// rather than argued for in a comment.
		if r.URL.RawQuery != "" {
			t.Errorf("the request carried a query string %q; it is a public read and "+
				"carries nothing", r.URL.RawQuery)
		}
		if agent := r.Header.Get("User-Agent"); agent != "kubetower" {
			t.Errorf("User-Agent is %q; it names the product and nothing else, because a "+
				"version in it turns a public read into a report of which build is running",
				agent)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"tag_name": tag,
			"html_url": "https://example.invalid/releases/" + tag,
		})
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func options(t *testing.T) Options {
	t.Helper()
	return Options{
		Version: "0.2.1",
		Enabled: true,
		Dir:     t.TempDir(),
		Now:     time.Now,
	}
}

// The off switch is honored **before any request is made**, which is the only
// reading of "it can be turned off" worth having.
func TestATurnedOffCheckMakesNoRequestAtAll(t *testing.T) {
	o := options(t)
	o.Enabled = false
	o.Client = refusing(t)
	o.Endpoint = "http://127.0.0.1:1/never"

	c := New(o)
	c.Check(context.Background())

	if got := c.Report().State; got != Off {
		t.Errorf("state is %q, want %q", got, Off)
	}
	if _, err := os.Stat(filepath.Join(o.Dir, cacheFile)); err == nil {
		t.Error("a check that made no request wrote a cache file; nothing happened, so there " +
			"is nothing to remember")
	}
}

// A console in a cluster is upgraded by its image tag. It does not ask, and the
// reason it does not ask is on the page rather than left as silence.
func TestTheInClusterBuildDoesNotCheck(t *testing.T) {
	o := options(t)
	o.InContainer = true
	o.Client = refusing(t)

	c := New(o)
	c.Check(context.Background())

	report := c.Report()
	if report.State != NotApplicable {
		t.Errorf("state is %q, want %q", report.State, NotApplicable)
	}
	if !strings.Contains(report.Note, "image tag") {
		t.Errorf("the note is %q and does not say why it did not check", report.Note)
	}
}

// A development build is never told it is out of date, and it does not ask
// either: `dev` cannot be ordered against a release, so the request would be
// made to answer a question that has no answer.
func TestADevelopmentBuildIsNeverToldItIsOutOfDate(t *testing.T) {
	o := options(t)
	o.Version = "dev"
	o.Development = true
	o.Client = refusing(t)

	c := New(o)
	c.Check(context.Background())

	report := c.Report()
	if report.State != NotApplicable {
		t.Errorf("state is %q, want %q — a development build must never be told it is out "+
			"of date", report.State, NotApplicable)
	}
	if report.Latest != "" {
		t.Errorf("it named a latest release %q; there was nothing to compare against",
			report.Latest)
	}
}

func TestANewerReleaseIsReportedWithSomewhereToGo(t *testing.T) {
	server, calls := release(t, "v0.3.0")
	o := options(t)
	o.Endpoint = server.URL

	c := New(o)
	c.Check(context.Background())

	report := c.Report()
	if report.State != Newer {
		t.Fatalf("state is %q, want %q", report.State, Newer)
	}
	if report.Latest != "0.3.0" {
		t.Errorf("latest is %q, want %q — the leading v is a git tag's and not a version's",
			report.Latest, "0.3.0")
	}
	if report.ReleaseURL == "" {
		t.Error("there is nowhere to go; the whole offer is the link")
	}
	if !strings.Contains(report.Note, "0.3.0") || !strings.Contains(report.Note, "0.2.1") {
		t.Errorf("the note is %q and does not name both versions", report.Note)
	}
	if *calls != 1 {
		t.Errorf("%d requests were made, want 1", *calls)
	}
}

func TestTheSameReleaseIsReportedAsCurrent(t *testing.T) {
	server, _ := release(t, "v0.2.1")
	o := options(t)
	o.Endpoint = server.URL

	c := New(o)
	c.Check(context.Background())

	if got := c.Report().State; got != Current {
		t.Errorf("state is %q, want %q", got, Current)
	}
}

// An older published release is not "newer", and an unparseable tag is not
// either. Neither is a reason to tell somebody to upgrade.
func TestAnOlderOrUnorderableReleaseIsNotAnOffer(t *testing.T) {
	for _, tag := range []string{"v0.1.0", "nightly", "v2026.09", ""} {
		t.Run(tag, func(t *testing.T) {
			server, _ := release(t, tag)
			o := options(t)
			o.Endpoint = server.URL

			c := New(o)
			c.Check(context.Background())

			if got := c.Report().State; got == Newer {
				t.Errorf("tag %q was reported as a newer release", tag)
			}
		})
	}
}

// Offline and up-to-date are different answers, and this is the test that says
// so (docs/DEFINITION-OF-DONE.md §3).
func TestOfflineIsNotReportedAsUpToDate(t *testing.T) {
	server, _ := release(t, "v0.3.0")
	endpoint := server.URL
	server.Close()

	o := options(t)
	o.Endpoint = endpoint
	o.Client = &http.Client{Timeout: 2 * time.Second}

	c := New(o)
	c.Check(context.Background())

	report := c.Report()
	if report.State != Failed {
		t.Fatalf("state is %q, want %q — a check that could not be made is not a check that "+
			"passed", report.State, Failed)
	}
	if report.State == Current {
		t.Fatal("offline rendered as current")
	}
	if !strings.Contains(report.Note, "unknown") {
		t.Errorf("the note is %q and does not say the answer is unknown", report.Note)
	}
}

// The status code an unauthenticated client gets from a private repository, and
// the one it will get until kubetower is public. It has its own sentence because
// "not found" reads as "gone" and means "not yours to see".
func TestAPrivateRepositoryIsSaidToBeUnreadableRatherThanAbsent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	defer server.Close()

	o := options(t)
	o.Endpoint = server.URL

	c := New(o)
	c.Check(context.Background())

	report := c.Report()
	if report.State != Failed {
		t.Fatalf("state is %q, want %q", report.State, Failed)
	}
	if !strings.Contains(report.Note, "unauthenticated") {
		t.Errorf("the note is %q and does not name the reason an operator can act on",
			report.Note)
	}
}

// Two starts inside the window make one request, not two.
func TestTwoStartsInsideTheWindowMakeOneRequest(t *testing.T) {
	server, calls := release(t, "v0.3.0")
	dir := t.TempDir()

	first := New(Options{Version: "0.2.1", Enabled: true, Dir: dir, Endpoint: server.URL})
	first.Check(context.Background())

	// The second process is a second Checker over the same state directory, which
	// is what a restart is. Its transport refuses, so a request would fail the
	// test rather than merely raise the count.
	second := New(Options{
		Version: "0.2.1", Enabled: true, Dir: dir,
		Endpoint: server.URL, Client: refusing(t),
	})
	second.Check(context.Background())

	if *calls != 1 {
		t.Errorf("%d requests were made across two starts, want 1", *calls)
	}
	if got := second.Report().State; got != Newer {
		t.Errorf("the second start reports %q; the cached answer is still the answer", got)
	}
	if second.Report().Latest != "0.3.0" {
		t.Errorf("the cached answer lost its version: %q", second.Report().Latest)
	}
}

// An upgrade inside the window is not told about the release it is running.
// The cache is the last answer *GitHub* gave - which release is newest - and
// the verdict on it belongs to the binary reading it. Served as written, 0.3.0
// started within a day of a check by 0.2.1 would read "KubeTower 0.3.0 has been
// released. This build is 0.2.1." on Settings, with the Dock badge and the
// notification following it.
func TestAnUpgradeInsideTheWindowIsNotOfferedItsOwnRelease(t *testing.T) {
	server, calls := release(t, "v0.3.0")
	dir := t.TempDir()

	before := New(Options{Version: "0.2.1", Enabled: true, Dir: dir, Endpoint: server.URL})
	before.Check(context.Background())
	if got := before.Report().State; got != Newer {
		t.Fatalf("0.2.1 against a 0.3.0 release reports %q, want newer", got)
	}

	// The upgraded binary, a restart later, over the same state directory. The
	// cache is still fresh, so it is still believed: the release it names is
	// what is re-read, not asked for again.
	after := New(Options{
		Version: "0.3.0", Enabled: true, Dir: dir,
		Endpoint: server.URL, Client: refusing(t),
	})
	after.Check(context.Background())

	report := after.Report()
	if report.State == Newer {
		t.Fatalf("0.3.0 is told a newer release exists: %q", report.Note)
	}
	if report.State != Current {
		t.Errorf("0.3.0 against a cached 0.3.0 release reports %q, want current", report.State)
	}
	if strings.Contains(report.Note, "0.2.1") {
		t.Errorf("the note still names the build that wrote the cache: %q", report.Note)
	}
	if report.Latest != "0.3.0" || report.ReleaseURL == "" {
		t.Errorf("the cached release was lost on the way: %+v", report)
	}
	if *calls != 1 {
		t.Errorf("%d requests were made; the window still holds, so one", *calls)
	}

	// And the other way round: a downgrade inside the window is offered the
	// release it came from.
	back := New(Options{Version: "0.2.0", Enabled: true, Dir: dir, Endpoint: server.URL, Client: refusing(t)})
	back.Check(context.Background())
	if got := back.Report(); got.State != Newer || !strings.Contains(got.Note, "This build is 0.2.0.") {
		t.Errorf("0.2.0 against a cached 0.3.0 release reports %+v", got)
	}
}

// A stale cache is asked again, and that is the other half of the window.
func TestAnAnswerOlderThanTheWindowIsAskedAgain(t *testing.T) {
	server, calls := release(t, "v0.3.0")
	dir := t.TempDir()

	now := time.Now()
	first := New(Options{
		Version: "0.2.1", Enabled: true, Dir: dir, Endpoint: server.URL,
		Now: func() time.Time { return now },
	})
	first.Check(context.Background())

	later := now.Add(goodFor + time.Minute)
	second := New(Options{
		Version: "0.2.1", Enabled: true, Dir: dir, Endpoint: server.URL,
		Now: func() time.Time { return later },
	})
	second.Check(context.Background())

	if *calls != 2 {
		t.Errorf("%d requests were made, want 2 — the day-old answer is not still the answer",
			*calls)
	}
}

// A failure is remembered for an hour and not for a day. An operator who was on
// a train when the console started, and on wifi ten minutes later, is not told
// "could not check" until tomorrow.
func TestAFailedCheckIsTriedAgainSoonerThanASuccessfulOne(t *testing.T) {
	server, _ := release(t, "v0.3.0")
	endpoint := server.URL
	server.Close()

	dir := t.TempDir()
	now := time.Now()
	failing := New(Options{
		Version: "0.2.1", Enabled: true, Dir: dir, Endpoint: endpoint,
		Client: &http.Client{Timeout: 2 * time.Second},
		Now:    func() time.Time { return now },
	})
	failing.Check(context.Background())
	if failing.Report().State != Failed {
		t.Fatalf("the first check did not fail; there is nothing to measure")
	}

	// Inside the failure window: no request, so a refusing transport must not be
	// reached.
	soon := New(Options{
		Version: "0.2.1", Enabled: true, Dir: dir, Endpoint: endpoint,
		Client: refusing(t),
		Now:    func() time.Time { return now.Add(retryAfter / 2) },
	})
	soon.Check(context.Background())
	if got := soon.Report().State; got != Failed {
		t.Errorf("state is %q, want %q", got, Failed)
	}

	// Past it, a fresh server answers and the report recovers.
	answering, calls := release(t, "v0.3.0")
	after := New(Options{
		Version: "0.2.1", Enabled: true, Dir: dir, Endpoint: answering.URL,
		Now: func() time.Time { return now.Add(retryAfter + time.Minute) },
	})
	after.Check(context.Background())
	if *calls != 1 {
		t.Errorf("%d requests after the failure window, want 1", *calls)
	}
	if got := after.Report().State; got != Newer {
		t.Errorf("state is %q, want %q", got, Newer)
	}
}

// The report is right before Check has run, because a Settings page opened in
// the first second of a process reads it then.
func TestTheReportIsAnswerableBeforeTheCheckHasRun(t *testing.T) {
	for _, c := range []struct {
		what string
		o    Options
		want State
	}{
		{"off", Options{Enabled: false}, Off},
		{"in a cluster", Options{Enabled: true, InContainer: true}, NotApplicable},
		{"a development build", Options{Enabled: true, Development: true}, NotApplicable},
		{"enabled", Options{Enabled: true, Version: "0.2.1"}, Failed},
	} {
		t.Run(c.what, func(t *testing.T) {
			report := New(c.o).Report()
			if report.State != c.want {
				t.Errorf("state is %q, want %q", report.State, c.want)
			}
			if report.Note == "" {
				t.Error("there is no sentence; a state with no sentence is a state the page " +
					"has to invent words for")
			}
		})
	}
}

// The switch is on unless the variable turns it off, and an unrecognized value
// leaves it on rather than guessing.
func TestTheOffSwitch(t *testing.T) {
	for _, c := range []struct {
		value string
		want  bool
	}{
		{"", true},
		{"1", true},
		{"true", true},
		{"nope", true},
		{"0", false},
		{"false", false},
		{"FALSE", false},
		{"no", false},
		{" off ", false},
	} {
		t.Run("KT_UPDATE_CHECK="+c.value, func(t *testing.T) {
			t.Setenv(EnvEnabled, c.value)
			if got := EnabledFromEnv(); got != c.want {
				t.Errorf("%q gave %v, want %v", c.value, got, c.want)
			}
		})
	}
}

func TestNewerOrdersReleasesAndRefusesWhatItCannotOrder(t *testing.T) {
	for _, c := range []struct {
		latest, running string
		want            bool
	}{
		{"0.3.0", "0.2.1", true},
		{"1.0.0", "0.9.9", true},
		{"0.2.2", "0.2.1", true},
		{"0.2.1", "0.2.1", false},
		{"0.2.0", "0.2.1", false},
		{"v0.3.0", "0.2.1", true},
		{"0.3.0-rc1", "0.2.1", true},
		{"0.3.0", "dev", false},
		{"nightly", "0.2.1", false},
		{"0.3", "0.2.1", false},
		{"", "0.2.1", false},
	} {
		if got := newer(c.latest, c.running); got != c.want {
			t.Errorf("newer(%q, %q) = %v, want %v", c.latest, c.running, got, c.want)
		}
	}
}

// A release tag is bare semver (`0.3.0`), and the
// earliest drafts were tagged `v0.1.0-rc.N`. Whichever form GitHub names, the
// verdict and the version reported are the same.
func TestABareTagAndAVTagCompareTheSame(t *testing.T) {
	for _, c := range []struct {
		version string
		want    State
	}{
		{"0.3.0", Newer},
		{"0.2.1", Current},
		{"0.1.0", Current},
		{"0.3.0-rc.4", Newer},
	} {
		got := map[string]Report{}
		for _, tag := range []string{c.version, "v" + c.version} {
			server, _ := release(t, tag)
			o := options(t)
			o.Endpoint = server.URL
			checker := New(o)
			checker.Check(context.Background())
			got[tag] = checker.Report()
		}
		bare, prefixed := got[c.version], got["v"+c.version]
		if bare.State != c.want || prefixed.State != c.want {
			t.Errorf("%s: tag %q reads %q and tag %q reads %q, want %q for both",
				c.version, c.version, bare.State, "v"+c.version, prefixed.State, c.want)
		}
		if bare.Latest != prefixed.Latest {
			t.Errorf("%s: the bare tag names latest %q and the v tag %q", c.version, bare.Latest, prefixed.Latest)
		}
		if newer(c.version, "0.2.1") != newer("v"+c.version, "0.2.1") {
			t.Errorf("newer() orders %q and %q differently", c.version, "v"+c.version)
		}
	}
}

// An unreadable cache is treated as no cache — one extra request, and never a
// refusal to start.
func TestAnUnreadableCacheIsAskedAgainRatherThanBelieved(t *testing.T) {
	server, calls := release(t, "v0.3.0")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, cacheFile), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("writing the cache: %v", err)
	}

	c := New(Options{Version: "0.2.1", Enabled: true, Dir: dir, Endpoint: server.URL})
	c.Check(context.Background())

	if *calls != 1 {
		t.Errorf("%d requests were made, want 1", *calls)
	}
	if got := c.Report().State; got != Newer {
		t.Errorf("state is %q, want %q", got, Newer)
	}
}
