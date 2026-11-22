package jvm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
)

const envPrefix = "env -u JAVA_TOOL_OPTIONS -u JDK_JAVA_OPTIONS "

// The commands of an inspection, in the order they are sent.
var inspected = []string{"VM.version", "VM.uptime", "VM.flags", "VM.command_line", "VM.system_properties",
	"GC.heap_info", "VM.metaspace basic", "Compiler.codecache", "VM.native_memory summary", "PerfCounter.print"}

// fixtureName is the testdata file a command's answer is kept in.
func fixtureName(jdk, command string) string {
	return jdk + "-" + strings.ReplaceAll(strings.ReplaceAll(command, ".", "-"), " ", "_")
}

// jdkHost is a pod `shop/billing-0` whose container `app` runs a JVM of jdk
// as process 1, answering every whitelisted command from the fixtures, with
// env in front (plain false) or not. nmt picks the fixture with native memory
// tracking on, where the JDK has one.
func jdkHost(t *testing.T, jdk string, plain, nmt bool) *fakeHost {
	t.Helper()
	h := newHost(t, pod("shop", "billing-0",
		memory(corev1.Container{Name: "app", Image: "eclipse-temurin:21-jdk"}, "256Mi", "512Mi")))
	h.execs = map[string]execAnswer{}
	h.live = map[string]extension.Resources{
		extension.ContainerKey("shop", "billing-0", "app"): {MemoryBytes: 160 << 20},
	}
	prefix := envPrefix + "jcmd "
	if plain {
		prefix = "jcmd "
		h.execs[envPrefix+"jcmd -l"] = execAnswer{err: fmt.Errorf("%w: \"env\" in container app", extension.ErrNoSuchCommand)}
	}
	h.execs[prefix+"-l"] = execAnswer{res: extension.ExecResult{Stdout: fixture(t, jdk+"-list")}}
	for _, c := range inspected {
		name := fixtureName(jdk, c)
		if c == "VM.native_memory summary" && !nmt {
			name += "-off"
		}
		out, err := os.ReadFile(filepath.Join("testdata", name+".txt"))
		if err != nil {
			out = fixture(t, name+"-off")
		}
		exit := 0
		if strings.Contains(string(out), "Unknown diagnostic command") {
			exit = 1
		}
		h.execs[prefix+"1 "+c] = execAnswer{res: extension.ExecResult{Stdout: out, ExitCode: exit,
			Stderr: []byte("Picked up JAVA_TOOL_OPTIONS: -Dspring.datasource.password=hunter2\n")}}
	}
	return h
}

