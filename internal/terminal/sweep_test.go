package terminal

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// TestAStartSweepsOnlyWhatAProcessThatIsGoneLeft: a start removes a term- or
// claude- directory whose owner
// is provably gone (here a reused PID: this process's PID with another start
// time), and one with no owner record once it is older than ownerlessTTL. It
// keeps one whose owner lives however old it is - a second running instance's
// open shell - a young ownerless one, `claude-workspace` (the assistant's
// trusted workspaces), and the desktop's own.
func TestAStartSweepsOnlyWhatAProcessThatIsGoneLeft(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	ancient := time.Now().Add(-ownerlessTTL - 24*time.Hour)
	reused := fmt.Sprintf("%d 0 %s", os.Getpid(), orDash(pidNamespace()))
	cases := []struct {
		name  string
		owner string // "" writes no owner record
		old   bool
		kept  bool
	}{
		{"term-1001", reused, false, false},                                  // a reused PID: gone
		{"claude-1002", reused, false, false},                                // the same, for the assistant
		{"term-1003", self(), true, true},                                    // a live owner, however old
		{"term-1004", "", true, false},                                       // ownerless, past the bound
		{"term-1005", "", false, true},                                       // ownerless and young
		{"claude-workspace", "", true, true},                                 // never a session directory
		{"desktop-1006", reused, false, true},                                // sweepLocal's, not this one's
		{"term-notdigits", reused, true, true},                               // not a name MkdirTemp makes
		{"term-1007", fmt.Sprintf("%d 0 pid:[1]", os.Getpid()), false, true}, // another PID namespace's record: never judged
	}
	for _, c := range cases {
		dir := filepath.Join(root, c.name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "kubeconfig"), []byte("credentials"), 0o600); err != nil {
			t.Fatal(err)
		}
		if c.owner != "" {
			if err := os.WriteFile(filepath.Join(dir, ownerFile), []byte(c.owner+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if c.old {
			if err := os.Chtimes(dir, ancient, ancient); err != nil {
				t.Fatal(err)
			}
		}
	}
	// The trusted workspace's files survive.
	slot := filepath.Join(root, claudeWorkspaceName, "s01")
	if err := os.MkdirAll(slot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(slot, "CLAUDE.md"), []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(root, claudeWorkspaceName), ancient, ancient); err != nil {
		t.Fatal(err)
	}

	kubeconfig := execKubeconfig(t, "somewhere")
	if _, err := New(managerFor(t, kubeconfig), Options{Kubeconfig: cluster.Kubeconfig{kubeconfig}, WorkRoot: root, Access: accesstest.Allow()}); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		_, err := os.Stat(filepath.Join(root, c.name))
		if gone := os.IsNotExist(err); gone == c.kept {
			t.Errorf("%s: kept=%t after a start, want kept=%t", c.name, !gone, c.kept)
		}
	}
	if _, err := os.Stat(filepath.Join(slot, "CLAUDE.md")); err != nil {
		t.Errorf("the start sweep took the assistant's trusted workspace: %v", err)
	}
}

// A session directory this process makes names this process as its owner.
func TestASessionDirectoryNamesItsOwner(t *testing.T) {
	dir := t.TempDir()
	claimDir(dir)
	if gone, known := ownerGone(dir); !known || gone {
		t.Errorf("a directory this live process owns reads gone=%t known=%t", gone, known)
	}
}

// TestClosingTheTerminalEndsItsSessionsAndRemovesTheirDirectories: at
// shutdown, the shells still open are ended and their directories removed
// before the process goes. http.Server.Shutdown does not wait for a hijacked
// connection, so nothing else would.
func TestClosingTheTerminalEndsItsSessionsAndRemovesTheirDirectories(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")
	kubeconfig := execKubeconfig(t, "somewhere")
	term, err := New(managerFor(t, kubeconfig), Options{Kubeconfig: cluster.Kubeconfig{kubeconfig}, Access: accesstest.Allow()})
	if err != nil {
		t.Fatal(err)
	}
	workdir := filepath.Join(t.TempDir(), "term-open")
	if err := os.Mkdir(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	pty := newFullPTY()
	sess := &session{pty: pty, workdir: workdir, exited: make(chan struct{})}

	// What Close waits for is the session's cleanup, not the handler's return.
	// The handler below defers in serveShell's order, so they run last-first:
	// the session closes (PTY reaped, directory removed), then release gives
	// the slot back and calls live.Done - the WaitGroup Close waits on - and
	// only then come stream.close, a no-op once Close's farewell has closed the
	// socket, and the return. So `cleaned` is closed before live.Done and is
	// ordered before Close returns; `returned` is not, and asserting on it
	// would race the handler's last two defers under -race.
	cleaned := make(chan struct{})
	returned := make(chan struct{})
	upgrader := websocket.Upgrader{CheckOrigin: sameOrigin}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(returned)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		s := term.openStream(conn, wireFramed)
		defer s.close()
		release, err := term.hold(r.Context(), s)
		if err != nil {
			return
		}
		defer release()
		defer func() {
			sess.close()
			close(cleaned)
		}()
		relay(s, sess)
	}))
	t.Cleanup(srv.Close)
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	waitFor(t, func() bool { return term.sessions.live() == 1 }, "the session never opened")

	term.Close()

	select {
	case <-cleaned:
	default:
		t.Fatal("Close returned before the session had cleaned up after itself")
	}
	if _, err := os.Stat(workdir); !os.IsNotExist(err) {
		t.Errorf("the session's directory, which holds a kubeconfig, survived Close: %v", err)
	}
	if n := term.sessions.live(); n != 0 {
		t.Errorf("%d sessions still held after Close", n)
	}
	// And the handler does end: what is left of it after release cannot block.
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Error("the session's handler had not returned five seconds after Close")
	}
}

// TestTheOwnerRecordIsWrittenWhole: claimDir writes to a temporary name and
// renames it into place, so no reader sees half a record, and no temporary
// file is left behind.
func TestTheOwnerRecordIsWrittenWhole(t *testing.T) {
	dir := t.TempDir()
	claimDir(dir)
	raw, err := os.ReadFile(filepath.Join(dir, ownerFile))
	if err != nil || strings.TrimSpace(string(raw)) != self() {
		t.Fatalf("the owner record reads %q (%v), want %q", raw, err, self())
	}
	if _, err := os.Stat(filepath.Join(dir, ownerFile+".tmp")); !os.IsNotExist(err) {
		t.Errorf("the temporary owner record was left behind: %v", err)
	}
}
