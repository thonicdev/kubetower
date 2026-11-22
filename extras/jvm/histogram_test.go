package jvm

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
)

// jdk21 is a real answer of a JVM of JDK 21 (a program holding twenty thousand
// int[] and strings); jdk8 is the older format, which writes no module.

func TestTheHistogramIsReadOnTheJDKsFormats(t *testing.T) {
	h := parseHistogram(fixture(t, "jdk21-GC-class_histogram"))
	if h.Classes != 260 || h.Instances != 87714 || h.Bytes != 3339720 || len(h.Rows) != MaxHistogramRows || !h.Cut {
		t.Fatalf("classes %d, instances %d, bytes %d, %d rows, cut %v", h.Classes, h.Instances, h.Bytes, len(h.Rows), h.Cut)
	}
	if r := h.Rows[0]; r.Rank != 1 || r.Name != "int[]" || r.Module != "java.base" || r.Instances != 21342 || r.Bytes != 1003808 || r.Share != 30 {
		t.Errorf("first row = %+v", r)
	}
	if r := h.Rows[3]; r.Name != "java.lang.Object[]" {
		t.Errorf("fourth row = %+v", r)
	}
	for i := 1; i < len(h.Rows); i++ {
		if h.Rows[i].Bytes > h.Rows[i-1].Bytes {
			t.Fatalf("row %d is larger than the one before", i)
		}
	}
	old := parseHistogram(fixture(t, "jdk8-GC-class_histogram"))
	if old.Classes != 5 || old.Cut || old.Bytes != 2161192 || old.Rows[0].Name != "char[]" || old.Rows[0].Module != "" ||
		old.Rows[4].Name != "java.util.HashMap$Node" {
		t.Errorf("jdk8: %+v", old)
	}
}

func TestClassNamesAreReadAsJavaWritesThem(t *testing.T) {
	for raw, want := range map[string]string{"[I": "int[]", "[[J": "long[][]", "[Ljava.lang.String;": "java.lang.String[]",
		"java.lang.String": "java.lang.String", "[": "[", "[Q": "[Q", "[Lfoo": "[Lfoo"} {
		if got := className(raw); got != want {
			t.Errorf("%q = %q, want %q", raw, got, want)
		}
	}
}

// synthHistogram is n classes' worth of jcmd output.
func synthHistogram(n int) []byte {
	var b strings.Builder
	b.WriteString("1:\n\n num     #instances         #bytes  class name (module)\n-------------------------------------------------------\n")
	var inst, size int64
	for i := 1; i <= n; i++ {
		c, s := int64(n-i+1), int64(n-i+1)*16
		inst, size = inst+c, size+s
		fmt.Fprintf(&b, "%4d: %13d %14d  com.example.generated.Class%d (app@1.0)\n", i, c, s, i)
	}
	fmt.Fprintf(&b, "Total %13d %14d\n", inst, size)
	return []byte(b.String())
}

// A histogram of fifty thousand classes is read in one pass, quickly, and
// keeps fifty rows: the memory it allocates is not in proportion to its size.
func TestFiftyThousandClassesAreReadQuickly(t *testing.T) {
	src := synthHistogram(50000)
	start := time.Now()
	h := parseHistogram(src)
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("50,000 classes took %s", took)
	}
	if h.Classes != 50000 || len(h.Rows) != MaxHistogramRows || !h.Cut || h.Rows[0].Name != "com.example.generated.Class1" {
		t.Fatalf("classes %d, rows %d", h.Classes, len(h.Rows))
	}
	if allocs := testing.AllocsPerRun(3, func() { parseHistogram(src) }); allocs > 1000 {
		t.Errorf("%.0f allocations for 50,000 lines: the parse keeps more than its rows", allocs)
	}
}

func BenchmarkParseHistogram50k(b *testing.B) {
	src := synthHistogram(50000)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for b.Loop() {
		parseHistogram(src)
	}
}

const histogramPath = "/x/kind/jvm/pod/shop/billing-0/app/histogram"

func histogramHost(t *testing.T) *fakeHost {
	t.Helper()
	h := newHost(t, pod("shop", "billing-0", corev1.Container{Name: "app", Image: "eclipse-temurin:21-jdk"}))
	h.execs = map[string]execAnswer{
		envPrefix + "jcmd -l":                   {res: extension.ExecResult{Stdout: fixture(t, "jdk21-list")}},
		envPrefix + "jcmd 1 GC.class_histogram": {res: extension.ExecResult{Stdout: fixture(t, "jdk21-GC-class_histogram")}},
	}
	return h
}

