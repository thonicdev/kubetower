package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

const (
	someHash   = "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ01234"
	someSecret = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	someDir    = "C:/Users/dev/.kubetower-dev"
)

// The three states a .env arrives in: absent, copied from .env.example (every
// variable present and empty), and already carrying a key and a directory that
// somebody chose and that must survive a new password.
func TestUpdateWritesTheHashAndKeepsWhatWasChosen(t *testing.T) {
	cases := []struct {
		name       string
		before     string
		wantSecret string
		wantDir    string
	}{
		{"absent", "", someSecret, someDir},
		{
			"copied from the example",
			"KT_USER=admin\nKT_PASSWORD_HASH=\nKT_SESSION_SECRET=\n# KT_STATE_DIR=/var/lib/kubetower\n",
			someSecret, someDir,
		},
		{
			"already chosen",
			"KT_PASSWORD_HASH='$2a$10$old'\nKT_SESSION_SECRET=kept-secret-kept-secret-kept-secret\nKT_STATE_DIR=/elsewhere\n",
			"kept-secret-kept-secret-kept-secret", "/elsewhere",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			after, said, err := update([]byte(c.before), someHash, someSecret, someDir)
			if err != nil {
				t.Fatal(err)
			}
			if got := lookup(after, keyHash); got != someHash {
				t.Errorf("KT_PASSWORD_HASH reads back as %q, want %q", got, someHash)
			}
			if got := lookup(after, keySecret); got != c.wantSecret {
				t.Errorf("KT_SESSION_SECRET reads back as %q, want %q", got, c.wantSecret)
			}
			if got := lookup(after, keyState); got != c.wantDir {
				t.Errorf("KT_STATE_DIR reads back as %q, want %q", got, c.wantDir)
			}
			if strings.Count(string(after), keyHash+"=") != 1 {
				t.Errorf("KT_PASSWORD_HASH is assigned more than once:\n%s", after)
			}
			if len(said) != 3 {
				t.Errorf("want one sentence per variable, got %d: %q", len(said), said)
			}
		})
	}
}

// The hash is single-quoted, and that is the property both readers depend on:
// Compose interpolates every `$` of an unquoted or double-quoted value, so
// `$2a$10$...` would arrive as a stump no password matches.
func TestTheHashIsWrittenSingleQuoted(t *testing.T) {
	after, _, err := update(nil, someHash, someSecret, someDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "KT_PASSWORD_HASH='"+someHash+"'") {
		t.Errorf("the hash is not written single-quoted:\n%s", after)
	}
}

// A duplicate further down would win over the line replaced, in both readers.
func TestEveryAssignmentOfTheHashIsReplaced(t *testing.T) {
	before := "KT_PASSWORD_HASH='$2a$10$first'\nOTHER=1\nexport KT_PASSWORD_HASH=$$2a$$10$$second\n"
	after, _, err := update([]byte(before), someHash, someSecret, someDir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), "first") || strings.Contains(string(after), "second") {
		t.Errorf("an older hash survived:\n%s", after)
	}
	if !strings.Contains(string(after), "OTHER=1") {
		t.Errorf("an unrelated line was lost:\n%s", after)
	}
}

func TestCRLFIsKept(t *testing.T) {
	after, _, err := update([]byte("KT_USER=admin\r\nKT_PASSWORD_HASH=\r\n"), someHash, someSecret, someDir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(after), "\n") != strings.Count(string(after), "\r\n") {
		t.Errorf("a CRLF file came back with bare LFs:\n%q", after)
	}
}

// End to end through run: a word on a pipe, a .env written, and the hash read
// back from the file opens with that word and with no other.
func TestRunWritesAHashTheWordOpens(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	env := filepath.Join(t.TempDir(), ".env")

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := write.WriteString("w0rd-under-test\n"); err != nil {
		t.Fatal(err)
	}
	write.Close()
	defer read.Close()

	var out strings.Builder
	if err := run(env, read, &out, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	raw, err := os.ReadFile(env)
	if err != nil {
		t.Fatal(err)
	}
	hash := lookup(raw, keyHash)
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("w0rd-under-test")) != nil {
		t.Errorf("the hash in .env does not open with the word given")
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("w0rd-under-test2")) == nil {
		t.Errorf("the hash in .env opens with another word")
	}
	if strings.Contains(string(raw), "w0rd-under-test") || strings.Contains(out.String(), "w0rd-under-test") {
		t.Errorf("the word itself reached the file or the output")
	}
	if !strings.Contains(out.String(), "created "+env) {
		t.Errorf("the output does not say the file was created: %q", out.String())
	}
}

func TestAnEmptyWordWritesNothing(t *testing.T) {
	env := filepath.Join(t.TempDir(), ".env")
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	write.WriteString("\n")
	write.Close()
	defer read.Close()

	var out strings.Builder
	if err := run(env, read, &out, &out); err == nil {
		t.Fatal("an empty password was accepted")
	}
	if _, err := os.Stat(env); err == nil {
		t.Error("a .env was written for an empty password")
	}
}

