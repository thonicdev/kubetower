package redis

import (
	"net/http"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/svcgroup"
)

// podsBySelector is what is behind each Service when the EndpointSlices could
// not be read but the pods can: a person granted pods and Services and not
// EndpointSlices still gets the pods of each Redis, so a pod's tile and the
// inventory name them.
//
// Read for one namespace only: across the cluster it would be one pod listing
// per namespace holding a Service, the largest read the console makes. A
// refused or partial pod listing leaves every pod unknown, as before. The
// pods read are returned so the grouping does not list them a second time.
func podsBySelector(r *http.Request, h extension.Host, cluster, namespace string, services map[string]*corev1.Service, lists ...[]svcgroup.Instance) map[string]*corev1.Pod {
	page, err := h.List(r, cluster, extension.ListQuery{GVR: podsGVR, Namespace: namespace})
	if err != nil || !page.Complete {
		return nil
	}
	pods := make([]*corev1.Pod, 0, len(page.Items))
	byName := make(map[string]*corev1.Pod, len(page.Items))
	for i := range page.Items {
		var pod corev1.Pod
		if err := fromUnstructured(&page.Items[i], &pod); err != nil {
			return nil
		}
		pods = append(pods, &pod)
		byName[pod.Namespace+"/"+pod.Name] = &pod
	}
	for _, instances := range lists {
		attachBySelector(instances, services, pods)
	}
	return byName
}

// attachBySelector fills in Pods, Ready and Endpointless from the Service's
// selector, as the EndpointSlice controller would: the pods it selects that
// are not finished and have been given an address. A Service with no selector
// has its endpoints written by hand, which only the slices say, so its pods
// stay unknown.
func attachBySelector(instances []svcgroup.Instance, services map[string]*corev1.Service, pods []*corev1.Pod) {
	for i := range instances {
		service := services[instances[i].Namespace+"/"+instances[i].Service]
		if service == nil || len(service.Spec.Selector) == 0 {
			continue
		}
		selector := labels.SelectorFromSet(service.Spec.Selector)
		names := []string{}
		ready := 0
		for _, pod := range pods {
			if pod.Namespace != service.Namespace || !selector.Matches(labels.Set(pod.Labels)) {
				continue
			}
			if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed || pod.Status.PodIP == "" {
				continue
			}
			names = append(names, pod.Name)
			if podReady(pod) {
				ready++
			}
		}
		sort.Strings(names)
		instances[i].Pods = names
		instances[i].Ready = ready
		instances[i].Endpointless = len(names) == 0
	}
}

func podReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}