func post(t *testing.T, h extension.Host, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	for _, r := range New().Routes(h) {
		mux.Handle(r.Method+" /x/{cluster}/jvm"+r.Pattern, r.Handler)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return rec
}

const inspectPath = "/x/kind/jvm/pod/shop/billing-0/app/inspect"

func ran(h *fakeHost) []string {
	out := make([]string, len(h.ran))
	for i, argv := range h.ran {
		out[i] = strings.Join(argv, " ")
	}
	return out
}

func TestInspectRunsTheWhitelistInOrderWithoutTheJavaOptions(t *testing.T) {
	h := jdkHost(t, "jdk21", false, true)
	rec := post(t, h, inspectPath, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d %s", rec.Code, rec.Body)
	}
	want := []string{envPrefix + "jcmd -l"}
	for _, c := range inspected {
		want = append(want, envPrefix+"jcmd 1 "+c)
	}
	if got := ran(h); !slices.Equal(got, want) {
		t.Fatalf("ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Fatalf("a secret left the console: %s", rec.Body)
	}
	got := decode[inspectAnswer](t, rec)
	if got.State != "inspected" || got.PID != 1 || got.PIDFrom != "listed" || got.Inherited || len(got.JVMs) != 0 {
		t.Errorf("answer = %+v", got)
	}
	for _, s := range got.Sections {
		if s.State != stateOK {
			t.Errorf("section %+v", s)
		}
	}
	s := got.Summary
	if s.Version != "JDK 21.0.13" || s.Major != 21 || s.GC != "G1" || s.Uptime != "1 min 0 s" ||
		s.Flags[0].Name != "MaxHeapSize" || s.Flags[0].Text != "512.0 MiB" {
		t.Errorf("summary = %+v", s)
	}
	m := got.Memory
	if m.Source != "nmt" || m.Committed.Bytes != 98002*k || m.WorkingSet.Bytes != 160<<20 || m.HeapUsed.Bytes != 2974*k ||
		m.HeapMax.Bytes != 512<<20 || m.CodeCacheUsed.Bytes != 1655*k || len(m.NMT) < 15 || m.Track.Bytes != 512<<20 {
		t.Errorf("memory = %+v", m)
	}
	// 2974K used and 34816K committed of a 512 MiB maximum.
	if m.HeapGauge == nil || m.HeapGauge.UsedPct != 0.6 || m.HeapGauge.CommittedPct != 6.6 {
		t.Errorf("heap gauge = %+v", m.HeapGauge)
	}
	last := m.Segments[len(m.Segments)-1]
	if !last.Unattributed || last.Bytes != 160<<20-98002*k {
		t.Errorf("the unattributed part = %+v", last)
	}
	if m.Segments[0].Key != "heap" || m.Segments[0].Bytes != 34816*k || m.Segments[0].Used.Bytes != 2974*k || m.Segments[0].Start != 0 {
		t.Errorf("heap segment = %+v", m.Segments[0])
	}
	var keys []string
	for _, mk := range m.Markers {
		keys = append(keys, mk.Key)
	}
	if strings.Join(keys, ",") != "request,limit,workingSet,heapMax" || m.Markers[1].At != 100 || m.Markers[0].At != 50 {
		t.Errorf("markers = %+v", m.Markers)
	}
	if got.CommandLine == nil || joined(got.CommandLine.JVMArgs) != "-XX:NativeMemoryTracking=summary -Dspring.datasource.password="+masked+" -Xmx512m -Dapp.name=shop" {
		t.Errorf("command line = %+v", got.CommandLine)
	}
	var props int
	for _, p := range got.Properties {
		if p.Masked {
			props++
		}
	}
	if props != 2 { // spring.datasource.password, sun.java.command
		t.Errorf("%d masked properties", props)
	}
}

// JDK 8 has neither VM.metaspace nor Compiler.codecache, and native memory
// tracking is off: both said, neither drawn as zero.
func TestJDK8SaysWhatItDoesNotHave(t *testing.T) {
	h := jdkHost(t, "jdk8", false, false)
	rec := post(t, h, inspectPath, "")
	got := decode[inspectAnswer](t, rec)
	states := map[string]string{}
	for _, s := range got.Sections {
		states[s.Command] = s.State
	}
	if states["VM.metaspace basic"] != stateUnavailable || states["Compiler.codecache"] != stateUnavailable ||
		states["VM.native_memory summary"] != stateNotEnabled || states["GC.heap_info"] != stateOK {
		t.Fatalf("states = %v", states)
	}
	for _, s := range got.Sections {
		if s.State == stateNotEnabled && !strings.Contains(s.Sentence, "-XX:NativeMemoryTracking=summary") {
			t.Errorf("NMT off does not say how to turn it on: %q", s.Sentence)
		}
	}
	m := got.Memory
	if m.Source != "jvm" || m.CodeCacheUsed != nil || m.HeapCommitted.Bytes != (9728+22016)*k || got.Summary.GC != "Parallel" {
		t.Errorf("memory = %+v", m)
	}
	if !strings.Contains(rec.Body.String(), `"codeCacheUsed":null`) {
		t.Error("the code cache JDK 8 cannot report is not null")
	}
	// heap, metaspace, then what is outside: no code cache segment at all.
	var keys []string
	for _, s := range m.Segments {
		keys = append(keys, s.Key)
	}
	if strings.Join(keys, ",") != "heap,metaspace,outside" {
		t.Errorf("segments = %v", keys)
	}
}

// An image with no env: jcmd runs as is, once env is found missing, and the
// answer says the container's Java options were inherited.
func TestWithoutEnvJcmdRunsPlainAndSaysSo(t *testing.T) {
	h := jdkHost(t, "jdk17", true, true)
	rec := post(t, h, inspectPath, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d %s", rec.Code, rec.Body)
	}
	got := ran(h)
	if got[0] != envPrefix+"jcmd -l" || got[1] != "jcmd -l" || got[2] != "jcmd 1 VM.version" || len(got) != 2+len(inspected) {
		t.Errorf("ran %q", got)
	}
	answer := decode[inspectAnswer](t, rec)
	if !answer.Inherited || len(answer.Notes) == 0 || !strings.Contains(answer.Notes[0], "JAVA_TOOL_OPTIONS") {
		t.Errorf("answer = %+v", answer)
	}
}

func TestEveryFailureIsNamed(t *testing.T) {
	noEnv := fmt.Errorf("%w: \"env\"", extension.ErrNoSuchCommand)
	noJcmd := fmt.Errorf("%w: \"jcmd\"", extension.ErrNoSuchCommand)
	refused := extension.ExecResult{ExitCode: 1, Stdout: []byte("1:\ncom.sun.tools.attach.AttachNotSupportedException: Unable to open socket file /proc/1/root/tmp/.java_pid1: target process 1 doesn't respond within 10500ms or HotSpot VM not loaded\n")}
	for _, c := range []struct {
		name   string
		execs  map[string]execAnswer
		status int
		code   string
	}{
		{"jcmd missing behind env", map[string]execAnswer{envPrefix + "jcmd -l": {res: extension.ExecResult{ExitCode: 127,
			Stderr: []byte("env: 'jcmd': No such file or directory")}}}, http.StatusUnprocessableEntity, CodeNoJcmd},
		{"neither env nor jcmd", map[string]execAnswer{envPrefix + "jcmd -l": {err: noEnv}, "jcmd -l": {err: noJcmd}},
			http.StatusUnprocessableEntity, CodeNoJcmd},
		{"attach refused", map[string]execAnswer{envPrefix + "jcmd -l": {res: extension.ExecResult{Stdout: []byte("1 app.Main\n")}},
			envPrefix + "jcmd 1 VM.version": {res: refused}}, http.StatusUnprocessableEntity, CodeAttachRefused},
		{"attach refused on stderr", map[string]execAnswer{envPrefix + "jcmd -l": {res: extension.ExecResult{Stdout: []byte("1 app.Main\n")}},
			envPrefix + "jcmd 1 VM.version": {res: extension.ExecResult{ExitCode: 1, Stderr: []byte("java.io.IOException: Operation not permitted")}}},
			http.StatusUnprocessableEntity, CodeAttachRefused},
		{"pid 1 has the attach disabled", map[string]execAnswer{envPrefix + "jcmd -l": {res: extension.ExecResult{Stdout: []byte("40 jdk.jcmd/sun.tools.jcmd.JCmd -l\n")}},
			envPrefix + "jcmd 1 VM.version": {res: extension.ExecResult{ExitCode: 1, Stdout: []byte("1:\ncom.sun.tools.attach.AttachNotSupportedException: The VM does not support the attach mechanism\n")}}},
			http.StatusUnprocessableEntity, CodeAttachDisabled},
		{"listed pid has the attach disabled", map[string]execAnswer{envPrefix + "jcmd -l": {res: extension.ExecResult{Stdout: []byte("1 app.Main\n")}},
			envPrefix + "jcmd 1 VM.version": {res: extension.ExecResult{ExitCode: 1, Stdout: []byte("com.sun.tools.attach.AttachNotSupportedException: The VM does not support the attach mechanism\n")}}},
			http.StatusUnprocessableEntity, CodeAttachDisabled},
		{"no JVM listed, pid 1 is not one", map[string]execAnswer{envPrefix + "jcmd -l": {res: extension.ExecResult{Stdout: []byte("40 jdk.jcmd/sun.tools.jcmd.JCmd -l\n")}},
			envPrefix + "jcmd 1 VM.version": {res: refused}}, http.StatusUnprocessableEntity, CodeNoJVM},
		{"exec denied", map[string]execAnswer{envPrefix + "jcmd -l": {err: fmt.Errorf("review: %w", extension.ErrDenied)}},
			http.StatusForbidden, CodeExecForbidden},
		{"exec unreviewable", map[string]execAnswer{envPrefix + "jcmd -l": {err: fmt.Errorf("review: %w", extension.ErrUnreviewable)}},
			http.StatusServiceUnavailable, CodeExecUnreviewed},
		{"busy", map[string]execAnswer{envPrefix + "jcmd -l": {err: extension.ErrExecBusy}}, http.StatusTooManyRequests, CodeExecBusy},
		{"timed out", map[string]execAnswer{envPrefix + "jcmd -l": {err: extension.ErrExecTimedOut}}, http.StatusGatewayTimeout, CodeExecTimedOut},
		{"unknown cluster", map[string]execAnswer{envPrefix + "jcmd -l": {err: fmt.Errorf("%w", extension.ErrUnknownCluster)}},
			http.StatusNotFound, CodeUnknownCluster},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHost(t, pod("shop", "billing-0", corev1.Container{Name: "app", Image: "app"}))
			h.execs = c.execs
			rec := post(t, h, inspectPath, "")
			got := decode[problem](t, rec)
			if rec.Code != c.status || got.Code != c.code || got.Message == "" {
				t.Errorf("answered %d %+v, want %d %s", rec.Code, got, c.status, c.code)
			}
		})
	}
}

