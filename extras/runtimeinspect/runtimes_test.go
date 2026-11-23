package runtimeinspect

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func envNamed(names ...string) []corev1.EnvVar {
	out := make([]corev1.EnvVar, 0, len(names))
	for _, n := range names {
		out = append(out, corev1.EnvVar{Name: n, Value: "x"})
	}
	return out
}

// Each runtime is read from its command line, its environment and its image,
// and what does not say so is not read as it.
func TestEachRuntimeIsReadFromTheSpec(t *testing.T) {
	for _, tc := range []struct {
		name   string
		c      corev1.Container
		want   string
		source string
	}{
		// Java, as the JVM Inspector read it.
		{"java image", corev1.Container{Image: "eclipse-temurin:21-jre"}, runtimeJava, sourceImage},
		{"java command", corev1.Container{Image: "registry.example.com/app:1", Command: []string{"java", "-jar", "app.jar"}}, runtimeJava, sourceCommand},
		{"java env", corev1.Container{Image: "registry.example.com/app:1", Env: envNamed("JAVA_TOOL_OPTIONS")}, runtimeJava, sourceEnv},

		// Node.js.
		{"node command", corev1.Container{Image: "registry.example.com/web:1", Command: []string{"node", "server.js"}}, runtimeNode, sourceCommand},
		{"npm command", corev1.Container{Image: "registry.example.com/web:1", Args: []string{"npm", "start"}}, runtimeNode, sourceCommand},
		{"node script in sh -c", corev1.Container{Image: "registry.example.com/web:1", Command: []string{"sh", "-c", "exec /usr/local/bin/node dist/main.mjs"}}, runtimeNode, sourceCommand},
		{"node env", corev1.Container{Image: "registry.example.com/web:1", Env: envNamed("NODE_OPTIONS")}, runtimeNode, sourceEnv},
		{"node official image", corev1.Container{Image: "node:22-alpine"}, runtimeNode, sourceImage},
		{"node library image behind a mirror", corev1.Container{Image: "mirror.example.com/library/node:22"}, runtimeNode, sourceImage},
		{"distroless nodejs", corev1.Container{Image: "gcr.io/distroless/nodejs22-debian12"}, runtimeNode, sourceImage},

		// Python.
		{"python command", corev1.Container{Image: "registry.example.com/api:1", Command: []string{"python3", "-m", "app"}}, runtimePython, sourceCommand},
		{"versioned python", corev1.Container{Image: "registry.example.com/api:1", Command: []string{"/usr/bin/python3.12", "main.py"}}, runtimePython, sourceCommand},
		{"gunicorn", corev1.Container{Image: "registry.example.com/api:1", Args: []string{"gunicorn", "app:wsgi", "-w", "4"}}, runtimePython, sourceCommand},
		{"uvicorn", corev1.Container{Image: "registry.example.com/api:1", Command: []string{"uvicorn", "main:app"}}, runtimePython, sourceCommand},
		{"python env", corev1.Container{Image: "registry.example.com/api:1", Env: envNamed("PYTHONUNBUFFERED")}, runtimePython, sourceEnv},
		{"python image", corev1.Container{Image: "python:3.12-slim"}, runtimePython, sourceImage},
		{"distroless python", corev1.Container{Image: "gcr.io/distroless/python3-debian12"}, runtimePython, sourceImage},

		// Go.
		{"go env", corev1.Container{Image: "registry.example.com/svc:1", Env: envNamed("GOMEMLIMIT")}, runtimeGo, sourceEnv},
		{"go product", corev1.Container{Image: "quay.io/prometheus/prometheus:v3.0.0"}, runtimeGo, sourceImage},
		{"go product, prefixed", corev1.Container{Image: "registry.example.com/bitnami-etcd:3.5"}, runtimeGo, sourceImage},
		{"golang image", corev1.Container{Image: "golang:1.27"}, runtimeGo, sourceImage},

		// Nothing.
		{"nginx", corev1.Container{Image: "nginx:1.27", Args: []string{"-g", "daemon off;"}}, "", ""},
		{"calico's node image", corev1.Container{Image: "quay.io/calico/node:v3.29"}, "", ""},
		{"kind's node image", corev1.Container{Image: "kindest/node:v1.32.0"}, "", ""},
		{"node exporter", corev1.Container{Image: "quay.io/prometheus/node-exporter:v1.8"}, "", ""},
		{"a word that only starts like a program", corev1.Container{Image: "registry.example.com/x:1", Command: []string{"nodemon-like", "pythonic"}}, "", ""},
		{"a runtime word past the first", corev1.Container{Image: "registry.example.com/web-python:1"}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := detect(tc.c, nil)
			if got.runtime != tc.want {
				t.Fatalf("runtime = %q (%v), want %q", got.runtime, got.hints, tc.want)
			}
			if tc.want == "" {
				return
			}
			if !slices.ContainsFunc(got.hints, func(h Hint) bool { return h.Source == tc.source }) {
				t.Errorf("hints %v carry no %s hint", got.hints, tc.source)
			}
		})
	}
}

