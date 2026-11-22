// Package recorder feeds the archive.
//
// It polls each reachable cluster for its recent events and hands them to
// `internal/archive`. Polling rather than watching: across many clusters behind
// lapsing SSO sessions and VPNs, a watch is a long-lived connection per cluster
// to re-establish, while a failed poll leaves no state behind. Kubernetes keeps
// events for about an hour, so an interval of minutes loses nothing.
package recorder

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/archive"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/resources"
	"github.com/thonicdev/kubetower/safego"
	"github.com/thonicdev/kubetower/textutil"
)

// Store is the slice of the archive this package writes to.
//
// Writable is needed because RecordEvents answers `(0, nil)` on a full disk:
// its success is not evidence that anything was written.
type Store interface {
	Enabled() bool
	RecordEvents(cluster string, events []archive.Event) (int, error)
	// Writable reports whether the store is taking writes, and why not when it
	// is not. An archive that is switched off answers true: that is a setting,
	// not a fault.
	Writable() (bool, string)
}

// storeFailure marks an error that came from the archive rather than from the
// cluster, so a full disk neither backs off a cluster that answered nor gets
// reported under the cluster's name.
type storeFailure struct{ err error }

func (e storeFailure) Error() string { return e.err.Error() }
func (e storeFailure) Unwrap() error { return e.err }

const (
	defaultInterval = 2 * time.Minute
	// perClusterTimeout bounds one cluster's list. Well under the interval, so a
	// cluster that has gone away cannot delay the next round.
	perClusterTimeout = 20 * time.Second
	// maxEvents is how many events one round hands over: the newest that many.
	//
	// `Limit` is a chunk size, not a ranking: a list is served in key order,
	// `<namespace>/<name>` (ordering_integration_test.go), so a bare limit
	// returns an alphabetical slice. Kubernetes has no ordering or time-based
	// selector on events, so the list is read to the end and ranked here, at
	// one round trip per page.
	maxEvents = 2000
	// maxEventPages bounds that read, so one cluster mid-incident cannot spend
	// the whole per-cluster timeout paging. Hitting it is reported: past it the
	// recorder no longer knows it has the newest events.
	maxEventPages = 40
	// maxMessage bounds one archived message, in runes (a byte cut would split
	// a multi-byte character on disk). A webhook denial or a panic arrives whole.
	maxMessage = 2000
	// concurrency bounds how many clusters are asked at once: many
	// simultaneous EKS token fetches stampede the AWS CLI's cache.
	concurrency = 4
)

// backoff is how long a cluster is left alone after failing, doubling to a
// ceiling. Most failures here are not transient (an expired SSO session fails
// identically every round), so without it the log is noise.
const (
	backoffStart = 5 * time.Minute
	backoffMax   = time.Hour
)

// Clusters is the cluster manager, as this package needs it. An interface so
// a round can be driven against clusters that answer in a chosen way.
type Clusters interface {
	Contexts() []string
	Client(context string) (*cluster.Client, error)
}

// Recorder polls the clusters on a timer and writes their events to the store.
type Recorder struct {
	clusters Clusters
	store    Store
	interval time.Duration
	logf     func(string, ...any)

	// now is injectable so the backoff can be tested without waiting.
	now func() time.Time

	mu    sync.Mutex
	state map[string]*clusterState
	// storage is the archive's own standing as the last round found it; not a
	// property of any cluster.
	storage StorageStatus
	// incomplete names the clusters whose last collection could not read the
	// whole event list, with what stopped it.
	incomplete map[string]string
	// said is one-shot logging for the subjects that are not clusters: a line
	// is emitted only when the sentence for a subject changes.
	said map[string]string
}

type clusterState struct {
	// nextAttempt is when this cluster may be asked again. Zero means now.
	nextAttempt time.Time
	penalty     time.Duration
	// reported is the last failure said out loud, so the same sentence is not
	// repeated every round.
	reported string
}

// Options configures New.
type Options struct {
	Clusters Clusters
	Store    Store
	// Interval between rounds. Zero means two minutes.
	Interval time.Duration
	Logf     func(string, ...any)
}

// New returns a recorder; Start runs it.
func New(opts Options) *Recorder {
	r := &Recorder{
		clusters: opts.Clusters,
		store:    opts.Store,
		interval: opts.Interval,
		logf:     opts.Logf,
		now:      time.Now,
		state:    map[string]*clusterState{},

		incomplete: map[string]string{},
		said:       map[string]string{},
		// "Not yet asked" must not render as "the archive is broken".
		storage: StorageStatus{OK: true},
	}
	if r.interval <= 0 {
		r.interval = defaultInterval
	}
	if r.logf == nil {
		r.logf = func(string, ...any) {}
	}
	return r
}

