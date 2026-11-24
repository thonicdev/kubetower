package launch

import (
	"fmt"
	"log"
	"net/netip"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/forward"
	"github.com/thonicdev/kubetower/internal/web"
)

// A flow UI (Hubble UI, Whisker) in a window of its own, drawn by the desktop
// shell beside the console's, instead of in the default browser (#65).
//
// No build tag: what may open, what the window is called and when it closes
// are decided here, and tested on Linux. The Windows and macOS files only draw
// and close a window when told to, and say when the operator closed one.
//
//   - **Only a tunnel this process opened.** The page names a forward by its id
//     and the path to open; the address is built from the forward's own
//     loopback address and checked again against the live list
//     (toolWindowTarget). Nothing else ever reaches the window.
//   - **Never the console's session.** The window's web view keeps no cookie,
//     storage or cache with the console's (a separate WebView2 user data
//     folder, a non-persistent WKWebsiteDataStore), so the tool's origin is
//     sent nothing the console set, whatever the port.
//   - **It closes with what it shows.** Closing the window closes the tunnel
//     when the tunnel was opened for it and no other window shows it; a
//     tunnel that goes away, or a cluster that leaves the kubeconfig, closes
//     the window; quitting closes everything.
//   - **No web→native channel.** The page asks through the console's API, as
//     for a forward; the window's page can reach nothing in this process
//     (TestNoWebToNativeChannel).

// toolWindowTools are the tools a window may be opened for, and the name the
// title gives each.
var toolWindowTools = map[string]string{
	"hubble":  "Hubble UI",
	"whisker": "Whisker",
}

// maxToolWindowPath bounds the path a page may ask for.
const maxToolWindowPath = 1024

// maxToolWindows bounds how many tool windows may be open at once: each is a
// web view with its own engine processes.
const maxToolWindows = 8

// toolWindowSweepEvery is how often the open windows are checked against the
// live forwards and the cluster list.
const toolWindowSweepEvery = 2 * time.Second

// toolWindowTarget is one window to draw.
type toolWindowTarget struct {
	URL    string // what the window opens
	Origin string // scheme://host:port of the tunnel: the window keeps to it
	Title  string // "KubeTower · <cluster> · Hubble UI"
}

// toolWindowTitle is the window's title: the application, the cluster, the
// tool, so the window says who opened it and for what.
func toolWindowTitle(cluster, tool string) string {
	return "KubeTower · " + cluster + " · " + toolWindowTools[tool]
}

// toolWindowPath checks the path and query a page asks for: an absolute path
// on the tunnel, never an address of its own.
func toolWindowPath(path string) error {
	switch {
	case path == "":
		return nil
	case len(path) > maxToolWindowPath:
		return fmt.Errorf("%w: the path is longer than %d bytes", api.ErrInvalid, maxToolWindowPath)
	case !strings.HasPrefix(path, "/"), strings.HasPrefix(path, "//"), strings.ContainsAny(path, "\\#"):
		return fmt.Errorf("%w: the path must be an absolute path on the tunnel", api.ErrInvalid)
	}
	for _, r := range path {
		if r < 0x21 || r == 0x7f {
			return fmt.Errorf("%w: the path must be an absolute path on the tunnel", api.ErrInvalid)
		}
	}
	u, err := url.Parse(path)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return fmt.Errorf("%w: the path must be an absolute path on the tunnel", api.ErrInvalid)
	}
	return nil
}

