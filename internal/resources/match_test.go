package resources

import (
	"strings"
	"testing"
)

func podColumns() []Column {
	return []Column{
		col("Name"), col("Namespace"), col("Status"), col("Node"), numCol("Age"),
	}
}

func podCells(status, node string) []Cell {
	return []Cell{
		{Text: "checkout-7d9f"}, {Text: "payments"}, {Text: status}, {Text: node}, {Text: "3d"},
	}
}

// The filter searches what the row shows, plus its labels.
//
// An operator on a pod list wants the pods on one node, or the pods of one
// app, and neither of those is a name.
func TestTheFilterSearchesTheRowAsDrawnAndItsLabels(t *testing.T) {
	labels := map[string]string{"app": "checkout", "tier": "backend"}

	cases := []struct {
		query string
		want  bool
		why   string
	}{
		{"checkout-7d", true, "the name, which is what it always did"},
		{"CHECKOUT-7D", true, "and case-insensitively"},
		{"worker-2", true, "the node, which is a column the operator can see"},
		{"crashloop", true, "the status, likewise"},
		{"tier=backend", true, "a label as it would be written in a selector"},
		{"backend", true, "or just its value"},
		{"nothing-like-this", false, "and something no cell and no label carries"},
	}

	m := NewMatcher("", podColumns())
	if !m.Empty() {
		t.Error("an empty query does not report itself empty, so callers cannot keep a fast path")
	}
	if !m.Match("anything", nil, nil) {
		t.Error("an empty query rejected a row; nothing typed must mean nothing filtered")
	}

	for _, c := range cases {
		m := NewMatcher(c.query, podColumns())
		got := m.Match("checkout-7d9f", podCells("CrashLoopBackOff", "worker-2"), labels)
		if got != c.want {
			t.Errorf("%q matched=%v, want %v — %s", c.query, got, c.want, c.why)
		}
	}
}

// The namespace is the one column the box does not search, and it is a decision
// rather than an oversight.
//
// There is a multi-select namespace control. A search for `api` inside one
// namespace that returns rows from another whose *namespace* contains `api` is
// a control being undone by a box, invisibly, for an operator who has just
// narrowed on purpose. The rule that generalizes it: the filter does not search
// the one column that has a control of its own.
func TestTheFilterDoesNotSearchTheNamespaceColumn(t *testing.T) {
	m := NewMatcher("payment", podColumns())

	// `payments` is the namespace cell and nothing else on this row says it.
	row := []Cell{
		{Text: "checkout-7d9f"}, {Text: "payments"}, {Text: "Running"}, {Text: "worker-2"}, {Text: "3d"},
	}
	if m.Match("checkout-7d9f", row, nil) {
		t.Error("the namespace matched, so the box widens a selection the namespace " +
			"control has just narrowed — and says nothing about having done so")
	}

	// And the exclusion is by title, not by position: a table whose namespace
	// column is somewhere else is covered by the same rule.
	moved := []Column{col("Name"), col("Status"), col("Namespace"), numCol("Age")}
	shifted := []Cell{{Text: "checkout-7d9f"}, {Text: "Running"}, {Text: "payments"}, {Text: "3d"}}
	if NewMatcher("payment", moved).Match("checkout-7d9f", shifted, nil) {
		t.Error("the namespace column is found by position rather than by title, so a " +
			"kind that orders its columns differently leaks it back into the search")
	}

	// A kind with no namespace column at all must not skip an arbitrary cell.
	clusterScoped := []Column{col("Name"), col("Taints"), numCol("Age")}
	m2 := NewMatcher("nosched", clusterScoped)
	if !m2.Match("node-a", []Cell{{Text: "node-a"}, {Text: "NoSchedule"}, {Text: "9d"}}, nil) {
		t.Error("a cluster-scoped kind lost a column to the namespace skip, which it does not have")
	}
}

// A cell's hint is part of what the row says.
//
// A node's taint column prints their *count* and puts the taints themselves in
// the hint, because "2" is the right thing in a column and the wrong thing to
// stop at. Searching only the text would make `NoSchedule` a thing the table
// knows, shows on hover, and cannot find.
func TestTheFilterSearchesACellsHint(t *testing.T) {
	columns := []Column{col("Name"), numCol("Taints"), numCol("Age")}
	cells := []Cell{
		{Text: "node-a"},
		{Text: "2", Hint: "node-role.kubernetes.io/control-plane:NoSchedule"},
		{Text: "9d"},
	}

	if !NewMatcher("noschedule", columns).Match("node-a", cells, nil) {
		t.Error("a taint the table shows on hover cannot be searched for")
	}
}

