package metrics

// Instant usage, for a list's CPU and memory columns.
//
// metrics-server is tried first whatever Discover decided: Discover prefers
// Prometheus for history, but an instant value is cheapest from metrics-server
// (one call per namespace, no port-forward).
//
// A cluster with no metrics backend is an absence, drawn as an em dash, and is
// kept distinct from a cluster that could not be asked (an expired session, a
// refusal), which the operator has to fix.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

// Usage is what one pod or node is using at this moment.
type Usage struct {
	// CPUMillicores is thousandths of a core, the unit of a Kubernetes request.
	CPUMillicores int64 `json:"cpuMillicores"`
	MemoryBytes   int64 `json:"memoryBytes"`
	// From is KindMetricsServer or KindPrometheus, which do not measure quite
	// the same thing.
	From string `json:"from"`
}

// ErrNoMetrics is the answer to "what is this using?" on a cluster that has no
// metrics backend installed. It is an absence, not a failure: test for it with
// IsNoMetrics and render an em dash.
var ErrNoMetrics = errors.New("this cluster has no metrics backend")

// Unavailable says no usage figure can be produced, and why.
type Unavailable struct {
	Cluster string
	// Kind is one of the Kind* constants: KindNone for an absence, and
	// KindUnreachable or KindUnauthorised when the cluster could not be asked.
	Kind   string
	Detail string
}

func (u *Unavailable) Error() string {
	return fmt.Sprintf("no usage figures for %s (%s): %s", u.Cluster, u.Kind, u.Detail)
}

// Unwrap makes errors.Is(err, ErrNoMetrics) true for an absence and false for a
// refusal.
func (u *Unavailable) Unwrap() error {
	if u.Kind == KindNone {
		return ErrNoMetrics
	}
	return nil
}

// IsNoMetrics reports that the cluster answered and has nothing to report with.
// Show an em dash.
func IsNoMetrics(err error) bool { return errors.Is(err, ErrNoMetrics) }

// CouldNotAsk reports that the cluster could not answer: an expired session, a
// refused read, no network. The operator has to fix it.
func CouldNotAsk(err error) bool {
	var unavailable *Unavailable
	if errors.As(err, &unavailable) {
		return unavailable.Kind == KindUnreachable || unavailable.Kind == KindUnauthorised
	}
	return false
}

// Key is how a usage map is keyed when it spans namespaces.
// Within one namespace the pod name is the key; across namespaces it is not
// unique, so the namespace is added.
func Key(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "/" + name
}

// PodUsage reports what every pod in a namespace is using. An empty namespace
// means every namespace, and the keys are then `namespace/name` — see Key.
func (m *Metrics) PodUsage(ctx context.Context, name, namespace string) (map[string]Usage, error) {
	if namespace != "" && !validName(namespace) {
		return nil, fmt.Errorf("namespace %q is not a Kubernetes name", namespace)
	}
	path := metricsAPIPath + "/pods"
	if namespace != "" {
		path = metricsAPIPath + "/namespaces/" + namespace + "/pods"
	}
	return m.usage(ctx, name, path, namespace, podKinded)
}

// NodeUsage reports what every node is using.
func (m *Metrics) NodeUsage(ctx context.Context, name string) (map[string]Usage, error) {
	return m.usage(ctx, name, metricsAPIPath+"/nodes", "", nodeKinded)
}

// kinded is what differs between the pod and node cases, so the flow
// (metrics-server, then Prometheus, then a typed absence) is written once.
type kinded struct {
	// sumContainers: a pod's usage is the sum of its containers', which
	// metrics-server does not state as a total. A node's is stated directly.
	sumContainers bool
	// cpu and memory build the Prometheus fallback's queries for a namespace.
	cpu    func(namespace string) string
	memory func(namespace string) string
	// key turns a series' labels into the map key.
	key func(labels map[string]string) string
}

