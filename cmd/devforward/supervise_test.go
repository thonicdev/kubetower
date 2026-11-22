package main

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The supervisor and -stop are tested against a fake kubectl: this test binary,
// started again with fakeKubectlEnv naming a directory. It never runs kubectl,
// and so never reaches a cluster.
const (
	fakeKubectlEnv = "DEVFORWARD_TEST_FAKE_KUBECTL"
	// fakeExitEnv makes the fake exit as soon as it has said it listens, so
	// the supervisor restarts it.
	fakeExitEnv = "DEVFORWARD_TEST_FAKE_EXIT"
)

func TestMain(m *testing.M) {
	if dir := os.Getenv(fakeKubectlEnv); dir != "" {
		fakeKubectl(dir)
		return
	}
	os.Exit(m.Run())
}

// fakeKubectl stands in for `kubectl port-forward`: it records its PID in dir,
// prints the line kubectl prints once it listens, and holds until it is killed.
// Its arguments are whatever it was started with, so its command line is what
// -stop reads.
func fakeKubectl(dir string) {
	_ = os.WriteFile(filepath.Join(dir, strconv.Itoa(os.Getpid())), nil, 0o644)
	fmt.Println("Forwarding from 127.0.0.1:0 -> 0")
	if os.Getenv(fakeExitEnv) != "" {
		os.Exit(1)
	}
	time.Sleep(2 * time.Minute)
	os.Exit(3)
}

// recordedPIDs is every PID a fake kubectl wrote into dir.
func recordedPIDs(t *testing.T, dir string) []int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// alive asks the platform, through the same commandLine -stop uses.
func alive(t *testing.T, pid int) bool {
	t.Helper()
	_, found, err := commandLine(pid)
	if err != nil {
		t.Fatalf("commandLine(%d): %v", pid, err)
	}
	return found
}

// killSurvivors kills every fake that recorded itself in dir and is still
// running, so a failing test leaves nothing behind.
func killSurvivors(t *testing.T, dir string) {
	for _, pid := range recordedPIDs(t, dir) {
		if alive(t, pid) {
			if p, err := os.FindProcess(pid); err == nil {
				_ = p.Kill()
			}
		}
	}
}

func waitFor(within time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func gone(path string) func() bool {
	return func() bool {
		_, err := os.Stat(path)
		return errors.Is(err, os.ErrNotExist)
	}
}

// supervising runs supervise on one forward against the fake kubectl, and
// returns the channel its exit code arrives on.
func supervising(t *testing.T, dir, pids string, addresses ...string) <-chan int {
	t.Helper()
	t.Setenv(fakeKubectlEnv, pids)
	t.Cleanup(func() { killSurvivors(t, pids) })
	if len(addresses) == 0 {
		addresses = []string{"127.0.0.1"}
	}
	f := forward{Name: "x", Namespace: "ns", Resource: "svc/x", Local: freePort(t), Remote: 1, Addresses: addresses}
	code := make(chan int, 1)
	go func() { code <- supervise(os.Args[0], "fake", dir, []forward{f}) }()
	return code
}

// A forward past the stopping check when the stop arrives must not start its
// kubectl after the shutdown has killed every child it knew of: nothing would
// kill it, and the shutdown would wait on it for ever. The hook
// holds the start until the shutdown has consumed the stop request - bounded,
// so that a supervisor which never consumes it still reaches the start.
func TestAForwardStartingDuringShutdownIsKilled(t *testing.T) {
	dir, pids := t.TempDir(), t.TempDir()
	stopPath := filepath.Join(dir, stopName)
	held := make(chan struct{})
	var once sync.Once
	startHook = func(string) {
		once.Do(func() {
			if err := os.WriteFile(stopPath, nil, 0o644); err != nil {
				t.Error(err)
			}
			waitFor(3*time.Second, gone(stopPath))
			close(held)
		})
	}
	t.Cleanup(func() { startHook = nil })

	code := supervising(t, dir, pids)
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("the forward never reached its start")
	}
	select {
	case c := <-code:
		if c != 0 {
			t.Errorf("supervise returned %d after a stop, want 0", c)
		}
	case <-time.After(8 * time.Second):
		t.Error("supervise did not return within 8s of being asked to stop")
	}
	// The fake may never have run far enough to record itself, when it is
	// killed at once; one that did must be gone.
	for _, pid := range recordedPIDs(t, pids) {
		if alive(t, pid) {
			t.Errorf("the fake kubectl (pid %d) started during the shutdown and outlived it", pid)
		}
	}
	if !gone(filepath.Join(dir, stateName))() {
		t.Error("the state file is still there after a stop")
	}
}

