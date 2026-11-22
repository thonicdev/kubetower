package launch

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/bcrypt"

	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/statefile"
)

// The first run of a binary somebody downloaded.
//
// A missing password is fatal, not a warning: this process holds credentials
// for production clusters. Nothing here changes that; what changes is where a
// password can come from.
//
// In a container the source is KT_PASSWORD_HASH in .env, written by hand from
// the procedure .env.example carries, and the environment is consulted first and
// wins. For a binary somebody double-clicks there is no .env, and the options
// are to refuse (and watch somebody look for the flag that disables the check),
// to ship a default password, or to draw a strong one, show it once and keep
// only its hash. This does the third: 20 characters from a 57-character alphabet
// drawn from crypto/rand, delivered once (terminal, a dialog on Windows, and a
// one-time file deleted at the first login, passwordonce.go). credentials.json
// holds a bcrypt hash, which nothing reverses.
//
// Everything fails closed: a credentials file that cannot be created or read,
// or is corrupt, stops the process. The one deliberate exception is the loopback
// desktop (loopbackDesktop, with no KT_PASSWORD_HASH), and what stands in front
// of it is not nothing: a Host that is not a loopback name is refused before any
// session is asked about (hostguard.go), and a session is opened only by a
// one-time code the process mints for a window it opens itself
// (internal/auth/window.go), or hands to a second launch that proves the
// launcher secret from this 0700 directory (launcher.go). The session is still
// the HttpOnly cookie.
//
// Anywhere else (a container, KT_LISTEN beyond loopback) a password is drawn and
// required, and a credentials file written without one on the loopback desktop
// gets one the first time it starts there (localCredentialsFor).
const (
	// credentialsFile sits in the state directory beside everything else the
	// console remembers.
	credentialsFile = "credentials.json"

	// generatedLength is 20 characters of a 57-character alphabet: about 116
	// bits, against which internal/auth's 750 ms failure delay makes guessing
	// hopeless.
	generatedLength = 20
)

// credentials is what the file holds. The password is not in it.
type credentials struct {
	// User is the login name, editable in this file instead of setting an
	// environment variable on every launch.
	User string `json:"user"`
	// PasswordHash is bcrypt. There is no field for the password: a file that
	// could give it back is worth stealing.
	PasswordHash string `json:"password_hash"`
	// SessionSecret signs the cookie. Persisted, because a key drawn per start
	// would sign the operator out at every restart.
	SessionSecret string `json:"session_secret"`

	// A credentials.json written by an older build may carry a third member
	// with no field here (its name is not written: a guard test greps the tree
	// for it). encoding/json ignores it and the next rewrite drops it; no route
	// accepts it.
}

// localCredentials loads or creates the credentials beside the other state, with
// a password: what every profile but the loopback desktop needs.
//
// It returns the hash and the session secret. KT_PASSWORD_HASH and
// KT_SESSION_SECRET win over both, so a stale file in a volume cannot change a
// container's behavior.
func localCredentials() (held credentials, secret []byte, err error) {
	return localCredentialsFor(true)
}

