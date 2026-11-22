package jvm

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/textutil"
)

// Reading `jcmd <pid> Thread.print -l`: a thread dump, parsed here so the page
// draws threads, states and deadlocks and never parses text itself.
//
// The format is HotSpot's, the same from JDK 8 to 21 in what is read here: a
// header line per thread (`"name" #id [nid] daemon prio=5 os_prio=0 cpu=…
// tid=0x… nid=… <status> [0x…]`), the `java.lang.Thread.State:` line, the
// frames (`\tat …`) with the monitors they hold or wait for (`\t- locked <0x…>
// (a …)`), and with -l each thread's locked ownable synchronizers. The JVM's
// own threads (GC, compiler, the VM thread) have the header alone. After the
// threads, each deadlock the JVM found is a section `Found one Java-level
// deadlock:` naming, for each thread of the cycle, the lock it waits for and
// the thread holding it.
//
// Thread names are the application's: they may carry anything it put there,
// so a dump is never logged, and a name is clipped, never interpreted.

// The ceilings of one answer. The output itself is bounded by Host.Exec
// (extension.ExecOutputLimit); these bound what the page is handed, and the
// answer says when either was reached.
const (
	// MaxThreads is the most threads one answer lists; the counts cover them
	// all.
	MaxThreads = 5000
	// MaxFrames is the most frames listed per thread, from the top of its
	// stack.
	MaxFrames = 64
	// maxDeadlocks is the most deadlocks listed; the JVM reports each cycle
	// once.
	maxDeadlocks = 64
	// maxCycle is the most threads listed in one deadlock's cycle.
	maxCycle = 64
	// maxThreadName and maxFrameText clip one name and one line.
	maxThreadName = 300
	maxFrameText  = 600
)

// The states a thread is counted under: java.lang.Thread.State's six, and
// `VM` for the JVM's own threads, which have no Java state.
const (
	threadRunnable     = "RUNNABLE"
	threadBlocked      = "BLOCKED"
	threadWaiting      = "WAITING"
	threadTimedWaiting = "TIMED_WAITING"
	threadNew          = "NEW"
	threadTerminated   = "TERMINATED"
	threadVM           = "VM"
)

// threadStates is the order the counts are given in.
var threadStates = []string{threadRunnable, threadBlocked, threadWaiting, threadTimedWaiting, threadNew,
	threadTerminated, threadVM}

// lockRef is one monitor or synchronizer a frame names: `- locked
// <0x00000000e1f13740> (a java.lang.Object)` is {Kind: "locked", ID:
// "0x00000000e1f13740", Class: "java.lang.Object"}.
type lockRef struct {
	// Kind is the words before the reference, as the JVM wrote them:
	// `locked`, `waiting to lock`, `waiting on`, `parking to wait for`,
	// `waiting to re-lock in wait()`, `eliminated`.
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Class string `json:"class,omitempty"`
	// HeldBy is the thread whose stack locked ID, for a lock a thread waits
	// for; empty when no listed thread holds it.
	HeldBy string `json:"heldBy,omitempty"`
}

// frame is one line of a stack, with the locks the JVM printed under it.
type frame struct {
	At    string    `json:"at"`
	Locks []lockRef `json:"locks,omitempty"`
}

// thread is one thread of the dump.
type thread struct {
	Name string `json:"name"`
	// ID is the Java thread id (`#12`), 0 for the JVM's own threads.
	ID     int64 `json:"id,omitempty"`
	Daemon bool  `json:"daemon"`
	// Prio is the Java priority, 0 for the JVM's own threads.
	Prio int `json:"prio,omitempty"`
	// State is one of threadStates; Detail what follows it on the line
	// (`sleeping`, `on object monitor`, `parking`).
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
	// Status is the header's own word for it: `waiting on condition`,
	// `waiting for monitor entry`, `runnable`.
	Status string `json:"status,omitempty"`
	// CPU and Elapsed are the header's, JDK 11 and later.
	CPU     string  `json:"cpu,omitempty"`
	Elapsed string  `json:"elapsed,omitempty"`
	Frames  []frame `json:"frames"`
	// FramesCut is how many frames below the first MaxFrames were left out.
	FramesCut int `json:"framesCut,omitempty"`
	// WaitingFor is the lock it is blocked or waiting on, with who holds it
	// where a listed thread does; nil when it waits for nothing.
	WaitingFor *lockRef `json:"waitingFor,omitempty"`
	// Holds is every monitor and ownable synchronizer it holds.
	Holds []lockRef `json:"holds,omitempty"`
}

