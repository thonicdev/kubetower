package launch

// Which events become a macOS notification, what it says,
// when it is sent and where a click goes. Every platform runs these; the darwin
// file only posts what `take` returns.
//
// "A forward the operator closed produces nothing" is decided one layer down,
// where the forward set is: `TestATunnelTheOperatorClosedIsNeverLost` and
// `TestTunnelsClosedOnShutdownAreNeverLost` in internal/forward hold that
// `Options.Lost` - which is `forwardLostNotice` here - is never called for one.
// What is held here is that nothing reaches the screen without that call.

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/thonicdev/kubetower/internal/forward"
	"github.com/thonicdev/kubetower/internal/update"
)

var noticeEpoch = time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)

func enabledNotices() *windowNotices {
	n := &windowNotices{}
	n.enabled.Store(true)
	return n
}

func webTunnel() forward.Tunnel {
	return forward.Tunnel{
		ID: "a1b2c3", Cluster: "dev-cluster", Namespace: "shop", Kind: "service", Name: "web",
		Target: "service/web", Port: 80, PodPort: 8080, Pod: "web-7f9c", LocalPort: 51423,
		LocalAddr: "127.0.0.1:51423", Scheme: "http", Opened: noticeEpoch, Age: "3m",
		Error: "lost connection to pod",
	}
}

// pathOf is where a notice's click goes: its link through the validator a
// click meets, which must accept it.
func pathOf(t *testing.T, n notice) string {
	t.Helper()
	path, err := consolePathFromURL(n.Link)
	if err != nil {
		t.Fatalf("the notice's link %q is refused by consolePathFromURL: %v", n.Link, err)
	}
	return path
}

func onlyNotice(t *testing.T, out []notice) notice {
	t.Helper()
	if len(out) != 1 {
		t.Fatalf("want exactly one notification, got %d: %+v", len(out), out)
	}
	return out[0]
}

func checkBounds(t *testing.T, n notice) {
	t.Helper()
	if c := utf8.RuneCountInString(n.Title); c > noticeTitleMax {
		t.Errorf("title is %d characters, more than %d: %q", c, noticeTitleMax, n.Title)
	}
	if c := utf8.RuneCountInString(n.Body); c > noticeBodyMax {
		t.Errorf("body is %d characters, more than %d: %q", c, noticeBodyMax, n.Body)
	}
	if !utf8.ValidString(n.Title) || !utf8.ValidString(n.Body) {
		t.Errorf("a bound cut a character in half: %q / %q", n.Title, n.Body)
	}
}

// --- the update -----------------------------------------------------------------

func TestANewerReleaseIsNotifiedOnceWhileTheWindowIsNotActive(t *testing.T) {
	saved := version
	version = "0.4.2"
	t.Cleanup(func() { version = saved })

	n := enabledNotices()
	report := update.Report{State: update.Newer, Latest: "0.5.0"}

	out, _, again := n.take(noticeEpoch, false, report)
	note := onlyNotice(t, out)
	if note.Title != "KubeTower 0.5.0 is available" {
		t.Errorf("title %q", note.Title)
	}
	if note.Body != "You are on 0.4.2. Settings ▸ Infos links to its release page." {
		t.Errorf("body %q", note.Body)
	}
	if note.ID != noticeUpdateID {
		t.Errorf("id %q", note.ID)
	}
	if got := pathOf(t, note); got != "/settings/infos" {
		t.Errorf("a click goes to %q, want Settings ▸ Infos", got)
	}
	checkBounds(t, note)
	if again != 0 {
		t.Errorf("asked to look again in %s with nothing waiting", again)
	}

	if out, _, _ := n.take(noticeEpoch.Add(time.Hour), false, report); len(out) != 0 {
		t.Errorf("the same release was notified twice: %+v", out)
	}
	out, _, _ = n.take(noticeEpoch.Add(2*time.Hour), false, update.Report{State: update.Newer, Latest: "0.6.0"})
	if note := onlyNotice(t, out); note.Title != "KubeTower 0.6.0 is available" {
		t.Errorf("a second, newer release says %q", note.Title)
	}
}

