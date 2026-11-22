package web

import (
	"context"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/helm"
)

// helmRecorder answers one release per namespace it is asked to read, and
// remembers which namespaces those were.
type helmRecorder struct{ asked [][]string }

func (h *helmRecorder) Releases(_ context.Context, cluster string, namespaces []string) helm.Report {
	h.asked = append(h.asked, append([]string(nil), namespaces...))
	report := helm.Report{Cluster: cluster, State: helm.StatePresent, Scope: namespaces}
	for _, ns := range namespaces {
		report.Releases = append(report.Releases, helm.Release{Name: "app", Namespace: ns, Status: "deployed"})
	}
	if len(report.Releases) == 0 {
		report.State = helm.StateEmpty
	}
	return report
}

func (h *helmRecorder) History(context.Context, string, string, string) helm.History {
	return helm.History{}
}

// The release page narrows itself as every other list does: a person who may
// list Secrets in one namespace, and chose none, is shown that namespace's
// releases and told the page is narrowed - not refused and asked to choose.
func TestHelmReleasesForAOneNamespacePersonListThatNamespace(t *testing.T) {
	s, _ := readsServer(t)
	recorder := &helmRecorder{}
	s.helm = recorder
	reading, fail := apiReads{s}.HelmReleases(person("dana@example.com", "oidc:shop-secrets"), "prod", nil)
	if fail != nil {
		t.Fatalf("a person who may list Secrets in shop was refused the releases: %+v", fail)
	}
	if len(recorder.asked) != 1 || strings.Join(recorder.asked[0], ",") != "shop" {
		t.Fatalf("the releases were read in %v, want shop and nowhere else", recorder.asked)
	}
	if len(reading.Releases) != 1 || reading.Releases[0].Namespace != "shop" {
		t.Errorf("releases = %+v, want shop's", reading.Releases)
	}
	if reading.AccessNote == "" {
		t.Error("a narrowed release list does not say it is narrowed")
	}
}

// Who may list Secrets in no namespace is refused, said as such.
func TestHelmReleasesForAPersonAllowedNowhereAreRefused(t *testing.T) {
	s, _ := readsServer(t)
	recorder := &helmRecorder{}
	s.helm = recorder
	_, fail := apiReads{s}.HelmReleases(person("bob@example.com", "oidc:nobody"), "prod", nil)
	if fail == nil || fail.State != api.StateForbidden {
		t.Fatalf("fail = %+v, want forbidden", fail)
	}
	if !strings.Contains(fail.Reason.Message, "may not list secrets in any namespace") {
		t.Errorf("message = %q", fail.Reason.Message)
	}
	if len(recorder.asked) != 0 {
		t.Errorf("releases were read for a person allowed nowhere: %v", recorder.asked)
	}
}

// A person who may list Secrets everywhere reads every namespace, as before,
// and the page says nothing about narrowing.
func TestHelmReleasesForAPersonAllowedEverywhereAreNotNarrowed(t *testing.T) {
	s, _ := readsServer(t)
	recorder := &helmRecorder{}
	s.helm = recorder
	reading, fail := apiReads{s}.HelmReleases(person("erin@example.com", "oidc:readers"), "prod", nil)
	if fail != nil {
		t.Fatalf("fail = %+v", fail)
	}
	if len(recorder.asked) != 1 || len(recorder.asked[0]) != 0 || reading.AccessNote != "" {
		t.Errorf("asked %v, note %q: want every namespace and no note", recorder.asked, reading.AccessNote)
	}
}