// Start runs rounds until the context ends. It returns immediately.
func (r *Recorder) Start(ctx context.Context) {
	if r.clusters == nil || r.store == nil {
		return
	}
	// A panic in one round is that round's, logged; the next tick records again.
	safego.Go("recorder: the round ticker", func() error {
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				_ = safego.Call("recorder: one round", func() error { r.Round(ctx); return nil })
			}
		}
	}, nil)
}

// Round asks every due cluster once and returns how many events were stored.
// Exported so it can be driven without a ticker.
func (r *Recorder) Round(ctx context.Context) int {
	// Checked every round: the archive can be switched from the settings page.
	if !r.store.Enabled() {
		// Writable() answers `(true, "")` on a switched-off archive, so asking
		// it here clears a standing refusal. The scan notes go too: they
		// describe a collection that is no longer happening.
		r.storageIs(r.store.Writable())
		r.forgetScans()
		return 0
	}

	names := r.clusters.Contexts()
	slots := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0

	for _, name := range names {
		if !r.due(name) {
			continue
		}
		wg.Add(1)
		// A panic collecting one cluster is that cluster's failed round.
		safego.Go("recorder: collecting a cluster's events", func() error {
			slots <- struct{}{}
			defer func() { <-slots }()

			var stored int
			err := safego.Call("recorder: collecting "+name, func() error {
				var err error
				stored, err = r.collect(ctx, name)
				return err
			})

			var fromStore storeFailure
			switch {
			case err == nil:
				r.succeeded(name)
			case errors.As(err, &fromStore):
				// The cluster answered; the archive refused. The cluster is not
				// backed off, and storageIs reports the refusal.
				r.succeeded(name)
			default:
				r.failed(name, err)
				return nil
			}

			mu.Lock()
			total += stored
			mu.Unlock()
			return nil
		}, func(error) { wg.Done() })
	}
	wg.Wait()

	// Asked after the writes: internal/archive discovers a full disk by trying.
	r.storageIs(r.store.Writable())
	return total
}

// collect asks one cluster and hands what it has to the archive.
func (r *Recorder) collect(ctx context.Context, name string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, perClusterTimeout)
	defer cancel()

	client, err := r.clusters.Client(name)
	if err != nil {
		return 0, err
	}

	// Every namespace.
	items, note, err := newestEvents(ctx, client.Clients.CoreV1().Events("").List)
	if err != nil {
		return 0, err
	}
	r.scanned(name, note)

	stored, err := r.store.RecordEvents(name, convert(name, items))
	if err != nil {
		// Marked as the archive's here: further up the two refusals cannot be
		// told apart.
		return stored, storeFailure{err}
	}
	return stored, nil
}

// newestEvents reads a cluster's events and keeps the most recent maxEvents.
// The second result is empty when the whole list was read, and otherwise says
// what stopped it.
//
// The continue token is followed as resources.Paged does: a server answering
// from its watch cache returns everything and no token, one honoring `Limit`
// returns pages, and the archive must hold the same either way.
func newestEvents(
	ctx context.Context,
	list func(context.Context, metav1.ListOptions) (*corev1.EventList, error),
) ([]corev1.Event, string, error) {
	opts := resources.Cached()
	var kept []corev1.Event

	for page := 1; ; page++ {
		l, err := list(ctx, opts)
		if err != nil {
			return nil, "", err
		}
		// Ranked and cut after every page, so memory stays bounded.
		kept = newestOf(append(kept, l.Items...))

		opts.Continue = l.GetContinue()
		if opts.Continue == "" {
			return kept, "", nil
		}
		// The API server refuses continue and resourceVersion together: the
		// token already pins the snapshot the rest of the pages come from.
		opts.ResourceVersion = ""

		if page >= maxEventPages {
			return kept, fmt.Sprintf(
				"more than %d events were reported and only the first %d were read, so these are the "+
					"newest of what was read and not necessarily the newest the cluster has",
				page*int(opts.Limit), page*int(opts.Limit)), nil
		}
	}
}

// newestOf ranks events by when they were last seen and keeps the head. It
// uses resources.EventTime, the same stamp convert writes to disk.
func newestOf(events []corev1.Event) []corev1.Event {
	if len(events) <= maxEvents {
		return events
	}
	sort.SliceStable(events, func(i, j int) bool {
		return resources.EventTime(&events[i]).After(resources.EventTime(&events[j]))
	})
	return events[:maxEvents]
}

