package detail

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// What a pod is made of: images, pull policy, resources, command and
// environment, without scrolling the YAML. This file only parses the pod in
// hand, so every function is pure; following an `envFrom` is a cluster read
// and lives in envfrom.go behind a cap.

// Container is one container of a pod, init or otherwise.
type Container struct {
	Name  string
	Image string
	// Init marks an init container: a stuck one is why the pod is stuck, and
	// looks like a crashing app container otherwise.
	Init bool
	// PullPolicy is carried only when it is not the default for the tag: `Always`
	// on a pinned tag costs a registry round trip per start, `IfNotPresent` on
	// `:latest` keeps whatever the node pulled first.
	PullPolicy string
	// Command and Args are the container's `command` and `args` as the spec writes
	// them, one element per argument, never joined or split (`sh -c "a b"` is two
	// elements). Both nil means the image's ENTRYPOINT and CMD run; Command nil
	// with Args set is the ENTRYPOINT given these arguments in place of its CMD.
	Command []string
	Args    []string
	// Requests and Limits, formatted, named as in the spec. Empty means the
	// container asked for nothing, which decides scheduling and the QoS class.
	Requests []Amount
	Limits   []Amount
	// Env is what the container was told in its own `env`, in declaration order.
	Env []EnvVar
	// EnvFrom is this container's whole-object imports, in declaration order,
	// each carrying what came of resolving it (envfrom.go).
	EnvFrom []Whole
	// Imported is the variables those imports contribute, one row per key, named
	// `PREFIX_KEY` as the container sees them. Kept apart from Env: Env is written
	// in the pod spec, Imported was learned by reading another object.
	Imported []EnvVar

	// Probes is liveness, readiness and startup, in that order, always all
	// three: "no readiness probe" is a finding, an omitted entry is not (health.go).
	Probes []Probe
	// Status is what the kubelet last reported about this container, matched by
	// name. Never nil: an unreported container carries ContainerUnreported.
	Status *ContainerStatus
}

// Amount is one resource figure.
type Amount struct {
	Name  string // "cpu", "memory", "ephemeral-storage"
	Value string
}

// EnvSource is the ConfigMap or Secret one variable is read out of. Structured,
// because it is the address the reveal control asks the server for.
type EnvSource struct {
	Kind string // "ConfigMap" or "Secret", as a person writes it
	Name string
	Key  string
	// Optional is the reference's `optional: true`: without it the kubelet will
	// not start the container until the key exists; with it the variable is
	// simply not set. Not in the row's URL: the reveal fragment only asks a
	// cluster for one value, which this does not affect.
	Optional bool
}

// EnvVar is one environment entry.
// EnvVar is one environment entry.
//
// A literal value is in the pod spec and already on the Manifest tab; a source
// is a pointer to another object. A key name is not a secret, a value is, and
// the kind alone decides what is done with the value behind the pointer:
//
//   - A ConfigMap's value is resolved on the drawer open and drawn (Resolved),
//     within the read budget the imports already use.
//   - A Secret's value is never fetched or held here. The operator asks for one
//     key at a time through `Reader.Data`.
type EnvVar struct {
	Name string
	// Value is the literal, when there is one; never a resolved value, and never
	// a Secret's contents.
	Value string
	// From describes a source that cannot be followed: "field spec.nodeName",
	// "resource limits.memory of api". Empty when Source is filled.
	From string
	// Source is the ConfigMap or Secret behind this variable, or nil. Filled for
	// `valueFrom.secretKeyRef`, `valueFrom.configMapKeyRef`, and for every row an
	// `envFrom` contributes.
	Source *EnvSource
	// Secret marks a source that is a Secret, so the panel can say what asking
	// for the value costs before it is asked for.
	Secret bool

	// Resolved is the value behind a ConfigMap source, drawn without a click;
	// never filled from a Secret (`dataOf` gives it nothing to fill from). It is
	// the row's rendering, the first line shortened with Clipped saying so: a key
	// can hold a whole nginx.conf.
	Resolved string
	// Held reports that Resolved was read. Not `Resolved != ""`: a key holding
	// the empty string must not look like a key nobody read.
	Held bool
	// Clipped reports that Resolved is only part of the value, so the row keeps a
	// control for the rest.
	Clipped bool
	// Problem says why there is no value on this row, in words the operator can
	// act on, from the same functions as Whole.Problem. Empty when there is
	// nothing to say.
	Problem string
	// Absence is the kind of absence Problem describes, so the panel draws it
	// without parsing the sentence. One enumeration because the kinds are
	// exclusive.
	Absence Absence
}

