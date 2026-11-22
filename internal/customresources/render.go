package customresources

// Turning custom objects into the console's own table: the CRD's columns,
// plus Name, Namespace and Age, which a CRD need not declare.
//
// Only one cell is graded. This console does not know what a custom
// resource's fields mean, so coloring them would be invention. The exception
// is a `.status.phase` column holding exactly `Failed` or `Succeeded`: the
// Kubernetes convention's words, borrowed with their meaning. `Pending` and
// every other value stay neutral. The tone carries no rank: custom rows sort
// by name, so a failed instance is colored where it stands.

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/jsonpath"
	"sigs.k8s.io/yaml"

	"github.com/thonicdev/kubetower/internal/resources"
)

// missing is what an unresolved value prints as: an em dash reads as "this
// object has no such field" (ordinary for status a controller has not filled
// yet), where a blank cell reads as a fault. Shared with internal/resources so
// custom and built-in rows read alike.
const missing = resources.Missing

// cellLimit caps one cell in characters: a field may hold a certificate chain
// or a full error.
const cellLimit = 120

// valueLimit caps how many results one JSONPath contributes before the cell
// says how many it left out.
const valueLimit = 4

// render builds the table for one list of instances. now is a parameter so the
// age column is testable.
func (d Definition) render(items []unstructured.Unstructured, namespace string, within []string, query string, filters []resources.Filter, now time.Time) resources.Table {
	shown := d.shownColumns()

	table := resources.Table{
		Kind:       d.Slug(),
		Title:      d.Title(),
		Namespaced: d.Namespaced,
		Namespace:  namespace,
		Query:      query,
		Columns:    d.tableColumns(shown),
	}

	// Compiled once per table, not once per row.
	paths := compile(shown)

	// The built-in kinds' matcher, so the filter box behaves the same (it
	// matches the name, not the namespace). No printer column is Filterable
	// (see resources.Column), so filters are dropped here; they are passed
	// through so a Filterable column would work without a second path.
	// Narrowed to the namespace selection first, as for a built-in kind.
	match := resources.NewMatcher(query, table.Columns).WithFilters(filters).Within(within)
	offered := match.Offer()
	table.Rows = make([]resources.Row, 0, len(items))
	for i := range items {
		object := &items[i]
		name, ns := object.GetName(), object.GetNamespace()
		if !d.Namespaced {
			// Cluster-scoped per the CRD: whatever metadata.namespace holds
			// is not repeated.
			ns = ""
		}
		if !match.InScope(ns) {
			continue
		}

		cells := make([]resources.Cell, 0, len(table.Columns))
		cells = append(cells, resources.Cell{Text: resources.Dash(name)})
		if d.Namespaced {
			cells = append(cells, resources.Cell{Text: resources.Dash(ns)})
		}
		for _, p := range paths {
			cells = append(cells, p.cell(object, now))
		}
		cells = append(cells, resources.Cell{Text: ageOf(object, now)})

		if !match.Keep(name, cells, object.GetLabels(), offered) {
			continue
		}

		table.Rows = append(table.Rows, resources.Row{
			Name:      name,
			Namespace: ns,
			Cells:     cells,
		})
	}

	table.Filters, table.Offered = match.Applied(), offered.Lists()

	// By name, then namespace. No health sort: a phase cell has no rank.
	sort.SliceStable(table.Rows, func(i, j int) bool {
		if table.Rows[i].Name != table.Rows[j].Name {
			return table.Rows[i].Name < table.Rows[j].Name
		}
		return table.Rows[i].Namespace < table.Rows[j].Namespace
	})
	return table
}

// shownColumns is the CRD's columns minus the wide ones and minus those that
// repeat Name, Namespace or Age.
func (d Definition) shownColumns() []PrinterColumn {
	out := make([]PrinterColumn, 0, len(d.Columns))
	for _, c := range d.Columns {
		if c.Wide() {
			// kubectl prints it only under `-o wide`; the CRD has decided
			// what fits.
			continue
		}
		if duplicatesBuiltIn(c.JSONPath) {
			// Usually an Age column on creationTimestamp, which this table
			// already has.
			continue
		}
		out = append(out, c)
	}
	return out
}

// duplicatesBuiltIn matches on the JSONPath, not the column's name: "Name"
// reading `.spec.secretName` is real information, "Created At" reading
// `.metadata.creationTimestamp` is the age column renamed.
func duplicatesBuiltIn(path string) bool {
	switch normalisePath(path) {
	case ".metadata.name", ".metadata.namespace", ".metadata.creationTimestamp":
		return true
	default:
		return false
	}
}

func (d Definition) tableColumns(shown []PrinterColumn) []resources.Column {
	columns := make([]resources.Column, 0, len(shown)+3)
	columns = append(columns, resources.Column{Title: "Name"})
	if d.Namespaced {
		columns = append(columns, resources.Column{Title: "Namespace"})
	}
	for _, c := range shown {
		columns = append(columns, resources.Column{Title: c.Name, Numeric: numericType(c.Type)})
	}
	columns = append(columns, resources.Column{Title: "Age", Numeric: true})
	return columns
}

// numericType right-aligns a column of digits; a date renders as an age,
// digits too.
func numericType(t string) bool {
	switch strings.ToLower(t) {
	case "integer", "number", "date":
		return true
	default:
		return false
	}
}