// A command line outranks an environment variable, which outranks an image:
// a Node.js command in a JDK image is Node.js.
func TestTheStrongestHintDecides(t *testing.T) {
	c := corev1.Container{Image: "eclipse-temurin:21", Command: []string{"node", "server.js"}}
	if got := detect(c, nil); got.runtime != runtimeNode {
		t.Errorf("command over image: %q", got.runtime)
	}
	c = corev1.Container{Image: "python:3.12", Env: envNamed("GOMAXPROCS")}
	if got := detect(c, nil); got.runtime != runtimeGo {
		t.Errorf("env over image: %q", got.runtime)
	}
	// Ties go to the providers' order: Java first.
	c = corev1.Container{Image: "registry.example.com/x:1", Env: envNamed("JAVA_HOME", "NODE_ENV")}
	if got := detect(c, nil); got.runtime != runtimeJava {
		t.Errorf("tie: %q", got.runtime)
	}
}

// The annotation wins over every hint, per container or for all of them;
// `none` takes a container out; an unknown runtime is ignored.
func TestTheAnnotationWins(t *testing.T) {
	app := corev1.Container{Name: "app", Image: "eclipse-temurin:21"}
	side := corev1.Container{Name: "side", Image: "registry.example.com/side:1"}
	containers := []corev1.Container{app, side}

	annotated := annotatedRuntimes(map[string]string{AnnotationRuntime: "app=python, side=go"}, containers)
	if got := detect(app, annotated); got.runtime != runtimePython || got.hints[0].Source != sourceAnnotation {
		t.Errorf("app = %+v", got)
	}
	if got := detect(side, annotated); got.runtime != runtimeGo {
		t.Errorf("side = %+v", got)
	}

	annotated = annotatedRuntimes(map[string]string{AnnotationRuntime: "node"}, containers)
	if detect(app, annotated).runtime != runtimeNode || detect(side, annotated).runtime != runtimeNode {
		t.Errorf("a bare runtime applies to every container: %v", annotated)
	}

	annotated = annotatedRuntimes(map[string]string{AnnotationRuntime: "app=none"}, containers)
	if got := detect(app, annotated); got.runtime != "" {
		t.Errorf("none = %+v", got)
	}

	annotated = annotatedRuntimes(map[string]string{AnnotationRuntime: "app=cobol,ruby"}, containers)
	if got := detect(app, annotated); got.runtime != runtimeJava {
		t.Errorf("an unknown runtime overrode the spec: %+v", got)
	}
}

// A mesh's proxy and an exporter are not the application, unless the
// annotation names them.
func TestSidecarsAreNotInspected(t *testing.T) {
	for _, c := range []corev1.Container{
		{Name: "istio-proxy", Image: "docker.io/istio/proxyv2:1.24", Env: envNamed("GOMAXPROCS")},
		{Name: "linkerd-proxy", Image: "cr.l5d.io/linkerd/proxy:stable"},
		{Name: "redis-exporter", Image: "oliver006/redis_exporter:v1.66", Env: envNamed("GOMAXPROCS")},
	} {
		if got := detect(c, nil); got.runtime != "" {
			t.Errorf("%s = %+v", c.Name, got)
		}
	}
	proxy := corev1.Container{Name: "istio-proxy", Image: "docker.io/istio/proxyv2:1.24"}
	annotated := annotatedRuntimes(map[string]string{AnnotationRuntime: "istio-proxy=go"}, []corev1.Container{proxy})
	if got := detect(proxy, annotated); got.runtime != runtimeGo {
		t.Errorf("the annotation did not reach a sidecar: %+v", got)
	}
}

