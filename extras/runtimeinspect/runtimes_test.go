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

		// A runtime is read from the program a command line starts, never
		// from a word among its arguments.
		{"node through env and its variables", corev1.Container{Image: "registry.example.com/web:1", Command: []string{"env", "NODE_ENV=production", "node", "server.js"}}, runtimeNode, sourceCommand},
		{"node chained in sh -ec", corev1.Container{Image: "registry.example.com/web:1", Command: []string{"sh", "-ec", "npm ci && exec node dist/main.mjs"}}, runtimeNode, sourceCommand},
		{"node with a script, in args", corev1.Container{Image: "registry.example.com/web:1", Args: []string{"node", "server.js"}}, runtimeNode, sourceCommand},
		{"python -m, in args", corev1.Container{Image: "registry.example.com/api:1", Args: []string{"python", "-m", "app"}}, runtimePython, sourceCommand},
		{"the EBS CSI driver's node subcommand", corev1.Container{Image: "public.ecr.aws/ebs-csi-driver/aws-ebs-csi-driver:v1.37.0", Args: []string{"node", "--endpoint=$(CSI_ENDPOINT)", "--v=2"}, Env: envNamed("CSI_ENDPOINT")}, "", ""},
		{"kubectl labelling a node", corev1.Container{Image: "bitnami/kubectl:1.31", Command: []string{"sh", "-c", "kubectl label node $NODE_NAME role=worker --overwrite"}}, "", ""},
		{"a url ending in node.js", corev1.Container{Image: "quay.io/prometheus/prometheus:v3.0.0", Args: []string{"--web.external-url=http://x/node.js"}}, runtimeGo, sourceImage},
		{"python paths among the arguments", corev1.Container{Image: "registry.example.com/x:1", Command: []string{"/app/server", "--venv", "/opt/python", "/usr/lib/python3"}}, "", ""},

		// Java, as the JVM Inspector read it: no other runtime's hint takes a
		// container with a Java hint, but its own interpreter as the program.
		{"hadoop's yarn", corev1.Container{Image: "apache/hadoop:3.4.1", Command: []string{"yarn", "resourcemanager"}, Env: envNamed("JAVA_HOME")}, runtimeJava, sourceEnv},
		{"a jvm property naming a script", corev1.Container{Image: "eclipse-temurin:21-jre", Args: []string{"-Dscript=/etc/init.js"}}, runtimeJava, sourceImage},
		{"spark running a python application", corev1.Container{Image: "apache/spark:3.5.3", Env: envNamed("JAVA_HOME"), Args: []string{"local:///opt/app.py"}}, runtimeJava, sourceEnv},
		{"a go variable in a jdk image", corev1.Container{Image: "eclipse-temurin:21-jre", Env: envNamed("GOMAXPROCS")}, runtimeJava, sourceImage},

		// A program that runs another runtime takes the container from Java;
		// one this page does not read leaves it unread.
		{"uvicorn with JAVA_HOME", corev1.Container{Image: "registry.example.com/api:1", Command: []string{"uvicorn", "main:app"}, Env: envNamed("JAVA_HOME")}, runtimePython, sourceCommand},
		{"gunicorn in a jdk image", corev1.Container{Image: "eclipse-temurin:21-jre", Args: []string{"gunicorn", "app:app"}}, runtimePython, sourceCommand},
		{"npm start with JAVA_HOME", corev1.Container{Image: "registry.example.com/web:1", Command: []string{"npm", "start"}, Env: envNamed("JAVA_HOME")}, runtimeNode, sourceCommand},
		{"npx with JAVA_HOME", corev1.Container{Image: "registry.example.com/web:1", Command: []string{"npx", "serve"}, Env: envNamed("JAVA_HOME")}, runtimeNode, sourceCommand},
		{"pnpm with JAVA_HOME", corev1.Container{Image: "registry.example.com/web:1", Command: []string{"pnpm", "start"}, Env: envNamed("JAVA_HOME")}, runtimeNode, sourceCommand},
		{"bun with JAVA_HOME", corev1.Container{Image: "registry.example.com/web:1", Command: []string{"bun", "run", "start"}, Env: envNamed("JAVA_HOME")}, "", ""},
		{"deno with JAVA_HOME", corev1.Container{Image: "registry.example.com/web:1", Command: []string{"deno", "run", "main.ts"}, Env: envNamed("JAVA_HOME")}, "", ""},

		// Where a program starts.
		{"bash -o pipefail -c", corev1.Container{Image: "registry.example.com/api:1", Command: []string{"bash", "-o", "pipefail", "-c", "python app.py"}}, runtimePython, sourceCommand},
		{"bash -eo pipefail -c", corev1.Container{Image: "registry.example.com/web:1", Command: []string{"bash", "-eo", "pipefail", "-c", "node server.js"}}, runtimeNode, sourceCommand},
		{"bash --rcfile -c", corev1.Container{Image: "registry.example.com/api:1", Command: []string{"bash", "--rcfile", "/etc/app.rc", "-c", "python app.py"}}, runtimePython, sourceCommand},
		{"tini --", corev1.Container{Image: "registry.example.com/web:1", Command: []string{"tini", "--", "node", "server.js"}}, runtimeNode, sourceCommand},
		{"dumb-init", corev1.Container{Image: "registry.example.com/web:1", Command: []string{"dumb-init", "node", "server.js"}}, runtimeNode, sourceCommand},
		{"after &&", corev1.Container{Image: "registry.example.com/web:1", Command: []string{"sh", "-c", "cd /app && node server.js"}}, runtimeNode, sourceCommand},
		{"after a glued ;", corev1.Container{Image: "registry.example.com/web:1", Command: []string{"sh", "-c", "cd /app; node server.js"}}, runtimeNode, sourceCommand},
		{"after a lone ;", corev1.Container{Image: "registry.example.com/web:1", Command: []string{"sh", "-c", "cd /app ; node server.js"}}, runtimeNode, sourceCommand},
		// The program that decides is the last of a chain, or the one exec'd,
		// never one that only prepares or reads a pipe (#157).
		{"npx before the java start script", corev1.Container{Image: "registry.example.com/api:1", Command: []string{"sh", "-c", "npx wait-on tcp:db:5432 && /app/bin/start"}, Env: envNamed("JAVA_HOME")}, runtimeJava, sourceEnv},
		{"deno warm-up before the java start script", corev1.Container{Image: "registry.example.com/api:1", Command: []string{"sh", "-c", "deno run warmup.ts && /app/bin/start"}, Env: envNamed("JAVA_HOME")}, runtimeJava, sourceEnv},
		{"npm ci then java", corev1.Container{Image: "registry.example.com/api:1", Command: []string{"sh", "-c", "npm ci; java -jar app.jar"}}, runtimeJava, sourceCommand},
		{"exec'd node before a trailing command", corev1.Container{Image: "registry.example.com/web:1", Command: []string{"sh", "-c", "cd /app && exec node server.js; echo never"}}, runtimeNode, sourceCommand},
		{"node piped to tee", corev1.Container{Image: "registry.example.com/web:1", Command: []string{"sh", "-c", "node server.js | tee /var/log/app.log"}}, runtimeNode, sourceCommand},
		{"npm last after a java warm-up", corev1.Container{Image: "registry.example.com/web:1", Command: []string{"sh", "-c", "java -version && npm start"}, Env: envNamed("JAVA_HOME")}, runtimeNode, sourceCommand},
		{"bash -ceo pipefail", corev1.Container{Image: "registry.example.com/api:1", Command: []string{"bash", "-ceo", "pipefail", "python app.py"}}, runtimePython, sourceCommand},
		{"bash -ec", corev1.Container{Image: "registry.example.com/web:1", Command: []string{"bash", "-ec", "node server.js"}}, runtimeNode, sourceCommand},
		{"bash -c -e", corev1.Container{Image: "registry.example.com/web:1", Command: []string{"bash", "-c", "-e", "node server.js"}}, runtimeNode, sourceCommand},
		{"a shell running a script file", corev1.Container{Image: "registry.example.com/x:1", Command: []string{"bash", "/start.sh", "node"}}, "", ""},
		{"a flag where the entrypoint's arguments start", corev1.Container{Image: "registry.example.com/x:1", Args: []string{"--config=/etc/app.js"}}, "", ""},

		// Go products.
		{"grafana's image renderer", corev1.Container{Image: "grafana/grafana-image-renderer:3.11.0"}, "", ""},
		{"grafana oss", corev1.Container{Image: "grafana/grafana-oss:11.3.0"}, runtimeGo, sourceImage},
		{"an exporter named after a go product", corev1.Container{Image: "prom/consul-exporter:v0.12.1"}, "", ""},
		{"a product's name inside another word", corev1.Container{Image: "registry.example.com/etcdkeeper:0.7"}, "", ""},
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

	annotated = annotatedRuntimes(map[string]string{AnnotationRuntime: "app=python,go"}, containers)
	if detect(app, annotated).runtime != runtimePython || detect(side, annotated).runtime != runtimeGo {
		t.Errorf("a bare runtime overrode a named one: %v", annotated)
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

// A bare annotation does not reach a mesh's proxy; one naming it does.
func TestABareAnnotationLeavesTheSidecarsOut(t *testing.T) {
	app := corev1.Container{Name: "app", Image: "registry.example.com/app:1"}
	proxy := corev1.Container{Name: "istio-proxy", Image: "docker.io/istio/proxyv2:1.24.0"}
	containers := []corev1.Container{app, proxy}

	annotated := annotatedRuntimes(map[string]string{AnnotationRuntime: "java"}, containers)
	if got := detect(app, annotated); got.runtime != runtimeJava {
		t.Errorf("app = %+v", got)
	}
	if got := detect(proxy, annotated); got.runtime != "" {
		t.Errorf("a bare annotation reached istio-proxy: %+v", got)
	}

	annotated = annotatedRuntimes(map[string]string{AnnotationRuntime: "istio-proxy=java"}, containers)
	if got := detect(proxy, annotated); got.runtime != runtimeJava {
		t.Errorf("an annotation naming istio-proxy did not reach it: %+v", got)
	}
}

// Every cluster runs coredns, etcd or Prometheus: an image's product name
// alone is no reason for the banner, a Go variable is.
func TestTheSuggestionIgnoresGoProductsAlone(t *testing.T) {
	system := []*corev1.Pod{
		pod("kube-system", "coredns", corev1.Container{Name: "coredns", Image: "registry.k8s.io/coredns/coredns:v1.11.3"}),
		pod("kube-system", "etcd", corev1.Container{Name: "etcd", Image: "registry.k8s.io/etcd:3.5.16-0"}),
		pod("monitoring", "prometheus", corev1.Container{Name: "prometheus", Image: "quay.io/prometheus/prometheus:v3.0.0"}),
	}
	r := httptest.NewRequest("GET", "/", nil)
	if got, err := New().Suggest(r, newHost(t, system...), "kind"); err != nil || got != "" {
		t.Errorf("suggest on a bare cluster = %q, %v", got, err)
	}
	app := pod("shop", "svc", corev1.Container{Name: "svc", Image: "registry.example.com/svc:1", Env: envNamed("GOMEMLIMIT")})
	if got, err := New().Suggest(r, newHost(t, append(system, app)...), "kind"); err != nil || got != "1 probable Go pod in 1 namespace" {
		t.Errorf("suggest with a Go application = %q, %v", got, err)
	}
}

// The banner names the runtimes of the containers it counts: a Go container
// read from its image's product alone, beside a Java one, is not named.
func TestTheSuggestionNamesOnlyTheRuntimesItCounts(t *testing.T) {
	h := newHost(t, pod("data", "api",
		corev1.Container{Name: "api", Image: "eclipse-temurin:21-jre"},
		corev1.Container{Name: "etcd", Image: "registry.k8s.io/etcd:3.5.16-0"},
	))
	got, err := New().Suggest(httptest.NewRequest("GET", "/", nil), h, "kind")
	if err != nil || got != "1 probable Java pod in 1 namespace" {
		t.Errorf("suggest = %q, %v", got, err)
	}
}
