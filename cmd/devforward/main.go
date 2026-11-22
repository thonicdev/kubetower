// Command devforward keeps a set of `kubectl port-forward` processes alive, and
// says so every time one of them dies and comes back.
//
// `task incluster:up` ends in it and `task incluster:down` stops it. The
// in-cluster development environment needs Dex (5556) and the console (8824)
// forwarded for as long as anybody works in it, and an unsupervised forward dies
// on a pod restart, a sleeping laptop or a `helm upgrade`.
//
// # Why a program and not a shell loop
//
//   - Stopping it from another terminal: on Windows a PID file and `kill` do
//     not work across Task's shell and Git Bash, so `-stop` drops a file the
//     supervisor polls for, and the supervisor closes its own children.
//   - Not restarting what is being stopped: on Windows Ctrl-C reaches kubectl
//     too, and a loop that restarts a forward on exit races its own shutdown.
//   - Knowing when a forward is back: "up again" is printed on kubectl's own
//     `Forwarding from` line, not when kubectl starts.
//
// Standard library only, like devwatch.
//
// It does not probe a running forward (kubectl exits when its pod goes; a
// wedged one is restarted by hand), and it never reads a kubeconfig: every
// kubectl call names the context it was given.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The state directory's two files. stateName is rewritten every heartbeat while
// the supervisor runs, so its age is the liveness test; stopName is how -stop
// asks the supervisor to end.
const (
	stateName = "devforward.json"
	stopName  = "devforward.stop"
)

const (
	heartbeat = 500 * time.Millisecond
	// stale is how old the state file may be before the supervisor that wrote
	// it is taken for dead. Several heartbeats, so a slow disk is not a death.
	stale = 5 * time.Second
	// firstDelay and maxDelay bound the restart backoff. A forward that stayed
	// up for at least steadyAfter restarts after firstDelay again: "back within
	// seconds" is the point, and only a forward failing in a loop backs off.
	firstDelay  = time.Second
	maxDelay    = 10 * time.Second
	steadyAfter = 10 * time.Second
)

// stopWait is how long -stop waits for the supervisor to answer before closing
// what it recorded itself. A variable so the tests need not wait it out.
var stopWait = 10 * time.Second

// startHook, when set, runs in a forward's goroutine between the stopping check
// and kubectl's start. Tests use it to hold a start open across a shutdown.
var startHook func(name string)

// forwardList is the repeatable -forward flag.
type forwardList []forward

func (l *forwardList) String() string { return fmt.Sprint(len(*l)) }
func (l *forwardList) Set(spec string) error {
	f, err := parseForward(spec)
	if err != nil {
		return err
	}
	for _, have := range *l {
		if have.Name == f.Name {
			return fmt.Errorf("two forwards named %q", f.Name)
		}
	}
	*l = append(*l, f)
	return nil
}

func main() {
	context := flag.String("context", "", "the kubectl context every forward runs against (required)")
	stateDir := flag.String("state", "", "the directory the supervisor records itself in (required)")
	kubectl := flag.String("kubectl", "kubectl", "the kubectl to run")
	stop := flag.Bool("stop", false, "stop the supervisor recorded in -state, and its forwards, then exit")
	var forwards forwardList
	flag.Var(&forwards, "forward", "name=namespace/resource:local:remote[@address,...] - repeatable")
	flag.Parse()

	if *stateDir == "" {
		fail("-state is required")
	}
	if *stop {
		os.Exit(stopRecorded(*stateDir))
	}
	if *context == "" {
		// Never kubectl's current context: on a developer machine that is a real
		// cluster, and the forwards are a development fixture's.
		fail("-context is required; devforward never uses the current context")
	}
	if len(forwards) == 0 {
		fail("no -forward given")
	}
	os.Exit(supervise(*kubectl, *context, *stateDir, forwards))
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "devforward: "+format+"\n", args...)
	os.Exit(2)
}