// Several JVMs: the list goes back with process ids and main classes only,
// nothing else is run, and `?pid=` then picks one without listing again.
func TestSeveralJVMsAreListedWithoutTheirArguments(t *testing.T) {
	h := newHost(t, pod("shop", "billing-0", corev1.Container{Name: "app", Image: "app"}))
	h.execs = map[string]execAnswer{envPrefix + "jcmd -l": {res: extension.ExecResult{
		Stdout: []byte("1 org.example.Launcher --api.token=s3cr3t\n7 /app/worker.jar -Dpassword=s3cr3t\n99 jdk.jcmd/sun.tools.jcmd.JCmd -l\n")}}}
	rec := post(t, h, inspectPath, "")
	if strings.Contains(rec.Body.String(), "s3cr3t") || strings.Contains(rec.Body.String(), "token") {
		t.Fatalf("jcmd -l's arguments were echoed: %s", rec.Body)
	}
	got := decode[inspectAnswer](t, rec)
	if got.State != "choose" || len(got.JVMs) != 2 || got.JVMs[1] != (listedJVM{PID: 7, MainClass: "worker.jar"}) || len(h.ran) != 1 {
		t.Errorf("answer = %+v, ran %q", got, ran(h))
	}

	chosen := jdkHost(t, "jdk21", false, true)
	for _, c := range inspected {
		chosen.execs[envPrefix+"jcmd 7 "+c] = chosen.execs[envPrefix+"jcmd 1 "+c]
	}
	rec = post(t, chosen, inspectPath+"?pid=7", "")
	answer := decode[inspectAnswer](t, rec)
	if answer.PID != 7 || answer.PIDFrom != "chosen" || ran(chosen)[0] != envPrefix+"jcmd 7 VM.version" {
		t.Errorf("answer %d %s, ran %q", answer.PID, answer.PIDFrom, ran(chosen))
	}
	for _, bad := range []string{"0", "-1", "1%20id", "abc", "99999999"} {
		if rec := post(t, newHost(t), inspectPath+"?pid="+bad, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("?pid=%s answered %d", bad, rec.Code)
		}
	}
}

