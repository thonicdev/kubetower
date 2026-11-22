package detail

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

const controlPlaneNodeDoc = `
apiVersion: v1
kind: Node
metadata:
  name: ip-10-0-3-17.eu-west-3.compute.internal
  labels:
    node-role.kubernetes.io/control-plane: ""
    node.kubernetes.io/instance-type: m6i.xlarge
    topology.kubernetes.io/zone: eu-west-3a
    topology.kubernetes.io/region: eu-west-3
spec:
  podCIDR: 10.244.1.0/24
  providerID: aws:///eu-west-3a/i-0abc123
  taints:
    - key: node-role.kubernetes.io/control-plane
      effect: NoSchedule
status:
  capacity:
    cpu: "4"
    memory: 16374624Ki
    pods: "110"
  allocatable:
    cpu: 3920m
    memory: 15234848Ki
    pods: "110"
  addresses:
    - type: InternalIP
      address: 10.0.3.17
    - type: Hostname
      address: ip-10-0-3-17.eu-west-3.compute.internal
  nodeInfo:
    kubeletVersion: v1.36.1
    containerRuntimeVersion: containerd://2.1.4
    osImage: Amazon Linux 2023
    kernelVersion: 6.1.115-126.197.amzn2023.x86_64
    architecture: amd64
`

const sickNodeDoc = `
apiVersion: v1
kind: Node
metadata:
  name: worker-2
spec:
  unschedulable: true
  taints:
    - key: node.kubernetes.io/memory-pressure
      effect: NoSchedule
    - key: dedicated
      value: gpu
      effect: NoExecute
status: {}
`

const plainNodeDoc = `
apiVersion: v1
kind: Node
metadata:
  name: worker-1
spec: {}
status:
  capacity:
    cpu: "2"
  allocatable:
    cpu: "2"
`

func nodeFrom(t *testing.T, doc string) *unstructured.Unstructured {
	t.Helper()
	var object map[string]any
	if err := yaml.Unmarshal([]byte(doc), &object); err != nil {
		t.Fatalf("the fixture is not valid YAML: %v", err)
	}
	return &unstructured.Unstructured{Object: object}
}

// factValue returns the first value carried under a label, and whether a row
// with that label exists at all. The two are different answers and the tests
// below depend on telling them apart.
func factValue(facts []Fact, label string) (string, bool) {
	for _, f := range facts {
		if f.Label == label {
			return f.Value, true
		}
	}
	return "", false
}

// A taint an administrator set and a taint the node controller set look
// identical in the manifest and mean opposite things. Grading them the same
// would either cry wolf on every control-plane node in the estate, or say
// nothing when a node is under memory pressure.
func TestATaintOfPolicyIsNotGradedLikeATaintOfFailure(t *testing.T) {
	policy := taintClass("node-role.kubernetes.io/control-plane", "NoSchedule")
	if policy == "bad" {
		t.Errorf("a control-plane node is supposed to carry its own taint; grading it %q "+
			"puts a warning on every control-plane node in the estate, and an operator who "+
			"sees that every day stops reading the color at all", policy)
	}

	failure := taintClass("node.kubernetes.io/memory-pressure", "NoSchedule")
	if failure != "bad" {
		t.Errorf("a taint the node controller added because the node is under memory pressure "+
			"is a symptom, and was graded %q", failure)
	}

	evicting := taintClass("dedicated", "NoExecute")
	if evicting != "warn" {
		t.Errorf("NoExecute evicts pods that are already running, which is a stronger statement "+
			"than refusing new ones; it was graded %q", evicting)
	}

	// The cordon's own taint is in the controller's namespace too, and it is a
	// decision, not a symptom: the Scheduling row already says "cordoned" in
	// amber, and a red pill beside it would call a healthy node broken.
	cordon := taintClass("node.kubernetes.io/unschedulable", "NoSchedule")
	if cordon != "warn" {
		t.Errorf("the taint every cordoned node carries is graded %q; it should read as the "+
			"cordon does, a warning", cordon)
	}
}