// forward is one `kubectl port-forward`, as the flag describes it.
type forward struct {
	Name      string   `json:"name"`
	Namespace string   `json:"namespace"`
	Resource  string   `json:"resource"`
	Local     int      `json:"local"`
	Remote    int      `json:"remote"`
	Addresses []string `json:"addresses"`
}

// parseForward reads `name=namespace/resource:local:remote[@address,...]`, for
// example `console=kubetower/svc/kubetower:8824:5823@127.0.0.1`. The addresses
// default to 127.0.0.1 alone; a host name among them is resolved each time the
// forward starts, because kubectl's --address takes IP addresses only.
func parseForward(spec string) (forward, error) {
	name, rest, ok := strings.Cut(spec, "=")
	if !ok || name == "" {
		return forward{}, fmt.Errorf("forward %q: want name=namespace/resource:local:remote[@address,...]", spec)
	}
	target, addrs, hasAddrs := strings.Cut(rest, "@")
	f := forward{Name: name, Addresses: []string{"127.0.0.1"}}
	if hasAddrs {
		f.Addresses = nil
		for _, a := range strings.Split(addrs, ",") {
			if a = strings.TrimSpace(a); a == "" {
				continue
			}
			// Every interface is refused: narrowing it is the point.
			if ip := net.ParseIP(a); ip != nil && ip.IsUnspecified() {
				return forward{}, fmt.Errorf("forward %q: %s is every interface on this machine; "+
					"name the addresses to bind instead", spec, a)
			}
			f.Addresses = append(f.Addresses, a)
		}
		if len(f.Addresses) == 0 {
			return forward{}, fmt.Errorf("forward %q: an empty address list", spec)
		}
	}
	parts := strings.Split(target, ":")
	if len(parts) != 3 {
		return forward{}, fmt.Errorf("forward %q: want namespace/resource:local:remote", spec)
	}
	ns, resource, ok := strings.Cut(parts[0], "/")
	if !ok || ns == "" || resource == "" {
		return forward{}, fmt.Errorf("forward %q: %q is not namespace/resource", spec, parts[0])
	}
	f.Namespace, f.Resource = ns, resource
	var err error
	if f.Local, err = port(parts[1]); err != nil {
		return forward{}, fmt.Errorf("forward %q: local port: %v", spec, err)
	}
	if f.Remote, err = port(parts[2]); err != nil {
		return forward{}, fmt.Errorf("forward %q: remote port: %v", spec, err)
	}
	return f, nil
}

func port(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("%q is not a port", s)
	}
	return n, nil
}

// resolve turns the forward's addresses into IP addresses kubectl accepts, and
// says what it did with every one that is not plainly loopback: one that puts
// the forward on the network, one that does not resolve, and one that resolves
// to every interface, which is refused as parseForward refuses it written out.
// lookup is net.LookupHost outside the tests.
func (f forward) resolve(lookup func(string) ([]string, error)) (ips, notes []string) {
	seen := map[string]bool{}
	add := func(ip string) {
		if !seen[ip] {
			seen[ip] = true
			ips = append(ips, ip)
		}
	}
	onNetwork := func(ip, via string) string {
		return fmt.Sprintf("%snot loopback: %s is reachable from this machine's network on %s while it runs",
			via, f.Name, net.JoinHostPort(ip, strconv.Itoa(f.Local)))
	}
	for _, a := range f.Addresses {
		if a == "localhost" {
			add(a)
			continue
		}
		if parsed := net.ParseIP(a); parsed != nil {
			if parsed.IsUnspecified() { // parseForward refuses it; resolve is not the only door
				notes = append(notes, fmt.Sprintf("%s is every interface, so %s is not bound there", a, f.Name))
				continue
			}
			add(a)
			if !parsed.IsLoopback() {
				notes = append(notes, onNetwork(a, a+" is "))
			}
			continue
		}
		found, err := lookup(a)
		if err != nil || len(found) == 0 {
			notes = append(notes, fmt.Sprintf("%s does not resolve on this machine, so %s is not bound there", a, f.Name))
			continue
		}
		for _, ip := range found {
			parsed := net.ParseIP(ip)
			switch {
			case parsed == nil:
			case parsed.IsUnspecified():
				notes = append(notes, fmt.Sprintf("%s resolves to %s here, which is every interface, so %s is not bound there",
					a, ip, f.Name))
			default:
				add(ip)
				if !parsed.IsLoopback() {
					notes = append(notes, onNetwork(ip, fmt.Sprintf("%s resolves to %s here, which is ", a, ip)))
				}
			}
		}
	}
	return ips, notes
}

