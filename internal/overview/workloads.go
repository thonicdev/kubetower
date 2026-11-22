package overview

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/resources"
)

// WorkloadsOverview is what is running: how much of each kind, how much of it
// is healthy, and which particular things are not (Trouble, the panel that
// matters; the counts say how much of the cluster it represents).
type WorkloadsOverview struct {
	Cluster string
	// Empty means every namespace.
	Namespace string
	Collected time.Time

	Kinds []KindSummary
	// Phases is the pod breakdown, in the order a stacked bar should draw it.
	Phases []Segment
	// Pods repeats the total, because a bar without its total is a shape.
	Pods int

	// Trouble is everything not in the state it is meant to be in, worst first.
	Trouble []Trouble
	// Restarting is the pods whose containers keep dying: the cheapest early
	// signal, invisible in a phase breakdown where CrashLoopBackOff counts as
	// Running.
	Restarting []Restarting
	// RestartingTotal is how many restarting containers there were before
	// Restarting was cut to restartsShown, so the page can say it is not whole.
	RestartingTotal int

	// Usage says whether instant figures could be had; false with a reason is
	// the ordinary state without metrics-server.
	Usage     Availability
	TopCPU    Top
	TopMemory Top

	Degraded []PanelError

	// Access is what the access review did to the lists this page is built
	// from: Narrowed when one holds only the namespaces the person may list,
	// and every count and panel is then about those.
	Access access.Visible
}

// KindSummary is one row of the counts panel.
type KindSummary struct {
	// Kind as printed: "Deployments".
	Kind string
	// Slug as the listing layer spells it, so a tile can link to the real list.
	Slug string

	Total   int
	Healthy int
	// Note says what the rest are, as printed ("2 not ready"); empty when
	// everything is as it should be.
	Note  string
	Class string
}

// Trouble is one object that is not in its intended state.
type Trouble struct {
	Kind      string
	Namespace string
	Name      string
	// Detail is the sentence: "1 of 3 replicas ready".
	Detail string
	Class  string
	// Since is when the object left the state it declares, read from the
	// object's own record of the transition, never from this page's clock.
	// Zero where the kind keeps no such record, and the row is undated rather
	// than reading "0s" over an old fault. Per kind:
	//   - Deployment: the Available condition's lastTransitionTime while it is
	//     False, else Progressing's when it reads ProgressDeadlineExceeded. One
	//     still Available is a rollout inside its budget and has no date.
	//   - StatefulSet, DaemonSet: the status carries no time, so the newest
	//     non-ready pod it owns lends its startTime; the newest, so the trouble
	//     is never claimed older than what it can point at.
	//   - Job: when it failed (resources.JobStopped), not when it started.
	//   - Pod: none.
	Since time.Time
	// Age is Since as "3d" or "47m", measured from the page's collection
	// instant like every other age on it; empty when Since is zero.
	Age string
}

// Restarting is one pod's restart count, with the reason if the cluster gave one.
type Restarting struct {
	Namespace string
	Name      string
	Container string
	// Init marks a container filed under initContainerStatuses: a plain init
	// container or a native sidecar. Always written, including when false:
	// a stuck init container is why a pod is stuck, and the row must say
	// which logs to read.
	Init     bool
	Restarts int32
	// Reason is the last termination's, when there was one: OOMKilled and
	// Error send an operator to two different places.
	Reason string
	// LastExit is the kubelet's stamp on that termination
	// (lastState.terminated.finishedAt). Zero when the kubelet no longer holds
	// the termination (it keeps only the latest, and a reboot loses it) or
	// stamped no time on it; it is then sent as absent, never as a time, and
	// AgoTitle says which of the two it was.
	LastExit time.Time
	// Ago is when that termination was ("4m", "3d"), and AgoTitle the same
	// instant written out for the tooltip: a count alone cannot tell old
	// crashes from a live crash loop.
	Ago      string
	AgoTitle string
	Class    string
}

// restartsShown bounds the restart panel's payload; the page draws it 20 at a
// time. Rows arrive worst first, so the cut only drops the quietest.
const restartsShown = 200

