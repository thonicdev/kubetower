package redis

import (
	"net/http"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/svcgroup"
)

// A Service whose ports are neither named like a Redis nor on 6379 is still a
// Redis when it says what it fronts: a StatefulSet of valkey/valkey behind a
// port named `client` on 6380 is one. This file finds those, after the port
// guess (looksLikeRedis) has had its say, and only for Services it took none
// of.

// labelKeys are the labels whose value says what a workload runs.
var labelKeys = []string{"app.kubernetes.io/name", "app"}

// engines are the words an image's path carries when it runs the server.
var engines = []string{"redis", "valkey"}

// tools are the words of an image or a label beside the server rather than
// the server itself: an exporter, a web or command-line client, a Sentinel,
// an operator, a backup or a migration. Found anywhere in a word.
var tools = []string{"exporter", "commander", "insight", "sentinel", "operator", "metrics", "admin", "browser",
	"proxy", "backup", "dump", "shake", "tools", "dashboard", "redisson"}

// toolTokens are the short ones, taken only as a whole token between `-`,
// `_` or `.` (`ui` is inside `build`, `cli` inside `client`), or glued to an
// engine word (`redisui`, `redisclient`).
var toolTokens = map[string]bool{"ui": true, "cli": true, "client": true, "tool": true, "gui": true, "web": true,
	"stat": true, "stats": true}

// isTool reports whether a word names a tool beside the server.
func isTool(word string) bool {
	if hasAny(word, tools) {
		return true
	}
	for _, token := range strings.FieldsFunc(word, func(r rune) bool { return r == '-' || r == '_' || r == '.' }) {
		if toolTokens[token] {
			return true
		}
		rest := token
		for _, engine := range engines {
			rest = strings.ReplaceAll(rest, engine, "")
		}
		if rest != token && toolTokens[rest] {
			return true
		}
	}
	return false
}

// chosenPort is the port a Service's evidence picked, and why.
type chosenPort struct {
	Name string
	Port int32
	Why  string
}

// notAServerPort is a port that is never the server's, whatever the Service
// says: an exporter, a Sentinel, the cluster bus, a web port.
func notAServerPort(port corev1.ServicePort) bool {
	name := strings.ToLower(strings.TrimSpace(port.Name))
	switch {
	case strings.Contains(name, "metrics"), strings.Contains(name, "exporter"), strings.Contains(name, "sentinel"),
		strings.Contains(name, "bus"), strings.HasPrefix(name, "http"), strings.HasPrefix(name, "https"):
		return true
	case port.Port == 9121, port.Port == 26379, port.Port == 16379:
		return true
	case port.Protocol != "" && port.Protocol != corev1.ProtocolTCP:
		return true
	}
	return false
}

// redisImage reports whether an image runs a Redis or a Valkey server, and the
// path segment that says so. An internal registry renames images, so the word
// may be inside the image's own name (`infra-valkey`, `infravalkey`,
// `redis-cluster`). A segment above it counts only when it is exactly `redis`
// or `valkey` (`team/redis/server`): `team-redis/web-app` or
// `redislabs/rebloom` say who publishes an image, not what it runs, and so
// does the first segment on Docker Hub (`redis/nginx`). The registry host is
// not the path. Neither that segment nor any below it may name a tool beside
// the server (`redis/exporter/v2`, `redis-cli`); one above it is a team or a
// project (`infra-tools/redis`).
func redisImage(image string) (string, bool) {
	segments, hub := imagePath(image)
	if len(segments) == 0 {
		return "", false
	}
	at := -1
	if hasAny(segments[len(segments)-1], engines) {
		at = len(segments) - 1
	} else {
		first := 0
		if hub {
			first = 1
		}
		for i := len(segments) - 2; i >= first; i-- {
			if segments[i] == "redis" || segments[i] == "valkey" {
				at = i
				break
			}
		}
	}
	if at < 0 {
		return "", false
	}
	for _, segment := range segments[at:] {
		if isTool(segment) {
			return "", false
		}
	}
	return segments[at], true
}

