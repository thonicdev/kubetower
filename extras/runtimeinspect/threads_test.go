package runtimeinspect

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
)

// The thread dumps are what `jcmd 1 Thread.print -l` printed on stdout for a
// JVM of JDK 8, 11, 17 and 21: `jdkN-Thread-print_-l` runs D.java, whose two
// threads worker-a and worker-b each hold one monitor and wait for the
// other's; `-nodeadlock` runs four sleeping threads pool-1-thread-0 to 3.

func count(d ThreadDump, state string) int {
	for _, c := range d.Counts {
		if c.State == state {
			return c.Count
		}
	}
	return -1
}

func byName(d ThreadDump, name string) *thread {
	for i := range d.Threads {
		if d.Threads[i].Name == name {
			return &d.Threads[i]
		}
	}
	return nil
}

func TestTheDeadlockIsReadOnEveryJDK(t *testing.T) {
	for _, jdk := range jdks {
		t.Run(jdk, func(t *testing.T) {
			d := parseThreads(body(fixture(t, jdk+"-Thread-print_-l")))
			if len(d.Deadlocks) != 1 || d.Deadlocks[0].Kind != "Java-level" || len(d.Deadlocks[0].Cycle) != 2 {
				t.Fatalf("deadlocks = %+v", d.Deadlocks)
			}
			for _, l := range d.Deadlocks[0].Cycle {
				other := map[string]string{"worker-a": "worker-b", "worker-b": "worker-a"}[l.Thread]
				if other == "" || l.HeldBy != other || !strings.HasPrefix(l.Waits, "to lock monitor 0x") ||
					!strings.HasPrefix(l.Lock, "0x") || l.Class != "java.lang.Object" {
					t.Errorf("link = %+v", l)
				}
			}
			if count(d, threadBlocked) != 2 || count(d, threadTimedWaiting) < 1 || count(d, threadVM) < 3 {
				t.Errorf("counts = %+v", d.Counts)
			}
			sum := 0
			for _, c := range d.Counts {
				sum += c.Count
			}
			if sum != d.Total || d.Total != len(d.Threads) || d.ThreadsCut || d.FramesCut != 0 {
				t.Errorf("total %d, sum %d, %d listed", d.Total, sum, len(d.Threads))
			}
			a := byName(d, "worker-a")
			if a == nil || a.State != threadBlocked || a.Detail != "on object monitor" || a.Daemon || a.Prio != 5 ||
				a.Status != "waiting for monitor entry" || len(a.Frames) < 3 || !strings.HasPrefix(a.Frames[0].At, "D.lambda$main$0(D.java:4)") {
				t.Fatalf("worker-a = %+v", a)
			}
			if a.WaitingFor == nil || a.WaitingFor.Kind != "waiting to lock" || a.WaitingFor.HeldBy != "worker-b" ||
				len(a.Holds) != 1 || a.Holds[0].Kind != "locked" || len(a.Frames[0].Locks) != 2 {
				t.Errorf("worker-a's locks = %+v %+v %+v", a.WaitingFor, a.Holds, a.Frames[0].Locks)
			}
			m := byName(d, "main")
			if m == nil || m.State != threadTimedWaiting || m.Detail != "sleeping" || m.ID != 1 || m.WaitingFor != nil {
				t.Errorf("main = %+v", m)
			}
			// A thread waiting in Object.wait() released the monitor its own
			// stack says it locked: it is not its own holder.
			f := byName(d, "Finalizer")
			if f == nil || !f.Daemon || f.State != threadWaiting || f.WaitingFor == nil || f.WaitingFor.HeldBy != "" {
				t.Errorf("Finalizer = %+v", f)
			}
			vm := byName(d, "VM Thread")
			if vm == nil || vm.State != threadVM || vm.ID != 0 || len(vm.Frames) != 0 || vm.Status != "runnable" {
				t.Errorf("VM Thread = %+v", vm)
			}
			if !strings.HasPrefix(d.VM, "OpenJDK 64-Bit Server VM") || !strings.HasPrefix(d.Taken, "2026-11-11 ") {
				t.Errorf("vm %q, taken %q", d.VM, d.Taken)
			}
			// The deadlock section's own thread lines are not threads.
			names := map[string]int{}
			for _, th := range d.Threads {
				names[th.Name]++
			}
			if names["worker-a"] != 1 || names["worker-b"] != 1 {
				t.Errorf("names = %v", names)
			}
		})
	}
}

