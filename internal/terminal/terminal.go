// Package terminal gives the operator a command line on a cluster, a way into
// a pod, and the ports that go with them.
//
// A local shell, `exec` into a pod and a forwarded port all hold a byte stream
// open against a cluster until somebody lets go, and the hard parts (refusing a
// cross-site upgrade, framing terminal bytes, making the far end die with the
// socket) are solved once here.
//
// The shell is a prompt with kubectl and helm already pointed at the cluster
// the UI shows, through a per-session kubeconfig produced by client-go.
package terminal

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/nodeshell"
	"github.com/thonicdev/kubetower/safego"
)

// Options configure a Terminal. Every field has a working default except the
// kubeconfig, which has a defensible one.
type Options struct {
	// Kubeconfig is the list of files a session copies its context out of. It must
	// be the list cluster.Manager reads, all of it: a context found by the manager
	// but missing from the session's source gives an empty session kubeconfig, and
	// kubectl reports no current context. See writeSessionKubeconfig.
	Kubeconfig cluster.Kubeconfig

	// Shell is the command line the operator lands on, argv[0] included. Empty
	// means the platform default (defaultSessionShell), which differs per platform;
	// its no-init flags matter. shell.go reads the family off argv[0] to decide how
	// the prompt and scripts reach it.
	Shell []string

	// WorkRoot is where per-session directories are created. Each holds a
	// kubeconfig with live credentials, so it must be readable by this account
	// only (see secureWorkRoot).
	WorkRoot string

	// ReadOnly reports whether a cluster is declared read-only. nil means no
	// cluster is.
	ReadOnly func(cluster string) bool

	// Access is the choke point a pod exec and a node shell are reviewed at before
	// they open: may the person behind the request `create pods/exec` on this pod.
	// Nil refuses both. See internal/access.
	Access *access.Gate

	// MaxSessions bounds the shells, pod execs, node shells and Claude sessions
	// open at once, across every cluster. Zero takes maxSessions; there is
	// deliberately no way to ask for no limit. See limit.go.
	MaxSessions int

	// MaxSessionsPerPerson is one person's share of MaxSessions, counted by the
	// name the front door puts on the request. Zero is no share (the desktop's one
	// person); internal/launch sets PerPersonSessions under the server profile. A
	// share at or above MaxSessions is no share.
	MaxSessionsPerPerson int

	// ClaudeBin is the Claude Code binary. Empty asks PATH; still empty means this
	// image has none, and the control says so.
	ClaudeBin string

	// ClaudeReadOnly puts the mutating-verb overrides in front of every Claude
	// session, on every cluster, not only those ReadOnly reports. internal/launch
	// defaults it to true: a model asked to fix something will run `kubectl delete`.
	ClaudeReadOnly bool

	// ClaudePermissionMode is what `claude --permission-mode` is given. Empty asks
	// KT_CLAUDE_PERMISSION_MODE, then "auto"; see defaultClaudePermissionMode for the
	// trade. It is read here rather than in internal/launch, unlike every other KT_
	// variable, which is a wart: it belongs beside ClaudeReadOnly.
	ClaudePermissionMode string

	// StateDir is where the node shell's image setting is kept. Empty asks
	// nodeshell.Dir() (KT_STATE_DIR); empty after that keeps the setting for the
	// life of the process.
	StateDir string

	// IdleExec ends a pod exec that has received nothing from the person for this
	// long, in a framed refusal. Zero is no bound (the desktop's one person).
	// internal/launch sets IdleExecBound under the server profile, where an idle
	// exec, kept alive by pongs and restored by a reload, holds a shared slot. See
	// watch.go.
	IdleExec time.Duration

	// Logf receives the operational log lines. nil sends them to the standard
	// logger.
	Logf func(format string, v ...any)
}