// --- JSONPath ---------------------------------------------------------------

// resolver is one printer column with its expression compiled, or nil when it
// does not parse: the API server does not validate JSONPath at admission.
type resolver struct {
	column PrinterColumn
	path   *jsonpath.JSONPath
	// phase is set for a `.status.phase` column, the one this file grades.
	phase bool
}

func compile(columns []PrinterColumn) []resolver {
	out := make([]resolver, 0, len(columns))
	for _, c := range columns {
		r := resolver{column: c, phase: normalisePath(c.JSONPath) == ".status.phase"}
		p := jsonpath.New(c.Name)
		// Missing keys are normal: unreconciled objects have no status yet.
		p.AllowMissingKeys(true)
		if err := p.Parse("{" + normalisePath(c.JSONPath) + "}"); err == nil {
			r.path = p
		}
		out = append(out, r)
	}
	return out
}

// normalisePath accepts `.spec.replicas` and `{.spec.replicas}`, the form
// `kubectl -o jsonpath` users type.
func normalisePath(path string) string {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "{") && strings.HasSuffix(path, "}") {
		path = strings.TrimSuffix(strings.TrimPrefix(path, "{"), "}")
	}
	return strings.TrimSpace(path)
}

// cell resolves one column against one object. Every failure is an em dash,
// never an error: one bad expression must not take down the table, nor leave
// a blank that cannot be told from "no column".
func (r resolver) cell(object *unstructured.Unstructured, now time.Time) resources.Cell {
	if r.path == nil || object == nil {
		return resources.Cell{Text: missing}
	}
	results, err := r.path.FindResults(object.Object)
	if err != nil {
		return resources.Cell{Text: missing}
	}

	var values []string
	for _, group := range results {
		for _, value := range group {
			if text, ok := format(value, r.column.Type, now); ok {
				values = append(values, text)
			}
		}
	}
	if len(values) == 0 {
		return resources.Cell{Text: missing}
	}
	cell := resources.Cell{Text: resources.Clip(resources.JoinCapped(values, valueLimit), cellLimit)}
	if r.phase && len(values) == 1 {
		cell.Class = phaseClass(values[0])
	}
	return cell
}

// phaseClass is the tone of a `.status.phase` word, matched exactly as the
// convention spells it (`failed` is some controller's own word). No rank: the
// row keeps its place in name order.
func phaseClass(word string) string {
	switch word {
	case "Failed":
		return resources.ClassBad
	case "Succeeded":
		return resources.ClassOK
	default:
		// "Pending" included: neutral, drawn as the quiet chip.
		return resources.ClassNone
	}
}

// format renders one resolved value. A scalar is printed; anything else is
// described by its shape, since a custom resource may hold a token or a whole
// kubeconfig and this code cannot judge what is safe to show.
func format(value reflect.Value, columnType string, now time.Time) (string, bool) {
	for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return "", false
		}
		value = value.Elem()
	}

	switch value.Kind() {
	case reflect.Invalid:
		return "", false
	case reflect.String:
		s := value.String()
		if strings.TrimSpace(s) == "" {
			return "", false
		}
		if strings.EqualFold(columnType, "date") {
			return asAge(s, now), true
		}
		return s, true
	case reflect.Bool:
		return strconv.FormatBool(value.Bool()), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(value.Int(), 10), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(value.Uint(), 10), true
	case reflect.Float32, reflect.Float64:
		// -1 precision: a JSON 3 arrives as float64 and must print as "3".
		return strconv.FormatFloat(value.Float(), 'f', -1, 64), true
	case reflect.Map, reflect.Struct:
		return "{object}", true
	case reflect.Slice, reflect.Array:
		return fmt.Sprintf("[%d items]", value.Len()), true
	default:
		return "", false
	}
}

// asAge renders a `type: date` column as kubectl does: an age, not a date.
func asAge(value string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		// Not RFC3339 (a controller wrote what it liked): printed as is.
		return value
	}
	return resources.ShortDuration(now.Sub(t))
}

func ageOf(object *unstructured.Unstructured, now time.Time) string {
	created := object.GetCreationTimestamp()
	if created.IsZero() {
		return missing
	}
	return resources.ShortDuration(now.Sub(created.Time))
}

// --- the manifest -----------------------------------------------------------

// manifest renders one object as YAML for the detail view.
//
// Nothing is redacted: which field holds a credential cannot be known, and the
// detail view exists to read the object, so blanking on a guess would make a
// quietly wrong manifest. managedFields is dropped: it is about who touched
// the object, not the object, and often outweighs it.
func manifest(object *unstructured.Unstructured) (string, error) {
	if object == nil {
		return "", fmt.Errorf("no object")
	}
	// Deep-copied: the object may be shared with a cache and this deletes from it.
	copied := object.DeepCopy()
	unstructured.RemoveNestedField(copied.Object, "metadata", "managedFields")

	raw, err := json.Marshal(copied.Object)
	if err != nil {
		return "", fmt.Errorf("encoding the object: %w", err)
	}
	out, err := yaml.JSONToYAML(raw)
	if err != nil {
		return "", fmt.Errorf("rendering YAML: %w", err)
	}
	return string(out), nil
}
