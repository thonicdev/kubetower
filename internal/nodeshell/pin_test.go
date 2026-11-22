package nodeshell

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The node shell's pod runs privileged, with hostPID and the node's `/`
// mounted, and `IfNotPresent`. A mutable tag there is whatever the registry or
// a node's cache answers for `alpine:3.24` on the day - a root shell on a
// production node from an image nobody here chose.

// pinned is a Docker Official Image reference with its tag kept for the reader
// and Renovate, and the multi-arch index's digest for the kubelet.
var pinned = regexp.MustCompile(`^alpine:(\d+\.\d+)@sha256:[a-f0-9]{64}$`)

func TestTheDefaultImageIsPinnedByDigest(t *testing.T) {
	if !pinned.MatchString(DefaultImage) {
		t.Fatalf("DefaultImage = %q, want alpine:<major.minor>@sha256:<64 hex>: a tag alone is "+
			"whatever the registry answers on the day the pod is created", DefaultImage)
	}
	// The setting's own check must accept the shipped default, or clearing
	// the field would store something the next load refuses.
	if err := Validate(DefaultImage); err != nil {
		t.Errorf("the shipped default does not pass Validate: %v", err)
	}
	if len(DefaultImage) > MaxImageLength {
		t.Errorf("the shipped default is %d characters, over the field's %d", len(DefaultImage), MaxImageLength)
	}
}

// **The tag is the Dockerfile's Alpine.** DefaultImage's comment says the
// default is what this repository's own image builds from, and the two can
// drift: the ARG moves and the constant does not. Renovate's custom manager moves this one and the Dockerfile's manager
// moves the ARG; this is what fails when only one of them did.
//
// The Dockerfile is in the image's build context (`.dockerignore` does not
// exclude it), so this runs in the image's test gate too; it skips rather than
// fails where the file is absent, which is a tree this test cannot judge.
func TestTheDefaultImageIsTheDockerfilesAlpine(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	if os.IsNotExist(err) {
		t.Skip("no Dockerfile at the repository root in this tree")
	}
	if err != nil {
		t.Fatal(err)
	}
	arg := regexp.MustCompile(`(?m)^ARG ALPINE_VERSION=(\S+)\s*$`).FindStringSubmatch(string(raw))
	if arg == nil {
		t.Fatal("the Dockerfile has no `ARG ALPINE_VERSION=`; this test reads the runtime image's Alpine from it")
	}
	tag := pinned.FindStringSubmatch(DefaultImage)
	if tag == nil {
		t.Fatalf("DefaultImage %q is not pinned (see TestTheDefaultImageIsPinnedByDigest)", DefaultImage)
	}
	if strings.TrimSpace(arg[1]) != tag[1] {
		t.Errorf("the node shell runs alpine:%s and the Dockerfile builds on alpine:%s; "+
			"move them together", tag[1], arg[1])
	}
}
