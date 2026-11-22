package launch

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/thonicdev/kubetower/internal/statefile"
)

// A drawn password, written once to a file that deletes itself at the first
// successful login.
//
// Off Windows consoleAttached is always true, so a banner on stderr cannot be
// counted as shown: a Finder or .desktop launch's stderr goes nowhere. A password
// drawn, shown to nobody and kept as a hash would lock the operator out, with
// `--reset-password` from a terminal the only way back. announce refuses to keep
// a password it could not deliver, and this file makes delivery possible
// without a console.
//
// firstrun.go argues against a file that can give the password back for ever;
// this is a one-time note whose life is the gap between drawing a password and
// the first login.
//
// Not used instead:
//
//   - refusing when stderr is not a terminal, which refuses the container's
//     own first run, where `docker compose logs` reads a pipe;
//   - detecting the container, a heuristic whose failure is a wrong refusal or
//     a lost password;
//   - refusing whenever no dialog opened, which fails every headless start.
//
// The cost is a plaintext password on disk until one login, which is no new
// exposure: credentials.json in the same directory, same mode, holds the session
// signing secret in clear, which mints a valid cookie with no password at all.
const (
	// passwordOnceFile is the note, beside credentials.json in the state
	// directory. Upper case to stand out in a listing; it says nothing of a
	// first run because `--reset-password` writes it too.
	//
	// It must never begin with `credentials.json.`: statefile's sweep removes
	// files named after the target plus a dot, and would delete the note in the
	// very save that keeps its hash. A test holds that.
	passwordOnceFile = "PASSWORD-ONCE.txt"
)

// passwordOncePath is where the note goes. A function because statefile.Dir
// reads the environment, which tests move.
func passwordOncePath() string {
	return filepath.Join(statefile.Dir(), passwordOnceFile)
}

// writePasswordOnce writes the note and reports where it put it.
//
// It goes through statefile's durable write. 0600, because this file holds a
// usable credential in clear (on Windows, where the mode means little,
// narrowToOwner sets the ACL); the sync, because a note lost to a crash is a
// password kept but never delivered; the rename, because a half-written password
// looks complete and is refused.
func writePasswordOnce(user, password, credentialsPath string) (string, error) {
	path := passwordOncePath()
	if err := statefile.Write(path, []byte(passwordOnceBody(user, password, credentialsPath))); err != nil {
		return "", fmt.Errorf("%s could not be written (%s)", path, withoutNotSaved(err))
	}
	narrowToOwner(path)
	// The path and never the value: the log file is how a Finder launch with no
	// banner and no box finds where to look.
	log.Printf("[kubetower] the drawn password is in %s until somebody logs in with it, when "+
		"KubeTower deletes it. That file is the only copy — only a bcrypt hash is kept", path)
	return path, nil
}

// withoutNotSaved keeps a statefile failure's cause and drops its sentence.
//
// statefile.ErrNotSaved says "in effect now, but it will not survive a
// restart", true for the stores it was written for and backwards here: nothing
// is in effect, and the failure is that no copy of a password exists. That
// sentence would reassure in front of the one message that must not.
func withoutNotSaved(err error) string {
	if err == nil {
		return ""
	}
	if !errors.Is(err, statefile.ErrNotSaved) {
		return err.Error()
	}
	return strings.TrimPrefix(err.Error(), statefile.ErrNotSaved.Error()+": ")
}

// passwordOnceBody is what the note says.
//
// Each line answers a question the operator would otherwise have to look up:
// where, as whom, what, that nothing can give it back, and what to run if lost.
//
// The password is alone on its line by design: a long random string is only
// obtainable if selecting it (a double-click, `head`, select-to-end-of-line)
// picks up nothing else.
func passwordOnceBody(user, password, credentialsPath string) string {
	listen := envOr("KT_LISTEN", defaultListen)

	var b strings.Builder
	b.WriteString("KubeTower drew this password because there was no other way to hand it to you.\n")
	b.WriteString("Log in with it once and KubeTower deletes this file.\n\n")
	fmt.Fprintf(&b, "    address:   %s\n", consoleURL(listen))
	if reachable(listen) {
		// A wildcard bind is the container's shape, where the address above
		// is the container's and the host's port is unknown here. Said only
		// then: on a laptop it would be noise.
		b.WriteString("               (this process is bound to every interface; in a container,\n")
		b.WriteString("                the host reaches it on the port your container runtime\n")
		b.WriteString("                publishes - with compose.dev.yaml, KT_PORT in .env)\n")
	}
	fmt.Fprintf(&b, "    user:      %s\n\n", user)
	b.WriteString("The password is the next line, alone on it, so it can be selected and\n")
	b.WriteString("pasted without picking anything else up:\n\n")
	b.WriteString(password)
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "Only its bcrypt hash is kept, in %s, so\n", credentialsPath)
	b.WriteString("once this file is gone nothing on this disk can give the password back.\n")
	b.WriteString("If you lose it before logging in, draw another with:\n\n")
	b.WriteString("    kubetower --reset-password\n")
	return b.String()
}

// drawnPasswordNote reports where the drawn password can still be read, or "".
//
// The file makes the password obtainable; this makes it findable: a Finder
// launch opens the browser on the login page, and internal/auth calls this to
// decide whether that page says a file was written. Only the path leaves this
// file, never the value (see Config.PasswordNote).
//
// A stat, not a flag captured when the note was written: removePasswordOnce
// ends the note from another goroutine at any moment, and the file survives a
// restart, so a flag would name a deleted file or miss a kept one.
//
// Not a dialog: off Windows that means osascript or zenity, absent on minimal
// images and failing over SSH or in a container while reporting success. A
// sentence on the login page needs no subprocess and disappears with the file.
func drawnPasswordNote() string {
	path := passwordOncePath()
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		// Every failure is "nothing to point at": a page naming a missing file
		// sends somebody hunting.
		return ""
	}
	return path
}

// removePasswordOnce deletes the note, and is what a successful login runs.
//
// Best effort, and it must never fail a login: a file that cannot be unlinked
// (a read-only volume, an indexer's lock) is far better than an operator shut
// out. Errors are swallowed.
//
// Safe on every login: a missing file is the ordinary case. The line is written
// only when a file really was removed.
func removePasswordOnce() {
	path := passwordOncePath()
	if err := os.Remove(path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			// Named, not fatal: a plaintext credential still on disk is
			// something the operator is entitled to know.
			log.Printf("[kubetower] WARNING: %s still holds the drawn password in clear and could not be "+
				"deleted (%v). Delete it by hand", path, err)
		}
		return
	}
	log.Printf("[kubetower] %s deleted: somebody has logged in, so the one-time copy of the "+
		"drawn password is gone", path)
}
