package customresources

// The kinds the Kubernetes project owns, and where the menu files them.
//
// Filing every non-typed kind under "Custom Resources" by API group would put
// the Gateway API beside cert-manager, where nobody looks for a Gateway, and
// leave kinds the API server serves itself (VolumeAttachments, the DRA kinds)
// with no entry. So: a rule about who owns a group, a section per group, and
// a short list of exclusions. Which of these kinds a cluster serves is
// discovery's to say (discovered.go).

import (
	"strings"
	"unicode"
)

// ProjectGroup reports whether an API group belongs to the Kubernetes project:
// the core group, `k8s.io` or `kubernetes.io` or a group under either, plus
// the core API's older groups `apps`, `batch`, `autoscaling` and `policy`,
// which a third party cannot claim. `x-k8s.io` groups are not under `k8s.io`:
// projects that chose that suffix (Cluster API, Kueue) asked to stay under
// Custom Resources.
func ProjectGroup(group string) bool {
	switch group {
	case "", "k8s.io", "kubernetes.io", "apps", "batch", "autoscaling", "policy":
		return true
	}
	return strings.HasSuffix(group, ".k8s.io") || strings.HasSuffix(group, ".kubernetes.io")
}

// The menu sections a project kind can be filed under, spelt exactly as
// internal/resources/kinds.go spells its headings, since the menu joins them
// by label (a test holds the two together).
const (
	SectionWorkloads     = "Workloads"
	SectionNetwork       = "Network"
	SectionConfiguration = "Configuration"
	SectionAccessControl = "Access Control"
	SectionStorage       = "Storage"
)

// sections files a project group under a heading. A suffix match covers a
// family of groups, so a new group under `networking.k8s.io` lands under
// Network without a line here. Nothing overlaps, so order does not matter.
var sections = []struct {
	group   string
	section string
}{
	// ControllerRevisions are read beside their DaemonSet or StatefulSet.
	{"apps", SectionWorkloads},
	{"batch", SectionWorkloads},
	// The Gateway API, ServiceCIDRs and IPAddresses beside Services;
	// external-dns's records beside what they name.
	{"networking.k8s.io", SectionNetwork},
	{"discovery.k8s.io", SectionNetwork},
	{"externaldns.k8s.io", SectionNetwork},
	// VolumeAttachments, the CSI kinds, volume snapshots and COSI buckets
	// beside StorageClasses and claims.
	{"storage.k8s.io", SectionStorage},
	{"objectstorage.k8s.io", SectionStorage},
	// Certificate requests and trust bundles are credentials being issued,
	// beside RBAC.
	{"certificates.k8s.io", SectionAccessControl},
	{"rbac.authorization.k8s.io", SectionAccessControl},
}

// sectionOf is where a project kind is filed.
//
// Configuration is the fallback on purpose: it already holds the cluster's
// machinery (leases, priority and runtime classes, webhooks), so admission
// policies, flow control, APIServices and DRA land beside their kin, and a
// future group is not filed somewhere wrong. PodTemplate is the one core kind
// filed by resource rather than group.
func sectionOf(group, resource string) string {
	if group == "" && resource == "podtemplates" {
		return SectionWorkloads
	}
	for _, s := range sections {
		if group == s.group || strings.HasSuffix(group, "."+s.group) {
			return s.section
		}
	}
	return SectionConfiguration
}

// exclusion is a project kind, or a whole project group, deliberately not
// offered, and why. Resource "" is every resource of the group.
type exclusion struct {
	group, resource string
	reason          string
}

// exclusions are the project kinds the menu does not offer although the
// cluster serves them and they can be listed. Create-only kinds (no `list`
// verb) are left out by `offered` in discovered.go, not named here.
var exclusions = []exclusion{
	{"events.k8s.io", "events",
		"the same objects as core Events, which the menu already lists; two entries would be one list twice"},
	{"metrics.k8s.io", "",
		"metrics-server's readings, which already feed the CPU and memory gauges; a node's or pod's figure is not an object anyone lists"},
	{"custom.metrics.k8s.io", "",
		"an aggregated metrics API: values asked for by name, not stored objects"},
	{"external.metrics.k8s.io", "",
		"an aggregated metrics API: values asked for by name, not stored objects"},
	{"", "componentstatuses",
		"deprecated since 1.19 and computed by probing on every read; it says nothing true on a cluster whose control plane is not on the API server's host"},
}

