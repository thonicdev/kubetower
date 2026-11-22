package detail

import (
	"testing"
	"time"
)

// A pod that declares all three probes on one container, two on another, and
// none on its init container - plus a status for each, in three different
// states. One fixture rather than three, because what these tests are mostly
// about is that two containers of the same pod come out saying different things.
const podWithProbes = `
apiVersion: v1
kind: Pod
metadata:
  name: api-0
  namespace: shop
spec:
  initContainers:
    - name: migrate
      image: registry.example.com/migrate:2.1
  containers:
    - name: api
      image: registry.example.com/api:1.25
      livenessProbe:
        httpGet:
          path: /healthz
          port: 8080
        initialDelaySeconds: 15
        periodSeconds: 10
        timeoutSeconds: 1
        failureThreshold: 3
      readinessProbe:
        httpGet:
          path: /readyz
          port: http
          scheme: HTTPS
        periodSeconds: 5
        failureThreshold: 2
      startupProbe:
        exec:
          command: ["sh", "-c", "test -f /tmp/ready"]
        periodSeconds: 2
        failureThreshold: 30
    - name: cache
      image: redis:7.2
      livenessProbe:
        tcpSocket:
          port: 6379
      readinessProbe:
        grpc:
          port: 9000
          service: health
status:
  initContainerStatuses:
    - name: migrate
      ready: false
      restartCount: 0
      state:
        terminated:
          exitCode: 0
          reason: Completed
  containerStatuses:
    - name: api
      ready: true
      restartCount: 2
      state:
        running:
          startedAt: "2026-09-15T08:00:00Z"
    - name: cache
      ready: false
      restartCount: 7
      state:
        waiting:
          reason: CrashLoopBackOff
          message: back-off 5m0s restarting failed container cache
`

// The same pod with nothing declared and nothing reported. A pod looks like this
// for as long as it is Pending, and this is also exactly what a pod that will
// never be scheduled looks like - so the two absences have to be said, not drawn
// as blank.
const podWithNoProbes = `
apiVersion: v1
kind: Pod
metadata:
  name: plain-0
  namespace: shop
spec:
  containers:
    - name: app
      image: nginx:1.27
    - name: sidecar
      image: envoy:1.31
status: {}
`

// Two declarations this console cannot describe, and a status entry it cannot
// read. None of the three is an absence, and drawing any of them as one is the
// defect these tests exist for.
const podWithUnreadableHealth = `
apiVersion: v1
kind: Pod
metadata:
  name: odd-0
  namespace: shop
spec:
  containers:
    - name: app
      image: nginx:1.27
      livenessProbe: every 10 seconds
      readinessProbe:
        smokeSignal:
          color: blue
status:
  containerStatuses:
    - name: app
      ready: false
      restartCount: 1
      state: {}
`

