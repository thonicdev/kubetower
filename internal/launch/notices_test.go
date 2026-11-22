package launch

import (
	"bytes"
	"strings"
	"testing"
	"testing/fstest"
)

func TestNoticesPrintsWhatTheBundleCarries(t *testing.T) {
	var out bytes.Buffer
	bundle := fstest.MapFS{
		"index.html":              &fstest.MapFile{Data: []byte("<!doctype html>")},
		"THIRD_PARTY_NOTICES.txt": &fstest.MapFile{Data: []byte("KubeTower - third-party notices\n")},
	}
	if err := printNotices(&out, bundle); err != nil {
		t.Fatal(err)
	}
	if out.String() != "KubeTower - third-party notices\n" {
		t.Errorf("printed %q", out.String())
	}
}

// A build without the notices fails in words, and prints nothing that could be
// read as an empty list.
func TestNoticesAbsentIsAnErrorNotAnEmptyAnswer(t *testing.T) {
	var out bytes.Buffer
	err := printNotices(&out, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html>")}})
	if err == nil || !strings.Contains(err.Error(), "no third-party notices") {
		t.Fatalf("got %v, want an error saying the build carries none", err)
	}
	if out.Len() != 0 {
		t.Errorf("printed %q", out.String())
	}
}
