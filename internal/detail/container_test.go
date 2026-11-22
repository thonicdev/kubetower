package detail

import (
	"strings"
	"testing"
)

const podWithEverything = `
apiVersion: v1
kind: Pod
metadata:
  name: api-0
  namespace: shop
spec:
  initContainers:
    - name: migrate
      image: registry.example.com/migrate:2.1
      imagePullPolicy: Always
      env:
        - name: DSN
          valueFrom:
            secretKeyRef:
              name: db-credentials
              key: dsn
  containers:
    - name: api
      image: registry.example.com/api:1.25
      resources:
        requests:
          cpu: 100m
          memory: 256Mi
          ephemeral-storage: 1Gi
        limits:
          memory: 512Mi
          nvidia.com/gpu: "1"
      env:
        - name: LOG_LEVEL
          value: debug
        - name: NODE
          valueFrom:
            fieldRef:
              fieldPath: spec.nodeName
        - name: BUDGET
          valueFrom:
            resourceFieldRef:
              containerName: api
              resource: limits.memory
        - name: TUNING
          valueFrom:
            configMapKeyRef:
              name: api-config
              key: tuning
      envFrom:
        - secretRef:
            name: api-secrets
          prefix: APP_
    - name: sidecar
      image: envoy:1.31
status: {}
`

// The panel exists to save reading a hundred lines of YAML, so it has to carry
// the four things the YAML is usually opened for.
func TestAPodReportsWhatItIsMadeOf(t *testing.T) {
	got := containersOf(nodeFrom(t, podWithEverything), "Pod")

	if len(got) != 3 {
		t.Fatalf("%d containers, want 3 (one init, two app)", len(got))
	}
	// Init first, and marked. An init container that will not start is why the
	// pod is stuck, and unmarked it is indistinguishable from a crashing app.
	if got[0].Name != "migrate" || !got[0].Init {
		t.Errorf("the first container is %q (init=%v); the init container must come first and "+
			"say so", got[0].Name, got[0].Init)
	}
	if got[1].Init || got[2].Init {
		t.Error("an app container is marked as an init container")
	}
	if got[0].PullPolicy != "Always" {
		t.Errorf("pull policy %q, want Always", got[0].PullPolicy)
	}
	if got[1].Image != "registry.example.com/api:1.25" {
		t.Errorf("image %q", got[1].Image)
	}

	// cpu and memory first whatever the map order: they are what a person looks
	// for, and ephemeral-storage sorting above them because it starts with an
	// `e` makes the two that matter harder to find.
	if len(got[1].Requests) != 3 || got[1].Requests[0].Name != "cpu" || got[1].Requests[1].Name != "memory" {
		t.Errorf("requests came out as %+v", got[1].Requests)
	}
	// And a resource this console has never heard of is still reported.
	var gpu bool
	for _, a := range got[1].Limits {
		if a.Name == "nvidia.com/gpu" && a.Value == "1" {
			gpu = true
		}
	}
	if !gpu {
		t.Errorf("a vendor resource was dropped from the limits: %+v", got[1].Limits)
	}

	// `envFrom` is parsed onto the container that declares it, prefix and
	// `optional` included. Resolving it into key names is envfrom.go's job and
	// costs a round trip; parsing it does not, and the two must stay apart.
	whole := got[1].EnvFrom
	if len(whole) != 1 || whole[0].Kind != "Secret" || whole[0].Name != "api-secrets" || whole[0].Prefix != "APP_" {
		t.Errorf("envFrom came out as %+v", whole)
	}
	if len(got[1].Imported) != 0 {
		t.Errorf("containersOf invented %d imported variable(s) without asking a cluster "+
			"anything: %+v", len(got[1].Imported), got[1].Imported)
	}
}