func TestANewerReleaseWaitsWhileTheWindowIsActive(t *testing.T) {
	n := enabledNotices()
	report := update.Report{State: update.Newer, Latest: "0.5.0"}

	out, lines, _ := n.take(noticeEpoch, true, report)
	if len(out) != 0 {
		t.Fatalf("notified while the window was in front: %+v", out)
	}
	if len(lines) != 1 {
		t.Errorf("want one line saying the notification waits, got %q", lines)
	}
	if _, lines, _ := n.take(noticeEpoch.Add(time.Second), true, report); len(lines) != 0 {
		t.Errorf("the wait was logged again: %q", lines)
	}
	out, _, _ = n.take(noticeEpoch.Add(time.Minute), false, report)
	onlyNotice(t, out)
}

func TestOnlyANewerReleaseIsNotified(t *testing.T) {
	for _, state := range []update.State{update.Current, update.Off, update.NotApplicable, update.Failed} {
		n := enabledNotices()
		if out, _, _ := n.take(noticeEpoch, false, update.Report{State: state, Latest: "0.5.0"}); len(out) != 0 {
			t.Errorf("%s produced a notification: %+v", state, out)
		}
	}
}

// The release feed is GitHub's answer, not this process's. Whatever it names
// that is not shaped like a version is not said.
func TestAVersionThatIsNotShapedLikeOneIsNotSaid(t *testing.T) {
	saved := version
	version = devVersion
	t.Cleanup(func() { version = saved })

	for _, latest := range []string{"<b>0.5.0</b>", "0.5.0\nclick here", strings.Repeat("9", 80), "v0.5.0"} {
		n := enabledNotices()
		out, _, _ := n.take(noticeEpoch, false, update.Report{State: update.Newer, Latest: latest})
		note := onlyNotice(t, out)
		if note.Title != "A newer KubeTower is available" || strings.Contains(note.Body, "You are on") {
			t.Errorf("for Latest %q the notification says %q / %q", latest, note.Title, note.Body)
		}
	}
}

// --- a lost forward ---------------------------------------------------------------

func TestALostServiceForwardIsSaidByNameAndOpensItsPage(t *testing.T) {
	n := enabledNotices()
	if !n.forwardLost(webTunnel(), noticeEpoch) {
		t.Fatal("an enabled window refused the event")
	}

	out, _, again := n.take(noticeEpoch, false, update.Report{})
	if len(out) != 0 || again != noticeSettle {
		t.Fatalf("released before the settle (%d notices, again %s)", len(out), again)
	}
	out, lines, _ := n.take(noticeEpoch.Add(noticeSettle), false, update.Report{})
	note := onlyNotice(t, out)
	if note.Title != "Port-forward lost: service/web" {
		t.Errorf("title %q", note.Title)
	}
	if note.Body != "Stopped without being closed: local port 51423 to 80, namespace shop on dev-cluster." {
		t.Errorf("body %q", note.Body)
	}
	if !strings.HasPrefix(note.ID, noticeForwardIDPrefix) {
		t.Errorf("id %q", note.ID)
	}
	if got := pathOf(t, note); got != "/c/dev-cluster/services/shop/web" {
		t.Errorf("a click goes to %q", got)
	}
	checkBounds(t, note)
	if len(lines) != 0 {
		t.Errorf("lines for a notice that is posted, which the poster logs: %q", lines)
	}
}

func TestALostPodForwardOpensThePodsPage(t *testing.T) {
	n := enabledNotices()
	pod := webTunnel()
	pod.Kind, pod.Name, pod.Target = "pod", "web-7f9c", "pod/web-7f9c"
	n.forwardLost(pod, noticeEpoch)
	out, _, _ := n.take(noticeEpoch.Add(noticeSettle), false, update.Report{})
	note := onlyNotice(t, out)
	if got := pathOf(t, note); got != "/c/dev-cluster/pods/shop/web-7f9c" {
		t.Errorf("a click goes to %q", got)
	}
	if note.Title != "Port-forward lost: pod/web-7f9c" {
		t.Errorf("title %q", note.Title)
	}
}