// Terminal serves the shell and the pod exec, and owns the read-only overrides
// both may run behind.
type Terminal struct {
	clusters *cluster.Manager
	opts     Options
	logf     func(string, ...any)

	// openExec builds the stream a pod exec runs over; nil is newExecutor. The
	// seam lets a test park pump the way a pod not reading its stdin does, which no
	// fake API server can.
	openExec func(*rest.Config, *url.URL) (remotecommand.Executor, error)

	// open is every live session's stream, by the person who opened it, so
	// signing a person out can close theirs. See hold and EndSessionsOf.
	openMu sync.Mutex
	open   map[string]map[*stream]string
	// closing is set by Close under openMu: hold refuses from then on, so no
	// session starts after Close's snapshot and live.Add never races live.Wait.
	// live counts the sessions hold handed out and not yet given back, so Close can
	// wait for their cleanup.
	live    sync.WaitGroup
	closing bool

	// pingEvery, when set, replaces pingPeriod on every socket this Terminal
	// opens, for tests. A field, not a package variable, for the race stream.go
	// describes.
	pingEvery time.Duration

	// recheckEvery and now replace recheckEvery and time.Now in watch, for tests.
	recheckEvery time.Duration
	now          func() time.Time

	upgrader websocket.Upgrader

	// roBinDir holds the kubectl and helm overrides prepended to PATH for a shell
	// on a cluster declared read-only.
	roBinDir string

	// assistantBinDir and assistantROBinDir are the overrides for a Claude
	// session, which also declines to read Secrets (nosecrets.go). Two directories,
	// not one flag, because a PATH search finds one `kubectl`: assistantROBinDir
	// also refuses writes, assistantBinDir only Secret reads, and
	// KT_CLAUDE_READONLY chooses.
	assistantBinDir   string
	assistantROBinDir string

	// fence is whether those overrides are what a session actually resolves,
	// measured once at start-up (fence.go).
	fence Fence

	// claudeBin is the resolved Claude Code binary, or empty when this image
	// has none.
	claudeBin string
	// claudeMode is the validated --permission-mode every session starts in.
	claudeMode string
	// claudeSlots hands each Claude session a workspace of its own, so two
	// sessions never share notes. Nil where this image has no Claude binary; take
	// answers "" on a nil receiver.
	claudeSlots *claudeSlots

	// nodeShell remembers which image a node shell runs. Never nil: New falls back
	// to an unpersisted store.
	nodeShell *nodeshell.Store

	// sessions is the ceiling shared by every kind of session.
	sessions *sessionLimit

	// swept remembers which clusters have had their leftover node-shell pods
	// looked at, once per cluster per process; a failed attempt is forgotten
	// (sweepNodeShells).
	swept sync.Map

	skew *skewCache
}

// sessionKubeconfig is the list a session's kubeconfig is copied out of: the
// one handed in, or, on the desktop only, KUBECONFIG and its default
// (cluster.KubeconfigFromEnv, the one reader of KUBECONFIG).
//
// Under the server profile an empty list stays empty: the one cluster is the
// pod's own, reached through rest.InClusterConfig() with no kubeconfig, and
// falling back would copy contexts from whatever file KUBECONFIG names in the
// pod. A session then fails on "no kubeconfig file to read" instead of reaching
// a cluster nobody chose.
func sessionKubeconfig(clusters interface{ InCluster() bool }, given cluster.Kubeconfig) cluster.Kubeconfig {
	if len(given) > 0 || clusters.InCluster() {
		return given
	}
	return cluster.KubeconfigFromEnv()
}

