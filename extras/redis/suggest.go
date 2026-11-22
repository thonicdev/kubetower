package redis

import (
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
)

// podSelectors are the labels a Redis or Valkey pod carries when no Service
// fronts it: the recommended label the charts set, and the older `app`.
// Asked through the API server's selector, so the fallback reads only those
// pods: this runs whenever a cluster's pages are opened.
var podSelectors = []string{
	"app.kubernetes.io/name in (redis,valkey)",
	"app in (redis,valkey)",
}

// Suggest is whether the cluster runs a Redis or a Valkey, for the banner that
// offers to turn the viewer on (extension.Suggester).
//
// From discovery alone, dialling nothing, and for the person behind r:
//
//   - the Services whose ports the inventory would offer (looksLikeRedis), so
//     an exporter's or a Sentinel's port suggests nothing on its own;
//   - failing those, pods labeled as a Redis or a Valkey (podSelectors).
//
// A listing that fails is an error, never "nothing found": the person sees no
// banner either way, but the log says which.
func (Redis) Suggest(r *http.Request, h extension.Host, cluster string) (string, error) {
	page, err := h.List(r, cluster, extension.ListQuery{GVR: servicesGVR})
	if err != nil {
		return "", err
	}
	services, namespaces := 0, map[string]bool{}
	for i := range page.Items {
		var service corev1.Service
		if err := fromUnstructured(&page.Items[i], &service); err != nil {
			return "", fmt.Errorf("a Service the cluster returned could not be read: %w", err)
		}
		for _, port := range service.Spec.Ports {
			if _, ok := looksLikeRedis(port); ok {
				services++
				namespaces[service.Namespace] = true
				break
			}
		}
	}
	if services > 0 {
		return atLeast(page.Complete) + found(services, "Service", len(namespaces)), nil
	}

	pods, complete := map[string]bool{}, true
	for _, selector := range podSelectors {
		page, err := h.List(r, cluster, extension.ListQuery{GVR: podsGVR, LabelSelector: selector})
		if err != nil {
			return "", err
		}
		complete = complete && page.Complete
		for i := range page.Items {
			pod := page.Items[i]
			pods[pod.GetNamespace()+"/"+pod.GetName()] = true
			namespaces[pod.GetNamespace()] = true
		}
	}
	if len(pods) > 0 {
		return atLeast(complete) + found(len(pods), "pod", len(namespaces)), nil
	}
	return "", nil
}

// atLeast is what a narrowed listing's count says (Page.Complete false): a
// floor, never an exact figure. Capitalized: it opens the banner's sentence.
func atLeast(complete bool) string {
	if complete {
		return ""
	}
	return "At least "
}

// found is the banner's evidence: "2 Redis or Valkey Services in 1 namespace".
func found(n int, what string, namespaces int) string {
	return fmt.Sprintf("%d Redis or Valkey %s in %d %s", n, plural(n, what), namespaces, plural(namespaces, "namespace"))
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

var _ extension.Suggester = Redis{}

// Component names what Suggest looks for (extension.Component).
func (Redis) Component() string { return "Redis or Valkey" }

var _ extension.Component = Redis{}