// A command that fails after the first is said in its section; the others are
// still read. A cut output is marked.
func TestALaterFailureIsItsSectionsOnly(t *testing.T) {
	h := jdkHost(t, "jdk21", false, true)
	h.execs[envPrefix+"jcmd 1 VM.system_properties"] = execAnswer{err: extension.ErrExecTimedOut}
	flags := h.execs[envPrefix+"jcmd 1 VM.flags"]
	flags.res.Truncated = true
	h.execs[envPrefix+"jcmd 1 VM.flags"] = flags
	got := decode[inspectAnswer](t, post(t, h, inspectPath, ""))
	for _, s := range got.Sections {
		switch s.Command {
		case "VM.system_properties":
			if s.State != stateFailed || !strings.Contains(s.Sentence, "deadline") {
				t.Errorf("%+v", s)
			}
		case "VM.flags":
			if s.State != stateTruncated || !s.Truncated || !strings.Contains(s.Sentence, "not read") {
				t.Errorf("%+v", s)
			}
		}
	}
	// A cut answer is not read: no flags, no collector, no maximum heap from it.
	if got.Properties != nil || got.Summary.Version == "" || got.AllFlags != nil || got.Summary.GC != "" || got.Memory.HeapMax != nil {
		t.Errorf("properties %v, flags %v, summary %+v", got.Properties, got.AllFlags, got.Summary)
	}
}

// Without metrics-server there is no working set: no marker for it and nothing
// called outside the JVM.
func TestNoWorkingSetDrawsNothingOutside(t *testing.T) {
	h := jdkHost(t, "jdk21", false, false)
	h.live = nil // no metrics-server
	rec := post(t, h, inspectPath, "")
	got := decode[inspectAnswer](t, rec)
	m := got.Memory
	if m.WorkingSet != nil || !strings.Contains(m.WorkingSetNote, "metrics-server") || !strings.Contains(rec.Body.String(), `"workingSet":null`) {
		t.Errorf("memory = %+v", m)
	}
	for _, s := range m.Segments {
		if s.Unattributed {
			t.Errorf("an unattributed part without a working set: %+v", s)
		}
	}
	for _, mk := range m.Markers {
		if mk.Key == "workingSet" {
			t.Error("a working-set marker without a working set")
		}
	}
}