// stateCount is how many threads are in one state.
type stateCount struct {
	State string `json:"state"`
	Count int    `json:"count"`
}

// deadlockLink is one thread of a deadlock's cycle: the lock it waits for,
// and the thread holding it.
type deadlockLink struct {
	Thread string `json:"thread"`
	// Waits is the JVM's words for the lock: `monitor 0x… (object 0x…, a
	// java.lang.Object)`, `ownable synchronizer 0x…, (a …ReentrantLock$NonfairSync)`.
	Waits  string `json:"waits"`
	Lock   string `json:"lock,omitempty"`
	Class  string `json:"class,omitempty"`
	HeldBy string `json:"heldBy"`
}

// deadlock is one cycle the JVM reported.
type deadlock struct {
	// Kind is the JVM's word: `Java-level`.
	Kind  string         `json:"kind"`
	Cycle []deadlockLink `json:"cycle"`
	// CycleCut is set when the cycle had more than maxCycle threads.
	CycleCut bool `json:"cycleCut,omitempty"`
}

// ThreadDump is a parsed thread dump.
type ThreadDump struct {
	// Taken is the timestamp the JVM printed, VM the `Full thread dump` line's
	// VM.
	Taken string `json:"taken,omitempty"`
	VM    string `json:"vm,omitempty"`
	// Total is how many threads the dump holds; Threads the first MaxThreads.
	Total   int          `json:"total"`
	Daemon  int          `json:"daemon"`
	Counts  []stateCount `json:"counts"`
	Threads []thread     `json:"threads"`
	// ThreadsCut is set when Total is above MaxThreads; FramesCut is how
	// many listed threads had frames left out.
	ThreadsCut bool       `json:"threadsCut,omitempty"`
	FramesCut  int        `json:"framesCut,omitempty"`
	Deadlocks  []deadlock `json:"deadlocks"`
	// DeadlocksCut is set when more than maxDeadlocks were reported.
	DeadlocksCut bool `json:"deadlocksCut,omitempty"`
}

var (
	// `- locked <0x00000000e1f13740> (a java.lang.Object)`; the reference may
	// be `<no object reference available>` or `<owner is scalar replaced>`.
	lockLine = regexp.MustCompile(`^-\s+(.*?)\s*<([^>]*)>(?:\s*\(a (.+)\))?\s*$`)
	// An ownable synchronizer under `Locked ownable synchronizers:`.
	syncLine = regexp.MustCompile(`^-\s+<([^>]*)>\s*(?:\(a (.+)\))?\s*$`)
	// `#12`, `prio=5`, `cpu=1.59ms`, `elapsed=56.64s`.
	idField      = regexp.MustCompile(`(?:^|\s)#(\d+)(?:\s|$)`)
	prioField    = regexp.MustCompile(`(?:^|\s)prio=(\d+)`)
	cpuField     = regexp.MustCompile(`(?:^|\s)cpu=(\S+)`)
	elapsedField = regexp.MustCompile(`(?:^|\s)elapsed=(\S+)`)
	// The status follows `nid=…`, and ends before the stack's address.
	statusField = regexp.MustCompile(`nid=\S+\s+(.*?)\s*(?:\[0x[0-9a-fA-F]+\])?\s*$`)
	// `waiting to lock monitor 0x… (object 0x…, a java.lang.Object),`
	objectField = regexp.MustCompile(`object (0x[0-9a-fA-F]+)`)
	addrField   = regexp.MustCompile(`(0x[0-9a-fA-F]+)`)
	classField  = regexp.MustCompile(`\(?(?:object 0x[0-9a-fA-F]+, )?a ([^)]+)\)`)
	timestamp   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$`)
)

// header splits a thread's first line into its name and the rest. The name
// is between the first quote and the last quote before ` tid=` (or, in a
// format without it, the line's last quote): a name may hold quotes, the
// fields after it never do.
func header(line string) (name, rest string, ok bool) {
	if !strings.HasPrefix(line, `"`) {
		return "", "", false
	}
	end := len(line)
	if i := strings.LastIndex(line, " tid="); i > 0 {
		end = i
	}
	q := strings.LastIndexByte(line[:end], '"')
	if q <= 0 {
		return "", "", false
	}
	return line[1:q], line[q+1:], true
}