// New builds a Terminal and writes the read-only overrides once, at start-up:
// they never vary, and a session that had to render them could fail for a
// reason the operator cannot act on.
func New(clusters *cluster.Manager, opts Options) (*Terminal, error) {
	if clusters == nil {
		return nil, errors.New("terminal: no cluster manager")
	}
	opts.Kubeconfig = sessionKubeconfig(clusters, opts.Kubeconfig)
	if len(opts.Shell) == 0 {
		opts.Shell = defaultSessionShell()
	}
	if opts.WorkRoot == "" {
		opts.WorkRoot = workRoot()
	}
	if opts.ReadOnly == nil {
		opts.ReadOnly = func(string) bool { return false }
	}

	t := &Terminal{
		clusters: clusters,
		opts:     opts,
		logf:     opts.Logf,
		sessions: newSessionLimit(opts.MaxSessions).shared(opts.MaxSessionsPerPerson),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin:     sameOrigin,
		},
	}
	if t.logf == nil {
		t.logf = func(format string, v ...any) { log.Printf("[kubetower] "+format, v...) }
	}
	t.skew = newSkewCache(clusters, opts.Kubeconfig)

	if err := secureWorkRoot(opts.WorkRoot); err != nil {
		return nil, err
	}
	t.sweepSessions()
	if err := t.writeReadOnlyShims(); err != nil {
		return nil, err
	}
	t.measureReadOnlyFence()

	// Which image a node shell runs, ready before the first click.
	stateDir := opts.StateDir
	if stateDir == "" {
		stateDir = nodeshell.Dir()
	}
	t.nodeShell = nodeshell.New(stateDir, t.logf)
	if path := t.nodeShell.Path(); path != "" {
		t.logf("node shell image remembered in %s (%s)", path, t.nodeShell.Image())
	}

	// Claude Code, with what must be ready before the first click: its workspaces,
	// and the config that stops it asking whether it trusts them.
	t.claudeBin = findClaude(opts.ClaudeBin)
	t.claudeMode = claudePermissionMode(
		envOr(opts.ClaudePermissionMode, os.Getenv("KT_CLAUDE_PERMISSION_MODE")), t.logf)
	if t.claudeBin != "" {
		// Every workspace up front, one per session the ceiling allows, pre-trusted in
		// one write (see claudeWorkspaceName).
		workspaces := t.claudeWorkspaces()
		for _, dir := range workspaces {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, fmt.Errorf("claude workspace: %w", err)
			}
		}
		// The parent is the fallback and is not handed out; the slots are the rest.
		t.claudeSlots = newClaudeSlots(workspaces[1:])
		prepareClaudeConfig(claudeConfigFile(), claudeConfigDir(), workspaces, claudeVersion(t.claudeBin), t.logf)
		t.logf("claude terminal available (%s, read-only sessions: %t, permission mode: %s)",
			t.claudeBin, opts.ClaudeReadOnly, t.claudeMode)
	} else {
		t.logf("no claude binary in this image, the Claude terminal will refuse in words")
	}
	return t, nil
}

// defaultSessionShell is the shell every dock session gets. Its
// no-initialization flags have two unrelated reasons, both easy to lose:
//
//   - without `--norc`, the image's `/etc/bash.bashrc` resets PS1 and the
//     cluster name disappears from the prompt;
//
//   - the read-only guardrail: sessionEnv prepends the override directory to
//     PATH, which wins only if nothing re-points it, and a profile (`brew
//     shellenv`, for one) would put the real kubectl first. Measured in an
//     Alpine container with a decoy kubectl prepended in `/etc/profile.d`,
//     `~/.bash_profile` and `~/.bashrc`:
//
//     bash --norc --noprofile -i  ->  the override
//     bash -l -c                  ->  the decoy
//
// Options.Shell is configurable, so this is not a guarantee, which is why
// fence.go probes the result; TestTheSessionShellReadsNoInitFile drives this
// slice against such a profile, with a login shell as the control.
//
// Per platform because `/bin/bash` cannot exist on Windows: resolveDefaultShell
// carries the two reasons across (`-NoProfile`, `/D`; see shell_windows.go), and
// shell_other.go says why macOS stays on bash.
func defaultSessionShell() []string {
	return resolveDefaultShell()
}

// envOr is the option, or the environment value behind it.
func envOr(configured, fallback string) string {
	if configured != "" {
		return configured
	}
	return fallback
}

