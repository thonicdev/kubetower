package report

// Where Argo CD actually is.
//
// The namespace is discovered, never assumed: many installations are not in
// `argocd`, and a hardcoded namespace would report "not installed" as an empty
// answer. The evidence, strongest first, and the page says which answered:
//
//  1. The argocd-server Service, labeled
//     `app.kubernetes.io/name=argocd-server` by the chart and the upstream
//     manifests.
//  2. Any Argo CD component (`app.kubernetes.io/part-of=argocd`), which finds
//     a core install with no server.
//  3. The AppProjects' namespace, already read: Argo CD's controller honors
//     AppProjects only in its own namespace.
//
// Applications come last and only say where the Applications are: with
// "applications in any namespace" they live with the tenants, not with Argo
// CD.

import (
	"sort"
	"strings"

	"github.com/thonicdev/kubetower/textutil"
)

// serviceLimit bounds the probe: an installation has a handful of Services,
// and hundreds under this label means the label means something else.
const serviceLimit = 20

// The two label selectors, in the order they are tried: what the Argo CD Helm
// chart and the upstream `install.yaml` put on their objects.
const (
	serverSelector    = "app.kubernetes.io/name=argocd-server"
	componentSelector = "app.kubernetes.io/part-of=argocd"
)

// Install is where Argo CD was found, and how.
type Install struct {
	// Namespaces holds every namespace the evidence pointed at, sorted. Several
	// is a real answer (one Argo CD per tenant).
	Namespaces []string
	// How is the sentence saying which evidence answered, always set when
	// Namespaces is. It is a whole capitalized sentence up to the namespaces,
	// which the caller appends.
	How string
	// Unavailable is why the Services could not be asked. It qualifies the
	// answer: the evidence from the objects still stands.
	Unavailable string
}

// Found reports whether anything at all named an installation.
func (i Install) Found() bool { return len(i.Namespaces) > 0 }

// ProbeInstall asks the cluster's Services where Argo CD is, through `list`,
// which answers the namespaces of the Services a label selector matches.
//
// Errors are carried rather than returned: an identity may list Applications
// and not Services, and the page must not lose the first over the second.
func ProbeInstall(list func(selector string) ([]string, error)) Install {
	if list == nil {
		return Install{Unavailable: "this build could not ask for the cluster's Services"}
	}
	for _, attempt := range []struct {
		selector string
		how      string
	}{
		{serverSelector, "Found the argocd-server Service in"},
		{componentSelector, "Found Argo CD's own components in"},
	} {
		namespaces, err := list(attempt.selector)
		if err != nil {
			// The first refusal ends the probe: the second selector would be
			// refused identically.
			return Install{Unavailable: textutil.FirstLineN(err.Error(), messageLimit)}
		}
		if found := unique(namespaces); len(found) > 0 {
			return Install{Namespaces: found, How: attempt.how}
		}
	}
	return Install{}
}

// completedBy falls back to the AppProjects and Applications the page already
// read, without asking the cluster again.
func (i Install) completedBy(projects []rawProject, apps []Application) Install {
	if i.Found() {
		return i
	}

	var projectNamespaces []string
	for n := range projects {
		projectNamespaces = append(projectNamespaces, projects[n].Metadata.Namespace)
	}
	if found := unique(projectNamespaces); len(found) > 0 {
		i.Namespaces = found
		// Worded as the inference it is.
		i.How = "No Argo CD Service answered; Argo reads AppProjects only from its own namespace, and they are in"
		return i
	}

	var appNamespaces []string
	for _, a := range apps {
		appNamespaces = append(appNamespaces, a.Namespace)
	}
	if found := unique(appNamespaces); len(found) > 0 {
		i.Namespaces = found
		// The weakest evidence, worded as about the Applications rather
		// than the installation.
		i.How = "Nothing named an installation; the Applications themselves live in"
		return i
	}
	return i
}

// unique sorts and deduplicates, dropping the empty string so an object
// without a namespace never reads as an installation in namespace "".
func unique(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