// imagePath is an image reference's repository path, lower-cased, without its
// registry host, tag or digest, and whether it is on Docker Hub, where the
// first segment is the publisher.
func imagePath(image string) ([]string, bool) {
	image = strings.ToLower(strings.TrimSpace(image))
	if at := strings.IndexByte(image, '@'); at >= 0 {
		image = image[:at]
	}
	segments := strings.Split(image, "/")
	last := segments[len(segments)-1]
	if colon := strings.IndexByte(last, ':'); colon >= 0 {
		segments[len(segments)-1] = last[:colon]
	}
	hub := true
	if len(segments) > 1 && (strings.ContainsAny(segments[0], ".:") || segments[0] == "localhost") {
		switch segments[0] {
		case "docker.io", "index.docker.io", "registry-1.docker.io":
		default:
			hub = false
		}
		segments = segments[1:]
	}
	return segments, hub
}

func hasAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// evidence picks, for each Service the port guess took nothing of, the one
// port that is its Redis, by "namespace/name".
//
// From a selected pod running a Redis image, whose container port the
// Service port targets, when pods is not nil (a read scoped to one
// namespace); then from an `app.kubernetes.io/name` or `app` label saying
// Redis or Valkey, on the Service or its selector, on its only candidate port
// or the one alone that looks like a Redis's; then from such a label on a
// selected pod, among the candidate ports that reach that pod. A Service
// with several candidate ports and nothing to tell them apart is left alone:
// a guess between two ports would dial the wrong one.
func evidence(services []corev1.Service, taken map[string]bool, pods []*corev1.Pod) map[string]chosenPort {
	out := map[string]chosenPort{}
	for i := range services {
		service := &services[i]
		key := service.Namespace + "/" + service.Name
		if taken[key] || service.Spec.Type == corev1.ServiceTypeExternalName {
			continue
		}
		var candidates []corev1.ServicePort
		for _, port := range service.Spec.Ports {
			if !notAServerPort(port) {
				candidates = append(candidates, port)
			}
		}
		if len(candidates) == 0 {
			continue
		}
		if chosen, ok := byImage(service, candidates, pods); ok {
			out[key] = chosen
			continue
		}
		if why, ok := byLabel(service); ok {
			if port, ok := onePort(candidates); ok {
				out[key] = chosenPort{Name: port.Name, Port: port.Port, Why: why}
			}
			continue
		}
		if chosen, ok := byPodLabel(service, candidates, pods); ok {
			out[key] = chosen
		}
	}
	return out
}

// labelSays reports whether a label value names a Redis or a Valkey server: it
// contains either word (`redis-cluster`, `infra-valkey`) and names no tool
// beside the server.
func labelSays(value string) bool {
	value = strings.ToLower(value)
	return hasAny(value, engines) && !isTool(value)
}

// byLabel is the label saying a Service fronts a Redis, on the Service or in
// its selector.
func byLabel(service *corev1.Service) (string, bool) {
	for _, set := range []map[string]string{service.Labels, service.Spec.Selector} {
		if why, ok := labelled(set); ok {
			return why, true
		}
	}
	return "", false
}

func labelled(set map[string]string) (string, bool) {
	for _, key := range labelKeys {
		if value := set[key]; labelSays(value) {
			return "it is labeled " + key + "=" + value, true
		}
	}
	return "", false
}

// byPodLabel is the port of a pod the Service selects and that is labelled a
// Redis, among the candidates that reach it: a `web` port on 8080 in front
// of a pod whose Redis listens on 6379 is not that Redis. A pod that declares
// no container port lends its label to every candidate.
func byPodLabel(service *corev1.Service, candidates []corev1.ServicePort, pods []*corev1.Pod) (chosenPort, bool) {
	if pods == nil || len(service.Spec.Selector) == 0 {
		return chosenPort{}, false
	}
	selector := labels.SelectorFromSet(service.Spec.Selector)
	for _, pod := range pods {
		if pod.Namespace != service.Namespace || !selector.Matches(labels.Set(pod.Labels)) {
			continue
		}
		why, ok := labelled(pod.Labels)
		if !ok {
			continue
		}
		reach := candidates
		if declaresPorts(pod) {
			reach = nil
			for _, port := range candidates {
				for _, container := range pod.Spec.Containers {
					if targets(port, container) {
						reach = append(reach, port)
						break
					}
				}
			}
		}
		if port, ok := onePort(reach); ok {
			return chosenPort{Name: port.Name, Port: port.Port, Why: why}, true
		}
	}
	return chosenPort{}, false
}

func declaresPorts(pod *corev1.Pod) bool {
	for _, container := range pod.Spec.Containers {
		if len(container.Ports) > 0 {
			return true
		}
	}
	return false
}

