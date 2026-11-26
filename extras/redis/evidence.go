package redis

import (
	"net/http"
	"sort"
	"strconv"
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
	"stat": true, "stats": true, "webui": true, "webapp": true}

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
// or `valkey` and the image's own name is a role (`team/redis/server`):
// `team-redis/web-app` says who publishes an image, not what it runs, and so
// does `redis/nginx`, on Docker Hub, on another registry (`ghcr.io/redis/nginx`)
// or behind a mirror (`hub-proxy/redis/nginx`). The registry host is not the
// path. Neither that segment nor any below it may name a tool beside the
// server (`redis/exporter/v2`, `redis-cli`); one above it is a team or a
// project (`infra-tools/redis`). Redis's module images run the server with
// their module loaded (`redislabs/rejson`), whatever their name says.
func redisImage(image string) (string, bool) {
	segments, hub := imagePath(image)
	if len(segments) == 0 {
		return "", false
	}
	if last := segments[len(segments)-1]; moduleImages[last] {
		return last, true
	}
	at := -1
	if hasAny(segments[len(segments)-1], engines) {
		at = len(segments) - 1
	} else if roles[segments[len(segments)-1]] {
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

// moduleImages are Redis's module images, each a redis-server with a module
// loaded.
var moduleImages = map[string]bool{"rejson": true, "rebloom": true, "redisgraph": true, "redistimeseries": true,
	"redisbloom": true, "redisjson": true, "redismod": true}

// roles are the image names that say which part of a Redis an image is, under
// a parent segment that names the engine (`team/redis/server`).
var roles = map[string]bool{"server": true, "master": true, "primary": true, "replica": true, "node": true,
	"cache": true, "db": true, "ha": true, "cluster": true, "standalone": true}

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
// From a selected pod running a Redis image or redis-server, whose container port the
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
				continue
			}
			// Several ports and nothing in their names: the pods it selects
			// may tell which one reaches their Redis.
		}
		if chosen, ok := byPodLabel(service, candidates, pods); ok {
			out[key] = chosen
		}
	}
	return out
}

// labelSays reports whether a label value names a Redis or a Valkey server: it
// carries either word as a token of its own (`redis-cluster`, `infra-valkey`,
// `redis7`), never inside another word (`predis`, `rediscover`), and names no
// tool beside the server (`redis-backup`, `redis-cli`). The UI's
// labelledRedis (web/app/extras/redis/shortcut.ts) is the same rule, and
// testdata/label-rule.json is the table both are tested on.
func labelSays(value string) bool {
	value = strings.ToLower(value)
	return engineToken(value) != "" && !isTool(value)
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
// of a pod whose Redis listens on 6379 is not that Redis.
//
// Where no candidate reaches a declared port, the candidates are those that
// reach none: beside a sidecar that declares its own (an exporter on 9121),
// the Redis that declares nothing is behind one of them, chosen as onePort
// chooses; a pod that declares no port at all says nothing about which, and
// its label counts only for the one alone whose name or number looks like a
// Redis's - never for `web:8080`.
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
		var reach, unreached []corev1.ServicePort
		for _, port := range candidates {
			hit := false
			for _, container := range pod.Spec.Containers {
				if targets(port, container) {
					hit = true
					break
				}
			}
			if hit {
				reach = append(reach, port)
			} else {
				unreached = append(unreached, port)
			}
		}
		if len(reach) > 0 {
			if port, ok := onePort(reach); ok {
				return chosenPort{Name: port.Name, Port: port.Port, Why: why}, true
			}
			continue
		}
		declaring, silent := declarers(pod)
		var pick func([]corev1.ServicePort) (corev1.ServicePort, bool)
		switch {
		case silent == 0:
			// Every container declares its ports and none is reached.
			continue
		case declaring == 0:
			pick = oneRedisLikePort
		default:
			// A sidecar declares its own and none is reached: the Redis is
			// a container that declares nothing, behind one of the others.
			pick = onePort
		}
		if port, ok := pick(unreached); ok {
			return chosenPort{Name: port.Name, Port: port.Port, Why: why}, true
		}
	}
	return chosenPort{}, false
}

// declarers counts a pod's containers that declare ports and those that
// declare none.
func declarers(pod *corev1.Pod) (declaring, silent int) {
	for _, container := range pod.Spec.Containers {
		if len(container.Ports) > 0 {
			declaring++
		} else {
			silent++
		}
	}
	return declaring, silent
}

