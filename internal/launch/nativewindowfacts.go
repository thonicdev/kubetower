package launch

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/update"
)

// What the macOS window knows about the rest of the process, and the decisions
// it takes from that: App Nap, the Dock badge, the ⌘Q question, the Cluster
// menu, and the script every menu item that reaches the page runs. No build
// tag, so they are tested on Linux; the darwin file only sends.
//
// Everything is read in-process, from the terminal registry, the forward set,
// the log streamer, the update checker and the cluster list: never through the
// console's HTTP API, which would need a session and be a second door to the
// same facts.

// windowSources is where the window reads what it shows. Functions, so a fact
// is read when needed rather than copied at start-up; each may be nil (the
// server profile has no forward set, a test passes only what it uses). Nil
// counts as nothing open and no update known, and for Clusters as a list that
// could not be read: an absent list is not an empty one.
type windowSources struct {
	// Terminals is every shell, pod exec, node shell and assistant session
	// open (`terminal.Terminal.Open`).
	Terminals func() int
	// Forwards is every port-forward open (`forward.Manager.List`).
	Forwards func() int
	// LogStreams is every log follow open (`web.Server.OpenLogStreams`).
	LogStreams func() int
	// Update is the desktop update check's last answer, read and never asked:
	// the requests are the checker's own.
	Update func() update.Report
	// Clusters is the console's own cluster list (`web.Server.Clusters`).
	Clusters func(context.Context) ([]api.Cluster, *api.Failure)
	// Tools is the tool windows (#65), which the native window attaches to
	// once drawn. Nil where none can open.
	Tools *toolWindows
}

func countOf(source func() int) int {
	if source == nil {
		return 0
	}
	if n := source(); n > 0 {
		return n
	}
	return 0
}

// openWork is what is open at one moment.
type openWork struct {
	Terminals, Forwards, LogStreams int
}

func (s windowSources) open() openWork {
	return openWork{
		Terminals:  countOf(s.Terminals),
		Forwards:   countOf(s.Forwards),
		LogStreams: countOf(s.LogStreams),
	}
}

// busy reports whether anything is open that a slowed process would degrade:
// a shell whose keystrokes lag, a forward whose connections stall, a log follow
// whose lines arrive in bursts. It is what the App Nap guard is held for.
func (o openWork) busy() bool { return o.Terminals+o.Forwards+o.LogStreams > 0 }

// --- ⌘Q -----------------------------------------------------------------------

// quitQuestion is what ⌘Q asks, and whether it asks at all.
//
// Nothing open, no question: a dialog that always appears gets clicked through.
//
// Terminals and port-forwards are counted, log streams are not: quitting ends
// the first two for good, while a log follow is re-read by reopening its panel,
// and counting it would teach the operator to dismiss the question.
//
// "terminal", not "shell": the count covers every dock session (local shell,
// pod exec, node shell, assistant), and the console already calls them
// terminals in its limit message.
func quitQuestion(open openWork) (message, detail string, ask bool) {
	var parts []string
	if open.Terminals > 0 {
		parts = append(parts, plural(open.Terminals, "terminal", "terminals"))
	}
	if open.Forwards > 0 {
		parts = append(parts, plural(open.Forwards, "port-forward", "port-forwards"))
	}
	if len(parts) == 0 {
		return "", "", false
	}
	return "Quit KubeTower?", strings.Join(parts, " and ") + " will close.", true
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// The alert's two buttons, in the order they are added: the first is the
// default (Return), and AppKit gives Escape to a button titled Cancel on its own.
const (
	quitButton   = "Quit"
	cancelButton = "Cancel"
	// alertFirstButtonReturn is NSAlertFirstButtonReturn (NSAlert.h): the
	// response a sheet's completion handler gets for the first button.
	alertFirstButtonReturn = 1000
)

// --- the Dock badge -----------------------------------------------------------

// updateBadge is the Dock icon's badge for the update check's last answer.
//
// Only Newer puts anything there: Off and NotApplicable asked nothing, Failed
// knows nothing, and a badge for those would claim an update nobody saw.
//
// "1" rather than the version: a Dock badge is a count everywhere on a Mac, and
// `0.5.0` would read as a number. The version and its link are on Settings ▸
// Infos.
func updateBadge(report update.Report) string {
	if report.State == update.Newer {
		return "1"
	}
	return ""
}

// --- App Nap ------------------------------------------------------------------

// The NSActivityOptions the guard is held with, from the named values in
// NSProcessInfo.h, so the arithmetic is checkable:
//
//	NSActivityIdleSystemSleepDisabled              = 1 << 20
//	NSActivityUserInitiated                        = 0x00FFFFFF | NSActivityIdleSystemSleepDisabled
//	NSActivityUserInitiatedAllowingIdleSystemSleep = NSActivityUserInitiated &^ NSActivityIdleSystemSleepDisabled
//
// Idle system sleep stays allowed: a shell or stream must keep its pace while
// the window is hidden, but a log panel left open must not keep a laptop awake.
const (
	activityIdleSystemSleepDisabled              uint64 = 1 << 20
	activityUserInitiated                        uint64 = 0x00FFFFFF | activityIdleSystemSleepDisabled
	activityUserInitiatedAllowingIdleSystemSleep uint64 = activityUserInitiated &^ activityIdleSystemSleepDisabled
)

// activityReason is the reason string the guard is held under, which is what
// `pmset -g assertions` and Activity Monitor show for it.
const activityReason = "KubeTower has a terminal, a port-forward or a log stream open"

// --- what the window is told, and when -----------------------------------------

// windowFacts is what the window applies when it is refreshed: the badge and
// whether the App Nap guard is held. Comparable, so the watcher can tell a
// change from a repeat.
//
// Newer is a fact of its own because the badge reads "1" for any newer release:
// without it a second newer release would change nothing compared, and its
// notification would never be asked for.
type windowFacts struct {
	Badge string
	Newer string
	Busy  bool
}

func (s windowSources) facts() windowFacts {
	f := windowFacts{Busy: s.open().busy()}
	if s.Update != nil {
		report := s.Update()
		f.Badge = updateBadge(report)
		if report.State == update.Newer {
			f.Newer = report.Latest
		}
	}
	return f
}

// windowFactsNow is the last facts the watcher published, read by the window on
// the main thread; nil, before the first, means nothing to apply yet.
var windowFactsNow atomic.Pointer[windowFacts]

// windowWatchEvery is how often the watcher looks.
//
// A poll is enough: telling the window from inside each package would put a
// macOS concern into internal/terminal, forward and logs, while polling costs
// three mutexes every two seconds. The lateness is harmless: the guard is taken
// up to two seconds after the first shell opens (from the window, which was not
// napping) and released up to two seconds after the last closes. App Nap may
// slow the poll only when nothing is open, when punctuality does not matter.
const windowWatchEvery = 2 * time.Second

// watchWindowFacts publishes the facts whenever they change, and once at the
// start, until done is closed. publish is refreshNativeWindow in production.
//
// It reads the update checker's answer rather than being told, so the start-up
// check, which may answer before or after the window is up, needs no hook, and
// nothing here can make a request.
func watchWindowFacts(done <-chan struct{}, sources windowSources, every time.Duration, publish func()) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	var last *windowFacts
	for {
		now := sources.facts()
		if last == nil || *last != now {
			last = &now
			windowFactsNow.Store(&now)
			publish()
		}
		select {
		case <-done:
			return
		case <-ticker.C:
		}
	}
}

