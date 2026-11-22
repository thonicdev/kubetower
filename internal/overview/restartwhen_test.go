package overview

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func restartingPod(name string, restarts int32, state corev1.ContainerState) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: name},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:                 "app",
			RestartCount:         restarts,
			LastTerminationState: state,
		}}},
	}
}

func terminatedAt(reason string, at time.Time) corev1.ContainerState {
	return corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
		Reason:     reason,
		FinishedAt: metav1.NewTime(at),
	}}
}

// The panel says when, because a count on its own answers a different question.
//
// A pod that crashed nine hundred times last week and has been quiet
// since, and one crashing right now, are the same cell without it — and they are
// opposite answers to the only question this panel is scanned for.
func TestTheRestartingPanelSaysWhenTheLastExitWas(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	rows, _ := restarting([]corev1.Pod{
		restartingPod("recent", 3, terminatedAt("OOMKilled", now.Add(-4*time.Minute))),
		restartingPod("stale", 900, terminatedAt("Error", now.Add(-72*time.Hour))),
	}, 10, now)

	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}

	byName := map[string]Restarting{}
	for _, r := range rows {
		byName[r.Name] = r
	}

	if got := byName["recent"].Ago; got != "4m" {
		t.Errorf("a container that exited four minutes ago reads %q, want %q", got, "4m")
	}
	if got := byName["stale"].Ago; got != "3d" {
		t.Errorf("a container that exited three days ago reads %q, want %q", got, "3d")
	}

	// The tooltip is the absolute instant, in UTC and saying so: the server's
	// clock is this container's and the browser's is the operator's, and a
	// timestamp that quietly picks one cannot be pasted into an incident channel.
	title := byName["recent"].AgoTitle
	if !strings.Contains(title, "11:56:00") || !strings.Contains(title, "UTC") {
		t.Errorf("the tooltip is %q; it should carry the exact instant and name its zone", title)
	}

	// The reason still comes from the same read, and still reaches the cell
	// beside it.
	if byName["recent"].Reason != "OOMKilled" {
		t.Errorf("reason = %q, want OOMKilled", byName["recent"].Reason)
	}
}

// The panel carries the instant itself, and not only two renderings of it.
//
// `Ago` is a word and `AgoTitle` is a sentence; neither is a time a client can
// do anything with, and `RestartEntry.lastExitAt` — declared in the contract
// and drawn by `Overview.tsx` — needs something to be filled from. The adapter
// cannot parse "4m" back into an instant, and it
// must not date the row from the page's clock, so the instant travels from here
// or it does not travel at all.
//
// This pins the producer; `TestARestartingRowCarriesTheExitInstantOntoTheWire`
// in internal/web pins that the adapter carries it, and neither is sufficient
// alone — internal/web imports internal/api, so the two halves cannot live in
// one package.
func TestARestartingRowCarriesTheExitInstantItself(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	exit := now.Add(-4 * time.Minute)

	rows, _ := restarting([]corev1.Pod{
		restartingPod("recent", 3, terminatedAt("OOMKilled", exit)),
	}, 10, now)

	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if got := rows[0].LastExit; !got.Equal(exit) {
		t.Errorf("LastExit = %v, want %v — the kubelet's own stamp, which is the only "+
			"thing `lastExitAt` can be filled from", got, exit)
	}
	// And it is the same instant the tooltip was written from, because both come
	// out of one read: a row whose cell and whose hover disagree about when a
	// container died is worse than one that says neither.
	if !strings.Contains(rows[0].AgoTitle, "11:56:00") {
		t.Errorf("the tooltip is %q and was written from a different read than LastExit %v",
			rows[0].AgoTitle, rows[0].LastExit)
	}
}