func (f forward) describe(ips []string) string {
	bound := make([]string, len(ips))
	for i, ip := range ips {
		bound[i] = net.JoinHostPort(ip, strconv.Itoa(f.Local))
	}
	return fmt.Sprintf("%s -> %s %s:%d", strings.Join(bound, ", "), f.Namespace, f.Resource, f.Remote)
}

// record is what the state file holds: enough for -stop to reach the
// supervisor, and to clean up after one that is gone.
type record struct {
	PID      int            `json:"pid"`
	Context  string         `json:"context"`
	Forwards []forward      `json:"forwards"`
	Children map[string]int `json:"children"`
}

// outMu serializes every line this process prints, so two forwards talking at
// once never interleave mid-line. stdout is where they go; the tests read it.
var (
	outMu  sync.Mutex
	stdout io.Writer = os.Stdout
)

// say prints one line, with the time: "back within seconds" is the claim a
// reader of this output checks.
func say(format string, args ...any) {
	outMu.Lock()
	defer outMu.Unlock()
	fmt.Fprintf(stdout, "devforward %s: %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

func supervise(kubectl, context, dir string, forwards []forward) int {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		say("%v", err)
		return 1
	}
	statePath := filepath.Join(dir, stateName)
	stopPath := filepath.Join(dir, stopName)

	if rec, age, err := readRecord(statePath); err == nil && age < stale {
		say("already running (pid %d, context %s). `task incluster:down` stops it.", rec.PID, rec.Context)
		return 1
	} else if err == nil {
		say("a supervisor recorded in %s stopped answering %s ago; its forwards may still hold their ports. "+
			"`task incluster:down` clears them.", statePath, age.Round(time.Second))
	}
	_ = os.Remove(stopPath)

	// Refuse to start over a port something already answers on, typically a
	// forward left by a killed supervisor: it would restart forever.
	for _, f := range forwards {
		ips, _ := f.resolve(net.LookupHost)
		for _, ip := range ips {
			addr := net.JoinHostPort(ip, strconv.Itoa(f.Local))
			if c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
				c.Close()
				say("something already answers on %s, which %s needs. `task incluster:down` stops a forward "+
					"this left behind; anything else, stop it yourself.", addr, f.Name)
				return 1
			}
		}
	}

	// mu guards stopping and children together: a forward either registers its
	// kubectl before the shutdown sets stopping, and is killed by it, or sees
	// stopping and kills its own. So no kubectl outlives a stop.
	var mu sync.Mutex
	stopping := false
	children := map[string]*os.Process{}
	isStopping := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return stopping
	}
	rec := record{PID: os.Getpid(), Context: context, Forwards: forwards}

	// One writer at a time: every writer uses the same temporary file, and on
	// Windows a rename over a file another writer holds open fails.
	var writeMu sync.Mutex
	writeState := func() {
		writeMu.Lock()
		defer writeMu.Unlock()
		mu.Lock()
		rec.Children = map[string]int{}
		for name, p := range children {
			rec.Children[name] = p.Pid
		}
		snapshot := rec
		mu.Unlock()
		if err := writeRecord(statePath, snapshot); err != nil {
			say("cannot write %s: %v", statePath, err)
		}
	}
	writeState()

	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupted)
	done := make(chan struct{})
	var wg sync.WaitGroup

	for _, f := range forwards {
		wg.Add(1)
		go func(f forward) {
			defer wg.Done()
			delay := firstDelay
			for restart := 0; ; restart++ {
				if isStopping() {
					return
				}
				// Resolve and report on every bind: after a network change a
				// name may resolve elsewhere.
				ips, notes := f.resolve(net.LookupHost)
				for _, n := range notes {
					say("%s", n)
				}
				if len(ips) == 0 {
					say("%s: none of %v resolves, so it cannot be bound", f.Name, f.Addresses)
					return
				}
				args := []string{"--context", context, "--namespace", f.Namespace, "port-forward", f.Resource,
					fmt.Sprintf("%d:%d", f.Local, f.Remote), "--address", strings.Join(ips, ",")}
				cmd := exec.Command(kubectl, args...)
				// Bounds Wait once kubectl has exited, should anything it
				// started still hold the pipe: a shutdown waits on this.
				cmd.WaitDelay = 2 * time.Second
				announced := false
				emit := func(line string) {
					switch kindOf(line) {
					case chatter:
					case listening:
						// One line per address; the first is the news.
						if !announced {
							announced = true
							if restart == 0 {
								say("%s: up, %s", f.Name, f.describe(ips))
							} else {
								say("%s: up again, %s (restart %d)", f.Name, f.describe(ips), restart)
							}
						}
					default:
						say("%s: %s", f.Name, line)
					}
				}
				// Both streams into one pipe, so every line is filtered and
				// prefixed whichever stream it arrives on.
				pr, pw := io.Pipe()
				cmd.Stdout, cmd.Stderr = pw, pw
				scanned := make(chan struct{})
				go func() { scanLines(pr, emit); close(scanned) }()
				if startHook != nil {
					startHook(f.Name)
				}
				started := time.Now()
				if err := cmd.Start(); err != nil {
					pw.Close()
					<-scanned
					say("%s: cannot start kubectl: %v", f.Name, err)
				} else {
					mu.Lock()
					if stopping {
						// Started after the shutdown killed every kubectl it
						// knew of, so nothing else will kill this one.
						mu.Unlock()
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
						pw.Close()
						<-scanned
						return
					}
					children[f.Name] = cmd.Process
					mu.Unlock()
					writeState()
					err := cmd.Wait()
					pw.Close()
					<-scanned
					mu.Lock()
					delete(children, f.Name)
					stopped := stopping
					mu.Unlock()
					if stopped {
						return
					}
					writeState()
					why := "it exited"
					if err != nil {
						why = err.Error()
					}
					if time.Since(started) >= steadyAfter {
						delay = firstDelay
					}
					say("%s: the forward died after %s (%s) - restarting in %s",
						f.Name, time.Since(started).Round(time.Second), why, delay)
				}
				select {
				case <-done:
					return
				case <-time.After(delay):
				}
				if delay *= 2; delay > maxDelay {
					delay = maxDelay
				}
			}
		}(f)
	}

	reason := ""
	tick := time.NewTicker(heartbeat)
	for reason == "" {
		select {
		case <-interrupted:
			reason = "interrupted"
		case <-tick.C:
			if _, err := os.Stat(stopPath); err == nil {
				reason = "asked to stop"
			} else {
				writeState()
			}
		}
	}
	tick.Stop()

	// stopping is set, under the registration lock, before any child is killed,
	// so a forward dying now is never restarted and one starting now kills its
	// own kubectl.
	killKnown := func() int {
		mu.Lock()
		defer mu.Unlock()
		stopping = true
		for _, p := range children {
			_ = p.Kill()
		}
		return len(children)
	}
	killKnown()
	close(done)
	// The request is consumed, so that a second one is a new file.
	_ = os.Remove(stopPath)

	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	poll := time.NewTicker(heartbeat)
	defer poll.Stop()
	again := ""
	for again == "" {
		select {
		case <-finished:
			_ = os.Remove(statePath)
			_ = os.Remove(stopPath)
			say("%s; every forward is closed", reason)
			return 0
		case <-interrupted:
			again = "interrupted again"
		case <-poll.C:
			if _, err := os.Stat(stopPath); err == nil {
				again = "asked to stop again"
			}
		}
	}
	// A second Ctrl-C or -stop while closing: kill what is known and leave
	// without waiting for the rest.
	n := killKnown()
	_ = os.Remove(statePath)
	_ = os.Remove(stopPath)
	say("%s while the forwards were closing; killed the %d it still knew of and exited without waiting", again, n)
	return 1
}