// --- the Cluster menu ---------------------------------------------------------

// clusterMenuItem is one line of the Cluster menu.
type clusterMenuItem struct {
	Title string
	// ToolTip is the context's own name when the title is the operator's
	// nameplate for it, so the real name is one hover away.
	ToolTip string
	// Path is where the item goes: `/c/<cluster>`, spelt as api.ClusterPath
	// spells it. Empty on a disabled line.
	Path string
	// Key is the ⌘ key equivalent, "1" to "9", or empty.
	Key string
	// Separator is a line between the rail's clusters and the hidden ones.
	Separator bool
	// Enabled is false on the line that says why there is nothing to choose.
	Enabled bool
}

// The two sentences an empty Cluster menu can say: "no cluster" sends the
// operator to write a kubeconfig, "could not be read" to fix the one they have.
const (
	clusterMenuEmpty      = "No cluster in the kubeconfig"
	clusterMenuUnreadable = "The clusters could not be read"
)

// clusterMenu is the Cluster menu for the console's own cluster list.
//
// The rail's order, sorted as `arrange` in web/ui/src/shell/clusterState.ts
// (hotbar, position within it, label), so ⌘1…⌘9 are the first nine tiles. The
// label tie-break here is byte order where the rail's is localeCompare, which
// only matters between unpositioned tiles whose labels differ in case or
// accents.
//
// Hidden clusters are listed after a separator with no shortcut: they are off
// the rail, not out of the kubeconfig, and a number is a position they lack.
// From the tenth on there is no shortcut either; ⌘0 is View ▸ Actual Size.
//
// A failure is one disabled line whose tooltip carries the console's sentence;
// an empty list with no failure is the other sentence.
func clusterMenu(list []api.Cluster, failure *api.Failure) []clusterMenuItem {
	if failure != nil {
		return []clusterMenuItem{{Title: clusterMenuUnreadable, ToolTip: failure.Reason.Message}}
	}
	if len(list) == 0 {
		return []clusterMenuItem{{Title: clusterMenuEmpty}}
	}

	arranged := append([]api.Cluster(nil), list...)
	sort.SliceStable(arranged, func(i, j int) bool {
		a, b := arranged[i], arranged[j]
		if ba, bb := barKey(a), barKey(b); ba != bb {
			return ba < bb
		}
		if oa, ob := orderKey(a), orderKey(b); oa != ob {
			return oa < ob
		}
		return a.Label < b.Label
	})

	var shown, hidden []api.Cluster
	for _, c := range arranged {
		if c.Hidden {
			hidden = append(hidden, c)
		} else {
			shown = append(shown, c)
		}
	}

	items := make([]clusterMenuItem, 0, len(list)+1)
	for i, c := range shown {
		item := clusterItem(c)
		if i < 9 {
			item.Key = strconv.Itoa(i + 1)
		}
		items = append(items, item)
	}
	if len(shown) > 0 && len(hidden) > 0 {
		items = append(items, clusterMenuItem{Separator: true})
	}
	for _, c := range hidden {
		items = append(items, clusterItem(c))
	}
	return items
}