// Never terminated and terminated at an unknown time are two states, not one.
//
// Both draw the em dash and both travel with `lastExitAt` absent — the contract
// has one member for the instant and none for the explanation, so the wire
// cannot tell them apart. The **tooltip** can, and must: a container whose
// `lastState.terminated` the kubelet is holding, with a reason in it and no
// `finishedAt`, told "the kubelet no longer holds this container's last exit"
// sends its reader to look for a node reboot that did not happen.
//
// It is reachable: `finishedAt` is a `metav1.Time` with a zero value, and the
// reason arrives beside it either way — which is what makes the two rows
// otherwise identical and this distinction worth keeping.
func TestARecordedExitWithNoStampIsNotAForgottenOne(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	rows, _ := restarting([]corev1.Pod{
		// Forgotten: no lastState.terminated at all.
		restartingPod("forgotten", 900, corev1.ContainerState{}),
		// Recorded, unstamped: a termination with its reason and no finishedAt.
		restartingPod("unstamped", 900, corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{Reason: "Error"},
		}),
	}, 10, now)

	byName := map[string]Restarting{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	forgotten, unstamped := byName["forgotten"], byName["unstamped"]

	// Neither dates the row, and neither may.
	for _, row := range []Restarting{forgotten, unstamped} {
		if !row.LastExit.IsZero() {
			t.Errorf("%s carries LastExit %v; there is no instant to carry", row.Name, row.LastExit)
		}
		if row.Ago != missing {
			t.Errorf("%s reads %q, want the em dash", row.Name, row.Ago)
		}
	}

	// The reason is the one thing the second row has and the first has not, and
	// it is what makes the tooltip's claim checkable.
	if unstamped.Reason != "Error" {
		t.Errorf("the unstamped row's reason = %q, want Error — the kubelet is holding "+
			"this termination and said why", unstamped.Reason)
	}

	if forgotten.AgoTitle == unstamped.AgoTitle {
		t.Fatalf("both tooltips read %q; a recorded exit with no stamp is being explained "+
			"as a record the kubelet no longer holds, which is a node reboot to go and "+
			"look for that did not happen", forgotten.AgoTitle)
	}
	if !strings.Contains(forgotten.AgoTitle, "kubelet no longer holds") {
		t.Errorf("the forgotten row's tooltip is %q", forgotten.AgoTitle)
	}
	if !strings.Contains(unstamped.AgoTitle, "no time") {
		t.Errorf("the unstamped row's tooltip is %q; it should say the exit is recorded "+
			"and its instant is not", unstamped.AgoTitle)
	}
}

// A restart count with no termination state must not read as a restart now.
//
// **This is the case the feature exists to get right, not an edge case.** The
// kubelet keeps only the most recent terminated container, and a node reboot or
// its garbage collector takes that with it, so a pod with a large restart count
// and nothing to say about any of them is ordinary. Rendering the zero value
// would put "0s" in the cell an operator reads to decide whether an incident is
// live — the most expensive possible lie on this panel: an answer that could
// not be given, drawn as an answer.
func TestARestartWithNoRecordedExitSaysSoRatherThanNow(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	rows, _ := restarting([]corev1.Pod{
		restartingPod("forgotten", 900, corev1.ContainerState{}),
	}, 10, now)

	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	got := rows[0]

	if got.Ago == "0s" {
		t.Fatal("a container whose last exit the kubelet no longer holds reads `0s`, " +
			"which says it restarted this second. That is the reading this panel is " +
			"scanned for and it is false")
	}
	if got.Ago != missing {
		t.Errorf("Ago = %q, want the em dash — the same mark the reason cell uses for "+
			"the same absence, from the same read", got.Ago)
	}
	if got.Reason != missing {
		t.Errorf("Reason = %q, want the em dash", got.Reason)
	}
	// And the tooltip explains the dash, so it does not read as a rendering fault.
	if !strings.Contains(got.AgoTitle, "kubelet") {
		t.Errorf("the tooltip is %q and does not say why there is no time; an em dash "+
			"with no explanation is indistinguishable from a broken cell", got.AgoTitle)
	}
}

// A clock disagreement is not a container that exits in the future.
func TestAnExitStampedInTheFutureReadsAsNow(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	rows, _ := restarting([]corev1.Pod{
		restartingPod("skewed", 2, terminatedAt("Error", now.Add(90*time.Second))),
	}, 10, now)

	if got := rows[0].Ago; got != "0s" {
		t.Errorf("Ago = %q for an exit stamped ninety seconds ahead of this container's "+
			"clock; it should clamp to 0s rather than print a negative age", got)
	}
}

// Every age in the panel is measured from one instant.
//
// Two rows built a millisecond apart must not disagree about how long ago
// "three days" was, and the panel already carries the instant it was collected.
func TestEveryRowIsAgedFromTheSameInstant(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	exit := now.Add(-90 * time.Minute)

	rows, _ := restarting([]corev1.Pod{
		restartingPod("a", 1, terminatedAt("Error", exit)),
		restartingPod("b", 1, terminatedAt("Error", exit)),
	}, 10, now)

	if rows[0].Ago != rows[1].Ago {
		t.Errorf("two containers that exited at the same instant read %q and %q",
			rows[0].Ago, rows[1].Ago)
	}
}

