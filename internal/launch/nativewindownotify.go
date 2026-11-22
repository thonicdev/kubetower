package launch

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/forward"
	"github.com/thonicdev/kubetower/internal/update"
)

// The macOS window's system notifications: which events become one, what it
// says, when it is sent, and where a click goes. No build tag, so it is tested
// on Linux; the darwin file asks for permission, posts what take returns and
// hands a click to the kubetower:// path a link takes.
//
// Composed by Go from typed fields, never by the page: there is no web→native
// channel, so nothing a page or a cluster says chooses what appears on the
// screen beyond the names below.
//
// Exactly two events:
//
//   - An update is available: the check answered Newer, as the Dock badge shows.
//   - A port-forward was lost: one the operator opened stopped without the
//     operator stopping it (forward.Manager decides; a forward closed by the
//     operator or the process never reaches here).
//
// Names only: cluster, namespace and object names and port numbers, never a
// value, a Secret or a cluster's error text. lostForward is built field by field,
// so a field added to forward.Tunnel cannot leak by accident; the update says a
// version-shaped version or none. Names lose control and bidi-override
// characters, and title and body are bounded.
//
// Only while the application is not active: a lost forward found with the
// window in front is logged (the Ports panel shows it). A newer release found
// then is held until the window is not in front, then notified once, since the
// check answers seconds after launch, when the window is almost always in
// front.
//
// Bounded, so a flapping forward cannot spam:
//
//   - Losses arriving together are said together: the first waits noticeSettle
//     (a dead cluster ends every forward into it within a moment).
//   - At most one forward notification per noticeGap; the rest go in the next.
//   - The same target lost again within noticeSameTarget is logged only.
//   - At most noticeQueueMax losses are held; more are counted.
//   - The update is notified once per version per process.

// The bounds, and the timing.
const (
	noticeTitleMax = 64
	noticeBodyMax  = 160

	noticeSettle     = 2 * time.Second
	noticeGap        = 30 * time.Second
	noticeSameTarget = 10 * time.Minute
	noticeQueueMax   = 32
)

// notice is one notification, composed: what the darwin glue posts.
type notice struct {
	// ID is the request's identifier. The update's is fixed, so a second post
	// replaces the first.
	ID    string
	Title string
	Body  string
	// Link is the kubetower:// address a click opens, from userInfo, checked
	// again by consolePathFromURL on the way back, so a click can do nothing a
	// link could not.
	Link string
}

// The identifiers, prefixed with the bundle identifier so they cannot collide
// with another application's.
const (
	noticeUpdateID        = "dev.thonicdev.kubetower.update"
	noticeForwardIDPrefix = "dev.thonicdev.kubetower.forward."
)

// lostForward is everything a lost-forward notification may say: names and
// port numbers, not forward.Tunnel, which also carries the cluster's error text
// and the local address.
type lostForward struct {
	Cluster   string
	Namespace string
	// Kind is "service" or "pod", as forward.Request.Kind says it.
	Kind string
	Name string
	// LocalPort is where it listened on this Mac, Port the port asked for on
	// the object.
	LocalPort int
	Port      int
}

func lostForwardOf(t forward.Tunnel) lostForward {
	return lostForward{
		Cluster:   t.Cluster,
		Namespace: t.Namespace,
		Kind:      t.Kind,
		Name:      t.Name,
		LocalPort: t.LocalPort,
		Port:      t.Port,
	}
}

// key is the target's identity, for "the same target lost again".
func (l lostForward) key() string {
	return strings.Join([]string{l.Cluster, l.Namespace, l.Kind, l.Name, strconv.Itoa(l.Port)}, "\x00")
}

// target is "service/web", as the Ports panel and the log say it.
func (l lostForward) target() string { return clean(l.Kind) + "/" + clean(l.Name) }

type queuedLoss struct {
	what lostForward
	at   time.Time
}