// writeReadOnlyShims writes kubectl and helm overrides into the directories
// prepended to PATH for a restricted session. Three sets, because a PATH search
// finds one `kubectl` and what is refused depends on the kind of session:
//
//	ro-bin            writes, plugins          a shell on a read-only cluster
//	assistant-bin     Secret reads, plugins    a Claude session the operator lets write
//	assistant-ro-bin  all three                a Claude session, the default
//
// One shim branching on an environment variable instead would make the safe
// state depend on that variable being present.
func (t *Terminal) writeReadOnlyShims() error {
	sets := []struct {
		dir   string
		write func(binary, real string) string
		into  *string
	}{
		{"ro-bin", shimContent, &t.roBinDir},
		{"assistant-bin", func(b, r string) string { return assistantShimContent(b, r, false) }, &t.assistantBinDir},
		{"assistant-ro-bin", func(b, r string) string { return assistantShimContent(b, r, true) }, &t.assistantROBinDir},
	}

	// Logged once, not once per set.
	missing := map[string]bool{}

	for _, set := range sets {
		dir := filepath.Join(t.opts.WorkRoot, set.dir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("override directory: %w", err)
		}

		for _, name := range []string{"kubectl", "helm"} {
			real, err := exec.LookPath(name)
			if err != nil {
				// A missing binary needs no restraining.
				if !missing[name] {
					t.logf("%s not found, no override installed", name)
					missing[name] = true
				}
				continue
			}
			// File name and script are both per platform: an extensionless `#!/bin/sh`
			// file is not executable on Windows, where PATHEXT decides (see
			// readonly_shimfile_windows.go).
			if err := os.WriteFile(filepath.Join(dir, shimFileName(name)),
				[]byte(set.write(name, real)), shimMode); err != nil {
				return fmt.Errorf("override for %s: %w", name, err)
			}
		}

		*set.into = dir
	}

	return nil
}

// measureReadOnlyFence establishes whether the overrides just written are what
// a read-only session's shell resolves.
//
// Never fatal: a probe that could not run leaves Fence.Measured false, so
// nothing claims a fence it could not see. Unwritable shims are fatal, in
// writeReadOnlyShims.
//
// Skipped where no session can open (a Windows without ConPTY,
// ptysupport_windows.go). On Windows otherwise the overrides are `.cmd` files
// and this probe is what shows a PowerShell session resolves them, since
// PATHEXT, an execution policy and a profile re-pointing $env:PATH can each
// defeat a correct shim.
//
// Skipped for the cmd.exe family too: an untested batch probe would be a
// measurement nobody checked, and "unmeasured" is honest where "intact" would be
// a claim.
func (t *Terminal) measureReadOnlyFence() {
	if !ptySupported {
		t.fence = Fence{Detail: "no dock session can open on this build, so there is nothing to probe " +
			"(this Windows has no ConPTY, which means older than 10 1809)."}
		return
	}
	if shellFamilyOf(t.opts.Shell) == familyCmd {
		t.fence = Fence{Detail: "the guardrail is not probed when the session shell is cmd.exe, " +
			"so KubeTower cannot say whether a session resolves the overrides in " + t.roBinDir + "."}
		return
	}

	t.fence = measureFence(t.opts.Shell, t.roBinDir, os.Getenv("PATH"), fenceRun(t.opts.Shell))

	// Logged in every state, distinctly: this is the line someone greps for when a
	// dock session surprises them.
	switch t.fence.State() {
	case "intact":
		t.logf("read-only guardrail %s", t.fence.Detail)
	case "absent":
		t.logf("READ-ONLY GUARDRAIL NOT IN PLACE: %s "+
			"KT_READONLY_CLUSTERS and KT_CLAUDE_READONLY cannot restrain kubectl or helm in a dock session "+
			"until this is fixed.", t.fence.Detail)
	default:
		t.logf("read-only guardrail not established: %s", t.fence.Detail)
	}
}

// ShellHandler serves the shell over a WebSocket. The cluster comes from the
// {cluster} path segment when the route has one, else from the query.
func (t *Terminal) ShellHandler() http.Handler {
	return t.shellHandler(wireRaw)
}

// FramedShellHandler serves the same shell over api/websocket.md's frame
// vocabulary: JSON both ways, bytes base64-encoded, and a refusal as a `state`
// frame rather than a red sentence. The session behind both wires is the same.
func (t *Terminal) FramedShellHandler() http.Handler {
	return t.shellHandler(wireFramed)
}

func (t *Terminal) shellHandler(speaks wire) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.serveShell(w, r, speaks)
	})
}