// localCredentialsFor is localCredentials, told whether this start asks for a
// password. Without one (the loopback desktop) nothing is drawn: no password,
// banner, dialog or PASSWORD-ONCE.txt; the file still holds the user name and
// the session secret, so a window's session survives a restart. With one, a
// file written without a password (by a loopback start, before the console moved
// into a container or onto the network) gets one drawn now and delivered as on a
// first run, keeping its session secret.
func localCredentialsFor(needPassword bool) (held credentials, secret []byte, err error) {
	dir := statefile.Dir()
	path := filepath.Join(dir, credentialsFile)

	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		narrowToOwner(path)
		if err := json.Unmarshal(raw, &held); err != nil {
			// Refused, not replaced: regenerating would silently change the
			// password of a console the operator may still be signed in to.
			return credentials{}, nil, fmt.Errorf("%s is not readable JSON (%w); move it aside "+
				"to start again with a new password", path, err)
		}
		passwordless := strings.TrimSpace(held.PasswordHash) == ""
		if passwordless && needPassword {
			// Written by a loopback desktop, now started where a password is
			// asked for: draw one, keeping the user and the session secret.
			return drawInto(path, false, held)
		}
		key, err := hex.DecodeString(held.SessionSecret)
		if err != nil || len(key) < 32 {
			// Drawn afresh rather than refused: the cost is signing in again,
			// where refusing to start costs the whole console.
			log.Printf("[kubetower] the stored session secret is unusable; drawing a new one, " +
				"so any open session will need to log in again")
			if key, err = randomKey(); err != nil {
				return credentials{}, nil, err
			}
			held.SessionSecret = hex.EncodeToString(key)
			if err := save(path, held); err != nil {
				return credentials{}, nil, err
			}
		}
		if passwordless {
			log.Printf("[kubetower] no password on this loopback desktop: the window opens signed in, " +
				"and a second launch or `kubetower open` brings another. A password is drawn the first " +
				"time this console starts in a container or with KT_LISTEN beyond loopback")
			return held, key, nil
		}
		if !needPassword {
			// The file holds a password from an earlier version, which this
			// console does not offer. It is kept (the same directory may serve
			// a container or a KT_LISTEN beyond loopback), as is any plain-text
			// first-run file; this says where both are.
			log.Printf("[kubetower] no password on this loopback desktop: the window opens signed in, "+
				"and a second launch or `kubetower open` brings another. The password an earlier version "+
				"drew stays in %s, hashed, for a container or a KT_LISTEN beyond loopback; it is not "+
				"offered here", path)
			once := filepath.Join(filepath.Dir(path), passwordOnceFile)
			if _, err := os.Stat(once); err == nil {
				log.Printf("[kubetower] %s still holds that password in plain text, from the first run. "+
					"Nothing here reads it and it is left as it was; delete it if this state directory "+
					"never serves a container or a KT_LISTEN beyond loopback", once)
			}
			return held, key, nil
		}
		// Said on every start: a stored password reused and one redrawn
		// because the file cannot be kept look identical from outside.
		// Absent this line, the start drew a new one.
		log.Printf("[kubetower] password read from %s - this is the one drawn on the first run, "+
			"not a new one. `--reset-password` is how to draw another", path)
		return held, key, nil

	case errors.Is(err, os.ErrNotExist):
		if !needPassword {
			return createWithoutPassword(path)
		}
		return create(path)

	default:
		return credentials{}, nil, fmt.Errorf("reading %s: %w", path, err)
	}
}

// createWithoutPassword is the loopback desktop's first run: a user name and a
// session secret, no password, nothing to announce.
func createWithoutPassword(path string) (credentials, []byte, error) {
	key, err := randomKey()
	if err != nil {
		return credentials{}, nil, err
	}
	held := credentials{User: envOr("KT_USER", "admin"), SessionSecret: hex.EncodeToString(key)}
	if err := save(path, held); err != nil {
		return credentials{}, nil, err
	}
	log.Printf("[kubetower] first run: no password was drawn, because this desktop listens on loopback "+
		"and its window opens signed in. The session secret is kept in %s", path)
	return held, key, nil
}

// create draws a password, prints it once, and keeps only its hash.
//
// It is the first run's door; `--reset-password` uses draw with onDemand set
// (see resetPassword).
func create(path string) (credentials, []byte, error) {
	return draw(path, false)
}

// draw is `create`, told whether somebody asked for this password.
//
// onDemand is true for `--reset-password` alone. It adds one line on stderr and
// changes nothing else.
func draw(path string, onDemand bool) (credentials, []byte, error) {
	return drawInto(path, onDemand, credentials{})
}

