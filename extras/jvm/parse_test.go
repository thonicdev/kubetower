package jvm

import (
	"os"
	"path/filepath"
	"testing"
)

// The fixtures are what jcmd printed on stdout for JVMs of JDK 8, 11, 17 and
// 21, started with -Xmx512m, G1 (8: Parallel, its default), and a password in
// a -D option and in the program's arguments; `-serial` is Serial GC in a
// 512 MiB container, `-off` native memory tracking off.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var jdks = []string{"jdk8", "jdk11", "jdk17", "jdk21"}

const k = int64(1024)

func TestTheVersionIsReadOnEveryJDK(t *testing.T) {
	want := map[string]struct {
		vm, jdk string
		major   int
	}{
		"jdk8":  {"OpenJDK 64-Bit Server VM version 25.512-b09", "JDK 8.0_512", 8},
		"jdk11": {"OpenJDK 64-Bit Server VM version 11.0.33+10", "JDK 11.0.33", 11},
		"jdk17": {"OpenJDK 64-Bit Server VM version 17.0.21+9", "JDK 17.0.21", 17},
		"jdk21": {"OpenJDK 64-Bit Server VM version 21.0.13+9-LTS", "JDK 21.0.13", 21},
	}
	for _, jdk := range jdks {
		got := parseVersion(body(fixture(t, jdk+"-VM-version")))
		if w := want[jdk]; got.VM != w.vm || got.JDK != w.jdk || got.Major != w.major {
			t.Errorf("%s: %+v, want %+v", jdk, got, w)
		}
		if secs, ok := parseUptime(body(fixture(t, jdk+"-VM-uptime"))); !ok || secs <= 0 {
			t.Errorf("%s uptime: %v %v", jdk, secs, ok)
		}
	}
	if got := major("1.8.0_402"); got != 8 {
		t.Errorf("major(1.8.0_402) = %d", got)
	}
}

func TestTheFlagsNameTheCollectorAndTheMaximumHeap(t *testing.T) {
	want := map[string]string{"jdk8": "Parallel", "jdk11": "G1", "jdk17": "G1", "jdk21": "G1"}
	for _, jdk := range jdks {
		flags := parseFlags(body(fixture(t, jdk+"-VM-flags")))
		if got := collector(flags); got != want[jdk] {
			t.Errorf("%s: collector %q, want %q", jdk, got, want[jdk])
		}
		if v, ok := flagValue(flags, "MaxHeapSize"); !ok || v != "536870912" {
			t.Errorf("%s: MaxHeapSize %q %v", jdk, v, ok)
		}
		if v, ok := flagValue(flags, "UseCompressedOops"); !ok || v != "true" {
			t.Errorf("%s: UseCompressedOops %q %v", jdk, v, ok)
		}
	}
	z := parseFlags([]string{"-XX:+UseZGC -XX:+ZGenerational -XX:-UseCompressedOops"})
	if got := collector(z); got != "ZGC (generational)" {
		t.Errorf("collector = %q", got)
	}
	if v, _ := flagValue(z, "UseCompressedOops"); v != "false" {
		t.Errorf("-XX:-UseCompressedOops read as %q", v)
	}
}

func TestTheHeapAndMetaspaceAreReadForEveryCollector(t *testing.T) {
	for _, c := range []struct {
		name                          string
		used, committed, mUsed, mComm int64
	}{
		{"jdk8-GC-heap_info", 2614 * k, (9728 + 22016) * k, 4103 * k, 4864 * k},
		{"jdk8-GC-heap_info-serial", 408 * k, (2432 + 5504) * k, 2661 * k, 4864 * k},
		{"jdk11-GC-heap_info", 1740 * k, 34816 * k, 535 * k, 4864 * k},
		{"jdk11-GC-heap_info-serial", 738 * k, (2432 + 5504) * k, 169 * k, 4864 * k},
		{"jdk17-GC-heap_info", 2463 * k, 34816 * k, 413 * k, 576 * k},
		{"jdk17-GC-heap_info-serial", 690 * k, (2432 + 5504) * k, 126 * k, 320 * k},
		{"jdk21-GC-heap_info", 2974 * k, 34816 * k, 524 * k, 704 * k},
		{"jdk21-GC-heap_info-serial", (277 + 1073) * k, (2432 + 5504) * k, 73 * k, 320 * k},
	} {
		got := parseHeapInfo(body(fixture(t, c.name)))
		if !got.Recognised || got.Used != c.used || got.Committed != c.committed {
			t.Errorf("%s heap: %+v, want used %d committed %d", c.name, got, c.used, c.committed)
		}
		if !got.Metaspace || got.MetaspaceUsed != c.mUsed || got.MetaspaceCommitted != c.mComm {
			t.Errorf("%s metaspace: %+v, want used %d committed %d", c.name, got, c.mUsed, c.mComm)
		}
	}
}