// windowNotices is the state between the events and the window. forwardLost is
// called from any goroutine, take from the main thread; one mutex.
type windowNotices struct {
	// enabled is set by the darwin window once it can notify (in a bundle, the
	// framework loaded). Until then, and on other platforms, events are dropped
	// at the door so nothing accumulates.
	enabled atomic.Bool

	mu sync.Mutex
	// lost is what waits to be said, oldest first; dropped counts what did not
	// fit.
	lost    []queuedLoss
	dropped int
	// lastForward is when the last forward notification was released (for
	// noticeGap); recent is when each target was last notified (for
	// noticeSameTarget).
	lastForward time.Time
	recent      map[string]time.Time
	// updateSaid is the version already notified; updateHeld the version whose
	// holding has already been logged, so the log says it once.
	updateSaid string
	updateHeld string
	seq        int
}

// notices is the process's one instance: there is one window.
var notices = &windowNotices{}

// forwardLostNotice is forward.Options.Lost: queue, then ask the window to
// look. Never blocks.
func forwardLostNotice(t forward.Tunnel) {
	if notices.forwardLost(t, time.Now()) {
		deliverNativeNotices()
	}
}

// forwardLost queues one lost forward and reports whether it was queued.
func (n *windowNotices) forwardLost(t forward.Tunnel, now time.Time) bool {
	if !n.enabled.Load() {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.lost) >= noticeQueueMax {
		n.dropped++
		return true
	}
	n.lost = append(n.lost, queuedLoss{what: lostForwardOf(t), at: now})
	return true
}

// take is what to post now, the lines to log, and how long until it should be
// asked again (0: not until something happens). active is whether the
// application is active; report is the update check's last answer.
//
// The lines say what was not notified and why; the poster logs what is posted,
// knowing whether macOS took it.
func (n *windowNotices) take(now time.Time, active bool, report update.Report) (out []notice, lines []string, again time.Duration) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if report.State == update.Newer && report.Latest != "" && report.Latest != n.updateSaid {
		if active {
			if n.updateHeld != report.Latest {
				n.updateHeld = report.Latest
				lines = append(lines, "a newer release is known while the window is in front, so its "+
					"notification waits until KubeTower is not the active application")
			}
		} else {
			n.updateSaid = report.Latest
			out = append(out, updateNotice(report.Latest))
		}
	}

	for key, at := range n.recent {
		if now.Sub(at) >= noticeSameTarget {
			delete(n.recent, key)
		}
	}
	if len(n.lost) == 0 {
		return out, lines, 0
	}
	release := n.lost[0].at.Add(noticeSettle)
	if !n.lastForward.IsZero() {
		if gap := n.lastForward.Add(noticeGap); gap.After(release) {
			release = gap
		}
	}
	if now.Before(release) {
		return out, lines, release.Sub(now)
	}

	batch, dropped := n.lost, n.dropped
	n.lost, n.dropped = nil, 0
	var fresh []lostForward
	for _, q := range batch {
		if at, ok := n.recent[q.what.key()]; ok && now.Sub(at) < noticeSameTarget {
			lines = append(lines, fmt.Sprintf("the port-forward to %s was lost again within %s of the last "+
				"notification about it, so this one is only in the log", q.what.target(), noticeSameTarget))
			continue
		}
		fresh = append(fresh, q.what)
	}
	if len(fresh) == 0 && dropped == 0 {
		return out, lines, 0
	}
	if active {
		lines = append(lines, fmt.Sprintf("%s lost while the window was in front, so no notification",
			plural(len(fresh)+dropped, "port-forward was", "port-forwards were")))
		return out, lines, 0
	}
	for _, l := range fresh {
		if n.recent == nil {
			n.recent = map[string]time.Time{}
		}
		n.recent[l.key()] = now
	}
	n.lastForward = now
	n.seq++
	note := lostNotice(fresh, dropped)
	note.ID = noticeForwardIDPrefix + strconv.Itoa(n.seq)
	out = append(out, note)
	return out, lines, 0
}

// versionShaped is what a version must look like to be said: ktbuild's shape
// for a stamped version, bounded. Anything else from the feed is not said.
var versionShaped = regexp.MustCompile(`^[0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6}(-[0-9A-Za-z.]{1,32})?$`)

// updatePath is where a click on the update notification goes: Settings ▸
// Infos, which holds the newer version and its release link.
const updatePath = "/settings/infos"

