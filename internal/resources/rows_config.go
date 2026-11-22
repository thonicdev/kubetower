package resources

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	policyv1 "k8s.io/api/policy/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// --- configuration -----------------------------------------------------------------

func configMapRow(c *corev1.ConfigMap, now time.Time) []Cell {
	// Names, not values: values can be huge, or hold a misplaced password.
	keys := len(c.Data) + len(c.BinaryData)
	names := append(sortedKeys(c.Data), sortedKeys(c.BinaryData)...)
	sort.Strings(names)

	return []Cell{
		text(c.Name),
		text(c.Namespace),
		count(keys),
		text(JoinCapped(names, 6)),
		age(c.CreationTimestamp, now),
	}
}

// secretRow shows what keys a Secret holds and never what is in them: a hard
// rule, since screens are shared and recorded. A value is revealed one at a
// time, deliberately, elsewhere. The base64 in `data` is not encryption.
func secretRow(s *corev1.Secret, now time.Time) []Cell {
	return []Cell{
		text(s.Name),
		text(s.Namespace),
		text(string(s.Type)),
		count(len(s.Data)),
		text(keyNames(s.Data, 6)),
		age(s.CreationTimestamp, now),
	}
}

// resourceQuotaRow is used against hard, fullest resource first, and the row
// sorts and colors on the highest: at 100% every further create in the
// namespace fails.
func resourceQuotaRow(q *corev1.ResourceQuota, now time.Time) []Cell {
	uses := quotaUsage(q)

	entries := make([]string, 0, len(uses))
	for _, u := range uses {
		entries = append(entries, u.text)
	}

	highest := Cell{Text: Missing}
	if len(uses) > 0 {
		percent := uses[0].fraction * 100
		highest = Cell{Text: fmt.Sprintf("%.0f%%", percent), Value: percent}
		switch {
		case percent >= 100:
			highest.Class, highest.Rank = ClassBad, RankBad
		case percent >= 90:
			highest.Class, highest.Rank = ClassWarn, RankWarn
		default:
			highest.Class, highest.Rank = ClassOK, RankOK
		}
	}

	return []Cell{
		text(q.Name),
		text(q.Namespace),
		count(len(uses)),
		text(JoinCapped(entries, 3)),
		highest,
		age(q.CreationTimestamp, now),
	}
}

// QuotaFigures is a quota's used against hard, one `pods 12/20` per resource,
// fullest first: the Used of hard column's entries, uncapped. Exported so the
// Namespace page reads a quota as its list does.
func QuotaFigures(q *corev1.ResourceQuota) []string {
	uses := quotaUsage(q)
	out := make([]string, 0, len(uses))
	for _, u := range uses {
		out = append(out, u.text)
	}
	return out
}

// LimitRangeFigures is a LimitRange's constraints, one
// `Container/cpu min=50m max=2 def=200m` per type and resource: the Constraints
// column's entries, uncapped. Exported for the same reason as QuotaFigures.
func LimitRangeFigures(l *corev1.LimitRange) []string {
	_, entries := limitRangeConstraints(l)
	return entries
}

// quotaUse is one line of a quota: `pods 12/20`, and how full that is.
type quotaUse struct {
	text     string
	fraction float64
}

// quotaUsage reads status, falling back to spec for a quota the controller has
// not accounted for yet (an empty status does not mean no constraint).
//
// The fraction uses AsApproximateFloat64, not Value(), which rounds `3500m`
// up to 4.
func quotaUsage(q *corev1.ResourceQuota) []quotaUse {
	hard := q.Status.Hard
	if len(hard) == 0 {
		hard = q.Spec.Hard
	}

	names := make([]string, 0, len(hard))
	for name := range hard {
		names = append(names, string(name))
	}
	sort.Strings(names)

	out := make([]quotaUse, 0, len(hard))
	for _, name := range names {
		limit := hard[corev1.ResourceName(name)]
		used, measured := q.Status.Used[corev1.ResourceName(name)]

		usedText := "?"
		fraction := 0.0
		if measured {
			usedText = used.String()
			if size := limit.AsApproximateFloat64(); size > 0 {
				fraction = used.AsApproximateFloat64() / size
			}
		}
		out = append(out, quotaUse{
			text:     fmt.Sprintf("%s %s/%s", name, usedText, limit.String()),
			fraction: fraction,
		})
	}

	// Fullest first, so a capped list shows what is about to block.
	sort.SliceStable(out, func(i, j int) bool { return out[i].fraction > out[j].fraction })
	return out
}