// Workloads gathers the workload-side dashboard. namespace scopes it; empty
// means the whole cluster. metrics may be nil: it is only the hottest-pods
// panel's second source, after metrics-server.
//
// It returns an error only when the pod list fails; every other list degrades
// its own panel, since the page is worth nothing without the pods.
func Workloads(ctx context.Context, gate *access.Gate, client *cluster.Client, namespace string, metrics Metrics) (WorkloadsOverview, error) {
	if client == nil || client.Clients == nil {
		return WorkloadsOverview{}, fmt.Errorf("no client for this cluster")
	}
	r := resources.ReaderFor(gate, client)

	out := WorkloadsOverview{
		Cluster:   client.Context,
		Namespace: namespace,
		Collected: time.Now(),
	}

	var (
		pods         []corev1.Pod
		podErr       error
		deployments  []appsv1.Deployment
		deployErr    error
		statefulSets []appsv1.StatefulSet
		statefulErr  error
		daemonSets   []appsv1.DaemonSet
		daemonErr    error
		jobs         []batchv1.Job
		jobErr       error
		cronJobs     []batchv1.CronJob
		cronErr      error
		usage        []PodUsage
		seen         [7]access.Visible
	)

	// Every task names where its failure goes; see gather.
	gather(
		// Shared with the cluster dashboard's list (see podShare, which
		// recovers on its own account since its waiter is outside this gather).
		guard(&podErr, "the pod list", func() { pods, seen[0], podErr = listPods(ctx, r, namespace) }),
		guard(&deployErr, "the Deployment list", func() { deployments, seen[1], deployErr = listDeployments(ctx, r, namespace) }),
		guard(&statefulErr, "the StatefulSet list", func() { statefulSets, seen[2], statefulErr = listStatefulSets(ctx, r, namespace) }),
		guard(&daemonErr, "the DaemonSet list", func() { daemonSets, seen[3], daemonErr = listDaemonSets(ctx, r, namespace) }),
		guard(&jobErr, "the Job list", func() { jobs, seen[4], jobErr = listJobs(ctx, r, namespace) }),
		guard(&cronErr, "the CronJob list", func() { cronJobs, seen[5], cronErr = listCronJobs(ctx, r, namespace) }),
		// No error of its own: the leaderboards say why they are empty
		// through an Availability.
		degrading(&out.Usage, "the pod usage read", func() { usage, seen[6], out.Usage = podUsage(ctx, r, namespace) }),
	)
	for _, v := range seen {
		out.Access = out.Access.Merge(v)
	}

	if podErr != nil {
		return WorkloadsOverview{}, fmt.Errorf("listing the pods of %s: %w", client.Context, podErr)
	}

	degrade(&out.Degraded, PanelDeployments, deployErr)
	degrade(&out.Degraded, PanelStatefulSets, statefulErr)
	degrade(&out.Degraded, PanelDaemonSets, daemonErr)
	degrade(&out.Degraded, PanelJobs, jobErr)
	// A cluster that does not serve batch/v1 CronJobs has no CronJob panel to
	// be missing: the kind is left out. A refusal or a failed read still
	// degrades.
	cronServed := !apierrors.IsNotFound(cronErr)
	if cronServed {
		degrade(&out.Degraded, PanelCronJobs, cronErr)
	}

	// A kind whose list failed is left out of the counts rather than counted
	// as zero: Degraded says it is missing, a zero would say there are none.
	if deployErr == nil {
		out.Kinds = append(out.Kinds, deploymentSummary(deployments))
		out.Trouble = append(out.Trouble, deploymentTrouble(deployments, out.Collected)...)
	}
	if statefulErr == nil {
		out.Kinds = append(out.Kinds, statefulSetSummary(statefulSets))
		out.Trouble = append(out.Trouble, statefulSetTrouble(statefulSets, pods, out.Collected)...)
	}
	if daemonErr == nil {
		out.Kinds = append(out.Kinds, daemonSetSummary(daemonSets))
		out.Trouble = append(out.Trouble, daemonSetTrouble(daemonSets, pods, out.Collected)...)
	}
	if jobErr == nil {
		out.Kinds = append(out.Kinds, jobSummary(jobs))
		out.Trouble = append(out.Trouble, jobTrouble(jobs, out.Collected)...)
	}
	if cronServed && cronErr == nil {
		out.Kinds = append(out.Kinds, cronJobSummary(cronJobs))
	}
	out.Kinds = append(out.Kinds, podSummary(pods))
	out.Trouble = append(out.Trouble, podTrouble(pods)...)

	// Worst first, then oldest first. The tone is the outer key so an undated
	// kind (every Pod) cannot sink under milder dated rows; within a tone an
	// undated row comes after the dated ones, since "no record" is not "zero
	// seconds".
	sort.SliceStable(out.Trouble, func(i, j int) bool {
		a, b := out.Trouble[i], out.Trouble[j]
		if ra, rb := rank(a.Class), rank(b.Class); ra != rb {
			return ra < rb
		}
		if a.Since.IsZero() != b.Since.IsZero() {
			return !a.Since.IsZero()
		}
		if !a.Since.Equal(b.Since) {
			return a.Since.Before(b.Since)
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})

	out.Pods = len(pods)
	out.Phases = phaseBreakdown(pods)
	out.Restarting, out.RestartingTotal = restarting(pods, restartsShown, out.Collected)

	var boards access.Visible
	out.TopCPU, out.TopMemory, out.Usage, boards = topBoards(ctx, r, metrics, usage, out.Usage)
	out.Access = out.Access.Merge(boards)

	return out, nil
}

// TopPods is the two leaderboards alone, for a page that wants the hottest
// pods without paying for the rest of the workloads dashboard. The backend
// choice is shared with Workloads (topBoards), so both rank alike.
//
// The Availability says whether anything answered at all: a caller ignoring it
// would report "nothing is using anything" about a backend it never reached.
// Both backends are narrowed for the person behind ctx, and Visible says so.
func TopPods(ctx context.Context, gate *access.Gate, client *cluster.Client, namespace string, metrics Metrics) (Top, Top, Availability, access.Visible) {
	if client == nil || client.Clients == nil {
		return Top{}, Top{}, unavailable("no client for this cluster"), access.Visible{}
	}
	r := resources.ReaderFor(gate, client)
	// Confined like every gathered read: the caller runs this on its own
	// goroutine, where an unrecovered panic ends the process. A crash is the
	// board's failure, carried as a Crash, never "no metrics backend".
	var (
		cpu, memory Top
		answered    Availability
		seen        access.Visible
	)
	gather(degrading(&answered, "the pod usage read", func() {
		usage, visible, av := podUsage(ctx, r, namespace)
		var boards access.Visible
		cpu, memory, answered, boards = topBoards(ctx, r, metrics, usage, av)
		seen = visible.Merge(boards)
	}))
	if !answered.OK && cpu.Unavailable == "" {
		cpu.Unavailable, memory.Unavailable = answered.Detail, answered.Detail
	}
	return cpu, memory, answered, seen
}

// topBoards ranks what metrics-server gave, or falls back to Prometheus, or
// says why there is nothing; shared by Workloads and TopPods.
//
// metrics-server's figures arrive narrowed by podUsage's review. Prometheus's
// do not (the console reads its own monitoring backend), so they are narrowed
// here by whether the person may list pods in each pod's namespace.
func topBoards(ctx context.Context, r resources.Reader, metrics Metrics, usage []PodUsage, av Availability) (Top, Top, Availability, access.Visible) {
	clusterName := r.Cluster
	switch {
	case av.OK:
		return Top{Pods: leaders(usage, func(p PodUsage) int64 { return p.CPUMilli }, topPods)},
			Top{Pods: leaders(usage, func(p PodUsage) int64 { return p.MemoryBytes }, topPods)},
			av, access.Visible{}
	case metrics != nil:
		// kube-prometheus-stack installs Prometheus without metrics-server, so
		// ask the other backend before giving up. Sequential: it only runs when
		// the first said no, and asking both every time would double the queries.
		cpu, memory, prom := promPodUsage(ctx, metrics, clusterName, topPods)
		if prom.OK {
			return narrowBoards(ctx, r, cpu, memory, prom)
		}
		// Both said no, and both reasons are kept, so the reader is not sent
		// to fix Prometheus when metrics-server is what is missing. The cause
		// classified is metrics-server's, the backend this page reads first
		// and the one the neighboring panels are classified from.
		both := unavailable(av.Detail + "; " + prom.Detail)
		both.Cause, both.Resource = av.Cause, av.Resource
		return Top{Unavailable: both.Detail}, Top{Unavailable: both.Detail}, both, access.Visible{}
	default:
		return Top{Unavailable: av.Detail}, Top{Unavailable: av.Detail}, av, access.Visible{}
	}
}

// narrowBoards keeps the pods of a Prometheus ranking the person may list.
// narrowBoards keeps the pods of a Prometheus ranking the person may list. A
// ranking is top-k over the whole cluster, so a narrowed board can be short.
func narrowBoards(ctx context.Context, r resources.Reader, cpu, memory Top, av Availability) (Top, Top, Availability, access.Visible) {
	question := resources.InNamespace("", "v1", "pods", "")
	where := func(p *PodUsage) string { return p.Namespace }
	cpuPods, seen, err := access.Keep(ctx, r.Gate, r.Cluster, question.Attrs, cpu.Pods, where)
	if err != nil {
		why := failedWith(err, "pods")
		return Top{Unavailable: why.Detail}, Top{Unavailable: why.Detail}, why, seen
	}
	memoryPods, memorySeen, err := access.Keep(ctx, r.Gate, r.Cluster, question.Attrs, memory.Pods, where)
	if err != nil {
		why := failedWith(err, "pods")
		return Top{Unavailable: why.Detail}, Top{Unavailable: why.Detail}, why, seen
	}
	cpu.Pods, memory.Pods = cpuPods, memoryPods
	return cpu, memory, av, seen.Merge(memorySeen)
}

// --- the counts ------------------------------------------------------------------

// summary is the shared arithmetic: a kind is healthy in as much as its
// objects are, and the note names the remainder.
func summary(kind, slug string, total, healthy int, complaint string) KindSummary {
	s := KindSummary{Kind: kind, Slug: slug, Total: total, Healthy: healthy, Class: ClassOK}
	if broken := total - healthy; broken > 0 {
		s.Note = fmt.Sprintf("%d %s", broken, complaint)
		s.Class = ClassBad
	}
	if total == 0 {
		// Nothing of this kind is not a fault, and green would claim a health
		// nobody measured.
		s.Class = ClassNone
	}
	return s
}

func deploymentSummary(items []appsv1.Deployment) KindSummary {
	healthy := 0
	for i := range items {
		if deploymentReady(&items[i]) {
			healthy++
		}
	}
	return summary("Deployments", "deployments", len(items), healthy, "not ready")
}

func statefulSetSummary(items []appsv1.StatefulSet) KindSummary {
	healthy := 0
	for i := range items {
		if statefulSetReady(&items[i]) {
			healthy++
		}
	}
	return summary("StatefulSets", "statefulsets", len(items), healthy, "not ready")
}

func daemonSetSummary(items []appsv1.DaemonSet) KindSummary {
	healthy := 0
	for i := range items {
		if items[i].Status.NumberReady >= items[i].Status.DesiredNumberScheduled {
			healthy++
		}
	}
	return summary("DaemonSets", "daemonsets", len(items), healthy, "not on every node")
}

func jobSummary(items []batchv1.Job) KindSummary {
	healthy := 0
	for i := range items {
		if !jobFailed(&items[i]) {
			healthy++
		}
	}
	return summary("Jobs", "jobs", len(items), healthy, "failed")
}

// cronJobSummary counts suspension, not health: a suspension is a decision,
// not a fault, so it is amber rather than red, and still worth saying because
// it often answers "why did the backup not run".
func cronJobSummary(items []batchv1.CronJob) KindSummary {
	suspended := 0
	for i := range items {
		if s := items[i].Spec.Suspend; s != nil && *s {
			suspended++
		}
	}
	s := KindSummary{Kind: "CronJobs", Slug: "cronjobs", Total: len(items), Healthy: len(items) - suspended}
	if suspended > 0 {
		s.Note = fmt.Sprintf("%d suspended", suspended)
		s.Class = ClassWarn
	} else if len(items) > 0 {
		s.Class = ClassOK
	}
	return s
}

func podSummary(items []corev1.Pod) KindSummary {
	healthy := 0
	for i := range items {
		if podHealthy(&items[i]) {
			healthy++
		}
	}
	return summary("Pods", "pods", len(items), healthy, "not running")
}

// --- what is not ready -------------------------------------------------------

func deploymentReady(d *appsv1.Deployment) bool {
	return d.Status.ReadyReplicas >= wanted(d.Spec.Replicas)
}

func statefulSetReady(s *appsv1.StatefulSet) bool {
	return s.Status.ReadyReplicas >= wanted(s.Spec.Replicas)
}

// wanted reads the replica count as the API defines it: nil means one, not
// none.
func wanted(replicas *int32) int32 {
	if replicas == nil {
		return 1
	}
	return *replicas
}

// now is the page's collection instant, so every age on the panel is measured
// from one moment.
func deploymentTrouble(items []appsv1.Deployment, now time.Time) []Trouble {
	var out []Trouble
	for i := range items {
		d := &items[i]
		want := wanted(d.Spec.Replicas)
		if d.Status.ReadyReplicas >= want {
			continue
		}
		out = append(out, dated(Trouble{
			Kind:      "Deployment",
			Namespace: d.Namespace,
			Name:      d.Name,
			Detail:    fmt.Sprintf("%d of %d replicas ready", d.Status.ReadyReplicas, want),
			Class:     replicaClass(d.Status.ReadyReplicas, want),
		}, deploymentSince(d), now))
	}
	return out
}

// deploymentSince is the Deployment's own record of leaving its intended state:
// Available going False, or else Progressing with ProgressDeadlineExceeded (a
// stuck rollout, during which Available stays True). With neither it is a
// rollout inside its budget, and zero is the honest answer.
func deploymentSince(d *appsv1.Deployment) time.Time {
	var deadline time.Time
	for _, c := range d.Status.Conditions {
		switch {
		case c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionFalse:
			return c.LastTransitionTime.Time
		case c.Type == appsv1.DeploymentProgressing && c.Status == corev1.ConditionFalse &&
			c.Reason == "ProgressDeadlineExceeded":
			deadline = c.LastTransitionTime.Time
		}
	}
	return deadline
}

func statefulSetTrouble(items []appsv1.StatefulSet, pods []corev1.Pod, now time.Time) []Trouble {
	var out []Trouble
	for i := range items {
		s := &items[i]
		want := wanted(s.Spec.Replicas)
		if s.Status.ReadyReplicas >= want {
			continue
		}
		out = append(out, dated(Trouble{
			Kind:      "StatefulSet",
			Namespace: s.Namespace,
			Name:      s.Name,
			Detail:    fmt.Sprintf("%d of %d replicas ready", s.Status.ReadyReplicas, want),
			Class:     replicaClass(s.Status.ReadyReplicas, want),
		}, newestNotReady(pods, "StatefulSet", s.Namespace, s.Name), now))
	}
	return out
}

func daemonSetTrouble(items []appsv1.DaemonSet, pods []corev1.Pod, now time.Time) []Trouble {
	var out []Trouble
	for i := range items {
		d := &items[i]
		want := d.Status.DesiredNumberScheduled
		if d.Status.NumberReady >= want {
			continue
		}
		out = append(out, dated(Trouble{
			Kind:      "DaemonSet",
			Namespace: d.Namespace,
			Name:      d.Name,
			Detail:    fmt.Sprintf("%d of %d nodes ready", d.Status.NumberReady, want),
			Class:     replicaClass(d.Status.NumberReady, want),
		}, newestNotReady(pods, "DaemonSet", d.Namespace, d.Name), now))
	}
	return out
}

// newestNotReady dates a StatefulSet's or DaemonSet's shortfall, whose status
// carries no time: the startTime of the newest unhealthy pod it controls. The
// newest, so a long-lived pod that just went unready does not date the row
// from its start. Ownership is the controller reference, not a name prefix.
func newestNotReady(pods []corev1.Pod, kind, namespace, name string) time.Time {
	var newest time.Time
	for i := range pods {
		p := &pods[i]
		if p.Namespace != namespace || podHealthy(p) || p.Status.StartTime == nil {
			continue
		}
		owner := metav1.GetControllerOf(p)
		if owner == nil || owner.Kind != kind || owner.Name != name {
			continue
		}
		if started := p.Status.StartTime.Time; started.After(newest) {
			newest = started
		}
	}
	return newest
}

func jobTrouble(items []batchv1.Job, now time.Time) []Trouble {
	var out []Trouble
	for i := range items {
		j := &items[i]
		if !jobFailed(j) {
			continue
		}
		// When it failed, not when it started; resources.JobStopped is also
		// the Jobs list's reading. No recorded time leaves the row undated.
		failed, _ := resources.JobStopped(j)
		out = append(out, dated(Trouble{
			Kind:      "Job",
			Namespace: j.Namespace,
			Name:      j.Name,
			Detail:    fmt.Sprintf("failed after %s", plural(int(j.Status.Failed), "attempt", "attempts")),
			Class:     ClassBad,
		}, failed, now))
	}
	return out
}

// dated stamps a row with the instant its kind recorded, aged from the page's
// collection instant. A zero instant leaves the row undated; see
// Trouble.Since.
func dated(row Trouble, since, now time.Time) Trouble {
	if since.IsZero() {
		return row
	}
	row.Since = since
	// ShortDuration clamps a clock disagreement to zero rather than
	// printing a trouble that begins in the future.
	row.Age = resources.ShortDuration(now.Sub(since))
	return row
}

func jobFailed(j *batchv1.Job) bool {
	for _, c := range j.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// replicaClass grades a shortfall: none ready is worse than one of three.
func replicaClass(ready, want int32) string {
	switch {
	case want == 0 || ready >= want:
		return ClassOK
	case ready == 0:
		return ClassBad
	default:
		return ClassWarn
	}
}

// --- pods --------------------------------------------------------------------

// podHealthy is stricter than the phase: a CrashLoopBackOff pod is Running.
// Running counts only when every container is ready; Succeeded counts.
//
// Readiness is resources.PodReady's count, which includes native sidecars (they
// report under InitContainerStatuses), so a pod whose mesh proxy is crashing
// is not healthy. The count rather than the Ready condition, so this agrees
// with the Pods list, which does not show readiness gates. Equal rather than
// at-least: a status for a container the spec no longer lists must not make
// up for one that is not ready.
func podHealthy(pod *corev1.Pod) bool {
	if pod.DeletionTimestamp != nil {
		return false
	}
	switch pod.Status.Phase {
	case corev1.PodSucceeded:
		return true
	case corev1.PodRunning:
		ready, total := resources.PodReady(pod)
		return ready == total
	default:
		return false
	}
}

// phaseOrder fixes the order of the stacked bar, so segments do not move
// between refreshes.
var phaseOrder = []struct {
	phase corev1.PodPhase
	label string
}{
	{corev1.PodRunning, "Running"},
	{corev1.PodPending, "Pending"},
	{corev1.PodSucceeded, "Succeeded"},
	{corev1.PodFailed, "Failed"},
	{corev1.PodUnknown, "Unknown"},
}

// phaseClass is the segment's color, graded by resources.GradePodStatus so the
// bar agrees with the pill in the list and the drawer (Unknown is red: the
// node stopped reporting). Succeeded is the one override: muted, see
// ClassMuted.
func phaseClass(phase corev1.PodPhase) string {
	if phase == corev1.PodSucceeded {
		return ClassMuted
	}
	class, _ := resources.GradePodStatus(string(phase))
	return class
}

// phaseBreakdown counts pods by phase, pulling Running pods that are not ready
// into their own segment, so a crash-looping cluster does not draw as healthy.
func phaseBreakdown(pods []corev1.Pod) []Segment {
	counts := map[corev1.PodPhase]int{}
	notReady := 0
	for i := range pods {
		pod := &pods[i]
		if pod.Status.Phase == corev1.PodRunning && !podHealthy(pod) {
			notReady++
			continue
		}
		counts[pod.Status.Phase]++
	}

	var out []Segment
	for _, p := range phaseOrder {
		if p.phase == corev1.PodRunning && notReady > 0 {
			out = append(out, Segment{Label: "Running", Value: counts[p.phase], Class: phaseClass(p.phase)})
			out = append(out, Segment{Label: "Not ready", Value: notReady, Class: ClassBad})
			continue
		}
		if counts[p.phase] == 0 {
			// A segment worth nothing is left out: a zero-width rect would still
			// show in the legend.
			continue
		}
		out = append(out, Segment{Label: p.label, Value: counts[p.phase], Class: phaseClass(p.phase)})
	}
	return out
}

func podTrouble(pods []corev1.Pod) []Trouble {
	var out []Trouble
	for i := range pods {
		pod := &pods[i]
		if podHealthy(pod) {
			continue
		}
		status, class := podStatus(pod)
		out = append(out, Trouble{
			Kind:      "Pod",
			Namespace: pod.Namespace,
			Name:      pod.Name,
			Detail:    status,
			Class:     class,
		})
	}
	return out
}

// podStatus is resources.PodStatus, graded for this panel, so every screen
// derives a pod's status one way. This panel lists only unhealthy pods, so a
// bare "Running" is suffixed ", not ready" to name what is wrong.
func podStatus(pod *corev1.Pod) (string, string) {
	status := resources.PodStatus(pod)
	if status == string(corev1.PodRunning) {
		status += ", not ready"
	}
	// The same grader: this package's classes are resources'.
	class, _ := resources.GradePodStatus(status)
	return status, class
}

// restarting lists every container with a restart count, worst first, cut to
// limit; the second return is the count before the cut. now is the page's
// collection instant, so every age is measured from one moment.
//
// Both status lists are read: an init container that will not start is why a
// pod is stuck, and a native sidecar's crash loop is an incident. They are
// marked Init rather than merged, as the Containers panel draws them, and not
// promoted in the sort (count, then namespace/name). Ephemeral containers are
// not read: a debug container's restart is about the debugging session.
func restarting(pods []corev1.Pod, limit int, now time.Time) ([]Restarting, int) {
	var out []Restarting
	for i := range pods {
		pod := &pods[i]
		// Init containers first, in the kubelet's order, as the Containers
		// panel draws them, before the sort reorders by count.
		collect := func(statuses []corev1.ContainerStatus, init bool) {
			for _, c := range statuses {
				if c.RestartCount == 0 {
					continue
				}
				exit := lastTermination(c, now)
				out = append(out, Restarting{
					Namespace: pod.Namespace,
					Name:      pod.Name,
					Container: c.Name,
					Init:      init,
					Restarts:  c.RestartCount,
					Reason:    exit.reason,
					LastExit:  exit.at,
					Ago:       exit.ago,
					AgoTitle:  exit.title,
					Class:     restartClass(c.RestartCount),
				})
			}
		}
		collect(pod.Status.InitContainerStatuses, true)
		collect(pod.Status.ContainerStatuses, false)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Restarts != out[j].Restarts {
			return out[i].Restarts > out[j].Restarts
		}
		return out[i].Namespace+"/"+out[i].Name < out[j].Namespace+"/"+out[j].Name
	})
	total := len(out)
	if total > limit {
		out = out[:limit]
	}
	return out, total
}

// lastTermination reads the reason and the instant of a container's previous
// termination in one read, so the adjacent cells cannot disagree about whether
// there was one. The stamp is UTC, as zoomStamp's.
//
// A restart count with no termination state is ordinary: the kubelet keeps only
// the latest, and a reboot or its garbage collector removes it. It draws an em
// dash, never "0s", since this cell decides whether an incident is live. The
// tooltip tells two absences apart: no lastState.terminated at all, or one with
// no finishedAt (whose reason still reaches the cell beside it).
//
// It reads lastState from a typed status, unlike internal/detail, which reads
// the current state from unstructured data: a different question, kept apart.
func lastTermination(c corev1.ContainerStatus, now time.Time) lastExit {
	out := lastExit{reason: missing, ago: missing}
	out.title = "the kubelet no longer holds this container's last exit — a node reboot " +
		"or its garbage collector takes that with it"

	t := c.LastTerminationState.Terminated
	if t == nil {
		return out
	}
	if r := strings.TrimSpace(t.Reason); r != "" {
		out.reason = r
	}
	if t.FinishedAt.IsZero() {
		out.title = "this container's last exit is recorded and carries no time — " +
			"the cluster stamped none on it"
		return out
	}
	out.at = t.FinishedAt.Time
	// ShortDuration clamps a clock disagreement to zero rather than
	// printing an exit in the future.
	out.ago = resources.ShortDuration(now.Sub(t.FinishedAt.Time))
	out.title = zoomStamp(t.FinishedAt.Time)
	return out
}

// lastExit is what one read of lastState.terminated yields, as one struct so
// no cell can be filled from a second read.
type lastExit struct {
	reason string
	// at is the kubelet's stamp, zero where there is none; never sent as a
	// time when zero.
	at    time.Time
	ago   string
	title string
}

func restartClass(n int32) string {
	// One restart happens on any node drain; five is a pattern.
	if n >= 5 {
		return ClassBad
	}
	return ClassWarn
}

// --- the API calls -------------------------------------------------------------

func listDeployments(ctx context.Context, r resources.Reader, ns string) ([]appsv1.Deployment, access.Visible, error) {
	return resources.Listed(ctx, r, resources.InNamespace("apps", "v1", "deployments", ns), all(), r.Clients.AppsV1().Deployments(ns).List,
		func(l *appsv1.DeploymentList) []appsv1.Deployment { return l.Items })
}

func listStatefulSets(ctx context.Context, r resources.Reader, ns string) ([]appsv1.StatefulSet, access.Visible, error) {
	return resources.Listed(ctx, r, resources.InNamespace("apps", "v1", "statefulsets", ns), all(), r.Clients.AppsV1().StatefulSets(ns).List,
		func(l *appsv1.StatefulSetList) []appsv1.StatefulSet { return l.Items })
}

func listDaemonSets(ctx context.Context, r resources.Reader, ns string) ([]appsv1.DaemonSet, access.Visible, error) {
	return resources.Listed(ctx, r, resources.InNamespace("apps", "v1", "daemonsets", ns), all(), r.Clients.AppsV1().DaemonSets(ns).List,
		func(l *appsv1.DaemonSetList) []appsv1.DaemonSet { return l.Items })
}

func listJobs(ctx context.Context, r resources.Reader, ns string) ([]batchv1.Job, access.Visible, error) {
	return resources.Listed(ctx, r, resources.InNamespace("batch", "v1", "jobs", ns), all(), r.Clients.BatchV1().Jobs(ns).List,
		func(l *batchv1.JobList) []batchv1.Job { return l.Items })
}

func listCronJobs(ctx context.Context, r resources.Reader, ns string) ([]batchv1.CronJob, access.Visible, error) {
	return resources.Listed(ctx, r, resources.InNamespace("batch", "v1", "cronjobs", ns), all(), r.Clients.BatchV1().CronJobs(ns).List,
		func(l *batchv1.CronJobList) []batchv1.CronJob { return l.Items })
}