// A row shorter than its column list is ordinary, not a fault.
//
// A CRD builds only the columns it draws, so a table declaring seven columns can
// hand this five cells. The skip index is only ever compared, never used to
// index, and this pins that.
func TestAShortRowDoesNotPanicTheMatcher(t *testing.T) {
	m := NewMatcher("worker", []Column{
		col("Name"), col("Namespace"), col("A"), col("B"), col("C"), col("Node"),
	})
	if !m.Match("x", []Cell{{Text: "x"}, {Text: "ns"}, {Text: "worker-9"}}, nil) {
		t.Error("a short row did not match a cell it plainly carries")
	}
}

// A column of figures is not searched, for two reasons.
//
// Searching "everything drawn" makes typing `m` match **every** pod, because
// every CPU cell ends in `m` — `2m`, `7m`, `3m`. So does `1`. A filter that
// matches the whole table on the first character of a name is a filter that
// does not work.
//
// The second is worse and is the reason this is not merely tuning: CPU and
// memory are re-read every two seconds. A filter matching `12` because a pod's
// CPU says `12m` loses that row on the next refresh and gets it back on the one
// after — a *filtered* list whose contents change with nothing touched and
// nothing on screen to explain it.
func TestAColumnOfFiguresIsNotSearched(t *testing.T) {
	columns := []Column{
		col("Name"), col("Namespace"), numCol("Ready"), col("Status"),
		numCol("Restarts"), numCol("CPU"), numCol("Age"),
	}
	cells := []Cell{
		{Text: "checkout-7d9f"}, {Text: "payments"}, {Text: "1/1"}, {Text: "Running"},
		{Text: "3"}, {Text: "12m"}, {Text: "3d"},
	}

	for _, c := range []struct{ query, why string }{
		{"12m", "a CPU reading changes every two seconds; a row that matched it would " +
			"leave a filtered list and come back on its own"},
		{"m", "every CPU cell ends in `m`, so this matched every pod in the cluster"},
		{"3d", "an age is a figure that moves, and nobody searches for one"},
		{"1/1", "a readiness count is a figure too"},
	} {
		if NewMatcher(c.query, columns).Match("checkout-7d9f", cells, nil) {
			t.Errorf("%q matched through a numeric column — %s", c.query, c.why)
		}
	}

	// And the columns that say what the thing *is* still match.
	for _, c := range []struct{ query, why string }{
		{"checkout", "the name"},
		{"running", "the status"},
	} {
		if !NewMatcher(c.query, columns).Match("checkout-7d9f", cells, nil) {
			t.Errorf("%q no longer matches — %s. The exclusion has gone too wide and taken "+
				"the columns the filter exists for", c.query, c.why)
		}
	}
}

// A numeric column's hint is still searched, and that is the distinction rather
// than an exception to it: a hint exists exactly where the figure is not the
// whole story. A node's taint column prints `2` and hides `NoSchedule` behind
// it — the number is not worth searching, the words are.
func TestANumericColumnsHintIsStillSearched(t *testing.T) {
	columns := []Column{col("Name"), numCol("Taints"), numCol("Age")}
	cells := []Cell{
		{Text: "node-a"},
		{Text: "2", Hint: "node-role.kubernetes.io/control-plane:NoSchedule"},
		{Text: "9d"},
	}

	if !NewMatcher("noschedule", columns).Match("node-a", cells, nil) {
		t.Error("excluding numeric columns took their hints with them, so a taint the " +
			"table shows on hover became unsearchable")
	}
	if NewMatcher("2", columns).Match("node-a", cells, nil) {
		t.Error("the taint *count* is searchable, which is the noise this is all about")
	}
}