// parseThreads reads a thread dump's body (the lines after jcmd's `<pid>:`).
func parseThreads(body []string) ThreadDump {
	d := ThreadDump{Threads: []thread{}, Deadlocks: []deadlock{}}
	counts := map[string]int{}
	var (
		cur      *thread // the thread being read, nil between threads
		inSyncs  bool    // under `Locked ownable synchronizers:`
		inDead   bool    // in a deadlock report
		inStacks bool    // in a deadlock report's `Java stack information`
		link     *deadlockLink
		dl       *deadlock
		kept     bool // cur is one of d.Threads, not past MaxThreads
	)
	// keepThread files the thread just read.
	keepThread := func() {
		if cur == nil {
			return
		}
		if cur.State == "" {
			cur.State = threadVM
		}
		counts[cur.State]++
		if cur.Daemon {
			d.Daemon++
		}
		d.Total++
		if kept {
			if cur.FramesCut > 0 {
				d.FramesCut++
			}
			d.Threads[len(d.Threads)-1] = *cur
		}
		cur, inSyncs = nil, false
	}
	endLink := func() {
		if link != nil && dl != nil {
			if len(dl.Cycle) < maxCycle {
				dl.Cycle = append(dl.Cycle, *link)
			} else {
				dl.CycleCut = true
			}
		}
		link = nil
	}
	endDeadlock := func() {
		endLink()
		if dl != nil {
			if len(d.Deadlocks) < maxDeadlocks {
				d.Deadlocks = append(d.Deadlocks, *dl)
			} else {
				d.DeadlocksCut = true
			}
		}
		dl, inStacks = nil, false
	}

	for _, raw := range body {
		line := strings.TrimSpace(raw)
		// A deadlock report: `Found one Java-level deadlock:`. The final
		// `Found 1 deadlock.` / `Found a total of 2 deadlocks.` is not one.
		if strings.HasPrefix(line, "Found one ") && strings.HasSuffix(line, " deadlock:") {
			keepThread()
			endDeadlock()
			inDead = true
			dl = &deadlock{Kind: strings.TrimSuffix(strings.TrimPrefix(line, "Found one "), " deadlock:"), Cycle: []deadlockLink{}}
			continue
		}
		if inDead {
			switch {
			case strings.HasPrefix(line, "Java stack information"):
				endLink()
				inStacks = true
			case inStacks, line == "", strings.Trim(line, "=") == "":
			case strings.HasPrefix(line, `"`) && strings.HasSuffix(line, `":`):
				endLink()
				link = &deadlockLink{Thread: textutil.Clip(line[1:len(line)-2], maxThreadName)}
			case link != nil && strings.HasPrefix(line, "which is held by "):
				held := strings.TrimPrefix(line, "which is held by ")
				link.HeldBy = textutil.Clip(strings.Trim(held, `"`), maxThreadName)
			case link != nil && strings.HasPrefix(line, "waiting "):
				link.Waits = textutil.Clip(strings.TrimSuffix(strings.TrimPrefix(line, "waiting "), ","), maxFrameText)
				if m := objectField.FindStringSubmatch(line); m != nil {
					link.Lock = m[1]
				} else if m := addrField.FindStringSubmatch(line); m != nil {
					link.Lock = m[1]
				}
				if m := classField.FindStringSubmatch(line); m != nil {
					link.Class = strings.TrimSpace(m[1])
				}
			case link != nil && link.HeldBy == "" && link.Waits != "":
				// A wait sentence the JVM wrapped onto a second line.
				link.Waits = textutil.Clip(link.Waits+" "+strings.TrimSuffix(line, ","), maxFrameText)
			}
			continue
		}
		if name, rest, ok := header(line); ok && strings.HasPrefix(raw, `"`) {
			keepThread()
			cur = &thread{Name: textutil.Clip(name, maxThreadName), Frames: []frame{}}
			fields := " " + rest + " "
			cur.Daemon = strings.Contains(fields, " daemon ")
			if m := idField.FindStringSubmatch(rest); m != nil {
				cur.ID, _ = strconv.ParseInt(m[1], 10, 64)
			}
			if m := prioField.FindStringSubmatch(rest); m != nil {
				cur.Prio, _ = strconv.Atoi(m[1])
			}
			if m := cpuField.FindStringSubmatch(rest); m != nil {
				cur.CPU = m[1]
			}
			if m := elapsedField.FindStringSubmatch(rest); m != nil {
				cur.Elapsed = m[1]
			}
			if m := statusField.FindStringSubmatch(rest); m != nil {
				cur.Status = textutil.Clip(m[1], 120)
			}
			kept = len(d.Threads) < MaxThreads
			if kept {
				d.Threads = append(d.Threads, thread{})
			} else {
				d.ThreadsCut = true
			}
			continue
		}
		if cur == nil {
			switch {
			case strings.HasPrefix(line, "Full thread dump "):
				d.VM = textutil.Clip(strings.TrimSuffix(strings.TrimPrefix(line, "Full thread dump "), ":"), 200)
			case d.Taken == "" && timestamp.MatchString(line):
				d.Taken = line
			}
			continue
		}
		switch {
		case line == "":
			// The blank line between a thread's stack and its synchronizers;
			// the thread ends at the next header.
		case strings.HasPrefix(line, "java.lang.Thread.State: "):
			state := strings.TrimPrefix(line, "java.lang.Thread.State: ")
			word, detail, _ := strings.Cut(state, " ")
			cur.State = word
			if !knownState(word) {
				cur.State = threadVM
			}
			cur.Detail = strings.Trim(strings.TrimSpace(detail), "()")
		case strings.HasPrefix(line, "Locked ownable synchronizers:"):
			inSyncs = true
		case strings.HasPrefix(line, "at "):
			inSyncs = false
			if len(cur.Frames) < MaxFrames {
				cur.Frames = append(cur.Frames, frame{At: textutil.Clip(strings.TrimPrefix(line, "at "), maxFrameText)})
			} else {
				cur.FramesCut++
			}
		case inSyncs && strings.HasPrefix(line, "-"):
			if m := syncLine.FindStringSubmatch(line); m != nil {
				cur.Holds = append(cur.Holds, lockRef{Kind: "owns", ID: m[1], Class: textutil.Clip(m[2], 200)})
			}
		case strings.HasPrefix(line, "-"):
			m := lockLine.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			l := lockRef{Kind: m[1], ID: m[2], Class: textutil.Clip(m[3], 200)}
			switch l.Kind {
			case "locked":
				cur.Holds = append(cur.Holds, l)
			case "waiting to lock", "waiting on", "parking to wait for", "waiting to re-lock in wait()":
				// The first such line is what the thread waits for: the
				// top of its stack.
				if cur.WaitingFor == nil && strings.HasPrefix(l.ID, "0x") {
					w := l
					cur.WaitingFor = &w
				}
			}
			if len(cur.Frames) > 0 && cur.FramesCut == 0 {
				f := &cur.Frames[len(cur.Frames)-1]
				f.Locks = append(f.Locks, l)
			}
		}
	}
	keepThread()
	endDeadlock()

	for _, s := range threadStates {
		d.Counts = append(d.Counts, stateCount{State: s, Count: counts[s]})
	}
	holders(&d)
	return d
}

