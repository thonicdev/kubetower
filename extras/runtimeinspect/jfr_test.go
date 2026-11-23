package runtimeinspect

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
)

// jfrHost answers the recording's commands the way a JDK 21 did (the
// fixtures are its real answers): JFR.check from a queue whose last answer
// repeats, JFR.start, JFR.stop, and the programs the read back needs.
type jfrHost struct {
	*fakeHost
	checks  [][]byte
	start   extension.ExecResult
	startEr error
	noRm    bool
	file    []byte
	// wc is wc -c's answer; wcAbsent has no wc in the image.
	wc       string
	wcAbsent bool
	ran      [][]string
	stream   [][]string
	readErr  error
	// onSleep runs in place of every wait.
	onSleep func()
}

func (h *jfrHost) RefreshApplication(*http.Request, string, string, string) error {
	return errors.New("RefreshApplication is not used here")
}

func (h *jfrHost) ConfirmToken(*http.Request) string { return "confirm" }

func (h *jfrHost) Exec(_ *http.Request, _ string, q extension.ExecQuery) (extension.ExecResult, error) {
	h.ran = append(h.ran, q.Command)
	cmd := strings.Join(q.Command, " ")
	switch {
	case q.Command[0] == "rm" && h.noRm:
		return extension.ExecResult{}, extension.ErrNoSuchCommand
	case q.Command[0] == "wc":
		if h.wcAbsent {
			return extension.ExecResult{}, extension.ErrNoSuchCommand
		}
		if h.wc != "" {
			return extension.ExecResult{Stdout: []byte(h.wc + " " + q.Command[2] + "\n")}, nil
		}
		return extension.ExecResult{Stdout: []byte(strconv.Itoa(len(h.file)) + " " + q.Command[2] + "\n")}, nil
	case q.Command[0] == "rm", q.Command[0] == "cat":
		return extension.ExecResult{}, nil
	case strings.Contains(cmd, " JFR.check"):
		out := h.checks[0]
		if len(h.checks) > 1 {
			h.checks = h.checks[1:]
		}
		return extension.ExecResult{Stdout: out}, nil
	case strings.Contains(cmd, " JFR.start "):
		return h.start, h.startEr
	case strings.Contains(cmd, " JFR.stop "):
		return extension.ExecResult{Stdout: []byte("7:\nStopped recording 1.\n")}, nil
	}
	h.t.Errorf("an argv the recording does not send: %q", q.Command)
	return extension.ExecResult{}, errors.New("unexpected")
}

func (h *jfrHost) ExecStream(_ *http.Request, _ string, q extension.ExecQuery, w io.Writer, limit int64) (extension.ExecStreamResult, error) {
	h.stream = append(h.stream, q.Command)
	if h.readErr != nil {
		return extension.ExecStreamResult{}, h.readErr
	}
	data := h.file
	if int64(len(data)) > limit {
		data = data[:limit]
	}
	n, _ := w.Write(data)
	return extension.ExecStreamResult{Written: int64(n)}, nil
}

const jfrPath = "/x/kind/runtime/pod/shop/billing-0/app/recording?pid=7&confirm=confirm&seconds=30"

func newJFRHost(t *testing.T) *jfrHost {
	t.Helper()
	jfrSleep = func(context.Context, time.Duration) {}
	t.Cleanup(func() { jfrSleep = realSleep })
	return &jfrHost{
		fakeHost: newHost(t, pod("shop", "billing-0", corev1.Container{Name: "app"})),
		// Before the start: none; after the time: none (the JVM wrote the file).
		checks: [][]byte{fixture(t, "jdk21-JFR-check-none")},
		start:  extension.ExecResult{Stdout: fixture(t, "jdk21-JFR-start")},
		file:   []byte("FLR\x00jfr-bytes"),
	}
}

var realSleep = jfrSleep

func (h *jfrHost) commands() []string {
	var out []string
	for _, argv := range h.ran {
		out = append(out, strings.TrimPrefix(strings.Join(argv, " "), envPrefix))
	}
	return out
}

func (h *jfrHost) started() (name, file string) {
	for _, c := range h.commands() {
		if strings.Contains(c, "JFR.start") {
			f := strings.Fields(c)
			return strings.TrimPrefix(f[3], "name="), strings.TrimPrefix(f[len(f)-1], "filename=")
		}
	}
	return "", ""
}