// The question asked of a pod first, answered without reading the raw
// manifest: is this container passing its probes.
func TestAContainerSaysWhichProbesAreDeclaredAndWhatTheyDo(t *testing.T) {
	got := containersOf(nodeFrom(t, podWithProbes), "Pod")
	if len(got) != 3 {
		t.Fatalf("%d containers, want 3 (one init, two app)", len(got))
	}

	// Init first, as ever, and then the two app containers in spec order.
	api := got[1]
	if api.Name != "api" {
		t.Fatalf("the second container is %q, want api", api.Name)
	}

	// Three entries, always, in the drawn order - and the startup probe is the
	// third of them rather than folded into the other two. It is the one that
	// explains a container that never gets as far as failing liveness, because
	// while it runs the kubelet holds the other two off entirely.
	if len(api.Probes) != 3 {
		t.Fatalf("%d probes on api, want 3: %+v", len(api.Probes), api.Probes)
	}
	for at, want := range []string{"liveness", "readiness", "startup"} {
		if api.Probes[at].Kind != want {
			t.Errorf("probe %d is %q, want %q; the order is what puts startup beside the other two",
				at, api.Probes[at].Kind, want)
		}
	}

	liveness := api.Probes[0]
	if liveness.State != ProbeDeclared {
		t.Fatalf("the liveness probe came out %q: %+v", liveness.State, liveness)
	}
	// Handler and target apart, never one rendered phrase: a caller that draws
	// them differently would otherwise have to parse English back apart.
	if liveness.Handler != "httpGet" || liveness.Target != "/healthz:8080" {
		t.Errorf("liveness handler=%q target=%q, want httpGet and /healthz:8080",
			liveness.Handler, liveness.Target)
	}
	// The four timings that actually decide behavior. All four and not one:
	// periodSeconds and failureThreshold are together how long a container has to
	// recover, and neither means much alone.
	if liveness.InitialDelaySeconds != 15 || liveness.PeriodSeconds != 10 ||
		liveness.TimeoutSeconds != 1 || liveness.FailureThreshold != 3 {
		t.Errorf("the liveness timings came out as %+v", liveness)
	}

	// A named port is a port. Rendering `http` as an empty string is how a probe
	// that works reads as one that is misconfigured.
	readiness := api.Probes[1]
	if readiness.Target != "https /readyz:http" {
		t.Errorf("readiness target %q; a named port and a non-default scheme are both facts about the check",
			readiness.Target)
	}

	startup := api.Probes[2]
	if startup.Handler != "exec" || startup.Target != "sh -c test -f /tmp/ready" {
		t.Errorf("startup handler=%q target=%q", startup.Handler, startup.Target)
	}
	// 30 × 2s is ten minutes of grace, and it is the number that explains a
	// container nothing has yet complained about. Read as 0 it would say the
	// opposite - which is what happens if the decoder hands back a float64 and
	// nothing accounts for it (see event.go's firstInt32).
	if startup.FailureThreshold != 30 || startup.PeriodSeconds != 2 {
		t.Errorf("the startup timings came out as %+v; a number read through the wrong decoder becomes 0 silently",
			startup)
	}

	cache := got[2]
	if cache.Probes[0].Handler != "tcpSocket" || cache.Probes[0].Target != ":6379" {
		t.Errorf("a tcpSocket probe came out as %+v", cache.Probes[0])
	}
	if cache.Probes[1].Handler != "grpc" || cache.Probes[1].Target != "health :9000" {
		t.Errorf("a grpc probe came out as %+v", cache.Probes[1])
	}
	// And the one it does not declare says so, on a container whose other two are
	// declared. This is the row an empty cell would have said nothing on.
	if cache.Probes[2].State != ProbeAbsent || cache.Probes[2].Problem != "" {
		t.Errorf("cache's startup probe came out %q with problem %q; not declared is not a failure to read",
			cache.Probes[2].State, cache.Probes[2].Problem)
	}
}

// "No readiness probe" is a finding, and a finding needs a row to be written on.
//
// A Service can be sending traffic to a container nothing has ever asked whether
// it is ready. A panel that filters the absent probes out draws an empty area
// there, which reads as a panel that did not look.
func TestAContainerWithNoProbeDeclaredStillCarriesThree(t *testing.T) {
	got := containersOf(nodeFrom(t, podWithNoProbes), "Pod")
	if len(got) != 2 {
		t.Fatalf("%d containers, want 2", len(got))
	}

	for _, container := range got {
		if len(container.Probes) != 3 {
			t.Fatalf("%s carries %d probes, want 3; the entry is the sentence and an empty list is not it",
				container.Name, len(container.Probes))
		}
		for _, probe := range container.Probes {
			if probe.State != ProbeAbsent {
				t.Errorf("%s's %s probe came out %q, want absent", container.Name, probe.Kind, probe.State)
			}
			// The member that distinguishes *not declared* from *could not be
			// read* is empty here, and that is the whole distinction: a problem
			// on this row would make a pod that is written exactly as intended
			// look like a console that failed.
			if probe.Problem != "" {
				t.Errorf("%s's %s probe carries a problem it has no business carrying: %q",
					container.Name, probe.Kind, probe.Problem)
			}
			if probe.Handler != "" || probe.Target != "" {
				t.Errorf("%s's %s probe invented a handler: %+v", container.Name, probe.Kind, probe)
			}
		}
	}
}