func TestADumpWithoutDeadlockHasNone(t *testing.T) {
	for _, jdk := range jdks {
		t.Run(jdk, func(t *testing.T) {
			d := parseThreads(body(fixture(t, jdk+"-Thread-print_-l-nodeadlock")))
			if len(d.Deadlocks) != 0 || count(d, threadBlocked) != 0 {
				t.Errorf("deadlocks %+v, counts %+v", d.Deadlocks, d.Counts)
			}
			for i := range 4 {
				th := byName(d, fmt.Sprintf("pool-1-thread-%d", i))
				if th == nil || th.State != threadTimedWaiting || th.Detail != "sleeping" {
					t.Errorf("pool-1-thread-%d = %+v", i, th)
				}
			}
			if d.Total < 10 || d.Daemon < 3 {
				t.Errorf("%d threads, %d daemons", d.Total, d.Daemon)
			}
		})
	}
}

// A name may hold quotes and anything the application put in it: it is read
// up to the last quote before the fields.
func TestAThreadNameIsReadWhole(t *testing.T) {
	d := parseThreads([]string{
		`"pool "x" #3 tid=1" #12 daemon prio=7 os_prio=0 cpu=1ms elapsed=2s tid=0x1 nid=0x2 runnable  [0x3]`,
		"   java.lang.Thread.State: RUNNABLE",
		"",
		`"odd" os_prio=0 tid=0x1 nid=0x2 runnable`,
	})
	if len(d.Threads) != 2 || d.Threads[0].Name != `pool "x" #3 tid=1` || d.Threads[0].ID != 12 || !d.Threads[0].Daemon ||
		d.Threads[0].Prio != 7 || d.Threads[0].CPU != "1ms" || d.Threads[1].Name != "odd" || d.Threads[1].State != threadVM {
		t.Errorf("threads = %+v", d.Threads)
	}
}

// synthetic is a dump of n threads, each with depth frames, one in eight
// BLOCKED on a lock the previous one holds.
func synthetic(n, depth int) string {
	var b strings.Builder
	b.WriteString("1:\n2026-11-11 12:00:00\nFull thread dump OpenJDK 64-Bit Server VM (21.0.13+9-LTS mixed mode, sharing):\n\n")
	states := []string{"RUNNABLE", "WAITING (parking)", "TIMED_WAITING (sleeping)", "RUNNABLE", "WAITING (on object monitor)",
		"TIMED_WAITING (parking)", "RUNNABLE", "BLOCKED (on object monitor)"}
	for i := range n {
		fmt.Fprintf(&b, "\"http-nio-8080-exec-%d\" #%d daemon prio=5 os_prio=0 cpu=1.00ms elapsed=10.00s tid=0x%x nid=%d runnable  [0x0]\n",
			i, i+20, i, i)
		fmt.Fprintf(&b, "   java.lang.Thread.State: %s\n", states[i%len(states)])
		for f := range depth {
			fmt.Fprintf(&b, "\tat com.example.Service.call%d(Service.java:%d)\n", f, f+1)
			if f == 0 {
				if i%8 == 7 {
					fmt.Fprintf(&b, "\t- waiting to lock <0x%016x> (a java.lang.Object)\n", i-1)
				} else {
					fmt.Fprintf(&b, "\t- locked <0x%016x> (a java.lang.Object)\n", i)
				}
			}
		}
		b.WriteString("\n   Locked ownable synchronizers:\n\t- None\n\n")
	}
	b.WriteString("\"VM Thread\" os_prio=0 cpu=1ms elapsed=10s tid=0x1 nid=0x2 runnable\n\nJNI global refs: 7, weak refs: 0\n")
	return b.String()
}

func TestTwoThousandThreadsAreReadQuickly(t *testing.T) {
	src := []byte(synthetic(2000, 20))
	start := time.Now()
	d := parseThreads(body(src))
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("2,000 threads took %s", took)
	}
	if d.Total != 2001 || len(d.Threads) != 2001 || d.ThreadsCut || count(d, threadBlocked) != 250 || count(d, threadRunnable) != 750 {
		t.Fatalf("total %d, counts %+v", d.Total, d.Counts)
	}
	b := byName(d, "http-nio-8080-exec-7")
	if b == nil || b.WaitingFor == nil || b.WaitingFor.HeldBy != "http-nio-8080-exec-6" {
		t.Errorf("exec-7 = %+v", b)
	}
}

