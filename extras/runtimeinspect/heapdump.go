package runtimeinspect

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/thonicdev/kubetower/extension"
)

// A heap dump of one JVM, downloaded in the browser.
//
// **The dump holds everything the application holds in memory**: passwords,
// tokens, keys, personal data. So it is never sent from the inspection: it has
// its own button, a confirmation that says so in words, and this route, which
// refuses a request that does not carry the confirmation word
// (Host.ConfirmToken). `GC.heap_dump` stays out of the diagnostics jcmdArgv
// builds from (jcmd.go): heapDumpArgv is the one place it is spelt, and only
// this route calls it.
//
// What runs in the container, each through the console's exec door, reviewed
// for the person as `create pods/exec` and recorded with its argv - the
// record names who dumped which pod's JVM into which file, never the content:
//
//  1. `cat /dev/null` and `rm -f <file>`: the two programs the download and
//     the removal need, asked for before the JVM is paused, so a dump is never
//     written where it could not be read back or removed;
//  2. `jcmd <pid> GC.heap_dump <file>`: the live objects, after a full
//     collection that pauses the JVM, into a file of the container's /tmp with
//     a random name;
//  3. `cat <file>`, streamed to the browser (extension.ExecStreamer) at most
//     heapDumpLimit bytes, never held here;
//  4. `rm -f <file>`, whatever happened after the dump was asked for.

// heapDumpLimit is the largest dump downloaded. A larger one is refused before
// a byte is sent, and removed.
const heapDumpLimit = 512 << 20

// The deadlines: the dump pauses the JVM for as long as it writes, and the
// download is as long as the file.
const (
	heapDumpTimeout = extension.MaxExecTimeout
	heapReadTimeout = extension.MaxExecStreamTimeout
	heapRmTimeout   = 15 * time.Second
)

// The problems only this route answers, and not_confirmed, which the class
// histogram answers too: both ask for a confirmation first.
const (
	CodeNotConfirmed    = "not_confirmed"
	CodeNoStream        = "no_stream"
	CodeNoReadBack      = "no_read_back"
	CodeNoSpace         = "no_space"
	CodeHeapTooLarge    = "heap_too_large"
	CodeHeapDumpFailed  = "heap_dump_failed"
	CodeChooseJVM       = "choose_jvm"
	heapDumpFilePattern = `^/tmp/kubetower-heap-[0-9a-f]{16}\.hprof$`
)

var heapDumpFile = regexp.MustCompile(heapDumpFilePattern)

// heapDumpPath is a fresh file name in the container's /tmp.
func heapDumpPath() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "/tmp/kubetower-heap-" + hex.EncodeToString(b[:]) + ".hprof", nil
}

// heapDumpArgv is the one argv that asks a JVM for a heap dump. The file is
// one this route named (heapDumpFile), never one a request carried.
func heapDumpArgv(plain bool, pid int, file string) ([]string, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("a JVM's process id is a positive number, not %d", pid)
	}
	if !heapDumpFile.MatchString(file) {
		return nil, fmt.Errorf("%q is not a file this route names", file)
	}
	return append(prefix(plain), strconv.Itoa(pid), "GC.heap_dump", file), nil
}

// ownedFile is a file one of this extra's routes named: a heap dump's or a
// flight recording's, with its random name, never one a request carried.
func ownedFile(file string) bool {
	return heapDumpFile.MatchString(file) || jfrFile.MatchString(file)
}

// fileArgv is cat or rm on a file this extra named.
func fileArgv(program, file string) ([]string, error) {
	if !ownedFile(file) {
		return nil, fmt.Errorf("%q is not a file this route names", file)
	}
	switch program {
	case "wc":
		return []string{"wc", "-c", file}, nil
	case "cat":
		return []string{"cat", file}, nil
	case "rm":
		return []string{"rm", "-f", file}, nil
	}
	return nil, fmt.Errorf("%q is not a program this route runs", program)
}

// heapDumpCreated reads jcmd's answer: `Heap dump file created [N bytes in
// S secs]`. jcmd exits 0 when the JVM refused, so the sentence is the answer.
var heapDumpCreated = regexp.MustCompile(`Heap dump file created(?: \[(\d+) bytes)?`)

// dumped is the dump's size from jcmd's answer, -1 when it did not say; ok is
// false when no dump was written.
func dumped(res extension.ExecResult) (size int64, ok bool) {
	m := heapDumpCreated.FindSubmatch(res.Stdout)
	if m == nil {
		return 0, false
	}
	if len(m[1]) == 0 {
		return -1, true
	}
	n, err := strconv.ParseInt(string(m[1]), 10, 64)
	if err != nil {
		return -1, true
	}
	return n, true
}

// outOfRoom is a dump the container had no room for.
func outOfRoom(res extension.ExecResult) bool {
	for _, s := range [][]byte{res.Stdout, res.Stderr} {
		if strings.Contains(string(s), "No space left on device") {
			return true
		}
	}
	return false
}

// heapDumpChoice is the answer when the container runs several JVMs and none
// was chosen.
type heapDumpChoice struct {
	State string      `json:"state"`
	JVMs  []listedJVM `json:"jvms"`
}

