package jvm

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/thonicdev/kubetower/extension"
)

// A Java Flight Recorder recording of one JVM, downloaded in the browser.
//
// The file is opened in JDK Mission Control; nothing is analyzed here. **A
// recording holds more than timings**: the JVM's system properties, its
// environment variables and its command line are events of the default
// profile, so the file can carry the same secrets a heap dump does, and the
// route is held to the same confirmation word (Host.ConfirmToken).
//
// What runs in the container, each through the console's exec door, reviewed
// for the person as `create pods/exec` and recorded with its argv:
//
//  1. `cat /dev/null` and `rm -f <file>` (readBackPossible), before anything
//     is started, so a file is never written where it could not be read back
//     or removed;
//  2. `jcmd <pid> JFR.check`: a recording this extra started that is still
//     running is the one recording a JVM may have at a time; JFR.check is also
//     where a JVM without Flight Recorder is found;
//  3. `jcmd <pid> JFR.start name=kubetower-<random> settings=default
//     duration=<N>s maxsize=200m filename=<file>`: N is one of
//     jfrDurations, the name and the file are random ones this route drew
//     (jfrName, jfrFile), and nothing else is variable;
//  4. a wait of N seconds, then JFR.check until the JVM has written the file
//     (the recording is gone); a recording still running past its time is
//     `JFR.stop name=<name>`, which writes it to the same file;
//  5. `cat <file>`, streamed (extension.ExecStreamer) at most jfrLimit bytes,
//     never held here;
//  6. `rm -f <file>`, and `JFR.stop` if the recording is still running,
//     whatever happened after JFR.start, on a context the page leaving does
//     not end.

// jfrDurations are the seconds a recording may last, and the only values that
// reach the argv: the request's number is looked up here, never formatted.
var jfrDurations = []int{30, 60, 120, 300}

const (
	// jfrMaxSize is JFR.start's maxsize: the recording keeps at most this much
	// on disk, so the file is under jfrLimit whatever the JVM does.
	jfrMaxSize = "200m"
	// jfrLimit is the most a download carries.
	jfrLimit       = 256 << 20
	jfrPollEvery   = 2 * time.Second
	jfrCmdTimeout  = 30 * time.Second
	CodeBadSeconds = "bad_seconds"
	CodeRecording  = "recording_busy"
	CodeNoJFR      = "jfr_unavailable"
	CodeJFRFailed  = "jfr_failed"
	jfrNamePattern = `^kubetower-[0-9a-f]{16}$`
	jfrFilePattern = `^/tmp/kubetower-jfr-[0-9a-f]{16}\.jfr$`
	jfrNamePrefix  = "kubetower-"
)

var (
	jfrName = regexp.MustCompile(jfrNamePattern)
	jfrFile = regexp.MustCompile(jfrFilePattern)
	// `Recording 1: name=kubetower-… duration=30s maxsize=200.0MB (running)`.
	recordingLine = regexp.MustCompile(`(?m)^Recording \d+: name=(\S+).*\((\w+)\)\s*$`)
	jfrStarted    = []byte("Started recording")
)

// jfrSettle is how long after its time a recording is given to be written,
// before it is stopped; a variable so a test can shorten it.
var jfrSettle = 30 * time.Second

// jfrSleep waits d, or until ctx ends; a variable so a test does not wait.
var jfrSleep = func(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// jfrSeconds is the duration a request asked for, from the allowlist.
func jfrSeconds(r *http.Request) (int, error) {
	v := strings.TrimSpace(r.URL.Query().Get("seconds"))
	n, err := strconv.Atoi(v)
	if err == nil {
		for _, d := range jfrDurations {
			if d == n {
				return d, nil
			}
		}
	}
	return 0, fmt.Errorf("%s is not a duration a recording may have: one of %v seconds", quote(v), jfrDurations)
}

func randomHex() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// jfrArgv is the argv of one JFR verb: `check`, `start` or `stop`. The name
// and the file are ones this route drew, and seconds one of jfrDurations;
// anything else is refused.
func jfrArgv(plain bool, pid int, verb, name, file string, seconds int) ([]string, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("a JVM's process id is a positive number, not %d", pid)
	}
	argv := append(prefix(plain), strconv.Itoa(pid))
	switch verb {
	case "check":
		return append(argv, "JFR.check"), nil
	case "stop":
		if !jfrName.MatchString(name) {
			return nil, fmt.Errorf("%q is not a recording this route names", name)
		}
		return append(argv, "JFR.stop", "name="+name), nil
	case "start":
		if !jfrName.MatchString(name) {
			return nil, fmt.Errorf("%q is not a recording this route names", name)
		}
		if !jfrFile.MatchString(file) {
			return nil, fmt.Errorf("%q is not a file this route names", file)
		}
		for _, d := range jfrDurations {
			if d == seconds {
				return append(argv, "JFR.start", "name="+name, "settings=default", "duration="+strconv.Itoa(d)+"s",
					"maxsize="+jfrMaxSize, "filename="+file), nil
			}
		}
		return nil, fmt.Errorf("%d seconds is not a duration a recording may have", seconds)
	}
	return nil, fmt.Errorf("%q is not a JFR command this route sends", verb)
}