// A pointer is named and never followed.
//
// Resolving a `secretKeyRef` would mean reading the Secret and putting a
// credential on a panel that opens by default — which nothing else in this
// console does. The Secret panel makes you ask for each value one at a time,
// and this keeps that.
func TestASecretReferenceIsNamedAndNeverRead(t *testing.T) {
	got := containersOf(nodeFrom(t, podWithEverything), "Pod")

	var dsn EnvVar
	for _, e := range got[0].Env {
		if e.Name == "DSN" {
			dsn = e
		}
	}
	if dsn.Name == "" {
		t.Fatal("the init container's env was not read at all")
	}
	if dsn.Value != "" {
		t.Errorf("a Secret-backed variable carries a value (%q). Nothing here reads a Secret, "+
			"so this can only be an invention", dsn.Value)
	}
	if !dsn.Secret {
		t.Error("the variable is not marked as coming from a Secret, so the panel cannot say " +
			"why it is showing no value and will look as though it failed")
	}
	if dsn.Source == nil {
		t.Fatal("the source is not addressable, so the reveal control has nothing to ask for")
	}
	if dsn.Source.Kind != "Secret" || dsn.Source.Name != "db-credentials" || dsn.Source.Key != "dsn" {
		t.Errorf("the source came out as %+v; it must name the object and the key, which are "+
			"not secret and are what someone would go and look at", *dsn.Source)
	}
}

// The other three shapes of valueFrom, and a literal.
func TestEveryValueSourceIsDescribed(t *testing.T) {
	got := containersOf(nodeFrom(t, podWithEverything), "Pod")
	byName := map[string]EnvVar{}
	for _, e := range got[1].Env {
		byName[e.Name] = e
	}

	if v := byName["LOG_LEVEL"]; v.Value != "debug" || v.From != "" {
		t.Errorf("a literal came out as %+v; it is in the pod spec and already on the Manifest "+
			"tab, so hiding it here would be theatre", v)
	}
	if v := byName["NODE"]; !strings.Contains(v.From, "spec.nodeName") || v.Secret {
		t.Errorf("a fieldRef came out as %+v", v)
	}
	if v := byName["BUDGET"]; !strings.Contains(v.From, "limits.memory") {
		t.Errorf("a resourceFieldRef came out as %+v", v)
	}
	if v := byName["TUNING"]; v.Source == nil || v.Source.Name != "api-config" ||
		v.Source.Kind != "ConfigMap" || v.Secret {
		t.Errorf("a configMapKeyRef came out as %+v — and it is not a Secret", v)
	}
}

// Nothing else grows containers — including something that happens to carry the
// same field.
//
// The first two cases below prove nothing on their own: a Node has no
// `spec.containers`, so it comes back empty whether the kind is checked or not.
// The one that matters is a CRD whose own schema uses that name, which several
// operators' do. Without the kind check its `spec.containers` would be read as a
// pod's and the drawer would grow a Containers section describing something else
// entirely — confidently, and in the right shape.
func TestOnlyAPodReportsContainers(t *testing.T) {
	if got := containersOf(nodeFrom(t, controlPlaneNodeDoc), "Node"); got != nil {
		t.Errorf("a Node reported %d containers", len(got))
	}
	if got := containersOf(nodeFrom(t, configMapDoc), "ConfigMap"); got != nil {
		t.Errorf("a ConfigMap reported %d containers", len(got))
	}

	const crdWithContainers = `
apiVersion: acme.example.com/v1
kind: Pipeline
metadata:
  name: nightly
spec:
  containers:
    - name: step-one
      image: busybox:1.37
`
	got := containersOf(nodeFrom(t, crdWithContainers), "Pipeline")
	if got != nil {
		t.Errorf("a Pipeline reported %d containers. `spec.containers` is a name several "+
			"operators use for their own thing; read as a pod's it draws a section that is "+
			"confidently about something else", len(got))
	}
}

