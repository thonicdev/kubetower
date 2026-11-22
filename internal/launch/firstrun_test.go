package launch

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/thonicdev/kubetower/internal/statefile"
)

// firstrun.go decides whether a downloaded binary has a password at all. These
// tests state the properties rather than the implementation: what it must never
// do, and what it must refuse.

// stateDir points the whole state layer at a temporary directory, so nothing here
// can touch the operator's own state.
func stateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(statefile.EnvDir, dir)
	return dir
}

// captureLog redirects the standard logger and hands back what it collected.
//
// Restored through t.Cleanup rather than a defer at the call site, because a
// failing test that leaves the logger pointing at a dead buffer takes the rest of
// the package's output with it.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &buf
}

// withConsole sets the flag announce() branches on, and puts it back.
func withConsole(t *testing.T, attached bool) {
	t.Helper()
	previous := consoleAttached
	consoleAttached = attached
	t.Cleanup(func() { consoleAttached = previous })
}

// withoutDialog takes away the two platform channels, and every test here needs
// it for one reason on Windows and a different one everywhere else.
//
// On Windows showFirstRunPassword opens a **modal box**: run under `go test` it
// puts a window on the screen and waits for somebody to press OK, which is a
// build that never finishes. Off Windows it already returns false, so a test
// written without this passes there and hangs here — the worst distribution of
// outcomes available, since the machine that hangs is the one this is developed
// on.
//
// It also makes the refusal testable at all: what create() must do when nothing
// can show a password cannot be asserted while one of the channels really works.
func withoutDialog(t *testing.T) {
	t.Helper()
	dialog, clipboard := showPassword, copyPassword
	showPassword = func(drawnPassword) bool { return false }
	copyPassword = func(string) bool { return false }
	t.Cleanup(func() { showPassword, copyPassword = dialog, clipboard })
}

// blockTheNote makes the one-time file unwritable and leaves everything else
// alone, which is what it takes to test `announce`'s refusal at all.
//
// **Not `unwritableStateDir`, which would pass for the wrong reason.** With the
// whole directory blocked, `save` fails too — so deleting the `if !obtainable`
// refusal from `create` entirely would leave the test green, and that is the
// one mutation it exists to catch.
//
// A **directory** where the note goes is the narrow version: `statefile.Write`
// gets as far as its rename and cannot replace a directory with a file, on POSIX
// and on Windows alike, while `credentials.json` beside it writes perfectly. No
// chmod, no root check, no build tag.
func blockTheNote(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(passwordOncePath(), 0o700); err != nil {
		t.Fatalf("blocking %s: %v", passwordOnceFile, err)
	}
	if _, err := writePasswordOnce("admin", "irrelevant", "irrelevant"); err == nil {
		t.Fatalf("%s was written over a directory of the same name, so this helper no longer "+
			"makes anything fail", passwordOnceFile)
	}
}

// unwritableStateDir points the state layer at a path that cannot be created,
// which is how the tests reach the one remaining refusal.
//
// The trick is portable and deliberate: KT_STATE_DIR names a directory *inside a
// regular file*, so `os.MkdirAll` fails with ENOTDIR on POSIX and its Windows
// equivalent, without needing a chmod, a root check or a build tag. Making the
// directory read-only instead does nothing at all on Windows, and nothing when
// the test runs as root — which is every container this repository builds.
func unwritableStateDir(t *testing.T) string {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("this is a file"), 0o600); err != nil {
		t.Fatalf("writing the blocker: %v", err)
	}
	dir := filepath.Join(blocker, "state")
	t.Setenv(statefile.EnvDir, dir)
	// Proven rather than assumed: if some platform ever allowed this, every test
	// resting on it would pass while asserting nothing.
	if err := os.MkdirAll(dir, 0o700); err == nil {
		t.Fatalf("%s could be created, so this helper no longer makes anything fail", dir)
	}
	return dir
}

