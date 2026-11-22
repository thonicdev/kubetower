package launch

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/thonicdev/kubetower/internal/auth"
	"github.com/thonicdev/kubetower/internal/statefile"
)

// The one-time note, tested as the three promises it makes: it can be pasted, it
// is no more exposed than what already sits beside it, and it goes away.

// TestTheOneTimeFileCanBePasted.
//
// Shown is not the same as obtainable: a
// twenty-character random string displayed perfectly is unusable if getting it
// out means selecting around a label. The note's whole layout exists for this —
// the password alone on its own line — so it is asserted rather than assumed,
// and the helper that reads it back does so the way a person would.
func TestTheOneTimeFileCanBePasted(t *testing.T) {
	dir := stateDir(t)
	captureLog(t)
	withConsole(t, false)
	withoutDialog(t)

	if _, _, err := create(filepath.Join(dir, credentialsFile)); err != nil {
		t.Fatalf("create: %v", err)
	}

	path := passwordOncePath()
	password := passwordFromOnceFile(t, path)
	if len(password) != generatedLength {
		t.Fatalf("the pasteable line is %d characters, want %d", len(password), generatedLength)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	note := string(raw)
	// The three questions an operator has when they find this file, each of which
	// they otherwise have to go and look up.
	for _, expected := range []string{
		"deletes this file", // that it is temporary
		"--reset-password",  // what to do if it is lost
		credentialsFile,     // where the hash went
		"user:",             // as whom
		"address:",          // and where
	} {
		if !strings.Contains(note, expected) {
			t.Errorf("the note never says %q. Every line in it is one an operator reads "+
				"exactly once, and a missing one is a question they have to go and answer "+
				"somewhere else:\n%s", expected, note)
		}
	}
}

// TestTheOneTimeFileIsNoMoreExposedThanTheCredentialsBesideIt.
//
// The argument for writing a password down at all is that this is not a new
// class of exposure: `credentials.json` is in the same directory, with the same
// mode, and holds the session signing secret in clear — the key that mints a
// valid cookie with no password at all. If the note were ever created more
// loosely than that file, the argument stops holding and this says so.
//
// **On Windows the mode means very little** and the test says which fact it is
// resting on instead: `statefile.Dir()` is under the operator's own profile, so
// the directory is per user before any mode is considered. Asserting 0600 there
// would be asserting something Go's os package emulates rather than something
// Windows enforces, which is worse than not asserting it.
func TestTheOneTimeFileIsNoMoreExposedThanTheCredentialsBesideIt(t *testing.T) {
	dir := stateDir(t)
	captureLog(t)
	withConsole(t, false)
	withoutDialog(t)

	if _, _, err := create(filepath.Join(dir, credentialsFile)); err != nil {
		t.Fatalf("create: %v", err)
	}

	note, err := os.Stat(passwordOncePath())
	if err != nil {
		t.Fatalf("stat %s: %v", passwordOnceFile, err)
	}
	kept, err := os.Stat(filepath.Join(dir, credentialsFile))
	if err != nil {
		t.Fatalf("stat %s: %v", credentialsFile, err)
	}

	if runtime.GOOS == "windows" {
		// What is left to assert here is that both files are in the one
		// per-user directory rather than anywhere else, which is the property
		// actually being relied on.
		if filepath.Dir(passwordOncePath()) != dir {
			t.Errorf("%s is not in the state directory (%s), so it does not inherit the "+
				"per-user protection credentials.json has", passwordOnceFile, dir)
		}
		return
	}

	if note.Mode().Perm() != kept.Mode().Perm() {
		t.Errorf("%s is %v and %s is %v. The note holds a usable password in clear, so it "+
			"may never be created more loosely than the file holding the session secret",
			passwordOnceFile, note.Mode().Perm(), credentialsFile, kept.Mode().Perm())
	}
	if note.Mode().Perm() != statefile.FilePerm {
		t.Errorf("%s is %v, want %v", passwordOnceFile, note.Mode().Perm(), statefile.FilePerm)
	}
}

// TestTheOneTimeFileCannotBeSweptAway.
//
// `internal/statefile` removes every file in the directory beginning with the
// target's own name and a dot — that is how it clears the temporary files a
// crash between CreateTemp and Rename leaves behind, and it is what destroys a
// `credentials.json.old` backup, which `resetPassword`'s own comment records.
// A note called `credentials.json.password` would therefore be deleted by the
// very save that keeps its hash, on the first run, before anybody read it.
//
// Cheap to assert and impossible to see in review, which is exactly the shape
// worth a test.
func TestTheOneTimeFileCannotBeSweptAway(t *testing.T) {
	if strings.HasPrefix(passwordOnceFile, credentialsFile+".") {
		t.Fatalf("%s begins with %q, so writing the credentials file sweeps it away before "+
			"anyone can read it", passwordOnceFile, credentialsFile+".")
	}
	// And the other direction, which nothing writes today and somebody could.
	if strings.HasPrefix(credentialsFile, passwordOnceFile+".") {
		t.Fatalf("%s begins with %q, so writing the note would sweep away the credentials",
			credentialsFile, passwordOnceFile+".")
	}
}

// TestRemovingTheNoteNeverFails. It is called from inside a login, and an
// operator shut out of their own console because a file could not be unlinked is
// a far worse outcome than a note that lingers. Every one of these is a real
// state: nothing written yet, written once, and a state directory that is not
// there at all.
func TestRemovingTheNoteNeverFails(t *testing.T) {
	dir := stateDir(t)
	captureLog(t)

	removePasswordOnce() // nothing there
	if err := os.WriteFile(passwordOncePath(), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	removePasswordOnce()
	if _, err := os.Stat(passwordOncePath()); !os.IsNotExist(err) {
		t.Fatalf("the note is still there: %v", err)
	}
	removePasswordOnce() // and again, which is every login after the first
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	removePasswordOnce() // no directory at all
}

// TestALoginTakesTheNoteAwayAndAFailedOneDoesNot is the whole chain, from the
// first run through `frontDoor` to a real POST on `/auth/login`.
//
// **It is deliberately not a test of `removePasswordOnce` in isolation.** That
// function is three lines and could not plausibly be wrong; what can be wrong —
// and what nothing else here would catch — is the *wiring*: `Config.OnLogin`
// left unset, set on a branch that a container's start does not take, or called
// before the credential check rather than after it. Each of those compiles, each
// passes every other test in this package, and each leaves a plaintext password
// on the disk for ever or deletes it for a guess.
func TestALoginTakesTheNoteAwayAndAFailedOneDoesNot(t *testing.T) {
	dir := stateDir(t)
	captureLog(t)
	withConsole(t, false)
	withoutDialog(t)

	// frontDoor reads the environment first, and an inherited hash would send it
	// down the branch that never draws a password.
	t.Setenv("KT_PASSWORD_HASH", "")
	t.Setenv("KT_SESSION_SECRET", "")
	t.Setenv("KT_USER", "")

	// storedCredentials is memoized for the life of the process — correctly, see
	// its comment — so a test that wants a first run has to clear the memo.
	//
	// Cleared again afterwards rather than restored: a sync.Once cannot be copied
	// (go vet says so, and it is right — the copy would carry a half-taken lock),
	// and the memo it guards is only ever a cache of what is on disk. Leaving a
	// fresh one behind means the next caller reads the state directory in front
	// of it, which is the correct answer for any test that follows.
	forgetCredentials(t)

	// A desktop that draws a password at all: one listening beyond loopback.
	// The loopback desktop draws none, and
	// passwordless_test.go holds that.
	t.Setenv("KT_LISTEN", "0.0.0.0:5823")

	front, _, err := frontDoor(false, nil)
	if err != nil {
		t.Fatalf("frontDoor: %v", err)
	}

	note := passwordOncePath()
	password := passwordFromOnceFile(t, note)
	if _, err := os.Stat(filepath.Join(dir, credentialsFile)); err != nil {
		t.Fatalf("the first run kept no credentials: %v", err)
	}

	// The wrong password first, because the interesting failure is a hook called
	// before the check rather than after it — and that ordering is invisible
	// unless a refused login is tried while the note is still there.
	post(t, front, "admin", password+"x", http.StatusUnauthorized)
	if _, err := os.Stat(note); err != nil {
		t.Fatalf("a refused login deleted %s. The one copy of the password is then gone and "+
			"the console is shut, on a mistyped character: %v", passwordOnceFile, err)
	}

	post(t, front, "admin", password, http.StatusSeeOther)
	if _, err := os.Stat(note); !os.IsNotExist(err) {
		t.Fatalf("%s survived a successful login, so a password sits in clear on the disk "+
			"that credentials.json deliberately holds only a bcrypt hash on: %v",
			passwordOnceFile, err)
	}

	// And the second login, which is every login for the rest of the
	// installation's life: it must not care that the file has gone.
	post(t, front, "admin", password, http.StatusSeeOther)
}

// forgetCredentials clears the process-wide memo in front of storedCredentials,
// before the test and again after it.
func forgetCredentials(t *testing.T) {
	t.Helper()
	clear := func() {
		credentialsOnce = sync.Once{}
		heldCredentials, heldKey, heldErr = credentials{}, nil, nil
	}
	clear()
	t.Cleanup(clear)
}

// post drives one login through the real handler and checks the status.
func post(t *testing.T, front *auth.Auth, user, password string, want int) {
	t.Helper()
	form := url.Values{"user": {user}, "password": {password}}
	r := httptest.NewRequest(http.MethodPost, auth.LoginPath, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	front.Login(w, r)
	if w.Code != want {
		t.Fatalf("login as %q: status %d, want %d", user, w.Code, want)
	}
}
