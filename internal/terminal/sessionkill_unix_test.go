//go:build !windows

package terminal

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestABackgroundJobDoesNotOutliveItsShell: a shell with job control puts
// `sleep 1000 &` in a process group of its own, which neither the hang-up of
// closing the terminal nor a kill of the shell's PID reaches: the job would be
// reparented to init and run on after the session closed. The teardown a
// session runs - Close, Kill, Wait - ends it.
func TestABackgroundJobDoesNotOutliveItsShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this machine")
	}
	session, err := startPTYSession(exec.Command(sh, "-i"), initialCols, initialRows)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(); _ = session.Kill(); _ = session.Wait() })

	// `set -m` turns job control on whatever the platform's sh decides for an
	// interactive shell, so the job is in a group of its own, as it is in the
	// operator's bash or zsh.
	if _, err := session.Write([]byte("set -m; sleep 1000 & echo JOB=$!\n")); err != nil {
		t.Fatal(err)
	}
	pid := readJobPID(t, session)

	if alive, _ := processStart(pid); !alive {
		t.Fatalf("the background job %d was not running before the session closed", pid)
	}
	_ = session.Close()
	_ = session.Kill()
	_ = session.Wait()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if exited(pid) {
			return
		}
		if time.Now().After(deadline) {
			// The state and the parent tell the two failures apart: a job Kill
			// never reached (state S, parent 1), or one that died and that
			// exited still misread.
			state, ppid := procStat(pid)
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the background job %d was still running 5s after its session closed (state %q, parent %q)",
				pid, state, ppid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// exited reports whether pid has ended: gone altogether, or dead and not yet
// reaped. The second is not a race to wait out. Once its shell is gone the job
// is reparented to PID 1, and in the image's test gate PID 1 is the `go`
// command itself: the RUN step's `sh -c` execs it, it waits for its own children
// only, so a job that Kill did end stays a zombie for the rest of the run, and
// kill(pid, 0) succeeds on a zombie, which would read as "still running 5s
// after its session closed".
// The shipped image runs under tini, which reaps it; so does a desktop's init.
//
// The state is read from /proc where there is one. macOS has none, and its
// launchd reaps an orphan at once, so kill(pid, 0) is enough there.
func exited(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return true
	}
	state, _ := procStat(pid)
	return state == "Z"
}

// procStat reads a process's state and parent PID from /proc/<pid>/stat. Both
// are empty where there is no /proc (macOS), or once the process is gone.
func procStat(pid int) (state, ppid string) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", ""
	}
	// The command name is in parentheses and may hold spaces or parentheses;
	// the state and the parent are the first two fields after the last one.
	rest := string(raw)
	if i := strings.LastIndexByte(rest, ')'); i >= 0 {
		rest = rest[i+1:]
	}
	fields := strings.Fields(rest)
	if len(fields) < 2 {
		return "", ""
	}
	return fields[0], fields[1]
}

var jobLine = regexp.MustCompile(`JOB=(\d+)`)

// readJobPID reads the terminal until the shell has said the job's PID.
func readJobPID(t *testing.T, session ptySession) int {
	t.Helper()
	found := make(chan int, 1)
	go func() {
		scanner := bufio.NewScanner(session)
		for scanner.Scan() {
			if m := jobLine.FindStringSubmatch(scanner.Text()); m != nil {
				if pid, err := strconv.Atoi(m[1]); err == nil && pid > 0 {
					found <- pid
					return
				}
			}
		}
	}()
	select {
	case pid := <-found:
		return pid
	case <-time.After(10 * time.Second):
		t.Fatal("the shell never said the background job's PID")
		return 0
	}
}