// A label's key is searched like its value, except the keys a controller
// stamps on every pod.
//
// `pod-template-hash` is on every pod a Deployment owns and contains an `m`, a
// `p`, a `t`, an `a`, an `l` and an `e`, so most first keystrokes would match
// the whole cluster through it. A key the operator wrote (`network`, `app`,
// `app.kubernetes.io/managed-by`) is what they search for (#739).
func TestALabelKeyMatchesUnlessAControllerStampedIt(t *testing.T) {
	labels := map[string]string{
		"pod-template-hash":            "68c4c49bfb",
		"app.kubernetes.io/managed-by": "Helm",
		"app":                          "checkout",
	}
	columns := []Column{col("Name"), col("Namespace"), col("Status"), numCol("Age")}
	cells := []Cell{{Text: "checkout-7d9f"}, {Text: "payments"}, {Text: "Running"}, {Text: "3d"}}

	for _, q := range []string{"template", "pod-template-hash"} {
		if NewMatcher(q, columns).Match("checkout-7d9f", cells, labels) {
			t.Errorf("%q matched through `pod-template-hash`, which every pod a Deployment owns carries", q)
		}
	}
	plain := map[string]string{"pod-template-hash": "68c4c49bfb", "app": "checkout"}
	if NewMatcher("m", columns).Match("checkout-7d9f", cells, plain) {
		t.Error("`m` matched through `pod-template-hash`: one keystroke would return the whole cluster")
	}
	for _, q := range []string{"managed-by", "MANAGED", "app.kubernetes.io"} {
		if !NewMatcher(q, columns).Match("checkout-7d9f", cells, labels) {
			t.Errorf("%q did not match a label key the operator wrote", q)
		}
	}
	if !NewMatcher("68c4", columns).Match("checkout-7d9f", cells, labels) {
		t.Error("a stamped key's value stopped being searchable")
	}
	for _, q := range []string{"app=checkout", "pod-template-hash=68c4", "managed-by=helm"} {
		if !NewMatcher(q, columns).Match("checkout-7d9f", cells, labels) {
			t.Errorf("%q did not match: a search written as key=value finds its row", q)
		}
	}
}

// The keys Kubernetes stamps on every Job pod, Node and Namespace are not
// matched by name, so one letter does not return the whole list; their values
// still are.
func TestKeysKubernetesStampsOnEveryRowDoNotMatchByName(t *testing.T) {
	columns := []Column{col("Name"), col("Status"), numCol("Age")}
	// Cells and values with none of the letters searched, so only a key could match.
	cells := []Cell{{Text: "x"}, {Text: "Up"}, {Text: "3d"}}
	rows := map[string]map[string]string{
		"a Job's pod": {
			"job-name": "nightly-29400000", "batch.kubernetes.io/job-name": "nightly-29400000",
			"controller-uid": "u1", "batch.kubernetes.io/controller-uid": "u1",
		},
		"a Node": {
			"kubernetes.io/hostname": "w1", "kubernetes.io/os": "linux", "kubernetes.io/arch": "x86",
			"node.kubernetes.io/instance-type": "t3", "beta.kubernetes.io/arch": "x86",
			"beta.kubernetes.io/os": "linux", "topology.kubernetes.io/zone": "z1",
		},
		"a Namespace": {"kubernetes.io/metadata.name": "x"},
	}
	for what, labels := range rows {
		for _, q := range []string{"m", "e", "a", "name", "kubernetes.io", "job", "host"} {
			if NewMatcher(q, columns).Match("x", cells, labels) {
				t.Errorf("%q matched %s through a key Kubernetes stamps on every one of them", q, what)
			}
		}
	}
	if !NewMatcher("nightly", columns).Match("x", cells, rows["a Job's pod"]) {
		t.Error("a stamped key's value stopped being searchable")
	}
}

// The case of #739: pods labelled `network=redis` whose name and cells say
// nothing of redis are found by the label's value and by its key, and a pod
// labelled otherwise is not.
func TestAPodIsFoundByItsLabelKeyAndValue(t *testing.T) {
	columns := podColumns()
	cells := podCells("Running", "worker-1")
	redis := map[string]string{"network": "redis", "pod-template-hash": "5d8f7"}
	web := map[string]string{"network": "frontend", "pod-template-hash": "5d8f7"}
	for _, q := range []string{"redis", "REDIS", "network=redis"} {
		m := NewMatcher(q, columns)
		if !m.Match("cache-alpha", cells, redis) {
			t.Errorf("%q did not find the pod labelled network=redis", q)
		}
		if m.Match("web-gamma", cells, web) {
			t.Errorf("%q found the pod labelled network=frontend", q)
		}
	}
	if m := NewMatcher("network", columns); !m.Match("cache-alpha", cells, redis) || !m.Match("web-gamma", cells, web) {
		t.Error("`network` did not find both pods carrying the key network")
	}
}

