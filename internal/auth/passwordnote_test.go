package auth

import (
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// The login page's one-time-password hint, from this package's side: what it
// draws, when it draws nothing, and what it does when the caller misbehaves.
//
// The end-to-end proof that the path and not the password reaches the page lives
// in internal/launch, where the real predicate and the real file are — see
// `TestTheLoginPageNamesTheOneTimeFileAndNeverThePasswordInIt`. What is left
// here is the contract this package promises about a callback it is handed.

func authWithNote(t *testing.T, note func() string) *Auth {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("preparing the test hash: %v", err)
	}
	a, err := New(Config{
		User:         "operator",
		PasswordHash: string(hash),
		SessionKey:   []byte("a-thirty-two-byte-key-for-tests!"),
		SessionIdle:  time.Hour,
		PasswordNote: note,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.delay = 0
	return a
}

func page(t *testing.T, a *Auth) string {
	t.Helper()
	w := httptest.NewRecorder()
	a.LoginPage(w, httptest.NewRequest(http.MethodGet, LoginPath, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: %d\n%s", LoginPath, w.Code, w.Body.String())
	}
	return w.Body.String()
}

// TestTheLoginPageOnlyMentionsAFileWhenThereIsOne.
//
// Three ways of having nothing to say, and all three have to draw the same page:
// no callback at all (the container, and every installation past its first
// login), a callback that answers "nothing", and one that answers with a path.
// Claiming a file that is not there sends somebody hunting on the one page where
// they have nothing else to go on.
func TestTheLoginPageOnlyMentionsAFileWhenThereIsOne(t *testing.T) {
	const path = "/var/lib/kubetower/PASSWORD-ONCE.txt"

	cases := []struct {
		name string
		note func() string
		says bool
	}{
		{name: "no callback", note: nil},
		{name: "nothing to point at", note: func() string { return "" }},
		{name: "a note on disk", note: func() string { return path }, says: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := page(t, authWithNote(t, tc.note))
			if got := strings.Contains(body, path); got != tc.says {
				t.Fatalf("the page names %s: %v, want %v\n%s", path, got, tc.says, body)
			}
			if tc.says {
				// Named with a sentence saying what it is, and in a block that
				// selects on its own — the path is meant to be taken somewhere
				// else, and shown is not the same as obtainable.
				if !strings.Contains(body, "First run.") {
					t.Errorf("a path with no sentence around it:\n%s", body)
				}
				if !strings.Contains(body, "<code>"+path+"</code>") {
					t.Errorf("the path is not in a block of its own, so selecting it takes "+
						"the prose with it:\n%s", body)
				}
				return
			}
			if strings.Contains(body, "First run.") {
				t.Errorf("the page claims a first run with no file behind it:\n%s", body)
			}
		})
	}
}

// TestAFailingHintCannotTakeTheLoginPageWithIt.
//
// The hint is the least important thing on this page and it renders inside
// net/http's handler goroutine, so a panic in a caller's predicate would abort
// the response for every visitor on every attempt — an operator locked out of
// their own console by a courtesy, with the cause in another package. Same
// contract as OnLogin, and the same reason it is written down rather than
// assumed.
func TestAFailingHintCannotTakeTheLoginPageWithIt(t *testing.T) {
	// The recovered panic is logged, and the log is the test's own stderr.
	// Restored to stderr rather than to nil, which is a writer that panics.
	log.SetOutput(&strings.Builder{})
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	a := authWithNote(t, func() string { panic("the state directory went away") })
	body := page(t, a)
	if !strings.Contains(body, `name="password"`) {
		t.Fatalf("the login form did not survive a panicking hint:\n%s", body)
	}
	if strings.Contains(body, "First run.") {
		t.Errorf("a hint was drawn from a callback that panicked:\n%s", body)
	}
}