// onePort is the port a labelled Service's Redis is on: its only candidate,
// or, among several, the one alone whose name says Redis or Valkey or whose
// number is near 6379. Two that nothing tells apart are neither.
func onePort(candidates []corev1.ServicePort) (corev1.ServicePort, bool) {
	if len(candidates) == 1 {
		return candidates[0], true
	}
	var like []corev1.ServicePort
	for _, port := range candidates {
		if hasAny(strings.ToLower(port.Name), engines) || (port.Port >= 6379 && port.Port <= 6399) {
			like = append(like, port)
		}
	}
	if len(like) == 1 {
		return like[0], true
	}
	return corev1.ServicePort{}, false
}

// byImage is the candidate port a selected pod's Redis container listens on.
func byImage(service *corev1.Service, candidates []corev1.ServicePort, pods []*corev1.Pod) (chosenPort, bool) {
	if pods == nil || len(service.Spec.Selector) == 0 {
		return chosenPort{}, false
	}
	selector := labels.SelectorFromSet(service.Spec.Selector)
	for _, pod := range pods {
		if pod.Namespace != service.Namespace || !selector.Matches(labels.Set(pod.Labels)) {
			continue
		}
		for _, container := range pod.Spec.Containers {
			name, ok := redisImage(container.Image)
			if !ok {
				continue
			}
			why := "its pod runs " + name
			var hits []corev1.ServicePort
			for _, port := range candidates {
				if targets(port, container) {
					hits = append(hits, port)
				}
			}
			if len(hits) == 1 {
				return chosenPort{Name: hits[0].Name, Port: hits[0].Port, Why: why}, true
			}
			if len(hits) == 0 && len(container.Ports) == 0 && len(candidates) == 1 {
				// The container declares nothing: the one port there is.
				return chosenPort{Name: candidates[0].Name, Port: candidates[0].Port, Why: why}, true
			}
		}
	}
	return chosenPort{}, false
}

// targets says whether the Service port reaches one of the container's ports:
// by name for a named targetPort, by number otherwise (an unset targetPort is
// the port itself).
func targets(port corev1.ServicePort, container corev1.Container) bool {
	target := port.TargetPort
	if target.Type == intstr.String && target.StrVal != "" {
		for _, cp := range container.Ports {
			if cp.Name == target.StrVal {
				return true
			}
		}
		return false
	}
	number := target.IntVal
	if number == 0 {
		number = port.Port
	}
	for _, cp := range container.Ports {
		if cp.ContainerPort == number {
			return true
		}
	}
	return false
}

// withEvidence adds, to what the port guess found, the port each Service's
// evidence chose, with what is behind it as for any other, in Discover's
// order (namespace, service, port).
func withEvidence(found []svcgroup.Instance, services []corev1.Service, slices []discoveryv1.EndpointSlice, slicesRead bool, chosen map[string]chosenPort) []svcgroup.Instance {
	if len(chosen) == 0 {
		return found
	}
	for i := range services {
		pick, ok := chosen[services[i].Namespace+"/"+services[i].Name]
		if !ok {
			continue
		}
		match := func(port corev1.ServicePort) (string, bool) {
			return pick.Why, port.Name == pick.Name && port.Port == pick.Port
		}
		found = append(found, svcgroup.Discover(services[i:i+1], slices, slicesRead, match)...)
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].Namespace != found[j].Namespace {
			return found[i].Namespace < found[j].Namespace
		}
		if found[i].Service != found[j].Service {
			return found[i].Service < found[j].Service
		}
		return found[i].Port < found[j].Port
	})
	return found
}

// podsIn lists one namespace's pods for the image evidence; nil when the
// listing was refused, partial or could not be decoded, which leaves the
// labels as the only evidence.
func podsIn(r *http.Request, h extension.Host, cluster, namespace string) []*corev1.Pod {
	page, err := h.List(r, cluster, extension.ListQuery{GVR: podsGVR, Namespace: namespace})
	if err != nil || !page.Complete {
		return nil
	}
	pods := make([]*corev1.Pod, 0, len(page.Items))
	for i := range page.Items {
		var pod corev1.Pod
		if err := fromUnstructured(&page.Items[i], &pod); err != nil {
			return nil
		}
		pods = append(pods, &pod)
	}
	return pods
}
