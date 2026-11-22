package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The console is built into a fresh directory of its own, never to a name in
// the shared temporary directory somebody else could have taken first.
func TestTheBuildGoesToAFreshPrivateDirectory(t *testing.T) {
	first, err := buildPath()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Dir(first))
	second, err := buildPath()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Dir(second))

	if filepath.Dir(first) == filepath.Dir(second) {
		t.Errorf("two builds share the directory %s", filepath.Dir(first))
	}
	if filepath.Dir(filepath.Dir(first)) != filepath.Clean(os.TempDir()) {
		t.Errorf("%s is not in a directory of its own under %s", first, os.TempDir())
	}
	info, err := os.Stat(filepath.Dir(first))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Errorf("the build directory is %v, want readable by this user only", info.Mode().Perm())
	}
}
