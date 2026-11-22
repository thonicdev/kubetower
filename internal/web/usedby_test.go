package web

// The used-by panel's three sentences, asserted separately and in the response bytes.
//
// **Unused, refused and capped are three different answers**, and this file
// exists because rendering any two of them the same is the collapse of an
// empty answer and a failed one — which on this panel is the entire product. A
// person meets "nothing found" on more than half the ConfigMaps and Secrets
// they open (67 of 129 and 65 of 115 on a measured cluster), and the obvious
// reading of it is "safe to delete".

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/detail"
)

func usedByPage(t *testing.T, kind, name string, use *detail.Usage, relations ...detail.Relation) api.DetailPage {
	t.Helper()
	return detailPageOf(
		api.ObjectRef{Cluster: "prod", Kind: kind, Namespace: "shop", Name: name},
		detail.Object{
			Kind: kind, Title: "ConfigMap", Name: name, Namespace: "shop",
			Relations: relations, Usage: use,
		},
	)
}

// The bytes, because a member that marshals away is a member nothing draws.
func wire(t *testing.T, page api.DetailPage) string {
	t.Helper()
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("marshalling the page: %v", err)
	}
	return string(raw)
}

// 1 — walked, found nothing. The sentence a person acts on, and the one this
// whole member was worth its contract change for.
func TestAConfigMapNothingUsesSaysNothingFoundAndNotUnused(t *testing.T) {
	page := usedByPage(t, "configmaps", "orphan", &detail.Usage{
		Subject: "ConfigMap",
		Looked:  []string{"Deployments", "StatefulSets", "DaemonSets", "CronJobs", "Jobs that have not finished"},
	})

	if page.UsedBy == nil {
		t.Fatal("no answer at all: 'walked and found nothing' renders as 'never walked'")
	}
	if page.UsedBy.State != api.StateEmpty {
		t.Fatalf("state = %q, want empty", page.UsedBy.State)
	}
	if page.UsedBy.Reason == nil {
		t.Fatal("empty with no sentence, which on this panel is silence")
	}
	message := page.UsedBy.Reason.Message
	// It must never say the word a person would act on.
	if strings.Contains(strings.ToLower(message), "unused") &&
		!strings.Contains(message, "not the same as unused") {
		t.Errorf("the sentence calls it unused: %q", message)
	}
	// And it must name the limit no walk can pass. A ConfigMap picked up by
	// label — a Grafana dashboard, a Prometheus rule — is named in no object
	// anywhere, so a panel that does not say so is confidently wrong on exactly
	// the objects somebody is most likely to delete.
	for _, must := range []string{"label", "Grafana", "not the same as unused", "not as safe to delete"} {
		if !strings.Contains(message, must) {
			t.Errorf("the sentence does not admit %q: %q", must, message)
		}
	}
	// The audit behind it: "nothing" is only worth reading beside what it was
	// looked for in.
	if !strings.Contains(page.UsedBy.Reason.Detail, "namespace shop") ||
		!strings.Contains(page.UsedBy.Reason.Detail, "Deployments") {
		t.Errorf("the reason does not say what was listed, or where: %q", page.UsedBy.Reason.Detail)
	}
	if body := wire(t, page); !strings.Contains(body, `"usedBy"`) || !strings.Contains(body, `"empty"`) {
		t.Errorf("the answer does not reach the wire: %s", body)
	}
}

// A Secret's sentence names its own blind spot, which is a different one: a
// Secret is rarely selected by label and is routinely named by a custom resource
// nothing here enumerates.
func TestASecretsEmptyAnswerNamesTheCustomResourcesItCannotSee(t *testing.T) {
	page := detailPageOf(
		api.ObjectRef{Cluster: "prod", Kind: "secrets", Namespace: "shop", Name: "orphan"},
		detail.Object{Kind: "secrets", Title: "Secret", Name: "orphan", Namespace: "shop",
			Usage: &detail.Usage{Subject: "Secret", Looked: []string{"Deployments", "PersistentVolumes"}}},
	)
	message := page.UsedBy.Reason.Message
	if !strings.Contains(message, "cert-manager") {
		t.Errorf("a Secret's sentence does not name the controllers that hold the reference: %q", message)
	}
	if strings.Contains(message, "ConfigMap") {
		t.Errorf("a Secret is told a ConfigMap's sentence: %q", message)
	}
}

