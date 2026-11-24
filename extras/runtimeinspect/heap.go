package runtimeinspect

import (
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/textutil"
)

// The JVM's own defaults the estimate follows (`java` man page, JDK 21, and
// HotSpot's heap sizing): with no -Xmx, the maximum heap is MaxRAMPercentage
// (25 %) of the memory the JVM sees, which in a container is its memory
// limit; where MinRAMPercentage (50 %) of that memory is under the default
// heap size, the memory is "small" and the heap is that 50 %; and otherwise
// the heap is never under the default heap size, about 125 MiB on a 64-bit
// JVM (96 MiB scaled for the word size).
const (
	defaultMaxRAMPercentage = 25.0
	defaultMinRAMPercentage = 50.0
	// defaultHeapSize is HotSpot's MaxHeapSize before ergonomics: 96 MiB × 1.3,
	// aligned down to 8 bytes.
	defaultHeapSize int64 = 130862280
	// riskShare is the heuristic share of the limit above which the heap
	// leaves too little room for the JVM's memory outside it. A choice of
	// this page, not a JDK rule, and every sentence that uses it says so.
	riskShare = 0.75
)

// The risk levels a container's reading carries.
const (
	riskOK      = "ok"
	riskWarning = "warning"
	riskError   = "error"
	// riskNoLimit: no memory limit, so the heap is sized on the node.
	riskNoLimit = "nolimit"
	// riskUnknown: a limit, and a heap sized on something the spec does not
	// say.
	riskUnknown = "unknown"
)

// How the maximum heap was arrived at.
const (
	heapExplicit   = "explicit"   // -Xmx or -XX:MaxHeapSize
	heapPercentage = "percentage" // -XX:MaxRAMPercentage of the limit
	heapDefault    = "default"    // 25 % of the limit
	heapSmall      = "small"      // MinRAMPercentage of a small limit
	heapFloor      = "floor"      // the default heap size, above 25 % of the limit
	heapNoLimit    = "nolimit"    // no limit: sized on the node
)

// Memory is a memory request or limit as the spec says it and in bytes.
type Memory struct {
	Bytes int64  `json:"bytes"`
	Text  string `json:"text"`
}

// Heap is the estimated maximum heap of one container.
type Heap struct {
	// Bytes is null when there is no memory limit and no -Xmx: the heap is
	// then sized on the node's memory, which the spec does not say.
	Bytes *int64 `json:"bytes"`
	// Text is Bytes as the page prints it, empty when Bytes is null.
	Text string `json:"text"`
	// From is how it was arrived at: explicit, percentage, default, small,
	// floor or nolimit.
	From string `json:"from"`
	// Source is the sentence: the option and where it was read, or the
	// default it follows.
	Source string `json:"source"`
}

// Risk is how close the maximum heap is to the memory limit.
type Risk struct {
	// Level is ok, warning, error, nolimit or unknown.
	Level string `json:"level"`
	// Percent is the heap's share of the limit, rounded; null without one.
	Percent *int `json:"percent"`
	// Sentence says what the level means, and that the 75 % line is a
	// heuristic.
	Sentence string `json:"sentence"`
}

// options is what the JVM would read from one container's spec, in the
// JVM's order: JAVA_TOOL_OPTIONS, then JDK_JAVA_OPTIONS, then the command
// line, the last value of a flag winning.
type options struct {
	// heap is the last -Xmx or -XX:MaxHeapSize, 0 when none; heapFlag the
	// flag as written and heapWhere where it was read.
	heap      int64
	heapFlag  string
	heapWhere string
	// maxPct and minPct are -XX:MaxRAMPercentage and -XX:MinRAMPercentage,
	// negative when not set.
	maxPct, minPct float64
	maxPctFlag     string
	maxPctWhere    string
	notes          []string
	maxRAMNoted    bool
	// containerSupportOff is -XX:-UseContainerSupport: the JVM does not read
	// its container's limit.
	containerSupportOff bool
}

// readOptions reads the heap options of c's spec as the JVM reads them.
// JAVA_OPTS is never one of them (detect.go): where it carries a heap option
// that is said in a note, and the estimate does not use it.
func readOptions(c corev1.Container) options {
	o := options{maxPct: -1, minPct: -1}
	env := map[string]corev1.EnvVar{}
	for _, e := range c.Env {
		env[e.Name] = e // the last of a name is the one the container gets
	}
	for _, name := range []string{envToolOptions, envJDKOptions} {
		e, ok := env[name]
		if !ok {
			continue
		}
		if e.ValueFrom != nil {
			o.notes = append(o.notes, name+" is set from a ConfigMap, a Secret or a field of the pod, "+
				"so the options in it are not read here.")
			continue
		}
		o.read(strings.Fields(e.Value), name)
	}
	o.read(commandLine(c), "the command line")
	if e, ok := env[envJavaOpts]; ok && e.ValueFrom == nil {
		for _, token := range strings.Fields(e.Value) {
			if strings.HasPrefix(token, "-Xmx") || strings.HasPrefix(token, "-XX:MaxHeapSize=") ||
				strings.HasPrefix(token, "-XX:MaxRAMPercentage=") {
				o.notes = append(o.notes, envJavaOpts+" carries "+quote(clip(token))+", which the JVM does not read: "+
					"it applies only if a launch script passes it on, and the estimate does not count it.")
				break
			}
		}
	}
	if o.containerSupportOff {
		o.notes = append(o.notes, "-XX:-UseContainerSupport is set: this JVM does not read its container's "+
			"limit, so a heap left to its defaults is sized on the node's memory instead.")
	}
	return o
}