// The layouts no fixture holds, from the JDK's own printers, and one this
// parser does not know: unrecognized, never zero.
func TestOtherHeapLayouts(t *testing.T) {
	for _, c := range []struct {
		name            string
		lines           []string
		used, committed int64
	}{
		{"shenandoah", []string{"Shenandoah Heap", " 524288K max, 524288K soft max, 34816K committed, 2973K used"}, 2973 * k, 34816 * k},
		{"zgc", []string{" ZHeap           used 8M, capacity 32M, max capacity 512M"}, 8 << 20, 32 << 20},
		{"g1, jdk 23", []string{" garbage-first heap   total reserved 524288K, committed 34816K, used 2974K [0x0, 0x1)"}, 2974 * k, 34816 * k},
		{"cms", []string{" par new generation   total 4928K, used 1000K [0x0)", " concurrent mark-sweep generation total 10944K, used 0K [0x0)"}, 1000 * k, (4928 + 10944) * k},
	} {
		got := parseHeapInfo(c.lines)
		if !got.Recognised || got.Used != c.used || got.Committed != c.committed {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	if got := parseHeapInfo([]string{" some future heap   12M in use"}); got.Recognised {
		t.Errorf("an unknown layout was read: %+v", got)
	}
}

func TestTheCodeCacheIsReadFromJDK11On(t *testing.T) {
	for _, c := range []struct {
		jdk        string
		used, size int64
	}{
		{"jdk11", (43 + 240 + 1079) * k, (120036 + 120032 + 5692) * k},
		{"jdk17", (36 + 179 + 1091) * k, (120036 + 120032 + 5692) * k},
		{"jdk21", 1655 * k, 245760 * k},
	} {
		got := parseCodeCache(body(fixture(t, c.jdk+"-Compiler-codecache")))
		if !got.Recognised || got.Used != c.used || got.Size != c.size {
			t.Errorf("%s: %+v, want used %d size %d", c.jdk, got, c.used, c.size)
		}
	}
	if got := parseCodeCache(body(fixture(t, "jdk8-Compiler-codecache"))); got.Recognised {
		t.Errorf("JDK 8's refusal was read as a code cache: %+v", got)
	}
}

func TestTheMetaspaceBasicReportIsReadFromJDK11On(t *testing.T) {
	for _, c := range []struct {
		jdk             string
		used, committed int64
	}{
		{"jdk11", 548116, 4980736},
		{"jdk17", 423833, 576 * k},
		{"jdk21", 537180, 704 * k},
	} {
		got := parseMetaspaceBasic(body(fixture(t, c.jdk+"-VM-metaspace_basic")))
		if !got.Recognised || got.Used != c.used || got.Committed != c.committed {
			t.Errorf("%s: %+v, want used %d committed %d", c.jdk, got, c.used, c.committed)
		}
	}
}

func TestNativeMemoryTrackingIsReadOrSaidToBeOff(t *testing.T) {
	for _, c := range []struct {
		jdk       string
		committed int64
	}{{"jdk17", 96728 * k}, {"jdk21", 98002 * k}} {
		got := parseNMT(body(fixture(t, c.jdk+"-VM-native_memory_summary")))
		if !got.Recognised || got.TotalCommitted != c.committed || len(got.Categories) < 15 {
			t.Fatalf("%s: %+v", c.jdk, got)
		}
		if heap := got.Categories[0]; heap.Name != "Java Heap" || heap.Committed != 34816*k || heap.Reserved != 524288*k {
			t.Errorf("%s: first category %+v", c.jdk, heap)
		}
		var cds bool
		for _, cat := range got.Categories {
			cds = cds || (cat.Name == "Shared class space" && cat.Committed > 0)
		}
		if !cds {
			t.Errorf("%s: the shared class space, with its readonly figure, was not read", c.jdk)
		}
	}
	for _, jdk := range jdks {
		out := fixture(t, jdk+"-VM-native_memory_summary-off")
		if parseNMT(body(out)).Recognised {
			t.Errorf("%s: NMT off was read as a breakdown", jdk)
		}
	}
	scaled := parseNMT([]string{"Total: reserved=1493MB, committed=33MB", "-    Java Heap (reserved=128MB, committed=8MB)",
		"-   Some New Thing (reserved=2MB, committed=1MB)"})
	if scaled.TotalCommitted != 33<<20 || len(scaled.Categories) != 2 || scaled.Categories[1].Committed != 1<<20 {
		t.Errorf("scale=MB: %+v", scaled)
	}
}

func TestTheCommandLineAndPropertiesAreRead(t *testing.T) {
	for _, jdk := range jdks {
		cl := parseCommandLine(body(fixture(t, jdk+"-VM-command_line")))
		if len(cl.JVMArgs) != 4 || cl.JVMArgs[1] != "-Dspring.datasource.password=hunter2" ||
			len(cl.JavaCommand) != 2 || cl.JavaCommand[1] != "--db.password=hunter2" {
			t.Errorf("%s: %+v", jdk, cl)
		}
		props := map[string]string{}
		for _, p := range parseProperties(body(fixture(t, jdk+"-VM-system_properties"))) {
			props[p.Key] = p.Value
		}
		if props["spring.datasource.password"] != "hunter2" || props["sun.java.command"] != "D --db.password=hunter2" ||
			props["app.name"] != "shop" || props["java.version"] == "" {
			t.Errorf("%s: %d properties, %q %q", jdk, len(props), props["sun.java.command"], props["java.version"])
		}
		if _, ok := props["#Wed Nov 11 11:03:18 UTC 2026"]; ok {
			t.Errorf("%s: the comment line was read as a property", jdk)
		}
	}
	got := parseProperties([]string{`a\=b=c\:d`, `path=C\:\\x`, `u=\u00e9`})
	if got[0].Key != "a=b" || got[0].Value != `c:d` || got[1].Value != `C:\x` || got[2].Value != "é" {
		t.Errorf("escapes: %+v", got)
	}
}

// The route parses stdout only, but a warning a jcmd JVM prints there before
// its `<pid>:` line - JDK 11 does, for options it inherited - is not part of
// the answer.
func TestALineBeforeThePidLineIsNotPartOfTheAnswer(t *testing.T) {
	out := []byte("Picked up JAVA_TOOL_OPTIONS: -Dx=1\nOpenJDK 64-Bit Server VM warning: Native Memory Tracking did not setup properly\n" +
		string(fixture(t, "jdk11-GC-heap_info")))
	if got := parseHeapInfo(body(out)); !got.Recognised || got.Used != 1740*k {
		t.Errorf("%+v", got)
	}
	if v := parseVersion(body([]byte("Picked up JDK_JAVA_OPTIONS: -Xmx1g\r\n1:\r\nOpenJDK VM\r\nJDK 17.0.1\r\n"))); v.Major != 17 || v.VM != "OpenJDK VM" {
		t.Errorf("%+v", v)
	}
}

// `jcmd -l` gives the process id and the main class; the arguments, which may
// carry secrets, are dropped, and jcmd's own line is not a JVM.
func TestTheListDropsArgumentsAndJcmdItself(t *testing.T) {
	for _, jdk := range jdks {
		got := parseList(fixture(t, jdk+"-list"))
		if len(got) != 1 || got[0].PID != 1 || got[0].MainClass != "D" {
			t.Errorf("%s: %+v", jdk, got)
		}
	}
	got := parseList([]byte("7 /app/lib/billing.jar --token=abc\n9 -- process information unavailable\n12 sun.tools.jcmd.JCmd -l\nnoise\n"))
	if len(got) != 2 || got[0].MainClass != "billing.jar" || got[1].MainClass != "unknown main class" {
		t.Errorf("%+v", got)
	}
}
