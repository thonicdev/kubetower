// Package overview builds the two cluster dashboards: one about the machines a
// cluster is made of, one about what is running on them.
//
// An empty answer and a failed one are different answers: a dashboard that
// cannot reach a cluster returns an error and draws nothing, one that reached
// it and found nothing says so, and a zero is only drawn when the cluster said
// zero. No metrics backend is not a failure: panels that need usage figures
// carry a typed reason for the absence and print an em dash.
package overview

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/internal/resources"
	"github.com/thonicdev/kubetower/textutil"
)

// The health classes are the listing layer's, so a dashboard never invents a
// fourth word for "broken" that nothing styles.
const (
	ClassNone = resources.ClassNone
	ClassOK   = resources.ClassOK
	ClassWarn = resources.ClassWarn
	ClassBad  = resources.ClassBad
)

// ClassMuted is a value neither good nor bad, such as a Succeeded pod: it is
// finished, not healthy.
const ClassMuted = "muted"

// missing is what an absent figure prints as. A blank reads as a rendering
// fault; an em dash reads as "the cluster did not say".
const missing = "—"

// ErrUnavailable marks the absence of a metrics backend, so a caller can tell it
// apart from a backend that was there and failed. Panels match on it with
// errors.Is and print an em dash rather than an alarm.
var ErrUnavailable = errors.New("no metrics backend")

// --- what a caller must pass in ------------------------------------------------

// Metrics is the metrics backend, as this package needs it. It speaks only in
// bytes and stdlib types, so one object can satisfy this and other interfaces
// without an adapter; which backends answer is worked out by Availability.
// nil means no Prometheus and is checked for, never dereferenced.
type Metrics interface {
	Query(ctx context.Context, cluster, promQL string) ([]byte, error)
	QueryRange(ctx context.Context, cluster, promQL string, start, end time.Time, step time.Duration) ([]byte, error)
}

// Availability is whether one kind of usage figure can be had from this
// cluster, established by asking. metrics-server and Prometheus are judged
// separately, since a cluster may have either, both or neither. Detail always
// says why.
type Availability struct {
	OK     bool
	Detail string
	// Cause is the failure a backend answered with (a 403, a 503, a timeout,
	// the access review's refusal) for internal/web's stateOf to classify, since
	// the sentence alone cannot tell them apart. Nil when OK and where there is
	// only a sentence (no client, a panic).
	Cause error
	// Resource is what Cause answered, as a failure's scope names it
	// (`pods.metrics.k8s.io`, `pods`), so a refusal names the missing
	// permission. Set with Cause, by failedWith.
	Resource string
}

// available and unavailable exist so the two spellings cannot drift apart.
func available(detail string) Availability   { return Availability{OK: true, Detail: detail} }
func unavailable(detail string) Availability { return Availability{Detail: detail} }

// failedWith is unavailable, keeping the error it was read from and what it
// was a read of.
func failedWith(err error, resource string) Availability {
	return Availability{Detail: firstLine(err.Error()), Cause: err, Resource: resource}
}

// Err wraps ErrUnavailable, so errors.Is tells a missing backend apart from
// one that failed: an em dash rather than an alarm.
func (a Availability) Err() error {
	if a.OK {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrUnavailable, a.Detail)
}

// --- figures -------------------------------------------------------------------

// Unit says what a Capacity's integers count. Millicores and bytes stay
// integers until formatting, so a total agrees with the sum of its rows.
type Unit int

const (
	UnitCount Unit = iota
	UnitCores
	UnitBytes
	// UnitBytesPerSecond is a rate: format prints it with "/s", so a rate never
	// reads as a total on an axis, a caption or the zoom's readout.
	UnitBytesPerSecond
)

// Capacity is "x of y", in one unit. Total == 0 means the cluster reported no
// capacity, drawn as "no data" and printed as an em dash.
type Capacity struct {
	// What this measures: "CPU", "Memory", "Pods". Read aloud by the charts.
	Name  string
	Unit  Unit
	Used  int64
	Total int64
}

// Known reports whether the cluster stated a total.
func (c Capacity) Known() bool { return c.Total > 0 }

// Fraction is Used/Total, uncapped: a cluster whose pods request more than its
// nodes offer is a real and common state, and rounding it down to 100% hides
// exactly the thing worth seeing.
func (c Capacity) Fraction() float64 {
	if !c.Known() {
		return 0
	}
	return float64(c.Used) / float64(c.Total)
}

