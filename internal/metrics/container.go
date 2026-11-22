package metrics

import (
	"context"
	"encoding/json"
	"fmt"
)

// ContainerUsage reports what every *container* of every pod in a namespace is
// using. An empty namespace means every namespace.
//
// The keys are always `namespace/pod/container` (ContainerKey), unlike
// PodUsage's, so their shape does not change with the question.
//
// It is metrics-server only, with no Prometheus fallback: per-container series
// carry an empty `container` label on exactly the series a naive query picks up
// (the pause container, the cgroup total), which would attribute a pod's whole
// memory to one container. Without metrics-server it returns a typed absence.
func (m *Metrics) ContainerUsage(ctx context.Context, name, namespace string) (map[string]Usage, error) {
	if namespace != "" && !validName(namespace) {
		return nil, fmt.Errorf("namespace %q is not a Kubernetes name", namespace)
	}

	resolved, ok := m.clusters.Resolve(name)
	if !ok {
		return nil, fmt.Errorf("no context named %q in the kubeconfig", name)
	}
	client, err := m.clusters.Client(resolved)
	if err != nil {
		return nil, &Unavailable{Cluster: resolved, Kind: KindUnreachable, Detail: shortError(err)}
	}

	path := metricsAPIPath + "/pods"
	if namespace != "" {
		path = metricsAPIPath + "/namespaces/" + namespace + "/pods"
	}

	raw, serverErr := m.raw(ctx, client, path, nil)
	if serverErr == nil {
		return parseContainerUsage(raw)
	}

	// Classified as the pod case is, so a refusal never reads as nothing to
	// report; there is no Prometheus branch (see the doc comment).
	if found := m.Discover(ctx, resolved, false); found.CouldNotAsk() {
		return nil, &Unavailable{Cluster: resolved, Kind: found.Kind, Detail: found.Detail}
	}
	if f := classify(serverErr); f != nil {
		return nil, &Unavailable{Cluster: resolved, Kind: f.kind, Detail: f.detail}
	}
	return nil, &Unavailable{
		Cluster: resolved,
		Kind:    KindNone,
		Detail: fmt.Sprintf("per-container usage needs metrics-server, and metrics.k8s.io is not there (%s)",
			shortError(serverErr)),
	}
}

// ContainerKey is how a container usage map is keyed, namespace always
// included.
func ContainerKey(namespace, pod, container string) string {
	return namespace + "/" + pod + "/" + container
}

func parseContainerUsage(raw []byte) (map[string]Usage, error) {
	var list usageList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("metrics-server answered with something unreadable: %w", err)
	}

	usage := make(map[string]Usage, len(list.Items))
	for _, item := range list.Items {
		for _, container := range item.Containers {
			usage[ContainerKey(item.Metadata.Namespace, item.Metadata.Name, container.Name)] = Usage{
				CPUMillicores: milliCores(container.Usage["cpu"]),
				MemoryBytes:   byteCount(container.Usage["memory"]),
				From:          KindMetricsServer,
			}
		}
	}
	return usage, nil
}
