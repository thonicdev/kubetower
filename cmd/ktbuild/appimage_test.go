package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestTheAppImageToolIsPinned. appimage.yml packs the Linux AppImage with
// appimagetool, a third-party binary it downloads and runs, and appimagetool
// in turn prepends a runtime it downloads itself unless it is handed one. Both
// come from GitHub releases that also publish a "continuous" build, whose file
// changes under one name. So both are pinned by a released version and by a
// SHA-256, the hash is checked before either file is executed, and the runtime
// is passed by path so appimagetool never fetches its own.
func TestTheAppImageToolIsPinned(t *testing.T) {
	jobs := workflowJobs(t, "appimage.yml")
	job, ok := jobs["appimage"]
	if !ok {
		t.Fatal("appimage.yml has no appimage job")
	}
	env, _ := job["env"].(map[string]any)
	version := regexp.MustCompile(`^[0-9][0-9.]*$`)
	sha := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, name := range []string{"APPIMAGETOOL", "APPIMAGE_RUNTIME"} {
		if v := fmt.Sprint(env[name+"_VERSION"]); !version.MatchString(v) {
			t.Errorf("appimage.yml pins %s_VERSION to %q, not a released version", name, v)
		}
		if v := fmt.Sprint(env[name+"_SHA256"]); !sha.MatchString(v) {
			t.Errorf("appimage.yml pins %s_SHA256 to %q, not a SHA-256", name, v)
		}
	}
	if !strings.Contains(jobText(job), "packaging/linux/appimage/build.sh ") {
		t.Error("appimage.yml's job does not run packaging/linux/appimage/build.sh")
	}

	script := readPackaging(t, "appimage/build.sh")
	for _, want := range []string{
		`releases/download/${APPIMAGETOOL_VERSION}/appimagetool-x86_64.AppImage"`,
		`echo "$APPIMAGETOOL_SHA256  $tools/appimagetool" | sha256sum --check --strict`,
		`releases/download/${APPIMAGE_RUNTIME_VERSION}/runtime-x86_64"`,
		`echo "$APPIMAGE_RUNTIME_SHA256  $tools/runtime" | sha256sum --check --strict`,
		`--runtime-file "$tools/runtime"`,
		`--appimage-extract-and-run --version`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("build.sh does not carry %q", want)
		}
	}
	if strings.Contains(script, "continuous") && !strings.Contains(script, `never the "continuous" release`) {
		t.Error("build.sh names the continuous release outside the comment that refuses it")
	}
	// Each file is checked before it is first executed.
	for run, check := range map[string]string{
		`APPIMAGE_EXTRACT_AND_RUN=1 "$tools/appimagetool"`: `echo "$APPIMAGETOOL_SHA256  $tools/appimagetool" | sha256sum --check --strict`,
		`--runtime-file "$tools/runtime"`:                  `echo "$APPIMAGE_RUNTIME_SHA256  $tools/runtime" | sha256sum --check --strict`,
	} {
		checked, used := strings.Index(script, check), strings.Index(script, run)
		if checked < 0 || used < 0 || used < checked {
			t.Errorf("build.sh runs %s before checking it (check at %d, first run at %d)", run, checked, used)
		}
	}
	// No update information and no signature: appimagetool writes either only
	// when asked, so no line that runs it asks.
	for _, line := range strings.Split(strings.ReplaceAll(script, "\\\n", " "), "\n") {
		if !strings.Contains(line, `"$tools/appimagetool"`) || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, flag := range []string{"-u", "-g", "-s", "--updateinformation", "--guess", "--sign", "--sign-key"} {
			if slices.Contains(strings.Fields(line), flag) {
				t.Errorf("build.sh passes %s to appimagetool: %s", flag, strings.TrimSpace(line))
			}
		}
	}
}