func (m *Metrics) usage(ctx context.Context, name, path, namespace string, kind kinded) (map[string]Usage, error) {
	resolved, ok := m.clusters.Resolve(name)
	if !ok {
		return nil, fmt.Errorf("no context named %q in the kubeconfig", name)
	}
	client, err := m.clusters.Client(resolved)
	if err != nil {
		return nil, &Unavailable{Cluster: resolved, Kind: KindUnreachable, Detail: shortError(err)}
	}

	raw, serverErr := m.raw(ctx, client, path, nil)
	if serverErr == nil {
		usage, err := parseUsageList(raw, namespace, kind.sumContainers)
		if err != nil {
			return nil, err
		}
		return usage, nil
	}

	// metrics-server did not answer. Discover knows whether there is a
	// Prometheus to ask instead, and has classified the silence.
	found := m.Discover(ctx, resolved, false)
	if found.Kind == KindPrometheus {
		return m.usageFromProm(ctx, resolved, namespace, kind)
	}
	if found.CouldNotAsk() {
		return nil, &Unavailable{Cluster: resolved, Kind: found.Kind, Detail: found.Detail}
	}
	if f := classify(serverErr); f != nil {
		// metrics.k8s.io itself refused: still "could not ask", never an em
		// dash.
		return nil, &Unavailable{Cluster: resolved, Kind: f.kind, Detail: f.detail}
	}
	return nil, &Unavailable{
		Cluster: resolved,
		Kind:    KindNone,
		Detail:  fmt.Sprintf("metrics.k8s.io is not there (%s) and no Prometheus was found", shortError(serverErr)),
	}
}

// --- metrics-server ----------------------------------------------------------

// usageList is the shape metrics.k8s.io answers with, reduced to what is read.
// Node and pod metrics differ only in whether usage is stated per container.
type usageList struct {
	Items []struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Usage      map[string]string `json:"usage"`
		Containers []struct {
			Name  string            `json:"name"`
			Usage map[string]string `json:"usage"`
		} `json:"containers"`
	} `json:"items"`
}

func parseUsageList(raw []byte, namespace string, sumContainers bool) (map[string]Usage, error) {
	var list usageList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("metrics-server answered with something unreadable: %w", err)
	}

	usage := make(map[string]Usage, len(list.Items))
	for _, item := range list.Items {
		var entry Usage
		entry.From = KindMetricsServer
		if sumContainers {
			for _, container := range item.Containers {
				entry.CPUMillicores += milliCores(container.Usage["cpu"])
				entry.MemoryBytes += byteCount(container.Usage["memory"])
			}
		} else {
			entry.CPUMillicores = milliCores(item.Usage["cpu"])
			entry.MemoryBytes = byteCount(item.Usage["memory"])
		}

		// Keyed by name alone when the caller asked about one namespace.
		key := item.Metadata.Name
		if namespace == "" && item.Metadata.Namespace != "" {
			key = Key(item.Metadata.Namespace, item.Metadata.Name)
		}
		usage[key] = entry
	}
	return usage, nil
}

func milliCores(quantity string) int64 {
	parsed, err := resource.ParseQuantity(quantity)
	if err != nil {
		return 0
	}
	return parsed.MilliValue()
}

func byteCount(quantity string) int64 {
	parsed, err := resource.ParseQuantity(quantity)
	if err != nil {
		return 0
	}
	return parsed.Value()
}

// --- Prometheus, when there is no metrics-server ------------------------------

// usageRateWindow is what a CPU rate is averaged over. Two minutes, because a
// 30-second scrape leaves one minute with two points and a jumpy figure.
const usageRateWindow = "2m"

// ScrapeLabels are the labels that say who scraped a series rather than what
// it measures, and Dedup drops them before anything is summed.
//
// Two kube-prometheus-stack releases each install a kubelet ServiceMonitor over
// the same endpoint, so every cAdvisor series is stored twice, differing only
// in `service`, and `sum()` returns double.
//
// `avg`, because several scrapes are several readings of one thing (a no-op
// with one target). `without` rather than `by`, because the dimensions to keep
// differ per metric (container, interface, device) while these five are to be
// dropped everywhere.
//
// internal/overview/object.go repeats this for the chart queries, and
// TestDedupIsOneRule pins the two together.
const ScrapeLabels = "job, instance, endpoint, service, metrics_path"

// Dedup makes one container scraped by several Prometheus targets count once.
// See ScrapeLabels.
func Dedup(inner string) string { return "avg without (" + ScrapeLabels + ") (" + inner + ")" }

var podKinded = kinded{
	sumContainers: true,
	// container!="" drops cadvisor's per-pod aggregate, which would double
	// every figure.
	cpu: func(namespace string) string {
		return fmt.Sprintf(`sum by (namespace, pod) (%s)`, Dedup(fmt.Sprintf(
			`rate(container_cpu_usage_seconds_total{%scontainer!=""}[%s])`,
			namespaceMatcher(namespace), usageRateWindow)))
	},
	memory: func(namespace string) string {
		return fmt.Sprintf(`sum by (namespace, pod) (%s)`, Dedup(fmt.Sprintf(
			`container_memory_working_set_bytes{%scontainer!=""}`, namespaceMatcher(namespace))))
	},
	key: func(labels map[string]string) string { return labels["pod"] },
}

