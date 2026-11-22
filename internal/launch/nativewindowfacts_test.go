package launch

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/update"
)

// The decisions behind the macOS window's ⌘Q question, Dock badge, App Nap
// guard, Cluster menu and page scripts. They are in
// `nativewindowfacts.go`, with no build tag, so these run on the Linux CI that
// cannot compile the window itself. Nothing here opens a window.

// ⌘Q asks only when something would be lost, and says how much.
func TestTheQuitQuestionCountsTerminalsAndForwards(t *testing.T) {
	for _, c := range []struct {
		open   openWork
		ask    bool
		detail string
	}{
		// Nothing open: no question at all.
		{openWork{}, false, ""},
		// A log follow loses nothing by quitting, so it alone asks nothing.
		{openWork{LogStreams: 4}, false, ""},
		{openWork{Terminals: 1}, true, "1 terminal will close."},
		{openWork{Terminals: 2, Forwards: 1}, true, "2 terminals and 1 port-forward will close."},
		{openWork{Forwards: 3}, true, "3 port-forwards will close."},
		{openWork{Terminals: 1, Forwards: 2, LogStreams: 5}, true, "1 terminal and 2 port-forwards will close."},
	} {
		message, detail, ask := quitQuestion(c.open)
		if ask != c.ask || detail != c.detail {
			t.Errorf("quitQuestion(%+v) = (%q, %v), want (%q, %v)", c.open, detail, ask, c.detail, c.ask)
		}
		if ask && message != "Quit KubeTower?" {
			t.Errorf("quitQuestion(%+v) message = %q", c.open, message)
		}
		if !ask && (message != "" || detail != "") {
			t.Errorf("quitQuestion(%+v) asks nothing but says %q / %q", c.open, message, detail)
		}
	}
	// The first button added is the one NSAlertFirstButtonReturn answers for.
	if quitButton != "Quit" || cancelButton != "Cancel" || alertFirstButtonReturn != 1000 {
		t.Errorf("buttons %q/%q, first return %d", quitButton, cancelButton, alertFirstButtonReturn)
	}
}

// What is open is read from the sources, and a missing or nonsensical source
// counts as nothing rather than panicking or going negative.
func TestTheWindowCountsWhatIsOpenInProcess(t *testing.T) {
	if got := (windowSources{}).open(); got != (openWork{}) || got.busy() {
		t.Errorf("no sources: %+v busy=%v, want nothing open", got, got.busy())
	}
	sources := windowSources{
		Terminals:  func() int { return 2 },
		Forwards:   func() int { return -1 },
		LogStreams: func() int { return 0 },
	}
	got := sources.open()
	if got != (openWork{Terminals: 2}) || !got.busy() {
		t.Errorf("open() = %+v busy=%v, want 2 terminals and busy", got, got.busy())
	}
	if !(openWork{LogStreams: 1}).busy() {
		t.Error("a log follow alone does not hold off App Nap; it is one of the three things the guard is for")
	}
}

// The badge appears for a newer release and for nothing else — least of all
// for a check that failed.
func TestTheDockBadgeIsOnlyForANewerRelease(t *testing.T) {
	for state, want := range map[update.State]string{
		update.Newer:         "1",
		update.Current:       "",
		update.Failed:        "",
		update.Off:           "",
		update.NotApplicable: "",
		"":                   "",
	} {
		if got := updateBadge(update.Report{State: state, Latest: "9.9.9"}); got != want {
			t.Errorf("updateBadge(%q) = %q, want %q", state, got, want)
		}
	}
	newer := windowSources{Update: func() update.Report { return update.Report{State: update.Newer} }}
	if f := newer.facts(); f.Badge != "1" {
		t.Errorf("facts() with a newer release: badge %q", f.Badge)
	}
	if f := (windowSources{}).facts(); f.Badge != "" {
		t.Errorf("facts() with no update source: badge %q", f.Badge)
	}
}