// drawInto is draw, keeping what base holds: its user name, and its session
// secret when usable. That is how a passwordless loopback file gets a password
// without signing out every session the secret signed.
func drawInto(path string, onDemand bool, base credentials) (credentials, []byte, error) {
	password, err := randomPassword(generatedLength)
	if err != nil {
		return credentials{}, nil, err
	}
	// The auth package's own cost, so there is one answer to how expensive a
	// login is.
	digest, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return credentials{}, nil, err
	}
	key, err := hex.DecodeString(base.SessionSecret)
	if err != nil || len(key) < 32 {
		if key, err = randomKey(); err != nil {
			return credentials{}, nil, err
		}
	}
	user := base.User
	if user == "" {
		user = envOr("KT_USER", "admin")
	}

	held := credentials{
		User:          user,
		PasswordHash:  string(digest),
		SessionSecret: hex.EncodeToString(key),
	}

	// Delivered before it is saved, which is the guard: saving first can leave
	// a credentials.json holding the hash of a password nobody saw, which every
	// later start reads as configured. Announcing first makes the failure a
	// first run that did not happen.
	obtainable, onceErr := announce(held.User, password, path, onDemand)
	if !obtainable {
		// Every channel failed. A password obtainable by nobody is a
		// lock-out, refused here. The cause travels with the refusal, since
		// the file is the one channel a Finder or .desktop launch can fix;
		// %v rather than wrapping, so withoutNotSaved's trimming applies.
		return credentials{}, nil, fmt.Errorf("a password was drawn for this first run and there is "+
			"nowhere to put it: no console is attached, no dialog could be opened, and the "+
			"one-time file could not be written (%v). Keeping the hash of a password nobody has "+
			"seen is the one thing this refuses to do, so nothing was saved. Fix that path, or "+
			"start KubeTower once from a terminal, and it will draw another", onceErr)
	}

	if err := save(path, held); err != nil {
		// The note names a password for an account that now does not
		// exist, so it goes with the failure.
		removePasswordOnce()
		return credentials{}, nil, err
	}

	// The fact, never the secret: this line may be going to a file. It names
	// the occasion, so the log does not contradict the command typed.
	occasion := "first run"
	if onDemand {
		occasion = "--reset-password"
	}
	log.Printf("[kubetower] %s: a password was drawn for %q and delivered once. Only its "+
		"bcrypt hash is kept, in %s", occasion, held.User, path)

	return held, key, nil
}

// showPassword and copyPassword are the platform hooks, held as variables so a
// test can express "nothing could show this": on Windows the real
// showFirstRunPassword opens a modal box, which would block `go test`, and the
// refusal that prevents a lock-out could not be tested.
var (
	showPassword = showFirstRunPassword
	copyPassword = toClipboard
)

// unattended says that nothing on this machine is looking at a screen; set by
// `--unattended` alone.
//
// It lets an automated first run in a fresh state directory finish. On Windows
// the first-run box opens before the server starts, so such a run would answer
// nothing on its port, never exit and print nothing, its log being inside a
// state directory the caller does not yet know.
//
// A flag of its own, because the existing signals do not serve:
//
//   - `--no-window` means do not open the application window, and still draws
//     a password under `--reset-password`; overloading it would fire where a
//     script did not ask.
//   - KT_PASSWORD_HASH being set says nothing: the run that hangs is the one
//     with no password configured.
//   - "stdout is not a terminal" is also true of a Start-menu launch, and would
//     take the box from the person it exists for.
//
// It softens nothing: the box is skipped, announce names the one-time file on
// stderr instead without counting that line as a delivery, so a run with no file
// and no console still reaches create's refusal and exits non-zero.
var unattended = false

// drawnPassword is everything a dialog needs to say about a password that has
// just been drawn. A struct, because positional strings and booleans could be
// swapped unseen, on the one run where a mistake cannot be retried.
type drawnPassword struct {
	// User is the login name.
	User string
	// Password is the drawn string, existing nowhere else in this process
	// outside create's stack.
	Password string
	// Kept is where the bcrypt hash went: credentials.json.
	Kept string
	// Copy is the one-time file holding the password in clear, or "" when it
	// could not be written. The box must not name a file that is not there.
	Copy string
	// Copied says whether the clipboard actually took it: an operator told it
	// is there who pastes nothing has lost the password.
	Copied bool
}