// What a forward notification may say is `lostForward` and nothing else. Every
// field of `forward.Tunnel` that is not one of those is given a marker, and no
// marker may reach the title, the body or the link - including a field added to
// the tunnel later.
func TestNoFieldButANameOrAPortReachesTheText(t *testing.T) {
	allowed := map[string]bool{
		"Cluster": true, "Namespace": true, "Kind": true, "Name": true, "LocalPort": true, "Port": true,
	}
	// `lostForward` holds exactly the allowed fields: a field added to it is a
	// decision to say something more, and has to be added here on purpose.
	var got []string
	lf := reflect.TypeOf(lostForward{})
	for i := range lf.NumField() {
		got = append(got, lf.Field(i).Name)
		if !allowed[lf.Field(i).Name] {
			t.Errorf("lostForward carries %s, which is not a name or a port", lf.Field(i).Name)
		}
	}
	if len(got) != len(allowed) {
		t.Errorf("lostForward has fields %v, want exactly %d", got, len(allowed))
	}

	tunnel := webTunnel()
	v := reflect.ValueOf(&tunnel).Elem()
	var markers []string
	for i := range v.NumField() {
		field := v.Type().Field(i)
		if allowed[field.Name] {
			continue
		}
		marker := fmt.Sprintf("LEAK%dLEAK", 100000+i)
		switch v.Field(i).Kind() {
		case reflect.String:
			v.Field(i).SetString(marker)
		case reflect.Int:
			v.Field(i).SetInt(int64(100000 + i))
			marker = fmt.Sprint(100000 + i)
		default:
			continue // a time or a bool cannot spell anything in the text
		}
		markers = append(markers, marker)
	}
	if len(markers) < 6 {
		t.Fatalf("only %d fields marked; the walk over forward.Tunnel is not seeing it", len(markers))
	}

	n := enabledNotices()
	n.forwardLost(tunnel, noticeEpoch)
	note := onlyNotice(t, func() []notice {
		out, _, _ := n.take(noticeEpoch.Add(noticeSettle), false, update.Report{})
		return out
	}())
	for _, m := range markers {
		for what, text := range map[string]string{"title": note.Title, "body": note.Body, "link": note.Link} {
			if strings.Contains(text, m) {
				t.Errorf("the %s carries %s, from a field that is not a name or a port: %q", what, m, text)
			}
		}
	}
}

func TestANameCannotBreakALineOrReorderTheText(t *testing.T) {
	n := enabledNotices()
	tunnel := webTunnel()
	tunnel.Name = "web\n\u202eevil\u2028x"
	tunnel.Cluster = "dev\tcluster\x00"
	n.forwardLost(tunnel, noticeEpoch)
	out, _, _ := n.take(noticeEpoch.Add(noticeSettle), false, update.Report{})
	note := onlyNotice(t, out)
	for _, bad := range []string{"\n", "\u202e", "\u2028", "\t", "\x00"} {
		if strings.Contains(note.Title+note.Body, bad) {
			t.Errorf("%q reached the text: %q / %q", bad, note.Title, note.Body)
		}
	}
	// The link is not cleaned but validated: a name the validator refuses
	// sends the click to the cluster list.
	if got := pathOf(t, note); got != "/" {
		t.Errorf("a click with a refused name goes to %q, want the cluster list", got)
	}
}

// The darwin glue makes an NSString of every field, and `stringWithUTF8String:`
// answers nil for bytes that are not UTF-8 - which, put in the userInfo
// dictionary, would raise. Whatever bytes a kubeconfig or a cluster hands
// over, the text and the link are valid UTF-8 with no NUL in them.
func TestTheTextIsAlwaysValidText(t *testing.T) {
	n := enabledNotices()
	tunnel := webTunnel()
	tunnel.Cluster = "dev\xff\xfe-cluster\x00"
	tunnel.Namespace = "sh\xc3op"
	tunnel.Name = strings.Repeat("\xe2\x82", 40)
	n.forwardLost(tunnel, noticeEpoch)
	out, _, _ := n.take(noticeEpoch.Add(noticeSettle), false, update.Report{})
	note := onlyNotice(t, out)
	for what, text := range map[string]string{"title": note.Title, "body": note.Body, "link": note.Link, "id": note.ID} {
		if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			t.Errorf("the %s is not valid text: %q", what, text)
		}
	}
	checkBounds(t, note)
}

func TestTheTextIsBounded(t *testing.T) {
	n := enabledNotices()
	tunnel := webTunnel()
	tunnel.Name = strings.Repeat("é", 253)
	tunnel.Namespace = strings.Repeat("n", 63)
	tunnel.Cluster = "arn:aws:eks:eu-west-3:123456789012:cluster/" + strings.Repeat("c", 100)
	n.forwardLost(tunnel, noticeEpoch)
	out, _, _ := n.take(noticeEpoch.Add(noticeSettle), false, update.Report{})
	note := onlyNotice(t, out)
	checkBounds(t, note)
	if !strings.HasSuffix(note.Title, "…") || !strings.HasSuffix(note.Body, "…") {
		t.Errorf("a cut text does not say it was cut: %q / %q", note.Title, note.Body)
	}
	// An EKS ARN holds a slash, which the link validator refuses inside a
	// segment: the click goes to the cluster list.
	if got := pathOf(t, note); got != "/" {
		t.Errorf("a click for an ARN-named cluster goes to %q, want the cluster list", got)
	}
}