func (c Capacity) Percent() int { return int(c.Fraction()*100 + 0.5) }

// Class grades the pressure. The thresholds are about scheduling, not aesthetics:
// past 90% requested, the next pod of any size has nowhere to go.
func (c Capacity) Class() string {
	if !c.Known() {
		return ClassNone
	}
	switch f := c.Fraction(); {
	case f >= 0.90:
		return ClassBad
	case f >= 0.75:
		return ClassWarn
	default:
		return ClassOK
	}
}

func (c Capacity) UsedText() string  { return format(c.Used, c.Unit) }
func (c Capacity) TotalText() string { return format(c.Total, c.Unit) }

// Text is the figure as a line of prose: "3.2 of 8 cores".
func (c Capacity) Text() string {
	if !c.Known() {
		return missing
	}
	return c.UsedText() + " of " + c.TotalText() + noun(c.Unit)
}

// Label is the sentence a screen reader gets for the chart.
func (c Capacity) Label() string {
	name := c.Name
	if name == "" {
		name = "Usage"
	}
	if !c.Known() {
		return name + ": the cluster reported no capacity"
	}
	return fmt.Sprintf("%s: %s used, %d%%", name, c.Text(), c.Percent())
}

// Point is one sample. At is kept because a gap in scraping is information: the
// sparkline places points by time, so a five-minute hole in the series looks
// like a five-minute hole and not like a steeper line.
type Point struct {
	At    time.Time
	Value float64
}

// History is one series over a window, or the reason there isn't one.
// Unavailable is a string because it is displayed, not handled.
type History struct {
	Title  string
	Unit   Unit
	Points []Point
	// Set when there is nothing to draw and why. Empty Points with an empty
	// Unavailable means the backend answered and the window was genuinely empty.
	Unavailable string
	// Step the samples were asked for at. Reported so the chart can say how many
	// samples it is drawing rather than implying a resolution it does not have.
	Step time.Duration
	// Missing says which kind of reason Unavailable is, for a caller that has to
	// branch on it rather than print it. Zero where nothing was established.
	Missing Missing
	// Err is the failure where Missing is MissingFailed, for internal/web's
	// stateOf to classify. Never printed; Unavailable is the sentence.
	Err error `json:"-"`
	// Period is the key of the period that shows what this empty line could
	// not, where one does: set by ExplainRun on a finished pod's line, never on
	// a line that drew. The client draws it as a button beside the sentence.
	Period string
	// RunTotal is a counter's whole run, set by CountRun on a finished pod's
	// empty network line where Prometheus holds a sample of the run. Nil
	// otherwise, and then nothing is shown in its place.
	RunTotal *RunTotal
}

// Missing is why a series has nothing to draw, as a value a caller can branch
// on and the API maps to a stable reason code; the Unavailable sentence may be
// reworded.
type Missing int

const (
	// MissingNone: drawn, or nothing was established about the absence.
	MissingNone Missing = iota
	// MissingEmpty: the backend answered and the window holds no sample.
	MissingEmpty
	// MissingCounter: this Prometheus does not export what the query needs.
	MissingCounter
	// MissingBackend: there is no Prometheus to ask - no backend configured, or
	// a cluster with metrics-server alone. No span of history can be chosen.
	MissingBackend
	// MissingFailed: a Prometheus is there and this query did not answer.
	MissingFailed
	// MissingNotAsked: this render did not ask for the series.
	MissingNotAsked
)

// Segment is one slice of a breakdown, for the stacked bar.
type Segment struct {
	Label string
	Value int
	Class string
}

// --- what went wrong, without losing the rest ----------------------------------

// PanelError is one panel that could not be filled, on a page whose other
// panels could: one forbidden read must not blank the rest, nor leave an empty
// list looking like a quiet cluster.
type PanelError struct {
	Panel  string
	Reason string
}

// The panels degrade can name. They are constants because internal/web's
// spoiltBy keys on them, and a copy of the printed label would stop matching
// silently when the wording changes. A new panel is added here, to
// DegradablePanels and to that map; tests fail if either is skipped.
const (
	PanelVersion      = "Kubernetes version"
	PanelEvents       = "Warning events"
	PanelRequests     = "Requested CPU and memory"
	PanelDeployments  = "Deployments"
	PanelStatefulSets = "StatefulSets"
	PanelDaemonSets   = "DaemonSets"
	PanelJobs         = "Jobs"
	PanelCronJobs     = "CronJobs"
)