// read takes the options of one source, in order.
func (o *options) read(tokens []string, where string) {
	for _, token := range tokens {
		switch {
		case strings.HasPrefix(token, "-Xmx"):
			o.heapOption(token, strings.TrimPrefix(token, "-Xmx"), where)
		case strings.HasPrefix(token, "-XX:MaxHeapSize="):
			o.heapOption(token, strings.TrimPrefix(token, "-XX:MaxHeapSize="), where)
		case strings.HasPrefix(token, "-XX:MaxRAMPercentage="):
			if pct, ok := percentage(strings.TrimPrefix(token, "-XX:MaxRAMPercentage=")); ok {
				o.maxPct, o.maxPctFlag, o.maxPctWhere = pct, token, where
			} else {
				o.unreadable(token, where)
			}
		case strings.HasPrefix(token, "-XX:MinRAMPercentage="):
			if pct, ok := percentage(strings.TrimPrefix(token, "-XX:MinRAMPercentage=")); ok {
				o.minPct = pct
			} else {
				o.unreadable(token, where)
			}
		case strings.HasPrefix(token, "-XX:MaxRAM="):
			if !o.maxRAMNoted {
				o.maxRAMNoted = true
				o.notes = append(o.notes, quote(clip(token))+" in "+where+" changes the memory the JVM sizes its heap on, "+
					"and is not taken into account here.")
			}
		case token == "-XX:-UseContainerSupport":
			o.containerSupportOff = true
		case token == "-XX:+UseContainerSupport":
			o.containerSupportOff = false
		}
	}
}

func (o *options) heapOption(token, size, where string) {
	n, ok := parseSize(size)
	if !ok {
		o.unreadable(token, where)
		return
	}
	o.heap, o.heapFlag, o.heapWhere = n, token, where
}

func (o *options) unreadable(token, where string) {
	o.notes = append(o.notes, quote(clip(token))+" in "+where+" could not be read as a size or a percentage, and is left out.")
}