// stopRecorded asks the supervisor recorded in dir to stop, and waits for it.
// When nothing answers it closes the recorded processes itself, each only if
// its command line carries what devforward started it with, since a recorded
// PID may have been reused.
func stopRecorded(dir string) int {
	statePath := filepath.Join(dir, stateName)
	stopPath := filepath.Join(dir, stopName)
	rec, age, err := readRecord(statePath)
	if errors.Is(err, os.ErrNotExist) {
		say("no forwards are recorded in %s - nothing to stop", dir)
		return 0
	}
	if err != nil {
		say("cannot read %s: %v", statePath, err)
		return 1
	}

	stopped := false
	if age < stale {
		if err := os.WriteFile(stopPath, nil, 0o644); err != nil {
			say("cannot write %s: %v", stopPath, err)
			return 1
		}
		deadline := time.Now().Add(stopWait)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(statePath); errors.Is(err, os.ErrNotExist) {
				stopped = true
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if stopped {
			say("the supervisor (pid %d) stopped and closed its forwards", rec.PID)
		} else {
			say("the supervisor (pid %d) did not answer within %s; closing what it recorded", rec.PID, stopWait)
		}
	} else {
		say("the supervisor (pid %d) stopped answering %s ago; closing what it recorded",
			rec.PID, age.Round(time.Second))
	}

	if !stopped {
		// The supervisor first, so it starts nothing more; then the record is
		// read again, since a forward restarted during the wait is only there.
		closeIfOurs("the supervisor", rec.PID, supervisorMarks(rec))
		children := map[int]string{}
		for name, pid := range rec.Children {
			children[pid] = name
		}
		if again, _, err := readRecord(statePath); err == nil {
			for name, pid := range again.Children {
				children[pid] = name
			}
		}
		for pid, name := range children {
			closeIfOurs(name, pid, childMarks(rec, name))
		}
		_ = os.Remove(statePath)
		_ = os.Remove(stopPath)
	}

	// What incluster:down is judged by: nothing answers on a forwarded port.
	// Loopback only, which the forwards always bind.
	time.Sleep(300 * time.Millisecond)
	still := 0
	for _, f := range rec.Forwards {
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(f.Local))
		if c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
			c.Close()
			say("%s: something still answers on %s", f.Name, addr)
			still++
		}
	}
	if still > 0 {
		return 1
	}
	return 0
}

