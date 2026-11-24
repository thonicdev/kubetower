package runtimeinspect

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/thonicdev/kubetower/extension"
)

// Suggest is whether the cluster runs pods of a runtime it knows, for the banner that
// offers to turn the inspector on (extension.Suggester).
//
// One pod list through Host.List, reviewed for the person behind r, read
// against the spec alone: nothing is run in a container. A pod that has
// finished is not counted, nor a container read as Go from its image's
// product name alone (productOnly). A listing that fails is an error, never
// "nothing found"; a narrowed one gives a floor, said as one.
func (JVM) Suggest(r *http.Request, h extension.Host, cluster string) (string, error) {
	page, err := h.List(r, cluster, extension.ListQuery{GVR: podsGVR})
	if err != nil {
		return "", err
	}
	pods, _, _ := scanFor(page.Items, func(c Container) bool { return c.Runtime != "" && !productOnly(c) })
	if len(pods) == 0 {
		return "", nil
	}
	// From the containers kept, not p.Runtimes: a Go container read from its
	// image's product alone is not named.
	var runtimes []string
	for _, p := range pods {
		for _, c := range p.Containers {
			if !slices.Contains(runtimes, c.Runtime) {
				runtimes = append(runtimes, c.Runtime)
			}
		}
	}
	slices.SortFunc(runtimes, func(a, b string) int { return providerOrder(a) - providerOrder(b) })
	namespaces := map[string]bool{}
	for _, p := range pods {
		namespaces[p.Namespace] = true
	}
	floor := ""
	if !page.Complete {
		floor = "At least "
	}
	return fmt.Sprintf("%s%d probable %s %s in %d %s", floor, len(pods), runtimesLabel(runtimes), plural(len(pods), "pod"),
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
func (JVM) Component() string { return "Java, Go, Node.js or Python pods" }

var _ extension.Component = JVM{}