// limitRangeRow answers "where did this request I did not write come from": a
// LimitRange is invisible in the pods it fills in, so its constraints are the
// row.
func limitRangeRow(l *corev1.LimitRange, now time.Time) []Cell {
	types, entries := limitRangeConstraints(l)
	return []Cell{
		text(l.Name),
		text(l.Namespace),
		text(types),
		count(len(entries)),
		text(JoinCapped(entries, 3)),
		age(l.CreationTimestamp, now),
	}
}

// limitRangeConstraints flattens the item × resource matrix into one line each:
// `Container/cpu min=50m max=2 def=200m`.
//
// The bounds first, then what gets filled in when the manifest is silent.
func limitRangeConstraints(l *corev1.LimitRange) (string, []string) {
	var types []string
	var entries []string
	for _, item := range l.Spec.Limits {
		kind := string(item.Type)
		if kind == "" {
			// The API defaults this to Container; do not guess for an old object.
			kind = "?"
		}
		if !slices.Contains(types, kind) {
			types = append(types, kind)
		}

		for _, name := range limitedResources(item) {
			resource := corev1.ResourceName(name)
			var parts []string
			if v, ok := item.Min[resource]; ok {
				parts = append(parts, "min="+v.String())
			}
			if v, ok := item.Max[resource]; ok {
				parts = append(parts, "max="+v.String())
			}
			if v, ok := item.DefaultRequest[resource]; ok {
				parts = append(parts, "req="+v.String())
			}
			if v, ok := item.Default[resource]; ok {
				parts = append(parts, "def="+v.String())
			}
			if v, ok := item.MaxLimitRequestRatio[resource]; ok {
				parts = append(parts, "ratio="+v.String())
			}
			entries = append(entries, strings.TrimSpace(
				fmt.Sprintf("%s/%s %s", kind, name, strings.Join(parts, " "))))
		}
	}
	return strings.Join(types, ", "), entries
}

// limitedResources is every resource this item says anything about, sorted:
// the five maps overlap and none is complete on its own.
func limitedResources(item corev1.LimitRangeItem) []string {
	var names []string
	for _, list := range []corev1.ResourceList{
		item.Min, item.Max, item.Default, item.DefaultRequest, item.MaxLimitRequestRatio,
	} {
		for name := range list {
			if !slices.Contains(names, string(name)) {
				names = append(names, string(name))
			}
		}
	}
	sort.Strings(names)
	return names
}

// hpaRow answers "why is it not scaling": it cannot read its metric, it is at a
// bound, or the metric says it should not. So the metric and its value sit
// beside the replica counts, and Status carries the failed condition.
func hpaRow(h *autoscalingv2.HorizontalPodAutoscaler, now time.Time) []Cell {
	minimum := Cell{Text: Missing}
	if h.Spec.MinReplicas != nil {
		minimum = count32(*h.Spec.MinReplicas)
	}

	status, class, rank := hpaStatus(h)
	return []Cell{
		text(h.Name),
		text(h.Namespace),
		text(h.Spec.ScaleTargetRef.Kind + "/" + h.Spec.ScaleTargetRef.Name),
		// Current against desired: a gap means the workload has not caught up.
		ratio(h.Status.CurrentReplicas, h.Status.DesiredReplicas),
		minimum,
		count32(h.Spec.MaxReplicas),
		text(hpaMetrics(h)),
		graded(status, class, rank),
		age(h.CreationTimestamp, now),
	}
}