// Every walked kind is told its own blind spot. A Role, a ClusterRole or a
// ServiceAccount told about Grafana dashboards and kube-root-ca.crt reaches the
// right conclusion for reasons that cannot apply to it; a Role or a ClusterRole
// is not named by any pod, counted or not. The ConfigMap's and the Secret's
// sentences are pinned to the byte.
func TestEachWalkedKindIsToldItsOwnBlindSpot(t *testing.T) {
	const configMap = "Nothing that was looked at names this ConfigMap. That is not the same as unused: " +
		"a controller can pick a ConfigMap up by label without naming it in any object at all — " +
		"a Grafana dashboard and a Prometheus rule work exactly that way, and no walk can see them; " +
		"Kubernetes projects kube-root-ca.crt into every pod from no template at all, " +
		"and a bare pod, a finished Job and a hand-made ReplicaSet were not counted. " +
		"Read it as nothing found, not as safe to delete."
	const secret = "Nothing that was looked at names this Secret. That is not the same as unused: " +
		"a custom resource can hold the reference in a field no walk enumerates — " +
		"cert-manager, ExternalSecrets and Argo CD repository credentials all do, " +
		"and a bare pod, a finished Job and a hand-made ReplicaSet were not counted. " +
		"Read it as nothing found, not as safe to delete."

	for _, tc := range []struct {
		resource, subject string
		exact             string
		must, mustNot     []string
	}{
		{resource: "configmaps", subject: "ConfigMap", exact: configMap},
		{resource: "secrets", subject: "Secret", exact: secret},
		{
			resource: "roles", subject: "Role",
			must:    []string{"RoleBinding", "not as safe to delete"},
			mustNot: []string{"ConfigMap", "Grafana", "kube-root-ca.crt", "bare pod"},
		},
		{
			resource: "clusterroles", subject: "ClusterRole",
			must:    []string{"aggregationRule", "not as safe to delete"},
			mustNot: []string{"ConfigMap", "Grafana", "kube-root-ca.crt", "bare pod"},
		},
		{
			resource: "serviceaccounts", subject: "ServiceAccount",
			must:    []string{"outside the cluster", "bare pod", "not as safe to delete"},
			mustNot: []string{"Grafana"},
		},
	} {
		t.Run(tc.subject, func(t *testing.T) {
			page := detailPageOf(
				api.ObjectRef{Cluster: "prod", Kind: tc.resource, Namespace: "shop", Name: "orphan"},
				detail.Object{Kind: tc.resource, Title: tc.subject, Name: "orphan", Namespace: "shop",
					Usage: &detail.Usage{Subject: tc.subject, Looked: []string{"Deployments"}}},
			)
			if page.UsedBy == nil || page.UsedBy.Reason == nil {
				t.Fatal("no sentence for a walk that found nothing")
			}
			message := page.UsedBy.Reason.Message
			if tc.exact != "" && message != tc.exact {
				t.Errorf("the %s sentence changed:\n got %q\nwant %q", tc.subject, message, tc.exact)
			}
			for _, must := range tc.must {
				if !strings.Contains(message, must) {
					t.Errorf("a %s's sentence does not say %q: %q", tc.subject, must, message)
				}
			}
			for _, not := range tc.mustNot {
				if strings.Contains(message, not) {
					t.Errorf("a %s is told %q, which cannot apply to it: %q", tc.subject, not, message)
				}
			}
		})
	}
}