func knownState(s string) bool {
	for _, k := range threadStates {
		if s == k && s != threadVM {
			return true
		}
	}
	return false
}

// holders names, for each lock a listed thread waits for, the listed thread
// that holds it. A thread `waiting on` a monitor in Object.wait() released it
// - its own stack also says `locked` for it - so it is not its own holder.
func holders(d *ThreadDump) {
	held := map[string]string{}
	for _, t := range d.Threads {
		for _, l := range t.Holds {
			if _, dup := held[l.ID]; !dup {
				held[l.ID] = t.Name
			}
		}
	}
	for i := range d.Threads {
		t := &d.Threads[i]
		if t.WaitingFor == nil {
			continue
		}
		if h, ok := held[t.WaitingFor.ID]; ok && h != t.Name {
			t.WaitingFor.HeldBy = h
		}
	}
}

// dumpNotes are the sentences an answer carries for what was cut.
func dumpNotes(d ThreadDump, outputCut bool, limit string) []string {
	notes := []string{}
	if outputCut {
		notes = append(notes, "jcmd's output reached the exec's limit of "+limit+" and was cut: the threads after it are "+
			"missing, and the counts cover only those read.")
	}
	if d.ThreadsCut {
		notes = append(notes, fmt.Sprintf("The JVM has %d threads; the first %d are listed, and the counts cover them all.",
			d.Total, MaxThreads))
	}
	if d.FramesCut > 0 {
		notes = append(notes, fmt.Sprintf("%d threads have stacks deeper than %d frames: their first %d frames are listed; "+
			"the download holds them whole.", d.FramesCut, MaxFrames, MaxFrames))
	}
	if d.DeadlocksCut {
		notes = append(notes, fmt.Sprintf("The JVM reported more than %d deadlocks; the first %d are listed.", maxDeadlocks, maxDeadlocks))
	}
	return notes
}