func TestOnlyARunningPodsOwnContainerIsInspected(t *testing.T) {
	pending := pod("shop", "web-0", corev1.Container{Name: "app"})
	pending.Status.Phase = corev1.PodPending
	h := newHost(t, pod("shop", "billing-0", corev1.Container{Name: "app"}), pending)
	for path, want := range map[string]string{
		"/x/kind/jvm/pod/shop/billing-0/sidecar/inspect":  CodeUnknownContainer,
		"/x/kind/jvm/pod/shop/gone-0/app/inspect":         CodeUnknownPod,
		"/x/kind/jvm/pod/shop/web-0/app/inspect":          CodeNotRunning,
		"/x/kind/jvm/pod/shop/billing-0/Bad_Name/inspect": CodeBadRequest,
	} {
		if got := decode[problem](t, post(t, h, path, "")); got.Code != want {
			t.Errorf("%s: %+v, want %s", path, got, want)
		}
	}
	if len(h.ran) != 0 {
		t.Errorf("ran %q", ran(h))
	}
}

// Reveal runs the one command again and answers the one value.
func TestRevealRunsOneCommandAndAnswersOneValue(t *testing.T) {
	for _, c := range []struct {
		body, command, value string
	}{
		{`{"pid":1,"kind":"property","name":"spring.datasource.password"}`, "VM.system_properties", "hunter2"},
		{`{"pid":1,"kind":"property","name":"sun.java.command"}`, "VM.system_properties", "D --db.password=hunter2"},
		{`{"pid":1,"kind":"option","name":"db.password"}`, "VM.command_line", "hunter2"},
		{`{"pid":1,"kind":"option","name":"spring.datasource.password"}`, "VM.command_line", "hunter2"},
	} {
		h := jdkHost(t, "jdk11", false, false)
		rec := post(t, h, inspectPath+"/reveal", c.body)
		got := decode[revealAnswer](t, rec)
		if rec.Code != http.StatusOK || got.Value != c.value {
			t.Errorf("%s: %d %+v", c.body, rec.Code, got)
		}
		if r := ran(h); len(r) != 1 || r[0] != envPrefix+"jcmd 1 "+c.command {
			t.Errorf("%s: ran %q", c.body, r)
		}
	}
	h := jdkHost(t, "jdk11", false, false)
	if got := decode[problem](t, post(t, h, inspectPath+"/reveal", `{"pid":1,"kind":"option","name":"app.name"}`)); got.Code != CodeNoSuchValue {
		t.Errorf("an option never masked: %+v", got)
	}
	for _, bad := range []string{``, `{}`, `{"pid":1,"kind":"heap_dump","name":"x"}`, `{"pid":0,"kind":"property","name":"x"}`,
		`{"pid":1,"kind":"property","name":"x","command":"GC.heap_dump"}`} {
		h := jdkHost(t, "jdk11", false, false)
		if rec := post(t, h, inspectPath+"/reveal", bad); rec.Code != http.StatusBadRequest || len(h.ran) != 0 {
			t.Errorf("%q answered %d and ran %q", bad, rec.Code, ran(h))
		}
	}
}

// The whitelist is structural: the argv builder refuses every command that is
// not in it, so no route can send one.
func TestOnlyTheWhitelistCanBeSent(t *testing.T) {
	for _, name := range []string{"GC.heap_dump", "JVMTI.agent_load", "VM.set_flag", "ManagementAgent.start",
		"GC.run", "System.gc", "GC.class_histogram -all=true", "GC.class_histogram -all", "Thread.print -l", "Thread.print -e", "JFR.start",
		"VM.native_memory detail", "PerfCounter.print -all", ""} {
		for _, plain := range []bool{false, true} {
			if argv, err := jcmdArgv(plain, 1, name); err == nil {
				t.Errorf("%q was built: %q", name, argv)
			}
		}
	}
	for _, name := range inspection {
		argv, err := jcmdArgv(false, 1, name)
		if err != nil || argv[len(argv)-len(diagnostics[name].args)-1] != diagnostics[name].name {
			t.Errorf("%s: %q %v", name, argv, err)
		}
	}
	if _, err := jcmdArgv(false, 0, dcmdVersion); err == nil {
		t.Error("pid 0 was accepted")
	}
	if len(diagnostics) != 12 {
		t.Errorf("%d commands are allowed: adding one is a decision, made here and in this test", len(diagnostics))
	}
}

