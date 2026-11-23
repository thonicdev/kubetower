package runtimeinspect

import (
	"fmt"
	"net/http"

	"github.com/thonicdev/kubetower/extension"
)

// Suggest is whether the cluster runs probable Java pods, for the banner that
// offers to turn the inspector on (extension.Suggester).
//
// One pod list through Host.List, reviewed for the person behind r, read
// against the spec alone: nothing is run in a container. A pod that has
// finished is not counted. A listing that fails is an error, never "nothing
// found"; a narrowed one gives a floor, said as one.
func (JVM) Suggest(r *http.Request, h extension.Host, cluster string) (string, error) {
	page, err := h.List(r, cluster, extension.ListQuery{GVR: podsGVR})
	if err != nil {
		return "", err
	}
	pods, _, _ := scan(page.Items)
	if len(pods) == 0 {
		return "", nil
	}
	namespaces := map[string]bool{}
	for _, p := range pods {
		namespaces[p.Namespace] = true
	}
	floor := ""
	if !page.Complete {
		floor = "At least "
	}
	return fmt.Sprintf("%s%d probable Java %s in %d %s", floor, len(pods), plural(len(pods), "pod"),
		len(namespaces), plural(len(namespaces), "namespace")), nil
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

var _ extension.Suggester = JVM{}

// Component names what Suggest looks for (extension.Component).
func (JVM) Component() string { return "Java pods" }

var _ extension.Component = JVM{}