// DegradablePanels is every panel this package can put in a Degraded slice, so
// that a caller mapping panels to what they spoil is checked against the whole
// set rather than against the ones somebody remembered.
var DegradablePanels = []string{
	PanelVersion,
	PanelEvents,
	PanelRequests,
	PanelDeployments,
	PanelStatefulSets,
	PanelDaemonSets,
	PanelJobs,
	PanelCronJobs,
}

// degrade records a panel's failure, keeping the first line of the error: a
// Kubernetes error runs to a paragraph and the first line is the one that names
// the problem.
func degrade(into *[]PanelError, panel string, err error) {
	if err == nil {
		return
	}
	*into = append(*into, PanelError{Panel: panel, Reason: firstLine(err.Error())})
}

// firstLine cuts on "\r" as well as "\n" and caps at detailLine: an
// Unavailable sentence sits in a fixed-width tile, where an uncapped one
// reflows the chart beside it.
func firstLine(s string) string { return textutil.FirstLineN(s, detailLine) }

// detailLine is the 200 that internal/metrics and internal/health use for the
// same sentences — this package reads from both.
const detailLine = 200

// Alert is something the operator must not be able to scroll past. Kind and
// Name identify Subject for a link; they are never parsed back out of Subject.
type Alert struct {
	Class   string
	Subject string
	Summary string
	Detail  string

	// Kind is the Kubernetes kind of Subject, singular and as Kubernetes spells
	// it: "Node". Empty means this alert is not about one addressable object.
	Kind string
	// Name is the object's own name. Cluster-scoped kinds have no namespace and
	// this struct deliberately carries no field for one.
	Name string
}

// EventLine is one warning from the cluster's event stream. Kind and Name are
// the involved object's identity, kept apart from the printed Object, so a
// kind this console does not serve produces no link rather than a wrong one.
type EventLine struct {
	When      time.Time
	Age       string
	Namespace string
	Object    string
	Kind      string
	Name      string
	Reason    string
	Message   string
	Count     int32
	Class     string
}

// --- helpers shared by both dashboards -----------------------------------------

// task is one unit of work in a gather, paired with where its panic is
// reported. A bare func() has no failure channel, so a recover could only
// swallow the panic and turn it into an empty success.
type task struct {
	run  func()
	fail func(crash any)
}

// attempt is the general form, for a task whose failure is more than one field.
func attempt(run func(), fail func(crash any)) task { return task{run: run, fail: fail} }

// guard is a task whose failure is an error variable. what names the call in
// words the page may print ("the node list"): it ends up in a PanelError.
func guard(err *error, what string, run func()) task {
	return attempt(run, func(crash any) {
		*err = fmt.Errorf("%s panicked: %s", what, firstLine(fmt.Sprint(crash)))
	})
}

// degrading is a task whose failure is an Availability: the history charts and
// usage boards report an absence in words without going red. A panic is said
// as such, and kept as a typed Cause so internal/web classifies it as a failed
// read rather than an absent backend.
func degrading(av *Availability, what string, run func()) task {
	return attempt(run, func(crash any) {
		failure := Crash{What: what, Value: firstLine(fmt.Sprint(crash))}
		*av = Availability{Detail: failure.Error(), Cause: failure}
	})
}

// Crash is a gathered task that panicked, as an error: what was being read and
// what the panic said. A defect in this console and never a fact about the
// cluster, so a caller classifying it must not read it as an absence.
type Crash struct {
	What  string
	Value string
}

func (c Crash) Error() string { return c.What + " panicked: " + c.Value }

// gather runs independent fetches at once and confines a panic in any of them
// to its own panel.
//
// The recover lives here because a panic on a goroutine this function started
// is seen by nothing: net/http's handler recovery is on another goroutine, so
// the runtime would end the whole process. A recovered panic is written through
// t.fail where the panel already looks for a reason, never turned into an
// empty success, and its stack is logged. A task with no fail has its panic
// re-raised on the caller's goroutine once every task has finished, where
// net/http can see it.
func gather(tasks ...task) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var unhandled any

	wg.Add(len(tasks))
	for _, t := range tasks {
		go func() {
			defer wg.Done()
			defer func() {
				crash := recover()
				if crash == nil {
					return
				}
				log.Printf("overview: a gathered task panicked and was confined to its panel: %v\n%s",
					crash, debug.Stack())
				if t.fail == nil {
					mu.Lock()
					if unhandled == nil {
						unhandled = crash
					}
					mu.Unlock()
					return
				}
				t.fail(crash)
			}()
			t.run()
		}()
	}
	wg.Wait()

	if unhandled != nil {
		panic(unhandled)
	}
}