// TestThePasswordNeverReachesTheLog is the security property of this file, and
// it is load-bearing because the log can be a file.
//
// The design is that only a bcrypt hash is kept, so nothing on this disk can give
// the password back. logfile.go sends the log to kubetower.log when there is no
// console — exactly the case where the banner has no terminal to go to — so a
// banner routed through log would write the password in clear, to a file, on
// precisely the runs that have no other channel.
func TestThePasswordNeverReachesTheLog(t *testing.T) {
	dir := stateDir(t)
	logged := captureLog(t)
	withConsole(t, true)
	withoutDialog(t)

	var banner bytes.Buffer
	realStderr := os.Stderr
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = write
	done := make(chan struct{})
	go func() { _, _ = banner.ReadFrom(read); close(done) }()

	path := filepath.Join(dir, credentialsFile)
	held, _, err := create(path)
	_ = write.Close()
	os.Stderr = realStderr
	<-done
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	password := passwordFromBanner(t, banner.String())
	if password == "" {
		t.Fatal("the banner carried no password, so this test proves nothing")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(held.PasswordHash), []byte(password)); err != nil {
		t.Fatalf("the password shown is not the one whose hash was kept: %v", err)
	}

	if strings.Contains(logged.String(), password) {
		t.Error("the drawn password is in the log. When there is no console the log is a FILE " +
			"(logfile.go), so this writes it in clear onto the disk that firstrun.go keeps only " +
			"a bcrypt hash on. The banner goes to stderr and to the dialog, never through log")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if bytes.Contains(raw, []byte(password)) {
		t.Error("credentials.json contains the password in clear")
	}
}

// passwordFromBanner pulls the password back out of what was printed, so the test
// asserts against what a person actually saw rather than against an internal.
func passwordFromBanner(t *testing.T, banner string) string {
	t.Helper()
	for _, line := range strings.Split(banner, "\n") {
		_, after, found := strings.Cut(line, "password:")
		if !found {
			continue
		}
		return strings.Trim(strings.TrimSpace(after), "│ ")
	}
	return ""
}

// TestTheDrawnPasswordNeverReachesTheLogFile is the same property as the test
// above, asserted against the **file on disk** rather than against a buffer, and
// on both answers this process can give to "is anyone reading stderr".
//
// It is worth having separately because the log file is unconditional: every
// start writes one, on every platform, and the exposure this guards
// against — a plaintext password sitting in `~/.kubetower/kubetower.log`, on the
// disk `firstrun.go` keeps only a bcrypt hash on — is reachable from a launch
// with a perfectly good terminal.
//
// Three cases, and the third matters most: a Finder launch or a `.desktop`
// entry, where there is no console and no dialog and the password reaches the
// operator only through `PASSWORD-ONCE.txt`. That path writes a `log.Printf`
// naming the file, so it is one more way for the
// password to leak into the log and it has to be covered by name.
//
// The first two subtests capture the password from the *dialog* hook rather than
// from the banner, because with no console there is no banner to parse; the third
// takes it out of the one-time file, which is what a person would have read.
func TestTheDrawnPasswordNeverReachesTheLogFile(t *testing.T) {
	cases := []struct {
		name    string
		console bool
		dialog  bool
	}{
		{"with a terminal", true, true},
		{"with no terminal", false, true},
		// No console, no dialog: macOS from the Finder, Linux from a menu. The
		// one-time file is the channel, and the log line it writes is an
		// exposure worth guarding.
		{"with neither, delivered by the one-time file alone", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := stateDir(t)
			withConsole(t, tc.console)

			// Where a working dialog exists it is also where the test learns what
			// was drawn, which is what a person would have been shown.
			var shown string
			dialog, clipboard := showPassword, copyPassword
			showPassword = func(p drawnPassword) bool {
				if !tc.dialog {
					return false
				}
				shown = p.Password
				return true
			}
			copyPassword = func(string) bool { return false }
			t.Cleanup(func() { showPassword, copyPassword = dialog, clipboard })

			previous := log.Writer()
			t.Cleanup(func() { log.SetOutput(previous) })
			logPath, file := logToFile()
			if logPath == "" {
				t.Fatal("logToFile could not open a file in a directory it had just been given")
			}
			t.Cleanup(func() { _ = file.Close() })

			var held credentials
			var err error
			_ = capturingStderr(t, func() {
				held, _, err = create(filepath.Join(dir, credentialsFile))
			})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if !tc.dialog {
				shown = passwordFromOnceFile(t, passwordOncePath())
			}
			if shown == "" {
				t.Fatal("nothing carried a password, so this test proves nothing")
			}
			if err := bcrypt.CompareHashAndPassword([]byte(held.PasswordHash), []byte(shown)); err != nil {
				t.Fatalf("the password captured is not the one whose hash was kept: %v", err)
			}

			raw, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("reading %s: %v", logPath, err)
			}
			// Without this the test passes just as happily when the logger was
			// never pointed at the file at all, and an absence proves nothing.
			if !strings.Contains(string(raw), "first run") {
				t.Fatal("the log file caught none of this run's lines, so the password not " +
					"being in it is evidence of nothing")
			}
			// And the same demand of the new code path specifically: the line
			// announcing the one-time file must be in there, or its absence is
			// what this subtest would be measuring.
			if !strings.Contains(string(raw), passwordOnceFile) {
				t.Fatalf("the log never named %s, so it caught none of the lines this test "+
					"exists to inspect", passwordOnceFile)
			}
			if strings.Contains(string(raw), shown) {
				t.Error("the drawn password is in kubetower.log, in clear, on the same disk " +
					"credentials.json deliberately holds only a bcrypt hash on. It goes to " +
					"stderr, to the dialog and to PASSWORD-ONCE.txt — which deletes itself — " +
					"and never through `log`. The log is a file on every " +
					"start, so routing anything about the password through it writes it down " +
					"for good, in a file nothing ever removes")
			}
		})
	}
}

