package runtimeinspect

import (
	"regexp"
	"strconv"
	"strings"
)

// The parsers of what each diagnostic command answers, read on the bodies
// body() returns. Each is tested on the outputs of JDK 8, 11, 17 and 21
// (testdata/). A figure a parser could not find is absent - a nil or a false -
// and never a zero.

// version is VM.version: the VM line and the JDK line.
type version struct {
	VM    string
	JDK   string
	Major int
}

func parseVersion(lines []string) version {
	var out version
	for _, line := range lines {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "JDK "):
			out.JDK = line
			out.Major = major(strings.TrimPrefix(line, "JDK "))
		case out.VM == "":
			out.VM = line
		}
	}
	return out
}

// major is the feature release of a version string: 8 for `8.0_512` and
// `1.8.0_512`, 21 for `21.0.13`.
func major(v string) int {
	v = strings.TrimPrefix(v, "1.")
	end := strings.IndexFunc(v, func(r rune) bool { return r < '0' || r > '9' })
	if end >= 0 {
		v = v[:end]
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

// parseUptime is VM.uptime's seconds: `49.776 s`.
func parseUptime(lines []string) (float64, bool) {
	for _, line := range lines {
		head, ok := strings.CutSuffix(strings.TrimSpace(line), " s")
		if !ok {
			continue
		}
		if v, err := strconv.ParseFloat(head, 64); err == nil && v >= 0 {
			return v, true
		}
	}
	return 0, false
}

// flag is one option VM.flags printed: `-XX:MaxHeapSize=536870912` is
// MaxHeapSize with its value, `-XX:+UseG1GC` is UseG1GC with "true".
type flag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// parseFlags is VM.flags, in the order printed. It lists the flags set on the
// command line or by the JVM's ergonomics, not every default.
func parseFlags(lines []string) []flag {
	var out []flag
	for _, line := range lines {
		for _, tok := range strings.Fields(line) {
			opt, ok := strings.CutPrefix(tok, "-XX:")
			if !ok || opt == "" {
				continue
			}
			switch {
			case opt[0] == '+':
				out = append(out, flag{Name: opt[1:], Value: "true"})
			case opt[0] == '-':
				out = append(out, flag{Name: opt[1:], Value: "false"})
			default:
				name, value, _ := strings.Cut(opt, "=")
				out = append(out, flag{Name: name, Value: value})
			}
		}
	}
	return out
}

func flagValue(flags []flag, name string) (string, bool) {
	for _, f := range flags {
		if f.Name == name {
			return f.Value, true
		}
	}
	return "", false
}

// The collectors, by the flag that selects them.
var collectors = []struct{ flag, name string }{
	{"UseG1GC", "G1"},
	{"UseParallelGC", "Parallel"},
	{"UseSerialGC", "Serial"},
	{"UseZGC", "ZGC"},
	{"UseShenandoahGC", "Shenandoah"},
	{"UseConcMarkSweepGC", "CMS"},
	{"UseEpsilonGC", "Epsilon"},
}

// collector is the GC in use, from VM.flags; empty when no flag says.
func collector(flags []flag) string {
	for _, c := range collectors {
		if v, ok := flagValue(flags, c.flag); ok && v == "true" {
			if c.name == "ZGC" {
				if g, ok := flagValue(flags, "ZGenerational"); ok && g == "true" {
					return "ZGC (generational)"
				}
			}
			return c.name
		}
	}
	return ""
}

// vmCommandLine is VM.command_line: the JVM's options and the command it ran.
type vmCommandLine struct {
	JVMArgs     []string
	JavaCommand []string
}

func parseCommandLine(lines []string) vmCommandLine {
	var out vmCommandLine
	for _, line := range lines {
		if v, ok := strings.CutPrefix(line, "jvm_args:"); ok {
			out.JVMArgs = strings.Fields(v)
		}
		if v, ok := strings.CutPrefix(line, "java_command:"); ok {
			out.JavaCommand = strings.Fields(v)
		}
	}
	return out
}

// property is one system property, unescaped.
type property struct {
	Key   string
	Value string
}

// parseProperties is VM.system_properties, a properties file: `#` comments,
// `key=value`, `\=` and `\:` escaped.
func parseProperties(lines []string) []property {
	var out []property
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" || trimmed[0] == '#' || trimmed[0] == '!' {
			continue
		}
		key, value := splitProperty(trimmed)
		out = append(out, property{Key: unescape(key), Value: unescape(value)})
	}
	return out
}

