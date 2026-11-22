package health

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The scan has to tell four things apart, and three of them are not findings.
//
// A false positive here is worse than no banner at all: a red panel on the health
// page of an operator whose credentials are fine teaches them that this page
// cries wolf, and the whole value of internal/health is that its rows can be
// believed. So every case below that is *not* a corruption is asserted as loudly
// as the one that is.
func TestTheCredentialCacheScanTellsDamageFromEverythingElse(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	// The failure. Two complete documents end to end, which is exactly what the
	// CLI leaves when two processes write one entry: the second is shorter, so the
	// tail of the first survives. `json.Valid` rejects the trailing data.
	write("aaa.json", `{"Credentials":{"Expiration":"2026-08-21T10:00:00Z"}}{"Credentials":{}}`)
	// The same shape as it actually arrives — a *fragment* rather than a whole
	// second document — because the overwrite is by length, not by document.
	write("bbb.json", `{"Credentials":{"SessionToken":"x"}}ials":{}}`)
	// Healthy entries. Two of them, so "1 of 14" arithmetic has something to be
	// wrong about.
	write("ccc.json", `{"Credentials":{"SessionToken":"y"}}`)
	write("ddd.json", `{"Credentials":{"SessionToken":"z"}}`)
	// The copy the CLI leaves beside an entry it has given up on. **Expected to be
	// unparseable**, and reporting it would double every count and name a file
	// whose deletion changes nothing.
	write("aaa.json.corrupt.bak", `{"Credentials":{}}{"Credentials":{}}`)
	// Not ours. `session.db` sits in this directory on a real installation.
	write("session.db", "\x00\x01not json at all")

	// New rather than a literal: it is what fills the readDir/readFile seam the
	// scan goes through, and a hand-built Health has neither.
	h := New(Options{CredDir: dir})
	got := h.CredentialCache()

	if !got.Checked {
		t.Fatal("the scan reports it did not look, at a directory it just read")
	}
	if want := 4; got.Total != want {
		t.Errorf("examined %d entries, want %d — only `.json` counts, so the `.corrupt.bak` "+
			"and `session.db` must not be in this number", got.Total, want)
	}
	if want := "aaa.json,bbb.json"; strings.Join(got.Corrupt, ",") != want {
		t.Errorf("corrupt = %v, want [%s]", got.Corrupt, want)
	}
	for _, name := range got.Corrupt {
		if strings.HasSuffix(name, ".bak") {
			t.Errorf("%q is reported as damage. It is the CLI's own copy of damage already "+
				"found; deleting it fixes nothing and naming it doubles the count", name)
		}
		if name == "session.db" {
			t.Error("session.db is reported as a damaged credential entry. It is not a " +
				"credential entry, and telling somebody to delete it is bad advice")
		}
	}
	if len(got.Unreadable) != 0 {
		t.Errorf("unreadable = %v on a directory everything in which opened", got.Unreadable)
	}
	if !got.Broken() {
		t.Error("Broken() is false with two damaged entries, so no banner would be drawn")
	}
}

// A directory that is not there is not a finding.
//
// This is the ordinary state of an operator who only uses SSO: there are no
// assume-role profiles, so the CLI never creates this directory. A question that
// was never asked is not an answer of "nothing wrong", and it is
// certainly not an answer of "something wrong".
func TestAnAbsentCredentialCacheIsNotAFinding(t *testing.T) {
	h := New(Options{CredDir: filepath.Join(t.TempDir(), "not-there")})
	got := h.CredentialCache()

	if got.Broken() {
		t.Errorf("a missing cache directory is reported as damage: %+v", got)
	}
	if got.Checked {
		t.Error("the scan claims it looked at a directory that does not exist. The page draws " +
			"nothing either way, but `Checked` is what tells a reader of the JSON whether " +
			"`no corruption` means `I looked` or `I could not`")
	}

	// And an empty one that does exist.
	empty := New(Options{CredDir: t.TempDir()})
	if got := empty.CredentialCache(); got.Broken() || !got.Checked {
		t.Errorf("an empty cache directory came back as %+v, want looked-at and nothing wrong", got)
	}
}

// The directory is derived from the SSO one, so a test — and an operator — cannot
// end up with the two pointing at different `~/.aws`.
func TestTheCredentialCacheSitsBesideTheSSOCache(t *testing.T) {
	h := New(Options{AWSDir: filepath.Join("/somewhere", ".aws", "sso", "cache")})
	want := filepath.Join("/somewhere", ".aws", "cli", "cache")
	if got := h.CredentialCacheDir(); got != want {
		t.Errorf("CredentialCacheDir = %q, want %q", got, want)
	}

	// An explicit override wins, which is what lets the test above exist.
	h2 := New(Options{AWSDir: "/a/.aws/sso/cache", CredDir: "/b"})
	if got := h2.CredentialCacheDir(); got != "/b" {
		t.Errorf("CredentialCacheDir = %q, want the override", got)
	}

	// Nothing known means no scan rather than a guess at the filesystem root.
	h3 := &Health{}
	if got := h3.CredentialCacheDir(); got != "" {
		t.Errorf("CredentialCacheDir = %q with no ~/.aws known; scanning a guessed path is "+
			"how a page comes to report on somebody else's files", got)
	}
}

