package recorder

// A round, driven end to end against clusters that answer in a chosen way.
//
// Everything else in this package's tests drives a predicate — `failed`, `due`,
// `convert`. These test the *call sites*: which remedy `collect` maps an error
// onto, and what `Limit` means to the call it is passed to. Neither is
// reachable from a predicate test.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// --- clusters that answer as the test says ------------------------------------

type fakeClusters struct {
	clients map[string]kubernetes.Interface
	errs    map[string]error
}

func (f fakeClusters) Contexts() []string {
	out := make([]string, 0, len(f.clients)+len(f.errs))
	for name := range f.clients {
		out = append(out, name)
	}
	for name := range f.errs {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (f fakeClusters) Client(name string) (*cluster.Client, error) {
	if err := f.errs[name]; err != nil {
		return nil, err
	}
	set, ok := f.clients[name]
	if !ok {
		return nil, fmt.Errorf("%q is not a context", name)
	}
	return &cluster.Client{Context: name, Clients: set}, nil
}

// clusterOf is one context reporting the events given.
func clusterOf(name string, events ...*corev1.Event) fakeClusters {
	objects := make([]runtime.Object, 0, len(events))
	for _, e := range events {
		objects = append(objects, e)
	}
	return fakeClusters{clients: map[string]kubernetes.Interface{name: fake.NewClientset(objects...)}}
}

func reportedEvent(name string, at time.Time) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: name, Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: name},
		Reason:         "BackOff",
		Type:           "Warning",
		LastTimestamp:  metav1.NewTime(at),
		FirstTimestamp: metav1.NewTime(at),
		Count:          1,
	}
}

// log collects what the operator would read in the container's own output.
type log struct {
	mu    sync.Mutex
	lines []string
}

func (l *log) write(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *log) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func (l *log) count(substring string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, line := range l.lines {
		if strings.Contains(line, substring) {
			n++
		}
	}
	return n
}

// --- whose fault, and which remedy ---------------------------------------------

// A disk that will not take the write is not a cluster that has stopped
// answering, and the remedy for one is wrong for the other. Backing a healthy
// cluster off doubles to an hour, stops the archive collecting from it long
// after the disk is fixed, and files the fault under the cluster's name — so the
// one screen that would have said "the disk is full" says "prod is not
// answering" instead.
func TestAStoreThatRefusesTheWriteDoesNotSilenceTheCluster(t *testing.T) {
	lines := &log{}
	store := &fakeStore{
		on:       true,
		writable: false,
		why:      "events for prod could not be written to /state/archive: no space left on device",
		refuse:   errors.New("write /state/archive/events.jsonl: no space left on device"),
	}
	r := New(Options{
		Clusters: clusterOf("prod", reportedEvent("web.1", time.Now())),
		Store:    store,
		Logf:     lines.write,
	})

	r.Round(context.Background())

	if !r.due("prod") {
		t.Error("the cluster is being backed off for the archive's failure; it answered, and it will " +
			"not be asked again for five minutes, then ten, then an hour")
	}
	status := r.Status()
	if len(status.Failing) != 0 {
		t.Errorf("the health page names %+v as failing; none of them is", status.Failing)
	}
	if status.Storage.OK {
		t.Fatal("nothing says the archive is refusing writes: the page reads as one context, " +
			"collecting, nothing failing — while nothing at all is being kept")
	}
	if !strings.Contains(status.Storage.Detail, "no space left on device") {
		t.Errorf("Storage.Detail = %q, which does not say what to fix", status.Storage.Detail)
	}
	if status.Storage.Since.IsZero() {
		t.Error("Storage.Since is zero, so the page cannot say how long this has been going on")
	}

	said := lines.all()
	if strings.Contains(said, "prod is not answering") {
		t.Errorf("the log blames the cluster for the disk:\n%s", said)
	}
	if !strings.Contains(said, "not taking writes") {
		t.Errorf("the log does not say the store is refusing:\n%s", said)
	}

	// And it is said once. This is unattended work on a two-minute timer: a full
	// disk fails every round for as long as it is full, and a line every two
	// minutes forever is the same as no logging at all.
	r.Round(context.Background())
	r.Round(context.Background())
	if got := lines.count("not taking writes"); got != 1 {
		t.Errorf("three rounds against a full disk produced %d lines, want 1", got)
	}

	// When it clears, that is worth exactly one line too.
	store.mu.Lock()
	store.refuse, store.writable, store.why = nil, true, ""
	store.mu.Unlock()

	r.Round(context.Background())
	if got := lines.count("taking writes again"); got != 1 {
		t.Errorf("the store came back and the log said so %d times, want 1:\n%s", got, lines.all())
	}
	if !r.Status().Storage.OK {
		t.Error("the store is writing again and Status still says it is not")
	}
}