// Past MaxThreads the threads are counted and not listed; past MaxFrames the
// frames are cut; and the answer says both.
func TestTheDumpIsBounded(t *testing.T) {
	d := parseThreads(body([]byte(synthetic(MaxThreads+500, 2))))
	if d.Total != MaxThreads+501 || len(d.Threads) != MaxThreads || !d.ThreadsCut {
		t.Errorf("total %d, listed %d, cut %v", d.Total, len(d.Threads), d.ThreadsCut)
	}
	sum := 0
	for _, c := range d.Counts {
		sum += c.Count
	}
	if sum != d.Total {
		t.Errorf("the counts cover %d of %d", sum, d.Total)
	}
	deep := parseThreads(body([]byte(synthetic(3, MaxFrames+36))))
	if deep.FramesCut != 3 || len(deep.Threads[0].Frames) != MaxFrames || deep.Threads[0].FramesCut != 36 {
		t.Errorf("frames %d, cut %d, threads cut %d", len(deep.Threads[0].Frames), deep.Threads[0].FramesCut, deep.FramesCut)
	}
	notes := dumpNotes(d, true, "4.0 MiB")
	if len(notes) != 2 || !strings.Contains(notes[0], "4.0 MiB") || !strings.Contains(notes[1], fmt.Sprint(MaxThreads)) {
		t.Errorf("notes = %q", notes)
	}
	if n := dumpNotes(deep, false, ""); len(n) != 1 || !strings.Contains(n[0], "deeper than") {
		t.Errorf("notes = %q", n)
	}
}

const threadsPath = "/x/kind/runtime/pod/shop/billing-0/app/threads"

// threadsHost answers jcmd -l and Thread.print -l from jdk's fixtures.
func threadsHost(t *testing.T, jdk, fixtureSuffix string) *fakeHost {
	t.Helper()
	h := newHost(t, pod("shop", "billing-0", corev1.Container{Name: "app", Image: "eclipse-temurin:21-jdk"}))
	h.execs = map[string]execAnswer{
		envPrefix + "jcmd -l": {res: extension.ExecResult{Stdout: fixture(t, jdk+"-list")}},
		envPrefix + "jcmd 1 Thread.print -l": {res: extension.ExecResult{Stdout: fixture(t, jdk+"-Thread-print_-l"+fixtureSuffix),
			Stderr: []byte("Picked up JAVA_TOOL_OPTIONS: -Dspring.datasource.password=hunter2\n")}},
	}
	return h
}

func TestTheThreadDumpRunsOneWhitelistedCommand(t *testing.T) {
	h := threadsHost(t, "jdk21", "")
	rec := post(t, h, threadsPath, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d %s", rec.Code, rec.Body)
	}
	if got, want := ran(h), []string{envPrefix + "jcmd -l", envPrefix + "jcmd 1 Thread.print -l"}; !slices.Equal(got, want) {
		t.Fatalf("ran %q", got)
	}
	if h.timeouts[1] != threadsTimeout || threadsTimeout >= extension.MaxExecTimeout || threadsBudget >= extension.MaxExecTimeout {
		t.Errorf("timeouts %v", h.timeouts)
	}
	if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "Picked up") {
		t.Fatalf("stderr reached the answer: %s", rec.Body)
	}
	got := decode[threadsAnswer](t, rec)
	if got.State != "dumped" || got.PID != 1 || got.PIDFrom != "listed" || got.ThreadDump == nil || len(got.Deadlocks) != 1 ||
		got.Total != len(got.Threads) || got.OutputCut || len(got.Notes) != 0 {
		t.Fatalf("answer = %+v", got)
	}
	if !strings.HasPrefix(got.Raw, "2026-11-11 ") || !strings.Contains(got.Raw, "Found one Java-level deadlock:") ||
		strings.HasPrefix(got.Raw, "1:") {
		t.Errorf("raw starts %q", got.Raw[:min(80, len(got.Raw))])
	}
}