func TestTheHistogramRunsOneConstantCommandOnAConfirmedClick(t *testing.T) {
	h := histogramHost(t)
	rec := post(t, h, histogramPath, `{"confirmed":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d %s", rec.Code, rec.Body)
	}
	if got, want := ran(h), []string{envPrefix + "jcmd -l", envPrefix + "jcmd 1 GC.class_histogram"}; !slices.Equal(got, want) {
		t.Fatalf("ran %q", got)
	}
	if strings.Contains(strings.Join(ran(h), " "), "-all") {
		t.Error("-all was sent")
	}
	if h.timeouts[1] != histogramTimeout || histogramBudget >= extension.MaxExecTimeout {
		t.Errorf("timeouts %v", h.timeouts)
	}
	got := decode[histogramAnswer](t, rec)
	if got.State != "histogram" || got.PID != 1 || got.Histogram == nil || len(got.Rows) != MaxHistogramRows || got.OutputCut {
		t.Fatalf("answer = %+v", got)
	}
}

// Nothing runs without the confirmation: no body, an unconfirmed one, and a
// body with an unknown field's worth of trouble all answer 400 and run nothing.
func TestTheHistogramAsksForItsConfirmation(t *testing.T) {
	for _, body := range []string{``, `{}`, `{"confirmed":false}`, `{"confirmed":"yes"}`, `nope`} {
		h := histogramHost(t)
		rec := post(t, h, histogramPath, body)
		if got := decode[problem](t, rec); rec.Code != http.StatusBadRequest || got.Code != CodeNotConfirmed || len(h.ran) != 0 {
			t.Errorf("%q answered %d %+v and ran %q", body, rec.Code, got, ran(h))
		}
	}
	h := histogramHost(t)
	if rec := post(t, h, histogramPath+"?pid=abc", `{"confirmed":true}`); rec.Code != http.StatusBadRequest || len(h.ran) != 0 {
		t.Errorf("a bad pid answered %d and ran %q", rec.Code, ran(h))
	}
	if rec := serve(t, histogramHost(t), histogramPath); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET answered %d", rec.Code)
	}
}

func TestTheHistogramNamesItsFailures(t *testing.T) {
	const cmd = envPrefix + "jcmd 1 GC.class_histogram"
	cases := map[string]struct {
		answer execAnswer
		status int
		code   string
	}{
		"attach": {execAnswer{res: extension.ExecResult{ExitCode: 1,
			Stdout: []byte("com.sun.tools.attach.AttachNotSupportedException: Unable to open socket file")}}, 422, CodeAttachRefused},
		"denied":  {execAnswer{err: extension.ErrDenied}, 403, CodeExecForbidden},
		"timeout": {execAnswer{err: extension.ErrExecTimedOut}, 504, CodeExecTimedOut},
		"absent":  {execAnswer{err: extension.ErrNoSuchCommand}, 422, CodeNoJcmd},
		"failed":  {execAnswer{res: extension.ExecResult{ExitCode: 1, Stderr: []byte("boom")}}, 502, CodeJcmdFailed},
		"empty":   {execAnswer{res: extension.ExecResult{Stdout: []byte("1:\n")}}, 502, CodeJcmdFailed},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := histogramHost(t)
			h.execs[cmd] = c.answer
			if c.code == CodeNoJcmd {
				// With env missing too, the run is repeated without it.
				h.execs[strings.TrimPrefix(cmd, envPrefix)] = c.answer
				h.execs[envPrefix+"jcmd -l"] = execAnswer{err: extension.ErrNoSuchCommand}
				h.execs["jcmd -l"] = execAnswer{err: extension.ErrNoSuchCommand}
			}
			rec := post(t, h, histogramPath, `{"confirmed":true}`)
			if got := decode[problem](t, rec); rec.Code != c.status || got.Code != c.code {
				t.Errorf("answered %d %+v", rec.Code, got)
			}
		})
	}
	h := histogramHost(t)
	h.execs[envPrefix+"jcmd 1 GC.class_histogram"] = execAnswer{res: extension.ExecResult{
		Stdout: fixture(t, "jdk21-GC-class_histogram")[:1500], Truncated: true}}
	if got := decode[histogramAnswer](t, post(t, h, histogramPath, `{"confirmed":true}`)); !got.OutputCut || got.Classes == 0 {
		t.Errorf("a cut answer: %+v", got)
	}
}
