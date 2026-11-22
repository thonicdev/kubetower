package health

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// A cache that could not be read and a cache holding nothing are different
// answers — and the one place on this page where getting it wrong is worse than
// being silent.
//
// If `SSO()` answered `Session{State: SSOAbsent}` for every `os.ReadDir`
// failure, a `~/.aws/sso/cache` that exists and refuses to list would read, all
// the way to the page, as "there is no SSO session". `internal/web.bannerOf`
// draws nothing for that, so the banner naming `aws sso login` would be deleted
// by the very failure that makes it necessary — and deleted in the reassuring
// direction.
//
// The trigger is ordinary: the operator's `~/.aws` is mounted into the
// container read-write, and a uid mismatch on that mount is exactly a directory
// that is there and will not open, while every EKS context fails inside `aws eks
// get-token`.

// failing is a readDir/readFile pair that refuses, which is how this runs on a
// Windows host.
//
// The seam rather than the filesystem, and both rather than either: mode bits do
// not deny the owner on Windows — internal/archive/unreadable_test.go skips
// itself there for that reason — and the obvious substitute lies. `os.ReadDir`
// over a path that is a *file* answers ERROR_PATH_NOT_FOUND on Windows, which
// `errors.Is(err, fs.ErrNotExist)` reports as "not there",
// so it would quietly exercise the absent branch and pass for the wrong reason,
// which is the defect this test is about.
func refusingDir(err error) func(string) ([]os.DirEntry, error) {
	return func(string) ([]os.DirEntry, error) { return nil, err }
}

func TestACacheThatWillNotListIsNotACacheHoldingNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	h := New(Options{AWSDir: dir})
	h.readDir = refusingDir(&fs.PathError{Op: "open", Path: dir, Err: fs.ErrPermission})

	got := h.SSO()
	if got.Checked {
		t.Fatal("a cache that could not be listed reported itself as checked, so the page " +
			"cannot tell 'there is no session' from 'nobody could look'")
	}
	if !got.Unknown() {
		t.Error("Unknown() is false for a cache nobody could read")
	}
	if got.Found {
		t.Error("a session was found in a directory that would not open")
	}
	if got.Expired() {
		t.Error("an unreadable cache reported an expired session; that is a claim, and " +
			"nothing was read to support it")
	}
	if got.Dir != dir {
		t.Errorf("Dir = %q, want %q — the page has nothing to name otherwise", got.Dir, dir)
	}
	if !strings.Contains(strings.ToLower(got.Problem), "permission") {
		t.Errorf("Problem = %q, want the reason in it; a banner saying only that "+
			"something went wrong is one nobody can act on", got.Problem)
	}
}