// excluded is the reason a project kind is not offered, if it is not.
func excluded(group, resource string) (string, bool) {
	for _, e := range exclusions {
		if e.group == group && (e.resource == "" || e.resource == resource) {
			return e.reason, true
		}
	}
	return "", false
}

// kindTitle is a project kind's heading, written as the console writes its
// own: "Volume attachments", "HTTP routes", "Service CIDRs".
//
// Derived rather than written per kind, so a newly discovered kind is titled
// the same way. The kind is split into words, words after the first are lower
// case unless acronyms, and the last word takes the plural's ending. Where
// the plural does not spell the kind's words, the plural is used, capitalized.
func kindTitle(kind, plural string) string {
	words := camelWords(kind)
	if len(words) == 0 {
		return capitalise(plural)
	}
	rest := strings.ToLower(plural)
	for _, w := range words[:len(words)-1] {
		lower := strings.ToLower(w)
		if !strings.HasPrefix(rest, lower) {
			return capitalise(plural)
		}
		rest = rest[len(lower):]
	}
	// What is left is the last word's plural: "routes", "policies", "cidrs".
	last := words[len(words)-1]
	if rest == "" || rest[0] != strings.ToLower(last)[0] {
		return capitalise(plural)
	}

	out := make([]string, len(words))
	for i, w := range words[:len(words)-1] {
		switch {
		case acronym(w) || capitals(w) > 1:
			out[i] = w
		case i == 0:
			out[i] = capitalise(strings.ToLower(w))
		default:
			out[i] = strings.ToLower(w)
		}
	}
	switch {
	case (acronym(last) || capitals(last) > 1) && strings.HasPrefix(rest, strings.ToLower(last)):
		// The acronym keeps its capitals and the ending stays lower case,
		// as the API reference writes it.
		out[len(out)-1] = last + rest[len(last):]
	case len(words) == 1:
		out[0] = capitalise(rest)
	default:
		out[len(out)-1] = rest
	}
	return strings.Join(out, " ")
}

// camelWords splits "HTTPRoute" into HTTP and Route, "IPAddress" into IP and
// Address, "VolumeSnapshotContent" into its three words.
func camelWords(s string) []string {
	runes := []rune(s)
	var words []string
	start := 0
	for i := 1; i < len(runes); i++ {
		prev, cur := runes[i-1], runes[i]
		next := rune(0)
		if i+1 < len(runes) {
			next = runes[i+1]
		}
		after := rune(0)
		if i+2 < len(runes) {
			after = runes[i+2]
		}
		// A capital after a lower case letter or a digit starts a word
		// ("PoolSet", "S3Bucket").
		wordStart := (unicode.IsLower(prev) || unicode.IsDigit(prev)) && unicode.IsUpper(cur)
		// The last capital of an acronym starts the next word ("HTTPRoute"),
		// only before two lower case letters, so "IPv6" stays whole.
		acronymEnd := unicode.IsUpper(prev) && unicode.IsUpper(cur) && unicode.IsLower(next) && unicode.IsLower(after)
		if wordStart || acronymEnd {
			words = append(words, string(runes[start:i]))
			start = i
		}
	}
	if start < len(runes) {
		words = append(words, string(runes[start:]))
	}
	return words
}

// capitals counts a word's capital letters: a word with two or more ("IPv6")
// is spelt as its author spelt it.
func capitals(w string) int {
	n := 0
	for _, r := range w {
		if unicode.IsUpper(r) {
			n++
		}
	}
	return n
}

// acronym is a word of two capitals or more: HTTP, CSI, CIDR.
func acronym(w string) bool {
	if len(w) < 2 {
		return false
	}
	for _, r := range w {
		if !unicode.IsUpper(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