// NSActivityUserInitiatedAllowingIdleSystemSleep, from the header's own
// definitions: user-initiated, minus keeping the system awake.
func TestTheAppNapGuardAllowsIdleSystemSleep(t *testing.T) {
	if activityUserInitiatedAllowingIdleSystemSleep != 0x00EFFFFF {
		t.Errorf("options = %#x, want 0x00EFFFFF", activityUserInitiatedAllowingIdleSystemSleep)
	}
	if activityUserInitiatedAllowingIdleSystemSleep&activityIdleSystemSleepDisabled != 0 {
		t.Error("the guard would keep the Mac awake: NSActivityIdleSystemSleepDisabled is set")
	}
	if activityReason == "" {
		t.Error("no reason: `pmset -g assertions` would list the activity with nothing beside it")
	}
}

func position(n int) *int { return &n }

// Empty and unreadable are two different sentences, and both are one disabled
// line.
func TestAnEmptyClusterMenuAndAnUnreadableOneSayDifferentThings(t *testing.T) {
	empty := clusterMenu(nil, nil)
	if len(empty) != 1 || empty[0].Title != clusterMenuEmpty || empty[0].Enabled || empty[0].Path != "" {
		t.Errorf("empty list: %+v", empty)
	}
	if again := clusterMenu([]api.Cluster{}, nil); len(again) != 1 || again[0].Title != clusterMenuEmpty {
		t.Errorf("empty, non-nil list: %+v", again)
	}

	failure := api.Fail(api.StateUnreadable, api.CodeUndecodable, "~/.kube/config could not be read")
	unreadable := clusterMenu([]api.Cluster{{Name: "ignored"}}, failure)
	if len(unreadable) != 1 || unreadable[0].Title != clusterMenuUnreadable || unreadable[0].Enabled {
		t.Errorf("unreadable list: %+v", unreadable)
	}
	if unreadable[0].ToolTip != "~/.kube/config could not be read" {
		t.Errorf("the unreadable line does not carry the console's own sentence: %q", unreadable[0].ToolTip)
	}
	if clusterMenuEmpty == clusterMenuUnreadable {
		t.Error("the two sentences are one")
	}

	// No source at all is not an empty kubeconfig.
	if none := readClusterMenu(nil); len(none) != 1 || none[0].Title != clusterMenuUnreadable {
		t.Errorf("no source: %+v", none)
	}
	// A source is called, and with a deadline.
	var hadDeadline bool
	read := readClusterMenu(func(ctx context.Context) ([]api.Cluster, *api.Failure) {
		_, hadDeadline = ctx.Deadline()
		return []api.Cluster{{Name: "prod", Label: "prod"}}, nil
	})
	if len(read) != 1 || read[0].Path != "/c/prod" || !hadDeadline {
		t.Errorf("readClusterMenu: %+v, deadline %v", read, hadDeadline)
	}
}