// 2 — refused. A person with no `list` in the namespace gets a refusal, and a
// refusal is not an absence: the walk established nothing.
func TestARefusedWalkIsForbiddenAndNeverEmpty(t *testing.T) {
	page := usedByPage(t, "configmaps", "api-config", &detail.Usage{
		Subject: "ConfigMap",
		Refused: []string{"Deployments", "StatefulSets"},
		Err:     errForbidden(`deployments.apps is forbidden: User "reader" cannot list resource "deployments"`),
	})

	if page.UsedBy == nil {
		t.Fatal("no answer at all")
	}
	if page.UsedBy.State == api.StateEmpty {
		t.Fatal("a refusal renders as 'nothing uses this', which is the collapse this member exists to prevent")
	}
	if page.UsedBy.State != api.StateForbidden {
		t.Fatalf("state = %q, want forbidden", page.UsedBy.State)
	}
	message := page.UsedBy.Reason.Message
	if !strings.Contains(message, "Deployments and StatefulSets") {
		t.Errorf("the refusal does not name what could not be listed: %q", message)
	}
	if !strings.Contains(page.UsedBy.Reason.Detail, "cannot list") {
		t.Errorf("the cluster's own wording is lost: %q", page.UsedBy.Reason.Detail)
	}
	if body := wire(t, page); !strings.Contains(body, `"forbidden"`) {
		t.Errorf("the refusal does not reach the wire: %s", body)
	}
}

// A partial walk that still found something stays `ok` — some of the referrers
// is a usable answer — and says which half it is in `relationsNote`, which is
// the member that already means "what was left out and why".
func TestAPartialWalkThatFoundSomethingStaysOK(t *testing.T) {
	page := usedByPage(t, "configmaps", "api-config",
		&detail.Usage{Subject: "ConfigMap", Found: 1, Total: 1,
			Refused: []string{"DaemonSets"}, Err: errForbidden("cannot list daemonsets")},
		detail.Relation{Role: detail.RoleUsedBy, Kind: "deployments", Namespace: "shop",
			Name: "api", Label: "Deployment/api"},
	)
	if page.UsedBy.State != api.StateOK {
		t.Fatalf("state = %q, want ok — rows were found and are on the panel", page.UsedBy.State)
	}
	if page.UsedBy.Reason != nil {
		t.Errorf("an ok answer carries a reason: %+v", page.UsedBy.Reason)
	}
}

// 3 — capped. The walk filtered the complete list, so it knows exactly how many
// it is not drawing, and it says so in the member that already means that.
func TestACappedWalkSaysHowManyItIsNotShowing(t *testing.T) {
	page := detailPageOf(
		api.ObjectRef{Cluster: "prod", Kind: "configmaps", Namespace: "shop", Name: "shared"},
		detail.Object{
			Kind: "configmaps", Title: "ConfigMap", Name: "shared", Namespace: "shop",
			RelationsNote: "7 more objects use this configmap and are not shown.",
			Usage:         &detail.Usage{Subject: "ConfigMap", Found: 25, Total: 32},
		},
	)
	if page.UsedBy.State != api.StateOK {
		t.Fatalf("state = %q, want ok", page.UsedBy.State)
	}
	body := wire(t, page)
	if !strings.Contains(body, "7 more objects use this configmap") {
		t.Errorf("the cap does not admit itself on the wire: %s", body)
	}
}

// And nothing of this reaches a kind the walk does not run for.
func TestAKindWithNoReverseWalkCarriesNoAnswer(t *testing.T) {
	page := detailPageOf(
		api.ObjectRef{Cluster: "prod", Kind: "pods", Namespace: "shop-prod", Name: "api-1"},
		detailFixtureObject(),
	)
	if page.UsedBy != nil {
		t.Errorf("a pod's page carries a reverse-walk answer: %+v", page.UsedBy)
	}
}

type forbiddenErr string

func (e forbiddenErr) Error() string { return string(e) }

func errForbidden(text string) error { return forbiddenErr(text) }