func updateNotice(latest string) notice {
	title := "A newer KubeTower is available"
	if versionShaped.MatchString(latest) {
		title = "KubeTower " + latest + " is available"
	}
	body := "Settings ▸ Infos links to its release page."
	if versionShaped.MatchString(version) {
		body = "You are on " + version + ". " + body
	}
	return notice{
		ID:    noticeUpdateID,
		Title: bound(title, noticeTitleMax),
		Body:  bound(body, noticeBodyMax),
		Link:  noticeLink(updatePath),
	}
}

// lostNotice says one or several lost forwards. `dropped` is the losses that
// did not fit in the queue: counted, not named.
func lostNotice(lost []lostForward, dropped int) notice {
	if len(lost) == 1 && dropped == 0 {
		l := lost[0]
		where := "namespace " + clean(l.Namespace) + " on " + clean(l.Cluster)
		body := fmt.Sprintf("Stopped without being closed: local port %d to %d, %s.", l.LocalPort, l.Port, where)
		return notice{
			Title: bound("Port-forward lost: "+l.target(), noticeTitleMax),
			Body:  bound(body, noticeBodyMax),
			Link:  noticeLink(objectPagePath(l)),
		}
	}

	total := len(lost) + dropped
	var targets []string
	clusters := map[string]bool{}
	for _, l := range lost {
		targets = append(targets, l.target())
		clusters[l.Cluster] = true
	}
	var names []string
	for c := range clusters {
		names = append(names, clean(c))
	}
	sort.Strings(names)
	body := "Stopped without being closed"
	if len(targets) > 0 {
		body += ": " + strings.Join(targets, ", ")
	}
	if dropped > 0 {
		body += fmt.Sprintf(" and %d more", dropped)
	}
	if len(names) > 0 {
		body += ", on " + strings.Join(names, ", ")
	}
	path := "/"
	if len(clusters) == 1 {
		path = api.ClusterPath(lost[0].Cluster)
	}
	return notice{
		Title: bound(strconv.Itoa(total)+" port-forwards lost", noticeTitleMax),
		Body:  bound(body+".", noticeBodyMax),
		Link:  noticeLink(path),
	}
}

// objectPagePath is the SPA's page of the object a forward pointed at
// (`/c/<cluster>/services|pods/<namespace>/<name>`), where its Ports panel is.
// A gone pod's page says so, which is why the forward stopped.
func objectPagePath(l lostForward) string {
	kind := "pods"
	if l.Kind == "service" {
		kind = "services"
	}
	segments := []string{kind, l.Namespace, l.Name}
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return api.ClusterPath(l.Cluster) + "/" + strings.Join(segments, "/")
}

// noticesAvailable is whether this process may use `UNUserNotificationCenter`
// at all, from what the system says about its main bundle, and the one line to
// log when it may not.
//
// Outside a bundle, asking for the notification center ends the process:
// currentNotificationCenter raises NSInternalInconsistencyException
// ("bundleProxyForCurrentProcess is nil") and aborts, and an Objective-C
// exception cannot be caught here (see guardWindow). So the center is asked
// only when the system reports a bundle identifier and a bundle path ending in
// .app; launchedFromAppBundle reads only the executable's path, which is not
// enough.
func noticesAvailable(bundleID, bundlePath string) (bool, string) {
	if bundleID != "" && strings.HasSuffix(strings.TrimSuffix(bundlePath, "/"), ".app") {
		return true, ""
	}
	return false, "this process is not running from an application bundle (the system reports no bundle " +
		"identifier, or a bundle that is not an .app), so it sends no macOS notifications: a lost " +
		"port-forward or a newer release is said in this log, and the newer release on the Dock badge"
}

// noticeLink is the kubetower:// address for a console path, checked by the
// validator a click will meet: a path it refuses (a cluster name holding a
// slash, such as an EKS ARN) becomes the cluster list rather than a dead click.
func noticeLink(path string) string {
	link := urlScheme + "://" + strings.TrimPrefix(path, "/")
	if _, err := consolePathFromURL(link); err != nil {
		return urlScheme + "://"
	}
	return link
}

// clean is a name as a notification may show it: control, line-breaking and
// bidirectional-formatting characters become a space, so a name cannot break
// a line or reorder the text around it.
func clean(name string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, name)
}

// bound cuts s to at most max runes, so a non-Latin name is never cut
// mid-character, ending in an ellipsis when cut.
func bound(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}