// TestAppRunHandsEveryArgumentOver. The AppImage's AppRun is the only thing
// between a person's command line (or the desktop entry's `--app`) and the
// binary, so it passes every argument through as it came and execs, so the
// binary is the process that gets the signals.
func TestAppRunHandsEveryArgumentOver(t *testing.T) {
	apprun := readPackaging(t, "appimage/AppRun")
	if !strings.HasPrefix(apprun, "#!/bin/sh\n") {
		t.Error("AppRun is not a /bin/sh script")
	}
	if !strings.Contains(apprun, `exec "$here/usr/bin/kubetower" "$@"`) {
		t.Error(`AppRun does not exec usr/bin/kubetower with "$@"`)
	}
	info, err := os.Stat(filepath.Join("..", "..", "packaging", "linux", "appimage", "AppRun"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("AppRun is not executable in the tree (%v)", info.Mode().Perm())
	}
}

// readPackaging reads a file under packaging/linux, skipping where the tree
// does not carry it (the image's build context).
func readPackaging(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "packaging", "linux", filepath.FromSlash(name)))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("packaging/linux/%s is not in this tree, so it is UNCHECKED here: %v", name, err)
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestTheReleaseChecksTheAppImageItShips. A tag builds the AppImage from the
// archives job's own Linux amd64 archive, checked against that job's
// checksums first; the draft checks the image against the hash the appimage
// job computed and lists it in checksums.txt; nothing is pushed or drafted
// before it exists; and ci.yml's full tier runs the same file, so a branch
// proves it without a tag.
func TestTheReleaseChecksTheAppImageItShips(t *testing.T) {
	release := workflowJobs(t, "release.yml")
	caller, ok := release["appimage"]
	if !ok {
		t.Fatal("release.yml has no appimage job")
	}
	if got := fmt.Sprint(caller["uses"]); got != "./.github/workflows/appimage.yml" {
		t.Errorf("release.yml's appimage job uses %q", got)
	}
	with, _ := caller["with"].(map[string]any)
	for key, want := range map[string]string{
		"artifact": "kubetower-${{ needs.verify.outputs.version }}",
		"sums":     "${{ needs.archives.outputs.sums }}",
		"ref":      "${{ needs.verify.outputs.sha }}",
	} {
		if got := fmt.Sprint(with[key]); got != want {
			t.Errorf("release.yml's appimage job passes %s %q, not %q", key, got, want)
		}
	}
	dtext := jobText(release["draft"])
	for _, want := range []string{
		"APPIMAGE: ${{ needs.appimage.outputs.sha256 }}",
		`echo "$APPIMAGE  ./kubetower-${VERSION}-x86_64.AppImage" | sha256sum --check --strict`,
		`echo "$APPIMAGE  ./kubetower-${VERSION}-x86_64.AppImage" >> checksums.txt`,
		" RPM APPIMAGE; do",
	} {
		if !strings.Contains(dtext, want) {
			t.Errorf("the draft job does not carry %q, so the AppImage it publishes is unchecked", want)
		}
	}
	if stepWhere(release["draft"], moves("download-artifact", "kubetower-appimage-${{ needs.verify.outputs.version }}")) < 0 {
		t.Error("the draft job does not download the AppImage")
	}
	for _, j := range []string{"image", "draft"} {
		if !slices.Contains(needs(release[j]), "appimage") {
			t.Errorf("release.yml's %s job does not need appimage: %v", j, needs(release[j]))
		}
	}

	ci := workflowJobs(t, "ci.yml")
	full, ok := ci["appimage"]
	if !ok {
		t.Fatal("ci.yml has no appimage job: the full tier would not prove what a tag runs")
	}
	if got := fmt.Sprint(full["uses"]); got != "./.github/workflows/appimage.yml" {
		t.Errorf("ci.yml's appimage job uses %q", got)
	}
	if got, want := fmt.Sprint(full["if"]), "needs.detect.outputs.go == 'yes' && needs.detect.outputs.full == 'yes'"; got != want {
		t.Errorf("ci.yml's appimage job runs if %q, not the full tier's %q", got, want)
	}
	// A reusable workflow that checks out this private repository gets no
	// more scope than its caller grants.
	for name, j := range map[string]map[string]any{"release.yml": caller, "ci.yml": full} {
		perms, _ := j["permissions"].(map[string]any)
		if got := fmt.Sprint(perms["contents"]); got != "read" {
			t.Errorf("%s's appimage job grants contents: %s, and appimage.yml checks out the repository", name, got)
		}
	}
}
