package overview

// podRequests against what the scheduler reserves, case by case.
//
// The expected figures are worked by hand from k8s.io/component-helpers
// v0.37.1, resource/helpers.go: PodRequests is aggregateContainerResourcesByFn
// plus Spec.Overhead, and aggregateContainerResourcesByFn charges an ordinary
// init container with the sidecars declared *before* it, not with all of them:
// charging all of them gives 3000m for a pod the scheduler reserves 2250m for.

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestRequestsAreWhatTheSchedulerReserves(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	sidecar := func(name, cpu, memory string) corev1.Container {
		return corev1.Container{Name: name, RestartPolicy: &always, Resources: requesting(cpu, memory)}
	}
	plain := func(name, cpu, memory string) corev1.Container {
		return corev1.Container{Name: name, Resources: requesting(cpu, memory)}
	}
	overhead := func(cpu, memory string) corev1.ResourceList {
		return corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(cpu),
			corev1.ResourceMemory: resource.MustParse(memory),
		}
	}
	const mi = int64(1) << 20

	for _, tc := range []struct {
		name       string
		init       []corev1.Container
		containers []corev1.Container
		overhead   corev1.ResourceList
		wantCPU    int64 // millicores
		wantMemory int64 // bytes
	}{
		{
			// Neither a sidecar nor an overhead: the plain sum of the
			// containers.
			name:       "plain pod",
			containers: []corev1.Container{plain("app", "250m", "256Mi"), plain("helper", "250m", "256Mi")},
			wantCPU:    500, wantMemory: 512 * mi,
		},
		{
			// Neither, with ordinary init containers: the largest one, not the
			// sum (TestInitContainerIsTheLargestAndNotTheSum's pod).
			name:       "ordinary init containers",
			init:       []corev1.Container{plain("migrate", "2", "1Gi"), plain("wait", "100m", "64Mi")},
			containers: []corev1.Container{plain("app", "250m", "256Mi"), plain("helper", "250m", "256Mi")},
			wantCPU:    2000, wantMemory: 1024 * mi,
		},
		{
			// A sidecar declared after an init container, without overhead. The
			// sidecar is not running while migrate runs: max(500m + 1, 2 + 0) =
			// 2, not 3000m.
			name:       "sidecar declared after the init container",
			init:       []corev1.Container{plain("migrate", "2", "1Gi"), sidecar("proxy", "1", "128Mi")},
			containers: []corev1.Container{plain("app", "500m", "256Mi")},
			wantCPU:    2000, wantMemory: 1024 * mi,
		},
		{
			// The same containers, the sidecar first: now it runs beside
			// migrate, and max(500m + 1, 2 + 1) = 3.
			name:       "sidecar declared before the init container",
			init:       []corev1.Container{sidecar("proxy", "1", "128Mi"), plain("migrate", "2", "1Gi")},
			containers: []corev1.Container{plain("app", "500m", "256Mi")},
			wantCPU:    3000, wantMemory: 1024*mi + 128*mi,
		},
		{
			// Two sidecars around one init container: only the first runs
			// beside it. Init phase 100m + 1 = 1.1; running 200m + 100m + 300m
			// = 600m. Charging both sidecars to the init phase would read 1400m.
			name: "sidecars on both sides of an init container",
			init: []corev1.Container{
				sidecar("first", "100m", "64Mi"),
				plain("migrate", "1", "512Mi"),
				sidecar("second", "300m", "128Mi"),
			},
			containers: []corev1.Container{plain("app", "200m", "128Mi")},
			wantCPU:    1100, wantMemory: 576 * mi,
		},
		{
			// A sidecar and nothing else to compete with: app plus sidecar
			// (TestNativeSidecarAddsToTheRequestRatherThanCompetingWithIt's pod).
			name:       "sidecar alone",
			init:       []corev1.Container{sidecar("proxy", "100m", "128Mi")},
			containers: []corev1.Container{plain("app", "250m", "256Mi")},
			wantCPU:    350, wantMemory: 384 * mi,
		},
		{
			// The RuntimeClass overhead (Kata, gVisor) is reserved on top of
			// the containers.
			name:       "overhead",
			containers: []corev1.Container{plain("app", "500m", "256Mi")},
			overhead:   overhead("250m", "64Mi"),
			wantCPU:    750, wantMemory: 320 * mi,
		},
		{
			// The whole probe: max(500m + 1, 2 + 0) + 250m = 2250m.
			name:       "sidecar after the init container, with overhead",
			init:       []corev1.Container{plain("migrate", "2", "1Gi"), sidecar("proxy", "1", "128Mi")},
			containers: []corev1.Container{plain("app", "500m", "256Mi")},
			overhead:   overhead("250m", "64Mi"),
			wantCPU:    2250, wantMemory: 1088 * mi,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := pod("p", "worker-1", corev1.PodRunning, tc.containers...)
			p.Spec.InitContainers = tc.init
			p.Spec.Overhead = tc.overhead

			got := podRequests(p)
			if got.cpuMilli != tc.wantCPU {
				t.Errorf("CPU request = %dm, want %dm", got.cpuMilli, tc.wantCPU)
			}
			if got.memoryBytes != tc.wantMemory {
				t.Errorf("memory request = %d, want %d", got.memoryBytes, tc.wantMemory)
			}
			if got.pods != 1 {
				t.Errorf("pod slots = %d, want 1", got.pods)
			}
		})
	}
}