// Absence is why a value is not on a row, as a kind rather than as a sentence.
//
// Not every absence is a warning: a healthy Argo CD server pod has dozens of
// unset `optional: true` references, and a warning color spent on the normal
// case is one the operator stops seeing.
type Absence string

const (
	// AbsenceNone: there is a value, or there is nothing to say.
	AbsenceNone Absence = ""
	// AbsenceUnread: the object behind the row was never read (past the budget,
	// refused, or missing on a non-optional reference). Asking again may answer
	// differently, so this is the one kind that keeps a control.
	AbsenceUnread Absence = "unread"
	// AbsenceUnset: the object was read and lacks the key, on a non-optional
	// reference. The kubelet will not start the container until it does, so it
	// is a warning.
	AbsenceUnset Absence = "unset"
	// AbsenceExpected: an absence on an `optional: true` reference, a missing
	// key or a missing object. The container runs without the variable, as
	// written, so it is drawn plainly with no pill, color or control. Problem
	// still tells the two cases apart in words.
	AbsenceExpected Absence = "expected"
	// AbsenceUnprintable: the value was read and is not text (a DER
	// certificate). Nothing is wrong with the pod.
	AbsenceUnprintable Absence = "unprintable"
)

// Whole is a whole-source import: `envFrom` naming a ConfigMap or a Secret,
// which contributes every key it holds under an optional prefix.
//
// The first four fields are read from the pod; Keys, Problem and Absence are
// filled by envfrom.go, so an object with no keys, one that may not be read
// and one nobody looked at do not look alike.
type Whole struct {
	Kind   string // "ConfigMap" or "Secret"
	Name   string
	Prefix string
	// Optional is the import's `optional: true`: with it a missing object still
	// lets the pod start, without it the pod does not start.
	Optional bool

	// Keys is how many variables this import contributed. Meaningful only when
	// Problem is empty.
	Keys int
	// Problem says why this import contributed nothing, in words the operator
	// can act on, or "" when it was read.
	Problem string
	// Absence is the kind of answer Problem is, as on EnvVar. Only AbsenceUnread
	// and AbsenceExpected occur here, and only beside a Problem; an optional import
	// of a missing ConfigMap is `expected`.
	Absence Absence
}

// containersOf reads the containers out of a pod, init containers first.
// containersOf reads the containers out of a pod, init containers first, in the
// order they run. Unstructured, like every kind this package reads through the
// dynamic client.
func containersOf(raw *unstructured.Unstructured, kind string) []Container {
	if !strings.EqualFold(kind, "Pod") {
		return nil
	}

	reported := statusesOf(raw)

	var out []Container
	for _, path := range []struct {
		field string
		init  bool
	}{{"initContainers", true}, {"containers", false}} {
		items, found, err := unstructured.NestedSlice(raw.Object, "spec", path.field)
		if err != nil || !found {
			continue
		}
		for _, item := range items {
			spec, ok := item.(map[string]any)
			if !ok {
				continue
			}
			c := containerOf(spec, path.init)
			if c.Name == "" {
				continue
			}
			// Both status lists are indexed by name whichever spec list the container
			// came from: a restartable sidecar is declared under `initContainers` and
			// reported under `initContainerStatuses` (health.go).
			c.Status = statusOf(reported[c.Name])
			out = append(out, c)
		}
	}
	return out
}

func containerOf(spec map[string]any, init bool) Container {
	c := Container{Init: init}
	c.Name, _, _ = unstructured.NestedString(spec, "name")
	c.Image, _, _ = unstructured.NestedString(spec, "image")
	c.PullPolicy, _, _ = unstructured.NestedString(spec, "imagePullPolicy")
	c.Command = argvOf(spec, "command")
	c.Args = argvOf(spec, "args")

	c.Requests = amountsOf(spec, "requests")
	c.Limits = amountsOf(spec, "limits")
	c.Env = envOf(spec)
	c.EnvFrom = wholeOf(spec)
	// Always all three, including when none is declared.
	c.Probes = probesOf(spec)
	return c
}