// A secret shorter than the console uses is not "already set": `sessionKey`
// draws a key at every start for one, which under `task dev` is a sign-out on
// every saved .go file. It is replaced, and the output says why.
func TestASecretTooShortForTheConsoleIsReplaced(t *testing.T) {
	short := strings.Repeat("s", minSecret-1)
	after, said, err := update([]byte("KT_SESSION_SECRET="+short+"\n"), someHash, someSecret, someDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := lookup(after, keySecret); got != someSecret {
		t.Errorf("a %d-character secret was kept (now %q); the console ignores anything under %d",
			len(short), got, minSecret)
	}
	if !strings.Contains(strings.Join(said, "\n"), "shorter than") {
		t.Errorf("the output does not say the secret was too short: %q", said)
	}

	// And exactly the threshold is kept, which is where `sessionKey` starts using it.
	enough := strings.Repeat("s", minSecret)
	after, _, err = update([]byte("KT_SESSION_SECRET="+enough+"\n"), someHash, someSecret, someDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := lookup(after, keySecret); got != enough {
		t.Errorf("a %d-character secret was replaced; the console uses it", minSecret)
	}
}

// A home directory with a quote in it only matters when its path is about to
// be written.
func TestAQuoteInTheHomeDirectoryIsNoObstacleWhenTheStateDirIsSet(t *testing.T) {
	quoted := "C:/Users/o'brien/.kubetower-dev"
	if _, _, err := update([]byte("KT_STATE_DIR=/elsewhere\n"), someHash, someSecret, quoted); err != nil {
		t.Errorf("refused although KT_STATE_DIR is already set and the home is not written: %v", err)
	}
	if _, _, err := update(nil, someHash, someSecret, quoted); err == nil {
		t.Error("a home directory with a quote was written into a single-quoted value")
	}
}

// A .env copied from .env.example carries the container's KT_FORWARD_PORTS.
// `task dev` does not take it; the developer is still told it is there.
func TestItWarnsAboutAContainerOnlyVariable(t *testing.T) {
	if got := warnings([]byte("KT_FORWARD_PORTS=30000-30031\n")); len(got) != 1 ||
		!strings.Contains(got[0], "KT_FORWARD_PORTS") || !strings.Contains(got[0], "0.0.0.0") {
		t.Errorf("warnings = %q, want one naming KT_FORWARD_PORTS and 0.0.0.0", got)
	}
	if got := warnings([]byte("# KT_FORWARD_PORTS=30000-30031\nKT_FORWARD_PORTS=\n")); len(got) != 0 {
		t.Errorf("warned about a commented or empty KT_FORWARD_PORTS: %q", got)
	}
}

// TestTheShellsOwnHashIsSaidToWin: devenv lets the shell win, so from a shell
// that sets KT_PASSWORD_HASH the sign-in advice would be wrong.
func TestTheShellsOwnHashIsSaidToWin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("KT_PASSWORD_HASH", "$2a$10$someoneelsesabcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQR")
	env := filepath.Join(t.TempDir(), ".env")
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	write.WriteString("w0rd-under-test\n")
	write.Close()
	defer read.Close()

	var out strings.Builder
	if err := run(env, read, &out, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	said := out.String()
	if !strings.Contains(said, "will NOT sign in with the word you chose") {
		t.Errorf("the output does not say the shell's KT_PASSWORD_HASH wins: %q", said)
	}
	if strings.Contains(said, "sign in to `task dev` as") {
		t.Errorf("the output still advises signing in with the word: %q", said)
	}
	if strings.Contains(said, "someoneelses") {
		t.Errorf("the shell's hash value reached the output: %q", said)
	}
}

func TestShellOverridesNamesEachKeyTheShellSets(t *testing.T) {
	env := map[string]string{"KT_STATE_DIR": "/x", "KT_SESSION_SECRET": ""}
	got := shellOverrides(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	joined := strings.Join(got, "\n")
	if len(got) != 2 || !strings.Contains(joined, "KT_STATE_DIR to a value") ||
		!strings.Contains(joined, "KT_SESSION_SECRET to the empty string") || strings.Contains(joined, "/x") {
		t.Errorf("shellOverrides = %q", got)
	}
}

func TestAStateDirTaskDevWouldRefuseIsWarnedAbout(t *testing.T) {
	got := strings.Join(warnings([]byte("KT_STATE_DIR=${USERPROFILE}/.kubetower-dev\n")), "\n")
	if !strings.Contains(got, "KT_STATE_DIR") || !strings.Contains(got, "will refuse") {
		t.Errorf("no warning for a KT_STATE_DIR task dev refuses: %q", got)
	}
}