// A big file is not a credential document and is not read.
//
// The bound is not about speed. It is that judging a file which has nothing to do
// with credentials would produce a sentence telling the operator to delete
// something unrelated, which is the worst outcome this banner can have.
func TestAFileTooLargeToBeACredentialIsLeftAlone(t *testing.T) {
	dir := t.TempDir()
	big := make([]byte, credCacheMaxBytes+1)
	for i := range big {
		big[i] = 'x' // not JSON, so it would be reported if it were read
	}
	if err := os.WriteFile(filepath.Join(dir, "huge.json"), big, 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	got := New(Options{CredDir: dir}).CredentialCache()
	if got.Broken() {
		t.Errorf("a %dKB file is reported as a damaged credential entry: %+v",
			len(big)/1024, got)
	}
	if got.Total != 0 {
		t.Errorf("Total = %d, want 0 — the file was skipped, so counting it would make "+
			"`0 of 1` out of a directory with nothing wrong in it", got.Total)
	}
}

// The operator's own cache is scanned separately, and only when it is mounted.
//
// **This exists because the fix for the corruption would blind the diagnosis of it.**
// The private volume and the per-context directories stop KubeTower corrupting
// anything — and they also hide the operator's own cache, which is the one that
// killed an `argocd cluster add` from their terminal. So it comes back read-only
// at a second path, and the two reports must stay distinguishable: a damaged entry
// in the container's cache takes KubeTower's clusters dark, one in the operator's
// takes their shell down while every row on the page stays green.
func TestTheOperatorsOwnCacheIsScannedApartFromThisProcesss(t *testing.T) {
	own, host := t.TempDir(), t.TempDir()
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	// This process's cache is fine; the operator's is not. That asymmetry is the
	// case the feature exists for and the one a shared code path would blur.
	write(own, "aaa.json", `{"Credentials":{"SessionToken":"y"}}`)
	write(host, "4cf52cfd.json", `{"Credentials":{}}{"Credentials":{}}`)
	write(host, "ok.json", `{"Credentials":{"SessionToken":"z"}}`)

	h := New(Options{CredDir: own, HostCredDir: host})

	mine := h.CredentialCache()
	if mine.Broken() {
		t.Errorf("this process's own cache is reported as damaged: %+v", mine)
	}
	if mine.Host {
		t.Error("this process's own report is marked as the host's, so the page would tell " +
			"the operator to fix the wrong directory")
	}

	theirs := h.HostCredentialCache()
	if !theirs.Broken() {
		t.Fatal("the operator's damaged cache is not reported, so the failure that breaks " +
			"their own terminal stays invisible — which is exactly what the " +
			"read-only mount exists to prevent")
	}
	if !theirs.Host {
		t.Error("the host report is not marked as such; the two banners would read alike")
	}
	if want := "4cf52cfd.json"; len(theirs.Corrupt) != 1 || theirs.Corrupt[0] != want {
		t.Errorf("corrupt = %v, want [%s]", theirs.Corrupt, want)
	}
	if theirs.Dir != host {
		t.Errorf("the host report names %q, not the mounted directory %q — and the `rm` on "+
			"the page is built from that path", theirs.Dir, host)
	}
}

// No mount means no second scan, and that is not a finding.
//
// It is the ordinary state of `go run ./cmd/kubetower`, where the process's own
// cache already *is* the operator's. A guessed path here would have this page
// reporting on a directory that has nothing to do with the deployment.
func TestWithNoHostMountThereIsNoSecondScan(t *testing.T) {
	t.Setenv("KT_HOST_AWS_CACHE", "")
	got := New(Options{CredDir: t.TempDir()}).HostCredentialCache()
	if got.Broken() || got.Checked || got.Dir != "" {
		t.Errorf("an unmounted host cache came back as %+v, want nothing looked at", got)
	}

	// And the environment is what supplies it in the container, since nothing sets
	// the field there.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{}{}`), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	t.Setenv("KT_HOST_AWS_CACHE", dir)
	if got := New(Options{}).HostCredentialCache(); !got.Broken() {
		t.Errorf("KT_HOST_AWS_CACHE is not read, so the compose deployment scans nothing: %+v", got)
	}
}