// TestNoErrorEverCarriesTheDrawnPassword. The log is the obvious channel and it
// is not the only one: an error travels further than a log line, through
// `log.Fatalf` in main, into a dialog on Windows, and into whatever a person
// pastes into a bug report. `create`'s refusal message names three files and a
// directory, and it would be one `%q` away from naming the password too.
func TestNoErrorEverCarriesTheDrawnPassword(t *testing.T) {
	captureLog(t)
	withConsole(t, false)
	withoutDialog(t)
	unwritableStateDir(t)

	// **Asserted as determinism rather than by capturing the password**, which is
	// both stronger and needs no test seam in the code under test. `create` draws
	// before it can fail — `randomPassword` is its first statement — so twenty
	// refusals are twenty different passwords, twenty different bcrypt hashes and
	// twenty different session keys. If the message were the same each time, none
	// of that can be in it; if any of it leaks, the messages differ and this
	// fires, whichever of the three it was.
	seen := map[string]int{}
	for i := 0; i < 20; i++ {
		_, _, err := create(filepath.Join(statefile.Dir(), credentialsFile))
		if err == nil {
			t.Fatal("create succeeded with no console, no dialog and an unwritable state " +
				"directory, so there was no error to inspect")
		}
		seen[err.Error()]++
	}
	if len(seen) != 1 {
		var texts []string
		for text := range seen {
			texts = append(texts, text)
		}
		sort.Strings(texts)
		t.Fatalf("twenty refusals produced %d different messages, so one of them is carrying "+
			"something that was drawn — the password, its hash or the session key. An error "+
			"reaches log.Fatalf, a dialog on Windows and every bug report it is pasted into.\n%s",
			len(seen), strings.Join(texts, "\n---\n"))
	}
	// And that the message is worth having at all: a constant empty string would
	// pass the check above.
	for text := range seen {
		if !strings.Contains(text, passwordOnceFile) {
			t.Errorf("the refusal does not name %s, which is the file an operator has to make "+
				"writable to get past it: %q", passwordOnceFile, text)
		}
	}
}

// passwordFromOnceFile reads the password back out of the note the way an
// operator would: the line on its own, which is the property the file's layout
// exists to give.
func passwordFromOnceFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if len(line) != generatedLength {
			continue
		}
		// A line of exactly the drawn length, made only of the alphabet the
		// drawer uses, and with no space in it. Nothing else in the note can look
		// like that, and reading it this way asserts the layout rather than
		// trusting it.
		if strings.ContainsAny(line, " \t:/.") {
			continue
		}
		return line
	}
	t.Fatalf("%s holds no line that is a password on its own, so it cannot be pasted:\n%s",
		path, raw)
	return ""
}