// recording is one line of JFR.check.
type recording struct{ Name, State string }

func recordings(stdout []byte) []recording {
	var out []recording
	for _, m := range recordingLine.FindAllSubmatch(stdout, -1) {
		out = append(out, recording{string(m[1]), string(m[2])})
	}
	return out
}

// live is a state the recording has not finished in.
func live(state string) bool { return state != "stopped" && state != "closed" }

// ours is the running recording this extra started, if any.
func ours(rs []recording) (string, bool) {
	for _, r := range rs {
		if strings.HasPrefix(r.Name, jfrNamePrefix) && live(r.State) {
			return r.Name, true
		}
	}
	return "", false
}

// noJFR is the JVM saying it does not have the command or the recorder.
func noJFR(res extension.ExecResult) bool {
	return unknownCommand(res) || bytes.Contains(res.Stdout, []byte("Flight Recorder")) &&
		bytes.Contains(res.Stdout, []byte("not"))
}

func (x *runner) jfr(pid int, verb, name, file string, seconds int) (extension.ExecResult, error) {
	return x.run(func(plain bool) ([]string, error) { return jfrArgv(plain, pid, verb, name, file, seconds) }, jfrCmdTimeout)
}

func (JVM) flightRecording(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("confirm") != h.ConfirmToken(r) {
			writeProblem(w, http.StatusBadRequest, CodeNotConfirmed, "A recording is taken only once it is confirmed.", nil)
			return
		}
		streamer, ok := h.(extension.ExecStreamer)
		if !ok {
			writeProblem(w, http.StatusNotImplemented, CodeNoStream, "This console cannot stream a file out of a container.", nil)
			return
		}
		seconds, err := jfrSeconds(r)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, CodeBadSeconds, err.Error(), nil)
			return
		}
		chosen, err := pidParam(r)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, CodeBadRequest, err.Error(), nil)
			return
		}
		t, ok := resolve(w, r, h)
		if !ok {
			return
		}
		run := t.runner(h, r)
		pid, from, jvms, ok := pick(w, run, t.cluster, chosen)
		if !ok {
			return
		}
		if len(jvms) > 0 {
			writeJSON(w, http.StatusConflict, heapDumpChoice{State: CodeChooseJVM, JVMs: jvms})
			return
		}
		id, err := randomHex()
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, CodeJFRFailed, "No name could be drawn.", err)
			return
		}
		name, file := jfrNamePrefix+id, "/tmp/kubetower-jfr-"+id+".jfr"
		if !readBackPossible(w, run, t.cluster, file) {
			return
		}

		// One recording at a time: the JVM's own list says whether this extra's
		// is still running, and whether it has a recorder at all.
		res, err := run.jfr(pid, "check", "", "", 0)
		if !answered(w, run, t.cluster, pid, from, "JFR.check", res, err) {
			return
		}
		if noJFR(res) {
			writeProblem(w, http.StatusUnprocessableEntity, CodeNoJFR, "This JVM has no Flight Recorder.", errors.New(said(res)))
			return
		}
		if _, busy := ours(recordings(res.Stdout)); busy {
			writeProblem(w, http.StatusConflict, CodeRecording, "A recording of this JVM is already running.", nil)
			return
		}

		// From JFR.start on, a recording and a file may exist: both are ended
		// and removed whatever happens, even when the page has gone.
		started := false
		defer func() {
			if started {
				endRecording(h, r, t, pid, name)
			}
			removeDump(h, r, t, file)
		}()
		res, err = run.jfr(pid, "start", name, file, seconds)
		started = err == nil
		switch {
		case err != nil:
			writeExecFailure(w, t.cluster, err)
			return
		case attachDisabled(res):
			writeProblem(w, http.StatusUnprocessableEntity, CodeAttachDisabled, attachDisabledText, errors.New(said(res)))
			return
		case attachRefused(res):
			writeProblem(w, http.StatusUnprocessableEntity, CodeAttachRefused, attachRefusedText, errors.New(said(res)))
			return
		case noJFR(res):
			started = false
			writeProblem(w, http.StatusUnprocessableEntity, CodeNoJFR, "This JVM has no Flight Recorder.", errors.New(said(res)))
			return
		case outOfRoom(res):
			writeProblem(w, http.StatusInsufficientStorage, CodeNoSpace, "The container has no room left in /tmp for the recording.",
				errors.New(said(res)))
			return
		case res.ExitCode != 0 || !bytes.Contains(res.Stdout, jfrStarted):
			started = false
			writeProblem(w, http.StatusBadGateway, CodeJFRFailed, "The JVM did not start a recording.", errors.New(said(res)))
			return
		}

		// The recording's time, then the JVM writes the file and forgets the
		// recording; one still listed after jfrSettle is stopped, which writes
		// it to the same file.
		jfrSleep(r.Context(), time.Duration(seconds)*time.Second)
		for waited := time.Duration(0); ; waited += jfrPollEvery {
			if r.Context().Err() != nil {
				return
			}
			res, err = run.jfr(pid, "check", "", "", 0)
			if err != nil {
				writeExecFailure(w, t.cluster, err)
				return
			}
			if _, still := ours(recordings(res.Stdout)); !still {
				started = false
				break
			}
			if waited >= jfrSettle {
				break
			}
			jfrSleep(r.Context(), jfrPollEvery)
		}
		if started {
			endRecording(h, r, t, pid, name)
			started = false
		}
		if r.Context().Err() != nil {
			return
		}
		size, known, err := fileSize(run, file)
		switch {
		case err != nil:
			writeExecFailure(w, t.cluster, err)
			return
		case known && size > jfrLimit:
			writeProblem(w, http.StatusRequestEntityTooLarge, CodeHeapTooLarge,
				"The recording is "+strconv.FormatInt(size, 10)+" bytes, past the "+strconv.Itoa(jfrLimit)+" a download carries.", nil)
			return
		case !known:
			size = 0
		}
		streamFile(w, r, streamer, t, file, ".jfr", size, jfrLimit)
	})
}

