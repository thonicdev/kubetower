package actions

import (
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/edit"
)

// What a verb will be reviewed as, said before it runs.
//
// A page offers a verb only where the person may perform it, and the honest
// source for "may" is the question the write itself will ask. So this uses
// edit.Attributes, the function the write door reviews through, and a test
// holds every verb's prediction equal to its recorded review.
//
// It is a prediction, and the write is still the control: an offered verb
// may still be refused (a grant removed within the review's minute, a pod in
// a namespace the person cannot evict in), and the act says so.

// jobsGVR is where a trigger writes. RBAC ignores the version.
var jobsGVR = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}

// podsGVR is where an eviction goes, for a drain whose pods are not the object
// on screen.
var podsGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

// Needs is every question performing verb on target will be reviewed with.
// where is the target's resource as the cluster serves it. Empty for a verb
// this console does not perform.
//
// Drain is the one approximation: its evictions target pods it has not
// listed yet, so the question is whether the person may evict pods anywhere.
func Needs(verb Verb, where edit.Location, target edit.Target) []access.Attributes {
	on := func(verb, subresource string) access.Attributes {
		return edit.Attributes(where, target, verb, subresource)
	}
	switch verb {
	case Delete:
		return []access.Attributes{on("delete", "")}
	case Restart:
		if isPod(where.Kind) {
			// A pod restart is its delete - see restart.
			return []access.Attributes{on("delete", "")}
		}
		return []access.Attributes{on("patch", "")}
	case Evict:
		return []access.Attributes{on("create", "eviction")}
	case Cordon, Uncordon, Suspend, Resume, Scale:
		return []access.Attributes{on("patch", "")}
	case Drain:
		pods := edit.Location{Resource: podsGVR, Namespaced: true}
		return []access.Attributes{
			on("patch", ""),
			edit.Attributes(pods, edit.Target{}, "create", "eviction"),
		}
	case Trigger:
		// `get` on the CronJob as well as `create` on Jobs, as kubectl's
		// `create job --from=cronjob/x` needs: the template is read with the
		// console's rights, so `create jobs` alone would let a person who may
		// not read the CronJob run what it holds.
		jobs := edit.Location{Resource: jobsGVR, Namespaced: true}
		return []access.Attributes{
			on("get", ""),
			edit.Attributes(jobs, edit.Target{Namespace: target.Namespace}, "create", ""),
		}
	}
	return nil
}