// The other half of the same distinction, which must keep working: a cluster
// that cannot be reached is still backed off, and still by name.
func TestAClusterThatCannotBeReachedIsStillBackedOff(t *testing.T) {
	lines := &log{}
	r := New(Options{
		Clusters: fakeClusters{errs: map[string]error{"sso": errors.New("token has expired")}},
		Store:    &fakeStore{on: true, writable: true},
		Logf:     lines.write,
	})

	r.Round(context.Background())

	if r.due("sso") {
		t.Error("a cluster that failed is due again immediately; the backoff is what keeps a lapsed " +
			"SSO session to one log line rather than one every two minutes")
	}
	status := r.Status()
	if len(status.Failing) != 1 || status.Failing[0].Cluster != "sso" {
		t.Fatalf("Failing = %+v, want the one cluster that failed", status.Failing)
	}
	if !status.Storage.OK {
		t.Error("a cluster failing was reported as the archive refusing writes")
	}
	if !strings.Contains(lines.all(), "sso is not answering") {
		t.Errorf("the log does not name the cluster:\n%s", lines.all())
	}
}

// --- which N --------------------------------------------------------------------

// `Limit` is a chunk size, not a ranking: a list is served in key order, so the
// first page of a cluster reporting more than the cap is an alphabetical slice
// of its namespaces. What reaches the archive must be the newest, which is the
// only claim the History tab and get_event_history make about it.
func TestTheArchiveGetsTheNewestEventsAndNotTheFirstPage(t *testing.T) {
	base := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)

	// Named in key order and stamped in the same order, so that a cut which
	// keeps the head of the list keeps the *oldest*, which is what a page of a
	// key-ordered list is.
	var events []*corev1.Event
	for i := range maxEvents + 100 {
		name := fmt.Sprintf("%05d-pod.evt", i)
		events = append(events, reportedEvent(name, base.Add(time.Duration(i)*time.Second)))
	}

	store := &fakeStore{on: true, writable: true}
	r := New(Options{Clusters: clusterOf("prod", events...), Store: store, Logf: func(string, ...any) {}})

	if got := r.Round(context.Background()); got != maxEvents {
		t.Fatalf("the round stored %d events, want %d", got, maxEvents)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.batches) != 1 {
		t.Fatalf("%d batches handed over, want 1", len(store.batches))
	}
	batch := store.batches[0]
	if len(batch) != maxEvents {
		t.Fatalf("the batch holds %d events, want %d", len(batch), maxEvents)
	}

	// The oldest hundred are the ones dropped, and the most recent of all is
	// certainly there.
	oldest := base.Add(100 * time.Second)
	for _, e := range batch {
		if e.Last.Before(oldest) {
			t.Fatalf("%s was archived at %s, older than %s — the cut kept a page rather than the newest",
				e.Object, e.Last, oldest)
		}
	}
	newest := base.Add(time.Duration(maxEvents+99) * time.Second)
	found := false
	for _, e := range batch {
		if e.Last.Equal(newest) {
			found = true
		}
	}
	if !found {
		t.Error("the most recent event of all is not in the archive")
	}
}

// A list longer than one round is willing to read is not "the newest N", and
// saying so is the difference between an archive that states which N it has and
// one that claims the wrong thing.
func TestAnEventListTooLongToReadIsReportedRatherThanPassedOffAsTheNewest(t *testing.T) {
	base := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	page := 0

	// An API server that honors the limit and never runs out: every page hands
	// back a continue token, which is what a cluster mid-incident looks like.
	endless := func(_ context.Context, opts metav1.ListOptions) (*corev1.EventList, error) {
		page++
		out := &corev1.EventList{}
		out.Continue = fmt.Sprintf("token-%d", page)
		for i := range int(opts.Limit) {
			at := base.Add(time.Duration(page*1000+i) * time.Second)
			out.Items = append(out.Items, *reportedEvent(fmt.Sprintf("p%d-%d", page, i), at))
		}
		return out, nil
	}

	got, note, err := newestEvents(context.Background(), endless)
	if err != nil {
		t.Fatalf("newestEvents: %v", err)
	}
	if page != maxEventPages {
		t.Errorf("read %d pages, want to stop at %d — one cluster must not spend the whole per-cluster "+
			"timeout paging", page, maxEventPages)
	}
	if len(got) != maxEvents {
		t.Errorf("kept %d events, want %d", len(got), maxEvents)
	}
	if note == "" {
		t.Fatal("a truncated read was reported as a complete one; the archive then says it holds the " +
			"last N when it holds some N")
	}
	if !strings.Contains(note, "not necessarily the newest") {
		t.Errorf("the note does not say what is uncertain: %q", note)
	}

	// And a complete read says nothing at all, so the page is not permanently
	// decorated with a caveat that does not apply.
	short := func(_ context.Context, _ metav1.ListOptions) (*corev1.EventList, error) {
		return &corev1.EventList{Items: []corev1.Event{*reportedEvent("only", base)}}, nil
	}
	if _, note, err := newestEvents(context.Background(), short); err != nil || note != "" {
		t.Errorf("a complete read reported %q, %v", note, err)
	}
}

