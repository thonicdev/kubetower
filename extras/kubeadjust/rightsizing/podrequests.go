package rightsizing

import (
	corev1 "k8s.io/api/core/v1"
)

// What a pod holds on its node, counted the way the scheduler and
// `kubectl describe node` ("Allocated resources") count it, which is more than
// the sum of its regular containers and sidecars:
//
//   - **init containers**: a pod's request is the larger of what its running
//     containers ask together and what its largest init container asks with
//     the sidecars already started before it. A migration step asking 2Gi in
//     front of a 256Mi app reserves 2Gi while it runs, and the scheduler
//     placed the pod on that figure;
//   - **pod overhead**: a RuntimeClass (Kata, gVisor) adds a fixed amount per
//     pod, reserved like a request;
//   - **pod-level resources** (`spec.resources`, beta since 1.34): when a pod
//     sets them, they are its request, whatever its containers say.
//
// Limits follow the same arithmetic, which is what `kubectl describe node`
// shows; a container with no limit adds nothing to it, as there.

// PodResources is one pod's effective request and limit for one resource, in
// millicores for CPU and bytes otherwise.
func PodResources(pod *corev1.Pod, name corev1.ResourceName) (request, limit int64) {
	return effective(pod, name, false), effective(pod, name, true)
}

// effective is the scheduler's PodRequests (component-helpers' resource
// package), for one resource, rewritten to avoid pulling in that module.
func effective(pod *corev1.Pod, name corev1.ResourceName, limits bool) int64 {
	value := func(list corev1.ResourceList) int64 {
		q, ok := list[name]
		if !ok {
			return 0
		}
		if name == corev1.ResourceCPU {
			return q.MilliValue()
		}
		return q.Value()
	}
	of := func(r corev1.ResourceRequirements) int64 {
		if limits {
			return value(r.Limits)
		}
		return value(r.Requests)
	}

	var total int64
	if pod.Spec.Resources != nil && of(*pod.Spec.Resources) > 0 {
		total = of(*pod.Spec.Resources)
	} else {
		var running int64
		for i := range pod.Spec.Containers {
			running += of(pod.Spec.Containers[i].Resources)
		}
		var sidecars, initPeak int64
		for i := range pod.Spec.InitContainers {
			c := &pod.Spec.InitContainers[i]
			if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
				// A sidecar starts and stays: it is added to the running total,
				// and every init container after it runs beside it.
				sidecars += of(c.Resources)
				initPeak = max(initPeak, sidecars)
				continue
			}
			initPeak = max(initPeak, of(c.Resources)+sidecars)
		}
		total = max(running+sidecars, initPeak)
	}
	// Overhead is a reservation, and a limit too: the kubelet sizes the pod's
	// cgroup with it.
	if pod.Spec.Overhead != nil && (!limits || total > 0) {
		total += value(pod.Spec.Overhead)
	}
	return total
}