// toolWindowAllowed is the allow-list: raw must be an http or https address,
// with no user information, on a loopback IP and the exact port of a live,
// healthy forward of this process, with that forward's scheme. It returns that
// forward.
//
// Every address a window is given passes it, at the moment it is drawn.
func toolWindowAllowed(raw string, live []forward.Tunnel) (forward.Tunnel, error) {
	refused := func(why string) (forward.Tunnel, error) {
		return forward.Tunnel{}, fmt.Errorf("%w: %s", api.ErrInvalid, why)
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return refused("not an address")
	case u.Scheme != "http" && u.Scheme != "https":
		return refused("only http and https open in a tool window")
	case u.User != nil:
		return refused("an address with user information does not open in a tool window")
	case u.Opaque != "":
		return refused("not an address")
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || !ip.IsLoopback() {
		return refused("only a loopback address opens in a tool window")
	}
	for _, t := range live {
		if !t.Healthy || t.LocalAddr != u.Host {
			continue
		}
		scheme := t.Scheme
		if scheme == "" {
			scheme = "http"
		}
		if scheme != u.Scheme {
			return refused("the tunnel at " + u.Host + " is " + scheme + ", not " + u.Scheme)
		}
		return t, nil
	}
	return refused("no tunnel of this console listens at " + u.Host)
}

// toolWindowHost is a platform's native side. Both calls come from any
// goroutine and return at once; the platform draws or closes the window on
// its own thread. When a window closes, by the operator or by closeToolWindow,
// the platform calls toolWindows.closed with its id.
type toolWindowHost interface {
	openToolWindow(id int, target toolWindowTarget)
	closeToolWindow(id int)
}

// tunnelSet is what the tool windows read of the forward set.
type tunnelSet interface {
	List() []forward.Tunnel
	Close(id string) bool
}

// toolWindowEntry is one open window.
type toolWindowEntry struct {
	forward string // the forward's id
	cluster string
	// closeTunnel is the page saying the forward was opened for this window:
	// closing the window then closes it, unless another window shows it.
	closeTunnel bool
}

// toolWindows is the desktop's tool windows: web.ToolWindows for the API, and
// the bookkeeping the platform's window reports to.
type toolWindows struct {
	forwards tunnelSet
	// known reports whether a cluster is still in the kubeconfig. Nil keeps
	// every cluster.
	known func(cluster string) bool

	mu   sync.Mutex
	host toolWindowHost // nil until the native window is drawn, and after
	next int
	open map[int]toolWindowEntry
}

func newToolWindows(forwards tunnelSet, known func(string) bool) *toolWindows {
	return &toolWindows{forwards: forwards, known: known, open: map[int]toolWindowEntry{}}
}

// attach is called by the native window once it is drawn, and with nil once it
// has closed: from then on no tool window opens.
func (t *toolWindows) attach(host toolWindowHost) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.host = host
	if host == nil {
		t.open = map[int]toolWindowEntry{}
	}
}

// Available reports whether a tool window can open: the console is in the
// desktop's own native window.
func (t *toolWindows) Available() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.host != nil
}

// errNoToolWindow is the refusal where no native window is drawn: the page
// opens the tunnel in the browser instead.
var errNoToolWindow = fmt.Errorf("%w: this console is not in the desktop window, so a tool opens in the browser", api.ErrConflict)

// OpenToolWindow opens the forward id's address at path in a tool window
// titled for tool.
func (t *toolWindows) OpenToolWindow(id, tool, path string, closeTunnel bool) error {
	if _, ok := toolWindowTools[tool]; !ok {
		return fmt.Errorf("%w: %q is not a tool that opens in a window", api.ErrInvalid, tool)
	}
	if err := toolWindowPath(path); err != nil {
		return err
	}
	if path == "" {
		path = "/"
	}
	live := t.forwards.List()
	var tunnel *forward.Tunnel
	for i := range live {
		if live[i].ID == id && live[i].Healthy {
			tunnel = &live[i]
			break
		}
	}
	if tunnel == nil {
		return fmt.Errorf("%w: no open port-forward %q", api.ErrNotFound, id)
	}
	scheme := tunnel.Scheme
	if scheme == "" {
		scheme = "http"
	}
	raw := scheme + "://" + tunnel.LocalAddr + path
	checked, err := toolWindowAllowed(raw, live)
	if err != nil {
		return err
	}
	target := toolWindowTarget{URL: raw, Origin: originOf(raw), Title: toolWindowTitle(checked.Cluster, tool)}

	t.mu.Lock()
	host := t.host
	if host == nil {
		t.mu.Unlock()
		return errNoToolWindow
	}
	if len(t.open) >= maxToolWindows {
		t.mu.Unlock()
		return fmt.Errorf("%w: %d tool windows are open; close one first", api.ErrConflict, maxToolWindows)
	}
	t.next++
	n := t.next
	t.open[n] = toolWindowEntry{forward: checked.ID, cluster: checked.Cluster, closeTunnel: closeTunnel}
	t.mu.Unlock()

	log.Printf("[kubetower] opening %s in a tool window (%s)", loggableURL(raw), target.Title)
	host.openToolWindow(n, target)
	return nil
}