// threadsAnswer is `POST /pod/{namespace}/{name}/{container}/threads`.
type threadsAnswer struct {
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	Container string `json:"container"`
	// State is `dumped`, or `choose` when jcmd lists several JVMs and no
	// `?pid=` chose one: JVMs is then the list, and nothing else was run.
	State   string      `json:"state"`
	JVMs    []listedJVM `json:"jvms,omitempty"`
	PID     int         `json:"pid,omitempty"`
	PIDFrom string      `json:"pidFrom,omitempty"`
	// Inherited is set when the image has no env (see inspectAnswer).
	Inherited bool `json:"inherited,omitempty"`
	*ThreadDump
	// OutputCut is set when jcmd's output reached Host.Exec's limit: the
	// threads after it are missing.
	OutputCut bool `json:"outputCut,omitempty"`
	// Raw is what Thread.print printed, after jcmd's `<pid>:` line, for the
	// download. Never logged.
	Raw   string   `json:"raw,omitempty"`
	Notes []string `json:"notes"`
}

func (JVM) threads(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chosen, err := pidParam(r)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, CodeBadRequest, err.Error(), nil)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), threadsBudget)
		defer cancel()
		r = r.WithContext(ctx)
		t, ok := resolve(w, r, h)
		if !ok {
			return
		}
		run := t.runner(h, r)
		out := threadsAnswer{Cluster: t.cluster, Namespace: t.namespace, Pod: t.name, Container: t.container,
			State: "dumped", Notes: []string{}}
		pid, from, jvms, ok := pick(w, run, t.cluster, chosen)
		if !ok {
			return
		}
		if len(jvms) > 0 {
			out.State, out.JVMs, out.Inherited = "choose", jvms, run.plain
			writeJSON(w, http.StatusOK, out)
			return
		}
		if ctx.Err() != nil {
			// The page went away while jcmd listed: the dump is not taken.
			return
		}
		out.PID, out.PIDFrom = pid, from
		res, err := run.dcmdWithin(pid, dcmdThreads, threadsTimeout)
		if !answered(w, run, t.cluster, pid, from, dcmdThreads, res, err) {
			return
		}
		if r.Context().Err() != nil {
			return
		}
		lines := body(res.Stdout)
		d := parseThreads(lines)
		out.ThreadDump = &d
		out.OutputCut = res.Truncated
		out.Raw = strings.Join(lines, "\n")
		out.Inherited = run.plain
		out.Notes = dumpNotes(d, res.Truncated, textutil.Bytes(extension.ExecOutputLimit))
		if run.plain {
			out.Notes = append(out.Notes, inheritedText)
		}
		writeJSON(w, http.StatusOK, out)
	})
}