// --- one pod list per refresh, not two -----------------------------------------

// podShare collapses concurrent identical pod lists. Both dashboards want
// every pod in the same scope and are drawn at the same moment, and two tabs
// on one cluster overlap too, so the second caller waits for the first. There
// is no cache: the entry is gone the moment the list lands. The slice is
// shared, so nothing downstream may write to it.
//
// The fetch is detached from the leader's cancellation, so a tab navigating
// away does not make another tab draw `context canceled` over a healthy
// cluster. It keeps a deadline, so a fetch that never returns cannot wedge the
// entry. A waiter whose own context ends leaves without disturbing the fetch.
type podShare struct {
	mu      sync.Mutex
	waiting map[string]*podFetch
}

type podFetch struct {
	done chan struct{}
	pods []corev1.Pod
	err  error
}

var podLists = &podShare{waiting: map[string]*podFetch{}}

// podShareCeiling bounds a fetch that has outlived the request that started
// it, when that request had no deadline: long enough for a large cluster's pod
// list, short enough that a wedged one cannot hold the entry.
const podShareCeiling = 30 * time.Second

func (s *podShare) list(ctx context.Context, cluster, namespace string, fetch func(context.Context) ([]corev1.Pod, error)) (pods []corev1.Pod, err error) {
	key := cluster + "\x00" + namespace

	s.mu.Lock()
	if f := s.waiting[key]; f != nil {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-f.done:
		}
		return f.pods, f.err
	}
	f := &podFetch{done: make(chan struct{})}
	s.waiting[key] = f
	s.mu.Unlock()

	// Detached from the leader's cancellation, keeping the tighter of its
	// deadline and the ceiling. Deferred before the cleanup so it runs after it.
	until := time.Now().Add(podShareCeiling)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(until) {
		until = deadline
	}
	fetchCtx, release := context.WithDeadline(context.WithoutCancel(ctx), until)
	defer release()

	// Deferred so a panicking fetch still removes the entry and closes done;
	// otherwise every later request for this key would block. The panic becomes
	// an error, never (nil, nil), which would read as "this cluster runs no
	// pods"; unrecovered it would end the process, as gather explains.
	defer func() {
		if r := recover(); r != nil {
			f.pods, f.err = nil, fmt.Errorf("listing the pods of %s panicked: %v", cluster, r)
			// The named results too: after a recovered panic they are what the
			// function returns, and nil would read as "this cluster runs no pods".
			pods, err = f.pods, f.err
		}
		s.mu.Lock()
		delete(s.waiting, key)
		s.mu.Unlock()
		close(f.done)
	}()

	f.pods, f.err = fetch(fetchCtx)
	return f.pods, f.err
}

func noun(u Unit) string {
	switch u {
	case UnitCores:
		return " cores"
	default:
		return ""
	}
}

func format(v int64, u Unit) string {
	switch u {
	case UnitCores:
		return formatCores(v)
	case UnitBytes:
		return formatBytes(v)
	case UnitBytesPerSecond:
		return formatBytes(v) + "/s"
	default:
		return strconv.FormatInt(v, 10)
	}
}

// formatCores prints millicores the way a request is written: under a core in
// the millicores the YAML used, above it in cores, which is how node capacity is
// discussed.
func formatCores(milli int64) string {
	if milli < 1000 {
		return strconv.FormatInt(milli, 10) + "m"
	}
	return trimZero(strconv.FormatFloat(float64(milli)/1000, 'f', 1, 64))
}

// formatBytes is the compact form ("8 GiB") that format and the hottest-pods
// boards print. The Capacity tile and the node table use internal/resources'
// spelling instead, to match the Nodes list.
func formatBytes(b int64) string { return textutil.BytesCompact(b) }

// trimZero drops a trailing ".0". "8 cores" and "8.0 cores" say the same thing
// and only one of them reads like a figure a human chose.
func trimZero(s string) string {
	return strings.TrimSuffix(s, ".0")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