// endRecording stops this extra's recording, on a context the page leaving
// does not end; stopping writes it to its file, which the caller removes.
func endRecording(h extension.Host, r *http.Request, t target, pid int, name string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), jfrCmdTimeout+5*time.Second)
	defer cancel()
	// Left to its own log line on failure: the recording ends by its duration
	// anyway, and the file is removed by the caller.
	_, _ = t.runner(h, r.WithContext(ctx)).jfr(pid, "stop", name, "", 0)
}

// fileSize is the size of file from `wc -c`, so the download can carry a
// Content-Length and a cut one fail in the browser. known is false when the
// size could not be read (no wc in the image, or an answer that is not a
// number): the file is then streamed without a length.
func fileSize(run *runner, file string) (size int64, known bool, err error) {
	argv, err := fileArgv("wc", file)
	if err != nil {
		return 0, false, err
	}
	res, err := run.exec(argv, jfrCmdTimeout)
	switch {
	case errors.Is(err, extension.ErrNoSuchCommand):
		return 0, false, nil
	case err != nil:
		return 0, false, err
	case res.ExitCode != 0:
		return 0, false, nil
	}
	f := strings.Fields(string(res.Stdout))
	if len(f) == 0 {
		return 0, false, nil
	}
	n, perr := strconv.ParseInt(f[0], 10, 64)
	if perr != nil || n < 0 {
		return 0, false, nil
	}
	return n, true, nil
}
