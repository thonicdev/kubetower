package resources

import (
	"sort"
	"strings"
)

// Matcher is the search box and the filter control, applied to rows.
//
// The search matches, case-insensitively by substring, the name, what the row
// shows, and label keys and values (what an operator selects on). Three
// exclusions:
//
//   - A Numeric column's text is not searched: `m` would match every CPU cell,
//     and a live figure would make rows flicker in and out of a filtered list.
//     Its Hint still is, since a hint holds words the number hides (a node's
//     taints).
//   - The namespace is not searched: it has a control of its own, and the box
//     must not widen what that control narrowed.
//   - A key Kubernetes stamps on every object of a kind (stampedKeys: a
//     controller's on its pods, a Node's, a Namespace's) is not searched by its
//     name; its value still is.
//
// A filter is a column and a value, equal rather than contained (a link to
// node `ip-10-0-1-7` must not include `ip-10-0-1-70`), case-insensitively. It
// arrives as its own repeatable `filter` parameter, `<column title>:<value>`,
// not as a grammar in the box. Only a Filterable column can be filtered; a
// filter on any other column narrows nothing and is not echoed (Applied).
// Values of one column are OR-ed, columns are AND-ed, and filters apply on
// top of the search.
//
// The values offered per column (Offered) come from the rows the search and
// every *other* column's filters leave, so a second value of an OR can still
// be chosen.
//
// internal/customresources uses this matcher too, so the box behaves the same
// on CRDs.
type Matcher struct {
	needle string
	// skip is the Namespace column's index, or -1. Found by title, so a CRD's
	// printer columns are covered too.
	skip int
	// numeric marks the columns whose text is a figure. Parallel to the column
	// list rather than to the cells, which can be shorter.
	numeric []bool
	// titles and filterable are what WithFilters resolves a filter against.
	titles     []string
	filterable []bool
	// wanted is the filters, by column index: a set of lowered values, any one
	// of which the cell may equal. Every column in it has to be satisfied.
	wanted map[int]map[string]bool
	// applied is the same filters as they will be echoed, in the order given.
	applied []Applied
	// within is a selection of several namespaces, or nil. See Within.
	within map[string]bool
}

// Filter is one value of the list's `filter` parameter: a column, by its title,
// and a value that column's cell must equal.
type Filter struct {
	Column string
	Value  string
}

// ParseFilter reads one `filter` parameter, `<column title>:<value>`.
//
// Split at the first colon: a title never holds one and a value may. Spaces
// around either half are trimmed. No colon or an empty half is not a filter.
func ParseFilter(raw string) (Filter, bool) {
	column, value, found := strings.Cut(raw, ":")
	column, value = strings.TrimSpace(column), strings.TrimSpace(value)
	if !found || column == "" || value == "" {
		return Filter{}, false
	}
	return Filter{Column: column, Value: value}, true
}

// Applied is a filter that narrowed this table, resolved to a column index:
// what a page echoes back, so a client draws a chip only for a filter that took
// effect.
type Applied struct {
	Column int
	Value  string
}

// NewMatcher reads the query and locates the namespace column once, rather
// than per row.
func NewMatcher(query string, columns []Column) Matcher {
	m := Matcher{
		needle:     strings.ToLower(strings.TrimSpace(query)),
		skip:       -1,
		numeric:    make([]bool, len(columns)),
		titles:     make([]string, len(columns)),
		filterable: make([]bool, len(columns)),
	}
	for i, c := range columns {
		m.numeric[i] = c.Numeric
		m.titles[i] = c.Title
		m.filterable[i] = c.Filterable
		if m.skip < 0 && strings.EqualFold(c.Title, "Namespace") {
			m.skip = i
		}
	}
	return m
}

// Within narrows the matcher to a selection of namespaces, before anything is
// matched or offered. A selection of several namespaces is read as one list of
// the whole cluster, so narrowing first keeps the offered values to the rows
// actually listed.
//
// An empty selection is every namespace. A row with no namespace (a
// cluster-scoped kind) is in no selection.
func (m Matcher) Within(namespaces []string) Matcher {
	m.within = nil
	if len(namespaces) == 0 {
		return m
	}
	m.within = make(map[string]bool, len(namespaces))
	for _, name := range namespaces {
		m.within[name] = true
	}
	return m
}

// InScope reports whether a row in namespace is inside the selection Within
// was given. Asked before Keep, so a row outside it neither lists nor offers.
func (m Matcher) InScope(namespace string) bool {
	return m.within == nil || (namespace != "" && m.within[namespace])
}

// filters reports whether column i may be filtered. The namespace and figures
// never are, checked here too in case a descriptor got it wrong.
func (m Matcher) filters(i int) bool {
	return m.filterable[i] && i != m.skip && !m.numeric[i]
}

// WithFilters adds the filter control's choices to the search. A filter on a
// column that is not Filterable, or a repeat, is dropped and absent from
// Applied.
func (m Matcher) WithFilters(filters []Filter) Matcher {
	m.wanted, m.applied = nil, nil
	for _, f := range filters {
		at := -1
		for i, title := range m.titles {
			if m.filters(i) && strings.EqualFold(title, strings.TrimSpace(f.Column)) {
				at = i
				break
			}
		}
		value := strings.TrimSpace(f.Value)
		if at < 0 || value == "" {
			continue
		}
		if m.wanted == nil {
			m.wanted = map[int]map[string]bool{}
		}
		if m.wanted[at] == nil {
			m.wanted[at] = map[string]bool{}
		}
		if m.wanted[at][strings.ToLower(value)] {
			continue
		}
		m.wanted[at][strings.ToLower(value)] = true
		m.applied = append(m.applied, Applied{Column: at, Value: value})
	}
	return m
}

