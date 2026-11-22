// Command devpassword chooses the development password once and keeps its hash
// in `.env`, which `task dev` loads.
//
// The desktop console draws a random password and keeps only its bcrypt hash,
// the wrong shape for a development loop: a forgotten password means
// `--reset-password`. KT_PASSWORD_HASH in the environment wins over
// credentials.json, so this writes it into the repository's .env, with a word
// the developer chose.
//
//   - KT_PASSWORD_HASH, always. The word itself is written nowhere.
//   - KT_SESSION_SECRET, only when .env has no usable one: without it the
//     console draws a key at every start, and devwatch restarts it on every
//     saved .go file, signing the developer out each time.
//   - KT_STATE_DIR, only when .env has none: ~/.kubetower-dev, so development
//     never writes into ~/.kubetower.
//
// Values are single-quoted: a bcrypt hash is `$2a$10$...`, and both Task's
// dotenv loader and Docker Compose take a single-quoted value literally.
//
// The word comes from standard input, never this process's arguments: `task
// dev:password -- <word>` pipes it in, and with no word a terminal is asked
// twice without echo.
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

	"github.com/thonicdev/kubetower/internal/devenv"
)

func main() {
	envPath := flag.String("env", ".env", "the .env file to write")
	flag.Parse()

	if err := run(*envPath, os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "dev:password: %v\n", err)
		os.Exit(1)
	}
}

func run(envPath string, in *os.File, out, prompt io.Writer) error {
	word, err := readWord(in, prompt)
	if err != nil {
		return err
	}

	digest, err := bcrypt.GenerateFromPassword([]byte(word), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hashing the password: %w", err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("finding the home directory for KT_STATE_DIR: %w", err)
	}

	abs, err := filepath.Abs(envPath)
	if err != nil {
		return err
	}
	before, err := os.ReadFile(abs)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading %s: %w", abs, err)
	}
	created := errors.Is(err, os.ErrNotExist)

	after, said, err := update(before, string(digest), hex.EncodeToString(secret),
		filepath.ToSlash(filepath.Join(home, ".kubetower-dev")))
	if err != nil {
		return err
	}
	if err := writeAtomically(abs, after); err != nil {
		return err
	}

	// Read back: the report is about the file `task dev` will load.
	check, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Errorf("reading %s back: %w", abs, err)
	}
	stored := lookup(check, keyHash)
	if bcrypt.CompareHashAndPassword([]byte(stored), []byte(word)) != nil {
		return fmt.Errorf("%s was written, and the KT_PASSWORD_HASH read back from it does not "+
			"match the word given", abs)
	}

	if created {
		fmt.Fprintf(out, "dev:password: created %s (git-ignored)\n", abs)
	}
	for _, line := range said {
		fmt.Fprintf(out, "dev:password: %s\n", line)
	}
	for _, line := range warnings(check) {
		fmt.Fprintf(out, "dev:password: %s\n", line)
	}
	for _, line := range shellOverrides(os.LookupEnv) {
		fmt.Fprintf(out, "dev:password: %s\n", line)
	}
	// The user `task dev` will take: the shell's KT_USER or `admin`. KT_USER in
	// .env is for the container, and devenv does not take it.
	user := os.Getenv("KT_USER")
	if user == "" {
		user = "admin"
	}
	if _, shellHash := os.LookupEnv(keyHash); shellHash {
		// devenv lets the shell win, so `task dev` from this shell would use
		// the shell's hash (or credentials.json when it is empty).
		fmt.Fprintf(out, "dev:password: `task dev` started from this shell will NOT sign in with "+
			"the word you chose: this shell's own %s wins over .env. Run `unset %s`, then start "+
			"`task dev` and sign in as %q.\n", keyHash, keyHash, user)
		return nil
	}
	fmt.Fprintf(out, "dev:password: sign in to `task dev` as %q with the word you chose. A "+
		"running `task dev` reads .env only when it starts, so restart it.\n", user)
	return nil
}

