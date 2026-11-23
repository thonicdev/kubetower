package runtimeinspect

import (
	"path"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// Which runtime a container runs, read from the pod's spec alone.
//
// Each runtime is a provider: the hints its spec gives, from its command
// line, its environment and its image. A container is the runtime whose
// strongest hint is the strongest (a command line over an environment
// variable over an image's name), ties going to the providers' order. The
// pod's annotation `kubetower.io/runtime` wins over every hint, and a known
// sidecar is not inspected unless the annotation names it.
//
// None is "not seen from the spec", never "no runtime": an image whose
// entrypoint starts the program with nothing in the spec says nothing here.

// The runtimes, as the answers and the page spell them.
const (
	runtimeJava   = "java"
	runtimeGo     = "go"
	runtimeNode   = "node"
	runtimePython = "python"
)

// AnnotationRuntime names a pod's containers' runtimes: `python` for every
// container, or `app=python,proxy=none` per container. `none` takes a
// container out; an unknown runtime is ignored.
const AnnotationRuntime = "kubetower.io/runtime"

// The sources of a hint, weakest first: the rank a provider is judged on.
const (
	sourceImage      = "image"
	sourceEnv        = "env"
	sourceCommand    = "command"
	sourceAnnotation = "annotation"
)

func rank(source string) int {
	switch source {
	case sourceAnnotation:
		return 4
	case sourceCommand:
		return 3
	case sourceEnv:
		return 2
	case sourceImage:
		return 1
	}
	return 0
}

// provider is one runtime's reading of a container's spec.
type provider struct {
	name  string
	hints func(corev1.Container) []Hint
}

// providers in the order a tie goes.
var providers = []provider{
	{name: runtimeJava, hints: hints},
	{name: runtimeNode, hints: nodeHints},
	{name: runtimePython, hints: pythonHints},
	{name: runtimeGo, hints: goHints},
}

// providerOrder is name's place in providers, past the end when unknown.
func providerOrder(name string) int {
	for i, p := range providers {
		if p.name == name {
			return i
		}
	}
	return len(providers)
}

func knownRuntime(name string) bool {
	for _, p := range providers {
		if p.name == name {
			return true
		}
	}
	return false
}

// sidecars are the containers a mesh, a proxy or an agent adds beside the
// application, by name: not the application's runtime.
var sidecars = map[string]bool{
	"istio-proxy": true, "istio-init": true, "istio-validation": true,
	"linkerd-proxy": true, "linkerd-init": true, "linkerd-network-validator": true,
	"envoy": true, "envoy-sidecar": true, "cloud-sql-proxy": true, "vault-agent": true, "vault-agent-init": true,
}

func isSidecar(c corev1.Container) bool {
	return sidecars[c.Name] || strings.HasSuffix(c.Name, "-exporter")
}

// detection is the runtime a container was read as, with what it rests on.
type detection struct {
	runtime string
	hints   []Hint
}

// detect is c's runtime in a pod annotated with annotated (AnnotationRuntime,
// parsed by annotatedRuntimes).
func detect(c corev1.Container, annotated map[string]string) detection {
	if name, ok := annotated[c.Name]; ok {
		if name == "none" {
			return detection{}
		}
		return detection{runtime: name, hints: []Hint{{Source: sourceAnnotation, Reason: AnnotationRuntime + " names " + name}}}
	}
	if isSidecar(c) {
		return detection{}
	}
	best, bestRank := detection{}, 0
	for _, p := range providers {
		found := p.hints(c)
		top := 0
		for _, h := range found {
			top = max(top, rank(h.Source))
		}
		if top > bestRank {
			best, bestRank = detection{runtime: p.name, hints: found}, top
		}
	}
	return best
}

// annotatedRuntimes reads AnnotationRuntime against containers: a bare
// runtime applies to each of them, `name=runtime` to that one.
func annotatedRuntimes(annotations map[string]string, containers []corev1.Container) map[string]string {
	value := strings.TrimSpace(annotations[AnnotationRuntime])
	if value == "" {
		return nil
	}
	out := map[string]string{}
	valid := func(name string) bool { return name == "none" || knownRuntime(name) }
	for _, part := range strings.Split(value, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		container, name, named := strings.Cut(part, "=")
		if !named {
			if valid(part) {
				for _, c := range containers {
					if _, set := out[c.Name]; !set {
						out[c.Name] = part
					}
				}
			}
			continue
		}
		container, name = strings.TrimSpace(container), strings.TrimSpace(name)
		if container != "" && valid(name) {
			out[container] = name
		}
	}
	return out
}

// --- Node.js -----------------------------------------------------------------

// nodePrograms start Node.js or a package manager that runs it.
var nodePrograms = map[string]bool{"node": true, "nodejs": true, "npm": true, "npx": true, "yarn": true, "pnpm": true}

func nodeHints(c corev1.Container) []Hint {
	var out []Hint
	for _, token := range commandLine(c) {
		base := path.Base(token)
		switch {
		case nodePrograms[base]:
			out = append(out, Hint{Source: sourceCommand, Reason: "the command line starts " + quote(token)})
		case strings.HasSuffix(token, ".js") || strings.HasSuffix(token, ".mjs") || strings.HasSuffix(token, ".cjs"):
			out = append(out, Hint{Source: sourceCommand, Reason: "the command line names a script: " + quote(clip(token))})
		default:
			continue
		}
		break
	}
	for _, e := range c.Env {
		switch e.Name {
		case "NODE_OPTIONS", "NODE_ENV", "NODE_EXTRA_CA_CERTS":
			out = append(out, Hint{Source: sourceEnv, Reason: e.Name + " is set"})
		}
	}
	if word, ok := imageWord(c.Image, "node", "nodejs"); ok {
		out = append(out, Hint{Source: sourceImage, Reason: "the image " + quote(c.Image) + " is " + word})
	}
	return out
}

// --- Python ------------------------------------------------------------------

// pythonServers are programs that run a Python application.
var pythonServers = map[string]bool{
	"gunicorn": true, "uvicorn": true, "hypercorn": true, "daphne": true, "celery": true, "flask": true,
	"django-admin": true, "uwsgi": true,
}

func isPythonProgram(base string) bool {
	if base == "python" || base == "python3" || base == "pypy" || base == "pypy3" {
		return true
	}
	if rest, ok := strings.CutPrefix(base, "python3."); ok && rest != "" && strings.Trim(rest, "0123456789") == "" {
		return true
	}
	return pythonServers[base]
}

func pythonHints(c corev1.Container) []Hint {
	var out []Hint
	for _, token := range commandLine(c) {
		base := path.Base(token)
		switch {
		case isPythonProgram(base):
			out = append(out, Hint{Source: sourceCommand, Reason: "the command line starts " + quote(token)})
		case strings.HasSuffix(token, ".py"):
			out = append(out, Hint{Source: sourceCommand, Reason: "the command line names a script: " + quote(clip(token))})
		default:
			continue
		}
		break
	}
	for _, e := range c.Env {
		switch e.Name {
		case "PYTHONPATH", "PYTHONUNBUFFERED", "PYTHONDONTWRITEBYTECODE", "PYTHONHASHSEED", "VIRTUAL_ENV":
			out = append(out, Hint{Source: sourceEnv, Reason: e.Name + " is set"})
		}
	}
	if word, ok := imageWord(c.Image, "python", "python3", "pypy"); ok {
		out = append(out, Hint{Source: sourceImage, Reason: "the image " + quote(c.Image) + " is " + word})
	}
	return out
}

// --- Go ----------------------------------------------------------------------

// goEnv are read by the Go runtime itself.
var goEnv = map[string]bool{"GOMAXPROCS": true, "GOMEMLIMIT": true, "GODEBUG": true, "GOGC": true, "GOTRACEBACK": true}

// goProducts are well-known images of software written in Go, matched as a
// word of the image's last path segment as javaProducts are. The list ages.
var goProducts = []string{
	"prometheus", "alertmanager", "coredns", "etcd", "traefik", "consul", "vault", "minio", "thanos", "loki",
	"tempo", "grafana", "caddy", "kube-state-metrics", "external-dns", "cert-manager-controller",
}

func goHints(c corev1.Container) []Hint {
	var out []Hint
	for _, e := range c.Env {
		if goEnv[e.Name] {
			out = append(out, Hint{Source: sourceEnv, Reason: e.Name + " is set: the Go runtime reads it"})
		}
	}
	if word, ok := imageWord(c.Image, "golang"); ok {
		out = append(out, Hint{Source: sourceImage, Reason: "the image " + quote(c.Image) + " is " + word})
		return out
	}
	repo, _ := splitImage(c.Image)
	last := path.Base(repo)
	if strings.HasSuffix(last, "-exporter") {
		return out
	}
	for _, product := range goProducts {
		if last == product || strings.HasSuffix(last, "-"+product) || strings.HasPrefix(last, product+"-") {
			out = append(out, Hint{Source: sourceImage, Reason: "the image " + quote(c.Image) + " is " + product + ", written in Go"})
			break
		}
	}
	return out
}

// imageWord is whether the first word of the image's last path segment is
// one of names, a trailing version allowed (`python3-debian12`,
// `nodejs20-debian12`, `golang`): the word found. `node` alone is a word many
// other images carry (Calico's and kind's node images run no Node.js), so it
// is taken only from the official image or Bitnami's.
func imageWord(image string, names ...string) (string, bool) {
	repo, _ := splitImage(image)
	if repo == "" {
		return "", false
	}
	all := words(path.Base(repo))
	for _, word := range all {
		if notTheProduct[word] {
			return "", false
		}
	}
	first := all[0]
	bare := strings.TrimRight(first, "0123456789")
	for _, name := range names {
		if first != name && bare != name {
			continue
		}
		if name == "node" && !officialNode(repo) {
			return "", false
		}
		return name, true
	}
	return "", false
}

// officialNode is the Docker library's node image or Bitnami's, under any
// registry or mirror: `node`, `docker.io/library/node`, `bitnami/node`.
func officialNode(repo string) bool {
	repo = strings.TrimPrefix(repo, "docker.io/")
	return repo == "node" || repo == "library/node" || repo == "bitnami/node" ||
		strings.HasSuffix(repo, "/library/node") || strings.HasSuffix(repo, "/bitnami/node")
}