// Output cut at the exec's limit is read as far as it goes, and said.
func TestACutDumpSaysSo(t *testing.T) {
	h := threadsHost(t, "jdk21", "")
	full := fixture(t, "jdk21-Thread-print_-l")
	h.execs[envPrefix+"jcmd 1 Thread.print -l"] = execAnswer{res: extension.ExecResult{Stdout: full[:len(full)/2], Truncated: true}}
	got := decode[threadsAnswer](t, post(t, h, threadsPath, ""))
	if !got.OutputCut || len(got.Notes) != 1 || !strings.Contains(got.Notes[0], "exec's limit") || got.Total == 0 {
		t.Errorf("answer: cut %v, notes %q, %d threads", got.OutputCut, got.Notes, got.Total)
	}
}

func TestTheThreadDumpNamesItsFailures(t *testing.T) {
	cases := map[string]struct {
		answer execAnswer
		status int
		code   string
	}{
		"attach": {execAnswer{res: extension.ExecResult{ExitCode: 1,
			Stdout: []byte("com.sun.tools.attach.AttachNotSupportedException: Unable to open socket file")}}, 422, CodeAttachRefused},
		"attach disabled": {execAnswer{res: extension.ExecResult{ExitCode: 1,
			Stdout: []byte("com.sun.tools.attach.AttachNotSupportedException: The VM does not support the attach mechanism")}}, 422, CodeAttachDisabled},
		"denied":  {execAnswer{err: extension.ErrDenied}, 403, CodeExecForbidden},
		"timeout": {execAnswer{err: extension.ErrExecTimedOut}, 504, CodeExecTimedOut},
		"failed":  {execAnswer{res: extension.ExecResult{ExitCode: 1, Stderr: []byte("boom")}}, 502, CodeJcmdFailed},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := threadsHost(t, "jdk21", "")
			h.execs[envPrefix+"jcmd 1 Thread.print -l"] = c.answer
			rec := post(t, h, threadsPath, "")
			if got := decode[problem](t, rec); rec.Code != c.status || got.Code != c.code {
				t.Errorf("answered %d %+v", rec.Code, got)
			}
		})
	}
	h := threadsHost(t, "jdk21", "")
	h.execs[envPrefix+"jcmd -l"] = execAnswer{res: extension.ExecResult{Stdout: []byte("1 a.Main\n2 b.Main\n")}}
	got := decode[threadsAnswer](t, post(t, h, threadsPath, ""))
	if got.State != "choose" || len(got.JVMs) != 2 || len(h.ran) != 1 || got.ThreadDump != nil {
		t.Errorf("several JVMs: %+v, ran %q", got, ran(h))
	}
	h = threadsHost(t, "jdk21", "")
	if rec := post(t, h, threadsPath+"?pid=abc", ""); rec.Code != http.StatusBadRequest || len(h.ran) != 0 {
		t.Errorf("a bad pid answered %d and ran %q", rec.Code, ran(h))
	}
	// The dump is a POST: a GET is not routed to it.
	if rec := serve(t, threadsHost(t, "jdk21", ""), threadsPath); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET answered %d", rec.Code)
	}
}

// A request that goes away while Thread.print runs returns at once, and
// leaves no goroutine behind.
func TestACancelledDumpLeavesNothingRunning(t *testing.T) {
	h := threadsHost(t, "jdk21", "")
	started := make(chan struct{})
	h.block = map[string]bool{envPrefix + "jcmd 1 Thread.print -l": true}
	h.blocked = started
	before := runtime.NumGoroutine()

	mux := http.NewServeMux()
	for _, r := range New().Routes(h) {
		mux.Handle(r.Method+" /x/{cluster}/"+Key+r.Pattern, r.Handler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	rec := httptest.NewRecorder()
	go func() {
		defer close(done)
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, threadsPath, nil).WithContext(ctx))
	}()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler did not return after its request was canceled")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("a gone request was answered: %s", rec.Body)
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("%d goroutines before, %d after", before, after)
	}
}

// --- PerfCounter.print ---------------------------------------------------------

