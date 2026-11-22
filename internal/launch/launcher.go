package launch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/thonicdev/kubetower/internal/auth"
	"github.com/thonicdev/kubetower/internal/statefile"
)

// The launcher secret, and a second launch handing off to the first. The
// server's half, and the argument, are internal/auth/handoff.go.

// launcherFile holds the launcher secret, beside credentials.json in the state
// directory. A file of its own: the credentials file is about a password, and
// the loopback desktop has none.
const launcherFile = "launcher.secret"

// launcherSecretBytes is the secret's entropy: 256 bits, hex-encoded in the file
// and on the wire.
const launcherSecretBytes = 32

// loopbackDesktop is whether this process is the loopback desktop: the desktop
// profile, outside a container, listening on loopback only. It is where the
// launcher secret and the handoff exist, and where the password is not offered.
func loopbackDesktop(listen string, inCluster, inContainer bool) bool {
	return !inCluster && !inContainer && listensOnLoopback(listen)
}

// launcherSecret reads the launcher secret from the state directory, and with
// create draws and writes one when there is none. Only the first instance
// creates it; a second launch reads it.
//
// The value goes only to the first instance's front door. A second launch sends
// MACs derived from it, never the value: not in a URL, the page, the log or on
// the wire.
//
//   - A file other accounts can read is not trusted (off Windows, where the mode
//     bits are the permission): a second launch refuses it and the instance
//     replaces it, as ssh refuses a readable private key. A restored backup or an
//     old 0755 state directory produces exactly that.
//   - On Windows the file gets an explicit owner-only DACL rather than the
//     inherited one, which on a second drive includes Authenticated Users.
//   - Two first launches at once agree: the file is published without replace
//     (publishSecret), so the loser reads the winner's secret, and the instance
//     that wins the port never holds a secret that lost the file.
func launcherSecret(create bool) (string, error) {
	path := filepath.Join(statefile.Dir(), launcherFile)
	secret, err := readLauncherSecret(path)
	switch {
	case err == nil:
		if create {
			// Narrowed on every start, so a file written without the DACL
			// is narrowed too. Best effort: the mode is already 0600.
			_ = restrictToOwner(path)
		}
		return secret, nil
	case errors.Is(err, os.ErrNotExist):
		if !create {
			return "", fmt.Errorf("no launcher secret at %s: no instance has started here yet", path)
		}
	default:
		if !create {
			return "", err
		}
		// Damaged, or readable by others: replaced. A second launch started
		// before this one could not hand off anyway.
		log.Printf("[kubetower] the launcher secret is replaced: %v", err)
		if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return "", fmt.Errorf("replacing %s: %w", path, rmErr)
		}
	}

	b := make([]byte, launcherSecretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	drawn := hex.EncodeToString(b)
	beforePublish()
	switch err := publishSecret(path, []byte(drawn+"\n")); {
	case err == nil:
		return drawn, nil
	case errors.Is(err, os.ErrExist):
		// Another first launch got there first: its secret is the one.
		return readLauncherSecret(path)
	default:
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
}

// beforePublish runs between drawing a secret and publishing it. A test seam,
// to put another process's file there first; nothing in production.
var beforePublish = func() {}

// errLauncherTooOpen is a launcher secret other accounts can read.
var errLauncherTooOpen = errors.New("other accounts can read it")

// readLauncherSecret reads and checks the file: os.ErrNotExist when there is
// none, errLauncherTooOpen when its mode lets others read it, and an error
// naming the file when it does not hold a secret this build wrote.
func readLauncherSecret(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if tooOpen(runtime.GOOS, info.Mode()) {
		return "", fmt.Errorf("%s is mode %o: %w", path, info.Mode().Perm(), errLauncherTooOpen)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	secret := strings.TrimSpace(string(raw))
	if b, decodeErr := hex.DecodeString(secret); decodeErr != nil || len(b) < launcherSecretBytes {
		return "", fmt.Errorf("%s does not hold a launcher secret this build wrote", path)
	}
	return secret, nil
}

// tooOpen is whether mode lets anybody but the owner at the file. Off Windows
// only: there the mode bits are the permission; on Windows they are not, and
// the DACL is set instead (restrictToOwner).
func tooOpen(goos string, mode os.FileMode) bool {
	return goos != "windows" && mode.Perm()&0o077 != 0
}

// publishSecret writes data to path, owner-only, without replacing a file
// already there (os.ErrExist). The bytes go to a narrowed temporary file which
// is hard-linked to path, so path appears complete or not at all; where a hard
// link is impossible, an O_EXCL create does the same with a short window in
// which the file is empty.
func publishSecret(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, statefile.DirPerm); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, launcherFile+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	_, werr := tmp.Write(data)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err := firstError(werr, serr, cerr); err != nil {
		return err
	}
	_ = os.Chmod(name, statefile.FilePerm)
	if err := restrictToOwner(name); err != nil {
		return fmt.Errorf("narrowing %s to its owner: %w", name, err)
	}
	linkErr := os.Link(name, path)
	if linkErr == nil || errors.Is(linkErr, os.ErrExist) {
		return linkErr
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, statefile.FilePerm)
	if err != nil {
		return err
	}
	if err := restrictToOwner(path); err != nil {
		f.Close()
		os.Remove(path)
		return fmt.Errorf("narrowing %s to its owner: %w", path, err)
	}
	_, werr = f.Write(data)
	serr = f.Sync()
	cerr = f.Close()
	if err := firstError(werr, serr, cerr); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}

// handoffTimeout bounds one handoff request. The instance is on this machine,
// so anything slower is not an answer worth waiting for.
const handoffTimeout = 5 * time.Second

// handoffClient returns what mints a one-time code from the instance running on
// listen, through the mutual exchange in internal/auth/handoff.go: the instance
// proves it holds the launcher secret before anything reusable is sent, and the
// code comes back sealed. Same signature as the front door's minter.
func handoffClient(listen, secret string) func(native bool) (string, error) {
	return func(native bool) (string, error) {
		return auth.RequestHandoff(context.Background(), secret, dialAddress(listen), native, handoffTimeout)
	}
}

// instanceAnswers reports whether something accepts connections on listen
// already - the sign that this is a second launch.
func instanceAnswers(listen string) bool {
	conn, err := net.DialTimeout("tcp", dialAddress(listen), 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// windowOpener is openWindow's shape, a parameter so a second launch can be
// exercised end to end without drawing a window.
type windowOpener func(ctx context.Context, listen string, mint func(native bool) (string, error),
	feed windowSources) (closed bool)

// secondLaunch reports whether this launch was a second one and has handed off
// to the instance already running, in which case Run returns at once: no
// kubeconfig, no clusters, no second server. Only a launch that would open a
// window, on the loopback desktop, onto a port that already answers.
func secondLaunch(window bool, listen string, inCluster, inContainer bool, open windowOpener) bool {
	if !window || !loopbackDesktop(listen, inCluster, inContainer) {
		return false
	}
	addr, err := instanceAddress(listen)
	if err != nil {
		// Said only when something answers, probed at the loopback
		// addresses on that port, never by resolving the name again.
		if answering := loopbackAnswering(listen); answering != "" {
			logf("something answers on %s, but this launch cannot ask it for a window: %v", answering, err)
		}
		return false
	}
	return instanceAnswers(addr) && handOff(addr, open)
}

// loopbackAnswering is the first loopback literal on listen's port that
// accepts a connection, or "": 127.0.0.1, then [::1].
func loopbackAnswering(listen string) string {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return ""
	}
	for _, host := range []string{"127.0.0.1", "::1"} {
		if addr := net.JoinHostPort(host, port); instanceAnswers(addr) {
			return addr
		}
	}
	return ""
}

// instanceAddressFile holds the address the running instance bound, written
// beside the launcher secret once it has bound (Run).
const instanceAddressFile = "launcher.address"

// writeInstanceAddress records the address this instance bound, owner-only.
func writeInstanceAddress(bound string) error {
	path := filepath.Join(statefile.Dir(), instanceAddressFile)
	if err := statefile.Write(path, []byte(bound+"\n")); err != nil {
		return err
	}
	return restrictToOwner(path)
}

// instanceAddress is the address a second launch reaches the running instance
// at: an address, never a name to resolve. With KT_LISTEN=localhost:5823 the
// instance bound one of the addresses localhost names, and resolving again
// could reach whatever holds the other.
//
//   - KT_LISTEN itself, when it is already an address.
//   - For a name, the address the instance recorded, when it is a loopback
//     literal on KT_LISTEN's port.
//   - Otherwise nothing: the name is refused rather than resolved.
func instanceAddress(listen string) (string, error) {
	host, port, err := net.SplitHostPort(dialAddress(listen))
	if err != nil {
		return "", fmt.Errorf("KT_LISTEN %q is not an address and a port", listen)
	}
	if _, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return net.JoinHostPort(strings.Trim(host, "[]"), port), nil
	}
	record := filepath.Join(statefile.Dir(), instanceAddressFile)
	// A record other accounts can write is not trusted.
	info, statErr := os.Stat(record)
	raw, err := os.ReadFile(record)
	if err == nil && statErr == nil && !tooOpen(runtime.GOOS, info.Mode()) {
		recorded := strings.TrimSpace(string(raw))
		if h, p, err := net.SplitHostPort(recorded); err == nil && p == port {
			if ip, err := netip.ParseAddr(h); err == nil && ip.Unmap().IsLoopback() {
				return recorded, nil
			}
		}
	}
	return "", fmt.Errorf("KT_LISTEN names %s, and no running instance recorded which address it bound for "+
		"that name. Set KT_LISTEN to an address, 127.0.0.1:%s", host, port)
}

// secondLaunchFeed is what a second launch's native window reads about the rest
// of the process: nothing, since all of it belongs to the first instance. On
// macOS that window has no Dock badge, App Nap guard or ⌘Q question, and its
// Cluster menu says the list could not be read.
var secondLaunchFeed = windowSources{}

// handOff is a second launch on the loopback desktop: instead of failing on the
// port, it asks the running instance for a code and opens a window on it. False
// means the occupant is not a KubeTower that will hand off (another program,
// another state directory, an older build), and the ordinary start then says
// the port is taken.
//
// The window is this process's own, on the first instance's address: closing it
// ends this process and leaves the server running; a browser window returns at
// once and this process exits behind it.
func handOff(listen string, open windowOpener) bool {
	secret, err := launcherSecret(false)
	if err != nil {
		logf("a KubeTower may already be running on %s, but this launch cannot ask it for a "+
			"window: %v", dialAddress(listen), err)
		return false
	}
	mint := handoffClient(listen, secret)
	// One request first, so a refusal is known before a window is drawn on a
	// sign-in page. The code is used by the native window or lapses in a minute.
	first, err := mint(true)
	if errors.Is(err, auth.ErrNotTheInstance) {
		// Something else holds the port, possibly waiting for this. It was sent
		// nothing reusable, and no window opens on it: that would show its page
		// at the console's address and hand it the window's stored cookie.
		logf("something other than this account's KubeTower holds %s: %v. No window was opened on it "+
			"and nothing it could reuse was sent to it. Close whatever holds the port, or start KubeTower "+
			"on another one with KT_LISTEN", dialAddress(listen), err)
		return false
	}
	if errors.Is(err, auth.ErrInstanceBusy) {
		// It is KubeTower, flooded with hellos by something on this machine:
		// not a reason to start a second server, so this launch ends here.
		logf("KubeTower is running on %s but %v. No window was opened", dialAddress(listen), err)
		return true
	}
	if err != nil {
		logf("something answers on %s, and it did not hand this launch a window (%v)", dialAddress(listen), err)
		return false
	}
	logf("KubeTower is already running on %s; opening a window on it", dialAddress(listen))
	used := false
	open(context.Background(), listen, func(native bool) (string, error) {
		if native && !used {
			used = true
			return first, nil
		}
		return mint(native)
	}, secondLaunchFeed)
	// A browser window returns at once and this process is about to exit; its
	// page goes once the browser has read it.
	releaseLaunchPages()
	// The native window's own line says closing it stops KubeTower, which is
	// not true of this one.
	logf("this launch is done; the KubeTower on %s keeps running", dialAddress(listen))
	return true
}