func readRecord(path string) (record, time.Duration, error) {
	info, err := os.Stat(path)
	if err != nil {
		return record{}, 0, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return record{}, 0, err
	}
	var rec record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return record{}, 0, fmt.Errorf("%s: %v", path, err)
	}
	return rec, time.Since(info.ModTime()), nil
}

// writeRecord replaces the state file whole, so -stop never reads half of one.
func writeRecord(path string, rec record) error {
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// supervisorMarks is what the supervisor's command line carries: the flags
// `incluster:forward` starts it with, and its context.
func supervisorMarks(rec record) []string {
	return []string{"devforward", "-state", "-forward", "-context", rec.Context}
}

// childMarks is what a forward's kubectl command line carries: the verb, the
// context, and the ports when the record still describes the forward.
func childMarks(rec record, name string) []string {
	marks := []string{"port-forward", "--context", rec.Context}
	for _, f := range rec.Forwards {
		if f.Name == name {
			marks = append(marks, fmt.Sprintf("%d:%d", f.Local, f.Remote))
		}
	}
	return marks
}

// missingMark is the first of marks the command line does not contain, or ""
// when it contains them all.
func missingMark(cmdline string, marks []string) string {
	for _, m := range marks {
		if !strings.Contains(cmdline, m) {
			return m
		}
	}
	return ""
}

// closeIfOurs kills pid only when it is running and its command line carries
// every mark. Anything it cannot read is left alone: a kubectl left behind is
// a port the check after it reports; a kill of somebody else's is not undone.
func closeIfOurs(what string, pid int, marks []string) {
	if pid <= 0 {
		return
	}
	cmdline, found, err := readCommandLine(pid)
	switch {
	case err != nil:
		say("%s (pid %d): cannot tell what it is (%v); left alone", what, pid, err)
	case !found:
		say("%s (pid %d): already gone", what, pid)
	case cmdline == "":
		// Exists but shows no command line (mid-exec, zombie, another user's):
		// "cannot tell", never a match.
		say("%s (pid %d): its command line cannot be read; left alone", what, pid)
	case missingMark(cmdline, marks) != "":
		say("%s (pid %d) is now %s, whose command line lacks %q - not ours; left alone",
			what, pid, executable(cmdline), missingMark(cmdline, marks))
	default:
		if proc, err := os.FindProcess(pid); err == nil && proc.Kill() == nil {
			say("%s (pid %d): closed", what, pid)
		} else {
			say("%s (pid %d): could not be closed", what, pid)
		}
	}
}

// executable is the first word of a command line, quoted or not: what is
// printed of a process that is not ours, since its arguments can carry a token.
func executable(cmdline string) string {
	cmdline = strings.TrimSpace(cmdline)
	if cmdline == "" {
		return "a process whose command line cannot be read"
	}
	if rest, ok := strings.CutPrefix(cmdline, `"`); ok {
		exe, _, _ := strings.Cut(rest, `"`)
		return exe
	}
	exe, _, _ := strings.Cut(cmdline, " ")
	return exe
}

// readCommandLine is commandLine, a variable so a test can stand in for a
// platform answer it cannot provoke.
var readCommandLine = commandLine

// commandLine is pid's command line, and whether such a process exists. A
// process that exists but whose command line cannot be read (another user's; on
// Linux a zombie or one still inside exec, which a PID read straight after
// cmd.Start may be) comes back found and empty.
func commandLine(pid int) (string, bool, error) {
	if runtime.GOOS == "windows" {
		// Win32_Process, because tasklist knows only the image name. "found"
		// first, so an absent process and an unreadable command line differ.
		script := fmt.Sprintf("$p = Get-CimInstance Win32_Process -Filter 'ProcessId = %d'; "+
			"if ($p) { 'found'; $p.CommandLine }", pid)
		raw, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
		if err != nil {
			return "", false, err
		}
		first, rest, _ := strings.Cut(strings.TrimLeft(string(raw), "\r\n"), "\n")
		if strings.TrimSpace(first) != "found" {
			return "", false, nil
		}
		return strings.TrimSpace(rest), true, nil
	}
	if runtime.GOOS == "linux" {
		// /proc rather than ps, which a slim image may not carry. A process
		// hidden from this user (hidepid) reads as gone, and is left alone.
		raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		return strings.TrimSpace(strings.ReplaceAll(string(raw), "\x00", " ")), true, nil
	}
	// -ww: the whole line, however wide; a truncated one could lose a mark.
	raw, err := exec.Command("ps", "-ww", "-p", strconv.Itoa(pid), "-o", "args=").Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return "", false, nil // ps exits 1 when the PID matches nothing
	}
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(string(raw)), true, nil
}

// The three kinds of line kubectl port-forward prints.
const (
	other     = iota
	listening // "Forwarding from 127.0.0.1:8824 -> 5823", once per bound address
	chatter   // "Handling connection for 8824": one per connection, dozens a minute
	//           on a console page, burying the lines that matter - dropped
)

func kindOf(line string) int {
	switch {
	case strings.HasPrefix(line, "Handling connection for"):
		return chatter
	case strings.HasPrefix(line, "Forwarding from"):
		return listening
	}
	return other
}

// scanLines hands every non-empty line read from r to emit, until r ends. A
// line too long for the scanner ends the scan, and the rest is drained rather
// than left in the pipe, where it would block kubectl's next write.
func scanLines(r io.Reader, emit func(string)) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			emit(line)
		}
	}
	_, _ = io.Copy(io.Discard, r)
}
