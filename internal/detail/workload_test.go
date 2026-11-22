package detail

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

const podTemplate = `  template:
    spec:
      containers:
        - name: app
          image: nginx:1.27
        - name: sidecar
          image: envoy:1.30
        - name: again
          image: nginx:1.27
`

func TestAWorkloadsSummarySaysReadyImagesAndStrategy(t *testing.T) {
	cases := map[string]struct {
		kind, spec, status, ready, class, strategy string
	}{
		"a Deployment short of its replicas": {
			kind:     "Deployment",
			spec:     "  replicas: 3\n  strategy:\n    type: RollingUpdate\n    rollingUpdate:\n      maxSurge: 25%\n      maxUnavailable: 1\n",
			status:   "readyReplicas: 2",
			ready:    "2/3",
			class:    "warn",
			strategy: "RollingUpdate (max surge 25%, max unavailable 1)",
		},
		"a Deployment with no replicas field wants the default one": {
			kind:     "Deployment",
			spec:     "  strategy:\n    type: Recreate\n",
			status:   "readyReplicas: 1",
			ready:    "1/1",
			class:    "ok",
			strategy: "Recreate",
		},
		"a Deployment scaled to nothing is no success": {
			kind:     "Deployment",
			spec:     "  replicas: 0\n  strategy:\n    type: Recreate\n",
			status:   "observedGeneration: 1",
			ready:    "0/0",
			class:    "",
			strategy: "Recreate",
		},
		"a StatefulSet on a partition": {
			kind:     "StatefulSet",
			spec:     "  replicas: 2\n  updateStrategy:\n    type: RollingUpdate\n    rollingUpdate:\n      partition: 1\n",
			status:   "readyReplicas: 2",
			ready:    "2/2",
			class:    "ok",
			strategy: "RollingUpdate (partition 1)",
		},
		"a DaemonSet counts the nodes it is scheduled on": {
			kind:     "DaemonSet",
			spec:     "  updateStrategy:\n    type: RollingUpdate\n    rollingUpdate:\n      maxUnavailable: 1\n",
			status:   "numberReady: 3\n  desiredNumberScheduled: 4",
			ready:    "3/4",
			class:    "warn",
			strategy: "RollingUpdate (max unavailable 1)",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			raw := object(t, "apiVersion: apps/v1\nkind: "+c.kind+"\nmetadata:\n  name: web\n  namespace: shop\nspec:\n"+c.spec+podTemplate+"status:\n  "+c.status+"\n")
			facts := factsOf(raw, schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: c.kind})
			ready, found := factNamed(facts, "Ready")
			if !found || ready.Value != c.ready || ready.Class != c.class || !ready.Pill {
				t.Errorf("Ready = %+v (found %v); want %q in class %q, as a pill", ready, found, c.ready, c.class)
			}
			images, found := factNamed(facts, "Images")
			if !found || images.Value != "nginx:1.27, envoy:1.30" || !images.Sentence {
				t.Errorf("Images = %+v (found %v); want each image once, wrapping", images, found)
			}
			strategy, found := factNamed(facts, "Strategy")
			if !found || strategy.Value != c.strategy {
				t.Errorf("Strategy = %+v (found %v); want %q", strategy, found, c.strategy)
			}
		})
	}
}

func TestOneImageIsCopyable(t *testing.T) {
	raw := object(t, "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\nspec:\n  template:\n    spec:\n      containers:\n        - name: app\n          image: nginx:1.27\n")
	image, found := factNamed(factsOf(raw, schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}), "Image")
	if !found || image.Value != "nginx:1.27" || image.Copy != "nginx:1.27" {
		t.Errorf("Image = %+v (found %v); want it named once, copyable", image, found)
	}
}

func TestAWorkloadDrawsEveryConditionAndMarksTheFailingOne(t *testing.T) {
	raw := object(t, `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  replicas: 1
status:
  conditions:
    - type: Available
      status: "True"
      reason: MinimumReplicasAvailable
    - type: Progressing
      status: "False"
      reason: ProgressDeadlineExceeded
    - type: ReplicaFailure
      status: "True"
      reason: FailedCreate
`)
	facts := factsOf(raw, schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"})
	for label, class := range map[string]string{"Available": "", "Progressing": "warn", "ReplicaFailure": "warn"} {
		fact, found := factNamed(facts, label)
		if !found || fact.Class != class || !fact.Sentence {
			t.Errorf("%s = %+v (found %v); want class %q, wrapping", label, fact, found, class)
		}
	}
}

func TestAnotherKindGetsNoWorkloadRows(t *testing.T) {
	for _, gvk := range []schema.GroupVersionKind{
		{Group: "apps", Version: "v1", Kind: "ReplicaSet"},
		{Group: "example.com", Version: "v1", Kind: "Deployment"},
	} {
		raw := object(t, "apiVersion: "+gvk.GroupVersion().String()+"\nkind: "+gvk.Kind+"\nmetadata:\n  name: web\nspec:\n  replicas: 3\n")
		if ready, found := factNamed(factsOf(raw, gvk), "Ready"); found {
			t.Errorf("%s drew %+v", gvk, ready)
		}
	}
}