// The same fact through the filesystem rather than the seam, where the
// filesystem can express it. Skipped rather than faked on Windows and as root,
// for internal/archive/unreadable_test.go's reason: a chmod that does not
// actually deny is a test that passes for the wrong reason, and a silent pass is
// the defect here.
func TestARealDirectoryThatDeniesUsIsNotAnAbsentSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits do not deny the owner on Windows; this needs a POSIX filesystem")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root, which ignores the mode bits this test depends on")
	}

	dir := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{
		"expiresAt":   time.Now().Add(6 * time.Hour).UTC().Format(time.RFC3339),
		"accessToken": "live-token",
		"sessionName": "the-one-that-matters",
	})
	if err := os.WriteFile(filepath.Join(dir, "s.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	// It reads before the directory is closed to us, or every assertion below
	// passes for the wrong reason.
	if got := New(Options{AWSDir: dir}).SSO(); !got.Found || !got.Checked {
		t.Fatalf("baseline: %+v, want a checked session", got)
	}

	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("making %s unreadable: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, err := os.ReadDir(dir); err == nil {
		t.Skip("this filesystem let the owner list a mode-000 directory, so there is " +
			"nothing to test")
	}

	got := New(Options{AWSDir: dir}).SSO()
	if got.Checked {
		t.Error("a mode-000 cache reported itself as checked")
	}
	if got.Found {
		t.Error("a session was found in a directory that cannot be listed")
	}
}

// A directory that is not there is an answer, and must stay one. This is the
// ordinary state of an installation with no SSO profiles, and reporting it as
// "nobody could tell" would put an amber banner on every one of them — which is
// the mirror of the bug and just as good at teaching people to ignore the page.
func TestACacheThatIsNotThereIsAnAnswerAndNotAFailureToLook(t *testing.T) {
	h := New(Options{AWSDir: filepath.Join(t.TempDir(), "nothing-here")})

	got := h.SSO()
	if !got.Checked {
		t.Error("an absent cache reported that nobody could tell. It is not there, which " +
			"is a fact about this installation and not a failure of this process")
	}
	if got.Unknown() {
		t.Error("Unknown() is true for a directory that simply does not exist")
	}
	if got.Found || got.Problem != "" {
		t.Errorf("%+v, want a plain absent session with nothing wrong", got)
	}
}

// One layer in: the directory lists and the files will not open. Same failure,
// same answer — a uid mismatch on the `~/.aws` mount produces whichever
// of the two the modes happen to give.
func TestASessionFileThatWillNotOpenIsNotAnAbsentSession(t *testing.T) {
	dir := t.TempDir()
	raw, _ := json.Marshal(map[string]any{
		"expiresAt":   time.Now().Add(6 * time.Hour).UTC().Format(time.RFC3339),
		"accessToken": "live-token",
		"sessionName": "prod",
	})
	if err := os.WriteFile(filepath.Join(dir, "s.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	h := New(Options{AWSDir: dir})
	h.readFile = func(name string) ([]byte, error) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
	}

	got := h.SSO()
	if got.Checked {
		t.Error("the only file in the cache could not be opened and the answer was still " +
			"'there is no session'")
	}
	if got.Problem == "" {
		t.Error("nothing said why, so the banner has nothing to show")
	}
}

// And the case that must *not* trip it: one unreadable file beside a session
// that did read. The question was answered, so the answer stands — otherwise a
// stray `.json` nobody can open turns a live session into an amber banner every
// thirty seconds.
func TestAnUnreadableFileBesideAGoodSessionDoesNotUnsettleTheAnswer(t *testing.T) {
	dir := t.TempDir()
	raw, _ := json.Marshal(map[string]any{
		"expiresAt":   time.Now().Add(6 * time.Hour).UTC().Format(time.RFC3339),
		"accessToken": "live-token",
		"sessionName": "prod",
	})
	for _, name := range []string{"good.json", "locked.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A torn file beside the good one is the same case: counted, and outweighed.
	if err := os.WriteFile(filepath.Join(dir, "torn.json"), []byte(`{"expiresAt":`), 0o600); err != nil {
		t.Fatal(err)
	}

	h := New(Options{AWSDir: dir})
	h.readFile = func(name string) ([]byte, error) {
		if strings.HasSuffix(name, "locked.json") {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
		}
		return os.ReadFile(name)
	}

	got := h.SSO()
	if !got.Checked || !got.Found || got.Session != "prod" {
		t.Errorf("%+v, want the session that did read, checked and found", got)
	}
}

// A file that opens and will not parse is the same failure as one that will not
// open: a torn write of the only session file must not read as "no session file
// here".
func TestATornSessionFileIsNotAnAbsentSession(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "torn.json"), []byte(`{"expiresAt":`), 0o600); err != nil {
		t.Fatal(err)
	}

	got := New(Options{AWSDir: dir}).SSO()
	if got.Checked {
		t.Error("the only file in the cache would not parse and the answer was still " +
			"'there is no session'")
	}
	if !strings.Contains(got.Problem, "1 file(s)") {
		t.Errorf("problem %q, want it to name 1 file that could not be read", got.Problem)
	}
}

// And one layer further in: a session file whose expiry will not parse. It
// carries an access token, so it is a session the cache holds and this read
// could not make sense of — not an absent one.
func TestASessionFileWithAnUnparseableExpiryIsNotAnAbsentSession(t *testing.T) {
	dir := t.TempDir()
	raw, _ := json.Marshal(map[string]any{
		"expiresAt": "yesterday", "accessToken": "a-token", "sessionName": "x",
	})
	if err := os.WriteFile(filepath.Join(dir, "s.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	got := New(Options{AWSDir: dir}).SSO()
	if got.Checked {
		t.Error("the only session file had an expiry that would not parse and the answer " +
			"was still 'there is no session'")
	}
	if !strings.Contains(got.Problem, "1 file(s)") {
		t.Errorf("problem %q, want it to name 1 file that could not be read", got.Problem)
	}
}

// **The readiness probe must not learn any of this.** Degraded means the
// kubeconfig no longer reads, or not one context answered — and
// nothing else. A container that cannot read a cache directory must not be
// restarted in a loop over it, because the restart takes away the one page that
// explains what happened.
func TestAnUnreadableSSOCacheDoesNotDegradeTheReadinessProbe(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")

	clusters, kubeconfig := oneAnsweringAndOneDark(t)

	h := New(Options{Clusters: clusters, Kubeconfig: cluster.Kubeconfig{kubeconfig}, Timeout: time.Second})
	h.readDir = refusingDir(&fs.PathError{Op: "open", Path: "/root/.aws/sso/cache",
		Err: fs.ErrPermission})
	h.awsDir = "/root/.aws/sso/cache"

	// The state the probe reads: one context answering, one that cannot be
	// reached. That is the ordinary state of this installation.
	h.All(context.Background(), true)

	if got := h.SSO(); got.Checked {
		t.Fatal("the SSO cache was readable, so this test proves nothing")
	}

	r := h.Readiness(context.Background())
	if !r.OK {
		t.Fatalf("readiness is degraded (%q) with a cluster answering. Some clusters "+
			"unreachable is 200 and an unreadable AWS cache is not a Kubernetes fact at "+
			"all; a probe that fails on it restarts the container in a loop and removes "+
			"the page that names the problem", r.Reason)
	}
	if r.Answered != 1 {
		t.Errorf("Answered = %d of %d probed, want 1", r.Answered, r.Probed)
	}
	// And the reason says nothing about a cache, which would be this fact leaking
	// into an answer that is only about clusters.
	if strings.Contains(strings.ToLower(r.Reason), "sso") ||
		strings.Contains(strings.ToLower(r.Reason), "cache") {
		t.Errorf("readiness reason is %q; /kt-healthz means two things and this is not "+
			"one of them", r.Reason)
	}
}

// A kubeconfig with one cluster that answers and one that refuses immediately.
// No cluster is contacted beyond the local test server: port 1 on the loopback
// has nothing listening and the refusal costs nothing.
//
// KT_IN_CONTAINER is off in the caller, because this test's cluster really is on
// this machine's 127.0.0.1 and inside a container that address is rewritten to
// the host — the loopback rewrite, from the other direction.
func oneAnsweringAndOneDark(t *testing.T) (*cluster.Manager, string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"gitVersion":"v1.31.4"}`)
	}))
	t.Cleanup(server.Close)

	kubeconfig := filepath.Join(t.TempDir(), "config")
	writeKubeconfig(t, kubeconfig, map[string]string{
		"local": server.URL,
		"dark":  "https://127.0.0.1:1",
	})
	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	return clusters, kubeconfig
}

// A refused connection is a refused connection on every platform.
//
// Against a local port with nothing listening, every row must not come back
// **"unclassified — Nobody yet: this failure is not one of the known
// shapes"**. The classifier's "connection
// refused" arm is the Linux wording; Windows says `connectex: No connection
// could be made because the target machine actively refused it.` In the
// container the text arm is right and this is unreachable, but the downloadable
// binary is exactly where a kind or docker-desktop cluster on the loopback runs.
//
// A real dial rather than a hand-written error, because the whole point is what
// the platform's own stack produces: a fabricated `syscall.ECONNREFUSED` would
// pass on Windows against a classifier that does not handle Winsock's number.
func TestARefusedConnectionIsNamedOnThisPlatformToo(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	// Closed at once: the address is then one nothing listens on, which is what a
	// cluster on the host's loopback looks like from inside a container.
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	_, dialErr := http.Get("http://" + addr + "/version?timeout=30s")
	if dialErr == nil {
		t.Skip("something answered a port that was just closed")
	}

	state, detail, _ := classify(dialErr)
	if state != StateRefused {
		t.Errorf("a refused connection was classified %q (%s) from %v.\n"+
			"On this page that renders as 'unclassified' and 'Nobody yet', on the one "+
			"failure whose cause is written down: a cluster on 127.0.0.1 means the host, "+
			"which inside a container is not this process", state, detail, dialErr)
	}
}

// The reason, not the path it happened to.
//
// With the path, the banner draws `open C:\Users\…\fakehome\.aws\sso\cach…` —
// the whole 180-character path, ellipsized by detailLine at 200 characters
// exactly where the reason would have started. The directory is already on the
// page in its own element, so repeating it inside the reason costs the reason:
// a destination is not a diagnosis.
func TestTheReasonIsReportedWithoutThePathItHappenedTo(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("a-long-path-segment/", 12), "cache")

	h := New(Options{AWSDir: long})
	h.readDir = refusingDir(&fs.PathError{Op: "open", Path: long, Err: fs.ErrPermission})

	got := h.SSO()
	if strings.Contains(got.Problem, "a-long-path-segment") {
		t.Errorf("Problem = %q. The path is in Dir already, and repeating it here is what "+
			"pushed the reason past the 200-character cut on the live page", got.Problem)
	}
	if !strings.Contains(got.Problem, "permission denied") {
		t.Errorf("Problem = %q, want the reason itself", got.Problem)
	}
	if got.Dir != long {
		t.Errorf("Dir = %q; the path still has to be somewhere", got.Dir)
	}
}