func TestAFlightRecordingIsStartedWaitedForStreamedAndRemoved(t *testing.T) {
	h := newJFRHost(t)
	rec := post(t, h, jfrPath, "")
	if rec.Code != http.StatusOK || rec.Body.String() != string(h.file) {
		t.Fatalf("answered %d %q", rec.Code, rec.Body.String())
	}
	name, file := h.started()
	if !jfrName.MatchString(name) || !jfrFile.MatchString(file) || name[len(jfrNamePrefix):] != file[len("/tmp/kubetower-jfr-"):len(file)-4] {
		t.Fatalf("name %q, file %q", name, file)
	}
	want := []string{
		"cat /dev/null",
		"rm -f " + file,
		"jcmd 7 JFR.check",
		"jcmd 7 JFR.start name=" + name + " settings=default duration=30s maxsize=200m filename=" + file,
		"jcmd 7 JFR.check",
		"wc -c " + file,
		"rm -f " + file,
	}
	if got := h.commands(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(h.stream) != 1 || strings.Join(h.stream[0], " ") != "cat "+file {
		t.Errorf("streamed %v", h.stream)
	}
	if rec.Header().Get("Content-Length") != strconv.Itoa(len(h.file)) {
		t.Errorf("Content-Length %q, want %d", rec.Header().Get("Content-Length"), len(h.file))
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, `attachment; filename="billing-0-app-`) || !strings.HasSuffix(cd, `.jfr"`) ||
		rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers %v", rec.Header())
	}
}

// Nothing runs without the confirmation word or with a duration off the list.
func TestARecordingThatIsNotConfirmedOrNotBoundedRunsNothing(t *testing.T) {
	base := "/x/kind/runtime/pod/shop/billing-0/app/recording?pid=7"
	for path, code := range map[string]string{
		base + "&seconds=30":                      CodeNotConfirmed,
		base + "&confirm=yes&seconds=30":          CodeNotConfirmed,
		base + "&confirm=confirm":                 CodeBadSeconds,
		base + "&confirm=confirm&seconds=0":       CodeBadSeconds,
		base + "&confirm=confirm&seconds=31":      CodeBadSeconds,
		base + "&confirm=confirm&seconds=3600":    CodeBadSeconds,
		base + "&confirm=confirm&seconds=-30":     CodeBadSeconds,
		base + "&confirm=confirm&seconds=30%3Brm": CodeBadSeconds,
		base + "&confirm=confirm&seconds=0x1e":    CodeBadSeconds,
		"/x/kind/runtime/pod/shop/billing-0/app/recording?pid=abc&confirm=confirm&seconds=30": CodeBadRequest,
		base + "&confirm=confirm&seconds=%2030%20abc":                                     CodeBadSeconds,
	} {
		h := newJFRHost(t)
		rec := post(t, h, path, "")
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), code) || len(h.ran)+len(h.stream) != 0 {
			t.Errorf("%s answered %d %s and ran %v", path, rec.Code, rec.Body.String(), h.commands())
		}
	}
	for _, d := range []string{"30", "60", "120", "300"} {
		h := newJFRHost(t)
		if rec := post(t, h, base+"&confirm=confirm&seconds="+d, ""); rec.Code != http.StatusOK {
			t.Errorf("%s seconds answered %d", d, rec.Code)
		}
	}
}

// One recording at a time: this extra's, still running, refuses a second; a
// recording somebody else started does not.
func TestOnlyOneRecordingRunsAtATime(t *testing.T) {
	h := newJFRHost(t)
	h.checks = [][]byte{fixture(t, "jdk21-JFR-check-running")}
	rec := post(t, h, jfrPath, "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), CodeRecording) {
		t.Fatalf("answered %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range h.commands() {
		if strings.Contains(c, "JFR.start") || strings.Contains(c, "JFR.stop") {
			t.Errorf("sent %q beside a running recording", c)
		}
	}
	h = newJFRHost(t)
	h.checks = [][]byte{[]byte("7:\nRecording 1: name=their-profile maxage=1h (running)\n\n"), fixture(t, "jdk21-JFR-check-none")}
	if rec := post(t, h, jfrPath, ""); rec.Code != http.StatusOK {
		t.Errorf("another's recording refused ours: %d %s", rec.Code, rec.Body.String())
	}
}

func TestARecordingIsNotStartedWhereItCouldNotBeRemovedOrWithoutAJFR(t *testing.T) {
	h := newJFRHost(t)
	h.noRm = true
	if rec := post(t, h, jfrPath, ""); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), CodeNoReadBack) {
		t.Fatalf("answered %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range h.commands() {
		if strings.Contains(c, "JFR") {
			t.Errorf("sent %q with no rm in the image", c)
		}
	}
	h = newJFRHost(t)
	h.checks = [][]byte{[]byte("7:\nUnknown diagnostic command 'JFR.check'\n")}
	if rec := post(t, h, jfrPath, ""); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), CodeNoJFR) {
		t.Errorf("a JVM with no recorder answered %d %s", rec.Code, rec.Body.String())
	}
}