// announce puts the drawn password everywhere the operator can get at it and
// reports whether any channel worked, with why the one-time file did not when
// it did not, so create's refusal can name the cause.
//
//   - A one-time file, PASSWORD-ONCE.txt, deleted at the first login
//     (passwordonce.go). Off Windows this is what makes the guarantee true.
//   - The terminal that started this process, framed so it is not mistaken for
//     a log line.
//   - A modal box on Windows only (firstrundialog_windows.go).
//
// It reports obtainable, not shown: whether a person saw it cannot be known,
// since consoleAttached is assumed true off Windows.
//
// log is never a channel: the log file is kept and pasted into bug reports. The
// one-time note holds nothing else, is 0600 and removes itself. Hence os.Stderr
// directly, not a logger that may point elsewhere.
func announce(user, password, path string, onDemand bool) (bool, error) {
	// The clipboard first, so every channel below can say truthfully whether
	// it worked: a message box text is not selectable, nor a Windows console
	// without QuickEdit.
	copied := copyPassword(password)

	// The file first too, so the others can name it: a scrolled-away banner or
	// a dismissed box still leave a path, known only once the write succeeded.
	once, onceErr := writePasswordOnce(user, password, path)
	obtainable := onceErr == nil
	if onceErr != nil {
		// Through log, which reaches a file on every start: this is the
		// sentence somebody debugging a lock-out needs, with no password. If
		// nothing else works, create refuses and repeats it.
		log.Printf("[kubetower] WARNING: the drawn password could not be written to a one-time file: %v", onceErr)
	}

	if consoleAttached {
		fmt.Fprintf(os.Stderr, "\n"+
			"  ┌─────────────────────────────────────────────────────────────┐\n"+
			"  │  First run. KubeTower has drawn a password for you.          │\n"+
			"  │                                                             │\n"+
			"  │     user:      %-44s │\n"+
			"  │     password:  %-44s │\n"+
			"  │                                                             │\n"+
			"  │  Only its hash is kept, in                                  │\n"+
			"  │  %-57s │\n"+
			"  │  Later starts reuse it. For another: --reset-password       │\n"+
			"  └─────────────────────────────────────────────────────────────┘\n",
			user, password, path)
		if copied {
			fmt.Fprint(os.Stderr, "  It is on your clipboard: paste it, then copy something else.\n")
		}
		// "The only sighting" only when it is: a false claim beside a file
		// that also has it makes the rest of the text doubtful.
		if once != "" {
			fmt.Fprintf(os.Stderr, "  It is also in %s, which\n"+
				"  KubeTower deletes the first time somebody logs in.\n\n", once)
		} else {
			fmt.Fprintf(os.Stderr, "  This is the only sighting: %v\n\n", onceErr)
		}
		obtainable = true
	}

	// All channels at once: they fail differently (a dismissed box leaves the
	// banner, a scrolled banner leaves the box, neither leaves the file).
	//
	// Unless the run said nobody is there: the box blocks the start-up (see
	// unattended).
	//
	// On every `--reset-password` the line is written too, before the box
	// (which blocks): without a console every log line goes to the file inside
	// the state directory, so a reset could otherwise exit 0 having printed
	// nothing, and running it again would lose the second password.
	//
	// An ordinary first run does not get the line: its log names the file, and
	// it would repeat the banner to the person reading it.
	if unattended || onDemand {
		sayWherePasswordIs(onDemand, user, once, onceErr)
	}
	if !unattended && showPassword(drawnPassword{
		User:     user,
		Password: password,
		Kept:     path,
		Copy:     once,
		Copied:   copied,
	}) {
		obtainable = true
	}

	return obtainable, onceErr
}

// sayWherePasswordIs is the line an automated caller gets in place of the box.
//
// It breaks a circle: everything else about a first run goes through log, which
// without a console reaches only kubetower.log inside a state directory the
// caller does not know. So this goes to os.Stderr directly, in one greppable
// line.
//
// The password is not in it: unattended stderr is usually a kept log, while the
// one-time file is 0600 and deletes itself.
//
// With a console it repeats the banner, which is the right trade: its presence
// does not depend on a measurement the caller cannot see. It names whether this
// was a first run or a reset.
func sayWherePasswordIs(onDemand bool, user, once string, onceErr error) {
	what := "kubetower first run"
	if onDemand {
		what = "kubetower --reset-password"
	}
	if once == "" {
		// Never name a file that is not there. An unattended first run is about
		// to be refused unless a console saw the banner, so this says why; on a
		// reset from a terminal the banner above may be where it is.
		fmt.Fprintf(os.Stderr, "%s: a password was drawn for %q and the one-time file could not be "+
			"written (%v), so no file holds it\n", what, user, onceErr)
		return
	}
	fmt.Fprintf(os.Stderr, "%s: the drawn password for %q is in %s, until the first successful "+
		"login deletes it\n", what, user, once)
}

// save writes the file through statefile's durable write, so a crash cannot
// leave a zero-length credentials file the next start reads as "no password".
func save(path string, held credentials) error {
	raw, err := json.MarshalIndent(held, "", "  ")
	if err != nil {
		return err
	}
	if err := statefile.Write(path, raw); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	narrowToOwner(path)
	return nil
}