// A restarting init container is on the panel.
//
// Reading `ContainerStatuses` alone, an init container that will not start —
// which is the *reason* a pod is stuck, and the distinction
// `PodContainer.Init` exists to make on the Containers panel — would produce
// no row at all.
//
// The pod below is the shape that hides it: one restarting init container and
// one restarting app container, so the pod itself is on the panel and looks
// complete. Only a count over both lists shows what is missing.
func TestARestartingInitContainerIsOnThePanelAndIsMarked(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "keycloak", Name: "keycloak-0"},
		Status: corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{{
				Name:                 "wait-for-db",
				RestartCount:         7,
				LastTerminationState: terminatedAt("Error", now.Add(-2*time.Minute)),
			}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:                 "keycloak",
				RestartCount:         3,
				LastTerminationState: terminatedAt("OOMKilled", now.Add(-30*time.Minute)),
			}},
		},
	}

	rows, _ := restarting([]corev1.Pod{pod}, 10, now)

	if len(rows) != 2 {
		t.Fatalf("got %d rows for a pod with a restarting init container and a "+
			"restarting app container; both are rows this panel exists for", len(rows))
	}

	byName := map[string]Restarting{}
	for _, r := range rows {
		byName[r.Container] = r
	}

	init, ok := byName["wait-for-db"]
	if !ok {
		t.Fatalf("the restarting init container is not on the panel; rows are %v", byName)
	}
	if !init.Init {
		t.Error("the init container's row does not say it is one; unmarked it reads as " +
			"an app container and sends the reader to the wrong logs")
	}
	if init.Restarts != 7 || init.Reason != "Error" {
		t.Errorf("the init row carries restarts=%d reason=%q, not the kubelet's 7/Error",
			init.Restarts, init.Reason)
	}
	if init.Ago != "2m" {
		t.Errorf("the init row's last exit reads %q; it goes through lastTermination "+
			"like every other row, not a second derivation", init.Ago)
	}

	app := byName["keycloak"]
	if app.Init {
		t.Error("the app container's row is marked as an init container")
	}
}

// A native sidecar is an init container with `restartPolicy: Always`, and the
// kubelet files its status under `initContainerStatuses` like any other. It is a
// long-running process, so its crash loop is an incident rather than a slow
// start — and it reaches the panel through the init container list.
func TestANativeSidecarIsOnThePanelAsAnInitContainer(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	always := corev1.ContainerRestartPolicyAlways

	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "istio", Name: "checkout-0"},
		Spec: corev1.PodSpec{InitContainers: []corev1.Container{
			{Name: "istio-proxy", RestartPolicy: &always},
		}},
		Status: corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{{
				Name:                 "istio-proxy",
				RestartCount:         12,
				LastTerminationState: terminatedAt("OOMKilled", now.Add(-time.Minute)),
			}},
		},
	}

	rows, _ := restarting([]corev1.Pod{pod}, 10, now)

	if len(rows) != 1 {
		t.Fatalf("got %d rows for a pod whose only restarting container is a native "+
			"sidecar; it is the whole of what that pod has to report", len(rows))
	}
	if !rows[0].Init {
		t.Error("a native sidecar's row is not marked init; the kubelet files it under " +
			"initContainerStatuses and the Containers panel draws it there")
	}
	if rows[0].Class != restartClass(12) {
		t.Errorf("Class = %q, not the grade twelve restarts earn", rows[0].Class)
	}
}

// An ephemeral container is deliberately not on this panel.
//
// A debug container is attached by an operator who is already looking at that
// pod; its restart is a fact about the debugging session and not about the
// workload. Pinned so that "read every status list" is not applied to the third
// one by somebody generalizing it.
func TestAnEphemeralContainerIsNotOnThePanel(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "debugged"},
		Status: corev1.PodStatus{
			EphemeralContainerStatuses: []corev1.ContainerStatus{{
				Name:         "debugger",
				RestartCount: 4,
			}},
		},
	}

	if rows, _ := restarting([]corev1.Pod{pod}, 10, now); len(rows) != 0 {
		t.Errorf("got %d rows for a pod whose only restarting container is an "+
			"ephemeral debug one", len(rows))
	}
}

// The sort is by count, and an init row takes its place in it rather than being
// promoted. "Arguably more urgent" is not a rule a reader can predict from the
// screen; the Init mark is what lets them see which kind a row is.
func TestAnInitRowIsRankedByCountLikeEveryOther(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "awx", Name: "awx-task-0"},
		Status: corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{{Name: "init-low", RestartCount: 2}},
			ContainerStatuses:     []corev1.ContainerStatus{{Name: "app-high", RestartCount: 9}},
		},
	}

	rows, _ := restarting([]corev1.Pod{pod}, 10, now)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0].Container != "app-high" || rows[1].Container != "init-low" {
		t.Errorf("order is %q then %q; the panel ranks by restart count and an init "+
			"container with fewer restarts does not jump the container with more",
			rows[0].Container, rows[1].Container)
	}
}