// TestAPasswordNobodyCouldBeShownIsNotKept is the lock-out guard.
//
// Saving a password no channel delivered produces the one unrecoverable state
// firstrun.go can reach: a credentials.json holding the hash of a password no
// person has ever seen, which every later start reads as properly configured.
// The refusal must leave nothing behind, or the *next* start reads that file and
// the console is shut for good.
//
// **The case it takes to reach that is narrow.** "No console and no dialog" is
// the ordinary shape of a Finder launch, and it is served by
// `PASSWORD-ONCE.txt`; the refusal is what happens when the note cannot be
// written either. That is the state
// directory being unwritable, which is also the one an operator can act on, so
// `TestNoErrorEverCarriesTheDrawnPassword` checks the message names it.
func TestAPasswordNobodyCouldBeShownIsNotKept(t *testing.T) {
	captureLog(t)
	withConsole(t, false)
	withoutDialog(t)
	dir := stateDir(t)
	blockTheNote(t)

	path := filepath.Join(dir, credentialsFile)
	_, _, err := create(path)
	if err == nil {
		t.Fatal("a password was drawn, delivered to nobody, and its hash kept. The next start " +
			"accepts that file as configured and no one can ever log in")
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Error("create refused and left a credentials file behind. The next start reads it as " +
			"a configured password nobody knows, which is worse than the refusal")
	}
}

// TestWithNoTerminalAndNoDialogTheDrawnPasswordIsStillObtainable is asserted
// from the operator's side.
//
// This is the macOS `.app` opened from the Finder and the Linux `.desktop`
// entry launched from a menu: an ordinary console-subsystem process whose
// stderr goes to /dev/null, and no dialog anywhere off Windows. `announce`
// would report the banner as shown regardless, because off Windows
// `consoleAttached` is a hardcoded `true` — so a password would be drawn, its
// hash kept, and nobody could ever log in.
//
// What must be true: the first run **succeeds**, and the password is somewhere
// the operator can get at. Both halves matter — refusing here would refuse the
// container's own first start.
func TestWithNoTerminalAndNoDialogTheDrawnPasswordIsStillObtainable(t *testing.T) {
	dir := stateDir(t)
	captureLog(t)
	withConsole(t, false)
	withoutDialog(t)

	held, _, err := create(filepath.Join(dir, credentialsFile))
	if err != nil {
		t.Fatalf("a first run with no terminal and no dialog was refused: %v. That is the "+
			"container's own first start as well as a Finder launch", err)
	}

	password := passwordFromOnceFile(t, passwordOncePath())
	if err := bcrypt.CompareHashAndPassword([]byte(held.PasswordHash), []byte(password)); err != nil {
		t.Fatalf("the password in %s does not open the console: %v", passwordOnceFile, err)
	}
}

// TestAnUnreadableCredentialsFileIsRefusedRatherThanReplaced. Regenerating over a
// file that will not parse changes the password of a console the operator may
// still have a session on, silently, at a restart they did not connect to it.
func TestAnUnreadableCredentialsFileIsRefusedRatherThanReplaced(t *testing.T) {
	dir := stateDir(t)
	captureLog(t)
	path := filepath.Join(dir, credentialsFile)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := localCredentials(); err == nil {
		t.Fatal("a corrupt credentials file was silently replaced")
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "{not json" {
		t.Error("the corrupt file was overwritten; it must be left for the operator to move aside")
	}
}

// TestTheStoredCredentialsAreStableAcrossStarts. A password that changes at every
// launch cannot be written down, and a session key that changes logs the operator
// out every time — right for a server, wrong for something with a window.
func TestTheStoredCredentialsAreStableAcrossStarts(t *testing.T) {
	stateDir(t)
	captureLog(t)
	withConsole(t, true)
	withoutDialog(t)

	first, firstKey, err := localCredentials()
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	second, secondKey, err := localCredentials()
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	if first.PasswordHash != second.PasswordHash {
		t.Error("the password hash changed between two starts")
	}
	if !bytes.Equal(firstKey, secondKey) {
		t.Error("the session key changed between two starts, so every open session is logged " +
			"out at a restart")
	}
}

// TestTheDrawnPasswordHasNoLookAlikesInIt. This is a string a person reads off a
// screen and types into a browser: 0/O and 1/l/I make a password that is right
// and looks wrong, which gets reported as broken.
func TestTheDrawnPasswordHasNoLookAlikesInIt(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		password, err := randomPassword(generatedLength)
		if err != nil {
			t.Fatalf("randomPassword: %v", err)
		}
		if len(password) != generatedLength {
			t.Fatalf("length %d, want %d", len(password), generatedLength)
		}
		if strings.ContainsAny(password, "0O1lI") {
			t.Fatalf("%q contains a look-alike character", password)
		}
		if seen[password] {
			t.Fatalf("%q was drawn twice in 200 draws, which is not crypto/rand", password)
		}
		seen[password] = true
	}
}