// A cluster whose list could not be read to the end is named, once, and said to
// be answering — because it is. The two are different facts and the page shows
// them differently.
func TestAnIncompleteScanIsNamedOnceAndIsNotAFailure(t *testing.T) {
	lines := &log{}
	r := New(Options{Store: &fakeStore{on: true, writable: true}, Logf: lines.write})

	r.scanned("prod", "more than 20000 events were reported and only the first 20000 were read, so "+
		"these are the newest of what was read and not necessarily the newest the cluster has")
	r.scanned("prod", "more than 20000 events were reported and only the first 20000 were read, so "+
		"these are the newest of what was read and not necessarily the newest the cluster has")

	if got := lines.count("prod"); got != 1 {
		t.Errorf("two rounds of the same truncation produced %d lines, want 1", got)
	}
	status := r.Status()
	if len(status.Incomplete) != 1 || status.Incomplete[0].Cluster != "prod" {
		t.Fatalf("Incomplete = %+v, want the one cluster that was truncated", status.Incomplete)
	}
	if len(status.Failing) != 0 {
		t.Errorf("a truncated read was reported as a failure: %+v", status.Failing)
	}

	// And it goes away when the cluster quietens down, rather than staying on
	// the page as a permanent caveat.
	r.scanned("prod", "")
	if len(r.Status().Incomplete) != 0 {
		t.Error("the note outlived the incident")
	}
}

// The whole list is followed, not the first page: an API server that honors
// `Limit` hands back a token, and the events on the pages behind it are the
// ones an operator comes back for.
func TestEveryPageIsFollowed(t *testing.T) {
	base := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	pages := [][]corev1.Event{
		{*reportedEvent("a", base)},
		{*reportedEvent("b", base.Add(time.Minute))},
		{*reportedEvent("c", base.Add(2*time.Minute))},
	}
	at := 0
	list := func(_ context.Context, opts metav1.ListOptions) (*corev1.EventList, error) {
		if at > 0 && opts.ResourceVersion != "" {
			t.Error("the continue token was sent with a resourceVersion, which the API server refuses")
		}
		out := &corev1.EventList{Items: pages[at]}
		at++
		if at < len(pages) {
			out.Continue = "more"
		}
		return out, nil
	}

	got, note, err := newestEvents(context.Background(), list)
	if err != nil || note != "" {
		t.Fatalf("newestEvents: %q, %v", note, err)
	}
	if len(got) != 3 {
		t.Fatalf("kept %d of 3 events; the pages behind the first were dropped", len(got))
	}
}

// --- the switch, and what a round leaves behind ---------------------------------

// truncatedCluster is one context whose event list never runs out of continue
// tokens, so every round against it stops at the page ceiling and records that
// it did. The note it leaves must not outlive the switch either.
func truncatedCluster(name string) fakeClusters {
	set := fake.NewClientset()
	set.PrependReactor("list", "events", func(k8stesting.Action) (bool, runtime.Object, error) {
		list := &corev1.EventList{}
		list.Continue = "more"
		return true, list, nil
	})
	return fakeClusters{clients: map[string]kubernetes.Interface{name: set}}
}