// A `configMapKeyRef` carries its own `optional`, read from the ref and not from
// the entry beside it.
//
// The same trap `wholeOf` has a comment about, one level down and easy to read
// the other way round from the YAML: `optional` sits inside `configMapKeyRef`,
// beside `name` and `key`. Getting it wrong turns "this pod will not start until
// the key exists" into "this is fine", which is the sentence the panel prints
// when the ConfigMap or the key is missing.
func TestAKeyReferenceCarriesItsOwnOptionalFlag(t *testing.T) {
	const pod = `
apiVersion: v1
kind: Pod
metadata:
  name: api-0
  namespace: shop
spec:
  containers:
    - name: api
      image: api:1
      env:
        - name: REQUIRED
          valueFrom:
            configMapKeyRef:
              name: api-config
              key: must
        - name: OPTIONAL
          valueFrom:
            configMapKeyRef:
              name: api-config
              key: maybe
              optional: true
        - name: MAYBE_SECRET
          valueFrom:
            secretKeyRef:
              name: api-secrets
              key: maybe
              optional: true
`
	got := containersOf(nodeFrom(t, pod), "Pod")
	if len(got) != 1 || len(got[0].Env) != 3 {
		t.Fatalf("the pod parsed as %+v", got)
	}
	for i, want := range []bool{false, true, true} {
		source := got[0].Env[i].Source
		if source == nil {
			t.Fatalf("%s has no source", got[0].Env[i].Name)
		}
		if source.Optional != want {
			t.Errorf("%s has optional=%v, want %v. It is the difference between a pod that "+
				"will not start and a pod that starts without the variable, and it is the "+
				"sentence the panel prints for a missing key",
				got[0].Env[i].Name, source.Optional, want)
		}
	}
}

// What a container is started with, exactly as the spec
// writes it - one element per argument, a script whole with its newlines, and
// nothing at all where the spec sets nothing, because that is the image's own
// entrypoint running and only the registry knows what it is.
func TestTheCommandAndArgumentsArriveAsTheSpecWritesThem(t *testing.T) {
	got := containersOf(nodeFrom(t, `
apiVersion: v1
kind: Pod
metadata:
  name: web-0
spec:
  initContainers:
    - name: render
      image: busybox:1.36
      command: ["/bin/sh", "-ec"]
      args:
        - |
          echo "rendering 'config'"
          cp /tmpl/* /out/
  containers:
    - name: web
      image: nginx:1.27
      command: ["nginx", "-g", "daemon off;"]
      args: ["--name", "a b", ""]
    - name: plain
      image: envoy:1.31
    - name: emptied
      image: envoy:1.31
      command: []
      args: []
    - name: odd
      image: envoy:1.31
      args: ["--port", 8080]
status: {}
`), "Pod")

	if len(got) != 5 {
		t.Fatalf("%d containers, want 5", len(got))
	}
	render, web, plain, emptied, odd := got[0], got[1], got[2], got[3], got[4]

	if len(render.Command) != 2 || render.Command[0] != "/bin/sh" || render.Command[1] != "-ec" {
		t.Errorf("the init command came out as %q", render.Command)
	}
	want := "echo \"rendering 'config'\"\ncp /tmpl/* /out/\n"
	if len(render.Args) != 1 || render.Args[0] != want {
		t.Errorf("the script came out as %q, want %q whole", render.Args, want)
	}

	// The space inside "a b" and the empty last argument are both arguments.
	if strings.Join(web.Command, "|") != "nginx|-g|daemon off;" {
		t.Errorf("the command came out as %q", web.Command)
	}
	if len(web.Args) != 3 || web.Args[1] != "a b" || web.Args[2] != "" {
		t.Errorf("the args came out as %q", web.Args)
	}

	// Neither set, and set to an empty list: the same thing to the kubelet.
	for _, c := range []Container{plain, emptied} {
		if c.Command != nil || c.Args != nil {
			t.Errorf("%s: command %q args %q, want both nil (the image's entrypoint runs)",
				c.Name, c.Command, c.Args)
		}
	}

	// A number where a string belongs is printed, not dropped: an argv missing
	// one argument is another command.
	if len(odd.Args) != 2 || odd.Args[1] != "8080" {
		t.Errorf("a non-string argument was lost: %q", odd.Args)
	}
}