// The absence of taints is stated, not implied by an absent row. An operator
// looking for a taint cannot tell "there are none" from "this panel does not
// report them" if the answer is silence.
func TestANodeWithNoTaintsSaysSoRatherThanSayingNothing(t *testing.T) {
	facts := nodeFacts(nodeFrom(t, plainNodeDoc))

	value, found := factValue(facts, "Taints")
	if !found {
		t.Fatalf("a node with no taints produced no taint row at all, so the panel is silent "+
			"where the operator asked a question. Rows: %v", labelsOf(facts))
	}
	if value != "none" {
		t.Errorf("Taints reads %q, want %q", value, "none")
	}
}

// A taint is rendered the way it would be typed, because the operator reading it
// is usually about to undo it.
func TestATaintIsRenderedAsKubectlWritesIt(t *testing.T) {
	facts := nodeFacts(nodeFrom(t, sickNodeDoc))

	var taints []string
	for _, f := range facts {
		if f.Label == "Taint" {
			taints = append(taints, f.Value)
		}
	}
	if len(taints) != 2 {
		t.Fatalf("want 2 taint rows, got %d: %v", len(taints), taints)
	}
	if taints[0] != "node.kubernetes.io/memory-pressure:NoSchedule" {
		t.Errorf("a taint with no value must read `key:Effect` and not `key=:Effect`; got %q", taints[0])
	}
	if taints[1] != "dedicated=gpu:NoExecute" {
		t.Errorf("a taint with a value must read `key=value:Effect`; got %q", taints[1])
	}
}

// Cordoned is one boolean in `spec` that no condition reflects, and it is the
// other reason a node that looks healthy is running nothing.
func TestACordonedNodeSaysItIsCordoned(t *testing.T) {
	facts := nodeFacts(nodeFrom(t, sickNodeDoc))

	value, found := factValue(facts, "Scheduling")
	if !found {
		t.Fatalf("`spec.unschedulable: true` produced no row; the node looks healthy and takes "+
			"no work, and the panel does not say why. Rows: %v", labelsOf(facts))
	}
	if !strings.Contains(value, "cordoned") {
		t.Errorf("Scheduling reads %q, which does not contain the word an operator would search for", value)
	}
}

// A role is the presence of a label key whose value is empty. Reading the value
// instead of the key returns nothing and looks exactly like a node with no role.
func TestARoleIsReadFromTheLabelKeyAndNotItsEmptyValue(t *testing.T) {
	facts := nodeFacts(nodeFrom(t, controlPlaneNodeDoc))

	value, found := factValue(facts, "Roles")
	if !found {
		t.Fatalf("a control-plane node reported no role. Its role is the *presence* of "+
			"`node-role.kubernetes.io/control-plane`, whose value is the empty string. Rows: %v",
			labelsOf(facts))
	}
	if value != "control-plane" {
		t.Errorf("Roles reads %q, want %q", value, "control-plane")
	}
}

// The gap between capacity and allocatable is the reserved slice that explains a
// scheduling failure on a node that looks half empty. Reporting only one of the
// two hides it.
func TestCapacityAndAllocatableAreReportedTogetherOnlyWhenTheyDiffer(t *testing.T) {
	facts := nodeFacts(nodeFrom(t, controlPlaneNodeDoc))

	cpu, found := factValue(facts, "CPU")
	if !found {
		t.Fatalf("no CPU row. Rows: %v", labelsOf(facts))
	}
	if !strings.Contains(cpu, "3920m") || !strings.Contains(cpu, "4") {
		t.Errorf("CPU reads %q; it must carry both allocatable and capacity, since the gap "+
			"between them is what explains a scheduling failure on a node that looks half empty", cpu)
	}

	// And when they are equal, saying it twice is noise.
	plain := nodeFacts(nodeFrom(t, plainNodeDoc))
	if cpu, _ := factValue(plain, "CPU"); cpu != "2" {
		t.Errorf("when allocatable equals capacity the row must not say it twice; got %q", cpu)
	}
}

// Not `15234848Ki allocatable of 16374624Ki` beside a Nodes list in GiB: both
// figures are in the list's unit.
func TestNodeMemoryIsInTheListsUnit(t *testing.T) {
	memory, found := factValue(nodeFacts(nodeFrom(t, controlPlaneNodeDoc)), "Memory")
	if !found {
		t.Fatal("no Memory row")
	}
	if memory != "14.5 GiB allocatable of 15.6 GiB" {
		t.Errorf("Memory reads %q, want %q", memory, "14.5 GiB allocatable of 15.6 GiB")
	}
	if got := memoryFigure("not-a-quantity"); got != "not-a-quantity" {
		t.Errorf("an unparsable quantity reads %q; it must be printed as written", got)
	}
}