// A pod reads every container, its native sidecars too, and lists each
// runtime once; an init container that runs to completion is not read.
func TestAPodListsItsRuntimes(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	p := pod("shop", "mixed",
		corev1.Container{Name: "api", Image: "eclipse-temurin:21-jre", Args: []string{"-Xmx512m"}},
		corev1.Container{Name: "worker", Image: "python:3.12"},
		corev1.Container{Name: "istio-proxy", Image: "docker.io/istio/proxyv2:1.24"},
		corev1.Container{Name: "api2", Image: "eclipse-temurin:21-jre"},
	)
	p.Spec.InitContainers = []corev1.Container{
		{Name: "migrate", Image: "python:3.12"},
		{Name: "agent", Image: "node:22", RestartPolicy: &always},
	}
	got := readPod(p)
	if !slices.Equal(got.Runtimes, []string{runtimeJava, runtimePython, runtimeNode}) {
		t.Errorf("runtimes = %v", got.Runtimes)
	}
	names := make([]string, 0, len(got.Containers))
	for _, c := range got.Containers {
		names = append(names, c.Name+"="+c.Runtime)
	}
	if want := []string{"api=java", "worker=python", "istio-proxy=", "api2=java", "agent=node"}; !slices.Equal(names, want) {
		t.Errorf("containers = %v, want %v", names, want)
	}
	if !got.Java || got.Containers[0].Heap == nil || got.Containers[1].Heap != nil {
		t.Errorf("the heap is read for Java alone: %+v", got.Containers[:2])
	}
}

// The column names every runtime, tones and links only Java, and still leaves
// a pod with no runtime without a cell.
func TestTheColumnNamesEveryRuntime(t *testing.T) {
	h := newHost(t,
		pod("shop", "web", corev1.Container{Name: "web", Image: "node:22", Command: []string{"node", "server.js"}}),
		pod("shop", "mixed",
			memory(corev1.Container{Name: "api", Image: "eclipse-temurin:21-jre", Args: []string{"-Xmx900m"}}, "", "1Gi"),
			corev1.Container{Name: "svc", Image: "registry.example.com/svc:1", Env: envNamed("GOMEMLIMIT")}),
		pod("shop", "static", corev1.Container{Name: "nginx", Image: "nginx:1.27"}),
	)
	got := column(t, h, "pods")
	if len(got) != 2 {
		t.Fatalf("cells = %v", got)
	}
	web := got["shop/web"]
	if web.Text != "Node.js" || web.Tone != "" || web.Href != "" || !strings.Contains(web.Hint, "web: Node.js (the command line starts") {
		t.Errorf("web = %+v", web)
	}
	mixed := got["shop/mixed"]
	if mixed.Text != "Java · Go" || mixed.Tone != "warn" || mixed.Href == "" || !strings.Contains(mixed.Hint, "svc: Go (GOMEMLIMIT") {
		t.Errorf("mixed = %+v", mixed)
	}
}

// The inventory, the heap page, stays Java's: a Go pod is not on it.
func TestTheInventoryStaysJava(t *testing.T) {
	h := newHost(t,
		pod("shop", "web", corev1.Container{Name: "web", Image: "node:22"}),
		pod("shop", "api", corev1.Container{Name: "api", Image: "eclipse-temurin:21-jre"}),
	)
	rec := serve(t, h, "/x/kind/runtime/")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	got := decode[inventoryAnswer](t, rec)
	if len(got.Pods) != 1 || got.Pods[0].Name != "api" || got.Pods[0].Containers[0].Runtime != runtimeJava {
		t.Errorf("pods = %+v", got.Pods)
	}
}

// The banner names the runtimes it found.
func TestTheSuggestionNamesTheRuntimes(t *testing.T) {
	h := newHost(t,
		pod("shop", "web", corev1.Container{Name: "web", Image: "node:22"}),
		pod("data", "api", corev1.Container{Name: "api", Image: "eclipse-temurin:21-jre"}),
	)
	got, err := New().Suggest(httptest.NewRequest("GET", "/", nil), h, "kind")
	if err != nil || got != "2 probable Java · Node.js pods in 2 namespaces" {
		t.Errorf("suggest = %q, %v", got, err)
	}
}