// The rail's order; ⌘1…⌘9 for its first nine tiles; the tenth and after with
// no shortcut; hidden clusters after a separator, unnumbered.
func TestTheClusterMenuFollowsTheRail(t *testing.T) {
	var list []api.Cluster
	// Eleven on bar 1, given in reverse so that sorting is what orders them.
	for i := 10; i >= 0; i-- {
		name := "c" + strconv.Itoa(i)
		list = append(list, api.Cluster{Name: name, Label: name, Bar: 1, Order: position(i)})
	}
	list = append(list,
		// Bar 2 sorts after every tile of bar 1, whatever its order.
		api.Cluster{Name: "second-bar", Label: "second-bar", Bar: 2, Order: position(0)},
		// No position on a bar: after the positioned ones, by label.
		api.Cluster{Name: "zz", Label: "Beta", Bar: 1},
		api.Cluster{Name: "aa", Label: "Alpha", Bar: 1},
		// Hidden: listed last, after a separator, no shortcut.
		api.Cluster{Name: "arn:aws:eks:eu-west-1:1:cluster/old", Label: "Old", Hidden: true},
	)

	items := clusterMenu(list, nil)
	var titles, keys []string
	for _, item := range items {
		if item.Separator {
			titles = append(titles, "—")
			keys = append(keys, "")
			continue
		}
		titles = append(titles, item.Title)
		keys = append(keys, item.Key)
		if !item.Enabled {
			t.Errorf("%q is disabled", item.Title)
		}
	}
	wantTitles := []string{"c0", "c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "c9", "c10",
		"Alpha", "Beta", "second-bar", "—", "Old"}
	wantKeys := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "", "", "", "", "", "", ""}
	if strings.Join(titles, ",") != strings.Join(wantTitles, ",") {
		t.Errorf("order:\n got %v\nwant %v", titles, wantTitles)
	}
	if strings.Join(keys, ",") != strings.Join(wantKeys, ",") {
		t.Errorf("keys:\n got %v\nwant %v", keys, wantKeys)
	}

	last := items[len(items)-1]
	if last.Path != "/c/arn:aws:eks:eu-west-1:1:cluster%2Fold" {
		t.Errorf("hidden cluster path = %q", last.Path)
	}
	// The nameplate is the title; the real name is the tooltip.
	if last.ToolTip != "arn:aws:eks:eu-west-1:1:cluster/old" {
		t.Errorf("a renamed cluster's tooltip = %q, want its context name", last.ToolTip)
	}
	if items[0].ToolTip != "" {
		t.Errorf("a cluster titled by its own name carries a tooltip %q", items[0].ToolTip)
	}

	// Exactly nine shortcuts, and no ⌘0 (View ▸ Actual Size).
	shortcuts := 0
	for _, item := range items {
		if item.Key != "" {
			shortcuts++
		}
		if item.Key == "0" {
			t.Error("⌘0 is Actual Size")
		}
	}
	if shortcuts != 9 {
		t.Errorf("%d shortcuts, want 9", shortcuts)
	}

	// Only hidden clusters: no separator before them, and no numbers.
	onlyHidden := clusterMenu([]api.Cluster{{Name: "h", Label: "h", Hidden: true}}, nil)
	if len(onlyHidden) != 1 || onlyHidden[0].Separator || onlyHidden[0].Key != "" {
		t.Errorf("only hidden: %+v", onlyHidden)
	}
	// A cluster with no label is titled by its name.
	if bare := clusterMenu([]api.Cluster{{Name: "bare"}}, nil); bare[0].Title != "bare" || bare[0].Key != "1" {
		t.Errorf("no label: %+v", bare)
	}
}

// A menu item goes to the address the console itself writes for the cluster:
// the cases `internal/web` and `web/ui/src/clusters/clusterPath.ts` are both held
// to.
func TestTheClusterMenuSpellsPathsAsTheConsoleDoes(t *testing.T) {
	raw, err := os.ReadFile("../../web/ui/src/clusters/clusterPath.cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct{ Name, Path string }
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 10 {
		t.Fatalf("%d cases read, want the shared table", len(cases))
	}
	for _, c := range cases {
		items := clusterMenu([]api.Cluster{{Name: c.Name, Label: c.Name}}, nil)
		if items[0].Path != c.Path {
			t.Errorf("%q: path %q, want %q", c.Name, items[0].Path, c.Path)
		}
	}
}

// One script shape for every native→web message: ⌘, and the Cluster menu are
// the same function, and no cluster name can break out of it.
func TestTheNavigationScriptIsOneMechanism(t *testing.T) {
	if showSettingsScript != navigateScript("/settings") {
		t.Error("⌘, does not use navigateScript")
	}
	for _, want := range []string{
		`var path = "/settings";`,
		"history.pushState(null, '', path);",
		"new PopStateEvent('popstate'",
		// Segment-wise, so /c/prod is not "already under" /c/prod-eu.
		"here.indexOf(path + '/') === 0",
		// The sign-in page has no router.
		"location.assign(path)",
	} {
		if !strings.Contains(showSettingsScript, want) {
			t.Errorf("the script lacks %q:\n%s", want, showSettingsScript)
		}
	}

	hostile := navigateScript("/c/a\"b'</script>\u2028x")
	if !strings.Contains(hostile, `var path = "/c/a\"b'\u003c/script\u003e\u2028x";`) {
		t.Errorf("a hostile path is not one escaped literal:\n%s", hostile)
	}

	if !strings.Contains(wakeScript, `new Event("kubetower:wake")`) || wakeEvent != "kubetower:wake" {
		t.Errorf("wake script = %q", wakeScript)
	}
}

// The watcher publishes at the start, on every change and never on a repeat,
// and stops when told.
func TestTheWatcherPublishesChangesOnly(t *testing.T) {
	var terminals, published atomic.Int32
	sources := windowSources{Terminals: func() int { return int(terminals.Load()) }}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		watchWindowFacts(done, sources, time.Millisecond, func() { published.Add(1) })
	}()

	waitUntil := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(time.Millisecond)
		}
	}

	waitUntil("the first publication", func() bool { return published.Load() == 1 })
	if f := windowFactsNow.Load(); f == nil || f.Busy {
		t.Fatalf("first facts = %+v, want published and not busy", f)
	}
	time.Sleep(20 * time.Millisecond)
	if n := published.Load(); n != 1 {
		t.Fatalf("%d publications with nothing changed, want 1", n)
	}

	terminals.Store(1)
	waitUntil("the change to busy", func() bool { return published.Load() == 2 })
	if f := windowFactsNow.Load(); !f.Busy {
		t.Fatalf("facts after a terminal opened = %+v, want busy", f)
	}

	terminals.Store(0)
	waitUntil("the change back", func() bool { return published.Load() == 3 })

	close(done)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher did not stop")
	}
}

