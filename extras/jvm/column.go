package jvm

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/thonicdev/kubetower/extension"
)

// ColumnCell is one cell of the "Java" column in the console's lists, the
// shape `@kubetower/ui`'s ExtensionCell declares.
type ColumnCell struct {
	Text string `json:"text"`
	// Tone is ok, warn or bad by the worst risk; empty is neutral (no limit,
	// or a heap the spec cannot size).
	Tone string `json:"tone,omitempty"`
	Hint string `json:"hint,omitempty"`
	Href string `json:"href,omitempty"`
}

// columnKinds is each list the column is drawn on, as the console's address
// spells it, and the owner a pod names for it: pods are keyed by their own
// name, a workload by the owner its pods name (ownerOf).
var columnKinds = map[string]string{
	"pods":         "",
	"deployments":  "Deployment",
	"statefulsets": "StatefulSet",
	"daemonsets":   "DaemonSet",
	"replicasets":  "ReplicaSet",
	"jobs":         "Job",
}

// severity orders risks for "the worst one": an OOMKill before a tight heap,
// then what cannot be graded, and room last.
func severity(level string) int {
	switch level {
	case riskError:
		return 4
	case riskWarning:
		return 3
	case riskUnknown:
		return 2
	case riskNoLimit:
		return 1
	}
	return 0
}

func toneOf(level string) string {
	switch level {
	case riskError:
		return "bad"
	case riskWarning:
		return "warn"
	case riskOK:
		return "ok"
	}
	return ""
}

// riskWords is the page's word for a risk, as data.ts's riskWord says it.
func riskWords(level string) string {
	switch level {
	case riskError:
		return "OOMKill risk"
	case riskWarning:
		return "tight"
	case riskOK:
		return "room"
	case riskNoLimit:
		return "no limit"
	}
	return "unknown"
}

// containerLine is one Java container in a hint: its heap against its limit.
func containerLine(pod string, c Container) string {
	heap := "a heap sized on the node"
	if c.Heap != nil && c.Heap.Text != "" {
		heap = "heap " + c.Heap.Text
	}
	limit := "no memory limit"
	if c.Limit != nil {
		limit = "limit " + c.Limit.Text
	}
	grade := riskWords(c.Risk.Level)
	if c.Risk.Percent != nil {
		grade = strconv.Itoa(*c.Risk.Percent) + " %, " + grade
	}
	name := c.Name
	if pod != "" {
		name = pod + "/" + c.Name
	}
	return name + ": " + heap + " of " + limit + " (" + grade + ")"
}

// pageHref is the JVM Inspector's page narrowed to one namespace and a search.
func pageHref(cluster, namespace, find string) string {
	q := url.Values{"ns": {namespace}, "q": {find}}
	return "/c/" + url.PathEscape(cluster) + "/x/" + Key + "?" + q.Encode()
}

// column answers `GET /column?kind=`: for every pod (or every workload owning
// pods) that is probably Java, a "Java" cell toned by its worst container's
// risk, keyed `namespace/name`. **One pod list for the whole list**, reviewed
// for the person, and no metrics: the cell is the spec's reading. A pod the
// person may not list has no cell, which the console draws empty.
func (JVM) column(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster, kind := r.PathValue("cluster"), r.URL.Query().Get("kind")
		owner, ok := columnKinds[kind]
		if !ok {
			writeProblem(w, http.StatusBadRequest, CodeBadRequest, "The Java column is drawn on pods and on the "+
				"workloads that own them, not on "+quote(kind)+".", nil)
			return
		}
		page, err := h.List(r, cluster, extension.ListQuery{GVR: podsGVR, Limit: MaxPodsRead})
		if err != nil {
			writeReadFailure(w, cluster, err)
			return
		}
		pods, _, _ := scan(page.Items)
		page.Items = nil

		type gathered struct {
			worst Container
			lines []string
			pods  int
		}
		by := map[string]*gathered{}
		var order []string
		for _, p := range pods {
			name := p.Name
			if owner != "" {
				if p.Owner == nil {
					continue
				}
				switch {
				case p.Owner.Kind == owner:
					name = p.Owner.Name
				case owner == "ReplicaSet" && p.Owner.ReplicaSet != "":
					name = p.Owner.ReplicaSet
				default:
					continue
				}
			}
			key := p.Namespace + "/" + name
			g := by[key]
			if g == nil {
				g = &gathered{}
				by[key] = g
				order = append(order, key)
			}
			g.pods++
			for _, c := range p.Containers {
				if c.Risk == nil {
					continue
				}
				if g.lines == nil || severity(c.Risk.Level) > severity(g.worst.Risk.Level) {
					g.worst = c
				}
				if owner == "" {
					g.lines = append(g.lines, containerLine("", c))
				} else {
					g.lines = append(g.lines, containerLine(p.Name, c))
				}
			}
		}

		out := make(map[string]ColumnCell, len(by))
		for _, key := range order {
			g := by[key]
			if g.lines == nil {
				continue
			}
			namespace, name, _ := strings.Cut(key, "/")
			hint := g.lines
			if len(hint) > 4 {
				hint = append(hint[:4:4], "and "+strconv.Itoa(len(g.lines)-4)+" more")
			}
			prefix := ""
			if owner != "" {
				prefix = strconv.Itoa(g.pods) + " Java " + plural(g.pods, "pod") + ". "
			}
			out[key] = ColumnCell{
				Text: "Java",
				Tone: toneOf(g.worst.Risk.Level),
				Hint: prefix + strings.Join(hint, "; "),
				Href: pageHref(cluster, namespace, name),
			}
		}
		writeJSON(w, http.StatusOK, out)
	})
}
