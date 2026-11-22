package rightsizing

import (
	"fmt"
	"sort"
	"strings"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Workloads something else already sizes.
//
// A HorizontalPodAutoscaler on CPU or memory utilization keeps usage near a
// share of the request: at a 50 % target every pod runs at half its request on
// purpose, and lowering the request only adds replicas. Such a workload is not
// over-provisioned.
//
// A VerticalPodAutoscaler in any mode but Off rewrites the requests of every
// pod it admits, so a `kubectl set resources` is overwritten by its next pod.

// Autoscaler is an HPA or a VPA aimed at a workload.
type Autoscaler struct {
	// Kind is "HorizontalPodAutoscaler" or "VerticalPodAutoscaler".
	Kind string
	Name string
	// Utilization is, for an HPA, each resource it scales on as a share of
	// the request ("cpu" → 70). Absent for a metric of another type: an
	// AverageValue target is not a share of the request, and does not move
	// when the request does.
	Utilization map[string]int32
	// Mode is a VPA's update mode. "Off" only recommends, and changes nothing.
	Mode string
}

// vpaManages says whether this VPA rewrites requests.
func (a Autoscaler) vpaManages() bool {
	return a.Kind == "VerticalPodAutoscaler" && a.Mode != "Off"
}

// AutoscalerKey is the index's shape: the workload the autoscaler targets.
func AutoscalerKey(namespace, kind, name string) string {
	return namespace + "/" + kind + "/" + name
}

// Autoscalers indexes HPAs and VPAs by the workload each targets.
//
// VPAs arrive unstructured: they are a custom resource the cluster may not
// have, and no Go type for them is in this module.
func Autoscalers(hpas []autoscalingv2.HorizontalPodAutoscaler, vpas []unstructured.Unstructured) map[string][]Autoscaler {
	out := map[string][]Autoscaler{}
	for i := range hpas {
		h := &hpas[i]
		a := Autoscaler{Kind: "HorizontalPodAutoscaler", Name: h.Name, Utilization: map[string]int32{}}
		for _, m := range h.Spec.Metrics {
			var name corev1.ResourceName
			var target autoscalingv2.MetricTarget
			switch {
			case m.Type == autoscalingv2.ResourceMetricSourceType && m.Resource != nil:
				name, target = m.Resource.Name, m.Resource.Target
			case m.Type == autoscalingv2.ContainerResourceMetricSourceType && m.ContainerResource != nil:
				name, target = m.ContainerResource.Name, m.ContainerResource.Target
			default:
				continue
			}
			if target.Type == autoscalingv2.UtilizationMetricType && target.AverageUtilization != nil {
				a.Utilization[string(name)] = *target.AverageUtilization
			}
		}
		key := AutoscalerKey(h.Namespace, h.Spec.ScaleTargetRef.Kind, h.Spec.ScaleTargetRef.Name)
		out[key] = append(out[key], a)
	}
	for i := range vpas {
		v := &vpas[i]
		kind, _, _ := unstructured.NestedString(v.Object, "spec", "targetRef", "kind")
		name, _, _ := unstructured.NestedString(v.Object, "spec", "targetRef", "name")
		if kind == "" || name == "" {
			continue
		}
		mode, _, _ := unstructured.NestedString(v.Object, "spec", "updatePolicy", "updateMode")
		if mode == "" {
			// The VPA's own default, and not "Off": a VPA with no update
			// policy applies its recommendations.
			mode = "Auto"
		}
		key := AutoscalerKey(v.GetNamespace(), kind, name)
		out[key] = append(out[key], Autoscaler{Kind: "VerticalPodAutoscaler", Name: v.GetName(), Mode: mode})
	}
	return out
}

// hpaOn is the HPA scaling this workload on this resource's utilization, and
// its target.
func hpaOn(autoscalers []Autoscaler, resource string) (Autoscaler, int32, bool) {
	for _, a := range autoscalers {
		if a.Kind != "HorizontalPodAutoscaler" {
			continue
		}
		if target, ok := a.Utilization[resource]; ok {
			return a, target, true
		}
	}
	return Autoscaler{}, 0, false
}

// vpaOver is the VPA that rewrites this workload's requests, when there is one.
func vpaOver(autoscalers []Autoscaler) (Autoscaler, bool) {
	for _, a := range autoscalers {
		if a.vpaManages() {
			return a, true
		}
	}
	return Autoscaler{}, false
}

// AutoscalerNotes are the sentences a workload's card carries about what else
// sizes it, and what that changes on the page. Sorted, so two reads draw the
// same card.
func AutoscalerNotes(autoscalers []Autoscaler) []string {
	var out []string
	for _, a := range autoscalers {
		switch {
		case a.Kind == "VerticalPodAutoscaler" && a.vpaManages():
			out = append(out, fmt.Sprintf("VPA %s (%s) sets this workload's requests, so no CPU or memory change is suggested here: change the VPA's policy instead", a.Name, a.Mode))
		case a.Kind == "VerticalPodAutoscaler":
			out = append(out, fmt.Sprintf("VPA %s only recommends (Off); its recommendation is a second opinion on the figures here", a.Name))
		case len(a.Utilization) > 0:
			var parts []string
			for _, r := range []struct{ key, label string }{{"cpu", "CPU"}, {"memory", "memory"}, {"ephemeral-storage", "ephemeral storage"}} {
				if t, ok := a.Utilization[r.key]; ok {
					parts = append(parts, fmt.Sprintf("%s at %d%% of the request", r.label, t))
				}
			}
			out = append(out, fmt.Sprintf("HPA %s scales this workload on %s: usage below the request is that target at work, so no request reduction is suggested for what it scales on", a.Name, strings.Join(parts, " and ")))
		default:
			out = append(out, fmt.Sprintf("HPA %s scales this workload on a metric that is not a share of the request, so the requests here can be sized as usual", a.Name))
		}
	}
	sort.Strings(out)
	return out
}