func (t *Terminal) serveShell(w http.ResponseWriter, r *http.Request, speaks wire) {
	// Resolving against the kubeconfig is also the filter: only a context already
	// in it reaches a command line.
	name, ok := t.clusters.Resolve(clusterParam(r))
	if !ok {
		http.Error(w, "unknown cluster", http.StatusNotFound)
		return
	}

	conn, err := t.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade has already written its own response.
		t.logf("terminal upgrade failed: %v", err)
		return
	}
	stream := t.openStream(conn, speaks)
	defer stream.close()

	// The ceiling is checked after the upgrade: a refused handshake gives the
	// browser no status and no body, so the pane could only show its generic
	// failure. Upgrading first costs one socket for one sentence that says what
	// happened.
	release, err := t.hold(r.Context(), stream)
	if err != nil {
		stream.refuse(stateUnreadable, codeNotSupportedHere, err.Error())
		t.logf("terminal refused on %q: %v", name, err)
		return
	}
	defer release()

	session, err := t.newSession(name)
	if err != nil {
		stream.refuse(stateUnreadable, codeUnclassified,
			fmt.Sprintf("cannot open a session: %v", err))
		return
	}
	defer session.close()

	readOnly := t.opts.ReadOnly(name)
	t.logf("terminal opened on %q (read-only: %t)", name, readOnly)

	// A read-only tab whose guardrail was not established says so before the
	// operator types: its prompt says `read-only` (buildPrompt), a claim this
	// corrects. Nothing is written for a writable cluster, where nothing was
	// claimed.
	if readOnly {
		if notice := fenceNotice(t.fence); notice != "" {
			stream.notice(notice)
		}
	}

	// In the background so the prompt appears at once: the check costs an API
	// round trip (on EKS, a token fetch). Through safego.
	safego.Go("terminal: the version skew notice", func() error {
		if warning := t.skew.warning(name); warning != "" {
			stream.notice(warning)
		}
		return nil
	}, nil)

	// relay says the shell ended before it closes the socket; said here,
	// afterwards, it would go into a closed connection.
	relay(stream, session)
	t.logf("terminal closed on %q", name)
}

// sweepSessions removes the shell and assistant session directories an
// earlier process left in the work root: each holds a kubeconfig, and one left
// by a crash, a kill or a logoff would otherwise stay for good.
//
// Only what a gone process left (owner.go): a directory whose owner is not
// running, or is a reused PID. One whose owner lives is kept however old, as it
// may be another running instance's open shell; one with no owner record is kept
// for ownerlessTTL. Only os.MkdirTemp's names are considered, so
// `claude-workspace` is never touched. Failures are ignored one directory at a
// time, as in sweepLocal.
func (t *Terminal) sweepSessions() {
	entries, err := os.ReadDir(t.opts.WorkRoot)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-ownerlessTTL)
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !sessionDirName.MatchString(name) {
			continue
		}
		dir := filepath.Join(t.opts.WorkRoot, name)
		if gone, known := ownerGone(dir); known {
			if gone {
				_ = os.RemoveAll(dir)
			}
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(dir)
		}
	}
}

// relay joins a browser to a pseudo-terminal until either ends, for the shell
// and the assistant alike, and either end finishing ends both.
//
// pump writes keystrokes into the PTY, and a full PTY input buffer (a large
// paste while a program that does not read stdin runs) blocks that write; pump
// then never reads the socket again, and with session.close deferred behind it,
// the shell, the slot and the kubeconfig directory would stay. Here the stream
// closing for any reason closes the session, and closing the PTY unparks the
// write.
func relay(stream *stream, session *session) {
	// Shell output towards the browser. Its end is how we learn the shell exited:
	// a PTY read fails once the last descriptor on the other side is gone.
	go func() {
		defer session.markExited()
		_ = stream.copyFrom(session.pty)
	}()

	// Closing the socket is what unblocks pump's read when the shell ends;
	// otherwise `exit` would leave the loop parked until the browser went, with the
	// credentials directory still there. The other way round, the browser going
	// closes the session. Both closes are once-only, so the caller's deferred ones
	// become no-ops.
	//
	// The end is said here, between the exit and the close, the one place that
	// knows the far end finished while the socket is still open: the output
	// goroutine marks the exit after its last write, and nothing closes the socket
	// before this does.
	go func() {
		select {
		case <-session.exited:
			stream.ended(nil)
		case <-stream.closed():
		}
		stream.close()
		session.close()
	}()

	stream.pump(session.pty, func(cols, rows uint16) {
		session.resize(cols, rows)
	})
}

