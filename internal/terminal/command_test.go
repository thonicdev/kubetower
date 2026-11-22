package terminal

import (
	"errors"
	"testing"
)

func TestTheOutputKeepsOneLimitAcrossBothStreamsAndStopsTheCommand(t *testing.T) {
	stopped := 0
	out := &boundedOutput{limit: 10, full: func() { stopped++ }}
	stdout, stderr := out.stream(&out.stdout), out.stream(&out.stderr)

	for _, write := range []struct {
		to   *boundedStream
		text string
	}{{stdout, "hello "}, {stderr, "wor"}, {stdout, "ld and more"}, {stderr, "late"}} {
		n, err := write.to.Write([]byte(write.text))
		if err != nil || n != len(write.text) {
			t.Fatalf("Write(%q) = %d, %v: a short write would fail the stream", write.text, n, err)
		}
	}
	got := out.result()
	if string(got.Stdout) != "hello l" || string(got.Stderr) != "wor" || !got.Truncated {
		t.Errorf("kept stdout %q, stderr %q, truncated %t", got.Stdout, got.Stderr, got.Truncated)
	}
	if stopped != 1 {
		t.Errorf("the command was stopped %d times, want once", stopped)
	}
}

func TestOutputWithinTheLimitIsKeptWhole(t *testing.T) {
	out := &boundedOutput{limit: 10, full: func() { t.Error("stopped within the limit") }}
	if _, err := out.stream(&out.stdout).Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	if got := out.result(); string(got.Stdout) != "0123456789" || got.Truncated {
		t.Errorf("result %+v", got)
	}
}

// The sentences are the runtimes' own, as the API server relays them: the
// first is kind's (containerd with runc), copied from a real exec.
func TestAProgramMissingFromTheImageIsToldFromAnotherFailure(t *testing.T) {
	for _, msg := range []string{
		`Internal error occurred: error executing command in container: failed to exec in container: failed to start exec "f5542b": OCI runtime exec failed: exec failed: unable to start container process: exec: "nosuchbin": executable file not found in $PATH`,
		`OCI runtime exec failed: exec failed: unable to start container process: exec: "/opt/java/openjdk/bin/jcmd": stat /opt/java/openjdk/bin/jcmd: no such file or directory`,
	} {
		if !notInImage(errors.New(msg)) {
			t.Errorf("not read as a missing program: %s", msg)
		}
	}
	for _, msg := range []string{
		`pods "api-1" not found`,
		`container app is not valid for pod api-1`,
		`unable to upgrade connection: Forbidden`,
	} {
		if notInImage(errors.New(msg)) {
			t.Errorf("read as a missing program: %s", msg)
		}
	}
}
