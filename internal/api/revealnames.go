package api

import (
	"regexp"
	"strings"
)

// The names a reveal may carry, checked against Kubernetes' own rules before
// anything is read or logged.
//
// The reveal writes an audit line naming the namespace, object, key and
// container, all from the query string; a newline in one could forge a
// separate `write:` line in the record of who read a Secret. The line is
// escaped where it is written (`internal/web`, identity.Line); this is the
// other half: a value no Kubernetes object could carry is refused as a bad
// request rather than read and logged.
//
// The rules are written out here because this package carries no Kubernetes
// dependency; TestTheRevealNamesAreKubernetesOwnRules holds them to
// `k8s.io/apimachinery/pkg/util/validation`.

// dns1123Label is a namespace's and a container's name: at most 63 characters of
// lower-case letters, digits and '-', starting and ending alphanumeric.
var dns1123Label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// dns1123Subdomain is a ConfigMap's and a Secret's name: dot-separated labels,
// at most 253 characters.
var dns1123Subdomain = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

// configMapKey is a key of a ConfigMap's or a Secret's data: letters, digits,
// '-', '_' and '.', at most 253 characters, and never `.`, `..` or a name
// starting `..`.
var configMapKey = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)

func isDNSLabel(s string) bool { return len(s) <= 63 && dns1123Label.MatchString(s) }

func isDNSSubdomain(s string) bool { return len(s) <= 253 && dns1123Subdomain.MatchString(s) }

func isDataKey(s string) bool {
	return len(s) <= 253 && configMapKey.MatchString(s) && s != "." && !strings.HasPrefix(s, "..")
}

// revealNamesProblem is why a reveal's names cannot be any object's, or "" when
// they can. The namespace and the container may be absent; when present they
// are checked too.
func revealNamesProblem(ref ObjectRef, key, container string) string {
	switch {
	case ref.Namespace != "" && !isDNSLabel(ref.Namespace):
		return "namespace is not a Kubernetes namespace name"
	case !isDNSSubdomain(ref.Name):
		return "name is not a Kubernetes object name"
	case !isDataKey(key):
		return "key is not a key a ConfigMap or a Secret can hold: letters, digits, '-', '_' and '.'"
	case container != "" && !isDNSLabel(container):
		return "container is not a Kubernetes container name"
	}
	return ""
}
