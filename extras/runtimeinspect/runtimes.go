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
	// Java is read as the JVM Inspector read it, and only a program that
	// runs another runtime, started as the program, takes a container with
	// a Java hint away from it (takesFromJava): Hadoop's `yarn`, a Go
	// variable or a word among the arguments does not. A runtime this page
	// does not read (Deno, Bun) leaves the container unread rather than Java.
	if java := hints(c); len(java) > 0 {
		switch taker := programTakingFromJava(c); {
		case taker == "":
			return detection{runtime: runtimeJava, hints: java}
		case otherRuntimes[taker]:
			return detection{}
		}
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
// runtime applies to each of them but the known sidecars, `name=runtime` to
// that one, sidecar or not.
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
					if _, set := out[c.Name]; !set && !isSidecar(c) {
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

// --- The program -------------------------------------------------------------

// A command line names a runtime by the program it starts, never by a word
// among its arguments: `kubectl label node x` starts kubectl, and
// `-Dscript=/etc/init.js` is a JVM's argument.

// program is one program a command line starts, with its arguments.
type program struct {
	words []string
	// afterEntrypoint is a program read from args with no command: the
	// image's own entrypoint, which the API does not carry, may take it as a
	// subcommand (the EBS CSI driver's `node`).
	afterEntrypoint bool
	// execd is a program the shell exec'd: it replaces the shell, and what
	// follows it never runs. orElse follows `||`: it runs only when the
	// program before it failed.
	execd, orElse bool
}

// shells run the script after their -c; wrappers run the program after them.
var (
	shells   = map[string]bool{"sh": true, "bash": true, "ash": true, "dash": true, "zsh": true}
	wrappers = map[string]bool{"exec": true, "env": true, "nohup": true, "tini": true, "dumb-init": true}
)

// programs are the programs c's command line starts: its first word, the
// first word of a shell's -c script and of each command the script chains
// (`&&`, `||`, `;`, `|`), each past exec, env and their variables.
func programs(c corev1.Container) []program {
	var out []program
	at := -1 // the program the next arguments belong to, if any
	start, wrapped, shell, optionArg := true, false, false, false
	// script: the shell's options held a c, so its first word that is not an
	// option, or the value of one, starts the script (`-ceo pipefail`).
	// execd and orElse describe the next program found.
	script, execd, orElse := false, false, false
	reset := func() {
		at, start, wrapped, shell, optionArg = -1, true, false, false, false
		script, execd, orElse = false, false, false
	}
	for _, token := range commandLine(c) {
		switch token {
		case "&&", "&", "|", "|&":
			reset()
			continue
		case "||":
			reset()
			orElse = true
			continue
		}
		// A `;`, alone or ending a word, ends the command.
		token, end := strings.CutSuffix(token, ";")
		switch {
		case token == "":
		case shell:
			switch {
			case optionArg:
				// The value of -o, -O or --rcfile: `bash -o pipefail -c …`.
				optionArg = false
			case token == "--rcfile" || token == "--init-file":
				optionArg = true
			case strings.HasPrefix(token, "--"):
			case isFlag(token) || strings.HasPrefix(token, "+"):
				// -c takes the first word left after every option, so a c
				// among them does not end them: `-ceo pipefail` gives
				// pipefail to -o (#157).
				if token[0] == '-' && strings.Contains(token, "c") {
					script = true
				}
				optionArg = strings.ContainsAny(token[1:], "oO")
			case script:
				// The script's first word: read as a program below.
				shell = false
			default:
				// The shell runs a script file: the program is the shell.
				shell, start = false, false
			}
			if shell || !start {
				break
			}
			fallthrough
		case start:
			switch base := path.Base(token); {
			case isFlag(token) && wrapped, isAssignment(token):
			case wrappers[base] || token == "--":
				wrapped = true
				execd = execd || base == "exec"
			case shells[base]:
				shell = true
			case isFlag(token):
				// An argument to an entrypoint the spec does not carry.
				start = false
			default:
				out = append(out, program{words: []string{token}, afterEntrypoint: len(out) == 0 && len(c.Command) == 0, execd: execd, orElse: orElse})
				at, start = len(out)-1, false
			}
		case at >= 0:
			out[at].words = append(out[at].words, token)
		}
		if end {
			reset()
		}
	}
	return out
}

func isFlag(token string) bool { return strings.HasPrefix(token, "-") }

// isAssignment is a shell's or env's `NAME=value` before a program.
func isAssignment(token string) bool {
	name, _, ok := strings.Cut(token, "=")
	if !ok || name == "" {
		return false
	}
	for i, r := range name {
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// isInterpreter is a runtime's own program: given nothing to run after an
// entrypoint, it is the entrypoint's subcommand (runsAScript).
func isInterpreter(base string) bool {
	return base == "node" || base == "nodejs" || isPythonInterpreter(base)
}

// otherRuntimes run a runtime this page does not read.
var otherRuntimes = map[string]bool{"deno": true, "bun": true}

// takesFromJava is a program that runs another runtime: its interpreter, a
// Python server, or a Node.js package manager. Not `yarn`, which is also
// Hadoop's YARN, started with JAVA_HOME set.
func takesFromJava(base string) bool {
	return isInterpreter(base) || pythonServers[base] || base == "npm" || base == "npx" || base == "pnpm" || otherRuntimes[base]
}

// runsAScript is whether an interpreter read after an entrypoint is given
// something to run: a script, or Python's -m or -c. `node --endpoint=…` is
// the EBS CSI driver's subcommand, not Node.js.
func runsAScript(p program) bool {
	for _, w := range p.words[1:] {
		if isNodeScript(w) || isPythonScript(w) || w == "-m" || w == "-c" {
			return true
		}
	}
	return false
}

// counts is whether p, whose program's base is base, may name a runtime:
// an interpreter read after an entrypoint must be given a script.
func counts(p program, base string) bool {
	return !p.afterEntrypoint || !isInterpreter(base) || runsAScript(p)
}

// plumbing are programs a command line runs around its application: they
// prepare (`cd`, `trap`), wait (`wait`, `sleep`, `tail -f`), say something
// (`echo`) or read a pipe (`tee`, `grep`), and never name its runtime.
var plumbing = map[string]bool{
	"cd": true, "trap": true, "set": true, "export": true, "ulimit": true, "umask": true, "source": true, ".": true,
	"wait": true, "sleep": true, "tail": true, "true": true, "false": true, ":": true, "test": true, "[": true,
	"echo": true, "printf": true, "date": true, "kill": true, "exit": true,
	"cat": true, "tee": true, "grep": true, "sed": true, "awk": true, "head": true, "cut": true, "tr": true,
	"sort": true, "uniq": true, "logger": true,
	"mkdir": true, "cp": true, "mv": true, "rm": true, "ln": true, "chmod": true, "chown": true, "touch": true,
}

// deciding is the program of c that names its runtime: the one a shell
// exec'd, which replaces it; otherwise the last that is not plumbing, and
// not the fallback after `||` while one before it qualifies.
// `npx wait-on … && /app/bin/start` is the start script's, not npx's;
// `node app.js | tee log`, `cat x | node`, `python app.py & wait` and
// `java -jar app.jar || echo failed` are the runtime's (#157).
func deciding(c corev1.Container) []program {
	all := programs(c)
	for _, p := range all {
		if p.execd {
			return []program{p}
		}
	}
	for _, fallback := range []bool{false, true} {
		for i := len(all) - 1; i >= 0; i-- {
			if !plumbing[path.Base(all[i].words[0])] && (fallback || !all[i].orElse) {
				return all[i : i+1]
			}
		}
	}
	return nil
}

// programHint is the command-line hint of c's deciding program, if
// isProgram knows it or it is a script isScript knows.
func programHint(c corev1.Container, isProgram func(base string) bool, isScript func(token string) bool) (Hint, bool) {
	for _, p := range deciding(c) {
		first := p.words[0]
		base := path.Base(first)
		switch {
		case isProgram(base) && counts(p, base):
			return Hint{Source: sourceCommand, Reason: "the command line starts " + quote(first)}, true
		case isScript(first):
			return Hint{Source: sourceCommand, Reason: "the command line runs a script: " + quote(clip(first))}, true
		}
	}
	return Hint{}, false
}

// programTakingFromJava is the base of c's deciding program when it takes c
// from Java (takesFromJava), or "".
func programTakingFromJava(c corev1.Container) string {
	for _, p := range deciding(c) {
		if base := path.Base(p.words[0]); takesFromJava(base) && counts(p, base) {
			return base
		}
	}
	return ""
}

// --- Node.js -----------------------------------------------------------------

// nodePrograms start Node.js or a package manager that runs it.
var nodePrograms = map[string]bool{"node": true, "nodejs": true, "npm": true, "npx": true, "yarn": true, "pnpm": true}

func isNodeScript(token string) bool {
	return strings.HasSuffix(token, ".js") || strings.HasSuffix(token, ".mjs") || strings.HasSuffix(token, ".cjs")
}

func nodeHints(c corev1.Container) []Hint {
	var out []Hint
	if h, ok := programHint(c, func(base string) bool { return nodePrograms[base] }, isNodeScript); ok {
		out = append(out, h)
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

// isPythonInterpreter is Python itself: `python`, `python3`, `python3.12`,
// `pypy`.
func isPythonInterpreter(base string) bool {
	if base == "python" || base == "python3" || base == "pypy" || base == "pypy3" {
		return true
	}
	rest, ok := strings.CutPrefix(base, "python3.")
	return ok && rest != "" && strings.Trim(rest, "0123456789") == ""
}

func isPythonProgram(base string) bool { return isPythonInterpreter(base) || pythonServers[base] }

func isPythonScript(token string) bool { return strings.HasSuffix(token, ".py") }

func pythonHints(c corev1.Container) []Hint {
	var out []Hint
	if h, ok := programHint(c, isPythonProgram, isPythonScript); ok {
		out = append(out, h)
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

// notGo are images named after a Go product that are not written in Go:
// Grafana's image renderer runs on Node.js.
var notGo = map[string]bool{"grafana-image-renderer": true}

// productOnly is whether a Go reading rests on the image's product name
// alone, which every cluster's coredns, etcd and Prometheus give: not a
// reason to offer the inspector (Suggest).
func productOnly(c Container) bool {
	if c.Runtime != runtimeGo || len(c.Hints) == 0 {
		return false
	}
	for _, h := range c.Hints {
		if h.Source != sourceImage {
			return false
		}
	}
	return true
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
	if strings.HasSuffix(last, "-exporter") || notGo[last] {
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
