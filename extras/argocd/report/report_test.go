package report

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The report's judgments: the order worst first,
// the zeroes in the bands, the headline that never claims both axes, the green
// pill gated on the whole page, and the sync refusal in the "since" cell.

var now = time.Date(2026, 10, 20, 18, 0, 0, 0, time.UTC)

func app(t *testing.T, namespace, name, sync, health string, extra map[string]any) json.RawMessage {
	t.Helper()
	status := map[string]any{"reconciledAt": "2026-10-20T17:58:00Z"}
	if sync != "" {
		status["sync"] = map[string]any{"status": sync, "revision": "53e28ff20cc530b9ada2173fbbd64d48338583ba"}
	}
	if health != "" {
		status["health"] = map[string]any{"status": health, "lastTransitionTime": "2026-10-16T18:00:00Z"}
	}
	obj := map[string]any{
		"metadata": map[string]any{"namespace": namespace, "name": name},
		"spec": map[string]any{
			"project": "default",
			"source":  map[string]any{"repoURL": "https://github.com/argoproj/argocd-example-apps.git", "path": name, "targetRevision": "HEAD"},
		},
		"status": status,
	}
	for k, v := range extra {
		switch k {
		case "deletionTimestamp":
			obj["metadata"].(map[string]any)[k] = v
		default:
			status[k] = v
		}
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func raw(t *testing.T, obj map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func served(plural string, items ...json.RawMessage) Kind {
	return Kind{Served: true, Plural: plural, Items: items}
}

func build(t *testing.T, apps ...json.RawMessage) View {
	t.Helper()
	projects := served("appprojects", raw(t, map[string]any{"metadata": map[string]any{"namespace": "argocd", "name": "default"}}))
	r := Build("docker-desktop", now, served("applications", apps...), served("applicationsets"), projects,
		Install{Namespaces: []string{"argocd"}, How: "Found the argocd-server Service in"})
	return ViewOf(r, now)
}

func TestTheOffendersAreWorstFirstAndTheHealthyAreNotOffenders(t *testing.T) {
	v := build(t,
		app(t, "argocd", "behind", "OutOfSync", "Healthy", nil),
		app(t, "argocd", "fine", "Synced", "Healthy", nil),
		app(t, "argocd", "broken", "Synced", "Degraded", nil),
		app(t, "argocd", "wedged", "Synced", "Healthy", map[string]any{"deletionTimestamp": "2026-10-17T18:00:00Z"}),
		app(t, "argocd", "cannot-compare", "Synced", "Healthy", map[string]any{
			"conditions": []any{map[string]any{"type": "ComparisonError", "message": "repository not found"}},
		}),
	)
	var names []string
	for _, r := range v.Offenders {
		names = append(names, r.Name)
	}
	if got := strings.Join(names, ","); got != "wedged,broken,cannot-compare,behind" {
		t.Fatalf("offenders %s, want being deleted, Degraded, an error condition, OutOfSync - and not fine", got)
	}
	if v.Offenders[0].Deleting != "being deleted, 3d" {
		t.Errorf("deleting %q, want how long it has been going", v.Offenders[0].Deleting)
	}
	if v.Summary != "4 of 5 Applications need attention (not both Healthy and Synced)" {
		t.Errorf("summary %q", v.Summary)
	}
}

func TestTheBandsKeepTheirZeroesInArgosOrder(t *testing.T) {
	v := build(t, app(t, "argocd", "fine", "Synced", "Healthy", nil))
	var sync []string
	for _, tile := range v.Sync {
		sync = append(sync, tile.Label)
	}
	if strings.Join(sync, ",") != "Synced,OutOfSync,Unknown" {
		t.Errorf("sync band %v, want Argo's three words with their zeroes", sync)
	}
	if len(v.Health) < 6 || v.Health[2].Label != "Degraded" || v.Health[2].Count != 0 {
		t.Errorf("health band %+v, want every word, Degraded at zero", v.Health)
	}
}

func TestTheGreenPillIsGatedOnTheWholePageAndTheHeadlineOnBothAxes(t *testing.T) {
	if v := build(t, app(t, "argocd", "fine", "Synced", "Healthy", nil)); !v.AllWell || v.Headline != "" {
		t.Errorf("all well %v headline %q, want the green pill and no headline", v.AllWell, v.Headline)
	}
	// All Healthy and some OutOfSync must not read as all well.
	v := build(t, app(t, "argocd", "behind", "OutOfSync", "Healthy", nil))
	if v.AllWell || v.Headline != "1 OutOfSync" || v.HeadlineKey != "outofsync" {
		t.Errorf("all well %v headline %q (%s), want 1 OutOfSync", v.AllWell, v.Headline, v.HeadlineKey)
	}
	// Being deleted outranks both axes.
	v = build(t,
		app(t, "argocd", "broken", "Synced", "Degraded", nil),
		app(t, "argocd", "wedged", "Synced", "Healthy", map[string]any{"deletionTimestamp": "2026-10-17T18:00:00Z"}),
	)
	if v.Headline != "1 Application being deleted" {
		t.Errorf("headline %q, want the deletion first", v.Headline)
	}
}

func TestSinceRefusesToInventASyncTime(t *testing.T) {
	v := build(t, app(t, "argocd", "behind", "OutOfSync", "Healthy", nil))
	lines := map[string]Since{}
	for _, s := range v.Offenders[0].Since {
		lines[s.Of] = s
	}
	if lines["health"].Value != "4d" {
		t.Errorf("health since %+v, want Argo's own transition, 4d", lines["health"])
	}
	if s := lines["sync"]; !s.Missing || s.Value != SyncSinceUnrecorded {
		t.Errorf("sync since %+v, want the refusal, never a figure", s)
	}
	if lines["as of"].Value != "2m ago" {
		t.Errorf("as of %+v, want how old the reading is", lines["as of"])
	}
}

func TestAProjectIsOpenedWhereItWasFoundAndAMissingOneIsSaid(t *testing.T) {
	v := build(t,
		app(t, "argocd", "behind", "OutOfSync", "Healthy", nil),
		raw(t, map[string]any{
			"metadata": map[string]any{"namespace": "argocd", "name": "orphan"},
			"spec":     map[string]any{"project": "nowhere"},
			"status":   map[string]any{"sync": map[string]any{"status": "OutOfSync"}, "health": map[string]any{"status": "Healthy"}},
		}),
	)
	rows := map[string]Row{}
	for _, r := range v.Offenders {
		rows[r.Name] = r
	}
	if rows["behind"].ProjectNamespace != "argocd" {
		t.Errorf("project namespace %q, want where the AppProject was found", rows["behind"].ProjectNamespace)
	}
	if rows["orphan"].ProjectMessage != "no AppProject of this name exists in this cluster" {
		t.Errorf("project message %q", rows["orphan"].ProjectMessage)
	}
}

func TestAFailingApplicationSetIsListedAndAHealthyOneIsNot(t *testing.T) {
	sets := served("applicationsets",
		raw(t, map[string]any{
			"metadata": map[string]any{"namespace": "argocd", "name": "broken-generator"},
			"status":   map[string]any{"conditions": []any{map[string]any{"type": "ErrorOccurred", "status": "True", "message": "git generator failed"}}},
		}),
		raw(t, map[string]any{
			"metadata": map[string]any{"namespace": "argocd", "name": "fine"},
			"status":   map[string]any{"conditions": []any{map[string]any{"type": "ErrorOccurred", "status": "False"}}},
		}),
	)
	r := Build("c", now, served("applications"), sets, served("appprojects"), Install{})
	v := ViewOf(r, now)
	if v.SetTotal != 2 || v.SetFailing != 1 || len(v.Sets) != 1 || v.Sets[0].Name != "broken-generator" {
		t.Errorf("sets %+v total %d failing %d", v.Sets, v.SetTotal, v.SetFailing)
	}
}

func TestNoArgoKindIsAbsentAndOffersTheInstall(t *testing.T) {
	r := Build("c", now, Kind{Plural: "applications"}, Kind{}, Kind{}, Install{})
	v := ViewOf(r, now)
	if v.State != "absent" || !strings.Contains(v.Install, "helm install argocd") {
		t.Errorf("state %q install %q", v.State, v.Install)
	}
}

func TestARefusedKindIsSaidAndNeverDrawnAsEmpty(t *testing.T) {
	apps := served("applications", app(t, "argocd", "fine", "Synced", "Healthy", nil))
	sets := Kind{Served: true, Plural: "applicationsets", Unavailable: "applicationsets.argoproj.io is forbidden"}
	v := ViewOf(Build("c", now, apps, sets, served("appprojects"), Install{}), now)
	if v.SetsUnavailable == "" {
		t.Errorf("sets unavailable %q, want the refusal said", v.SetsUnavailable)
	}
	for _, a := range v.Areas {
		if a.Name == "ApplicationSets" && (a.Unavailable == "" || a.Summary != "") {
			t.Errorf("area %+v, want the reason and no figure", a)
		}
	}
}
