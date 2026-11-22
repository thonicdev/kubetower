package launch

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/thonicdev/kubetower/internal/auth"
)

// The other half of a first run with no console.
//
// `PASSWORD-ONCE.txt` makes the drawn password **obtainable**. Nothing else makes it
// **discoverable**: a `.app` opened from the Finder puts the operator on a login
// page with no credentials and no reason to believe a file was written. The
// login page names it, which is the one place they are certainly looking.
//
// These tests drive the real thing end to end — `frontDoor` builds the front
// door the process runs, so what is under test is the wiring as much as the
// markup. A stub predicate here would assert that a template renders a string it
// was handed, which nothing has ever got wrong.

// loginPage renders the page the way a browser asks for it.
func loginPage(t *testing.T, front *auth.Auth) string {
	t.Helper()
	w := httptest.NewRecorder()
	front.LoginPage(w, httptest.NewRequest(http.MethodGet, auth.LoginPath, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: %d", auth.LoginPath, w.Code)
	}
	return w.Body.String()
}

// TestTheLoginPageNamesTheOneTimeFileAndNeverThePasswordInIt.
//
// The whole guarantee in one test, because the two halves are one decision: the
// page is unauthenticated, so it may say **where** the password is and may never
// say **what** it is. The marker is not synthetic — it is the password this
// first run actually drew, read back out of the note the way an operator would —
// so a change that put the file's contents on the page fails here whatever it
// did to the wording.
//
// It also covers the third property: the hint goes when the file goes. A first
// login deletes the note, and a page still pointing at it would send somebody
// hunting for something that no longer exists, which is the inverse of the
// failure this feature closes.
func TestTheLoginPageNamesTheOneTimeFileAndNeverThePasswordInIt(t *testing.T) {
	stateDir(t)
	captureLog(t)
	withConsole(t, false)
	withoutDialog(t)

	// A first run: nothing configured, so the password is drawn and the note is
	// written. An inherited hash would take the other branch entirely.
	t.Setenv("KT_PASSWORD_HASH", "")
	t.Setenv("KT_SESSION_SECRET", "")
	t.Setenv("KT_USER", "")
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

	body := loginPage(t, front)
	if !strings.Contains(body, note) {
		t.Errorf("the login page never names %s, so a first run on a machine with no console "+
			"leaves the operator on a password prompt with no way of knowing a file was "+
			"written:\n%s", note, body)
	}
	if !strings.Contains(body, "First run.") {
		t.Errorf("the page names a path with no sentence saying what it is:\n%s", body)
	}
	if strings.Contains(body, password) {
		t.Fatalf("the login page carries the drawn password itself. This page is served " +
			"before anything is trusted: the path is public, the value never is")
	}
	raw, err := os.ReadFile(note)
	if err != nil {
		t.Fatal(err)
	}
	// And not by another route either — the whole note, or the line above the
	// password, would each carry it onto the page.
	for _, line := range strings.Split(string(raw), "\n") {
		if trimmed := strings.TrimSpace(line); len(trimmed) > 8 && strings.Contains(body, trimmed) &&
			!strings.Contains(note, trimmed) {
			t.Errorf("a line of the note reached the login page: %q", trimmed)
		}
	}

	// The file's whole life is the gap before somebody logs in. Afterwards the
	// hint must be gone — which is why the predicate is asked per render.
	post(t, front, "admin", password, http.StatusSeeOther)
	if _, err := os.Stat(note); !os.IsNotExist(err) {
		t.Fatalf("the login did not take the note away: %v", err)
	}
	after := loginPage(t, front)
	if strings.Contains(after, note) || strings.Contains(after, "First run.") {
		t.Errorf("the login page still points at %s after it was deleted:\n%s", note, after)
	}
}

// TestTheLoginPageSaysNothingAboutAPasswordTheDeploymentSet.
//
// A stale note is the case this is about, and it is a real one: an operator who
// ran the binary once and then put `KT_PASSWORD_HASH` in `.env` has a file on
// disk whose password opens nothing. `removePasswordOnce` still clears it at
// their first login — that hook is wired on every branch, deliberately — but a
// login page pointing at it would be worse than saying nothing, because it names
// a file that is wrong rather than one that is merely absent.
func TestTheLoginPageSaysNothingAboutAPasswordTheDeploymentSet(t *testing.T) {
	stateDir(t)
	captureLog(t)

	const marker = "marker-for-a-password-the-page-must-not-carry"
	if err := os.WriteFile(passwordOncePath(), []byte(marker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte("configured"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KT_PASSWORD_HASH", string(hash))
	t.Setenv("KT_SESSION_SECRET", strings.Repeat("a", 64))
	t.Setenv("KT_USER", "")
	forgetCredentials(t)

	front, _, err := frontDoor(false, nil)
	if err != nil {
		t.Fatalf("frontDoor: %v", err)
	}

	body := loginPage(t, front)
	for _, refuse := range []string{passwordOnceFile, marker, "First run."} {
		if strings.Contains(body, refuse) {
			t.Errorf("the login page says %q on an installation whose password came from "+
				"KT_PASSWORD_HASH, where any note on disk is stale:\n%s", refuse, body)
		}
	}
}

// TestTheHintIsAskedForOnEveryRenderAndNeverHeld.
//
// The predicate is a stat, not a flag captured when the note was written, and
// this is the property that difference buys: the file appears and disappears
// under a running process — `--reset-password` writes one, a login deletes one —
// and a page rendered from a boolean read at start-up would be wrong in both
// directions.
func TestTheHintIsAskedForOnEveryRenderAndNeverHeld(t *testing.T) {
	stateDir(t)

	if got := drawnPasswordNote(); got != "" {
		t.Fatalf("with no note on disk the hint is %q, want nothing to point at", got)
	}
	if err := os.WriteFile(passwordOncePath(), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := drawnPasswordNote(); got != passwordOncePath() {
		t.Fatalf("with a note on disk the hint is %q, want %q", got, passwordOncePath())
	}
	if err := os.Remove(passwordOncePath()); err != nil {
		t.Fatal(err)
	}
	if got := drawnPasswordNote(); got != "" {
		t.Fatalf("after the note was deleted the hint is still %q", got)
	}
}
