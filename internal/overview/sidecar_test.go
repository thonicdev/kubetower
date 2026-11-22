package overview

// A native sidecar, on the overview's side.
//
// `resources.PodReady`, `PodStatus` and `PodRestarts` read a native sidecar's
// status out of InitContainerStatuses, and podHealthy must too: reading
// ContainerStatuses alone, a pod whose istio-proxy is in CrashLoopBackOff —
// `Init:CrashLoopBackOff`, 1/2, in red, in the Pods list — would count healthy,
// in the green Running segment and absent from the Trouble panel, while its
// own Ready condition says False.

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/resources"
)

// meshedPod is a Running pod with one app container and one native sidecar,
// owned by a StatefulSet so newestNotReady can be asked about it too.
func meshedPod(name string, app, sidecar corev1.ContainerStatus) *corev1.Pod {
	always := corev1.ContainerRestartPolicyAlways
	controller := true
	started := metav1.NewTime(clock.Add(-time.Hour))
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "StatefulSet", Name: "web", Controller: &controller,
			}},
		},
		Spec: corev1.PodSpec{
			NodeName:       "worker-1",
			InitContainers: []corev1.Container{{Name: "istio-proxy", RestartPolicy: &always}},
			Containers:     []corev1.Container{{Name: "app"}},
		},
		Status: corev1.PodStatus{
			Phase:                 corev1.PodRunning,
			StartTime:             &started,
			InitContainerStatuses: []corev1.ContainerStatus{sidecar},
			ContainerStatuses:     []corev1.ContainerStatus{app},
		},
	}
}

func TestANativeSidecarCountsTowardsAPodsHealth(t *testing.T) {
	yes, no := true, false
	running := corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	appReady := corev1.ContainerStatus{Name: "app", Ready: true, Started: &yes, State: running}

	for _, tc := range []struct {
		name string
		pod  *corev1.Pod
		// What the Pods list says about the same pod, asserted here first so the
		// fixture cannot drift away from the case it claims to be.
		wantStatus string
		wantReady  [2]int32 // ready, total
		// What the overview must agree with.
		wantHealthy bool
		wantDetail  string // the Trouble panel's row; empty when there is none
	}{
		{
			// The motivating case: the app is ready, the mesh proxy is
			// crash-looping. A sidecar in back-off has not `started`, so the
			// shared status names it.
			name: "crash-looping sidecar",
			pod: meshedPod("crashing-proxy", appReady, corev1.ContainerStatus{
				Name: "istio-proxy", Ready: false, Started: &no, RestartCount: 11,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			}),
			wantStatus:  "Init:CrashLoopBackOff",
			wantReady:   [2]int32{1, 2},
			wantHealthy: false,
			wantDetail:  "Init:CrashLoopBackOff",
		},
		{
			// Started but not ready: the init sequence has moved on, so the
			// shared status is the bare phase, and the panel's readiness suffix
			// is what names it.
			name: "sidecar started but not ready",
			pod: meshedPod("probing-proxy", appReady, corev1.ContainerStatus{
				Name: "istio-proxy", Ready: false, Started: &yes, State: running,
			}),
			wantStatus:  "Running",
			wantReady:   [2]int32{1, 2},
			wantHealthy: false,
			wantDetail:  "Running, not ready",
		},
		{
			// The control: a meshed pod that is fine must stay fine, or the rule
			// has only moved the lie to the other color.
			name: "sidecar ready",
			pod: meshedPod("healthy-proxy", appReady, corev1.ContainerStatus{
				Name: "istio-proxy", Ready: true, Started: &yes, State: running,
			}),
			wantStatus:  "Running",
			wantReady:   [2]int32{2, 2},
			wantHealthy: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resources.PodStatus(tc.pod); got != tc.wantStatus {
				t.Fatalf("fixture: PodStatus = %q, want %q", got, tc.wantStatus)
			}
			if r, n := resources.PodReady(tc.pod); r != tc.wantReady[0] || n != tc.wantReady[1] {
				t.Fatalf("fixture: PodReady = %d/%d, want %d/%d", r, n, tc.wantReady[0], tc.wantReady[1])
			}

			out, err := Workloads(context.Background(), accesstest.Allow(), client(tc.pod), "", nil)
			if err != nil {
				t.Fatalf("Workloads: %v", err)
			}

			// The summary.
			var pods KindSummary
			for _, k := range out.Kinds {
				if k.Kind == "Pods" {
					pods = k
				}
			}
			wantHealthy := 0
			if tc.wantHealthy {
				wantHealthy = 1
			}
			if pods.Total != 1 || pods.Healthy != wantHealthy {
				t.Errorf("Pods summary = %d of %d healthy, want %d of 1 — the Pods list reads %d/%d",
					pods.Healthy, pods.Total, wantHealthy, tc.wantReady[0], tc.wantReady[1])
			}

			// The breakdown: an unready pod leaves the Running segment.
			segments := map[string]int{}
			for _, s := range out.Phases {
				segments[s.Label] = s.Value
			}
			if tc.wantHealthy {
				if segments["Running"] != 1 || segments["Not ready"] != 0 {
					t.Errorf("phases = %+v, want the pod in Running", out.Phases)
				}
			} else if segments["Running"] != 0 || segments["Not ready"] != 1 {
				t.Errorf("phases = %+v, want the pod in Not ready and not in the green Running segment", out.Phases)
			}

			// The Trouble panel.
			var rows []Trouble
			for _, row := range out.Trouble {
				if row.Kind == "Pod" {
					rows = append(rows, row)
				}
			}
			switch {
			case tc.wantDetail == "" && len(rows) != 0:
				t.Errorf("Trouble = %+v, want no row for a healthy pod", rows)
			case tc.wantDetail != "" && (len(rows) != 1 || rows[0].Detail != tc.wantDetail):
				t.Errorf("Trouble = %+v, want one row reading %q", rows, tc.wantDetail)
			}

			// And the date a StatefulSet's shortfall is given: the pod is a
			// candidate only when it is not healthy.
			since := newestNotReady([]corev1.Pod{*tc.pod}, "StatefulSet", "default", "web")
			if tc.wantHealthy != since.IsZero() {
				t.Errorf("newestNotReady = %v, want zero only for a healthy pod", since)
			}
		})
	}
}

// The other side of counting sidecars: an ordinary init container that has
// finished is not one of the containers a running pod is made of, and must not
// turn a ready pod unready. Asserted because "count the init statuses" is the
// obvious wrong fix.
func TestAFinishedInitContainerDoesNotMakeAPodUnready(t *testing.T) {
	yes := true
	p := pod("migrated", "worker-1", corev1.PodRunning, corev1.Container{Name: "app"})
	p.Spec.InitContainers = []corev1.Container{{Name: "migrate"}}
	p.Status.InitContainerStatuses = []corev1.ContainerStatus{{
		Name: "migrate", Ready: false,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed"}},
	}}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", Ready: true, Started: &yes}}

	if !podHealthy(p) {
		t.Error("a Running pod whose only init container completed and whose app is ready reads unhealthy")
	}
}