// Each failure is named, nothing is streamed, and the file is removed.
func TestARecordingThatFailsIsNamedAndRemoved(t *testing.T) {
	for name, tc := range map[string]struct {
		start extension.ExecResult
		err   error
		code  string
	}{
		"refused":  {extension.ExecResult{}, extension.ErrDenied, CodeExecForbidden},
		"timeout":  {extension.ExecResult{}, extension.ErrExecTimedOut, CodeExecTimedOut},
		"no room":  {extension.ExecResult{Stdout: []byte("7:\njava.io.IOException: No space left on device\n")}, nil, CodeNoSpace},
		"attach":   {extension.ExecResult{ExitCode: 1, Stdout: []byte("AttachNotSupportedException: Unable to open socket file")}, nil, CodeAttachRefused},
		"no start": {extension.ExecResult{Stdout: []byte("7:\nCould not start recording, not enough memory.\n")}, nil, CodeJFRFailed},
	} {
		t.Run(name, func(t *testing.T) {
			h := newJFRHost(t)
			h.start, h.startEr = tc.start, tc.err
			rec := post(t, h, jfrPath, "")
			if !strings.Contains(rec.Body.String(), tc.code) || rec.Header().Get("Content-Disposition") != "" || len(h.stream) != 0 {
				t.Errorf("answered %d %s", rec.Code, rec.Body.String())
			}
			if last := h.commands()[len(h.commands())-1]; !strings.HasPrefix(last, "rm -f /tmp/kubetower-jfr-") {
				t.Errorf("not removed: the last command was %q", last)
			}
		})
	}
	h := newJFRHost(t)
	h.readErr = extension.ErrExecTimedOut
	if rec := post(t, h, jfrPath, ""); rec.Code != http.StatusGatewayTimeout || rec.Header().Get("Content-Disposition") != "" {
		t.Errorf("a failed read back answered %d with %v", rec.Code, rec.Header())
	}
}

// A recording still listed past its time is stopped, which writes the file.
func TestARecordingPastItsTimeIsStopped(t *testing.T) {
	old := jfrSettle
	jfrSettle = 6 * time.Second
	defer func() { jfrSettle = old }()
	h := newJFRHost(t)
	h.checks = [][]byte{fixture(t, "jdk21-JFR-check-none"), fixture(t, "jdk21-JFR-check-running")}
	rec := post(t, h, jfrPath, "")
	name, file := h.started()
	if rec.Code != http.StatusOK || rec.Body.String() != string(h.file) {
		t.Fatalf("answered %d %s", rec.Code, rec.Body.String())
	}
	cmds := h.commands()
	stop := "jcmd 7 JFR.stop name=" + name
	if cmds[len(cmds)-3] != stop || cmds[len(cmds)-1] != "rm -f "+file {
		t.Errorf("the end of the run: %q", cmds[len(cmds)-4:])
	}
}

// A page that leaves mid-recording still has its recording stopped and its
// file removed, and nothing is streamed.
func TestALeftRecordingIsStoppedAndRemoved(t *testing.T) {
	h := newJFRHost(t)
	ctx, cancel := context.WithCancel(context.Background())
	jfrSleep = func(context.Context, time.Duration) { cancel() }
	mux := http.NewServeMux()
	for _, r := range New().Routes(h) {
		mux.Handle(r.Method+" /x/{cluster}/"+Key+r.Pattern, r.Handler)
	}
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, jfrPath, nil).WithContext(ctx))
	name, file := h.started()
	cmds := h.commands()
	if len(cmds) < 2 || cmds[len(cmds)-2] != "jcmd 7 JFR.stop name="+name || cmds[len(cmds)-1] != "rm -f "+file || len(h.stream) != 0 {
		t.Errorf("ran %q, streamed %v", cmds, h.stream)
	}
}

