package api

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// A JSON Schema evaluator, for the conformance test and for nothing else.
//
// # Why this is here rather than a dependency
//
// This module's own package comment says it keeps deliberately few dependencies,
// and every one of them earns its place in the binary. A schema validator used by
// one test file does not, so it is written here — in a `_test.go` file, so it
// reaches no build of the product.
//
// # Why it is not a copy of the schema
//
// That is the whole point of the test above it. The thing that would be useless
// is a Go struct restating what `api/openapi.yaml` says: two copies agree with
// each other and neither has to agree with the file. This reads the YAML, walks
// the schema objects it finds there, and knows nothing about KubeTower at all —
// so a schema edited in the file is a schema this enforces on the next run, and a
// handler that stops matching it fails.
//
// # What it supports, and why that is enough rather than arbitrary
//
// Exactly the keywords `api/openapi.yaml` uses, which were counted rather than
// guessed — see TestTheSchemaEvaluatorCoversEveryKeywordTheSpecUses, which walks
// the file and fails on a keyword this evaluator would silently ignore. **That
// test is the load-bearing one:** a validator that quietly skips `if`/`then`
// would pass `{"state":"ok","items":[]}`, which is the single shape this whole
// contract exists to refuse.

// schema is a resolved JSON Schema node. `true`/`false` schemas are not used by
// the spec, so a node is always an object.
type schema map[string]any

// validator walks a document against a schema, resolving `$ref` against the root.
type validator struct {
	root map[string]any
}

// validate returns every way the document fails the schema, each with the path
// it failed at. Every way, rather than the first: a response that is wrong in
// three places is more usefully reported once than three times.
func (v *validator) validate(doc any, s schema, path string) []string {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, path+": "+fmt.Sprintf(format, args...))
	}

	if ref, ok := s["$ref"].(string); ok {
		resolved, err := v.resolve(ref)
		if err != nil {
			return []string{path + ": " + err.Error()}
		}
		// A sibling of $ref is ignored by JSON Schema 2020-12 only for some
		// keywords; the spec never writes one, so resolving and continuing is
		// both correct here and detected by the keyword census if it changes.
		return v.validate(doc, resolved, path)
	}

	// --- composition ---------------------------------------------------------

	for _, sub := range subschemas(s["allOf"]) {
		problems = append(problems, v.validate(doc, sub, path)...)
	}
	if raw, ok := s["anyOf"]; ok {
		matched := false
		for _, sub := range subschemas(raw) {
			if len(v.validate(doc, sub, path)) == 0 {
				matched = true
				break
			}
		}
		if !matched {
			add("matches no branch of anyOf")
		}
	}
	if raw, ok := s["oneOf"]; ok {
		count := 0
		for _, sub := range subschemas(raw) {
			if len(v.validate(doc, sub, path)) == 0 {
				count++
			}
		}
		if count != 1 {
			add("matches %d branches of oneOf, expected exactly 1", count)
		}
	}
	if raw, ok := s["not"]; ok {
		if sub, ok := asSchema(raw); ok && len(v.validate(doc, sub, path)) == 0 {
			add("matches a schema it must not")
		}
	}
	if raw, ok := s["if"]; ok {
		condition, _ := asSchema(raw)
		taken := "else"
		if len(v.validate(doc, condition, path)) == 0 {
			taken = "then"
		}
		if branch, ok := asSchema(s[taken]); ok {
			problems = append(problems, v.validate(doc, branch, path)...)
		}
	}

	// --- type ----------------------------------------------------------------

	if raw, ok := s["type"]; ok {
		want := typeNames(raw)
		if !matchesAnyType(doc, want) {
			add("is %s, expected %s", jsonKindOf(doc), strings.Join(want, " or "))
			// Nothing below can say anything useful about a value of the wrong
			// type, and the keyword checks would report nonsense.
			return problems
		}
	}

	if raw, ok := s["const"]; ok && !sameJSON(doc, raw) {
		add("is %v, expected the constant %v", doc, raw)
	}
	if raw, ok := s["enum"].([]any); ok {
		found := false
		for _, option := range raw {
			if sameJSON(doc, option) {
				found = true
				break
			}
		}
		if !found {
			add("is %v, which is not in the enum", doc)
		}
	}

	switch value := doc.(type) {
	case map[string]any:
		problems = append(problems, v.object(value, s, path, add)...)
	case []any:
		problems = append(problems, v.array(value, s, path, add)...)
	case string:
		problems = append(problems, stringKeywords(value, s, add)...)
	case float64:
		problems = append(problems, numberKeywords(value, s, add)...)
	}
	return problems
}

func (v *validator) object(doc map[string]any, s schema, path string, add func(string, ...any)) []string {
	var problems []string
	if raw, ok := s["required"].([]any); ok {
		for _, name := range raw {
			key, _ := name.(string)
			if _, present := doc[key]; !present {
				add("is missing the required member %q", key)
			}
		}
	}
	properties, _ := s["properties"].(map[string]any)
	for key, value := range doc {
		sub, declared := properties[key]
		if !declared {
			// `additionalProperties: false` is how the spec says "one cell per
			// request"; `additionalProperties: {schema}` is how it types a map.
			switch extra := s["additionalProperties"].(type) {
			case bool:
				if !extra {
					add("carries %q, which the schema does not allow", key)
				}
			case map[string]any:
				problems = append(problems, v.validate(value, extra, path+"."+key)...)
			}
			continue
		}
		if subSchema, ok := asSchema(sub); ok {
			problems = append(problems, v.validate(value, subSchema, path+"."+key)...)
		}
	}
	return problems
}

