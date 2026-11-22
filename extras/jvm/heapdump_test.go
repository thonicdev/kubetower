package jvm

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
)

// heapHost answers the heap dump's commands: the two programs asked for, the
// dump, the read back and the removal, whatever random file name was drawn.
type heapHost struct {
	*fakeHost
	dump     extension.ExecResult
	dumpErr  error
	file     []byte
	noRm     bool
	ran      [][]string
	streamed [][]string
	readErr  error
}

func (h *heapHost) RefreshApplication(*http.Request, string, string, string) error {
	return errors.New("RefreshApplication is not used here")
}

func (h *heapHost) ConfirmToken(*http.Request) string { return "confirm" }

func (h *heapHost) Exec(_ *http.Request, _ string, q extension.ExecQuery) (extension.ExecResult, error) {
	h.ran = append(h.ran, q.Command)
	switch {
	case q.Command[0] == "rm" && h.noRm:
		return extension.ExecResult{}, extension.ErrNoSuchCommand
	case q.Command[0] == "rm", q.Command[0] == "cat":
		return extension.ExecResult{}, nil
	case strings.Contains(strings.Join(q.Command, " "), "GC.heap_dump"):
		return h.dump, h.dumpErr
	}
	h.t.Errorf("an argv the heap dump does not send: %q", q.Command)
	return extension.ExecResult{}, errors.New("unexpected")
}

func (h *heapHost) ExecStream(_ *http.Request, _ string, q extension.ExecQuery, w io.Writer, limit int64) (extension.ExecStreamResult, error) {
	h.streamed = append(h.streamed, q.Command)
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

const heapPath = "/x/kind/jvm/pod/shop/billing-0/app/heapdump?pid=7&confirm=confirm"

func newHeapHost(t *testing.T) *heapHost {
	return &heapHost{fakeHost: newHost(t, pod("shop", "billing-0", corev1.Container{Name: "app"})),
		dump: extension.ExecResult{Stdout: []byte("7:\nDumping heap to /tmp/x.hprof ...\nHeap dump file created [11 bytes in 0.004 secs]\n")},
		file: []byte("JAVA PROFILE")[:11]}
}

// commands is what ran, as the record would name it.
func commands(h *heapHost) []string {
	var out []string
	for _, argv := range append(append([][]string{}, h.ran...), h.streamed...) {
		out = append(out, strings.Join(argv, " "))
	}
	return out
}

func TestAHeapDumpIsStreamedAndThenRemoved(t *testing.T) {
	h := newHeapHost(t)
	rec := post(t, h, heapPath, "")
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), h.file) {
		t.Fatalf("answered %d %q", rec.Code, rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, `attachment; filename="billing-0-app-`) ||
		rec.Header().Get("Content-Length") != "11" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers %v", rec.Header())
	}
	ran := commands(h)
	var file string
	for _, c := range ran {
		if strings.Contains(c, "GC.heap_dump") {
			file = c[strings.LastIndex(c, " ")+1:]
		}
	}
	if !heapDumpFile.MatchString(file) {
		t.Fatalf("the dump was written to %q", file)
	}
	if last := h.ran[len(h.ran)-1]; strings.Join(last, " ") != "rm -f "+file {
		t.Errorf("the last command was %q, want the removal of %s", last, file)
	}
	if len(h.streamed) != 1 || strings.Join(h.streamed[0], " ") != "cat "+file {
		t.Errorf("streamed %v", h.streamed)
	}
}

// Without the confirmation word nothing runs.
func TestAHeapDumpThatWasNotConfirmedRunsNothing(t *testing.T) {
	for _, path := range []string{
		"/x/kind/jvm/pod/shop/billing-0/app/heapdump?pid=7",
		"/x/kind/jvm/pod/shop/billing-0/app/heapdump?pid=7&confirm=yes",
	} {
		h := newHeapHost(t)
		rec := post(t, h, path, "")
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), CodeNotConfirmed) || len(h.ran)+len(h.streamed) != 0 {
			t.Errorf("%s answered %d and ran %v", path, rec.Code, commands(h))
		}
	}
}