// narrowToOwner gives a file holding a secret the owner-only, protected DACL
// on Windows (restrictToOwner); elsewhere the file is already 0600.
//
// credentials.json holds the session signing key, which forges any session, and
// PASSWORD-ONCE.txt a plain-text password; on Windows they would inherit their
// directory's ACL, which on another drive may grant every account. Narrowed
// after each write and on every read, so an older file is narrowed at the next
// start. Between statefile.Write's rename and this call the new file briefly
// carries its directory's ACL.
//
// A failure is logged, not fatal: FAT and exFAT have no ACLs, and refusing to
// start over it would cost the whole console.
func narrowToOwner(path string) {
	if err := restrictToOwner(path); err != nil {
		log.Printf("[kubetower] %s could not be narrowed to its owner (%v); it keeps its directory's "+
			"permissions", path, err)
	}
}

// randomPassword draws from an alphabet with no look-alikes in it.
//
// 0/O and 1/l/I are out: the string is read off a screen and typed, and a
// password that looks wrong is reported as broken.
func randomPassword(n int) (string, error) {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	out := make([]byte, n)
	for i := range out {
		// crypto/rand; rand.Int is uniform over [0, max).
		pick, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		out[i] = alphabet[pick.Int64()]
	}
	return string(out), nil
}

func randomKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

// resetPassword draws a new password on demand, shows it, and keeps only its
// hash.
//
// It saves the operator finding and deleting a JSON file whose path differs
// per profile, and a renamed backup (`credentials.json.old`) would be destroyed
// by the next save: statefile sweeps files named after the target plus a dot.
//
// The session secret goes with it: the cookie is stateless, and only its
// signature can revoke the open sessions.
//
// It writes the one-time file too: a reset from a shortcut has the first run's
// problem, no console, and the operators who need it are the locked-out ones.
// The note is overwritten, so it never names a replaced password.
func resetPassword() (err error) {
	// A failed reset says so on stderr: without a console log goes to the file
	// in the state directory, and the terminal would show only exit status 1.
	defer func() {
		if err != nil && !consoleAttached {
			fmt.Fprintf(os.Stderr, "kubetower --reset-password: %v\n", err)
		}
	}()

	// Refused while KT_PASSWORD_HASH is set, before anything is touched: the
	// front door then never reads credentials.json, so a drawn password would
	// never be asked for. `task dev:password` and a container's .env both set
	// it, and a Taskfile precondition would cover only one task.
	if os.Getenv("KT_PASSWORD_HASH") != "" {
		return errors.New("KT_PASSWORD_HASH is set, so the console signs in with that hash and " +
			"never reads credentials.json: a password drawn now would never be asked for, and " +
			"nothing was changed. Change the hash where it is set (for `task dev`, run `task " +
			"dev:password`), or unset it to go back to a drawn password")
	}
	path := filepath.Join(statefile.Dir(), credentialsFile)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", path, err)
	}
	// draw refuses if it cannot show the result to anybody, which makes this
	// safe from a shortcut.
	if _, _, err := draw(path, true); err != nil {
		return err
	}
	// Said, because the loopback desktop does not ask for this password, and a
	// reset that seems to do nothing gets repeated.
	if loopbackDesktop(envOr("KT_LISTEN", defaultListen), false, cluster.InContainer()) {
		fmt.Fprintln(os.Stderr, "Note: this desktop listens on loopback, where no password is asked for - the "+
			"window opens signed in, and `kubetower open` brings another. The password above is the one "+
			"asked for in a container or with KT_LISTEN beyond loopback.")
	}
	return nil
}

// storedCredentials reads the credentials file once for the whole process.
//
// Memoized because localCredentials may repair the file (an unusable session
// secret), and a second read after that write would be a second answer.
// needPassword is the profile's answer, asked by the first call; a process has
// one profile.
func storedCredentials(needPassword bool) (credentials, []byte, error) {
	credentialsOnce.Do(func() {
		heldCredentials, heldKey, heldErr = localCredentialsFor(needPassword)
	})
	return heldCredentials, heldKey, heldErr
}

var (
	credentialsOnce sync.Once
	heldCredentials credentials
	heldKey         []byte
	heldErr         error
)