// A second newer release - 0.3.0 known, then 0.4.0 - is a change the watcher
// publishes, and the refresh it asks for delivers a second notification. The
// badge alone reads "1" for both, so before the release was a fact of its own
// nothing was published and the notice for 0.4.0 was never asked for.
func TestASecondNewerReleaseIsPublishedAndNotified(t *testing.T) {
	saved := version
	version = "0.2.1"
	t.Cleanup(func() { version = saved })

	var latest atomic.Pointer[string]
	first := "0.3.0"
	latest.Store(&first)
	sources := windowSources{Update: func() update.Report {
		return update.Report{State: update.Newer, Latest: *latest.Load()}
	}}

	// What `kubeTowerRefresh:` does on the Mac: apply the facts, then ask for
	// the notices, with the application not in front.
	n := enabledNotices()
	var mu sync.Mutex
	var delivered []notice
	var published atomic.Int32
	publish := func() {
		out, _, _ := n.take(noticeEpoch, false, sources.Update())
		mu.Lock()
		delivered = append(delivered, out...)
		mu.Unlock()
		published.Add(1)
	}

	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		watchWindowFacts(done, sources, time.Millisecond, publish)
	}()
	t.Cleanup(func() {
		close(done)
		<-stopped
	})

	waitUntil := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(time.Millisecond)
		}
	}
	titles := func() []string {
		mu.Lock()
		defer mu.Unlock()
		var out []string
		for _, note := range delivered {
			out = append(out, note.Title)
		}
		return out
	}

	waitUntil("the first publication", func() bool { return published.Load() == 1 })
	if got := titles(); len(got) != 1 || got[0] != "KubeTower 0.3.0 is available" {
		t.Fatalf("after the first release, delivered %q", got)
	}

	second := "0.4.0"
	latest.Store(&second)
	waitUntil("a publication for the second release", func() bool { return published.Load() == 2 })
	if f := windowFactsNow.Load(); f == nil || f.Badge != "1" || f.Newer != "0.4.0" {
		t.Fatalf("facts after the second release = %+v, want badge 1 and newer 0.4.0", f)
	}
	if got := titles(); len(got) != 2 || got[1] != "KubeTower 0.4.0 is available" {
		t.Fatalf("after the second release, delivered %q, want a second notice for 0.4.0", got)
	}

	time.Sleep(20 * time.Millisecond)
	if n := published.Load(); n != 2 {
		t.Errorf("%d publications with the release unchanged, want 2", n)
	}
}