// No rm in the image: the JVM is never paused for a file that would stay.
func TestAHeapDumpIsNotTakenWhereItCouldNotBeRemoved(t *testing.T) {
	h := newHeapHost(t)
	h.noRm = true
	rec := post(t, h, heapPath, "")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), CodeNoReadBack) {
		t.Fatalf("answered %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range commands(h) {
		if strings.Contains(c, "GC.heap_dump") {
			t.Errorf("the JVM was dumped with no rm in the image: %v", commands(h))
		}
	}
}

// Out of room, too large, refused: named, nothing streamed, the file removed.
func TestAHeapDumpThatFailsIsNamedAndRemoved(t *testing.T) {
	for name, tc := range map[string]struct {
		dump extension.ExecResult
		err  error
		code string
	}{
		"no room":   {extension.ExecResult{Stdout: []byte("7:\njava.io.IOException: No space left on device\n")}, nil, CodeNoSpace},
		"too large": {extension.ExecResult{Stdout: []byte("7:\nHeap dump file created [900000000 bytes in 3.1 secs]\n")}, nil, CodeHeapTooLarge},
		"refused":   {extension.ExecResult{}, extension.ErrDenied, CodeExecForbidden},
		"no dump":   {extension.ExecResult{Stdout: []byte("7:\nFile exists\n")}, nil, CodeHeapDumpFailed},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHeapHost(t)
			h.dump, h.dumpErr = tc.dump, tc.err
			rec := post(t, h, heapPath, "")
			if !strings.Contains(rec.Body.String(), tc.code) || rec.Header().Get("Content-Disposition") != "" {
				t.Errorf("answered %d %s", rec.Code, rec.Body.String())
			}
			if len(h.streamed) != 0 {
				t.Errorf("streamed %v", h.streamed)
			}
			if last := strings.Join(h.ran[len(h.ran)-1], " "); !strings.HasPrefix(last, "rm -f /tmp/kubetower-heap-") {
				t.Errorf("not removed: the last command was %q", last)
			}
		})
	}
}

// A read back that fails before a byte is a problem, not a broken file.
func TestAReadBackThatFailsBeforeAByteIsAProblem(t *testing.T) {
	h := newHeapHost(t)
	h.readErr = extension.ErrExecTimedOut
	rec := post(t, h, heapPath, "")
	if rec.Code != http.StatusGatewayTimeout || rec.Header().Get("Content-Disposition") != "" || rec.Header().Get("Content-Length") != "" {
		t.Errorf("answered %d with %v", rec.Code, rec.Header())
	}
}

// GC.heap_dump is spelt in one place only, which takes no file a request
// carries; the diagnostics still refuse it.
func TestTheHeapDumpIsOnlyThisRoutesToSend(t *testing.T) {
	if _, err := jcmdArgv(false, 7, "GC.heap_dump"); err == nil {
		t.Error("jcmdArgv sends GC.heap_dump")
	}
	for _, file := range []string{"/etc/passwd", "/tmp/kubetower-heap-0123456789abcdef.hprof; rm -rf /", "../x", "",
		"/tmp/kubetower-heap-0123456789ABCDEF.hprof"} {
		if _, err := heapDumpArgv(false, 7, file); err == nil {
			t.Errorf("heapDumpArgv took %q", file)
		}
		if _, err := fileArgv("rm", file); err == nil {
			t.Errorf("fileArgv took %q", file)
		}
	}
	if _, err := fileArgv("sh", "/tmp/kubetower-heap-0123456789abcdef.hprof"); err == nil {
		t.Error("fileArgv runs sh")
	}
	got, err := heapDumpPath()
	if err != nil || !heapDumpFile.MatchString(got) {
		t.Errorf("heapDumpPath = %q, %v", got, err)
	}
}
