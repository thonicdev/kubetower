package edit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// The patch a manifest edit turns into.
//
// The drawer shows the object trimmed (no `managedFields`, no last-applied
// annotation). Sending the edited document back as a whole-object Update
// would delete those fields, which the operator was never shown, and on a
// kind whose `status` is written through the main resource would flatten
// that too. So the edited YAML is diffed against the same trimmed form and
// only the difference is sent: exactly what the operator changed, which is
// what the on-screen diff shows.

// mergePatch computes the RFC 7386 patch that turns base into edited.
//
// A null means "delete this key", and an array is replaced wholesale: an
// array that changed at all is sent in full, as the diff shows it.
func mergePatch(base, edited map[string]any) map[string]any {
	patch := map[string]any{}

	for key, next := range edited {
		prev, had := base[key]
		if !had {
			patch[key] = next
			continue
		}
		prevMap, prevIsMap := prev.(map[string]any)
		nextMap, nextIsMap := next.(map[string]any)
		if prevIsMap && nextIsMap {
			if sub := mergePatch(prevMap, nextMap); len(sub) > 0 {
				patch[key] = sub
			}
			continue
		}
		if !reflect.DeepEqual(prev, next) {
			patch[key] = next
		}
	}

	// A removed key must be sent as an explicit null: absent from a merge
	// patch means "unchanged", and the deletion would never reach the cluster.
	for key := range base {
		if _, still := edited[key]; !still {
			patch[key] = nil
		}
	}
	return patch
}

// normalize puts a document into the one shape the comparison above can trust.
//
// An unstructured object holds int64 and a YAML decode holds float64, so
// `8080` from each is not DeepEqual. Both are round-tripped through JSON
// with UseNumber, so a number is its exact text on both sides, with no
// float64 rounding of a large value.
func normalise(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return decodeJSON(raw)
}

func decodeJSON(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var out map[string]any
	if err := decoder.Decode(&out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("the document is empty")
	}
	return out, nil
}

// paths lists the dotted leaves of a patch, so the log line and the confirmation
// can name what is being changed rather than counting it.
//
// A null is named as a removal: a confirmation must not read the same
// whether a label was set or deleted.
func paths(patch map[string]any) []string {
	var out []string
	walkPaths("", patch, &out)
	sort.Strings(out)
	return out
}

func walkPaths(prefix string, node map[string]any, out *[]string) {
	for key, value := range node {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		switch typed := value.(type) {
		case map[string]any:
			if len(typed) == 0 {
				*out = append(*out, path)
				continue
			}
			walkPaths(path, typed, out)
		case nil:
			*out = append(*out, path+" (removed)")
		default:
			*out = append(*out, path)
		}
	}
}

// pretty renders the patch, indented, for the confirmation screen.
func pretty(patch map[string]any) string {
	out, err := json.MarshalIndent(patch, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(out)
}

// hasPath reports whether the patch touches a dotted path, which is how the
// predictable refusals in guard.go are expressed.
func hasPath(patch map[string]any, path string) bool {
	node := patch
	segments := strings.Split(path, ".")
	for i, segment := range segments {
		value, present := node[segment]
		if !present {
			return false
		}
		if i == len(segments)-1 {
			return true
		}
		next, ok := value.(map[string]any)
		if !ok {
			// The patch replaces the whole node here, which changes everything
			// below it.
			return true
		}
		node = next
	}
	return false
}