// oneRedisLikePort is the one port alone whose name says Redis or Valkey or
// whose number is in 6379 to 6399.
func oneRedisLikePort(candidates []corev1.ServicePort) (corev1.ServicePort, bool) {
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

// onePort is the port a labelled Service's Redis is on: its only candidate,
// or, among several, the one alone whose name says Redis or Valkey or whose
// number is near 6379. Two that nothing tells apart are neither.
func onePort(candidates []corev1.ServicePort) (corev1.ServicePort, bool) {
	if len(candidates) == 1 {
		return candidates[0], true
	}
	return oneRedisLikePort(candidates)
}

// byImage is the candidate port a selected pod's Redis container listens on:
// a container whose image says Redis or Valkey, or that runs the server. A
// container that declares no port is found by the port its command names
// (`--port 6380`), or as the Service's only candidate.
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
			command, listens, runsServer := serverCommandPort(container)
			if !ok {
				name, ok = command, runsServer
			}
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
			if len(hits) == 0 && len(container.Ports) == 0 && listens > 0 {
				for _, port := range candidates {
					if targetNumber(port) == int32(listens) {
						hits = append(hits, port)
					}
				}
				if len(hits) == 1 {
					return chosenPort{Name: hits[0].Name, Port: hits[0].Port, Why: why}, true
				}
			}
			if len(hits) == 0 && len(container.Ports) == 0 && len(candidates) == 1 {
				// The container declares nothing: the one port there is.
				return chosenPort{Name: candidates[0].Name, Port: candidates[0].Port, Why: why}, true
			}
		}
	}
	return chosenPort{}, false
}

// servers are the binaries a container runs when it is the server.
var servers = []string{"redis-server", "valkey-server", "keydb-server"}

// launchers start a later word as the program and are skipped before it.
// withUser take a user (or user:group) first (`gosu redis redis-server`);
// flagged skip their own options (`tini -s --`, `env -i A=b`).
var (
	launchers = map[string]bool{"exec": true, "tini": true, "dumb-init": true, "--": true, "env": true,
		"gosu": true, "su-exec": true}
	withUser = map[string]bool{"gosu": true, "su-exec": true}
	flagged  = map[string]bool{"tini": true, "dumb-init": true, "env": true}
)

// keywords open a shell's compound command: the command after them is read
// (`then exec redis-server`, `do redis-server`).
var keywords = map[string]bool{"if": true, "then": true, "else": true, "elif": true, "do": true, "while": true,
	"until": true, "{": true, "!": true}

// notServing are a server's flags that print and exit instead of serving.
var notServing = map[string]bool{"--version": true, "-v": true, "--help": true, "-h": true,
	"--test-memory": true, "--check-system": true}

// shells run their `-c` argument as a command line.
var shells = map[string]bool{"sh": true, "bash": true, "ash": true, "dash": true, "zsh": true}

// shellFileOptions are the shells' long options that take a file after them.
var shellFileOptions = map[string]bool{"--rcfile": true, "--init-file": true}

// serverCommand reports whether a container runs redis-server, valkey-server
// or keydb-server, and which: as its program, by path or behind a launcher
// (`tini -s --`, `exec`, `gosu redis`, `su-exec redis`, `env A=b`), or as the
// program of a command of a shell's `-c` line (`sh -c "exec redis-server
// /etc/redis.conf"`, `while :; do redis-server; done`). Only the program
// counts, never an argument: an exporter's `--redis.addr` or a `nc -z
// redis-server 6379` loop names a host, and `redis-server.sh` is a script. A
// server started with `--sentinel` is a Sentinel; one started with
// `--version` serves nothing.
func serverCommand(container corev1.Container) (string, bool) {
	name, _, ok := serverCommandPort(container)
	return name, ok
}

// serverCommandPort is serverCommand, with the port its arguments name
// (`--port 6380`, `--port=6380`), 0 where they name none.
func serverCommandPort(container corev1.Container) (string, int, bool) {
	argv := append(append([]string{}, container.Command...), container.Args...)
	return runs(argv, true)
}

