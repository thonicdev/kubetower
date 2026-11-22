// Package drift compares what Argo CD last applied to an object with the
// object as it is now.
//
// A client-side apply writes the manifest it sent to
// `kubectl.kubernetes.io/last-applied-configuration`; comparing it with the
// live object shows what changed since the last sync (a `kubectl scale`, an
// edit, a controller). A change still only in git is in neither, so it cannot
// show here.
//
// The walk covers only the applied manifest, as `kubectl apply`'s three-way
// merge does: fields the API server defaulted, or that Argo CD did not set,
// are not drift.
package drift

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

// LastApplied is the annotation a client-side apply writes.
const LastApplied = "kubectl.kubernetes.io/last-applied-configuration"

// Masked replaces every value on a Secret: the paths show, the values do not,
// as the console's own manifest masks them.
const Masked = "<masked>"

// Change is what happened to one field.
type Change string

const (
	// Changed: the field is live, with another value.
	Changed Change = "changed"
	// Missing: Argo CD set it, and it is gone.
	Missing Change = "missing"
	// Added: an item the live list has and the applied list did not, only on
	// a list an apply replaces whole.
	Added Change = "added"
)

// Field is one gap.
type Field struct {
	Path    string `json:"path"`
	Applied string `json:"applied,omitempty"`
	Live    string `json:"live,omitempty"`
	Change  Change `json:"change"`
}

// Options are how one object is compared.
type Options struct {
	// Mask hides every value, keeping the paths: a Secret's.
	Mask bool
}

// Compare walks applied and reports, in path order, every field whose live
// value is not the applied one.
func Compare(applied, live map[string]any, opts Options) []Field {
	// Never nil: the page reads the length, so an empty result must be `[]`.
	c := comparer{opts: opts, out: []Field{}}
	// Round-trip the live object through JSON so both sides hold JSON's types
	// (unstructured holds int64 where the annotation decodes float64).
	c.walk("", trimmed(applied), normalized(live))
	sort.SliceStable(c.out, func(i, j int) bool { return c.out[i].Path < c.out[j].Path })
	return c.out
}

type comparer struct {
	opts Options
	out  []Field
}

// trimmed drops what is never drift: the status, and the annotation that is
// the applied manifest itself.
func trimmed(applied map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range applied {
		if key == "status" {
			continue
		}
		out[key] = value
	}
	if meta, ok := out["metadata"].(map[string]any); ok {
		copied := map[string]any{}
		for key, value := range meta {
			copied[key] = value
		}
		if annotations, ok := copied["annotations"].(map[string]any); ok {
			kept := map[string]any{}
			for key, value := range annotations {
				if key != LastApplied {
					kept[key] = value
				}
			}
			copied["annotations"] = kept
		}
		out["metadata"] = copied
	}
	return out
}

func normalized(live map[string]any) map[string]any {
	raw, err := json.Marshal(live)
	if err != nil {
		return live
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return live
	}
	return out
}

func (c *comparer) walk(path string, applied, live any) {
	switch want := applied.(type) {
	case nil:
		// An apply drops a null (`"creationTimestamp": null`), so it asks
		// for nothing.
		return
	case map[string]any:
		have, ok := live.(map[string]any)
		if live == nil {
			if len(want) > 0 {
				c.add(path, applied, nil, Missing)
			}
			return
		}
		if !ok {
			c.add(path, applied, live, Changed)
			return
		}
		keys := make([]string, 0, len(want))
		for key := range want {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			c.walk(join(path, key), want[key], have[key])
		}
	case []any:
		have, ok := live.([]any)
		if live == nil {
			if len(want) > 0 {
				c.add(path, applied, nil, Missing)
			}
			return
		}
		if !ok {
			c.add(path, applied, live, Changed)
			return
		}
		if key := listKey(want, have); key != "" {
			c.keyed(path, key, want, have)
			return
		}
		c.positional(path, want, have)
	default:
		if live == nil {
			// The API server drops a plain omitempty field applied at its zero
			// value (`hostNetwork: false`), so absent live is what was asked
			// for. Only the known such fields: a pointer field keeps its
			// `false`, so `allowPrivilegeEscalation: false` gone live is drift.
			if !(zero(applied) && droppedAtZero(path)) {
				c.add(path, applied, nil, Missing)
			}
			return
		}
		if !same(path, applied, live) {
			c.add(path, applied, live, Changed)
		}
	}
}

// The keys a list of objects is merged by, most specific first: a container
// and an env var by name, a port by its number, a mount by its path.
var mergeKeys = []string{"name", "containerPort", "port", "mountPath", "devicePath", "ip", "topologyKey"}