func TestTheGCCountersAreReadOnEveryJDK(t *testing.T) {
	want := map[string][]string{
		"jdk8":     {"Copy", "MSC"},
		"jdk11":    {"Copy", "MSC"},
		"jdk17":    {"Serial young collection pauses", "Serial full collection pauses"},
		"jdk21":    {"Serial young collection pauses", "Serial full collection pauses"},
		"jdk8-g1":  {"PSScavenge", "PSParallelCompact"},
		"jdk11-g1": {"G1 incremental collections", "G1 stop-the-world full collections", "G1 stop-the-world phases"},
		"jdk17-g1": {"G1 young collection pauses", "G1 full collection pauses", "G1 concurrent cycle pauses"},
		"jdk21-g1": {"G1 young collection pauses", "G1 full collection pauses", "G1 concurrent cycle pauses"},
	}
	for name, collectors := range want {
		jdk, variant, _ := strings.Cut(name, "-")
		file := jdk + "-PerfCounter-print"
		if variant != "" {
			file += "-" + variant
		}
		gc, ok := parseGC(body(fixture(t, file)))
		if !ok || len(gc.Collectors) != len(collectors) || gc.Uptime < 60 || gc.Uptime > 120 {
			t.Errorf("%s: %+v %v", name, gc, ok)
			continue
		}
		for i, c := range gc.Collectors {
			if c.Name != collectors[i] || c.Young != (i == 0) || (i < 2 && c.Invocations <= 0) || c.Seconds < 0 || c.Share < 0 {
				t.Errorf("%s collector %d = %+v", name, i, c)
			}
		}
		if gc.Collectors[0].Invocations < 15 || gc.Share < 0 || gc.Share > 5 || (variant == "" && gc.Share <= 0) {
			t.Errorf("%s: %+v", name, gc)
		}
	}
	if _, ok := parseGC([]string{"sun.os.hrt.frequency=1000000000", "sun.os.hrt.ticks=5"}); ok {
		t.Error("no collector was read as some")
	}
	if _, ok := parseGC([]string{"sun.os.hrt.frequency=1000000000", "sun.os.hrt.ticks=5", "sun.gc.collector.0.invocations=3"}); ok {
		t.Error("a collector without its name and time was read")
	}
}

// PerfCounter.print holds the command line and the system properties: none of
// it leaves the inspection but the GC counters.
func TestNothingButTheGCCountersLeavesPerfCounterPrint(t *testing.T) {
	for _, variant := range []string{"", "-g1"} {
		h := jdkHost(t, "jdk21", false, true)
		out := fixture(t, "jdk21-PerfCounter-print"+variant)
		if !strings.Contains(string(out), "hunter2") || !strings.Contains(string(out), "sun.rt.javaCommand") {
			t.Fatal("the fixture no longer holds the values this test looks for")
		}
		h.execs[envPrefix+"jcmd 1 PerfCounter.print"] = execAnswer{res: extension.ExecResult{Stdout: out}}
		rec := post(t, h, inspectPath, "")
		got := decode[inspectAnswer](t, rec)
		if got.GC == nil || len(got.GC.Collectors) < 2 {
			t.Fatalf("gc = %+v", got.GC)
		}
		for _, leak := range []string{"hunter2", "sun.rt.", "java.property.", "java.rt.vmArgs", "secret.token", "G --db"} {
			if strings.Contains(rec.Body.String(), leak) {
				t.Errorf("%q left the route", leak)
			}
		}
		// The GC part holds the collectors' names and numbers, and nothing else.
		gc, _ := json.Marshal(got.GC)
		for _, leak := range []string{"/", "password", "java."} {
			if strings.Contains(string(gc), leak) {
				t.Errorf("%q in the GC answer: %s", leak, gc)
			}
		}
	}
	// A failed PerfCounter.print says nothing of what it printed.
	h := jdkHost(t, "jdk21", false, true)
	h.execs[envPrefix+"jcmd 1 PerfCounter.print"] = execAnswer{res: extension.ExecResult{ExitCode: 1,
		Stdout: []byte("1:\nsun.rt.javaCommand=\"G --db-password=hunter2\"\n")}}
	rec := post(t, h, inspectPath, "")
	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Errorf("a failure sentence carried PerfCounter.print's output: %s", rec.Body)
	}
	got := decode[inspectAnswer](t, rec)
	if got.GC != nil || got.Sections[len(got.Sections)-1].State != stateFailed {
		t.Errorf("gc %+v, sections %+v", got.GC, got.Sections)
	}
}