// Not declared and could not be read are two different answers.
//
// An empty answer and a failed one are different answers, on the row that
// exists to say whether a container is healthy. A
// probe declared in a shape this build cannot describe is a gap in the console;
// a container with no probe is a gap in the pod. Rendering them alike sends
// somebody to fix the wrong one.
func TestADeclarationThisConsoleCannotReadIsNotAnAbsence(t *testing.T) {
	got := containersOf(nodeFrom(t, podWithUnreadableHealth), "Pod")
	if len(got) != 1 {
		t.Fatalf("%d containers, want 1", len(got))
	}
	probes := got[0].Probes

	// A `livenessProbe` that is not an object at all.
	if probes[0].State != ProbeUnreadable {
		t.Errorf("a malformed liveness probe came out %q, want unreadable", probes[0].State)
	}
	if probes[0].Problem == "" {
		t.Error("an unreadable probe carries no problem, so nothing on the row distinguishes it from a probe that was never declared")
	}

	// A handler shape this build has never heard of. Something *is* declared and
	// this console cannot say what, which is the same answer sourceOf gives for
	// a `valueFrom` it does not recognize rather than a blank.
	if probes[1].State != ProbeUnreadable || probes[1].Problem == "" {
		t.Errorf("an unknown handler came out as %+v", probes[1])
	}
	if probes[1].Handler != "" {
		t.Errorf("an unknown handler was given a name anyway: %q", probes[1].Handler)
	}

	// And the one that genuinely is not there is still `absent`, on the same
	// container. The two states have to be producible side by side or the
	// distinction is theoretical.
	if probes[2].State != ProbeAbsent {
		t.Errorf("the startup probe came out %q, want absent", probes[2].State)
	}

	// A reported container whose state names none of the three. Reported and
	// unreadable at once, which is closer to "nothing was reported" than to any
	// of them - and saying `running` because the entry exists would be the one
	// wrong answer that looks right.
	if got[0].Status.State != ContainerUnreported || got[0].Status.Problem == "" {
		t.Errorf("a status with no recognized state came out as %+v", got[0].Status)
	}
}

// The current state, matched to the spec by name - which is the only thing that
// matches them: neither status list is in the spec's order, and a restartable
// sidecar is declared under `initContainers` and reported under
// `initContainerStatuses`.
func TestAContainerCarriesTheStateTheKubeletReported(t *testing.T) {
	got := containersOf(nodeFrom(t, podWithProbes), "Pod")

	migrate := got[0].Status
	if migrate == nil {
		t.Fatal("the init container carries no status; a container nobody reported carries `unreported`, never nil")
	}
	// Read out of `initContainerStatuses`, which is a different list from the one
	// the two app containers came from.
	if migrate.State != ContainerTerminated || migrate.Reason != "Completed" {
		t.Errorf("the init container's status came out as %+v", migrate)
	}

	api := got[1].Status
	if api.State != ContainerRunning || !api.Ready || api.Restarts != 2 {
		t.Errorf("api's status came out as %+v", api)
	}
	// Running and nothing wrong: the reason is empty because there is nothing to
	// say, which is the ordinary case and not an absence.
	if api.Reason != "" {
		t.Errorf("a running container was given a reason: %q", api.Reason)
	}

	// The word an operator actually looks for, with the sentence beside it rather
	// than instead of it.
	cache := got[2].Status
	if cache.State != ContainerWaiting || cache.Reason != "CrashLoopBackOff" {
		t.Errorf("cache's status came out as %+v", cache)
	}
	if cache.Message == "" {
		t.Error("the kubelet's message was dropped; the reason is the word somebody searches for and the message is the detail")
	}
	if cache.Ready {
		t.Errorf("a crash-looping container came out ready: %+v", cache)
	}
	if cache.Restarts != 7 {
		t.Errorf("restarts %d, want 7", cache.Restarts)
	}
}

// Nothing reported is not "not ready".
//
// This is the empty-versus-failed rule on the one row that exists to answer
// whether a container is healthy. Every container of a Pending pod looks like
// this, and so does every container of a pod that will never be scheduled - and
// neither of them has failed a readiness check, because nothing has run one.
func TestAContainerNobodyReportedSaysSoRatherThanNotReady(t *testing.T) {
	got := containersOf(nodeFrom(t, podWithNoProbes), "Pod")

	for _, container := range got {
		status := container.Status
		if status == nil {
			t.Fatalf("%s carries no status at all; a missing member is indistinguishable from a mapper that dropped it",
				container.Name)
		}
		if status.State != ContainerUnreported {
			t.Errorf("%s came out %q, want unreported", container.Name, status.State)
		}
		if status.Problem == "" {
			t.Errorf("%s carries no sentence saying why there is no status, so the row is an empty cell",
				container.Name)
		}
		// Ready says nothing under `unreported`, and the producer does not invent
		// a value for it. What matters is that the state is read first; this pins
		// that nothing else was filled in either.
		if status.Reason != "" || status.Restarts != 0 {
			t.Errorf("%s was given a reason or a restart count nobody reported: %+v", container.Name, status)
		}
	}
}

// Only a pod has any of this, for the same reason only a pod has containers.
func TestOnlyAPodCarriesProbesAndStatus(t *testing.T) {
	for _, kind := range []string{"Deployment", "Service", "ConfigMap"} {
		if got := containersOf(nodeFrom(t, podWithProbes), kind); got != nil {
			t.Errorf("containersOf answered %d containers for a %s", len(got), kind)
		}
	}
}