// hpaMetrics prints each metric as current-against-target: `cpu 43%/70%`.
// Spec and status are unkeyed lists, matched on the metric's identity: the
// resource name, plus the container for a container metric.
func hpaMetrics(h *autoscalingv2.HorizontalPodAutoscaler) string {
	current := make(map[string]string, len(h.Status.CurrentMetrics))
	for _, m := range h.Status.CurrentMetrics {
		if name := currentMetricName(m); name != "" {
			current[name] = currentMetricValue(m)
		}
	}

	entries := make([]string, 0, len(h.Spec.Metrics))
	for _, m := range h.Spec.Metrics {
		name, target := specMetric(m)
		if name == "" {
			continue
		}
		value, measured := current[name]
		if !measured || value == "" {
			// The autoscaler could not read it, which is the report.
			value = "?"
		}
		entries = append(entries, fmt.Sprintf("%s %s/%s", name, value, target))
	}
	return JoinCapped(entries, 3)
}

func specMetric(m autoscalingv2.MetricSpec) (string, string) {
	switch m.Type {
	case autoscalingv2.ResourceMetricSourceType:
		if m.Resource == nil {
			return "", ""
		}
		return string(m.Resource.Name), metricTarget(m.Resource.Target)
	case autoscalingv2.ContainerResourceMetricSourceType:
		if m.ContainerResource == nil {
			return "", ""
		}
		return string(m.ContainerResource.Name) + "/" + m.ContainerResource.Container,
			metricTarget(m.ContainerResource.Target)
	case autoscalingv2.PodsMetricSourceType:
		if m.Pods == nil {
			return "", ""
		}
		return m.Pods.Metric.Name, metricTarget(m.Pods.Target)
	case autoscalingv2.ObjectMetricSourceType:
		if m.Object == nil {
			return "", ""
		}
		return m.Object.Metric.Name, metricTarget(m.Object.Target)
	case autoscalingv2.ExternalMetricSourceType:
		if m.External == nil {
			return "", ""
		}
		return m.External.Metric.Name, metricTarget(m.External.Target)
	}
	return "", ""
}

func currentMetricName(m autoscalingv2.MetricStatus) string {
	switch m.Type {
	case autoscalingv2.ResourceMetricSourceType:
		if m.Resource == nil {
			return ""
		}
		return string(m.Resource.Name)
	case autoscalingv2.ContainerResourceMetricSourceType:
		if m.ContainerResource == nil {
			return ""
		}
		return string(m.ContainerResource.Name) + "/" + m.ContainerResource.Container
	case autoscalingv2.PodsMetricSourceType:
		if m.Pods == nil {
			return ""
		}
		return m.Pods.Metric.Name
	case autoscalingv2.ObjectMetricSourceType:
		if m.Object == nil {
			return ""
		}
		return m.Object.Metric.Name
	case autoscalingv2.ExternalMetricSourceType:
		if m.External == nil {
			return ""
		}
		return m.External.Metric.Name
	}
	return ""
}

func currentMetricValue(m autoscalingv2.MetricStatus) string {
	switch m.Type {
	case autoscalingv2.ResourceMetricSourceType:
		if m.Resource != nil {
			return metricValue(m.Resource.Current)
		}
	case autoscalingv2.ContainerResourceMetricSourceType:
		if m.ContainerResource != nil {
			return metricValue(m.ContainerResource.Current)
		}
	case autoscalingv2.PodsMetricSourceType:
		if m.Pods != nil {
			return metricValue(m.Pods.Current)
		}
	case autoscalingv2.ObjectMetricSourceType:
		if m.Object != nil {
			return metricValue(m.Object.Current)
		}
	case autoscalingv2.ExternalMetricSourceType:
		if m.External != nil {
			return metricValue(m.External.Current)
		}
	}
	return ""
}

// metricTarget and metricValue print the one of three fields that is set,
// utilization first: it compares across metrics.
func metricTarget(t autoscalingv2.MetricTarget) string {
	switch {
	case t.AverageUtilization != nil:
		return fmt.Sprintf("%d%%", *t.AverageUtilization)
	case t.AverageValue != nil:
		return t.AverageValue.String()
	case t.Value != nil:
		return t.Value.String()
	}
	return "?"
}