// parseSize is a JVM size: digits, then nothing (bytes) or one of k, m, g
// and t, either case. Anything else is not a size the JVM would accept.
func parseSize(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	unit := int64(1)
	switch s[len(s)-1] {
	case 'k', 'K':
		unit = 1 << 10
	case 'm', 'M':
		unit = 1 << 20
	case 'g', 'G':
		unit = 1 << 30
	case 't', 'T':
		unit = 1 << 40
	}
	if unit != 1 {
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 || n > (1<<62)/unit {
		return 0, false
	}
	return n * unit, true
}

// percentage is a -XX:...RAMPercentage value: a number from 0 to 100.
func percentage(s string) (float64, bool) {
	pct, err := strconv.ParseFloat(s, 64)
	if err != nil || pct < 0 || pct > 100 {
		return 0, false
	}
	return pct, true
}

// expand is Kubernetes' own substitution of $(NAME) in a command or its
// arguments, from the container's literal environment: `args: ["$(JAVA_OPTS)"]`
// puts JAVA_OPTS on the command line, where the JVM does read it. `$$(NAME)`
// is the escape, kept as `$(NAME)`. A variable set from elsewhere is left as
// written.
func expand(parts []string, env map[string]corev1.EnvVar) []string {
	if len(parts) == 0 {
		return nil
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		var b strings.Builder
		for i := 0; i < len(part); {
			if strings.HasPrefix(part[i:], "$$(") {
				b.WriteString("$(")
				i += 3
				continue
			}
			if strings.HasPrefix(part[i:], "$(") {
				if end := strings.IndexByte(part[i+2:], ')'); end >= 0 {
					name := part[i+2 : i+2+end]
					if e, ok := env[name]; ok && e.ValueFrom == nil {
						b.WriteString(e.Value)
						i += 2 + end + 1
						continue
					}
				}
			}
			b.WriteByte(part[i])
			i++
		}
		out = append(out, b.String())
	}
	return out
}

// memoryOf is the container's memory request or limit, nil when not set.
func memoryOf(list corev1.ResourceList) *Memory {
	q, ok := list[corev1.ResourceMemory]
	if !ok {
		return nil
	}
	return &Memory{Bytes: q.Value(), Text: q.String()}
}

// estimate is the maximum heap the JVM would size from o under limit (nil:
// none), and the risk that leaves.
func estimate(o options, limit *Memory) (Heap, Risk) {
	var heap Heap
	switch {
	case o.heap > 0:
		n := o.heap
		heap = Heap{Bytes: &n, From: heapExplicit, Source: quote(o.heapFlag) + " in " + o.heapWhere}
	case limit == nil || limit.Bytes <= 0:
		heap = Heap{From: heapNoLimit, Source: "No memory limit: the JVM sizes its heap on the node's memory."}
	case o.containerSupportOff:
		heap = Heap{From: heapNoLimit, Source: "-XX:-UseContainerSupport: the JVM sizes its heap on the node's memory, not on its limit."}
	default:
		heap = ergonomic(o, limit.Bytes)
	}
	if heap.Bytes != nil {
		heap.Text = textutil.Bytes(*heap.Bytes)
	}
	return heap, risk(heap, limit)
}

// ergonomic is the heap HotSpot sizes with no -Xmx, from the limit.
func ergonomic(o options, limit int64) Heap {
	maxPct, minPct := defaultMaxRAMPercentage, defaultMinRAMPercentage
	if o.maxPct >= 0 {
		maxPct = o.maxPct
	}
	if o.minPct >= 0 {
		minPct = o.minPct
	}
	const caveat = " (a JVM that reads its container's limit: JDK 10 and later, 8u191 and later)"
	if small := int64(float64(limit) * minPct / 100); small < defaultHeapSize {
		return Heap{Bytes: &small, From: heapSmall,
			Source: "MinRAMPercentage, " + pct(minPct) + " of the memory limit, which the JVM uses for a small memory" + caveat}
	}
	n := int64(float64(limit) * maxPct / 100)
	if n < defaultHeapSize {
		floor := defaultHeapSize
		return Heap{Bytes: &floor, From: heapFloor,
			Source: "The JVM's default heap size, about " + textutil.Bytes(defaultHeapSize) + ", which is above " +
				pct(maxPct) + " of the memory limit" + caveat}
	}
	if o.maxPct >= 0 {
		return Heap{Bytes: &n, From: heapPercentage, Source: quote(o.maxPctFlag) + " in " + o.maxPctWhere + ", of the memory limit" + caveat}
	}
	return Heap{Bytes: &n, From: heapDefault, Source: "The JVM's default, " + pct(maxPct) + " of the memory limit" + caveat}
}

// risk grades heap against limit: an error when an explicit heap reaches the
// limit, a warning above riskShare of it, no grade without a limit, and
// unknown when the heap is sized on the node though a limit is set.
func risk(heap Heap, limit *Memory) Risk {
	if limit == nil || limit.Bytes <= 0 {
		return Risk{Level: riskNoLimit, Sentence: "No memory limit: the container is not killed for a limit of its " +
			"own, and the heap grows with what the node has rather than with what was planned for it."}
	}
	if heap.Bytes == nil {
		return Risk{Level: riskUnknown, Sentence: "The heap is sized on the node's memory, which the spec does not say: " +
			"how close it comes to the memory limit of " + textutil.Bytes(limit.Bytes) + " is unknown, and on a node with more " +
			"than four times that memory it can pass it."}
	}
	share := float64(*heap.Bytes) / float64(limit.Bytes)
	percent := int(share*100 + 0.5)
	size, of := textutil.Bytes(*heap.Bytes), textutil.Bytes(limit.Bytes)
	switch {
	case heap.From == heapExplicit && *heap.Bytes >= limit.Bytes:
		return Risk{Level: riskError, Percent: &percent, Sentence: "The heap may grow to " + size +
			", at or above the memory limit of " + of + ": the container is OOMKilled before Java ever throws " +
			"an OutOfMemoryError."}
	case share > riskShare:
		return Risk{Level: riskWarning, Percent: &percent, Sentence: "The heap may grow to " + size + ", " +
			strconv.Itoa(percent) + " % of the memory limit of " + of + ", which leaves little room for the JVM's " +
			"memory outside the heap: metaspace, code cache, thread stacks, buffers. The 75 % line is a " +
			"heuristic of this page, not a JDK rule."}
	}
	return Risk{Level: riskOK, Percent: &percent, Sentence: "The heap may grow to " + size + ", " +
		strconv.Itoa(percent) + " % of the memory limit of " + of + ", which leaves the rest for the JVM's memory " +
		"outside the heap. The 75 % line is a heuristic of this page, not a JDK rule."}
}

func pct(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64) + " %"
}