// A container that was OOM-killed and restarted is
// running again, and only `lastState.terminated` still says why. Both are read:
// the current state is what it is doing, the last termination why it restarted.
func TestARestartedContainerCarriesItsLastTerminationBesideItsState(t *testing.T) {
	got := statusOf(map[string]any{
		"name":         "api",
		"ready":        true,
		"restartCount": int64(4),
		"state":        map[string]any{"running": map[string]any{"startedAt": "2026-10-13T09:42:00Z"}},
		"lastState": map[string]any{"terminated": map[string]any{
			"exitCode":   int64(137),
			"reason":     "OOMKilled",
			"startedAt":  "2026-10-13T09:00:00Z",
			"finishedAt": "2026-10-13T09:41:30Z",
		}},
	})

	if got.State != ContainerRunning || !got.Ready || got.Restarts != 4 {
		t.Errorf("the current state came out as %+v; the last termination must not replace it", got)
	}
	if got.Last == nil {
		t.Fatal("no last termination, on a container whose lastState.terminated the kubelet kept")
	}
	if got.Last.ExitCode != 137 || got.Last.Reason != "OOMKilled" {
		t.Errorf("the last termination came out as %+v; want 137, OOMKilled", got.Last)
	}
	if ran := got.Last.FinishedAt.Sub(got.Last.StartedAt); ran != 41*time.Minute+30*time.Second {
		t.Errorf("the run lasted %s by its own timestamps; want 41m30s", ran)
	}
}

// No lastState is not "never stopped": it is a container the kubelet keeps no
// previous run for, and nothing is drawn rather than a claim.
func TestAContainerWithNoPreviousRunCarriesNone(t *testing.T) {
	got := statusOf(map[string]any{
		"name":  "api",
		"state": map[string]any{"running": map[string]any{}},
	})
	if got.Last != nil {
		t.Errorf("a container with no lastState came out with a last termination: %+v", got.Last)
	}
	// And an empty lastState - `{}`, which is what the kubelet writes for a
	// container that has not restarted - is the same answer.
	got = statusOf(map[string]any{
		"name":      "api",
		"state":     map[string]any{"running": map[string]any{}},
		"lastState": map[string]any{},
	})
	if got.Last != nil {
		t.Errorf("an empty lastState came out with a last termination: %+v", got.Last)
	}
}

// The same for init containers: a restartable sidecar is
// declared under `initContainers` and reported under `initContainerStatuses`,
// and it restarts like any other, so its last termination is read from that
// list. Through containersOf, so the path from the pod to the panel is the one
// under test, not statusOf alone.
func TestAnInitContainerCarriesItsLastTerminationToo(t *testing.T) {
	got := containersOf(nodeFrom(t, `
apiVersion: v1
kind: Pod
metadata:
  name: api-0
  namespace: shop
spec:
  initContainers:
    - name: proxy
      image: registry.example.com/proxy:1.0
      restartPolicy: Always
  containers:
    - name: api
      image: registry.example.com/api:1.25
status:
  initContainerStatuses:
    - name: proxy
      ready: true
      restartCount: 1
      state:
        running:
          startedAt: "2026-10-13T09:42:00Z"
      lastState:
        terminated:
          exitCode: 143
          reason: Error
          startedAt: "2026-10-13T09:00:00Z"
          finishedAt: "2026-10-13T09:41:00Z"
  containerStatuses:
    - name: api
      ready: true
      restartCount: 0
      state:
        running:
          startedAt: "2026-10-13T09:00:00Z"
      lastState: {}
`), "Pod")

	if len(got) != 2 || !got[0].Init || got[0].Name != "proxy" {
		t.Fatalf("containers came out as %+v; want the init container proxy first", got)
	}
	proxy := got[0].Status
	if proxy == nil || proxy.Last == nil {
		t.Fatalf("the init container's last termination was not read: %+v", proxy)
	}
	if proxy.State != ContainerRunning || proxy.Restarts != 1 {
		t.Errorf("the init container's current state came out as %+v", proxy)
	}
	if proxy.Last.ExitCode != 143 || proxy.Last.Reason != "Error" {
		t.Errorf("the init container's last termination came out as %+v; want 143, Error", proxy.Last)
	}
	if got[1].Status == nil || got[1].Status.Last != nil {
		t.Errorf("api has an empty lastState and came out with %+v", got[1].Status)
	}
}