// Host.Exec is called from one place, runner.exec, which takes its argv from
// jcmdArgv, listArgv, heapDumpArgv or fileArgv only.
func TestNothingElseCallsExec(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		n := strings.Count(string(src), ".Exec(")
		if want := map[bool]int{true: 1, false: 0}[f == "jcmd.go"]; n != want {
			t.Errorf("%s calls Exec %d times, want %d", f, n, want)
		}
	}
}

// The routes that read the spec run nothing: fakeHost fails the test on an
// Exec it was not given answers for.
func TestTheReadsRunNothing(t *testing.T) {
	h := shop(t)
	for _, path := range []string{"/x/kind/jvm/", "/x/kind/jvm/?ns=shop", "/x/kind/jvm/pod/shop/billing-0"} {
		if rec := serve(t, h, path); rec.Code != http.StatusOK {
			t.Errorf("%s answered %d", path, rec.Code)
		}
	}
	if len(h.ran) != 0 {
		t.Errorf("ran %q", ran(h))
	}
}

// Every exec carries a deadline tighter than Host.Exec's default, and the
// whole inspection has a budget.
func TestEveryExecHasATightDeadline(t *testing.T) {
	h := jdkHost(t, "jdk21", false, true)
	post(t, h, inspectPath, "")
	if len(h.timeouts) != 1+len(inspected) {
		t.Fatalf("%d execs", len(h.timeouts))
	}
	for i, d := range h.timeouts {
		if d <= 0 || d > dcmdTimeout || d >= extension.ExecTimeout {
			t.Errorf("exec %d asked for %s", i, d)
		}
	}
	if inspectBudget >= extension.MaxExecTimeout || revealBudget >= inspectBudget {
		t.Errorf("budgets %s, %s", inspectBudget, revealBudget)
	}
}

// A request that goes away mid-exec stops the inspection there: nothing more
// is sent, and no goroutine is left behind.
func TestACancelledInspectionStopsAndLeavesNothingRunning(t *testing.T) {
	h := jdkHost(t, "jdk21", false, true)
	started := make(chan struct{})
	h.block = map[string]bool{envPrefix + "jcmd 1 VM.uptime": true}
	h.blocked = started
	before := runtime.NumGoroutine()

	mux := http.NewServeMux()
	for _, r := range New().Routes(h) {
		mux.Handle(r.Method+" /x/{cluster}/jvm"+r.Pattern, r.Handler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, inspectPath, nil).WithContext(ctx))
	}()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler did not return after its request was canceled")
	}
	if got := ran(h); len(got) != 3 || got[2] != envPrefix+"jcmd 1 VM.uptime" {
		t.Errorf("ran %q: nothing is sent after the request went", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("%d goroutines before, %d after", before, after)
	}
}

// What a JVM prints is listed up to a bound, and the answer says it was cut.
func TestLongAnswersAreBounded(t *testing.T) {
	h := jdkHost(t, "jdk21", false, true)
	var props, flags strings.Builder
	props.WriteString("1:\n")
	flags.WriteString("1:\n")
	for i := range 5000 {
		fmt.Fprintf(&props, "p%d=v\n", i)
		fmt.Fprintf(&flags, "-XX:Flag%d=%d ", i, i)
	}
	h.execs[envPrefix+"jcmd 1 VM.system_properties"] = execAnswer{res: extension.ExecResult{Stdout: []byte(props.String())}}
	h.execs[envPrefix+"jcmd 1 VM.flags"] = execAnswer{res: extension.ExecResult{Stdout: []byte(flags.String())}}
	got := decode[inspectAnswer](t, post(t, h, inspectPath, ""))
	if len(got.Properties) != maxProperties || len(got.AllFlags) != maxFlags || len(got.Notes) != 2 {
		t.Errorf("%d properties, %d flags, notes %q", len(got.Properties), len(got.AllFlags), got.Notes)
	}
	var list strings.Builder
	for i := range 100 {
		fmt.Fprintf(&list, "%d app.Main%d\n", i+1, i)
	}
	if n := len(parseList([]byte(list.String()))); n != maxListedJVMs {
		t.Errorf("%d JVMs listed", n)
	}
}
