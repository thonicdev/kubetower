package runtimeinspect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/textutil"
)

// Reading `jcmd <pid> GC.class_histogram`: the classes whose instances hold
// the most bytes, parsed here so the page draws bars and never parses text.
//
// **It runs a full GC** and the application pauses while it does: the route
// answers nothing unless the request says it was confirmed (`confirmed`), and
// the page asks first. The command is sent without `-all`, so the unreachable
// objects are collected before they are counted and the figures are the live
// ones; the flag is never sent (jcmd.go).
//
// The format is HotSpot's, sorted by bytes, largest first:
//
//	 num     #instances         #bytes  class name (module)
//	-------------------------------------------------------
//	   1:         21342        1003808  [I (java.base@21.0.13)
//	 ...
//	Total         87714        3339720
//
// JDK 8 to 11 write no module. A class name is the application's, so a
// histogram is never logged, and a name is clipped, never interpreted.

const (
	// MaxHistogramRows is the most classes one answer lists, the largest
	// first; the totals cover every class read.
	MaxHistogramRows = 50
	maxClassName     = 200
)

// classRow is one class of the histogram.
type classRow struct {
	Rank      int    `json:"rank"`
	Name      string `json:"name"`
	Module    string `json:"module,omitempty"`
	Instances int64  `json:"instances"`
	Bytes     int64  `json:"bytes"`
	// Share is Bytes as a percentage of the heap's histogram total, to one
	// decimal: the page draws the bar from it.
	Share float64 `json:"share"`
}

// Histogram is a parsed class histogram.
type Histogram struct {
	// Classes is how many classes the output listed.
	Classes   int   `json:"classes"`
	Instances int64 `json:"instances"`
	Bytes     int64 `json:"bytes"`
	// Rows are the first MaxHistogramRows classes by bytes.
	Rows []classRow `json:"rows"`
	// Cut is set when Classes is above MaxHistogramRows.
	Cut bool `json:"cut,omitempty"`
}

// parseHistogram reads out in one pass without splitting it into a slice of
// lines: memory stays at the top rows however long the output is.
func parseHistogram(out []byte) Histogram {
	h := Histogram{Rows: []classRow{}}
	var sumInstances, sumBytes int64
	var totalInstances, totalBytes int64 = -1, -1
	for len(out) > 0 {
		var line []byte
		if i := bytes.IndexByte(out, '\n'); i >= 0 {
			line, out = out[:i], out[i+1:]
		} else {
			line, out = out, nil
		}
		var buf [5][]byte
		f := splitFields(line, buf[:0])
		if len(f) == 3 && string(f[0]) == "Total" {
			a, e1 := strconv.ParseInt(string(f[1]), 10, 64)
			b, e2 := strconv.ParseInt(string(f[2]), 10, 64)
			if e1 == nil && e2 == nil {
				totalInstances, totalBytes = a, b
			}
			continue
		}
		if len(f) < 4 || len(f[0]) < 2 || f[0][len(f[0])-1] != ':' {
			continue
		}
		rank, err := strconv.Atoi(string(f[0][:len(f[0])-1]))
		inst, e1 := strconv.ParseInt(string(f[1]), 10, 64)
		size, e2 := strconv.ParseInt(string(f[2]), 10, 64)
		if err != nil || e1 != nil || e2 != nil || inst < 0 || size < 0 {
			continue
		}
		h.Classes++
		sumInstances += inst
		sumBytes += size
		if len(h.Rows) < MaxHistogramRows {
			name, module := className(string(f[3])), ""
			if len(f) > 4 {
				module = moduleOf(string(f[4]))
			}
			h.Rows = append(h.Rows, classRow{Rank: rank, Name: textutil.Clip(name, maxClassName), Module: module,
				Instances: inst, Bytes: size})
		}
	}
	h.Instances, h.Bytes = sumInstances, sumBytes
	if totalBytes >= 0 {
		// The JVM's own total wins: it is there only when the output was whole.
		h.Instances, h.Bytes = totalInstances, totalBytes
	}
	h.Cut = h.Classes > MaxHistogramRows
	for i := range h.Rows {
		if h.Bytes > 0 {
			h.Rows[i].Share = float64(h.Rows[i].Bytes*1000/h.Bytes) / 10
		}
	}
	return h
}