// convert turns Kubernetes events into the archive's own shape, a separate
// type because it is written to disk and read back by later builds.
func convert(clusterName string, items []corev1.Event) []archive.Event {
	out := make([]archive.Event, 0, len(items))
	for i := range items {
		e := &items[i]

		// resources.EventTime honors `series.lastObservedTime` and the
		// creation-time floor, so no event is archived as 1970.
		when := resources.EventTime(e)
		first := resources.EventFirstTime(e)

		from := e.Source.Component
		if from == "" {
			from = e.ReportingController
		}

		out = append(out, archive.Event{
			Cluster:   clusterName,
			Namespace: e.Namespace,
			Kind:      e.InvolvedObject.Kind,
			Object:    e.InvolvedObject.Name,
			UID:       string(e.InvolvedObject.UID),
			Type:      e.Type,
			Reason:    e.Reason,
			Message:   textutil.Clip(e.Message, maxMessage),
			From:      from,
			// EventCount and not e.Count: the newer API leaves the bare field at
			// zero.
			Count: resources.EventCount(e),
			First: first,
			Last:  when,
		})
	}

	// Oldest first, so a file read back reads forwards in time.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Last.Before(out[j].Last) })
	return out
}

// --- what it is doing ---------------------------------------------------------

// Status is the poller's own standing. The poller says each failure once, so
// without this an archive that stopped collecting from most clusters would look
// like one collecting from all of them; the health page renders it.
type Status struct {
	// Enabled is whether the archive is accepting anything at all. False means
	// nothing below matters: the rounds return at the switch.
	Enabled bool
	// Interval is how often a round happens.
	Interval time.Duration
	// Contexts is how many clusters a round covers.
	Contexts int
	// Failing is the clusters currently being backed off, worst first. A cluster
	// that is answering keeps no state here at all, so an empty list means every
	// context is being collected from.
	Failing []ClusterStatus

	// Storage is whether the archive itself is taking writes. Separate from
	// Failing: a full disk is one archive refusing, not every cluster failing.
	Storage StorageStatus

	// Incomplete names the clusters whose last collection hit the page ceiling
	// and so may not hold the newest events, sorted by name. Empty is normal.
	Incomplete []ClusterNote
}

// StorageStatus is the archive's own standing, as the poller last found it.
type StorageStatus struct {
	// OK is false only when the store said it is refusing writes. A switched-off
	// archive is OK: that is a setting, and the settings page says so.
	OK bool
	// Detail is why, from the store, and is empty when OK.
	Detail string
	// Since is when it started refusing.
	Since time.Time
}

// ClusterNote is one cluster and something true of its last collection that is
// not a failure.
type ClusterNote struct {
	Cluster string
	Note    string
}

// ClusterStatus is one cluster the poller is currently leaving alone.
type ClusterStatus struct {
	Cluster string
	// NextAttempt is when it may be asked again. Zero means now.
	NextAttempt time.Time
	// Penalty is how long it is being left alone for, doubling to an hour.
	Penalty time.Duration
	// Reason is the failure as it was last logged.
	Reason string
}

// Status reports what the poller is doing. Safe on a nil recorder or one with
// no store or clusters.
func (r *Recorder) Status() Status {
	if r == nil {
		return Status{}
	}
	s := Status{Interval: r.interval}
	if r.store != nil {
		s.Enabled = r.store.Enabled()
	}
	if r.clusters != nil {
		s.Contexts = len(r.clusters.Contexts())
	}

	r.mu.Lock()
	for name, state := range r.state {
		s.Failing = append(s.Failing, ClusterStatus{
			Cluster:     name,
			NextAttempt: state.nextAttempt,
			Penalty:     state.penalty,
			Reason:      state.reported,
		})
	}
	s.Storage = r.storage
	for name, note := range r.incomplete {
		s.Incomplete = append(s.Incomplete, ClusterNote{Cluster: name, Note: note})
	}
	r.mu.Unlock()

	sort.SliceStable(s.Incomplete, func(i, j int) bool {
		return strings.ToLower(s.Incomplete[i].Cluster) < strings.ToLower(s.Incomplete[j].Cluster)
	})

	// Longest penalty first (missing from the archive longest), then by name
	// so the list is stable between refreshes.
	sort.SliceStable(s.Failing, func(i, j int) bool {
		if a, b := s.Failing[i].Penalty, s.Failing[j].Penalty; a != b {
			return a > b
		}
		return strings.ToLower(s.Failing[i].Cluster) < strings.ToLower(s.Failing[j].Cluster)
	})
	return s
}

