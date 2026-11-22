package jvm

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

const (
	mi = int64(1) << 20
	gi = int64(1) << 30
)

// A JVM size is digits and one optional unit, k, m, g or t in either case;
// anything else is not one the JVM would accept.
func TestASizeIsReadInTheJVMsUnits(t *testing.T) {
	for in, want := range map[string]int64{
		"1048576": 1 << 20, "512k": 512 << 10, "512K": 512 << 10, "768m": 768 * mi, "768M": 768 * mi,
		"2g": 2 * gi, "2G": 2 * gi, "1t": 1 << 40, "1T": 1 << 40,
	} {
		if got, ok := parseSize(in); !ok || got != want {
			t.Errorf("parseSize(%q) = %d, %v, want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "m", "1.5g", "2gb", "-1g", "0", "1p", "99999999999999t"} {
		if got, ok := parseSize(in); ok {
			t.Errorf("parseSize(%q) = %d, read as a size", in, got)
		}
	}
}

// reading is the estimate and the risk of one container.
func reading(t *testing.T, c corev1.Container) (Heap, Risk, []string) {
	t.Helper()
	if c.Image == "" {
		c.Image = "eclipse-temurin:21-jre"
	}
	got := read(c)
	if got.Heap == nil || got.Risk == nil {
		t.Fatalf("no estimate for a Java container: %+v", got)
	}
	return *got.Heap, *got.Risk, got.Notes
}

// -Xmx in the arguments is the heap, and an -Xmx at or above the limit is
// the error: OOMKilled before Java throws.
func TestXmxInTheArgumentsAtTheLimitIsAnError(t *testing.T) {
	heap, risk, _ := reading(t, memory(corev1.Container{Args: []string{"-Xmx2g", "-jar", "app.jar"}}, "1Gi", "2Gi"))
	if heap.From != heapExplicit || heap.Bytes == nil || *heap.Bytes != 2*gi || !strings.Contains(heap.Source, "-Xmx2g") ||
		!strings.Contains(heap.Source, "the command line") {
		t.Errorf("heap = %+v, want -Xmx2g from the command line", heap)
	}
	if risk.Level != riskError || risk.Percent == nil || *risk.Percent != 100 || !strings.Contains(risk.Sentence, "OOMKilled") {
		t.Errorf("risk = %+v, want an error at 100 %%", risk)
	}
	// Above the limit too.
	_, risk, _ = reading(t, memory(corev1.Container{Args: []string{"-Xmx3g"}}, "", "2Gi"))
	if risk.Level != riskError {
		t.Errorf("-Xmx3g under a 2Gi limit: risk %s, want error", risk.Level)
	}
}

// Above 75 % of the limit is a warning, said as a heuristic; at or under it,
// ok, said as a heuristic too.
func TestAHeapAboveThreeQuartersOfTheLimitIsAWarning(t *testing.T) {
	_, risk, _ := reading(t, memory(corev1.Container{Args: []string{"-Xmx800m"}}, "", "1Gi"))
	if risk.Level != riskWarning || *risk.Percent != 78 || !strings.Contains(risk.Sentence, "heuristic") {
		t.Errorf("-Xmx800m of 1Gi: %+v, want a warning at 78 %% saying it is a heuristic", risk)
	}
	_, risk, _ = reading(t, memory(corev1.Container{Args: []string{"-Xmx768m"}}, "", "1Gi"))
	if risk.Level != riskOK || *risk.Percent != 75 || !strings.Contains(risk.Sentence, "heuristic") {
		t.Errorf("-Xmx768m of 1Gi: %+v, want ok at exactly 75 %%", risk)
	}
}

// The options in the environment the JVM reads count, in the JVM's order:
// JAVA_TOOL_OPTIONS, then JDK_JAVA_OPTIONS, then the command line, the last
// one winning.
func TestTheLastHeapOptionInTheJVMsOrderWins(t *testing.T) {
	for _, tc := range []struct {
		name  string
		c     corev1.Container
		want  int64
		where string
	}{
		{"JAVA_TOOL_OPTIONS alone", corev1.Container{Env: env("JAVA_TOOL_OPTIONS", "-Xmx512m")}, 512 * mi, "JAVA_TOOL_OPTIONS"},
		{"JDK_JAVA_OPTIONS after JAVA_TOOL_OPTIONS",
			corev1.Container{Env: env("JDK_JAVA_OPTIONS", "-Xmx700m", "JAVA_TOOL_OPTIONS", "-Xmx512m")}, 700 * mi, "JDK_JAVA_OPTIONS"},
		{"the command line last",
			corev1.Container{Args: []string{"-Xmx300m"}, Env: env("JDK_JAVA_OPTIONS", "-Xmx700m")}, 300 * mi, "the command line"},
		{"MaxHeapSize is the same flag",
			corev1.Container{Args: []string{"-Xmx300m", "-XX:MaxHeapSize=400m"}}, 400 * mi, "the command line"},
		{"within one source, the last",
			corev1.Container{Env: env("JAVA_TOOL_OPTIONS", "-Xmx1g -XX:+UseG1GC -Xmx600m")}, 600 * mi, "JAVA_TOOL_OPTIONS"},
	} {
		heap, _, _ := reading(t, memory(tc.c, "", "1Gi"))
		if heap.From != heapExplicit || *heap.Bytes != tc.want || !strings.Contains(heap.Source, tc.where) {
			t.Errorf("%s: heap %+v (%d), want %d from %s", tc.name, heap, *heap.Bytes, tc.want, tc.where)
		}
	}
}

// JAVA_OPTS is not read by the JVM: its -Xmx is said in a note and never
// counted.
func TestJAVAOPTSIsNeverCountedAsRead(t *testing.T) {
	heap, risk, notes := reading(t, memory(corev1.Container{Env: env("JAVA_OPTS", "-Xmx4g")}, "", "2Gi"))
	if heap.From != heapDefault || *heap.Bytes != 512*mi {
		t.Errorf("heap = %+v, want the default 25 %% of 2Gi: JAVA_OPTS is not the JVM's", heap)
	}
	if risk.Level != riskOK {
		t.Errorf("risk = %s, want ok", risk.Level)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "JAVA_OPTS") || !strings.Contains(notes[0], "does not read") {
		t.Errorf("notes = %v, want one saying the JVM does not read JAVA_OPTS", notes)
	}
	// Passed on the command line by Kubernetes' own substitution, it is read.
	heap, _, _ = reading(t, memory(corev1.Container{Args: []string{"$(JAVA_OPTS)", "-jar", "app.jar"},
		Env: env("JAVA_OPTS", "-Xmx1g")}, "", "2Gi"))
	if heap.From != heapExplicit || *heap.Bytes != gi {
		t.Errorf("$(JAVA_OPTS) on the command line: heap %+v, want -Xmx1g", heap)
	}
}

// MaxRAMPercentage is a share of the limit; above 75 % it is a warning, and
// never an error, which is kept for an explicit -Xmx.
func TestMaxRAMPercentageIsAShareOfTheLimit(t *testing.T) {
	heap, risk, _ := reading(t, memory(corev1.Container{Env: env("JAVA_TOOL_OPTIONS", "-XX:MaxRAMPercentage=80.0")}, "", "2Gi"))
	limit := float64(2 * gi)
	want := int64(limit * 80 / 100)
	if heap.From != heapPercentage || *heap.Bytes != want || !strings.Contains(heap.Source, "MaxRAMPercentage=80.0") {
		t.Errorf("heap = %+v, want 80 %% of 2Gi", heap)
	}
	if risk.Level != riskWarning {
		t.Errorf("risk = %s, want warning", risk.Level)
	}
	_, risk, _ = reading(t, memory(corev1.Container{Args: []string{"-XX:MaxRAMPercentage=100"}}, "", "2Gi"))
	if risk.Level != riskWarning {
		t.Errorf("MaxRAMPercentage=100: risk %s, want a warning: an error is for an explicit -Xmx", risk.Level)
	}
	// An -Xmx wins over a percentage, wherever each is.
	heap, _, _ = reading(t, memory(corev1.Container{Args: []string{"-XX:MaxRAMPercentage=50"}, Env: env("JAVA_TOOL_OPTIONS", "-Xmx256m")}, "", "2Gi"))
	if heap.From != heapExplicit || *heap.Bytes != 256*mi {
		t.Errorf("-Xmx and a percentage: heap %+v, want the -Xmx", heap)
	}
}

// With nothing set, the JVM's own ergonomics: 25 % of the limit, 50 % of a
// small one, and never under its default heap size between the two.
func TestWithNoOptionTheJVMsDefaultsAreFollowed(t *testing.T) {
	for _, tc := range []struct {
		limit string
		from  string
		want  int64
	}{
		{"2Gi", heapDefault, 512 * mi},
		{"200Mi", heapSmall, 100 * mi},
		{"300Mi", heapFloor, defaultHeapSize},
	} {
		heap, risk, _ := reading(t, memory(corev1.Container{}, "", tc.limit))
		if heap.From != tc.from || *heap.Bytes != tc.want {
			t.Errorf("limit %s: heap %+v (%d), want %s %d", tc.limit, heap, *heap.Bytes, tc.from, tc.want)
		}
		if risk.Level != riskOK {
			t.Errorf("limit %s: risk %s, want ok", tc.limit, risk.Level)
		}
	}
}

// With no limit, the heap is sized on the node: no number, and no grade.
func TestWithNoLimitTheHeapIsSizedOnTheNode(t *testing.T) {
	got := read(memory(corev1.Container{Image: "openjdk:17"}, "512Mi", ""))
	if got.Heap == nil || got.Heap.Bytes != nil || got.Heap.From != heapNoLimit || !strings.Contains(got.Heap.Source, "node") {
		t.Errorf("heap = %+v, want no number, sized on the node", got.Heap)
	}
	if got.Risk == nil || got.Risk.Level != riskNoLimit || got.Risk.Percent != nil {
		t.Errorf("risk = %+v, want nolimit with no percentage", got.Risk)
	}
	if got.Request == nil || got.Request.Bytes != 512*mi || got.Request.Text != "512Mi" || got.Limit != nil {
		t.Errorf("request %+v, limit %+v, want 512Mi and none", got.Request, got.Limit)
	}
	// An -Xmx with no limit is still the heap, and still ungraded.
	got = read(corev1.Container{Image: "openjdk:17", Args: []string{"-Xmx1g"}})
	if got.Heap.Bytes == nil || *got.Heap.Bytes != gi || got.Risk.Level != riskNoLimit {
		t.Errorf("-Xmx1g with no limit: heap %+v, risk %+v", got.Heap, got.Risk)
	}
}

// A JVM told not to read its container sizes on the node even under a limit:
// its risk is unknown, never ok.
func TestAJVMThatIgnoresItsContainerIsNotGraded(t *testing.T) {
	heap, risk, notes := reading(t, memory(corev1.Container{Args: []string{"-XX:-UseContainerSupport"}}, "", "1Gi"))
	if heap.Bytes != nil || risk.Level != riskUnknown || len(notes) != 1 {
		t.Errorf("heap %+v, risk %+v, notes %v; want no number, unknown, and a note", heap, risk, notes)
	}
}

// Options that cannot be read, or that come from a ConfigMap or a Secret,
// are said, never silently dropped.
func TestWhatCouldNotBeReadIsSaid(t *testing.T) {
	_, _, notes := reading(t, memory(corev1.Container{Args: []string{"-Xmx1.5g"}}, "", "2Gi"))
	if len(notes) != 1 || !strings.Contains(notes[0], "-Xmx1.5g") {
		t.Errorf("notes = %v, want one naming -Xmx1.5g", notes)
	}
	c := memory(corev1.Container{Env: []corev1.EnvVar{{Name: "JAVA_TOOL_OPTIONS",
		ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{Key: "opts"}}}}}, "", "2Gi")
	heap, _, notes := reading(t, c)
	if heap.From != heapDefault || len(notes) != 1 || !strings.Contains(notes[0], "not read here") {
		t.Errorf("JAVA_TOOL_OPTIONS from a ConfigMap: heap %+v, notes %v", heap, notes)
	}
}

// A container nothing says is Java has no heap and no risk: an estimate for a
// process with no heap would be invented.
func TestANonJavaContainerHasNoEstimate(t *testing.T) {
	got := read(memory(corev1.Container{Name: "proxy", Image: "nginx:1.27", Args: []string{"-g", "daemon off;"}}, "64Mi", "128Mi"))
	if got.Java || got.Heap != nil || got.Risk != nil || len(got.Hints) != 0 || got.Limit == nil {
		t.Errorf("reading = %+v, want not Java, no heap, no risk, its limit still said", got)
	}
}