// A second stop, while the shutdown waits on a forward that does not end, ends
// the supervisor at once rather than leaving the terminal that asked hanging.
// The hook holds a start for good, which is what keeps the shutdown waiting.
func TestASecondStopEndsAShutdownThatHangs(t *testing.T) {
	dir, pids := t.TempDir(), t.TempDir()
	stopPath := filepath.Join(dir, stopName)
	release := make(chan struct{})
	var once sync.Once
	startHook = func(string) {
		once.Do(func() {
			_ = os.WriteFile(stopPath, nil, 0o644)
			<-release
		})
	}
	t.Cleanup(func() { startHook = nil })

	code := supervising(t, dir, pids)
	// Released before supervising's own cleanups, which run after this one:
	// the held start then meets a stopped supervisor while the fake's
	// environment is still set, and kills its own kubectl.
	t.Cleanup(func() { close(release); time.Sleep(300 * time.Millisecond) })
	if !waitFor(10*time.Second, func() bool { return !gone(stopPath)() }) {
		t.Fatal("the hook never asked to stop")
	}
	if !waitFor(5*time.Second, gone(stopPath)) {
		t.Fatal("the shutdown never consumed the first stop request")
	}
	if err := os.WriteFile(stopPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-code:
		if c != 1 {
			t.Errorf("supervise returned %d after a second stop, want 1", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a second stop did not end a shutdown that hangs")
	}
}

// The note that a forward is on the network is said
// on every bind, not only the first - a restart after a network change binds
// whatever the address is now.
func TestEveryBindSaysWhenAForwardIsOnTheNetwork(t *testing.T) {
	var buf bytes.Buffer
	outMu.Lock()
	stdout = &buf
	outMu.Unlock()
	t.Cleanup(func() {
		outMu.Lock()
		stdout = os.Stdout
		outMu.Unlock()
	})
	read := func() string {
		outMu.Lock()
		defer outMu.Unlock()
		return buf.String()
	}

	dir, pids := t.TempDir(), t.TempDir()
	t.Setenv(fakeExitEnv, "1")
	// TEST-NET-1: never this machine, so nothing answers the supervisor's
	// look before it starts; the fake binds nothing anyway.
	code := supervising(t, dir, pids, "127.0.0.1", "192.0.2.1")
	ok := waitFor(10*time.Second, func() bool { return strings.Count(read(), "up again") >= 1 })
	if err := os.WriteFile(filepath.Join(dir, stopName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-code:
	case <-time.After(8 * time.Second):
		t.Fatal("supervise did not stop")
	}
	out := read()
	if !ok {
		t.Fatalf("the forward was never restarted:\n%s", out)
	}
	binds := strings.Count(out, ": up")
	notes := strings.Count(out, "192.0.2.1 is not loopback")
	if notes < binds || binds < 2 {
		t.Errorf("%d binds and %d notes that the forward is on the network, want a note per bind:\n%s", binds, notes, out)
	}
}

// fake is a fake kubectl started by hand, for -stop to be pointed at.
type fake struct {
	pid    int
	exited chan struct{}
}

func startFake(t *testing.T, pids string, args ...string) fake {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), fakeKubectlEnv+"="+pids)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	f := fake{pid: cmd.Process.Pid, exited: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(f.exited) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-f.exited })
	waitRecorded(t, pids, f.pid)
	return f
}

// waitRecorded holds until the fake kubectl pid has written itself into dir.
// cmd.Start returns once the exec is under way, not once it is done: on Linux
// /proc/<pid>/cmdline can still read "" then, until the kernel has set the new
// arguments. The fake writes the file from its own main, after the exec, so its
// command line is in place once the file is.
func waitRecorded(t *testing.T, dir string, pid int) {
	t.Helper()
	path := filepath.Join(dir, strconv.Itoa(pid))
	if !waitFor(10*time.Second, func() bool { _, err := os.Stat(path); return err == nil }) {
		t.Fatalf("the fake kubectl (pid %d) did not record itself within 10s", pid)
	}
}

func (f fake) exitedWithin(d time.Duration) bool {
	select {
	case <-f.exited:
		return true
	case <-time.After(d):
		return false
	}
}