// openStream is newStream with this Terminal's keepalive period (overridden
// only in tests).
func (t *Terminal) openStream(conn *websocket.Conn, speaks wire) *stream {
	if t.pingEvery > 0 {
		return newTimedStream(conn, speaks, pongWait, t.pingEvery, writeWait)
	}
	return newStream(conn, speaks)
}

// clusterParam reads the cluster from wherever the route put it.
func clusterParam(r *http.Request) string {
	if v := r.PathValue("cluster"); v != "" {
		return v
	}
	return r.URL.Query().Get("cluster")
}

// --- the session -------------------------------------------------------------

type session struct {
	// pty is the terminal and the process behind it, an interface because Windows
	// has no *os.File PTY or *exec.Cmd for it (ConPTY needs its own CreateProcess).
	// See pty.go.
	pty     ptySession
	workdir string

	// release gives back what this session borrowed for its lifetime, today
	// Claude's workspace slot. Called from close, which runs on every exit path; a
	// leaked slot makes later sessions share a workspace and its notes.
	release func()

	// logf is where a failed resize is said; nil says nothing.
	logf func(format string, v ...any)

	// resizeFailed is set at the first failed resize, so a pane being dragged
	// after the shell is gone logs one line per session, not one per frame.
	resizeFailed atomic.Bool

	closeOnce sync.Once

	// exited is closed once the shell's output stream has ended, the reliable
	// sign the process is gone: a PTY read fails once the last descriptor on the
	// other side closes.
	exited   chan struct{}
	exitOnce sync.Once
}

// resize passes the browser's pane size to the terminal. A failure does not
// end the session but is logged once, with the size, since the pane then keeps
// its old size silently.
func (s *session) resize(cols, rows uint16) {
	err := s.pty.Resize(cols, rows)
	if err == nil || s.logf == nil || !s.resizeFailed.CompareAndSwap(false, true) {
		return
	}
	s.logf("terminal resize to %dx%d failed in %s (later failures of this session not logged): %v",
		cols, rows, filepath.Base(s.workdir), err)
}

func (t *Terminal) newSession(name string) (*session, error) {
	// Each session gets its own kubeconfig, narrowed to the chosen context and
	// marked current, so kubectl and helm need no flags and two tabs on two clusters
	// do not interfere.
	workdir, err := os.MkdirTemp(t.opts.WorkRoot, "term-")
	if err != nil {
		return nil, fmt.Errorf("session directory: %w", err)
	}
	// Owned, so a later start's sweep can tell it from a dead process's.
	claimDir(workdir)

	kubeconfig := filepath.Join(workdir, "kubeconfig")
	// The session's own AWS home, inside its directory so it is removed with it
	// (awshome.go).
	if err := writeSessionKubeconfig(t.opts.Kubeconfig, name, kubeconfig, newSessionAWSHome(workdir), t.logf); err != nil {
		_ = os.RemoveAll(workdir)
		return nil, err
	}

	readOnly := t.opts.ReadOnly(name)

	// The prompt reaches each shell family differently (PS1 for POSIX, a function
	// for PowerShell), so the argv is built here; see shell.go.
	argv := sessionArgv(t.opts.Shell, name, readOnly)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = sessionEnv(os.Environ(), sessionSpec{
		cluster:    name,
		kubeconfig: kubeconfig,
		readOnly:   readOnly,
		roBinDir:   t.roBinDir,
		path:       os.Getenv("PATH"),
		family:     shellFamilyOf(t.opts.Shell),
	})
	cmd.Dir = workdir

	term, err := startPTY(cmd, initialCols, initialRows)
	if err != nil {
		_ = os.RemoveAll(workdir)
		return nil, fmt.Errorf("starting the shell: %w", err)
	}

	return &session{pty: term, workdir: workdir, exited: make(chan struct{}), logf: t.logf}, nil
}