func TestALostForwardWhileTheWindowIsActiveIsOnlyLogged(t *testing.T) {
	n := enabledNotices()
	n.forwardLost(webTunnel(), noticeEpoch)
	out, lines, again := n.take(noticeEpoch.Add(noticeSettle), true, update.Report{})
	if len(out) != 0 {
		t.Errorf("notified while the window was in front: %+v", out)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "in front") {
		t.Errorf("want one line saying why there was no notification, got %q", lines)
	}
	if again != 0 {
		t.Errorf("asked to look again in %s with nothing left", again)
	}
	if out, _, _ := n.take(noticeEpoch.Add(time.Minute), false, update.Report{}); len(out) != 0 {
		t.Errorf("a loss dropped while active was notified later: %+v", out)
	}
}

func TestNothingIsQueuedForAWindowThatCannotNotify(t *testing.T) {
	n := &windowNotices{}
	if n.forwardLost(webTunnel(), noticeEpoch) {
		t.Error("a window that cannot notify queued the event")
	}
	if out, _, _ := n.take(noticeEpoch.Add(time.Hour), false, update.Report{}); len(out) != 0 {
		t.Errorf("something was notified: %+v", out)
	}
}

// --- spam -------------------------------------------------------------------------

func TestLossesThatArriveTogetherAreSaidTogether(t *testing.T) {
	n := enabledNotices()
	for i, name := range []string{"web", "api", "db"} {
		tunnel := webTunnel()
		tunnel.Name = name
		n.forwardLost(tunnel, noticeEpoch.Add(time.Duration(i)*300*time.Millisecond))
	}
	out, _, _ := n.take(noticeEpoch.Add(noticeSettle), false, update.Report{})
	note := onlyNotice(t, out)
	if note.Title != "3 port-forwards lost" {
		t.Errorf("title %q", note.Title)
	}
	if note.Body != "Stopped without being closed: service/web, service/api, service/db, on dev-cluster." {
		t.Errorf("body %q", note.Body)
	}
	if got := pathOf(t, note); got != "/c/dev-cluster" {
		t.Errorf("a click goes to %q, want the cluster", got)
	}
}

func TestLossesOnSeveralClustersOpenTheClusterList(t *testing.T) {
	n := enabledNotices()
	a, b := webTunnel(), webTunnel()
	b.Cluster = "staging"
	n.forwardLost(a, noticeEpoch)
	n.forwardLost(b, noticeEpoch)
	out, _, _ := n.take(noticeEpoch.Add(noticeSettle), false, update.Report{})
	note := onlyNotice(t, out)
	if !strings.HasSuffix(note.Body, "on dev-cluster, staging.") {
		t.Errorf("body %q", note.Body)
	}
	if got := pathOf(t, note); got != "/" {
		t.Errorf("a click goes to %q", got)
	}
}

func TestForwardNotificationsAreAtLeastAGapApart(t *testing.T) {
	n := enabledNotices()
	first := webTunnel()
	n.forwardLost(first, noticeEpoch)
	onlyNotice(t, func() []notice {
		out, _, _ := n.take(noticeEpoch.Add(noticeSettle), false, update.Report{})
		return out
	}())

	second := webTunnel()
	second.Name = "api"
	at := noticeEpoch.Add(noticeSettle + time.Second)
	n.forwardLost(second, at)
	out, _, again := n.take(at.Add(noticeSettle), false, update.Report{})
	if len(out) != 0 {
		t.Fatalf("a second notification inside the gap: %+v", out)
	}
	if want := noticeEpoch.Add(noticeSettle + noticeGap).Sub(at.Add(noticeSettle)); again != want {
		t.Errorf("asked to look again in %s, want %s (the end of the gap)", again, want)
	}
	out, _, _ = n.take(noticeEpoch.Add(noticeSettle+noticeGap), false, update.Report{})
	if note := onlyNotice(t, out); note.Title != "Port-forward lost: service/api" {
		t.Errorf("after the gap: %q", note.Title)
	}
}