// The box is plain text: `node:x` is the substring `node:x`.
//
// There is no `column:value` grammar typed into the box; a column's exact
// value is the filter control's, through Filters. So a query shaped like such
// a grammar is searched whole, and finds a row only where a cell or a label
// really contains it.
func TestAColumnAndAColonInTheBoxIsAPlainSearch(t *testing.T) {
	columns := podColumns()
	cells := podCells("Running", "ip-10-0-1-7")

	if NewMatcher("node:ip-10-0-1-7", columns).Match("checkout-7d9f", cells, nil) {
		t.Error("`node:ip-10-0-1-7` addressed the Node column from the search box; " +
			"there is no grammar there and the text is a substring like any other")
	}
	// And a colon that really is in a cell is found, with no special case.
	taint := []Column{col("Name"), numCol("Taints"), numCol("Age")}
	hinted := []Cell{{Text: "node-a"}, {Text: "2", Hint: "node-role.kubernetes.io/control-plane:NoSchedule"}, {Text: "9d"}}
	if !NewMatcher("control-plane:NoSchedule", taint).Match("node-a", hinted, nil) {
		t.Error("a colon inside a hint stopped being searchable")
	}
	// The substring rule: the bare node name still contains-matches.
	if !NewMatcher("ip-10-0-1-7", columns).Match("checkout-7d9f", podCells("Running", "ip-10-0-1-70"), nil) {
		t.Error("the bare search stopped matching a containing node name")
	}
}

func filterColumns() []Column {
	return []Column{
		col("Name"), col("Namespace"), filterCol("Status"), filterCol("Node"), numCol("Age"),
	}
}

func on(filters ...string) []Filter {
	out := make([]Filter, 0, len(filters))
	for _, raw := range filters {
		f, ok := ParseFilter(raw)
		if !ok {
			panic("not a filter: " + raw)
		}
		out = append(out, f)
	}
	return out
}

// A filter is a column's value, equal rather than contained, and the case it
// exists for is the one a substring search gets silently wrong: `ip-10-0-1-7` is
// contained in `ip-10-0-1-70`, and a list a link narrowed for the operator must
// not hold a pod from the wrong machine.
func TestAFilterNarrowsByExactCaseInsensitiveValue(t *testing.T) {
	m := NewMatcher("", filterColumns()).WithFilters(on("Node:ip-10-0-1-7"))
	if !m.Match("a", podCells("Running", "ip-10-0-1-7"), nil) {
		t.Error("the filter did not keep the row on the node it names")
	}
	if m.Match("a", podCells("Running", "ip-10-0-1-70"), nil) {
		t.Error("`Node:ip-10-0-1-7` kept the pod on ip-10-0-1-70 - contained rather than equal")
	}
	upper := NewMatcher("", filterColumns()).WithFilters(on("NODE:IP-10-0-1-7"))
	if !upper.Match("a", podCells("Running", "ip-10-0-1-7"), nil) {
		t.Error("the filter is case-sensitive on the column or the value")
	}
	if m.Empty() {
		t.Error("a filter alone reported an empty query, so a caller's fast path would skip it")
	}
}

// Several values of one column are OR-ed; several columns are AND-ed; and the
// search is a requirement beside them, never instead of them.
func TestFiltersAreOrWithinAColumnAndAndAcrossColumns(t *testing.T) {
	cases := []struct {
		search  string
		filters []string
		status  string
		node    string
		want    bool
		why     string
	}{
		{"", []string{"Node:worker-2", "Node:worker-3"}, "Running", "worker-3", true, "either node of two"},
		{"", []string{"Node:worker-2", "Node:worker-3"}, "Running", "worker-9", false, "neither node of two"},
		{"", []string{"Node:worker-2", "Status:Running"}, "Running", "worker-2", true, "this status, here"},
		{"", []string{"Node:worker-2", "Status:Running"}, "Pending", "worker-2", false, "here, another status"},
		{"", []string{"Node:worker-2", "Status:Running"}, "Running", "worker-3", false, "this status, elsewhere"},
		{"checkout", []string{"Node:worker-2"}, "Running", "worker-2", true, "a word and a filter both met"},
		{"payments-api", []string{"Node:worker-2"}, "Running", "worker-2", false, "the word dropped beside a filter"},
		{"checkout", []string{"Node:worker-9"}, "Running", "worker-2", false, "the filter dropped beside a word"},
	}
	for _, c := range cases {
		m := NewMatcher(c.search, filterColumns()).WithFilters(on(c.filters...))
		if got := m.Match("checkout-7d9f", podCells(c.status, c.node), nil); got != c.want {
			t.Errorf("%s: search %q filters %v on %s/%s = %v, want %v",
				c.why, c.search, c.filters, c.status, c.node, got, c.want)
		}
	}
}

