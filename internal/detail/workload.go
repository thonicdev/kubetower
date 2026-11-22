package detail

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/thonicdev/kubetower/internal/resources"
)

// workloadFacts is what a Deployment, a StatefulSet and a DaemonSet add to the
// Summary: ready over wanted replicas, the images, and the rollout strategy.
//
// False for any other kind, and for an object that does not convert to its
// type: the generic reading then stands.
func workloadFacts(raw *unstructured.Unstructured, gvk schema.GroupVersionKind) ([]Fact, bool) {
	if gvk.Group != "apps" {
		return nil, false
	}
	switch gvk.Kind {
	case "Deployment":
		var d appsv1.Deployment
		if !converted(raw, &d) {
			return nil, false
		}
		wanted := int32(1) // what the API server defaults an absent spec.replicas to
		if d.Spec.Replicas != nil {
			wanted = *d.Spec.Replicas
		}
		return workloadRows(d.Status.ReadyReplicas, wanted, d.Spec.Template.Spec, deploymentStrategy(d.Spec.Strategy)), true
	case "StatefulSet":
		var s appsv1.StatefulSet
		if !converted(raw, &s) {
			return nil, false
		}
		wanted := int32(1)
		if s.Spec.Replicas != nil {
			wanted = *s.Spec.Replicas
		}
		return workloadRows(s.Status.ReadyReplicas, wanted, s.Spec.Template.Spec, statefulSetStrategy(s.Spec.UpdateStrategy)), true
	case "DaemonSet":
		var d appsv1.DaemonSet
		if !converted(raw, &d) {
			return nil, false
		}
		return workloadRows(d.Status.NumberReady, d.Status.DesiredNumberScheduled, d.Spec.Template.Spec, daemonSetStrategy(d.Spec.UpdateStrategy)), true
	}
	return nil, false
}

func converted(raw *unstructured.Unstructured, into any) bool {
	return runtime.DefaultUnstructuredConverter.FromUnstructured(raw.Object, into) == nil
}

func workloadRows(ready, wanted int32, pod corev1.PodSpec, strategy string) []Fact {
	class := resources.ClassOK
	switch {
	case ready < wanted:
		class = resources.ClassWarn
	case wanted == 0:
		// Scaled to nothing is a state, not a success.
		class = resources.ClassNone
	}
	return []Fact{
		{Label: "Ready", Value: fmt.Sprintf("%d/%d", ready, wanted), Class: class, Pill: true},
		imagesFact(pod),
		{Label: "Strategy", Value: strategy},
	}
}

// imagesFact is the images the pod template runs, each once. A single image is
// copyable; several are a wrapping list with no one thing to copy.
func imagesFact(pod corev1.PodSpec) Fact {
	seen := map[string]bool{}
	var images []string
	for _, container := range pod.Containers {
		if container.Image != "" && !seen[container.Image] {
			seen[container.Image] = true
			images = append(images, container.Image)
		}
	}
	if len(images) == 1 {
		return Fact{Label: "Image", Value: images[0], Copy: images[0]}
	}
	return Fact{Label: "Images", Value: strings.Join(images, ", "), Sentence: true}
}

func deploymentStrategy(strategy appsv1.DeploymentStrategy) string {
	if rolling := strategy.RollingUpdate; rolling != nil {
		return fmt.Sprintf("%s (max surge %s, max unavailable %s)",
			strategy.Type, intOrPercent(rolling.MaxSurge), intOrPercent(rolling.MaxUnavailable))
	}
	return string(strategy.Type)
}

func statefulSetStrategy(strategy appsv1.StatefulSetUpdateStrategy) string {
	if rolling := strategy.RollingUpdate; rolling != nil && rolling.Partition != nil && *rolling.Partition > 0 {
		return string(strategy.Type) + " (partition " + strconv.Itoa(int(*rolling.Partition)) + ")"
	}
	return string(strategy.Type)
}

func daemonSetStrategy(strategy appsv1.DaemonSetUpdateStrategy) string {
	if rolling := strategy.RollingUpdate; rolling != nil {
		return fmt.Sprintf("%s (max unavailable %s)", strategy.Type, intOrPercent(rolling.MaxUnavailable))
	}
	return string(strategy.Type)
}

func intOrPercent(value *intstr.IntOrString) string {
	if value == nil {
		return "—"
	}
	return value.String()
}

// workloadConditionFacts draws every condition of a workload, not only the
// failing ones: `Available` and `Progressing` say a rollout is done, and there
// are at most three. A failing one is marked as conditionsOf marks it.
func workloadConditionFacts(raw *unstructured.Unstructured, now time.Time) []Fact {
	conditions := conditionsIn(raw)
	out := make([]Fact, 0, len(conditions))
	for _, c := range conditions {
		class := resources.ClassNone
		if conditionFailing(c) {
			class = resources.ClassWarn
		}
		out = append(out, Fact{Label: c.kind, Value: conditionText(c, now), Class: class, Sentence: true})
	}
	return out
}