// runs reads one command line; nested says a shell's `-c` line may be read.
func runs(argv []string, nested bool) (string, int, bool) {
	i := 0
	for i < len(argv) {
		word := base(argv[i])
		if keywords[word] || assignment(argv[i]) {
			i++
			continue
		}
		if !launchers[word] {
			break
		}
		i++
		if withUser[word] && i < len(argv) {
			i++
		}
		for flagged[word] && i < len(argv) && strings.HasPrefix(argv[i], "-") && argv[i] != "--" {
			i++
		}
	}
	if i >= len(argv) {
		return "", 0, false
	}
	program := base(argv[i])
	if nested && shells[program] {
		// The shell's own options come before its script or its -c line:
		// `sh /start.sh -c redis-server` runs /start.sh. A long option
		// (`--login`) is stepped over, with the file `--rcfile` names, and so
		// is the name an `-o`, `+o`, `-O` or `+O` sets (`-o pipefail`).
		for j := i + 1; j < len(argv); j++ {
			flag := argv[j]
			switch {
			case flag == "--":
				return "", 0, false
			case strings.HasPrefix(flag, "--"):
				if shellFileOptions[flag] {
					j++
				}
				continue
			case strings.HasPrefix(flag, "-"):
				if strings.Contains(flag, "c") {
					if j+1 >= len(argv) {
						return "", 0, false
					}
					return shellLine(argv[j+1])
				}
			case !strings.HasPrefix(flag, "+") || len(flag) < 2:
				return "", 0, false
			}
			if last := flag[len(flag)-1]; last == 'o' || last == 'O' {
				j++
			}
		}
		return "", 0, false
	}
	for _, server := range servers {
		if program != server {
			continue
		}
		port := 0
		args := argv[i+1:]
		for k, arg := range args {
			if arg == "--sentinel" || notServing[arg] {
				return "", 0, false
			}
			if value, ok := strings.CutPrefix(arg, "--port="); ok {
				port = portNumber(value)
			} else if arg == "--port" && k+1 < len(args) {
				port = portNumber(args[k+1])
			}
		}
		return server, port, true
	}
	return "", 0, false
}

// portNumber is a decimal port, 0 for anything else.
func portNumber(value string) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n < 1 || n > 65535 {
		return 0
	}
	return n
}

// shellLine reads each command of a shell line, between `;`, `&&`, `||`, `|`,
// `&` and new lines outside quotes, its words split with their quotes
// dropped: a separator inside quotes (`echo "a; redis-server"`) splits nothing.
func shellLine(line string) (string, int, bool) {
	for _, segment := range shellSegments(line) {
		if name, port, ok := runs(segment, false); ok {
			return name, port, true
		}
	}
	return "", 0, false
}

// shellSegments splits a shell line into its commands' words.
func shellSegments(line string) [][]string {
	var segments [][]string
	var words []string
	var word strings.Builder
	inWord := false
	var quote rune
	endWord := func() {
		if inWord {
			words = append(words, word.String())
			word.Reset()
			inWord = false
		}
	}
	endSegment := func() {
		endWord()
		if len(words) > 0 {
			segments = append(segments, words)
			words = nil
		}
	}
	for _, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
			inWord = true
		case r == ';' || r == '&' || r == '|' || r == '\n' || r == '\r':
			endSegment()
		case r == ' ' || r == '\t':
			endWord()
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	endSegment()
	return segments
}

// base is a program's name without its directory, lower-cased.
func base(word string) string {
	word = strings.ToLower(strings.TrimSpace(word))
	if at := strings.LastIndexByte(word, '/'); at >= 0 {
		word = word[at+1:]
	}
	return word
}

// assignment is an environment assignment before the program (`A=b prog`).
func assignment(word string) bool {
	eq := strings.IndexByte(word, '=')
	return eq > 0 && !strings.ContainsAny(word[:eq], "/-")
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
	number := targetNumber(port)
	for _, cp := range container.Ports {
		if cp.ContainerPort == number {
			return true
		}
	}
	return false
}

// targetNumber is the number a Service port forwards to: its numeric
// targetPort, or the port itself where unset; 0 for a named targetPort.
func targetNumber(port corev1.ServicePort) int32 {
	if port.TargetPort.Type == intstr.String && port.TargetPort.StrVal != "" {
		return 0
	}
	if port.TargetPort.IntVal != 0 {
		return port.TargetPort.IntVal
	}
	return port.Port
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