func (JVM) heapDump(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("confirm") != h.ConfirmToken(r) {
			writeProblem(w, http.StatusBadRequest, CodeNotConfirmed, "A heap dump is taken only once it is confirmed.", nil)
			return
		}
		streamer, ok := h.(extension.ExecStreamer)
		if !ok {
			writeProblem(w, http.StatusNotImplemented, CodeNoStream, "This console cannot stream a file out of a container.", nil)
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
		pid, _, jvms, ok := pick(w, run, t.cluster, chosen)
		if !ok {
			return
		}
		if len(jvms) > 0 {
			writeJSON(w, http.StatusConflict, heapDumpChoice{State: CodeChooseJVM, JVMs: jvms})
			return
		}
		file, err := heapDumpPath()
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, CodeHeapDumpFailed, "No file name could be drawn.", err)
			return
		}
		if !readBackPossible(w, run, t.cluster, file) {
			return
		}
		// From here a file may exist: it is removed whatever happens, even when
		// the page has gone, on a context of its own.
		defer removeDump(h, r, t, file)

		res, err := run.run(func(plain bool) ([]string, error) { return heapDumpArgv(plain, pid, file) }, heapDumpTimeout)
		size, created := dumped(res)
		switch {
		case err != nil:
			writeExecFailure(w, t.cluster, err)
			return
		case outOfRoom(res):
			writeProblem(w, http.StatusInsufficientStorage, CodeNoSpace, "The container has no room left in /tmp for the dump.",
				errors.New(said(res)))
			return
		case attachDisabled(res):
			writeProblem(w, http.StatusUnprocessableEntity, CodeAttachDisabled, attachDisabledText, errors.New(said(res)))
			return
		case attachRefused(res):
			writeProblem(w, http.StatusUnprocessableEntity, CodeAttachRefused, attachRefusedText, errors.New(said(res)))
			return
		case !created:
			writeProblem(w, http.StatusBadGateway, CodeHeapDumpFailed, "The JVM wrote no heap dump.", errors.New(said(res)))
			return
		case size > heapDumpLimit:
			writeProblem(w, http.StatusRequestEntityTooLarge, CodeHeapTooLarge,
				"The dump is "+strconv.FormatInt(size, 10)+" bytes, past the "+strconv.Itoa(heapDumpLimit)+" a download carries.", nil)
			return
		}
		if r.Context().Err() != nil {
			return
		}

		streamFile(w, r, streamer, t, file, ".hprof", size, heapDumpLimit)
		// Past the first byte a failure breaks the download, which the
		// browser shows as failed: Content-Length does not match.
	})
}

// readBackPossible asks for cat and rm before the JVM is paused. On a failure
// it has answered w.
func readBackPossible(w http.ResponseWriter, run *runner, cluster, file string) bool {
	for _, program := range []string{"cat", "rm"} {
		argv := []string{"cat", "/dev/null"}
		if program == "rm" {
			argv, _ = fileArgv("rm", file)
		}
		res, err := run.exec(argv, heapRmTimeout)
		switch {
		case errors.Is(err, extension.ErrNoSuchCommand):
			writeProblem(w, http.StatusUnprocessableEntity, CodeNoReadBack,
				"There is no "+program+" in this image, so a file could not be downloaded and removed.", nil)
			return false
		case err != nil:
			writeExecFailure(w, cluster, err)
			return false
		case res.ExitCode != 0:
			writeProblem(w, http.StatusBadGateway, CodeNoReadBack, program+" failed in the container (exit "+
				strconv.Itoa(res.ExitCode)+").", errors.New(strings.TrimSpace(string(res.Stderr))))
			return false
		}
	}
	return true
}

// removeDump removes the file, on a context the page leaving does not end.
func removeDump(h extension.Host, r *http.Request, t target, file string) {
	argv, err := fileArgv("rm", file)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), heapRmTimeout+5*time.Second)
	defer cancel()
	res, err := t.runner(h, r.WithContext(ctx)).exec(argv, heapRmTimeout)
	if err != nil || res.ExitCode != 0 {
		log.Printf("[kubetower] jvm: the file %s could not be removed from %s/%s container %s: %v %s",
			file, t.namespace, t.name, t.container, err, strings.TrimSpace(string(res.Stderr)))
	}
}

// streamFile streams file out of the container as a download named after the
// pod, the container and the time, at most limit bytes, with Content-Length
// when size is known (above zero). Nothing is held here. A failure before the
// first byte is answered as a problem; past it the download is broken, which
// the browser shows as failed when the length does not match.
func streamFile(w http.ResponseWriter, r *http.Request, streamer extension.ExecStreamer, t target, file, ext string, size, limit int64) {
	cat, _ := fileArgv("cat", file)
	hd := w.Header()
	hd.Set("Content-Type", "application/octet-stream")
	hd.Set("Content-Disposition", `attachment; filename="`+t.name+"-"+t.container+"-"+time.Now().UTC().Format("20060102-150405")+ext+`"`)
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Content-Type-Options", "nosniff")
	if size > 0 {
		hd.Set("Content-Length", strconv.FormatInt(size, 10))
		limit = size
	}
	got, err := streamer.ExecStream(r, t.cluster, extension.ExecQuery{
		Namespace: t.namespace, Pod: t.name, Container: t.container, Command: cat, Timeout: heapReadTimeout,
	}, w, limit)
	if got.Written == 0 && (err != nil || got.ExitCode != 0) {
		// Nothing was sent yet: the answer can still be a problem.
		for _, k := range []string{"Content-Type", "Content-Disposition", "Content-Length"} {
			hd.Del(k)
		}
		if err != nil {
			writeExecFailure(w, t.cluster, err)
			return
		}
		writeProblem(w, http.StatusBadGateway, CodeHeapDumpFailed, "The file could not be read back from the container.",
			errors.New(strings.TrimSpace(string(got.Stderr))))
	}
}