// readWord takes the password from standard input: asked twice without echo on
// a terminal, one line from a pipe.
func readWord(in *os.File, prompt io.Writer) (string, error) {
	fd := int(in.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(prompt, "Development password: ")
		first, err := term.ReadPassword(fd)
		fmt.Fprintln(prompt)
		if err != nil {
			return "", err
		}
		fmt.Fprint(prompt, "Again: ")
		second, err := term.ReadPassword(fd)
		fmt.Fprintln(prompt)
		if err != nil {
			return "", err
		}
		if string(first) != string(second) {
			return "", errors.New("the two entries differ; nothing was written")
		}
		return checkWord(string(first))
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return checkWord(strings.TrimRight(line, "\r\n"))
}

func checkWord(word string) (string, error) {
	if word == "" {
		return "", errors.New("no password given; nothing was written. " +
			"Use `task dev:password -- <word>`, or run it with no word to be asked")
	}
	return word, nil
}

const (
	keyHash   = "KT_PASSWORD_HASH"
	keySecret = "KT_SESSION_SECRET"
	keyState  = "KT_STATE_DIR"

	// minSecret is the shortest KT_SESSION_SECRET the console uses; for
	// anything shorter it draws a key at every start, which under `task dev`
	// signs out on every saved .go file. A shorter one is replaced here.
	minSecret = 32
)

// update returns the new content of a .env and one sentence per variable saying
// what happened to it.
//
// The hash replaces every assignment of KT_PASSWORD_HASH, so no duplicate can
// win. The secret is kept when its effective (last) value is one the console
// will use, and the state directory when it is not empty; otherwise each is
// written in place of the old assignment or appended.
func update(before []byte, hash, secret, stateDir string) ([]byte, []string, error) {
	newline := "\n"
	if strings.Contains(string(before), "\r\n") {
		newline = "\r\n"
	}
	lines := devenv.Lines(before)

	var said []string
	var appended []string

	set := func(key, value string) {
		replaced := false
		for i, line := range lines {
			if k, _, ok := devenv.Assignment(line); ok && k == key {
				lines[i] = key + "='" + value + "'"
				replaced = true
			}
		}
		if !replaced {
			appended = append(appended, key+"='"+value+"'")
		}
	}

	set(keyHash, hash)
	said = append(said, fmt.Sprintf("%s written: the bcrypt hash of the word given. The word "+
		"itself is stored nowhere", keyHash))

	switch v, _ := devenv.Effective(lines, keySecret); {
	case len(v) >= minSecret:
		said = append(said, keySecret+" already set, kept")
	case v != "":
		set(keySecret, secret)
		said = append(said, fmt.Sprintf("%s was %d characters, shorter than the %d the console "+
			"uses, so it drew a key at every start and every saved .go file signed you out. "+
			"Replaced with a drawn one", keySecret, len(v), minSecret))
	default:
		set(keySecret, secret)
		said = append(said, keySecret+" drawn, so a session survives the restarts `task dev` makes "+
			"on every Go change")
	}

	if v, _ := devenv.Effective(lines, keyState); v != "" {
		said = append(said, fmt.Sprintf("%s already set to %s, kept", keyState, v))
	} else {
		// Refused only when about to be written: a quote in the home directory
		// does not matter when KT_STATE_DIR is already set.
		if strings.Contains(stateDir, "'") {
			return nil, nil, fmt.Errorf("the home directory %q contains a single quote, which a "+
				"single-quoted .env value cannot hold; set %s in .env by hand", stateDir, keyState)
		}
		set(keyState, stateDir)
		said = append(said, fmt.Sprintf("%s set to %s, so the development loop never writes "+
			"into your own ~/.kubetower", keyState, stateDir))
	}

	if len(appended) > 0 {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, "# Written by `task dev:password` (cmd/devpassword). `.env.example` says what each is.")
		lines = append(lines, appended...)
	}

	return []byte(strings.Join(lines, newline) + newline), said, nil
}

// warnings is what the rest of the .env would do to a desktop process if it
// reached one. `task dev` takes only devenv.Keys; the warning is for a shell
// that sources .env, or a tool that loads all of it.
func warnings(content []byte) []string {
	var out []string
	// A kept KT_STATE_DIR that `task dev` will refuse, since .env is read
	// literally.
	if v, _ := devenv.Effective(devenv.Lines(content), keyState); v != "" {
		if problem := devenv.StateDirProblem(v); problem != "" {
			out = append(out, fmt.Sprintf("WARNING: %s in .env is %q, which `task dev` will refuse: %s. "+
				"Write an absolute path, or remove the line and run this again to have one written",
				keyState, v, problem))
		}
	}
	for _, key := range devenv.Present(content) {
		out = append(out, fmt.Sprintf("WARNING: this .env also sets %s, which is for `task container`. "+
			"`task dev` does not take it, but a desktop process that did would bind every "+
			"port-forward on 0.0.0.0, reachable from your network. Do not source this file into a "+
			"shell that runs the binary", key))
	}
	return out
}

func lookup(content []byte, key string) string {
	v, _ := devenv.Effective(devenv.Lines(content), key)
	return v
}

// writeAtomically writes beside the target and renames over it, mode 0600: a
// half-written .env is a password hash cut short, which no word matches.
func writeAtomically(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".env.devpassword-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// shellOverrides names each of devenv.Keys this process's environment already
// sets, which `task dev` started from the same shell would use instead of .env:
// devenv lets the shell win, even with an empty value. Names only.
func shellOverrides(lookup func(string) (string, bool)) []string {
	var out []string
	for _, key := range devenv.Keys {
		value, set := lookup(key)
		if !set {
			continue
		}
		state := "to a value"
		if value == "" {
			state = "to the empty string"
		}
		out = append(out, fmt.Sprintf("WARNING: this shell sets %s %s, and the shell wins over "+
			".env: `task dev` started from it uses the shell's, not the one just written", key, state))
	}
	return out
}