// nodeKinded sums the containers a node runs, the one figure cadvisor labels
// with a node name everywhere. It is a floor (the kubelet and runtime are not
// in it), which is why it is used only without metrics-server.
var nodeKinded = kinded{
	cpu: func(string) string {
		return fmt.Sprintf(`sum by (node) (%s)`, Dedup(fmt.Sprintf(
			`rate(container_cpu_usage_seconds_total{container!=""}[%s])`, usageRateWindow)))
	},
	memory: func(string) string {
		return fmt.Sprintf(`sum by (node) (%s)`, Dedup(`container_memory_working_set_bytes{container!=""}`))
	},
	key: func(labels map[string]string) string { return labels["node"] },
}

func namespaceMatcher(namespace string) string {
	if namespace == "" {
		return ""
	}
	// Validated as a Kubernetes name before here, so it cannot carry a quote.
	return fmt.Sprintf("namespace=%q,", namespace)
}

func (m *Metrics) usageFromProm(ctx context.Context, name, namespace string, kind kinded) (map[string]Usage, error) {
	// The shared session: a table refreshing every two seconds asks twice
	// each time.
	session, release, err := m.acquire(ctx, name)
	if err != nil {
		return nil, err
	}
	defer release()

	cpu, err := session.Query(ctx, kind.cpu(namespace))
	if err != nil {
		return nil, err
	}
	memory, err := session.Query(ctx, kind.memory(namespace))
	if err != nil {
		return nil, err
	}

	usage := map[string]Usage{}
	if err := collectSeries(cpu, namespace, kind, usage, func(entry *Usage, value float64) {
		// Cores to millicores.
		entry.CPUMillicores = int64(value * 1000)
	}); err != nil {
		return nil, err
	}
	if err := collectSeries(memory, namespace, kind, usage, func(entry *Usage, value float64) {
		entry.MemoryBytes = int64(value)
	}); err != nil {
		return nil, err
	}
	if len(usage) == 0 {
		return nil, &Unavailable{
			Cluster: name,
			Kind:    KindNone,
			Detail:  "Prometheus answered, but holds no container series for this selection",
		}
	}
	return usage, nil
}

// promSeries is an instant query's answer, reduced to what a table needs.
type promSeries struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Value  []any             `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

func collectSeries(raw []byte, namespace string, kind kinded, into map[string]Usage, set func(*Usage, float64)) error {
	var parsed promSeries
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("Prometheus answered with something unreadable: %w", err)
	}
	if parsed.Status != "success" {
		return fmt.Errorf("Prometheus rejected the usage query: %s", parsed.Error)
	}

	for _, series := range parsed.Data.Result {
		name := kind.key(series.Metric)
		if name == "" {
			// No pod or node label: there is no row to put it in.
			continue
		}
		key := name
		if namespace == "" {
			if ns := series.Metric["namespace"]; ns != "" {
				key = Key(ns, name)
			}
		}
		value, ok := sampleValue(series.Value)
		if !ok {
			continue
		}
		entry := into[key]
		entry.From = KindPrometheus
		set(&entry, value)
		into[key] = entry
	}
	return nil
}

// sampleValue reads the `[timestamp, "value"]` pair of a Prometheus sample.
//
// NaN (a rate over a counter just reset) and Inf parse without error under
// `%g`, and `int64(math.NaN()*1000)` is the minimum int64 on amd64, so both are
// refused: false makes collectSeries skip the series, and the column shows an
// em dash rather than a number.
func sampleValue(sample []any) (float64, bool) {
	if len(sample) < 2 {
		return 0, false
	}
	text, ok := sample[1].(string)
	if !ok {
		return 0, false
	}
	var value float64
	if _, err := fmt.Sscanf(strings.TrimSpace(text), "%g", &value); err != nil {
		return 0, false
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

// nameRE is the Kubernetes name grammar, applied (validName) to anything that
// reaches a PromQL string literal or an API path from a browser.
var nameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,61}[a-z0-9])?$`)

func validName(name string) bool { return nameRE.MatchString(name) }