// argvOf reads `command` or `args` as the spec gives it, element by element.
// An empty list is nil: the kubelet treats `command: []` as a missing
// `command`. A non-string element (a manifest the API server would refuse) is
// kept as its printed value: dropping it would show a different command.
func argvOf(spec map[string]any, field string) []string {
	items, found, err := unstructured.NestedSlice(spec, field)
	if err != nil || !found || len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
			continue
		}
		out = append(out, fmt.Sprint(item))
	}
	return out
}

// amountsOf reads one half of `resources`, in a stable order.
// Ordered cpu, memory, ephemeral-storage first, the ones people look for.
func amountsOf(spec map[string]any, half string) []Amount {
	block, found, err := unstructured.NestedStringMap(spec, "resources", half)
	if err != nil || !found || len(block) == 0 {
		return nil
	}

	var out []Amount
	for _, name := range []string{"cpu", "memory", "ephemeral-storage"} {
		if v, ok := block[name]; ok {
			out = append(out, Amount{Name: name, Value: v})
			delete(block, name)
		}
	}
	// Anything else (GPUs, hugepages, vendor resources), in map order.
	for name, v := range block {
		out = append(out, Amount{Name: name, Value: v})
	}
	return out
}

func envOf(spec map[string]any) []EnvVar {
	items, found, err := unstructured.NestedSlice(spec, "env")
	if err != nil || !found {
		return nil
	}

	var out []EnvVar
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(entry, "name")
		if name == "" {
			continue
		}
		v := EnvVar{Name: name}
		if value, found, _ := unstructured.NestedString(entry, "value"); found {
			v.Value = value
			out = append(out, v)
			continue
		}
		v.From, v.Source = sourceOf(entry)
		v.Secret = v.Source != nil && v.Source.Kind == "Secret"
		out = append(out, v)
	}
	return out
}

// sourceOf describes a `valueFrom`, and does not follow it.
// A ConfigMap or Secret source has an address a reveal control can ask for; a
// fieldRef's value is decided by the kubelet and stored nowhere readable, so
// it is only described.
func sourceOf(entry map[string]any) (string, *EnvSource) {
	for _, shape := range []struct{ field, kind string }{
		{"secretKeyRef", "Secret"},
		{"configMapKeyRef", "ConfigMap"},
	} {
		ref, found, _ := unstructured.NestedMap(entry, "valueFrom", shape.field)
		if !found {
			continue
		}
		name, _, _ := unstructured.NestedString(ref, "name")
		key, _, _ := unstructured.NestedString(ref, "key")
		// `optional` sits on the ref: the difference between a pod that will not
		// start and one that starts without the variable.
		optional, _, _ := unstructured.NestedBool(ref, "optional")
		return "", &EnvSource{Kind: shape.kind, Name: name, Key: key, Optional: optional}
	}
	if ref, found, _ := unstructured.NestedMap(entry, "valueFrom", "fieldRef"); found {
		path, _, _ := unstructured.NestedString(ref, "fieldPath")
		return "field " + path, nil
	}
	if ref, found, _ := unstructured.NestedMap(entry, "valueFrom", "resourceFieldRef"); found {
		resource, _, _ := unstructured.NestedString(ref, "resource")
		container, _, _ := unstructured.NestedString(ref, "containerName")
		if container != "" {
			return fmt.Sprintf("resource %s of %s", resource, container), nil
		}
		return "resource " + resource, nil
	}
	// An unknown `valueFrom` shape: said so, since blank would read as "no value".
	return "a source this console does not recognize", nil
}

// wholeOf reads `envFrom`.
func wholeOf(spec map[string]any) []Whole {
	items, found, err := unstructured.NestedSlice(spec, "envFrom")
	if err != nil || !found {
		return nil
	}

	var out []Whole
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		prefix, _, _ := unstructured.NestedString(entry, "prefix")
		for _, shape := range []struct{ field, kind string }{
			{"configMapRef", "ConfigMap"},
			{"secretRef", "Secret"},
		} {
			ref, found, _ := unstructured.NestedMap(entry, shape.field)
			if !found {
				continue
			}
			name, _, _ := unstructured.NestedString(ref, "name")
			// `optional` sits on the ref, not on the entry beside `prefix`; reading
			// it from the wrong place turns "will not start" into "fine".
			optional, _, _ := unstructured.NestedBool(ref, "optional")
			out = append(out, Whole{Kind: shape.kind, Name: name, Prefix: prefix, Optional: optional})
		}
	}
	return out
}