// Applied is the filters that narrow this table, in the order they were given.
func (m Matcher) Applied() []Applied { return m.applied }

// Offered is, per Filterable column, the values a filter control can offer:
// lowered value to the spelling first seen. Started by Offer, filled by Keep.
type Offered map[int]map[string]string

// Offer starts an Offered with an entry for every Filterable column, so a
// column whose rows were all filtered away reads as no values.
func (m Matcher) Offer() Offered {
	offered := Offered{}
	for i := range m.filterable {
		if m.filters(i) {
			offered[i] = map[string]string{}
		}
	}
	return offered
}

// Lists is every column's Values, keyed as Offered is.
func (o Offered) Lists() map[int][]string {
	out := make(map[int][]string, len(o))
	for column := range o {
		out[column] = o.Values(column)
	}
	return out
}

// Values is one column's offered values, sorted case-insensitively. Never nil
// for a column Offer started, so "no values" is an empty list on the wire.
func (o Offered) Values(column int) []string {
	seen, ok := o[column]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(seen))
	for _, value := range seen {
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i]), strings.ToLower(out[j])
		if a != b {
			return a < b
		}
		return out[i] < out[j]
	})
	return out
}

// Empty reports that nothing was typed or chosen, so a caller can skip
// matching.
func (m Matcher) Empty() bool { return m.needle == "" && len(m.wanted) == 0 }

// Match reports whether this row answers what was typed and what was chosen.
//
// cells may be shorter than the column list (a CRD builds only the columns it
// shows); a short row is not an error.
func (m Matcher) Match(name string, cells []Cell, labels map[string]string) bool {
	return m.Keep(name, cells, labels, nil)
}

// Keep is Match, and on the way it records into offered (when not nil) the
// values this row contributes to each Filterable column's menu: a row the
// search keeps offers its value of a column when every *other* column's filter
// accepts it, whatever that column's own filter says.
func (m Matcher) Keep(name string, cells []Cell, labels map[string]string, offered Offered) bool {
	if !m.searched(name, cells, labels) {
		return false
	}
	misses, missed := 0, -1
	for column, values := range m.wanted {
		// A row without the filtered column does not match it.
		if column >= len(cells) || !values[strings.ToLower(strings.TrimSpace(cells[column].Text))] {
			misses++
			missed = column
		}
	}
	for column, seen := range offered {
		if column >= len(cells) || misses > 1 || (misses == 1 && missed != column) {
			continue
		}
		value := strings.TrimSpace(cells[column].Text)
		if _, ok := seen[strings.ToLower(value)]; !ok {
			seen[strings.ToLower(value)] = value
		}
	}
	return misses == 0
}

// searched is the box: the words against the name, the cells and the labels.
func (m Matcher) searched(name string, cells []Cell, labels map[string]string) bool {
	if m.needle == "" {
		return true
	}
	if strings.Contains(strings.ToLower(name), m.needle) {
		return true
	}
	for i := range cells {
		if i == m.skip {
			continue
		}
		// A figure's text is not searched (see Matcher). A CRD may hand fewer
		// cells than columns, hence the bounds check.
		if !(i < len(m.numeric) && m.numeric[i]) &&
			strings.Contains(strings.ToLower(cells[i].Text), m.needle) {
			return true
		}
		// The hint always, numeric or not: a node's taints live there.
		if cells[i].Hint != "" && strings.Contains(strings.ToLower(cells[i].Hint), m.needle) {
			return true
		}
	}
	// A label's value and its key, except the keys Kubernetes stamps on every
	// object of a kind (stampedKeys): `pod-template-hash` holds a `p`,
	// an `m` and a `t`, so most first keystrokes would match the whole
	// cluster through a word nobody typed and nothing draws. `key=value` as
	// typed matches too, so a selector-shaped search still finds its row.
	pair := strings.Contains(m.needle, "=")
	for key, value := range labels {
		if strings.Contains(strings.ToLower(value), m.needle) {
			return true
		}
		if pair && strings.Contains(strings.ToLower(key+"="+value), m.needle) {
			return true
		}
		if !stampedKeys[key] && strings.Contains(strings.ToLower(key), m.needle) {
			return true
		}
	}
	return false
}

// stampedKeys are the label keys Kubernetes writes itself on every object of
// a kind: a controller on what it makes (a Deployment's pods, a Job's pods),
// the kubelet and cloud provider on every Node, the API server on every
// Namespace. Their names say nothing an operator chose and are on nearly every
// row of their list, so a search matching them would return the whole list for
// one letter. Their values are still searched, and a selector still selects on
// them.
var stampedKeys = map[string]bool{
	// Workload controllers, on the pods and revisions they make.
	"pod-template-hash":                        true,
	"controller-revision-hash":                 true,
	"pod-template-generation":                  true,
	"statefulset.kubernetes.io/pod-name":       true,
	"apps.kubernetes.io/pod-index":             true,
	"controller-uid":                           true,
	"job-name":                                 true,
	"batch.kubernetes.io/controller-uid":       true,
	"batch.kubernetes.io/job-name":             true,
	"batch.kubernetes.io/job-completion-index": true,
	// The kubelet and the cloud provider, on every Node.
	"kubernetes.io/hostname":                   true,
	"kubernetes.io/os":                         true,
	"kubernetes.io/arch":                       true,
	"beta.kubernetes.io/os":                    true,
	"beta.kubernetes.io/arch":                  true,
	"node.kubernetes.io/instance-type":         true,
	"beta.kubernetes.io/instance-type":         true,
	"topology.kubernetes.io/region":            true,
	"topology.kubernetes.io/zone":              true,
	"failure-domain.beta.kubernetes.io/region": true,
	"failure-domain.beta.kubernetes.io/zone":   true,
	// The API server, on every Namespace.
	"kubernetes.io/metadata.name": true,
}
