package web

import (
	"slices"
	"testing"

	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/detail"
)

// A toggle offers only the half that would change the object: Resume on a
// suspended CronJob, Suspend on one that is not, and the same for a node's
// cordon. Every surface reads `objectActions` (drawer, page, row menu), so this
// is where it is held.
func TestObjectActionsOfferTheToggleHalfThatActs(t *testing.T) {
	cases := []struct {
		kind     string
		switches map[string]string
		want     string
		withheld string
	}{
		{"cronjobs", map[string]string{"spec.suspend": "true"}, "resume", "suspend"},
		{"cronjobs", map[string]string{"spec.suspend": "false"}, "suspend", "resume"},
		{"nodes", map[string]string{"spec.unschedulable": "true"}, "uncordon", "cordon"},
		{"nodes", map[string]string{"spec.unschedulable": "false"}, "cordon", "uncordon"},
	}
	for _, c := range cases {
		got := verbNames(objectActions(c.kind, detail.Object{Kind: c.kind, Switches: c.switches}, true, ""))
		if !slices.Contains(got, c.want) || slices.Contains(got, c.withheld) {
			t.Errorf("%s %v offers %v, want %s and not %s", c.kind, c.switches, got, c.want, c.withheld)
		}
	}
}

// A state the read did not carry withholds nothing: both halves stay, as
// before, and the confirmation says when an act changes nothing.
func TestObjectActionsKeepBothHalvesWhenTheStateIsUnknown(t *testing.T) {
	got := verbNames(objectActions("cronjobs", detail.Object{Kind: "cronjobs"}, true, ""))
	if !slices.Contains(got, "suspend") || !slices.Contains(got, "resume") {
		t.Errorf("a CronJob of unknown state offers %v, want both suspend and resume", got)
	}
	// The other verbs are untouched: Run now on a suspended CronJob, Drain on a
	// cordoned node.
	got = verbNames(objectActions("cronjobs", detail.Object{Kind: "cronjobs", Switches: map[string]string{"spec.suspend": "true"}}, true, ""))
	if !slices.Equal(got, []string{"trigger", "resume", "delete"}) {
		t.Errorf("a suspended CronJob offers %v, want [trigger resume delete]", got)
	}
	got = verbNames(objectActions("nodes", detail.Object{Kind: "nodes", Switches: map[string]string{"spec.unschedulable": "true"}}, true, ""))
	if !slices.Equal(got, []string{"uncordon", "drain", "delete"}) {
		t.Errorf("a cordoned node offers %v, want [uncordon drain delete]", got)
	}
}

// What a list row says it would not change, for the selection bar.
func TestUnchangedVerbsPerRow(t *testing.T) {
	cases := []struct {
		kind     string
		switches map[string]string
		want     []string
	}{
		{"cronjobs", map[string]string{"spec.suspend": "true"}, []string{"suspend"}},
		{"cronjobs", map[string]string{"spec.suspend": "false"}, []string{"resume"}},
		{"nodes", map[string]string{"spec.unschedulable": "true"}, []string{"cordon"}},
		{"nodes", map[string]string{"spec.unschedulable": "false"}, []string{"uncordon"}},
		{"cronjobs", nil, nil},
		{"pods", nil, nil},
	}
	for _, c := range cases {
		if got := unchangedVerbs(c.kind, c.switches); !slices.Equal(got, c.want) {
			t.Errorf("unchangedVerbs(%s, %v) = %v, want %v", c.kind, c.switches, got, c.want)
		}
	}
}

// The paths internal/detail and internal/resources fill are the ones the
// verbs write: a path that drifted from `actions.FieldFor` would settle nothing
// and quietly offer both halves again.
func TestSwitchPathsAreTheVerbsFields(t *testing.T) {
	for verb, path := range map[actions.Verb]string{
		actions.Suspend: "spec.suspend", actions.Resume: "spec.suspend",
		actions.Cordon: "spec.unschedulable", actions.Uncordon: "spec.unschedulable",
	} {
		field, _, ok := actions.FieldFor(verb)
		if !ok || field != path {
			t.Errorf("FieldFor(%s) = %q, %v; the switches are keyed %q", verb, field, ok, path)
		}
	}
}