func (v *validator) array(doc []any, s schema, path string, add func(string, ...any)) []string {
	var problems []string
	if items, ok := asSchema(s["items"]); ok {
		for i, element := range doc {
			problems = append(problems, v.validate(element, items, fmt.Sprintf("%s[%d]", path, i))...)
		}
	}
	if raw, ok := s["minItems"].(float64); ok && float64(len(doc)) < raw {
		add("has %d elements, fewer than the %v required", len(doc), raw)
	}
	if raw, ok := s["maxItems"].(float64); ok && float64(len(doc)) > raw {
		add("has %d elements, more than the %v allowed", len(doc), raw)
	}
	return problems
}

func stringKeywords(value string, s schema, add func(string, ...any)) []string {
	if raw, ok := s["minLength"].(float64); ok && float64(len([]rune(value))) < raw {
		add("is shorter than the minimum %v", raw)
	}
	if raw, ok := s["maxLength"].(float64); ok && float64(len([]rune(value))) > raw {
		add("is longer than the maximum %v", raw)
	}
	if raw, ok := s["pattern"].(string); ok {
		matcher, err := regexp.Compile(raw)
		if err != nil {
			add("has an unusable pattern %q: %v", raw, err)
		} else if !matcher.MatchString(value) {
			add("%q does not match %q", value, raw)
		}
	}
	return nil
}

func numberKeywords(value float64, s schema, add func(string, ...any)) []string {
	if raw, ok := s["minimum"].(float64); ok && value < raw {
		add("is %v, below the minimum %v", value, raw)
	}
	if raw, ok := s["maximum"].(float64); ok && value > raw {
		add("is %v, above the maximum %v", value, raw)
	}
	return nil
}

// resolve follows a local `$ref`. Only local ones exist in this document, and a
// remote one would be a change worth failing on rather than fetching.
func (v *validator) resolve(ref string) (schema, error) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("only local refs are supported, not %q", ref)
	}
	node := any(v.root)
	for _, segment := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		object, ok := node.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%q does not resolve", ref)
		}
		node, ok = object[segment]
		if !ok {
			return nil, fmt.Errorf("%q does not resolve", ref)
		}
	}
	resolved, ok := asSchema(node)
	if !ok {
		return nil, fmt.Errorf("%q is not a schema", ref)
	}
	return resolved, nil
}

func asSchema(raw any) (schema, bool) {
	object, ok := raw.(map[string]any)
	return schema(object), ok
}

func subschemas(raw any) []schema {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]schema, 0, len(list))
	for _, element := range list {
		if sub, ok := asSchema(element); ok {
			out = append(out, sub)
		}
	}
	return out
}

func typeNames(raw any) []string {
	switch value := raw.(type) {
	case string:
		return []string{value}
	case []any:
		out := make([]string, 0, len(value))
		for _, element := range value {
			if name, ok := element.(string); ok {
				out = append(out, name)
			}
		}
		return out
	}
	return nil
}

func matchesAnyType(doc any, want []string) bool {
	for _, name := range want {
		if matchesType(doc, name) {
			return true
		}
	}
	return len(want) == 0
}

func matchesType(doc any, name string) bool {
	switch name {
	case "object":
		_, ok := doc.(map[string]any)
		return ok
	case "array":
		_, ok := doc.([]any)
		return ok
	case "string":
		_, ok := doc.(string)
		return ok
	case "boolean":
		_, ok := doc.(bool)
		return ok
	case "number":
		_, ok := doc.(float64)
		return ok
	case "integer":
		value, ok := doc.(float64)
		return ok && value == math.Trunc(value)
	case "null":
		return doc == nil
	}
	return false
}

func jsonKindOf(doc any) string {
	switch doc.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "an object"
	case []any:
		return "an array"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case float64:
		return "a number"
	}
	return "unknown"
}

func sameJSON(a, b any) bool { return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b) }

// supportedKeywords is what this evaluator acts on. Anything else in the file's
// schemas is either annotation — which changes no answer — or a keyword this
// would silently ignore, which is the failure mode the census test exists to
// catch.
var supportedKeywords = map[string]bool{
	"$ref": true, "allOf": true, "anyOf": true, "oneOf": true, "not": true,
	"if": true, "then": true, "else": true,
	"type": true, "const": true, "enum": true,
	"required": true, "properties": true, "additionalProperties": true,
	"items": true, "minItems": true, "maxItems": true,
	"minLength": true, "maxLength": true, "pattern": true,
	"minimum": true, "maximum": true,
}

// annotationKeywords change no answer. Listed rather than assumed, so that a
// keyword nobody thought about lands in neither set and is reported.
var annotationKeywords = map[string]bool{
	"description": true, "summary": true, "title": true, "default": true,
	"example": true, "examples": true, "deprecated": true, "format": true,
	"readOnly": true, "writeOnly": true, "nullable": true,
}

// keywordsIn walks every schema object reachable from a node and reports the
// keywords used, so a test can assert this evaluator understands all of them.
func keywordsIn(node any, out map[string]bool) {
	switch value := node.(type) {
	case map[string]any:
		for key, child := range value {
			out[key] = true
			keywordsIn(child, out)
		}
	case []any:
		for _, child := range value {
			keywordsIn(child, out)
		}
	}
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