func writeStale(t *testing.T, dir string, rec record) {
	t.Helper()
	path := filepath.Join(dir, stateName)
	if err := writeRecord(path, rec); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// -stop closes a recorded PID only when its command
// line is the forward devforward started - not on its image name alone, which
// the operator's own kubectl shares.
func TestStopClosesOnlyTheForwardsItStarted(t *testing.T) {
	dir, pids := t.TempDir(), t.TempDir()
	local := freePort(t)
	ports := fmt.Sprintf("%d:5823", local)
	ours := startFake(t, pids, "--context", "kind-dev", "--namespace", "ns", "port-forward", "svc/x", ports,
		"--address", "127.0.0.1")
	// The operator's kubectl on the same context, on a PID the record names.
	theirs := startFake(t, pids, "--context", "kind-dev", "get", "pods", "--watch")
	// The same forward against another context.
	elsewhere := startFake(t, pids, "--context", "prod", "--namespace", "ns", "port-forward", "svc/x", ports)
	writeStale(t, dir, record{
		PID:      theirs.pid, // a reused supervisor PID: a process, and not devforward
		Context:  "kind-dev",
		Forwards: []forward{{Name: "console", Namespace: "ns", Resource: "svc/x", Local: local, Remote: 5823}},
		Children: map[string]int{"console": ours.pid, "dex": theirs.pid, "old": elsewhere.pid},
	})

	if code := stopRecorded(dir); code != 0 {
		t.Errorf("stopRecorded returned %d, want 0", code)
	}
	if !ours.exitedWithin(5 * time.Second) {
		t.Error("the recorded forward was not closed")
	}
	if theirs.exitedWithin(300 * time.Millisecond) {
		t.Error("a kubectl that is not a port-forward was closed")
	}
	if elsewhere.exitedWithin(0) {
		t.Error("a port-forward against another context was closed")
	}
	if !gone(filepath.Join(dir, stateName))() {
		t.Error("the state file is still there")
	}
}

// When the supervisor does not answer, -stop closes
// what the record names after the wait, not before it - a forward restarted
// while it waited is only in the record as it is now.
func TestStopReadsTheRecordAgainAfterWaiting(t *testing.T) {
	dir, pids := t.TempDir(), t.TempDir()
	saved := stopWait
	stopWait = time.Second
	t.Cleanup(func() { stopWait = saved })

	local := freePort(t)
	// Fresh, so -stop asks and waits; its supervisor PID is not devforward, so
	// nothing answers and nothing is killed for it.
	supervisor := startFake(t, pids, "not", "a", "supervisor")
	rec := record{PID: supervisor.pid, Context: "kind-dev",
		Forwards: []forward{{Name: "console", Namespace: "ns", Resource: "svc/x", Local: local, Remote: 5823}}}
	if err := writeRecord(filepath.Join(dir, stateName), rec); err != nil {
		t.Fatal(err)
	}
	restarted := startFake(t, pids, "--context", "kind-dev", "--namespace", "ns", "port-forward", "svc/x",
		fmt.Sprintf("%d:5823", local))
	go func() {
		time.Sleep(300 * time.Millisecond)
		rec.Children = map[string]int{"console": restarted.pid}
		if err := writeRecord(filepath.Join(dir, stateName), rec); err != nil {
			t.Error(err)
		}
	}()

	stopRecorded(dir)
	if !restarted.exitedWithin(5 * time.Second) {
		t.Error("a forward recorded while -stop waited was left running")
	}
}

func TestMarks(t *testing.T) {
	rec := record{Context: "docker-desktop", Forwards: []forward{{Name: "dex", Local: 5556, Remote: 5556}}}
	line := `C:\go-build\kubectl.exe --context docker-desktop --namespace dex port-forward svc/dex 5556:5556 --address 127.0.0.1`
	if m := missingMark(line, childMarks(rec, "dex")); m != "" {
		t.Errorf("our forward's command line lacks %q", m)
	}
	for _, other := range []string{
		`kubectl.exe --context docker-desktop get pods -w`,
		`kubectl.exe --context prod --namespace dex port-forward svc/dex 5556:5556`,
		`kubectl.exe --context docker-desktop --namespace dex port-forward svc/dex 15556:5557`,
		``, // a command line that cannot be read
	} {
		if missingMark(other, childMarks(rec, "dex")) == "" {
			t.Errorf("%q taken for our forward", other)
		}
	}
	sup := `C:\Temp\go-build1\b001\exe\devforward.exe -context docker-desktop -state C:/Temp/kubetower-incluster -forward dex=dex/svc/dex:5556:5556`
	if m := missingMark(sup, supervisorMarks(rec)); m != "" {
		t.Errorf("the supervisor's command line lacks %q", m)
	}
	if missingMark(`devforward.test.exe -test.run TestX`, supervisorMarks(rec)) == "" {
		t.Error("a devforward that is not a supervisor taken for one")
	}

	for line, want := range map[string]string{
		`"C:\Program Files\k\kubectl.exe" --token abc get pods`: `C:\Program Files\k\kubectl.exe`,
		`/usr/bin/kubectl --token abc get pods`:                 `/usr/bin/kubectl`,
	} {
		if got := executable(line); got != want {
			t.Errorf("executable(%q) = %q, want %q", line, got, want)
		}
	}
}

// commandLine tells a running process from one that is gone, and reads the
// arguments it was started with.
func TestCommandLine(t *testing.T) {
	pids := t.TempDir()
	f := startFake(t, pids, "--context", "kind-dev", "port-forward", "svc/x", "1:2")
	line, found, err := commandLine(f.pid)
	if err != nil || !found {
		t.Fatalf("commandLine(%d) = %q, %v, %v", f.pid, line, found, err)
	}
	if !strings.Contains(line, "port-forward svc/x 1:2") {
		t.Errorf("command line %q", line)
	}
	p, _ := os.FindProcess(f.pid)
	_ = p.Kill()
	<-f.exited
	if !waitFor(5*time.Second, func() bool { _, found, _ := commandLine(f.pid); return !found }) {
		t.Errorf("pid %d still found after it exited", f.pid)
	}
}

// A process that exists and shows no command line - on Linux one still inside
// exec, or a zombie - is "cannot tell" to -stop: left alone, never killed, even
// when the marks asked for would match anything.
func TestCloseIfOursLeavesAnUnreadableCommandLineAlone(t *testing.T) {
	pids := t.TempDir()
	f := startFake(t, pids, "--context", "kind-dev", "--namespace", "ns", "port-forward", "svc/x", "1:2")
	saved, savedOut := readCommandLine, stdout
	t.Cleanup(func() { readCommandLine, stdout = saved, savedOut })
	readCommandLine = func(pid int) (string, bool, error) {
		if pid == f.pid {
			return "", true, nil
		}
		return saved(pid)
	}
	var out bytes.Buffer
	stdout = &out

	rec := record{Context: "kind-dev", Forwards: []forward{{Name: "x", Local: 1, Remote: 2}}}
	for _, marks := range [][]string{childMarks(rec, "x"), {""}, nil} {
		closeIfOurs("x", f.pid, marks)
	}
	if f.exitedWithin(300 * time.Millisecond) {
		t.Fatal("a process whose command line reads empty was killed")
	}
	if got := strings.Count(out.String(), "cannot be read; left alone"); got != 3 {
		t.Errorf("%d of 3 calls said the command line cannot be read:\n%s", got, out.String())
	}
}

// The same without a stand-in, on the platform that produces it: a zombie on
// Linux exists and reads an empty command line, and -stop leaves it alone.
func TestALinuxZombieIsLeftAlone(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("a zombie with an empty /proc/<pid>/cmdline is Linux's")
	}
	pids := t.TempDir()
	cmd := exec.Command(os.Args[0], "--context", "kind-dev", "port-forward", "svc/x", "1:2")
	cmd.Env = append(os.Environ(), fakeKubectlEnv+"="+pids)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	waitRecorded(t, pids, cmd.Process.Pid)
	// Killed and not yet waited on: a zombie until Wait reaps it.
	_ = cmd.Process.Kill()
	stat := filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "stat")
	if !waitFor(10*time.Second, func() bool {
		raw, err := os.ReadFile(stat)
		s := string(raw)
		i := strings.LastIndex(s, ") ") // the name in parentheses may hold ") "
		return err == nil && i >= 0 && strings.HasPrefix(s[i+2:], "Z")
	}) {
		t.Fatalf("pid %d did not become a zombie", cmd.Process.Pid)
	}

	line, found, err := commandLine(cmd.Process.Pid)
	if err != nil || !found || line != "" {
		t.Fatalf("commandLine(zombie %d) = %q, %v, %v; want \"\", true, nil", cmd.Process.Pid, line, found, err)
	}
	savedOut := stdout
	t.Cleanup(func() { stdout = savedOut })
	var out bytes.Buffer
	stdout = &out
	closeIfOurs("x", cmd.Process.Pid, nil)
	if !strings.Contains(out.String(), "cannot be read; left alone") {
		t.Errorf("closeIfOurs on a zombie said %q", out.String())
	}
}