func clusterItem(c api.Cluster) clusterMenuItem {
	item := clusterMenuItem{Title: c.Label, Path: api.ClusterPath(c.Name), Enabled: true}
	if item.Title == "" {
		item.Title = c.Name
	}
	if item.Title != c.Name {
		item.ToolTip = c.Name
	}
	return item
}

// barKey and orderKey are `arrange`'s `?? Number.MAX_SAFE_INTEGER`: a tile with
// no bar sorts after every bar, and one with no position after every position.
func barKey(c api.Cluster) int {
	if c.Bar <= 0 {
		return int(^uint(0) >> 1)
	}
	return c.Bar
}

func orderKey(c api.Cluster) int {
	if c.Order == nil {
		return int(^uint(0) >> 1)
	}
	return *c.Order
}

// clusterMenuTimeout bounds the menu's one read of the cluster list, which is
// answered from memory: a bound on a defect, not an expected wait.
const clusterMenuTimeout = 5 * time.Second

// readClusterMenu reads the list once and turns it into menu lines. A nil
// source is a list that could not be read, not an empty one.
func readClusterMenu(source func(context.Context) ([]api.Cluster, *api.Failure)) []clusterMenuItem {
	if source == nil {
		return clusterMenu(nil, api.Absent("this window was given no cluster list"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), clusterMenuTimeout)
	defer cancel()
	return clusterMenu(source(ctx))
}

// --- the one native→web mechanism ----------------------------------------------

// navigateScript is what a menu item that reaches the console runs in the page:
// a route change inside the SPA to `path`, which must be absolute and carry no
// origin.
// A route change, not a page load: reloading would rebuild the page and close
// every terminal and log stream in the dock. Pushing the path and firing
// popstate is what the back button does, React Router answers it, and the dock,
// outside the router's outlet, survives.
//
// Nothing happens when the page is already there or under it (Settings ▸
// General for ⌘,; a pod list for its cluster's ⌘ digit), compared by segment so
// `/c/prod` is not under `/c/prod-eu`.
//
// The path is absolute with no origin: the window's address carries a one-time
// sign-in token and must not be glued together by string. It goes in as a JSON
// string, whose encoder escapes `<`, `>`, `&`, U+2028 and U+2029, so no cluster
// name can end the literal or the script.
//
// On a page that is not the SPA (the server-rendered sign-in page, no #root)
// the address is loaded instead; there is nothing there to lose.
func navigateScript(path string) string {
	return routeScript(path, `here === path || here.indexOf(path + '/') === 0`)
}

// openPageScript is what a `kubetower://` link runs in the page
// (nativewindowlink.go): the same route change as navigateScript, except that a
// link goes to the page it names even from under it, as the same link clicked
// in the console would. A menu item is a place to go back to; a link is a page.
func openPageScript(path string) string {
	return routeScript(path, `here === path`)
}

// routeScript is the one native→web mechanism, parameterized by when the page
// counts as already there. `there` is a JavaScript condition over `here` and
// `path`, and is one of the two constants above, never composed from input.
func routeScript(path, there string) string {
	literal, err := json.Marshal(path)
	if err != nil {
		// A Go string always marshals.
		literal = []byte(`"/"`)
	}
	return fmt.Sprintf(`(function () {
  var path = %s;
  if (!document.getElementById('root')) { location.assign(path); return; }
  var here = location.pathname;
  if (%s) return;
  history.pushState(null, '', path);
  dispatchEvent(new PopStateEvent('popstate', { state: null }));
})();`, literal, there)
}

// showSettingsScript is what the ⌘, menu item runs in the page.
//
// `/settings` rather than `/settings/general`: the router redirects the first,
// and it survives the tabs being renamed.
var showSettingsScript = navigateScript("/settings")

// wakeEvent is the DOM event the window dispatches on the page's `window` when
// the Mac wakes from sleep (NSWorkspaceDidWakeNotification), through
// evaluateJavaScript like every native→web message.
//
// Nothing in the console listens for it, because nothing needs to: a log follow
// that drops reconnects by itself (web/ui/src/api/logstream.ts), and the
// browser-to-server half is loopback, which sleep does not cut; a follow the
// server ended stays ended by design; a terminal's process is gone with its
// socket; the lists refetch on focus. So the window says the Mac woke, once, in
// a form a page change can listen for without native code, and logs a line at
// the same moment.
const wakeEvent = "kubetower:wake"

// wakeScript is the script that dispatches `wakeEvent`.
var wakeScript = fmt.Sprintf(`dispatchEvent(new Event(%q));`, wakeEvent)