// TestTheStoredFileHoldsNoFieldThatCouldGiveThePasswordBack. The struct has no
// password field and never will; this says so out loud, so that adding one for
// convenience fails here rather than shipping.
func TestTheStoredFileHoldsNoFieldThatCouldGiveThePasswordBack(t *testing.T) {
	dir := stateDir(t)
	captureLog(t)
	withConsole(t, true)
	withoutDialog(t)

	if _, _, err := localCredentials(); err != nil {
		t.Fatalf("localCredentials: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, credentialsFile))
	if err != nil {
		t.Fatal(err)
	}
	var held map[string]any
	if err := json.Unmarshal(raw, &held); err != nil {
		t.Fatalf("the file is not readable JSON: %v", err)
	}
	// The allowlist, and the argument it forces.
	//
	// A new field in the credentials file has to be argued for, and this test
	// fires on one. The list is exactly what is written: an allowlist that
	// permits a field nothing writes permits the next thing that happens to be
	// called that.
	//
	// What must never appear is a field that gives back the *password*. If one
	// does, this test says so.
	for key := range held {
		switch key {
		case "user", "password_hash", "session_secret":
		default:
			t.Errorf("credentials.json carries an unexpected field %q. If it is a password in "+
				"any form, the file is now worth stealing — and if it is not, say why here "+
				"rather than adding it to this list", key)
		}
	}

	// And the field that must never exist, asserted directly rather than left to
	// the default arm above: a `password` alongside `password_hash` is the exact
	// mistake the hash exists to prevent.
	for _, forbidden := range []string{"password", "secret", "plaintext"} {
		if _, present := held[forbidden]; present {
			t.Errorf("credentials.json carries %q", forbidden)
		}
	}
}

// capturingStderr runs fn with os.Stderr on a pipe and returns what it wrote.
//
// The banner goes to stderr directly rather than through log — see announce, and
// TestThePasswordNeverReachesTheLog for why — so this is the only way a test can
// see what a person would have seen.
func capturingStderr(t *testing.T, fn func()) string {
	t.Helper()
	real := os.Stderr
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	var out bytes.Buffer
	done := make(chan struct{})
	go func() { _, _ = out.ReadFrom(read); close(done) }()

	os.Stderr = write
	fn()
	_ = write.Close()
	os.Stderr = real
	<-done
	return out.String()
}

// TestResettingDrawsAnotherPasswordAndRevokesEverySession.
//
// `--reset-password` exists because the alternative is telling an operator to
// find and delete a JSON file — advice that is worse than it looks, since the
// rename people reach for first, `credentials.json.old`, is swept away by the
// next successful save: internal/statefile removes everything in the directory
// beginning with the target's own name and a dot.
//
// Two properties, and the second is the one that would be quietly skipped. A
// reset that leaves the session secret alone has revoked nothing: the cookie is
// stateless, so every session opened under the old password stays valid for its
// full TTL and the reset is cosmetic.
func TestResettingDrawsAnotherPasswordAndRevokesEverySession(t *testing.T) {
	t.Setenv("KT_PASSWORD_HASH", "")
	stateDir(t)
	captureLog(t)
	withConsole(t, true)
	withoutDialog(t)

	var first credentials
	var firstKey []byte
	firstBanner := capturingStderr(t, func() {
		held, key, err := localCredentials()
		if err != nil {
			t.Errorf("first start: %v", err)
			return
		}
		first, firstKey = held, key
	})
	if t.Failed() {
		return
	}

	secondBanner := capturingStderr(t, func() {
		if err := resetPassword(); err != nil {
			t.Errorf("resetPassword: %v", err)
		}
	})
	if t.Failed() {
		return
	}
	second, secondKey, err := localCredentials()
	if err != nil {
		t.Fatalf("after the reset: %v", err)
	}

	before := passwordFromBanner(t, firstBanner)
	after := passwordFromBanner(t, secondBanner)
	if before == "" || after == "" {
		t.Fatalf("a banner carried no password (first %q, second %q)", before, after)
	}
	if before == after {
		t.Error("--reset-password showed the same password again, so it reset nothing")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(second.PasswordHash), []byte(after)); err != nil {
		t.Errorf("the password shown by the reset is not the one now stored: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(second.PasswordHash), []byte(before)); err == nil {
		t.Error("the old password still opens the console after a reset")
	}
	if first.PasswordHash == second.PasswordHash {
		t.Error("the stored hash did not change")
	}
	if bytes.Equal(firstKey, secondKey) {
		t.Error("the session secret survived the reset, so every session opened under the old " +
			"password is still valid for its full TTL. The cookie is stateless: rotating this " +
			"key is the only revocation there is")
	}
}

// withUnattended sets the flag `--unattended` writes, and puts it back.
func withUnattended(t *testing.T, on bool) {
	t.Helper()
	previous := unattended
	unattended = on
	t.Cleanup(func() { unattended = previous })
}

// dialogAttempts replaces the two platform channels with stubs that count the
// calls, and hands back the counter.
//
// It is `withoutDialog` with a number attached, and the number is the whole point
// of the two tests below: "the box was not opened" cannot be asserted from its
// return value, because a box that was opened and dismissed and a box that was
// never attempted both arrive here as `false`.
func dialogAttempts(t *testing.T) *int {
	t.Helper()
	dialog, clipboard := showPassword, copyPassword
	attempts := 0
	showPassword = func(drawnPassword) bool { attempts++; return false }
	copyPassword = func(string) bool { return false }
	t.Cleanup(func() { showPassword, copyPassword = dialog, clipboard })
	return &attempts
}

// TestAnUnattendedFirstRunNeverAttemptsTheBox: an automated first run must
// serve, or exit non-zero, never hang on a box.
//
// Without the flag, on Windows, with a state directory that does not exist and
// no KT_PASSWORD_HASH, one process holds a window titled `KubeTower - first
// run`, `/healthz` on the listen address answers nothing, and stdout and stderr
// are both zero bytes. The box is opened
// before the server goroutine starts, so nothing about that run is recoverable by
// waiting.
//
// Two halves, and the second is what makes the flag usable rather than merely
// quiet: the box is not attempted **and** stderr names the file the password is
// in. Every other line this process writes about a first run can end up in
// `kubetower.log` inside the state directory, which is the one place a caller
// that does not know where the state directory is cannot look.
func TestAnUnattendedFirstRunNeverAttemptsTheBox(t *testing.T) {
	dir := stateDir(t)
	captureLog(t)
	withConsole(t, false)
	withUnattended(t, true)
	attempts := dialogAttempts(t)

	var held credentials
	var err error
	said := capturingStderr(t, func() {
		held, _, err = create(filepath.Join(dir, credentialsFile))
	})
	if err != nil {
		t.Fatalf("an unattended first run was refused: %v", err)
	}
	if *attempts != 0 {
		t.Errorf("the first-run box was attempted %d time(s) under --unattended. On Windows that "+
			"is a modal window on a machine nobody is sitting at, and a console that never "+
			"opens its port", *attempts)
	}

	once := passwordOncePath()
	if !strings.Contains(said, once) {
		t.Errorf("stderr does not name %s. A caller that does not know where the state "+
			"directory is reads the launch report nowhere, because the launch report is "+
			"inside it. stderr said: %q", once, said)
	}

	password := passwordFromOnceFile(t, once)
	if strings.Contains(said, password) {
		t.Error("the line on stderr carries the drawn password itself. The one-time file is " +
			"0600 and deletes itself at the first login; whatever a caller redirected stderr " +
			"to does neither")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(held.PasswordHash), []byte(password)); err != nil {
		t.Fatalf("the password in %s does not open the console: %v", passwordOnceFile, err)
	}
}

// TestWithoutTheFlagTheFirstRunStillOpensTheBox is the other half of the same
// decision, and it is the one that would rot in silence.
//
// The box is correct on a desktop: the Windows binary is linked `-H windowsgui`
// and has no console at all, so removing the box there would produce a first run
// that asks for a password nothing ever displayed. `--unattended` is a flag
// precisely so that a launch without it is untouched — if this test ever passes
// by the box having quietly stopped being opened, the flag has become a
// decoration and a Start-menu launch has become a lock-out.
func TestWithoutTheFlagTheFirstRunStillOpensTheBox(t *testing.T) {
	dir := stateDir(t)
	captureLog(t)
	withConsole(t, true)
	withUnattended(t, false)
	attempts := dialogAttempts(t)

	capturingStderr(t, func() {
		if _, _, err := create(filepath.Join(dir, credentialsFile)); err != nil {
			t.Errorf("a first run with a console was refused: %v", err)
		}
	})
	if *attempts != 1 {
		t.Errorf("showPassword was called %d time(s) without --unattended, want 1. The box is "+
			"the only channel a -H windowsgui launch has before the one-time file", *attempts)
	}
}

// TestAnUnattendedFirstRunWithNowhereToPutThePasswordIsStillRefused.
//
// The flag takes a channel away, so the guard underneath it has to be shown to
// still hold: no console, no box, and a one-time file that cannot be written is
// a lock-out, and it must stay a refusal rather than become a hash nobody has
// the password for. An unattended run *serves, or exits non-zero*, and this is
// the non-zero.
func TestAnUnattendedFirstRunWithNowhereToPutThePasswordIsStillRefused(t *testing.T) {
	dir := stateDir(t)
	captureLog(t)
	withConsole(t, false)
	withUnattended(t, true)
	dialogAttempts(t)
	blockTheNote(t)

	path := filepath.Join(dir, credentialsFile)
	var err error
	said := capturingStderr(t, func() { _, _, err = create(path) })
	if err == nil {
		t.Fatal("an unattended first run with no console and no one-time file kept the hash of " +
			"a password nobody can ever obtain")
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Error("create refused and left a credentials file behind")
	}
	if !strings.Contains(said, "could not be written") {
		t.Errorf("stderr does not say why there is nowhere to read the password from: %q", said)
	}
}

// TestAResetSaysWhereThePasswordWentWhateverElseReachedAnybody.
//
// `go run ./cmd/kubetower --reset-password` must not exit 0 having printed
// nothing, leaving the rewritten hash as the only trace of the password it
// drew. So the worst case is the
// one tested: no console, so no banner and every log line elsewhere, and a box
// that does not reach anybody. A reset over an existing credentials.json, since
// that is the only state a reset is run against.
//
// Three properties: exactly one line, naming the one-time file and so its
// directory; the password in neither that line nor the log; and the box still
// attempted, because the line is an addition and not a replacement.
func TestAResetSaysWhereThePasswordWentWhateverElseReachedAnybody(t *testing.T) {
	t.Setenv("KT_PASSWORD_HASH", "")
	stateDir(t)
	logged := captureLog(t)
	withConsole(t, false)
	withUnattended(t, false)
	attempts := dialogAttempts(t)

	if _, _, err := localCredentials(); err != nil {
		t.Fatalf("first start: %v", err)
	}
	if err := os.Remove(passwordOncePath()); err != nil {
		t.Fatalf("standing in for the first login, which deletes the note: %v", err)
	}
	*attempts = 0

	var err error
	said := capturingStderr(t, func() { err = resetPassword() })
	if err != nil {
		t.Fatalf("resetPassword: %v", err)
	}

	once := passwordOncePath()
	var naming []string
	for _, line := range strings.Split(strings.TrimSpace(said), "\n") {
		if strings.Contains(line, once) {
			naming = append(naming, line)
		}
	}
	if len(naming) != 1 {
		t.Fatalf("stderr has %d line(s) naming %s, want exactly 1. A reset that prints nothing "+
			"has replaced a password and said so to nobody. stderr said: %q", len(naming), once, said)
	}
	line := naming[0]
	if !strings.Contains(line, "--reset-password") || !strings.Contains(line, "deletes it") {
		t.Errorf("the line does not say it is a reset, or that the first login deletes the file: %q", line)
	}
	if !strings.Contains(line, filepath.Dir(once)) {
		t.Errorf("the line does not name the state directory %s: %q", filepath.Dir(once), line)
	}

	password := passwordFromOnceFile(t, once)
	if strings.Contains(said, password) {
		t.Error("with no console attached, the drawn password reached stderr. That is whatever the " +
			"caller redirected it to, which is usually a log somebody keeps")
	}
	if strings.Contains(logged.String(), password) {
		t.Error("the drawn password reached the log, which is a file on every start")
	}
	if *attempts != 1 {
		t.Errorf("the box was attempted %d time(s) on a reset without --unattended, want 1: the "+
			"line is an addition to the channels, not one of them taken away", *attempts)
	}
}

// TestAnUnattendedResetSaysItOnce: the reset and `--unattended` each ask for the
// line, and a caller grepping for it should find one, not two.
func TestAnUnattendedResetSaysItOnce(t *testing.T) {
	t.Setenv("KT_PASSWORD_HASH", "")
	stateDir(t)
	captureLog(t)
	withConsole(t, false)
	withUnattended(t, true)
	attempts := dialogAttempts(t)

	var err error
	said := capturingStderr(t, func() { err = resetPassword() })
	if err != nil {
		t.Fatalf("resetPassword: %v", err)
	}
	if n := strings.Count(said, passwordOncePath()); n != 1 {
		t.Errorf("stderr names the one-time file %d time(s), want 1: %q", n, said)
	}
	if *attempts != 0 {
		t.Errorf("the box was attempted %d time(s) under --unattended", *attempts)
	}
}

// TestADesktopFirstRunDoesNotGainTheLine pins the other side of that line: it
// is the reset that always speaks, not every first run. A first run with a
// console already has the banner, and the line there would only repeat it.
func TestADesktopFirstRunDoesNotGainTheLine(t *testing.T) {
	dir := stateDir(t)
	captureLog(t)
	withConsole(t, false)
	withUnattended(t, false)
	dialogAttempts(t)

	said := capturingStderr(t, func() {
		if _, _, err := create(filepath.Join(dir, credentialsFile)); err != nil {
			t.Errorf("first run: %v", err)
		}
	})
	if strings.Contains(said, "--reset-password:") || strings.Contains(said, "kubetower first run:") {
		t.Errorf("a first run without --unattended wrote the where-is-it line: %q", said)
	}
}

// TestAResetIsRefusedWhileTheEnvironmentHasAHash: with KT_PASSWORD_HASH set the
// console never reads credentials.json, so a reset there would draw a password
// nobody is ever asked for. It must refuse, and touch nothing.
func TestAResetIsRefusedWhileTheEnvironmentHasAHash(t *testing.T) {
	stateDir(t)
	captureLog(t)
	withConsole(t, false)
	withUnattended(t, true)
	dialogAttempts(t)

	if _, _, err := localCredentials(); err != nil {
		t.Fatalf("first start: %v", err)
	}
	path := filepath.Join(statefile.Dir(), credentialsFile)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("KT_PASSWORD_HASH", "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ01234")
	var resetErr error
	said := capturingStderr(t, func() { resetErr = resetPassword() })
	if resetErr == nil || !strings.Contains(resetErr.Error(), "KT_PASSWORD_HASH is set") {
		t.Fatalf("resetPassword with KT_PASSWORD_HASH set = %v, want a refusal naming it", resetErr)
	}
	// With no console, `log` is a file inside the state directory, so the
	// refusal must reach stderr itself or the terminal shows only an exit code.
	if !strings.Contains(said, "kubetower --reset-password: KT_PASSWORD_HASH is set") {
		t.Errorf("the refusal did not reach stderr with no console attached: %q", said)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("credentials.json is gone after a refused reset: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("a refused reset rewrote credentials.json")
	}
}