// splitFields splits line on spaces and tabs into dst's array, five fields at
// most (the rest of a class's line is never read), without allocating.
func splitFields(line []byte, dst [][]byte) [][]byte {
	for len(dst) < cap(dst) {
		for len(line) > 0 && (line[0] == ' ' || line[0] == '\t' || line[0] == '\r') {
			line = line[1:]
		}
		if len(line) == 0 {
			break
		}
		end := 0
		for end < len(line) && line[end] != ' ' && line[end] != '\t' && line[end] != '\r' {
			end++
		}
		dst = append(dst, line[:end])
		line = line[end:]
	}
	return dst
}

// arrayKinds is the JVM's one-letter element types.
var arrayKinds = map[byte]string{'B': "byte", 'C': "char", 'D': "double", 'F': "float", 'I': "int", 'J': "long",
	'S': "short", 'Z': "boolean"}

// className is a class as Java writes it: `[I` is `int[]` and
// `[Ljava.lang.Object;` is `java.lang.Object[]`. Anything else is left as is.
func className(raw string) string {
	dims := 0
	for dims < len(raw) && raw[dims] == '[' {
		dims++
	}
	if dims == 0 || dims == len(raw) {
		return raw
	}
	rest := raw[dims:]
	var elem string
	switch {
	case len(rest) == 1 && arrayKinds[rest[0]] != "":
		elem = arrayKinds[rest[0]]
	case rest[0] == 'L' && strings.HasSuffix(rest, ";"):
		elem = rest[1 : len(rest)-1]
	default:
		return raw
	}
	return elem + strings.Repeat("[]", dims)
}

// moduleOf is `java.base` out of `(java.base@21.0.13)`, empty for a class
// with none (`(unnamed module)` splits into two fields and is never reached).
func moduleOf(field string) string {
	field = strings.TrimSuffix(strings.TrimPrefix(field, "("), ")")
	if i := strings.IndexByte(field, '@'); i >= 0 {
		field = field[:i]
	}
	if field == "" || field == "unnamed" {
		return ""
	}
	return textutil.Clip(field, 80)
}

// histogramAnswer is `POST /pod/{namespace}/{name}/{container}/histogram`.
type histogramAnswer struct {
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	Container string `json:"container"`
	// State is `histogram`, or `choose` when jcmd lists several JVMs and no
	// `?pid=` chose one: JVMs is then the list, and no GC was run.
	State     string      `json:"state"`
	JVMs      []listedJVM `json:"jvms,omitempty"`
	PID       int         `json:"pid,omitempty"`
	PIDFrom   string      `json:"pidFrom,omitempty"`
	Inherited bool        `json:"inherited,omitempty"`
	*Histogram
	// OutputCut is set when jcmd's output reached Host.Exec's limit: the
	// classes after it are not counted, so the totals are short.
	OutputCut bool `json:"outputCut,omitempty"`
}

// histogramRequest is the body: the person confirmed the full GC.
type histogramRequest struct {
	Confirmed bool `json:"confirmed"`
}

func (JVM) histogram(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chosen, err := pidParam(r)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, CodeBadRequest, err.Error(), nil)
			return
		}
		var req histogramRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil || !req.Confirmed {
			writeProblem(w, http.StatusBadRequest, CodeNotConfirmed, "A class histogram runs a full GC: say it was confirmed.", nil)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), histogramBudget)
		defer cancel()
		r = r.WithContext(ctx)
		t, ok := resolve(w, r, h)
		if !ok {
			return
		}
		run := t.runner(h, r)
		out := histogramAnswer{Cluster: t.cluster, Namespace: t.namespace, Pod: t.name, Container: t.container,
			State: "histogram"}
		pid, from, jvms, ok := pick(w, run, t.cluster, chosen)
		if !ok {
			return
		}
		if len(jvms) > 0 {
			out.State, out.JVMs, out.Inherited = "choose", jvms, run.plain
			writeJSON(w, http.StatusOK, out)
			return
		}
		if ctx.Err() != nil {
			return
		}
		out.PID, out.PIDFrom = pid, from
		res, err := run.dcmdWithin(pid, dcmdHistogram, histogramTimeout)
		if !answered(w, run, t.cluster, pid, from, dcmdHistogram, res, err) {
			return
		}
		if r.Context().Err() != nil {
			return
		}
		hist := parseHistogram(res.Stdout)
		out.Histogram, out.OutputCut, out.Inherited = &hist, res.Truncated, run.plain
		if hist.Classes == 0 {
			// jcmd answered and listed no class: the JVM said something else.
			writeProblem(w, http.StatusBadGateway, CodeJcmdFailed, fmt.Sprintf("jcmd %d GC.class_histogram listed no class.", pid),
				errors.New(said(res)))
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
}
