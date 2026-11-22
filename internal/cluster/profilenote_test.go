package cluster

import (
	"os"
	"strings"
	"testing"
)

// An empty profile is the default resolution, not the absence of credentials.
//
// The distinction is the whole reason this string exists. A kubeconfig that
// names no profile hands the decision to `LoadDefaultConfig`, which reads
// `AWS_PROFILE` from *this process's* environment and then falls back to
// `default` — and a console started from a shortcut has neither of a shell's.
// So a terminal where `kubectl` works and a GUI-launched console can authenticate
// as two different identities out of one kubeconfig, which EKS answers with a
// bare 401 and no explanation.
func TestAnUnnamedProfileSaysWhatDecidedInstead(t *testing.T) {
	named := profileNote("prod-admin")
	if !strings.Contains(named, "prod-admin") {
		t.Errorf("the profile is not in the line: %q", named)
	}

	unnamed := profileNote("")
	if !strings.Contains(unnamed, "AWS_PROFILE") {
		t.Errorf("an unnamed profile does not say what decided instead: %q", unnamed)
	}
	if strings.Contains(strings.ToLower(unnamed), "no credentials") {
		t.Errorf("an unnamed profile reads as having none, which sends the reader to the wrong "+
			"problem entirely: %q", unnamed)
	}
}

// And it reaches the log, which is the only channel a GUI-launched console has.
//
// A source assertion, because the helper asserted directly stays green when
// `profileNote(spec.Profile)` is dropped from the log call. The line is the one
// artifact a person on another machine can send, so what is *in* it is the
// whole of what can be diagnosed remotely — cluster and region say where the
// token goes and only the profile says as whom.
func TestTheStartupLineNamesTheProfileItSignsWith(t *testing.T) {
	source, err := os.ReadFile("manager.go")
	if err != nil {
		t.Fatalf("reading manager.go: %v", err)
	}
	text := string(source)

	i := strings.Index(text, "EKS tokens are signed in this process")
	if i < 0 {
		t.Fatal("the line that says which credential path a context took is gone")
	}
	end := strings.Index(text[i:], ")\n")
	if end < 0 {
		t.Fatal("the log call is unterminated")
	}
	call := text[i : i+end]

	if !strings.Contains(call, "profileNote(") {
		t.Errorf("the startup line does not name the profile, so a bare 401 on another "+
			"machine cannot be diagnosed from the one artifact that machine can send:\n%s", call)
	}
}

// The marker has to reach the client, or nothing downstream can ask.
func TestTheSigningMarkerIsPutOnTheClient(t *testing.T) {
	source, err := os.ReadFile("manager.go")
	if err != nil {
		t.Fatalf("reading manager.go: %v", err)
	}
	if !strings.Contains(string(source), "EKS: signing,") {
		t.Error("the client is built without the signing marker, so internal/health cannot say " +
			"which identity a refused context authenticated as")
	}
}