// `Writable()` answers `(true, "")` on a switched-off archive *precisely so* a
// standing refusal clears, so Round must still ask it when the archive is off.
// Otherwise a full disk followed by turning the archive off in Settings leaves
// the health page saying both "not collecting — the archive is off" and "the
// archive is refusing writes" until a restart, and the stale scan notes survive
// the same way.
//
// What the reader sees is asserted in internal/web, on the rendered page. This is
// the same walk one layer down, where the transition itself lives.
func TestSwitchingTheArchiveOffClearsWhatTheLastRoundLeftBehind(t *testing.T) {
	const full = "events for prod could not be written to /state/archive: no space left on device"

	type state struct {
		on       bool
		writable bool
		why      string
	}
	tests := []struct {
		name    string
		history []state
		// wantStorageOK is what the page reads to decide whether to draw the
		// refusal at all.
		wantStorageOK bool
		wantDetail    string
		// wantIncomplete is whether the "read to the ceiling" note survives.
		wantIncomplete bool
	}{
		{
			name:           "enabled and writable",
			history:        []state{{on: true, writable: true}},
			wantStorageOK:  true,
			wantIncomplete: true,
		},
		{
			name:           "enabled and refusing",
			history:        []state{{on: true, writable: false, why: full}},
			wantStorageOK:  false,
			wantDetail:     "no space left on device",
			wantIncomplete: true,
		},
		{
			name:           "disabled after refusing",
			history:        []state{{on: true, writable: false, why: full}, {on: false}},
			wantStorageOK:  true,
			wantIncomplete: false,
		},
		{
			name:           "disabled having never refused",
			history:        []state{{on: false}},
			wantStorageOK:  true,
			wantIncomplete: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			r := New(Options{
				// A cluster whose event list never ends, so an enabled round
				// genuinely leaves an incomplete-scan note behind rather than
				// one planted by the test.
				Clusters: truncatedCluster("prod"),
				Store:    store,
				Logf:     func(string, ...any) {},
			})

			for _, s := range tc.history {
				store.mu.Lock()
				store.on, store.writable, store.why = s.on, s.writable, s.why
				// The store's own rule: a disabled archive is writable, because being switched
				// off is a setting rather than a fault.
				if !s.on {
					store.writable, store.why = true, ""
				}
				store.mu.Unlock()

				r.Round(context.Background())
			}

			status := r.Status()
			if status.Storage.OK != tc.wantStorageOK {
				t.Errorf("Storage.OK = %v, want %v — detail %q", status.Storage.OK, tc.wantStorageOK,
					status.Storage.Detail)
			}
			if tc.wantDetail != "" && !strings.Contains(status.Storage.Detail, tc.wantDetail) {
				t.Errorf("Storage.Detail = %q, which does not say what to fix", status.Storage.Detail)
			}
			if tc.wantStorageOK && status.Storage.Detail != "" {
				t.Errorf("the archive is taking writes and still carries %q", status.Storage.Detail)
			}
			if tc.wantStorageOK && !status.Storage.Since.IsZero() {
				t.Error("Storage.Since outlived the refusal, so the page can date a problem that is over")
			}
			if got := len(status.Incomplete) > 0; got != tc.wantIncomplete {
				t.Errorf("an incomplete-scan note is present = %v, want %v: %+v",
					got, tc.wantIncomplete, status.Incomplete)
			}
		})
	}
}

// And the line that goes with it. A switched-off archive answers "writable", so
// a naive clearing would announce "the store is taking writes again" about a
// store writing nothing at all.
func TestSwitchingTheArchiveOffIsNotAnnouncedAsTheDiskBeingFixed(t *testing.T) {
	lines := &log{}
	store := &fakeStore{on: true, writable: false, why: "no space left on device"}
	r := New(Options{
		Clusters: clusterOf("prod", reportedEvent("web.1", time.Now())),
		Store:    store,
		Logf:     lines.write,
	})

	r.Round(context.Background())
	if lines.count("not taking writes") != 1 {
		t.Fatalf("the refusal was not reported once:\n%s", lines.all())
	}

	store.mu.Lock()
	store.on, store.writable, store.why = false, true, ""
	store.mu.Unlock()
	r.Round(context.Background())

	if got := lines.count("taking writes again"); got != 0 {
		t.Errorf("turning the archive off logged %d recovery lines:\n%s", got, lines.all())
	}

	// And the silence is a reset rather than a permanent one: switch the archive
	// back on with the disk still full and the refusal is said again. Clearing
	// the flag must not buy quiet at the price of the one line that says nothing
	// is being kept.
	store.mu.Lock()
	store.on, store.writable, store.why = true, false, "no space left on device"
	store.mu.Unlock()
	r.Round(context.Background())

	if got := lines.count("not taking writes"); got != 2 {
		t.Errorf("the archive came back to a disk that is still full and the log said so %d times, "+
			"want 2:\n%s", got, lines.all())
	}
	if r.Status().Storage.OK {
		t.Error("the refusal did not come back on the page either")
	}
}