// Every JFR argv is built here from a name, a file and a duration this route
// drew; nothing else gets through, and the diagnostics still refuse JFR.
func TestTheRecordingArgvTakesOnlyWhatThisRouteNames(t *testing.T) {
	for _, name := range []string{"JFR.start", "JFR.check", "JFR.stop", "JFR.dump"} {
		if _, err := jcmdArgv(false, 7, name); err == nil {
			t.Errorf("jcmdArgv sends %s", name)
		}
	}
	const okName, okFile = "kubetower-0123456789abcdef", "/tmp/kubetower-jfr-0123456789abcdef.jfr"
	for _, bad := range []string{"", "x", "kubetower-0123456789ABCDEF", "kubetower-0123456789abcdef settings=profile", "../x"} {
		if _, err := jfrArgv(false, 7, "stop", bad, "", 0); err == nil {
			t.Errorf("stop took the name %q", bad)
		}
		if _, err := jfrArgv(false, 7, "start", bad, okFile, 30); err == nil {
			t.Errorf("start took the name %q", bad)
		}
	}
	for _, bad := range []string{"", "/etc/passwd", "/tmp/kubetower-jfr-0123456789abcdef.jfr;x", "/tmp/kubetower-heap-0123456789abcdef.hprof"} {
		if _, err := jfrArgv(false, 7, "start", okName, bad, 30); err == nil {
			t.Errorf("start took the file %q", bad)
		}
	}
	for _, secs := range []int{0, 1, 29, 31, 301, 3600, -30} {
		if _, err := jfrArgv(false, 7, "start", okName, okFile, secs); err == nil {
			t.Errorf("start took %d seconds", secs)
		}
	}
	if _, err := jfrArgv(false, 0, "check", "", "", 0); err == nil {
		t.Error("pid 0 was accepted")
	}
	if _, err := jfrArgv(false, 7, "dump", okName, okFile, 30); err == nil {
		t.Error("an unknown verb was built")
	}
	// The read back and removal take the recording's file, and a heap dump's.
	for _, f := range []string{okFile, "/tmp/kubetower-heap-0123456789abcdef.hprof"} {
		if _, err := fileArgv("rm", f); err != nil {
			t.Errorf("fileArgv refused %q: %v", f, err)
		}
	}
}

func TestTheRecordingListIsRead(t *testing.T) {
	rs := recordings([]byte("7:\nRecording 1: name=a (stopped)\nRecording 2: name=kubetower-0123456789abcdef duration=30s maxsize=200.0MB (running)\n"))
	if len(rs) != 2 || rs[0] != (recording{"a", "stopped"}) {
		t.Fatalf("recordings = %+v", rs)
	}
	if n, ok := ours(rs); !ok || n != "kubetower-0123456789abcdef" {
		t.Errorf("ours = %q %v", n, ok)
	}
	if _, ok := ours(recordings(fixture(t, "jdk21-JFR-check-none"))); ok {
		t.Error("no recording is ours")
	}
}

// The size is read with wc before the stream: past the limit nothing is
// streamed and the file is removed; with no wc the file is streamed with no
// length.
func TestTheRecordingsSizeIsReadBeforeItIsStreamed(t *testing.T) {
	h := newJFRHost(t)
	h.wc = strconv.Itoa(jfrLimit + 1)
	rec := post(t, h, jfrPath, "")
	_, file := h.started()
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), CodeHeapTooLarge) || len(h.stream) != 0 ||
		rec.Header().Get("Content-Disposition") != "" {
		t.Fatalf("answered %d %s, streamed %v", rec.Code, rec.Body.String(), h.stream)
	}
	if cmds := h.commands(); cmds[len(cmds)-1] != "rm -f "+file {
		t.Errorf("not removed: %q", cmds)
	}

	h = newJFRHost(t)
	h.wcAbsent = true
	rec = post(t, h, jfrPath, "")
	if rec.Code != http.StatusOK || rec.Body.String() != string(h.file) || rec.Header().Get("Content-Length") != "" {
		t.Errorf("no wc: answered %d with length %q", rec.Code, rec.Header().Get("Content-Length"))
	}
	// An answer that is not a number is no size either.
	h = newJFRHost(t)
	h.wc = "many"
	if rec := post(t, h, jfrPath, ""); rec.Code != http.StatusOK || rec.Header().Get("Content-Length") != "" {
		t.Errorf("a bad wc answered %d with length %q", rec.Code, rec.Header().Get("Content-Length"))
	}
}