type sessionSpec struct {
	cluster    string
	kubeconfig string
	readOnly   bool
	roBinDir   string
	path       string
	// family is which shell this environment is for, and decides only which
	// variable the prompt goes in. The zero value is POSIX.
	family shellFamily
}

// sessionEnv assembles the environment one shell runs in. Separate because it
// is the other half of the read-only guardrail: the shims always exist, and this
// alone decides whether the shell resolves kubectl to one. A correct shim off
// the PATH protects nothing, and looks the same from outside.
func sessionEnv(base []string, spec sessionSpec) []string {
	env := append(withoutKubeTowerVars(base),
		"KUBECONFIG="+spec.kubeconfig,
		"KT_CLUSTER="+spec.cluster,
		// True on Windows too: a ConPTY is an xterm-compatible VT, which is what
		// conhost renders into the pipe.
		"TERM=xterm-256color",
		// Set for every family, including those that ignore it: inert in PowerShell,
		// needed by a POSIX shell configured on Windows, and a condition here would be a
		// second place the family is read.
		"PS1="+buildPrompt(spec.cluster, spec.readOnly),
	)
	if spec.family == familyCmd {
		// cmd.exe reads its prompt from the environment as a POSIX shell reads PS1,
		// in another variable and escape language.
		env = append(env, "PROMPT="+buildCmdPrompt(spec.cluster, spec.readOnly))
	}

	// The PATH the operator types at, carrying the overrides only when the cluster
	// is declared read-only. Prepended: appended, the real kubectl would come first.
	path := spec.path
	if spec.readOnly {
		env = append(env, "KT_READONLY=1")
		path = spec.roBinDir + string(os.PathListSeparator) + path
	}
	return append(env, "PATH="+path)
}

// withoutKubeTowerVars drops every inherited KT_ variable from an environment.
//
// Both callers pass os.Environ(), and this process's environment holds
// KT_SESSION_SECRET (the HMAC key every session cookie is signed with) and
// KT_PASSWORD_HASH, which `env` in any terminal would otherwise print. A Claude
// session's transcript leaves the machine, and a cookie-signing key in it forges
// sessions for as long as the key lives.
//
// The whole prefix rather than a list of names: a new secret is excluded by
// default, and anything a session needs is set explicitly (KUBECONFIG,
// KT_CLUSTER, TERM, PS1 above; KT_READONLY and, for Claude, KT_READONLY_WHY are
// set by this package, never inherited). Every other KT_ variable is read once
// at start-up by internal/launch and means nothing to kubectl, helm or Claude
// Code.
//
// Not extended to AWS_*: an EKS context authenticates by running `aws eks
// get-token`, which needs them.
func withoutKubeTowerVars(base []string) []string {
	out := make([]string, 0, len(base))
	for _, entry := range base {
		if strings.HasPrefix(entry, "KT_") {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// wait blocks until the shell has stopped producing output.
func (s *session) wait() { <-s.exited }

// markExited is called by whoever notices the PTY has ended.
func (s *session) markExited() { s.exitOnce.Do(func() { close(s.exited) }) }

func (s *session) close() {
	s.closeOnce.Do(func() {
		// Close the terminal, kill what is behind it, reap it. Which step does the
		// work differs by platform (a ConPTY absorbs most of it into Close), hence three
		// interface calls.
		_ = s.pty.Close()
		_ = s.pty.Kill()
		// Reaped here, or the shell lingers as a zombie holding its descriptors.
		_ = s.pty.Wait()
		s.markExited()
		// Last, and unconditionally: this directory holds a kubeconfig with live
		// credentials.
		_ = os.RemoveAll(s.workdir)
		if s.release != nil {
			s.release()
		}
	})
}

// buildPrompt tells the operator which cluster they are on, in bash's PS1
// language, which sessionEnv exports. The tag and color come from promptTag
// (shell.go), shared with PowerShell and cmd.exe; the escapes are bash's:
// `\[…\]` marks a zero-width run for readline, and `\w` is the working
// directory.
func buildPrompt(name string, readOnly bool) string {
	tag, colour := promptTag(name, readOnly)
	return `\[\033[` + colour + `m\][` + tag + `]\[\033[0m\] \w $ `
}