func metricValue(v autoscalingv2.MetricValueStatus) string {
	switch {
	case v.AverageUtilization != nil:
		return fmt.Sprintf("%d%%", *v.AverageUtilization)
	case v.AverageValue != nil:
		return v.AverageValue.String()
	case v.Value != nil:
		return v.Value.String()
	}
	return ""
}

// hpaStatus reports the condition's reason, the searchable string, rather
// than a summary. ScalingLimited is not graded: it is True on every HPA idling
// at its minimum.
func hpaStatus(h *autoscalingv2.HorizontalPodAutoscaler) (string, string, int) {
	for _, c := range h.Status.Conditions {
		if c.Status != corev1.ConditionFalse {
			continue
		}
		switch c.Type {
		case autoscalingv2.AbleToScale, autoscalingv2.ScalingActive:
			reason := string(c.Reason)
			if reason == "" {
				reason = string(c.Type) + "=False"
			}
			return reason, ClassBad, RankBad
		}
	}
	if len(h.Status.Conditions) == 0 {
		// Not yet reconciled: claim nothing.
		return "", ClassNone, 0
	}
	return "Active", ClassOK, RankOK
}

// pdbRow exists for disruptionsAllowed: at zero a drain blocks without naming
// the budget, so zero is graded and ranked. Healthy against desired is beside
// it: zero with everything healthy needs another replica; zero with pods
// missing is a broken workload.
func pdbRow(p *policyv1.PodDisruptionBudget, now time.Time) []Cell {
	budget := ""
	switch {
	case p.Spec.MinAvailable != nil:
		budget = "min available " + p.Spec.MinAvailable.String()
	case p.Spec.MaxUnavailable != nil:
		budget = "max unavailable " + p.Spec.MaxUnavailable.String()
	}

	allowed := count32(p.Status.DisruptionsAllowed)
	if p.Status.DisruptionsAllowed == 0 {
		allowed.Class, allowed.Rank = ClassBad, RankBad
	} else {
		allowed.Class, allowed.Rank = ClassOK, RankOK
	}

	return []Cell{
		text(p.Name),
		text(p.Namespace),
		text(budget),
		text(selectorText(p.Spec.Selector, "all pods")),
		ratio(p.Status.CurrentHealthy, p.Status.DesiredHealthy),
		allowed,
		age(p.CreationTimestamp, now),
	}
}

func priorityClassRow(p *schedulingv1.PriorityClass, now time.Time) []Cell {
	preemption := ""
	if p.PreemptionPolicy != nil {
		preemption = string(*p.PreemptionPolicy)
	}

	global := Cell{Text: Missing}
	if p.GlobalDefault {
		// The class a pod gets when its manifest names none; usually none.
		global = Cell{Text: "default", Class: ClassOK}
	}

	return []Cell{
		text(p.Name),
		count32(p.Value),
		global,
		text(preemption),
		text(Clip(p.Description, 80)),
		age(p.CreationTimestamp, now),
	}
}

func runtimeClassRow(r *nodev1.RuntimeClass, now time.Time) []Cell {
	overhead := ""
	if r.Overhead != nil {
		var parts []string
		for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
			if v, ok := r.Overhead.PodFixed[name]; ok {
				parts = append(parts, string(name)+" "+v.String())
			}
		}
		overhead = strings.Join(parts, ", ")
	}

	selector := ""
	if r.Scheduling != nil {
		selector = labelsText(r.Scheduling.NodeSelector)
	}

	return []Cell{
		text(r.Name),
		// The handler (`runc`, `gvisor`, `kata`): one no node has leaves pods
		// Pending.
		text(r.Handler),
		text(overhead),
		text(selector),
		age(r.CreationTimestamp, now),
	}
}