// A forward the operator reopens and that keeps dying is told once, then only
// logged until `noticeSameTarget` has passed.
func TestAFlappingForwardIsNotifiedOnce(t *testing.T) {
	n := enabledNotices()
	now := noticeEpoch
	notified := 0
	for range 10 {
		n.forwardLost(webTunnel(), now)
		now = now.Add(noticeGap + noticeSettle)
		out, _, _ := n.take(now, false, update.Report{})
		notified += len(out)
	}
	if notified != 1 {
		t.Errorf("a forward lost ten times in %s was notified %d times, want 1", now.Sub(noticeEpoch), notified)
	}
	n.forwardLost(webTunnel(), now.Add(noticeSameTarget))
	out, _, _ := n.take(now.Add(noticeSameTarget+noticeSettle), false, update.Report{})
	onlyNotice(t, out)
}

func TestTheQueueIsBounded(t *testing.T) {
	n := enabledNotices()
	for i := range noticeQueueMax + 8 {
		tunnel := webTunnel()
		tunnel.Name = fmt.Sprintf("svc-%d", i)
		n.forwardLost(tunnel, noticeEpoch)
	}
	if len(n.lost) != noticeQueueMax {
		t.Errorf("%d losses held, want at most %d", len(n.lost), noticeQueueMax)
	}
	out, _, _ := n.take(noticeEpoch.Add(noticeSettle), false, update.Report{})
	note := onlyNotice(t, out)
	if want := fmt.Sprintf("%d port-forwards lost", noticeQueueMax+8); note.Title != want {
		t.Errorf("title %q, want %q", note.Title, want)
	}
	checkBounds(t, note)
}

// The two notices travel in one drain without waiting on each other.
func TestAnUpdateDoesNotWaitForTheForwardSettle(t *testing.T) {
	n := enabledNotices()
	n.forwardLost(webTunnel(), noticeEpoch)
	out, _, again := n.take(noticeEpoch, false, update.Report{State: update.Newer, Latest: "0.5.0"})
	if note := onlyNotice(t, out); note.ID != noticeUpdateID {
		t.Errorf("want the update now, got %+v", note)
	}
	if again != noticeSettle {
		t.Errorf("the forward is to be looked at again in %s, want %s", again, noticeSettle)
	}
}

// --- the click ----------------------------------------------------------------------

// Every route a notification can carry is one `consolePathFromURL` accepts, and
// one a link would open: the click goes through the same door.
func TestEveryNoticeLinkPassesTheLinkValidator(t *testing.T) {
	links := []string{
		noticeLink(updatePath),
		noticeLink("/"),
		noticeLink("/c/dev-cluster"),
		noticeLink(objectPagePath(lostForward{Cluster: "kind kind", Namespace: "default", Kind: "service", Name: "x"})),
		noticeLink(objectPagePath(lostForward{Cluster: "a/b", Namespace: "default", Kind: "pod", Name: "x"})),
		noticeLink("/api/v1/secrets"),
		noticeLink("/c/prod/x/extension"),
	}
	for _, link := range links {
		if !strings.HasPrefix(link, urlScheme+"://") {
			t.Errorf("%q is not a %s:// link", link, urlScheme)
		}
		if _, err := consolePathFromURL(link); err != nil {
			t.Errorf("%q is refused by the validator a click meets: %v", link, err)
		}
	}
	if got, _ := consolePathFromURL(noticeLink("/api/v1/secrets")); got != "/" {
		t.Errorf("a path that is not a page became %q, want the cluster list", got)
	}
}

// --- outside a bundle -----------------------------------------------------------------

func TestNotificationsNeedAnApplicationBundle(t *testing.T) {
	for _, c := range []struct {
		id, path string
		want     bool
	}{
		{"dev.thonicdev.kubetower", "/Applications/KubeTower.app", true},
		{"dev.thonicdev.kubetower", "/Users/me/Applications/KubeTower.app/", true},
		// A bare executable from a terminal: no identifier, and the "bundle"
		// is the directory it sits in.
		{"", "/usr/local/bin", false},
		{"", "/Applications/KubeTower.app", false},
		{"dev.thonicdev.kubetower", "/opt/kubetower", false},
	} {
		ok, line := noticesAvailable(c.id, c.path)
		if ok != c.want {
			t.Errorf("noticesAvailable(%q, %q) = %t, want %t", c.id, c.path, ok, c.want)
		}
		if ok == (line != "") {
			t.Errorf("noticesAvailable(%q, %q): available %t with the line %q", c.id, c.path, ok, line)
		}
	}
}