// listKey is the key both lists can be matched by: present and unique in every
// applied item, and present in every live one. Empty when there is none, and
// the list is compared by position.
func listKey(want, have []any) string {
	for _, key := range mergeKeys {
		if keyedBy(want, key) && keyedBy(have, key) {
			return key
		}
	}
	return ""
}

func keyedBy(items []any, key string) bool {
	if len(items) == 0 {
		return true
	}
	seen := map[string]bool{}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return false
		}
		value, ok := m[key]
		if !ok || value == nil {
			return false
		}
		id := fmt.Sprint(value)
		if seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

// keyed matches items by key. An extra live item is not reported: a merge by
// key keeps what somebody else added, as Argo CD's own diff does.
func (c *comparer) keyed(path, key string, want, have []any) {
	live := map[string]any{}
	for _, item := range have {
		live[fmt.Sprint(item.(map[string]any)[key])] = item
	}
	for _, item := range want {
		id := fmt.Sprint(item.(map[string]any)[key])
		c.walk(fmt.Sprintf("%s[%s=%s]", path, key, id), item, live[id])
	}
}

// positional compares a list that an apply replaces whole, so an extra live
// item is a gap.
func (c *comparer) positional(path string, want, have []any) {
	for i, item := range want {
		var live any
		if i < len(have) {
			live = have[i]
		}
		c.walk(fmt.Sprintf("%s[%d]", path, i), item, live)
	}
	for i := len(want); i < len(have); i++ {
		c.add(fmt.Sprintf("%s[%d]", path, i), nil, have[i], Added)
	}
}

// omittedAtZero are the last segments of the workload fields that are plain
// omitempty values in the Kubernetes API types (PodSpec, Container), so the
// API server stores nothing for their zero value. A pointer field - most of
// securityContext, automountServiceAccountToken, replicas - is not here.
var omittedAtZero = map[string]bool{
	"hostNetwork": true, "hostPID": true, "hostIPC": true,
	"stdin": true, "stdinOnce": true, "tty": true,
	"subdomain": true, "hostname": true, "nodeName": true, "priorityClassName": true,
	"serviceAccount": true, "serviceAccountName": true,
}

// droppedAtZero says whether the field at path is one of omittedAtZero.
// Under `spec` only: a ConfigMap's `data.hostname` or a label `tty` is a
// map key, kept whatever its value.
func droppedAtZero(path string) bool {
	if !strings.HasPrefix(path, "spec.") {
		return false
	}
	last := path
	if i := strings.LastIndex(path, "."); i >= 0 {
		last = path[i+1:]
	}
	return omittedAtZero[last]
}

// zero is a scalar the API server leaves out when an omitempty field holds it.
func zero(value any) bool {
	switch v := value.(type) {
	case bool:
		return !v
	case string:
		return v == ""
	case float64:
		return v == 0
	case int64:
		return v == 0
	case int:
		return v == 0
	}
	return false
}

// same compares two scalars as the API server would: a quantity by its value
// ("1000m" is "1"), a number whatever its spelling, an int-or-string by its
// text (80 is "80").
func same(path string, applied, live any) bool {
	if reflect.DeepEqual(applied, live) {
		return true
	}
	a, b := fmt.Sprint(applied), fmt.Sprint(live)
	if a == b {
		return true
	}
	if strings.Contains(path, "resources.") || strings.Contains(path, "capacity") || strings.Contains(path, "storage") {
		qa, errA := resource.ParseQuantity(a)
		qb, errB := resource.ParseQuantity(b)
		if errA == nil && errB == nil {
			return qa.Cmp(qb) == 0
		}
	}
	return false
}

func (c *comparer) add(path string, applied, live any, change Change) {
	c.out = append(c.out, Field{
		Path:    path,
		Applied: c.text(applied),
		Live:    c.text(live),
		Change:  change,
	})
}

// text is a value as one line: a string as itself, anything else as JSON.
func (c *comparer) text(value any) string {
	if value == nil {
		return ""
	}
	if c.opts.Mask {
		return Masked
	}
	if s, ok := value.(string); ok {
		return s
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(raw)
}

// join spells a path the way `kubectl explain` reads, with a key that is not a
// plain word in brackets: metadata.labels["app.kubernetes.io/name"].
func join(path, key string) string {
	plain := key != ""
	for _, r := range key {
		if !(r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			plain = false
			break
		}
	}
	if !plain {
		return fmt.Sprintf("%s[%q]", path, key)
	}
	if path == "" {
		return key
	}
	return path + "." + key
}