// --- the archive, and how much of a cluster was read ---------------------------

// storageIs records what the store said about itself and logs it once, not
// once a round, in a line distinct from any cluster's.
func (r *Recorder) storageIs(ok bool, detail string) {
	r.mu.Lock()
	was := r.storage
	r.storage.OK = ok
	r.storage.Detail = detail
	switch {
	case ok:
		r.storage.Since = time.Time{}
	case was.OK || was.Since.IsZero():
		r.storage.Since = r.now()
	}
	r.mu.Unlock()

	if ok {
		r.say("storage", "")
		// A disabled archive answers writable too, so the recovery line is only
		// logged when the archive is on.
		if !was.OK && r.enabled() {
			r.logf("archive: the store is taking writes again")
		}
		return
	}
	r.say("storage", fmt.Sprintf("archive: the store is not taking writes, so nothing is being kept "+
		"(the clusters are answering and are still being asked): %s", firstLine(detail)))
}

// scanned records whether one cluster's whole event list was read.
func (r *Recorder) scanned(name, note string) {
	r.mu.Lock()
	if note == "" {
		delete(r.incomplete, name)
	} else {
		r.incomplete[name] = note
	}
	r.mu.Unlock()

	if note == "" {
		r.say("scan:"+name, "")
		return
	}
	r.say("scan:"+name, fmt.Sprintf("archive: %s: %s", name, note))
}

// enabled is the store's switch, safe on a recorder assembled without one.
func (r *Recorder) enabled() bool { return r.store != nil && r.store.Enabled() }

// forgetScans drops every "read only the first N" note, through scanned so the
// one-shot log key goes too and the truncation is said again after a re-enable.
// scanned takes the same non-reentrant mutex, so it is called outside the lock.
func (r *Recorder) forgetScans() {
	r.mu.Lock()
	names := make([]string, 0, len(r.incomplete))
	for name := range r.incomplete {
		names = append(names, name)
	}
	r.mu.Unlock()

	for _, name := range names {
		r.scanned(name, "")
	}
}

// say logs a line only when the sentence for a subject changes, and remembers
// nothing else. An empty message clears the subject, so the next occurrence is
// said again.
func (r *Recorder) say(subject, message string) {
	r.mu.Lock()
	repeat := r.said[subject] == message
	if message == "" {
		delete(r.said, subject)
	} else {
		r.said[subject] = message
	}
	r.mu.Unlock()

	if !repeat && message != "" {
		r.logf("%s", message)
	}
}

// --- backoff -----------------------------------------------------------------

func (r *Recorder) due(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.state[name]
	return !ok || !r.now().Before(s.nextAttempt)
}

func (r *Recorder) failed(name string, err error) {
	say := ""
	// The log line is composed under the lock and emitted after it: on Windows
	// logf writes a file, and Status() takes this mutex on every page render.
	defer func() {
		if say != "" {
			r.logf("%s", say)
		}
	}()

	r.mu.Lock()
	defer r.mu.Unlock()

	s := r.state[name]
	if s == nil {
		s = &clusterState{}
		r.state[name] = s
	}
	if s.penalty == 0 {
		s.penalty = backoffStart
	} else if s.penalty < backoffMax {
		s.penalty *= 2
		if s.penalty > backoffMax {
			s.penalty = backoffMax
		}
	}
	s.nextAttempt = r.now().Add(s.penalty)

	// A cluster that could not be asked has no scan to be incomplete.
	delete(r.incomplete, name)

	reason := firstLine(err.Error())
	if s.reported != reason {
		s.reported = reason
		// Said once per distinct reason, not once per round.
		say = fmt.Sprintf("archive: %s is not answering, backing off to %s: %s",
			name, s.penalty, reason)
	}
}

func (r *Recorder) succeeded(name string) {
	recovered := false
	// Emitted after the unlock, as in failed.
	defer func() {
		if recovered {
			r.logf("archive: %s is answering again", name)
		}
	}()

	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.state[name]; s != nil {
		recovered = s.reported != ""
		delete(r.state, name)
	}
}

// firstLine caps a failure for a log line: a multi-kilobyte `aws` failure
// repeated per cluster is noise.
func firstLine(s string) string { return textutil.FirstLineN(s, detailLine) }

// detailLine matches internal/health, which classifies the very same errors.
const detailLine = 200
