// Package kindref turns the kind a URL carries into something a cluster can be
// asked about.
//
// It is one package so that `internal/detail` and `internal/changes` cannot
// translate differently. A custom resource's URL carries the qualified name
// (`applicationsets.argoproj.io`), because two operators may each define a
// "Policy"; passed whole as a GroupVersionResource's `Resource` it matches
// nothing, since a RESTMapper holds resource and group in separate fields.
package kindref

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Resolve turns "pods", "po", "deployments.apps" or
// "applicationsets.argoproj.io" into the kind behind it.
//
// The order is kubectl's:
//
//  1. Fully qualified, `resource.version.group`, exact when it applies. It is a
//     fall-through rather than a test of the string's shape, because
//     `applicationsets.argoproj.io` also parses this way, as nonsense the
//     mapper refuses.
//  2. Resource plus group, what a custom resource's URL carries.
//  3. The bare string, what a built-in kind's URL carries.
//
// A discovery-built mapper knows singular, plural and short names, so `po`,
// `pod` and `pods` all resolve without help here.
func Resolve(mapper meta.RESTMapper, kind string) (schema.GroupVersionKind, error) {
	arg := strings.ToLower(strings.TrimSpace(kind))
	if arg == "" {
		return schema.GroupVersionKind{}, fmt.Errorf("no kind was given")
	}

	for _, candidate := range candidates(arg) {
		if gvk, err := mapper.KindFor(candidate); err == nil {
			return gvk, nil
		}
	}

	// Not the mapper's error: it names a GroupVersionResource nobody typed.
	return schema.GroupVersionKind{}, fmt.Errorf("%q is not a kind this cluster serves", kind)
}

// candidates is the list Resolve walks, in order.
func candidates(arg string) []schema.GroupVersionResource {
	fullySpecified, groupResource := schema.ParseResourceArg(arg)

	out := make([]schema.GroupVersionResource, 0, 3)
	if fullySpecified != nil {
		out = append(out, *fullySpecified)
	}
	if groupResource.Group != "" {
		out = append(out, groupResource.WithVersion(""))
	}
	return append(out, schema.GroupVersionResource{Resource: arg})
}

// Mapping resolves a kind all the way to a REST mapping, which is what a caller
// needs to address it: the resource, the version and the scope. Prefer it to
// Resolve: a guessed scope reads a node's name as a namespace, which passes
// every validator and reaches `kubectl -n`.
func Mapping(mapper meta.RESTMapper, kind string) (*meta.RESTMapping, error) {
	gvk, err := Resolve(mapper, kind)
	if err != nil {
		return nil, err
	}
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, fmt.Errorf("%q resolves to %s and that has no endpoint on this cluster",
			kind, gvk.GroupKind())
	}
	return mapping, nil
}