// A filter on a column that cannot be filtered narrows nothing and is not
// echoed: not the figures (their text flickers every refresh), not the
// namespace (it has a control of its own), not a column the kind does not have,
// and not a column the descriptor did not flag.
func TestAFilterOnAColumnThatCannotBeFilteredIsDroppedAndNotEchoed(t *testing.T) {
	columns := append(filterColumns(), col("Image"))
	cells := append(podCells("Running", "worker-2"), Cell{Text: "nginx"})
	for _, raw := range []string{"Age:3d", "Namespace:elsewhere", "Colour:blue", "Image:redis"} {
		m := NewMatcher("", columns).WithFilters(on(raw))
		if !m.Match("checkout-7d9f", cells, nil) {
			t.Errorf("%q narrowed the list; it names a column that cannot be filtered", raw)
		}
		if len(m.Applied()) != 0 {
			t.Errorf("%q was echoed as applied: %+v - a chip would be drawn for a filter that did nothing",
				raw, m.Applied())
		}
	}
	// A repeat is one filter, echoed once, in the spelling first given.
	m := NewMatcher("", columns).WithFilters(on("Node:worker-2", "node:WORKER-2"))
	if got := m.Applied(); len(got) != 1 || got[0] != (Applied{Column: 3, Value: "worker-2"}) {
		t.Errorf("a repeated filter was echoed as %+v, want one, on column 3", got)
	}
}

// The parameter's own shape: split at the first colon, a value may hold more.
func TestParseFilterSplitsAtTheFirstColon(t *testing.T) {
	cases := []struct {
		raw  string
		want Filter
		ok   bool
	}{
		{"Node:worker-2", Filter{"Node", "worker-2"}, true},
		{"Storage class: gp3-encrypted", Filter{"Storage class", "gp3-encrypted"}, true},
		{"Image:registry:5000/app:1.2", Filter{"Image", "registry:5000/app:1.2"}, true},
		{"worker-2", Filter{}, false},
		{"Node:", Filter{}, false},
		{":worker-2", Filter{}, false},
	}
	for _, c := range cases {
		got, ok := ParseFilter(c.raw)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseFilter(%q) = %+v, %v; want %+v, %v", c.raw, got, ok, c.want, c.ok)
		}
	}
}

// A row shorter than its columns cannot answer a filter, and saying yes would
// be worse than saying no: the cell the filter names is not there.
func TestAFilterOnAShortRowDoesNotMatchAndDoesNotPanic(t *testing.T) {
	m := NewMatcher("", filterColumns()).WithFilters(on("Node:worker-2"))
	short := []Cell{{Text: "x"}, {Text: "ns"}}
	if m.Match("x", short, nil) {
		t.Error("a filter matched a row that does not carry the column it names")
	}
	offered := m.Offer()
	m.Keep("x", short, nil, offered)
	if len(offered.Values(3)) != 0 {
		t.Errorf("a short row offered %v for a column it does not have", offered.Values(3))
	}
}

// What the control offers for a column is what the rows hold once the search
// and every *other* column's filters have had their say - so choosing one node
// leaves the other nodes on the Node menu, which is the only way a second value
// of an OR can ever be chosen.
func TestTheOfferedValuesIgnoreTheirOwnColumnsFilter(t *testing.T) {
	rows := [][]Cell{
		podCells("Running", "worker-2"),
		podCells("Pending", "worker-2"),
		podCells("Running", "worker-3"),
		podCells("Failed", "worker-4"),
	}
	m := NewMatcher("", filterColumns()).WithFilters(on("Node:worker-2", "Status:Running"))
	offered := m.Offer()
	kept := 0
	for _, cells := range rows {
		if m.Keep("checkout-7d9f", cells, nil, offered) {
			kept++
		}
	}
	if kept != 1 {
		t.Errorf("%d rows kept, want 1 (Running on worker-2)", kept)
	}
	// Node's menu: the Running rows, whatever their node.
	if got := offered.Values(3); strings.Join(got, ",") != "worker-2,worker-3" {
		t.Errorf("Node offered %v, want [worker-2 worker-3]: the rows Status leaves", got)
	}
	// Status's menu: the worker-2 rows, whatever their status.
	if got := offered.Values(2); strings.Join(got, ",") != "Pending,Running" {
		t.Errorf("Status offered %v, want [Pending Running]: the rows Node leaves", got)
	}
	// A column that cannot be filtered is offered nothing at all - not an
	// empty menu, no menu.
	if _, ok := m.Offer()[4]; ok {
		t.Error("Age, a figure, was given a menu")
	}
	// And a row the search drops offers nothing: the menu is of this list.
	searched := NewMatcher("nothing-like-this", filterColumns())
	none := searched.Offer()
	searched.Keep("checkout-7d9f", rows[0], nil, none)
	if got := none.Values(3); got == nil || len(got) != 0 {
		t.Errorf("a row the search dropped offered %v; want an empty, non-nil list", got)
	}
}
