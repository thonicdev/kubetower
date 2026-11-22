package health

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// A credential cache that could not be listed and one holding nothing are
// different answers — the SSO cache's rule, one directory along.
//
// Answering every `os.ReadDir` failure with `CredentialCache{Dir, Host}`:
// `Checked: false`, no entries, would make `Broken()` false and
// `internal/web.cacheBannerOf` return nil. **No banner at all**, on
// the one page that exists to look at this directory, for the one failure in
// this whole package that **cannot be diagnosed from the error** — the AWS CLI
// prints the cache key as its entire message and client-go does not pass even
// that through (see credcache.go's header). If the page cannot look, it must say
// it could not look.
//
// Milder than the SSO one only in that there is no file to name. It is the same
// silence in the same reassuring direction.
func TestACredentialCacheThatWillNotListIsNotACacheHoldingNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	h := New(Options{CredDir: dir})
	h.readDir = refusingDir(&fs.PathError{Op: "open", Path: dir, Err: fs.ErrPermission})

	got := h.CredentialCache()
	if !got.Unknown() {
		t.Fatalf("%+v — a cache directory that refused to list reported nothing wrong, so "+
			"cacheBannerOf draws no banner and the page is silent about the one failure "+
			"only looking can find", got)
	}
	if got.Checked {
		t.Error("Checked is true for a directory nobody could list")
	}
	if got.Broken() {
		t.Error("Broken() is true with no entry named. The banner it gates builds an `rm` " +
			"out of the file names, so this would put a bare `rm` on the page with nothing " +
			"after it")
	}
	if got.Dir != dir {
		t.Errorf("Dir = %q, want %q — looking by hand is the only remaining answer, and "+
			"the page has nothing to name otherwise", got.Dir, dir)
	}
	if !strings.Contains(strings.ToLower(got.Problem), "permission") {
		t.Errorf("Problem = %q, want the reason in it", got.Problem)
	}
	// The reason and not the path it happened to: Dir already carries the path,
	// and repeating it pushes the banner's reason past detailLine's
	// 200-character cut.
	if strings.Contains(got.Problem, dir) {
		t.Errorf("Problem = %q repeats the directory that is already in Dir", got.Problem)
	}
}