// closed is the platform saying window n has closed. The tunnel goes with it
// when it was opened for the window and no other window shows it. A window
// already forgotten (closed by sweep) changes nothing.
func (t *toolWindows) closed(n int) {
	t.mu.Lock()
	closing := t.forget(n)
	t.mu.Unlock()
	t.closeTunnel(closing)
}

// forget drops window n and returns the forward to close, or "". Called with
// mu held.
func (t *toolWindows) forget(n int) string {
	entry, ok := t.open[n]
	if !ok {
		return ""
	}
	delete(t.open, n)
	if !entry.closeTunnel {
		return ""
	}
	for _, other := range t.open {
		if other.forward == entry.forward {
			return ""
		}
	}
	return entry.forward
}

func (t *toolWindows) closeTunnel(id string) {
	if id != "" && t.forwards.Close(id) {
		log.Printf("[kubetower] closed the port-forward %s with its tool window", id)
	}
}

// sweep closes every window whose forward is gone or whose cluster left the
// kubeconfig, and returns how many it closed.
func (t *toolWindows) sweep() int {
	live := map[string]bool{}
	for _, tunnel := range t.forwards.List() {
		live[tunnel.ID] = true
	}
	t.mu.Lock()
	host := t.host
	var gone []int
	var tunnels []string
	for n, entry := range t.open {
		if live[entry.forward] && (t.known == nil || t.known(entry.cluster)) {
			continue
		}
		gone = append(gone, n)
	}
	for _, n := range gone {
		tunnels = append(tunnels, t.forget(n))
	}
	t.mu.Unlock()

	for i, n := range gone {
		t.closeTunnel(tunnels[i])
		if host != nil {
			host.closeToolWindow(n)
		}
	}
	return len(gone)
}

// watch sweeps until done is closed.
func (t *toolWindows) watch(done <-chan struct{}, every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-tick.C:
			t.sweep()
		}
	}
}

// nativeToolWindows reports whether this platform's native window can draw a
// tool window: Windows and macOS, the two with a native window.
func nativeToolWindows(goos string) bool { return goos == "windows" || goos == "darwin" }

// startToolWindows is the tool windows, where a native window can be drawn
// and there are forwards to show; nil otherwise.
func startToolWindows(forwards web.Forwards, known func(string) bool) *toolWindows {
	set, ok := forwards.(tunnelSet)
	if !ok || set == nil || !nativeToolWindows(runtime.GOOS) {
		return nil
	}
	return newToolWindows(set, known)
}

// setupToolWindows is SetupInfo.ToolWindows: a nil interface, not a typed nil,
// where there are none.
func setupToolWindows(t *toolWindows) api.ToolWindows {
	if t == nil {
		return nil
	}
	return t
}

// toolNavigation decides a main-frame navigation in a tool window: it stays on
// the tunnel's own origin, or is the blank page a window starts on. Anything
// else is canceled, and an http or https address is handed to the default
// browser (handOut). Stricter than the console's window (nativewindownav.go):
// a tool's page is not this console's, so no other scheme loads in it.
func toolNavigation(origin, target string) (allow, handOut bool) {
	if target == "about:blank" {
		return true, false
	}
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false, false
	}
	if u.User == nil && u.Scheme+"://"+u.Host == origin {
		return true, false
	}
	return false, true
}

// The tool window's opening size, on Windows and macOS.
const (
	toolWindowWidth  = 1280
	toolWindowHeight = 820
)