// 16200000Ki and 16097600Ki both round to `15.4 GiB`, and comparing the
// rounded figures would draw one of them alone - the 100 MiB the kubelet
// reserves would vanish from the row that exists to show it.
// And two spellings of one quantity are still one figure.
func TestNodeMemoryThatRoundsAlikeStillShowsTheReservedSlice(t *testing.T) {
	node := func(capacity, allocatable string) string {
		facts := nodeFacts(&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "Node",
			"metadata": map[string]any{"name": "worker-1"},
			"status": map[string]any{
				"capacity":    map[string]any{"memory": capacity},
				"allocatable": map[string]any{"memory": allocatable},
			},
		}})
		memory, found := factValue(facts, "Memory")
		if !found {
			t.Fatal("no Memory row")
		}
		return memory
	}
	if got, want := node("16200000Ki", "16097600Ki"), "15.4 GiB allocatable of 15.4 GiB (100 MiB reserved)"; got != want {
		t.Errorf("Memory reads %q, want %q", got, want)
	}
	if got, want := node("1Gi", "1024Mi"), "1.0 GiB"; got != want {
		t.Errorf("one quantity spelled two ways reads %q, want %q", got, want)
	}
}

// The hostname is dropped when it repeats the name, and kept when it does not.
func TestAHostnameThatRepeatsTheNameIsNotShownTwice(t *testing.T) {
	facts := nodeFacts(nodeFrom(t, controlPlaneNodeDoc))
	if value, found := factValue(facts, "Hostname"); found {
		t.Errorf("the hostname %q is the node name; repeating it fills a row with nothing", value)
	}
	if ip, found := factValue(facts, "Internal IP"); !found || ip != "10.0.3.17" {
		t.Errorf("Internal IP reads %q (found=%v), want 10.0.3.17", ip, found)
	}
}

// A Node reaching the generic panel carries its taints. This is the assertion
// that fails if the wiring in factsOf is removed while every function in this
// file still passes its own test.
func TestANodeReachingTheGenericPanelCarriesItsTaints(t *testing.T) {
	gvk := schema.GroupVersionKind{Version: "v1", Kind: "Node"}
	facts := factsOf(nodeFrom(t, controlPlaneNodeDoc), gvk)

	for _, f := range facts {
		if f.Label == "Taint" || f.Label == "Taints" {
			return
		}
	}
	t.Fatalf("a Node went through the generic fact list and came out with no taint row, so the "+
		"node facts are built and never wired in. Rows: %v", labelsOf(facts))
}

// A ConfigMap must not sprout node rows.
func TestNodeFactsAreNotAddedToEverythingElse(t *testing.T) {
	gvk := schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}
	facts := factsOf(nodeFrom(t, configMapDoc), gvk)

	if _, found := factValue(facts, "Taints"); found {
		t.Errorf("a ConfigMap reported a taint row. Rows: %v", labelsOf(facts))
	}
}

func labelsOf(facts []Fact) []string {
	out := make([]string, 0, len(facts))
	for _, f := range facts {
		out = append(out, f.Label)
	}
	return out
}

// The older label form, which carries the role in the *value* rather than in the
// key. A node labeled this way must still report its role rather than none,
// with nothing failing anywhere if it did not.
func TestTheOlderRoleLabelIsStillRead(t *testing.T) {
	doc := `
apiVersion: v1
kind: Node
metadata:
  name: worker-9
  labels:
    kubernetes.io/role: worker
spec: {}
status: {}
`
	facts := nodeFacts(nodeFrom(t, doc))

	value, found := factValue(facts, "Roles")
	if !found {
		t.Fatalf("a node labeled `kubernetes.io/role: worker` reported no role. That spelling "+
			"puts the answer in the value, not the key, and several managed providers still "+
			"use it. Rows: %v", labelsOf(facts))
	}
	if value != "worker" {
		t.Errorf("Roles reads %q, want %q", value, "worker")
	}
}