// The same fact through the filesystem, where the filesystem can express it.
//
// Both this and the one above, not either: Windows has no mode bits that deny
// the owner, and the obvious substitute lies there — `os.ReadDir` over a path
// that is a *file* answers ERROR_PATH_NOT_FOUND, which `errors.Is(err,
// fs.ErrNotExist)` reports as "not there". So on Windows
// the filesystem trick would silently exercise the *absent* branch and pass for
// the wrong reason, which is the defect itself.
func TestARealCredentialCacheThatDeniesUsIsNotAnEmptyOne(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(dir, "a.json"),
		[]byte(`{"Credentials":{"SessionToken":"x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// It reads before the directory is closed to us, or every assertion below
	// passes for the wrong reason.
	if got := New(Options{CredDir: dir}).CredentialCache(); !got.Checked || got.Total != 1 {
		t.Fatalf("baseline: %+v, want one entry examined", got)
	}

	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("making %s unreadable: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, err := os.ReadDir(dir); err == nil {
		t.Skip("this filesystem let the owner list a mode-000 directory, so there is " +
			"nothing to test")
	}

	got := New(Options{CredDir: dir}).CredentialCache()
	if !got.Unknown() {
		t.Errorf("%+v — a mode-000 cache reported nothing wrong", got)
	}
	if got.Checked {
		t.Error("Checked is true for a mode-000 directory")
	}
}

// And the case that must **not** trip it: a directory that is simply not there.
//
// This is the ordinary state of an installation with no assume-role profiles, and
// of every `go run ./cmd/kubetower` with no host mount. Reporting it as "nobody
// could tell" would put an amber panel on every one of those pages — the mirror
// of the bug and just as good at teaching people to ignore this page.
func TestAnAbsentCredentialCacheIsStillNotAFailureToLook(t *testing.T) {
	got := New(Options{CredDir: filepath.Join(t.TempDir(), "not-there")}).CredentialCache()
	if got.Unknown() {
		t.Errorf("%+v — a directory that does not exist is reported as a failure to look. "+
			"It is not there, which is a fact about this installation", got)
	}
	if got.Problem != "" {
		t.Errorf("Problem = %q for an absent directory", got.Problem)
	}

	// And an empty one that does exist: looked at, nothing wrong, nothing unknown.
	if got := New(Options{CredDir: t.TempDir()}).CredentialCache(); got.Unknown() ||
		got.Broken() || !got.Checked {
		t.Errorf("%+v, want looked-at with nothing wrong", got)
	}
}

// One layer in: the directory lists and an entry will not open. Already reported,
// and it must stay reported *as a permissions problem* rather than being folded
// into the unreadable-directory answer — the two have different fixes and the
// page says so.
func TestAnEntryThatWillNotOpenIsStillItsOwnAnswer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	h := New(Options{CredDir: dir})
	h.readFile = func(name string) ([]byte, error) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
	}

	got := h.CredentialCache()
	if got.Unknown() {
		t.Errorf("%+v — the directory listed, so this is not a failure to look. Folding it "+
			"in would lose the sentence that says it is a permissions problem and not a "+
			"damaged file", got)
	}
	if !got.Broken() || len(got.Unreadable) != 1 {
		t.Errorf("%+v, want the entry named as unreadable", got)
	}
}

// **Readiness degrades on two conditions only, and this is the assertion for it.**
//
// `/kt-healthz` degraded means exactly two things: the kubeconfig no longer
// reads, or not one context answered its last probe. An AWS cache directory this
// process cannot list is neither. A probe that failed on it would restart the
// container in a loop and take away the one page that names the problem, at the
// moment somebody opened it — which is the whole argument in readiness.go.
//
// Some clusters unreachable is still 200, and this test's kubeconfig is in
// exactly that state: one context answering, one dark.
func TestAnUnreadableCredentialCacheDoesNotDegradeTheReadinessProbe(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")
	t.Setenv(hostCredDirEnv, "")

	clusters, kubeconfig := oneAnsweringAndOneDark(t)

	// Two directories, and readDir refuses only the credential one: the SSO cache
	// must go on answering, or this test would pass on the SSO banner's arm rather
	// than on the credential cache's.
	credDir := filepath.Join(t.TempDir(), "cli-cache")
	ssoDir := filepath.Join(t.TempDir(), "sso-cache")
	if err := os.MkdirAll(ssoDir, 0o700); err != nil {
		t.Fatal(err)
	}

	h := New(Options{Clusters: clusters, Kubeconfig: cluster.Kubeconfig{kubeconfig}, Timeout: time.Second,
		AWSDir: ssoDir, CredDir: credDir})
	real := h.readDir
	h.readDir = func(name string) ([]os.DirEntry, error) {
		if name == credDir {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
		}
		return real(name)
	}

	h.All(context.Background(), true)

	if !h.CredentialCache().Unknown() {
		t.Fatal("the credential cache was readable, so this test proves nothing")
	}
	if got := h.SSO(); !got.Checked {
		t.Fatal("the SSO cache was also unreadable, so a pass here would not be about " +
			"the credential cache at all")
	}

	r := h.Readiness(context.Background())
	if !r.OK {
		t.Fatalf("readiness is degraded (%q) with a cluster answering. An unreadable AWS "+
			"credential cache is not a Kubernetes fact; a probe that fails on it restarts "+
			"the container in a loop and removes the page that names the problem",
			r.Reason)
	}
	if r.Answered != 1 {
		t.Errorf("Answered = %d of %d probed, want 1", r.Answered, r.Probed)
	}
	// And the reason says nothing about a cache, which would be this fact leaking
	// into an answer that is only ever about clusters.
	for _, word := range []string{"cache", "credential", "aws"} {
		if strings.Contains(strings.ToLower(r.Reason), word) {
			t.Errorf("readiness reason is %q; /kt-healthz means two things and this is not "+
				"one of them", r.Reason)
		}
	}
}
