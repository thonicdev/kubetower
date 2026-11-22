package main

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestABuildThatShipsRefusesATreeWithNoConsole. The setup, and any stamped
// version, refuse a tree on which the console was never built - before linking
// anything, and before looking for makensis - and say what to run.
func TestABuildThatShipsRefusesATreeWithNoConsole(t *testing.T) {
	root := t.TempDir()
	dist := filepath.Join(root, "web", "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	// The committed placeholder, and nothing else: a fresh checkout.
	if err := os.WriteFile(filepath.Join(dist, ".gitkeep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	saved := moduleRoot
	moduleRoot = func() (string, error) { return root, nil }
	t.Cleanup(func() { moduleRoot = saved })

	out := filepath.Join(t.TempDir(), "kubetower-setup.exe")
	for _, tc := range []target{
		{goos: "windows", goarch: "amd64", setup: true},
		{goos: "windows", goarch: "amd64", setup: true, version: "0.1.0", built: "2026-10-15"},
		{goos: "linux", goarch: "amd64", version: "0.1.0"},
		{goos: "darwin", goarch: universalArch, version: "0.1.0"},
	} {
		err := build(tc, out)
		if err == nil || !strings.Contains(err.Error(), consoleIndex) || !strings.Contains(err.Error(), "task build:web") {
			t.Errorf("build(%+v) on a tree with no console = %v, want a refusal naming %s and what to run",
				tc, err, consoleIndex)
		}
	}
	if _, err := os.Stat(out); err == nil {
		t.Errorf("a refused build still wrote %s", out)
	}

	// An empty index.html is a build that wrote nothing, not a console.
	ui := filepath.Join(dist, "ui")
	if err := os.MkdirAll(ui, 0o755); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(ui, "index.html")
	if err := os.WriteFile(index, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := consoleBuilt(root); err == nil {
		t.Error("an empty index.html passed as a built console")
	}
	if err := os.WriteFile(index, []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := consoleBuilt(root); err != nil {
		t.Errorf("a built index.html was refused: %v", err)
	}
}

// TestABuildThatShipsIsReadBackForTheConsole. An index.html on disk is not a
// console in the binary - a build could embed another directory, or none - so
// what was linked is read back too. Exercised on a module standing in for this
// one: its cmd/kubetower embeds nothing, then embeds the bundle.
func TestABuildThatShipsIsReadBackForTheConsole(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module hello\n\ngo 1.21\n")
	for rel, body := range standInBundle() {
		write("web/"+rel, body)
	}
	saved := moduleRoot
	moduleRoot = func() (string, error) { return root, nil }
	t.Cleanup(func() { moduleRoot = saved })
	stamped := target{goos: "linux", goarch: "amd64", version: "0.1.0"}

	write("cmd/kubetower/main.go", "package main\n\nfunc main() {}\n")
	out := filepath.Join(t.TempDir(), "kubetower")
	if err := build(stamped, out); err == nil || !strings.Contains(err.Error(), "does not embed the console") {
		t.Errorf("a stamped build whose binary embeds no console = %v, want a refusal", err)
	}

	write("web/embed.go", "package web\n\nimport \"embed\"\n\n//go:embed all:dist\nvar Assets embed.FS\n")
	write("cmd/kubetower/main.go", "package main\n\nimport (\n\t\"fmt\"\n\n\t\"hello/web\"\n)\n\n"+
		"func main() { fmt.Println(web.Assets.ReadDir(\".\")) }\n")
	if err := build(stamped, out); err != nil {
		t.Errorf("a stamped build whose binary embeds the console was refused: %v", err)
	}
}

// TestAStagedSetupNeedsNoConsoleInTheTree. `-setup -stage` wraps a binary
// already built, and release.yml's setup job runs it on a checkout with no
// web/dist at all: asking that tree for web/dist/ui/index.html would refuse
// every tag. The binary in the stage is read back instead (buildSetup). Runs
// with or without makensis: whatever else refuses an empty stage, it must not
// be the tree's console.
func TestAStagedSetupNeedsNoConsoleInTheTree(t *testing.T) {
	root := t.TempDir() // no web/ at all, as release.yml's setup checkout
	saved := moduleRoot
	moduleRoot = func() (string, error) { return root, nil }
	t.Cleanup(func() { moduleRoot = saved })

	out := filepath.Join(t.TempDir(), "kubetower-setup.exe")
	for _, tc := range []target{
		{goos: "windows", goarch: "amd64", setup: true, stage: t.TempDir()},
		{goos: "windows", goarch: "amd64", setup: true, stage: t.TempDir(), version: "0.1.0", built: "2026-10-15"},
	} {
		if err := build(tc, out); err != nil && strings.Contains(err.Error(), consoleIndex) {
			t.Errorf("build(%+v) asks the tree for its console, which a -stage build never uses: %v", tc, err)
		}
	}
}

// TestTheCheckConsoleFlagFailsTheCommand. Three workflow lines rest on `ktbuild
// -check-console` exiting non-zero, not on the function behind it: the payload
// checks in ci.yml and release.yml, and release.yml's check of the zip's
// binary. So the command itself is run, on a binary without the console and on
// one with it.
func TestTheCheckConsoleFlagFailsTheCommand(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go is not on PATH, and this test runs ktbuild with it: %v", err)
	}
	run := func(binary string) (string, error) {
		t.Helper()
		cmd := exec.Command(gobin, "run", ".", "-check-console", binary)
		cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=")
		raw, err := cmd.CombinedOutput()
		return string(raw), err
	}

	bare := standIn(t, "placeholder.exe", "-s -w", map[string]string{"dist/.gitkeep": ""})
	out, err := run(bare)
	if err == nil || !strings.Contains(out, "does not embed the console") {
		t.Errorf("ktbuild -check-console on a binary without the console: err %v, output %q; want a non-zero "+
			"exit naming the console", err, out)
	}

	full := standIn(t, "bundle.exe", "-s -w", standInBundle())
	if out, err := run(full); err != nil || !strings.Contains(out, "embeds the console") {
		t.Errorf("ktbuild -check-console on a binary with the console: err %v, output %q", err, out)
	}
}

// TestAnUnstampedBuildStillNeedsNoNode. The placeholder exists so `go build`,
// `go test` and an unstamped `task build:api` work with no Node installed
// (web/embed.go); only a build that ships asks for the console.
func TestAnUnstampedBuildStillNeedsNoNode(t *testing.T) {
	for _, tc := range []struct {
		t    target
		want bool
	}{
		{target{goos: "linux", goarch: "amd64"}, false},
		{target{goos: "windows", goarch: "amd64"}, false},
		{target{goos: "darwin", goarch: universalArch}, false},
		{target{goos: "windows", goarch: "amd64", setup: true}, true},
		{target{goos: "windows", goarch: "amd64", setup: true, stage: "stage"}, true},
		{target{goos: "linux", goarch: "arm64", version: "0.1.0"}, true},
	} {
		if got := tc.t.shipsConsole(); got != tc.want {
			t.Errorf("%+v ships the console = %v, want %v", tc.t, got, tc.want)
		}
	}
}

// standInIndex is a built index.html as Vite writes it, cut down.
const standInIndex = `<!doctype html>
<html lang="en">
  <head>
    <title>KubeTower</title>
    <script type="module" crossorigin src="/assets/index-CLp_VK_a.js"></script>
  </head>
  <body>
    <div id="root"></div>
  </body>
</html>
`

// standInBundle is the dist/ tree of a built console: the placeholder, the
// index, and the entry chunk it names.
func standInBundle() map[string]string {
	return map[string]string{
		"dist/.gitkeep":                    "",
		"dist/ui/index.html":               standInIndex,
		"dist/ui/assets/index-CLp_VK_a.js": "console.log('kubetower')\n",
	}
}

// standIn links a windows/amd64 program embedding files the way web/embed.go
// embeds web/dist, with ldflags, and returns its path.
func standIn(t *testing.T, name, ldflags string, files map[string]string) string {
	t.Helper()
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go is not on PATH, and this test builds a stand-in with it: %v", err)
	}
	dir := t.TempDir()
	files["go.mod"] = "module hello\n\ngo 1.21\n"
	files["main.go"] = "package main\n\nimport (\n\t\"embed\"\n\t\"fmt\"\n\t\"io/fs\"\n)\n\n" +
		"//go:embed all:dist\nvar assets embed.FS\n\n" +
		"func main() {\n\tn := 0\n\t_ = fs.WalkDir(assets, \".\", func(string, fs.DirEntry, error) error { n++; return nil })\n" +
		"\tfmt.Println(n)\n}\n"
	for rel, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(dir, name)
	cmd := exec.Command(gobin, "build", "-ldflags", ldflags, "-o", out, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=windows", "GOARCH=amd64", "GOTOOLCHAIN=local", "GOFLAGS=")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building %s: %v\n%s", name, err, raw)
	}
	return out
}

// TestTheConsoleCheckTellsTheBundleFromThePlaceholder runs the read-back on
// three real binaries embedding a dist/ tree the way web/embed.go does: a
// Vite-shaped bundle, the placeholder alone, and an index whose entry chunk is
// not in the binary - internal/web/spa.go's "renders, and renders nothing".
func TestTheConsoleCheckTellsTheBundleFromThePlaceholder(t *testing.T) {
	bundle := standIn(t, "bundle.exe", "-s -w", standInBundle())
	if err := checkEmbedsConsole(bundle); err != nil {
		t.Errorf("a binary embedding the bundle was refused: %v", err)
	}

	placeholder := standIn(t, "placeholder.exe", "-s -w", map[string]string{"dist/.gitkeep": ""})
	if err := checkEmbedsConsole(placeholder); err == nil || !strings.Contains(err.Error(), "does not embed the console") {
		t.Errorf("a binary embedding only the placeholder = %v, want a refusal", err)
	}

	noChunk := standIn(t, "nochunk.exe", "-s -w", map[string]string{
		"dist/.gitkeep":                    "",
		"dist/ui/index.html":               standInIndex,
		"dist/ui/assets/other-AbCdEf12.js": "console.log('kubetower')\n",
	})
	if err := checkEmbedsConsole(noChunk); err == nil || !strings.Contains(err.Error(), "index-CLp_VK_a.js") {
		t.Errorf("a binary whose index names a chunk it does not embed = %v, want a refusal naming the chunk", err)
	}
}

// TestTheSetupIsNotWrappedAroundABinaryWithoutTheConsole is release.yml's way
// in, end to end: `-setup -stage` handed a GUI kubetower.exe that embeds only
// the placeholder refuses before makensis runs and writes no setup, and the
// same stage around a binary that embeds the console builds one. Needs
// makensis, so it skips without it - and ci.yml's setup job runs it with
// KT_REQUIRE_MAKENSIS, which makes that skip a failure.
func TestTheSetupIsNotWrappedAroundABinaryWithoutTheConsole(t *testing.T) {
	if _, err := exec.LookPath("makensis"); err != nil {
		if os.Getenv("KT_REQUIRE_MAKENSIS") != "" {
			t.Fatalf("KT_REQUIRE_MAKENSIS is set and makensis is not on PATH: %v", err)
		}
		t.Skipf("makensis is not on PATH, so the -stage console refusal is UNCHECKED here (ci.yml's setup job runs it): %v", err)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	stageWith := func(exe string) string {
		t.Helper()
		stage := t.TempDir()
		raw, err := os.ReadFile(exe)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stage, setupExe), raw, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range append([]string{setupNotices}, setupLicences...) {
			text, err := os.ReadFile(filepath.Join(root, "LICENSE")) // any text will do here
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(stage, name), text, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return stage
	}
	gui := "-s -w " + windowsGUI

	out := filepath.Join(t.TempDir(), "kubetower-setup.exe")
	bare := standIn(t, "placeholder.exe", gui, map[string]string{"dist/.gitkeep": ""})
	err = build(target{goos: "windows", goarch: "amd64", setup: true, stage: stageWith(bare)}, out)
	if err == nil || !strings.Contains(err.Error(), "does not embed the console") {
		t.Errorf("-setup -stage around a binary with no console = %v, want a refusal", err)
	}
	if _, err := os.Stat(out); err == nil {
		t.Errorf("the refused setup was written anyway: %s", out)
	}

	full := standIn(t, "bundle.exe", gui, standInBundle())
	if err := build(target{goos: "windows", goarch: "amd64", setup: true, stage: stageWith(full)}, out); err != nil {
		t.Fatalf("-setup -stage around a binary that embeds the console: %v", err)
	}
	if err := checkSetup(out, "dev"); err != nil {
		t.Errorf("the setup does not read back: %v", err)
	}
}

// TestNothingButTheBundleCarriesTheConsoleMarker. The read-back searches the
// whole binary, so it means something only while nothing else linked into
// kubetower carries what it looks for: no Go source outside a test, and no
// file under internal/web/static (the other embed). A fixture of the built
// page in non-test code would make every placeholder binary pass.
func TestNothingButTheBundleCarriesTheConsoleMarker(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, root))
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "testdata":
				return fs.SkipDir
			}
			if strings.HasSuffix(rel, "/web/dist") {
				return fs.SkipDir
			}
			return nil
		}
		embedded := strings.Contains(rel, "/internal/web/static/")
		source := strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, "_test.go")
		if !embedded && !source {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		seen++
		if consoleEntry.Match(raw) {
			t.Errorf("%s carries what checkEmbedsConsole looks for, so a binary with no console would pass it", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen < 100 {
		t.Fatalf("read %d files, which is not the module: the walk is not where it thinks it is", seen)
	}
}