// splitProperty cuts a line at its first unescaped `=` or `:`.
func splitProperty(line string) (key, value string) {
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '\\':
			i++
		case '=', ':':
			return line[:i], line[i+1:]
		}
	}
	return line, ""
}

func unescape(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 == len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'f':
			b.WriteByte('\f')
		case 'u':
			if i+4 < len(s) {
				if r, err := strconv.ParseUint(s[i+1:i+5], 16, 32); err == nil {
					b.WriteRune(rune(r))
					i += 4
					continue
				}
			}
			b.WriteByte('u')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// size is a figure in bytes as jcmd prints it: `1740K`, `1655Kb`, `512M`,
// `98002KB`, `3G`, `704 bytes`.
func size(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, " bytes")
	mult := int64(1)
	lower := strings.ToLower(s)
	for _, u := range []struct {
		suffix string
		mult   int64
	}{{"kb", 1 << 10}, {"mb", 1 << 20}, {"gb", 1 << 30}, {"k", 1 << 10}, {"m", 1 << 20}, {"g", 1 << 30}, {"b", 1}} {
		if strings.HasSuffix(lower, u.suffix) {
			s, mult = s[:len(s)-len(u.suffix)], u.mult
			break
		}
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n * mult, true
}

// heapInfo is what GC.heap_info says of the heap and of metaspace.
type heapInfo struct {
	// Recognized is false when no line was a heap layout this parser knows:
	// the heap's figures are then unknown, never zero.
	Recognised bool
	Used       int64
	Committed  int64
	// Metaspace is false when the Metaspace line is absent.
	Metaspace          bool
	MetaspaceUsed      int64
	MetaspaceCommitted int64
	MetaspaceReserved  int64
}

var (
	// A generation or G1's heap: `garbage-first heap   total 34816K, used 1740K`,
	// `PSYoungGen      total 9728K, used 2614K`, `def new generation   total 2432K, used 408K`.
	generationLine = regexp.MustCompile(`^\s*\S.*?\s+total (\d+[KMG]), used (\d+[KMG])`)
	// JDK 23 and later: `garbage-first heap   total reserved 524288K, committed 34816K, used 2974K`.
	reservedLine = regexp.MustCompile(`^\s*\S.*?\s+total reserved \d+[KMG], committed (\d+[KMG]), used (\d+[KMG])`)
	// Shenandoah: `  524288K max, 524288K soft max, 34816K committed, 2973K used`.
	shenandoahLine = regexp.MustCompile(`(\d+[KMG]) committed, (\d+[KMG]) used`)
	// ZGC: ` ZHeap           used 8M, capacity 32M, max capacity 512M`.
	zLine = regexp.MustCompile(`ZHeap\s+used (\d+[KMG]), capacity (\d+[KMG])`)
	// `Metaspace       used 4103K, capacity 4674K, committed 4864K, reserved 1056768K`
	// (JDK 8 and 11) or `Metaspace       used 524K, committed 704K, reserved 1114112K`.
	metaspaceLine = regexp.MustCompile(`^\s*Metaspace\s+used (\d+[KMG]),(?: capacity \d+[KMG],)? committed (\d+[KMG]), reserved (\d+[KMG])`)
)

func parseHeapInfo(lines []string) heapInfo {
	var out heapInfo
	for _, line := range lines {
		if m := metaspaceLine.FindStringSubmatch(line); m != nil {
			u, ok1 := size(m[1])
			c, ok2 := size(m[2])
			r, ok3 := size(m[3])
			if ok1 && ok2 && ok3 {
				out.Metaspace, out.MetaspaceUsed, out.MetaspaceCommitted, out.MetaspaceReserved = true, u, c, r
			}
			continue
		}
		var used, committed string
		if m := generationLine.FindStringSubmatch(line); m != nil {
			committed, used = m[1], m[2]
		} else if m := reservedLine.FindStringSubmatch(line); m != nil {
			committed, used = m[1], m[2]
		} else if m := shenandoahLine.FindStringSubmatch(line); m != nil {
			committed, used = m[1], m[2]
		} else if m := zLine.FindStringSubmatch(line); m != nil {
			used, committed = m[1], m[2]
		} else {
			continue
		}
		u, ok1 := size(used)
		c, ok2 := size(committed)
		if ok1 && ok2 {
			out.Recognised = true
			out.Used += u
			out.Committed += c
		}
	}
	return out
}

// codeCache is what Compiler.codecache says: the code cache's used and
// reserved bytes.
type codeCache struct {
	Recognised bool
	Used       int64
	Size       int64
}

var (
	// JDK 17 and later: `CodeCache: size=245760Kb, used=1655Kb, ...`.
	codeCacheTotal = regexp.MustCompile(`^CodeCache: size=(\d+Kb),? used=(\d+Kb)`)
	// Every JDK from 11, one line per segment:
	// `CodeHeap 'non-profiled nmethods': size=120036Kb used=43Kb ...`.
	codeHeapLine = regexp.MustCompile(`^CodeHeap '[^']*': size=(\d+Kb) used=(\d+Kb)`)
)

func parseCodeCache(lines []string) codeCache {
	var total, heaps codeCache
	for _, line := range lines {
		if m := codeCacheTotal.FindStringSubmatch(line); m != nil {
			s, ok1 := size(m[1])
			u, ok2 := size(m[2])
			if ok1 && ok2 {
				total = codeCache{Recognised: true, Used: u, Size: s}
			}
			continue
		}
		if m := codeHeapLine.FindStringSubmatch(line); m != nil {
			s, ok1 := size(m[1])
			u, ok2 := size(m[2])
			if ok1 && ok2 {
				heaps.Recognised = true
				heaps.Size += s
				heaps.Used += u
			}
		}
	}
	if total.Recognised {
		return total
	}
	return heaps
}

// nmtCategory is one category of VM.native_memory summary.
type nmtCategory struct {
	Name      string
	Reserved  int64
	Committed int64
}

// nmt is VM.native_memory summary.
type nmt struct {
	Recognised     bool
	TotalReserved  int64
	TotalCommitted int64
	Categories     []nmtCategory
}

var (
	nmtTotal        = regexp.MustCompile(`^Total: reserved=(\d+[KMG]?B), committed=(\d+[KMG]?B)`)
	nmtCategoryLine = regexp.MustCompile(`^-\s+(.+?) \(reserved=(\d+[KMG]?B), committed=(\d+[KMG]?B)`)
)

func parseNMT(lines []string) nmt {
	var out nmt
	for _, line := range lines {
		if m := nmtTotal.FindStringSubmatch(line); m != nil {
			r, ok1 := size(m[1])
			c, ok2 := size(m[2])
			if ok1 && ok2 {
				out.Recognised, out.TotalReserved, out.TotalCommitted = true, r, c
			}
			continue
		}
		if m := nmtCategoryLine.FindStringSubmatch(line); m != nil {
			r, ok1 := size(m[2])
			c, ok2 := size(m[3])
			if ok1 && ok2 {
				out.Categories = append(out.Categories, nmtCategory{Name: strings.TrimSpace(m[1]), Reserved: r, Committed: c})
			}
		}
	}
	return out
}

// metaspaceBasic is the committed and used metaspace VM.metaspace basic
// reports on its `Both:` lines; used is absent from JDK 11's virtual-space
// line and read from its usage line.
type metaspaceBasic struct {
	Recognised bool
	Used       int64
	Committed  int64
}

var (
	// JDK 17+: `       Both:    524.59 KB used.`; JDK 11:
	// `       Both:      4.41 MB capacity,   535.27 KB ( 12%) used, ...`.
	basicUsed = regexp.MustCompile(`^\s*Both:.*?([\d.]+ (?:bytes|KB|MB|GB))(?: \([^)]*\))? used`)
	// `             Both:        1.06 GB reserved,     704.00 KB ( <1%) committed.`
	basicCommitted = regexp.MustCompile(`^\s*Both:\s+[\d.]+ (?:bytes|KB|MB|GB) reserved,\s+([\d.]+ (?:bytes|KB|MB|GB))(?: \([^)]*\))? committed`)
)

func parseMetaspaceBasic(lines []string) metaspaceBasic {
	var out metaspaceBasic
	var haveUsed, haveCommitted bool
	for _, line := range lines {
		if m := basicCommitted.FindStringSubmatch(line); m != nil {
			out.Committed, haveCommitted = decimalSize(m[1])
			continue
		}
		if m := basicUsed.FindStringSubmatch(line); m != nil {
			out.Used, haveUsed = decimalSize(m[1])
		}
	}
	out.Recognised = haveUsed && haveCommitted
	return out
}

// decimalSize is `524.59 KB` or `704 bytes`, in bytes.
func decimalSize(s string) (int64, bool) {
	num, unit, ok := strings.Cut(strings.TrimSpace(s), " ")
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseFloat(num, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	mult := map[string]float64{"bytes": 1, "KB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30}[unit]
	if mult == 0 {
		return 0, false
	}
	return int64(v * mult), true
}