// leaseRow is who holds a lease and when they last renewed it. A stale lease
// is a controller that stopped working while its Deployment still reads 1/1.
func leaseRow(l *coordinationv1.Lease, now time.Time) []Cell {
	holder := ""
	if l.Spec.HolderIdentity != nil {
		holder = *l.Spec.HolderIdentity
	}

	return []Cell{
		text(l.Name),
		text(l.Namespace),
		// Clipped: the hostname, first, is the identifying half.
		text(Clip(holder, 60)),
		leaseRenewed(l, now),
		leaseDuration(l),
		age(l.CreationTimestamp, now),
	}
}

// leaseRenewed grades staleness against the lease's own duration, since
// durations differ by an order of magnitude: two durations is late, five is
// gone, and a lease stating none is not judged.
func leaseRenewed(l *coordinationv1.Lease, now time.Time) Cell {
	if l.Spec.RenewTime == nil {
		return Cell{Text: Missing}
	}
	since := now.Sub(l.Spec.RenewTime.Time)
	cell := elapsed(since)

	seconds := 0
	if l.Spec.LeaseDurationSeconds != nil {
		seconds = int(*l.Spec.LeaseDurationSeconds)
	}
	if seconds <= 0 {
		return cell
	}

	stated := time.Duration(seconds) * time.Second
	switch {
	case since > 5*stated:
		cell.Class, cell.Rank = ClassBad, RankBad
	case since > 2*stated:
		cell.Class, cell.Rank = ClassWarn, RankWarn
	default:
		cell.Class, cell.Rank = ClassOK, RankOK
	}
	return cell
}

func leaseDuration(l *coordinationv1.Lease) Cell {
	if l.Spec.LeaseDurationSeconds == nil {
		return Cell{Text: Missing}
	}
	return elapsed(time.Duration(*l.Spec.LeaseDurationSeconds) * time.Second)
}

// mutatingWebhookRow and its validating twin say how many hooks a
// configuration installs and what happens to every apply when their backend
// does not answer. `failurePolicy: Fail` is colored, not ranked: it is correct
// for many stock configurations.
func mutatingWebhookRow(w *admissionv1.MutatingWebhookConfiguration, now time.Time) []Cell {
	policies := make([]string, 0, len(w.Webhooks))
	backends := make([]string, 0, len(w.Webhooks))
	for _, hook := range w.Webhooks {
		policies = append(policies, failurePolicy(hook.FailurePolicy))
		backends = append(backends, webhookBackend(hook.ClientConfig))
	}
	return webhookCells(w.Name, len(w.Webhooks), policies, backends, w.CreationTimestamp, now)
}

func validatingWebhookRow(w *admissionv1.ValidatingWebhookConfiguration, now time.Time) []Cell {
	policies := make([]string, 0, len(w.Webhooks))
	backends := make([]string, 0, len(w.Webhooks))
	for _, hook := range w.Webhooks {
		policies = append(policies, failurePolicy(hook.FailurePolicy))
		backends = append(backends, webhookBackend(hook.ClientConfig))
	}
	return webhookCells(w.Name, len(w.Webhooks), policies, backends, w.CreationTimestamp, now)
}

// webhookCells is shared: the two kinds differ only in their hook types.
func webhookCells(name string, hooks int, policies, backends []string, created metav1.Time, now time.Time) []Cell {
	policy := text(JoinCapped(distinct(policies), 2))
	if slices.Contains(policies, string(admissionv1.Fail)) {
		policy.Class = ClassWarn
	}
	return []Cell{
		text(name),
		count(hooks),
		policy,
		text(JoinCapped(distinct(backends), 2)),
		age(created, now),
	}
}

// failurePolicy defaults to Fail, the v1 default; nil is not Ignore.
func failurePolicy(p *admissionv1.FailurePolicyType) string {
	if p == nil {
		return string(admissionv1.Fail)
	}
	return string(*p)
}

// webhookBackend is what must be reachable for the webhook to answer: a
// Service in this cluster, or a URL outside it.
func webhookBackend(config admissionv1.WebhookClientConfig) string {
	if config.Service != nil {
		return config.Service.Namespace + "/" + config.Service.Name
	}
	if config.URL != nil {
		return *config.URL
	}
	return ""
}
