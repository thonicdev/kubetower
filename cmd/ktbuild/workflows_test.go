package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestTheReleaseGateNamesJobsCIStillHas. release.yml's `verify` job decides
// that a commit passed ci by finding two jobs of a ci.yml run by their display
// names: the Go job and the front-end job. Renaming either in ci.yml would make
// every tag refuse - the safe direction, but a surprise on the day of a
// release - so a rename has to fail here, on the pull request that makes it.
//
// Skipped only where .github is absent: the image's build context drops it
// (`.dockerignore`), and this suite runs inside that build.
func TestTheReleaseGateNamesJobsCIStillHas(t *testing.T) {
	workflows := filepath.Join("..", "..", ".github", "workflows")
	ci, err := os.ReadFile(filepath.Join(workflows, "ci.yml"))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("ci.yml is not in this tree (the image's build context excludes .github), so the "+
			"release gate's job names are UNCHECKED here: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	release, err := os.ReadFile(filepath.Join(workflows, "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"gofmt, vet, test -race, the contract from outside", "front end - lint, typecheck, build"}
	for _, name := range names {
		if !strings.Contains(string(ci), "    name: "+name+"\n") {
			t.Errorf("ci.yml has no job named %q, and release.yml's verify job looks for it: "+
				"every tag would be refused as not having passed ci", name)
		}
		if !strings.Contains(string(release), `.name == "`+name+`"`) {
			t.Errorf("release.yml's verify job no longer looks for the ci job %q; if the gate "+
				"changed, change this test with it", name)
		}
	}
	// And it counts all of them: a list that grew while the count did not
	// would accept a run missing one.
	if want := fmt.Sprintf(`if [ "$n" = %d ]`, len(names)); !strings.Contains(string(release), want) {
		t.Errorf("release.yml's verify job does not require all %d jobs (%s)", len(names), want)
	}
}

// workflowJobs parses a workflow and returns its jobs, or skips where .github
// is absent (the image's build context).
func workflowJobs(t *testing.T, file string) map[string]map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", file))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("%s is not in this tree (the image's build context excludes .github), so it is UNCHECKED here: %v", file, err)
	}
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Jobs map[string]map[string]any `json:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return doc.Jobs
}

// jobText is everything a job runs or is handed, as one string: the run:
// scripts and the env: values of its steps, in order.
func jobText(job map[string]any) string {
	var b strings.Builder
	steps, _ := job["steps"].([]any)
	for _, step := range steps {
		m, _ := step.(map[string]any)
		if env, ok := m["env"].(map[string]any); ok {
			keys := make([]string, 0, len(env))
			for k := range env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(&b, "%s: %v\n", k, env[k])
			}
		}
		if run, ok := m["run"].(string); ok {
			b.WriteString(run + "\n")
		}
	}
	return b.String()
}

func needs(job map[string]any) []string {
	var out []string
	switch v := job["needs"].(type) {
	case string:
		out = append(out, v)
	case []any:
		for _, n := range v {
			out = append(out, fmt.Sprint(n))
		}
	}
	return out
}

// TestTheReleaseChecksTheSetupItShips. release.yml's setup job runs only from a
// tag, so reading it is the only guard on three things:
//
//   - the setup job checks the zip it wraps against the archives job's own
//     checksum output before building on it, and wraps it with -stage;
//   - the draft checks the setup it downloads against the setup job's hash
//     output, not against a file that traveled with it;
//   - the image is pushed only after the setup exists, since a push is the one
//     step a re-run cannot take back.
func TestTheReleaseChecksTheSetupItShips(t *testing.T) {
	jobs := workflowJobs(t, "release.yml")
	setup, ok := jobs["setup"]
	if !ok {
		t.Fatal("release.yml has no setup job")
	}
	if outputs, _ := setup["outputs"].(map[string]any); outputs["sha256"] == nil {
		t.Error("the setup job has no sha256 output for the draft to check against")
	}
	text := jobText(setup)
	sums := strings.Index(text, `echo "$SUMS  checksums.txt" | sha256sum --check --strict`)
	zip := strings.Index(text, `grep -F "  ./$zip" checksums.txt | sha256sum --check --strict`)
	build := strings.Index(text, "go run ./cmd/ktbuild -os windows -arch amd64 -setup -stage ")
	switch {
	case sums < 0:
		t.Error("the setup job does not check checksums.txt against the archives job's output")
	case zip < 0:
		t.Error("the setup job does not check the Windows zip against checksums.txt")
	case build < 0:
		t.Error("the setup job does not build with -stage, around the zip's binary")
	case !(sums < zip && zip < build):
		t.Error("the setup job builds before it has checked what it builds on")
	}
	if !strings.Contains(text, "SUMS: ${{ needs.archives.outputs.sums }}") {
		t.Error("the setup job's SUMS is not the archives job's output")
	}

	draft := jobs["draft"]
	dtext := jobText(draft)
	for _, want := range []string{
		"SETUP: ${{ needs.setup.outputs.sha256 }}",
		`echo "$SETUP  ./kubetower-${VERSION}-setup.exe" | sha256sum --check --strict`,
		`echo "$SETUP  ./kubetower-${VERSION}-setup.exe" >> checksums.txt`,
		"for v in SUMS SETUP ",
	} {
		if !strings.Contains(dtext, want) {
			t.Errorf("the draft job does not carry %q, so the setup it publishes is unchecked", want)
		}
	}
	for _, job := range []string{"image", "draft"} {
		if !slices.Contains(needs(jobs[job]), "setup") {
			t.Errorf("release.yml's %s job does not need setup: %v", job, needs(jobs[job]))
		}
	}
}

// TestTheReleaseWriteScopesAreHeldAndDropped. release.yml's image and draft
// jobs hold its only write scopes, and may run on a self-hosted runner like
// every other job (TestEveryJobRunsOnThePool pins where). Because a self-hosted
// runner is not discarded, what is pinned here is what keeps a credential from outliving
// the job: the workflow grants nothing, each holds exactly its
// one write permission, the registry login goes into a directory of the job's
// own and never ~/.docker, gh keeps its directory under runner.temp, and an
// always() step logs out and removes it all as the job's last step.
func TestTheReleaseWriteScopesAreHeldAndDropped(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("release.yml is not in this tree (the image's build context excludes .github), so it is UNCHECKED here: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Permissions any `json:"permissions"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if got, ok := doc.Permissions.(map[string]any); !ok || len(got) != 0 {
		t.Errorf("release.yml's workflow-level permissions are %v, not {}", doc.Permissions)
	}
	jobs := workflowJobs(t, "release.yml")
	for name, job := range jobs {
		perms, _ := job["permissions"].(map[string]any)
		for scope, level := range perms {
			if level != "write" {
				continue
			}
			want := map[string]string{"image": "packages", "draft": "contents"}[name]
			if scope != want {
				t.Errorf("release.yml's %s job holds %s: write; only image may write packages and only draft contents", name, scope)
			}
		}
	}
	for name, want := range map[string]string{"image": "packages", "draft": "contents"} {
		job, ok := jobs[name]
		if !ok {
			t.Errorf("release.yml has no %s job", name)
			continue
		}
		perms, _ := job["permissions"].(map[string]any)
		if perms[want] != "write" {
			t.Errorf("release.yml's %s job does not hold %s: write (%v)", name, want, perms)
		}
		all := steps(job)
		if len(all) == 0 {
			t.Errorf("release.yml's %s job has no steps", name)
			continue
		}
		last := all[len(all)-1]
		run, _ := last["run"].(string)
		if fmt.Sprint(last["if"]) != "always()" {
			t.Errorf("release.yml's %s job does not end on an always() clean-up step (its last step runs if %v)", name, last["if"])
		}
		cleanup := []string{`rm -rf "$GH_CONFIG_DIR"`}
		if name == "image" {
			cleanup = append(cleanup, `docker --config "$KT_DOCKER_CONFIG" logout ghcr.io`, `rm -rf "$KT_DOCKER_CONFIG"`,
				`docker image rm "$REF"`,
				// A daemon that does not answer is a failure, never "the image is gone".
				`if ! docker info >/dev/null 2>&1; then`, `grep -qi 'no such image'`)
		} else {
			cleanup = []string{`for d in dist "$GH_CONFIG_DIR"; do`, `rm -rf "$d"`}
		}
		for _, want := range cleanup {
			if !strings.Contains(run, want) {
				t.Errorf("release.yml's %s job's last always() step does not run %q", name, want)
			}
		}
		// A clean-up that fails is a red job, never a green one with a line in
		// the log: every FAIL sets rc, and the step ends on it.
		lines := strings.Split(strings.TrimSpace(run), "\n")
		if got := strings.TrimSpace(lines[len(lines)-1]); got != "exit $rc" {
			t.Errorf("release.yml's %s job's clean-up ends on %q, not `exit $rc`, so a failed clean-up would read green", name, got)
		}
		for _, line := range lines {
			if strings.Contains(line, `echo "FAIL`) && !strings.Contains(line, "rc=1") {
				t.Errorf("release.yml's %s job's clean-up reports a failure without failing: %s", name, strings.TrimSpace(line))
			}
		}
		if n := strings.Count(run, "rc=1"); n == 0 {
			t.Errorf("release.yml's %s job's clean-up never sets rc=1, so it cannot fail", name)
		}
		// rc starts at 0 once, at the top, and is only ever raised after: an
		// `rc=0` further down, or any `exit` before the last line, would turn a
		// failed clean-up green.
		if !strings.HasPrefix(strings.TrimSpace(run), "set +e") {
			t.Errorf("release.yml's %s job's clean-up does not start with `set +e`: under the step's `bash -e` "+
				"one failing action would stop the ones after it", name)
		}
		if got := regexp.MustCompile(`\brc=[^1]`).FindAllString(run, -1); len(got) != 1 || !strings.Contains(run, "\nrc=0\n") {
			t.Errorf("release.yml's %s job's clean-up sets rc other than to 1 %d times (%q), want `rc=0` once, at the top",
				name, len(got), got)
		} else if first := strings.Index(run, "\nrc=0\n"); first > strings.Index(run, "rc=1") {
			t.Errorf("release.yml's %s job's clean-up sets rc=0 after it has raised it", name)
		}
		if n := len(regexp.MustCompile(`(?m)(^|[;&|]\s*|\s)exit\b`).FindAllString(run, -1)); n != 1 {
			t.Errorf("release.yml's %s job's clean-up has %d exits, want only the last `exit $rc`", name, n)
		}
		if last["continue-on-error"] != nil {
			t.Errorf("release.yml's %s job's clean-up has continue-on-error, so a failure would not turn the job red", name)
		}
		// Every step that calls gh keeps its directory under runner.temp.
		for _, step := range all {
			env, _ := step["env"].(map[string]any)
			if env["GH_TOKEN"] != nil && !strings.HasPrefix(fmt.Sprint(env["GH_CONFIG_DIR"]), "${{ runner.temp }}/") {
				t.Errorf("release.yml's %s job's step %q hands gh a token with GH_CONFIG_DIR %v, not under runner.temp",
					name, step["name"], env["GH_CONFIG_DIR"])
			}
		}
	}
	// draft has no checkout and the pool keeps the workspace, so it empties
	// dist before the first download: a dist left by a killed run would fail
	// every re-run.
	draft := jobs["draft"]
	empty := stepWhere(draft, runs("rm -rf dist\nmkdir dist\n"))
	download := stepWhere(draft, func(s map[string]any) bool {
		uses, _ := s["uses"].(string)
		return strings.HasPrefix(uses, "actions/download-artifact@")
	})
	switch {
	case empty < 0:
		t.Error("release.yml's draft job does not start from an empty dist (`rm -rf dist` then `mkdir dist`)")
	case download < 0:
		t.Error("release.yml's draft job downloads nothing, which this compares against")
	case empty > download:
		t.Error("release.yml's draft job empties dist after it has downloaded into it")
	}

	// The login: into a directory of the job's own, never the runner user's
	// ~/.docker, and every later docker call that talks to the registry with it.
	login := jobText(jobs["image"])
	for _, want := range []string{
		`cfg=$(mktemp -d "$RUNNER_TEMP/docker-config.XXXXXX")`,
		`docker --config "$cfg" login ghcr.io`,
		`docker --config "$cfg" push "$REF"`,
	} {
		if !strings.Contains(login, want) {
			t.Errorf("release.yml's image job does not carry %q", want)
		}
	}
	if regexp.MustCompile(`docker (login|push)`).MatchString(login) {
		t.Error("release.yml's image job logs in or pushes without --config, into the runner user's ~/.docker")
	}
	if regexp.MustCompile(`(?m)^\s*-?\s*uses:\s*docker/login-action`).Match(raw) {
		t.Error("release.yml uses docker/login-action, which writes the credential into ~/.docker")
	}
}

// TestTheHeavyJobsAreFullTierOnly. A pull request and a push to main run the fast tier
// only. The cross-compile, the Windows setup, the macOS disk images and the
// docker build run on the full tier - the weekly schedule on main, or a
// dispatch asking for it - and on a tag in release.yml; the docker build also
// runs on a pull request that touches what only the image build reads. detect
// decides both, and the jobs read its answer and nothing else.
func TestTheHeavyJobsAreFullTierOnly(t *testing.T) {
	jobs := workflowJobs(t, "ci.yml")
	for job, want := range map[string]string{
		// `!cancelled()` because they also need `web`, whose lint failing is no
		// reason to skip them (TestEveryBinaryThatShipsEmbedsTheConsole).
		"platforms": "${{ !cancelled() && needs.go.result == 'success' && needs.detect.outputs.go == 'yes' && needs.detect.outputs.full == 'yes' }}",
		"setup":     "${{ !cancelled() && needs.detect.outputs.go == 'yes' && needs.detect.outputs.full == 'yes' }}",
		"dmg":       "needs.detect.outputs.go == 'yes' && needs.detect.outputs.full == 'yes'",
		"image":     "needs.detect.outputs.image == 'yes'",
	} {
		j, ok := jobs[job]
		if !ok {
			t.Errorf("ci.yml has no %s job", job)
			continue
		}
		if got := fmt.Sprint(j["if"]); got != want {
			t.Errorf("ci.yml's %s job runs if %q, not %q", job, got, want)
		}
	}
	detect := jobs["detect"]
	env, _ := detect["env"].(map[string]any)
	// The full tier is the schedule and a dispatch asking for it: never a push
	// or a pull request.
	full := fmt.Sprint(env["FULL"])
	if want := "${{ github.event_name == 'schedule' || (github.event_name == 'workflow_dispatch' && inputs.full == true) }}"; full != want {
		t.Errorf("ci.yml's full tier is %q, not the schedule or a dispatch asking for it (%q)", full, want)
	}
	text := jobText(detect)
	for _, want := range []string{
		`full=no; [ "$FULL" = "true" ] && full=yes`,
		`if [ $full = yes ]; then`,
		`elif [ "$EVENT" = pull_request ]; then`,
		`grep -qE "$IMAGE_INPUTS"`,
		`echo "image=$image"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("ci.yml's detect job does not carry %q", want)
		}
	}

	// What only the image build reads sends a pull request to the docker
	// build; a source file, proved by the go and web jobs, does not.
	inputs, err := regexp.Compile(fmt.Sprint(env["IMAGE_INPUTS"]))
	if err != nil {
		t.Fatalf("ci.yml's IMAGE_INPUTS is not a regular expression: %v", err)
	}
	for path, want := range map[string]bool{
		"Dockerfile":                       true,
		"build/Dockerfile.dev":             true,
		".dockerignore":                    true,
		"go.mod":                           true,
		"go.sum":                           true,
		"web/package.json":                 true,
		"web/ui/package.json":              true,
		"web/pnpm-lock.yaml":               true,
		"web/pnpm-workspace.yaml":          true,
		"web/index.html":                   true,
		"web/vite.config.ts":               true,
		"api/openapi.yaml":                 true,
		"internal/web/static/pwa/icon.svg": true,
		"internal/web/static/pwa/icon-maskable.svg":    true,
		"internal/web/static/pwa/manifest.webmanifest": true,
		"cmd/ktnotices/main.go":                        true,
		"LICENSE":                                      true,
		"LICENSING.md":                                 true,
		"internal/resources/resources.go":              false,
		"web/ui/src/App.tsx":                           false,
		"boundary/go.mod":                              false,
		"mygo.mod":                                     false,
		"cmd/ktbuild/main.go":                          false,
	} {
		if got := inputs.MatchString(path); got != want {
			t.Errorf("ci.yml's IMAGE_INPUTS matches %s: %v, want %v", path, got, want)
		}
	}
	// And every file the Dockerfile copies or adds by name from the context
	// is one (dockerfileSources says how a line is read).
	if dockerfile, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile")); err == nil {
		named := dockerfileSources(string(dockerfile))
		if len(named) == 0 {
			t.Error("no COPY or ADD of a named file found in the Dockerfile, so this checked nothing")
		}
		for _, src := range named {
			if !inputs.MatchString(src) {
				t.Errorf("the Dockerfile copies %s, which IMAGE_INPUTS does not name", src)
			}
		}
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// `on:` is read the YAML 1.1 way by sigs.k8s.io/yaml, as the key `true`,
	// so both spellings are looked up: a test that only read "on" would find
	// no trigger at all and pass anything.
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	on, _ := doc["on"].(map[string]any)
	if on == nil {
		on, _ = doc["true"].(map[string]any)
	}
	if on["pull_request"] == nil || on["push"] == nil {
		t.Fatalf("ci.yml's triggers could not be read (%v), so its schedule is unchecked", on)
	}
	// One schedule, weekly: Sunday 02:00 UTC, outside working hours in Paris.
	schedule, _ := on["schedule"].([]any)
	if len(schedule) != 1 {
		t.Fatalf("ci.yml has %d schedules, want the one weekly full tier", len(schedule))
	}
	if cron := fmt.Sprint(schedule[0].(map[string]any)["cron"]); cron != "0 2 * * 0" {
		t.Errorf("ci.yml's schedule is %q, not the weekly full tier %q", cron, "0 2 * * 0")
	}
}

// dockerfileSources is every file a Dockerfile's COPY and ADD instructions take
// from the build context by name: continuation lines joined, any flag
// (`--chown=`, `--chmod=`, `--link`) skipped, a `--from=` copy - from a stage
// or an image, not the context - left out, the destination dropped, and a
// directory or `.` left out, since its files are sources the go and web jobs
// prove. A JSON-array form is read too.
func dockerfileSources(dockerfile string) []string {
	joined := strings.ReplaceAll(dockerfile, "\\\n", " ")
	var out []string
	for _, line := range strings.Split(joined, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		switch strings.ToUpper(fields[0]) {
		case "COPY", "ADD":
		default:
			continue
		}
		args := fields[1:]
		fromStage := false
		for len(args) > 0 && strings.HasPrefix(args[0], "--") {
			if strings.HasPrefix(args[0], "--from=") {
				fromStage = true
			}
			args = args[1:]
		}
		if fromStage || len(args) < 2 {
			continue
		}
		if strings.HasPrefix(args[0], "[") {
			var list []string
			if json.Unmarshal([]byte(strings.Join(args, " ")), &list) != nil || len(list) < 2 {
				continue
			}
			args = list
		}
		for _, src := range args[:len(args)-1] {
			if strings.HasSuffix(src, "/") || src == "." || strings.Contains(src, "://") {
				continue
			}
			out = append(out, src)
		}
	}
	return out
}

// TestDockerfileSourcesReadsEveryForm holds dockerfileSources to the shapes a
// Dockerfile can take, so the IMAGE_INPUTS check cannot go blind on one.
func TestDockerfileSourcesReadsEveryForm(t *testing.T) {
	got := dockerfileSources(strings.Join([]string{
		"FROM alpine AS a",
		"COPY go.mod go.sum ./",
		"COPY --chown=1000:1000 LICENSE /x/",
		"COPY --chmod=644 --link web/index.html /x/",
		"ADD api/openapi.yaml /api/",
		"add --chown=0:0 cmd/ktnotices/main.go /x/",
		`COPY ["internal/web/static/pwa/icon.svg", "/icons/"]`,
		"COPY internal/web/static/pwa/icon.svg \\",
		"     internal/web/static/pwa/icon-maskable.svg ./",
		"COPY --from=console /web/dist ./web/dist",
		"COPY --from=build /src/LICENSE /usr/share/",
		"COPY web/ ./",
		"COPY . .",
		"ADD https://example.invalid/x.tgz /x/",
	}, "\n"))
	want := []string{"go.mod", "go.sum", "LICENSE", "web/index.html", "api/openapi.yaml", "cmd/ktnotices/main.go",
		"internal/web/static/pwa/icon.svg", "internal/web/static/pwa/icon.svg", "internal/web/static/pwa/icon-maskable.svg"}
	if !slices.Equal(got, want) {
		t.Errorf("dockerfileSources read %q, want %q", got, want)
	}
}

// TestSmokeWatchesWhatTheConsoleImports. smoke.yml's header says how its paths
// are derived: every package of this module that the smoke's binaries link,
// dependencies of dependencies included (`go list -deps`). This asks Go for
// that list and holds the paths to it, so a new import anywhere in the graph
// does not leave smoke blind to the package it brings in. Skipped where .github
// is absent.
func TestSmokeWatchesWhatTheConsoleImports(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "smoke.yml"))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("smoke.yml is not in this tree (the image's build context excludes .github): %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	on, _ := doc["on"].(map[string]any)
	if on == nil {
		on, _ = doc["true"].(map[string]any)
	}
	pr, _ := on["pull_request"].(map[string]any)
	listed, _ := pr["paths"].([]any)
	if len(listed) == 0 {
		t.Fatalf("smoke.yml's pull_request paths could not be read (%v)", on)
	}
	paths := map[string]bool{}
	for _, p := range listed {
		paths[fmt.Sprint(p)] = true
	}
	const module = "github.com/thonicdev/kubetower/"
	cmd := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}",
		"./internal/web", "./internal/api", "./app",
		"./cmd/kubetower", "./cmd/ktbuild", "./cmd/ktnotices", "./cmd/devpassword")
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	seen := 0
	for _, imp := range strings.Fields(string(out)) {
		if !strings.HasPrefix(imp, module) {
			continue
		}
		seen++
		// Covered by its own directory or one above it.
		covered := false
		for dir := strings.TrimPrefix(imp, module); dir != "." && dir != ""; dir = filepath.ToSlash(filepath.Dir(dir)) {
			if paths[dir+"/**"] {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("the smoke's binaries link %s, and no path of smoke.yml's covers it", imp)
		}
	}
	if seen == 0 {
		t.Fatal("go list named no package of this module, so this checked nothing")
	}
	for _, want := range []string{"web/**", "api/openapi.yaml", "app/**", "internal/web/**", "internal/terminal/**",
		"extras/kubeadjust/**", "cmd/kubetower/**", "go.mod", "go.sum"} {
		if !paths[want] {
			t.Errorf("smoke.yml's paths do not name %s", want)
		}
	}
}

// TestNoRunOnMainIsCancelled. Every workflow that runs on a pull request or a
// push cancels a superseded pull-request run and nothing else, and groups
// every other event by commit, so a merge to main is never canceled - not
// even while pending, which one group per ref would do to the middle one of
// three quick merges.
func TestNoRunOnMainIsCancelled(t *testing.T) {
	const (
		group  = "${{ github.workflow }}-${{ github.event_name == 'pull_request' && github.ref || github.sha }}"
		cancel = "${{ github.event_name == 'pull_request' }}"
	)
	for _, file := range []string{"ci.yml", "guards.yml", "layout.yml", "security.yml", "smoke.yml"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", file))
		if errors.Is(err, fs.ErrNotExist) {
			t.Skipf("%s is not in this tree (the image's build context excludes .github), so it is UNCHECKED here: %v", file, err)
		}
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Concurrency map[string]any `json:"concurrency"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if got := fmt.Sprint(doc.Concurrency["group"]); got != group {
			t.Errorf("%s's concurrency group is %q, not %q", file, got, group)
		}
		if got := fmt.Sprint(doc.Concurrency["cancel-in-progress"]); got != cancel {
			t.Errorf("%s cancels in progress on %q, not %q", file, got, cancel)
		}
	}
}

// runsOn is the runs-on of every job here outside the release's files, and
// releaseRunsOn that of every job in them. A job runs where a repository
// variable says - RUNS_ON, or RUNS_ON_RELEASE for the release's jobs, each a
// JSON list of runner labels such as ["self-hosted","linux"] - and on a
// GitHub-hosted runner when the variable is unset, so a copy of this
// repository that sets nothing runs on hosted runners and needs no runner of
// its own.
//
// **A pull request from a fork runs on a GitHub-hosted runner whatever the
// variable says.** A self-hosted runner is not discarded after a job: it keeps
// what the job left on disk and shares its machine with the next job, so code
// nobody with write access has read must never reach one. The fork test comes
// first in the expression, so no variable can undo it. A fork's head
// repository may be gone (deleted after the pull request was opened); its
// full_name is then null, which differs from github.repository, so it is still
// read as a fork.
const (
	runsOn = "${{ github.event_name == 'pull_request' && " +
		"github.event.pull_request.head.repo.full_name != github.repository && 'ubuntu-latest' || " +
		"vars.RUNS_ON && fromJSON(vars.RUNS_ON) || 'ubuntu-latest' }}"
	releaseRunsOn = "${{ github.event_name == 'pull_request' && " +
		"github.event.pull_request.head.repo.full_name != github.repository && 'ubuntu-latest' || " +
		"vars.RUNS_ON_RELEASE && fromJSON(vars.RUNS_ON_RELEASE) || 'ubuntu-latest' }}"
)

// releaseFiles are the workflows whose jobs run the release's steps:
// release.yml and the three it calls. ci.yml's full tier calls the same three,
// which is why they take RUNS_ON_RELEASE for every caller: their steps need gh
// and hfsprogs, which not every runner carries.
var releaseFiles = map[string]bool{
	"release.yml": true, "dmg.yml": true, "appimage.yml": true, "linux-packages.yml": true,
}

// TestEveryJobRunsOnThePool. Every job here runs where runsOn, or
// releaseRunsOn in the release's files, sends it, written exactly. The rule is
// one expression rather than a set of acceptable labels because the other way
// is not checkable: a self-hosted runner does not have to be asked for as
// `self-hosted`, and `[linux, other-runner]` or `{ group: other-pool }` both
// reach one - with a fork's code, if the job runs on a pull request.
//
// **Read as GitHub reads it**: every workflow file, `.yaml` as well as `.yml`,
// parsed as YAML rather than matched line by line, so a quoted `"runs-on":` key
// and a job written as a flow mapping are read like any other; and a job that
// calls a reusable workflow names one in this directory, whose own jobs are
// checked here, or is refused. runsOnProblems is the rule, and
// TestTheRunsOnRuleRefusesEachWayRoundIt holds it to the ways a line-matching
// rule could be got round.
func TestEveryJobRunsOnThePool(t *testing.T) {
	dir := filepath.Join("..", "..", ".github", "workflows")
	files := workflowFiles(t, dir)
	local := map[string]bool{}
	for _, file := range files {
		local[filepath.Base(file)] = true
	}
	jobs := 0
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		problems, n := runsOnProblems(filepath.Base(file), raw, local)
		jobs += n
		for _, problem := range problems {
			t.Error(problem)
		}
	}
	if jobs == 0 {
		t.Fatal("no job found in .github/workflows, so this test checked nothing")
	}
	t.Logf("%d jobs read from %d workflow files", jobs, len(files))
}

// workflowFiles is every file GitHub would run as a workflow from dir: `.yml`
// and `.yaml` both. Skips where dir is absent (the image's build context).
func workflowFiles(t *testing.T, dir string) []string {
	t.Helper()
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("%s is not in this tree (the image's build context excludes .github), so the "+
			"workflows are UNCHECKED here: %v", dir, err)
	}
	var files []string
	for _, pattern := range []string{"*.yml", "*.yaml"} {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, matches...)
	}
	sort.Strings(files)
	return files
}

// runsOnProblems is what is wrong with where the jobs of one workflow run, and
// how many jobs it read. local names the workflow files in the same directory,
// which a job may call as a reusable workflow because their own jobs are held
// to this rule too; any other reusable workflow is refused.
func runsOnProblems(name string, raw []byte, local map[string]bool) ([]string, int) {
	var workflow struct {
		Jobs map[string]map[string]any `json:"jobs"`
	}
	// **Strict, so that a key is read once.** sigs.k8s.io/yaml resolves keys
	// the YAML 1.1 way - `y`, `yes` and `on` are true, `n`, `no` and `off`
	// false - and keeps the last of two equal keys, so jobs `y:` and `yes:`
	// would be one job, the second, and a self-hosted `y` would run unseen; a
	// second `runs-on:` would hide the first the same way. GitHub
	// reads keys as strings. YAMLToJSONStrict refuses a duplicate, folded or
	// written, so neither reads as anything but a refusal.
	strict, err := yaml.YAMLToJSONStrict(raw)
	if err == nil {
		err = json.Unmarshal(strict, &workflow)
	}
	if err != nil {
		return []string{fmt.Sprintf("%s cannot be read as a workflow (%v), so where its jobs run is unchecked: "+
			"a key written twice, or two that YAML 1.1 reads as one (`y`/`yes`, `n`/`no`, `on`/`off`), "+
			"hides a job - rename it", name, err)}, 0
	}
	want, other, variable := runsOn, releaseRunsOn, "RUNS_ON"
	if releaseFiles[name] {
		want, other, variable = releaseRunsOn, runsOn, "RUNS_ON_RELEASE"
	}
	ids := make([]string, 0, len(workflow.Jobs))
	for id := range workflow.Jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var problems []string
	for _, id := range ids {
		job := workflow.Jobs[id]
		where := fmt.Sprintf("%s job %q", name, id)
		got, hasRunsOn := job["runs-on"]
		if uses, ok := job["uses"]; ok {
			// A reusable workflow's jobs choose their own runners, in a file
			// this test reads only when it is one of ours, here.
			const dir = "./.github/workflows/"
			path, _ := uses.(string)
			if !strings.HasPrefix(path, dir) || !local[strings.TrimPrefix(path, dir)] {
				problems = append(problems, fmt.Sprintf("%s calls the reusable workflow %v: its jobs choose their "+
					"own runners, which this test cannot read. Call one in "+
					".github/workflows (%s<file>), whose jobs are checked here, or run the steps in a job of "+
					"this file", where, uses, dir))
			}
			if hasRunsOn {
				problems = append(problems, fmt.Sprintf("%s has both uses: and runs-on:, which GitHub refuses", where))
			}
			continue
		}
		switch {
		case !hasRunsOn:
			problems = append(problems, fmt.Sprintf("%s has no runs-on and calls no workflow, so where it runs "+
				"cannot be named", where))
		case got == want:
		case got == other && releaseFiles[name]:
			problems = append(problems, fmt.Sprintf("%s reads RUNS_ON: this workflow's steps need gh and "+
				"hfsprogs, so its jobs read RUNS_ON_RELEASE - write `runs-on: %s`", where, want))
		case got == other:
			problems = append(problems, fmt.Sprintf("%s reads RUNS_ON_RELEASE, which only the release's "+
				"workflows (release.yml, dmg.yml, appimage.yml, linux-packages.yml) may - write `runs-on: %s`",
				where, want))
		default:
			problems = append(problems, fmt.Sprintf("%s runs on %v: every job here runs where %s says, and a "+
				"fork's pull request on a GitHub-hosted runner - write `runs-on: %s`", where, got, variable, want))
		}
	}
	return problems, len(ids)
}

// TestTheRunsOnRuleRefusesEachWayRoundIt. Each numbered case is a way a job
// could get past a line-matching version of the rule, and each has to be
// refused. The shapes after them are the ones the rule allows, so that refusing
// everything does not pass.
func TestTheRunsOnRuleRefusesEachWayRoundIt(t *testing.T) {
	local := map[string]bool{"ci.yml": true}
	// The expression without its fork test: a variable naming a self-hosted
	// runner would then send a fork's code to it.
	const noForkTest = "${{ vars.RUNS_ON && fromJSON(vars.RUNS_ON) || 'ubuntu-latest' }}"
	job := func(runs string) string { return "jobs:\n  build:\n    runs-on: " + runs + "\n" }
	for _, tc := range []struct {
		name, workflow string
		refused        bool
	}{
		{"1. a custom label shaped like a hosted one", job("ubuntu-other-runner"), true},
		{"3. a quoted runs-on key", "jobs:\n  build:\n    \"runs-on\": [self-hosted, other-runner]\n", true},
		{"4. a job written as a flow mapping", "jobs: { build: { runs-on: [self-hosted, other-runner], steps: [{ run: \"true\" }] } }\n", true},
		{"5. a job calling an external reusable workflow", "jobs:\n  build:\n    uses: someone/else/.github/workflows/build.yml@main\n", true},
		{"a reusable workflow outside this directory", "jobs:\n  build:\n    uses: ./elsewhere/build.yml\n", true},
		{"a local reusable workflow that is not there", "jobs:\n  build:\n    uses: ./.github/workflows/gone.yml\n", true},
		{"a runner group", job("{ group: other-pool }"), true},
		{"another expression", job("${{ matrix.os }}"), true},
		{"the expression without its fork test", job(noForkTest), true},
		{"the expression with a trailing word", job(runsOn + " x"), true},
		{"a self-hosted label list", job("[self-hosted, linux]"), true},
		{"a hosted runner", job("ubuntu-latest"), true},
		{"no runs-on at all", "jobs:\n  build:\n    steps: []\n", true},
		// The sixth: keys YAML 1.1 folds into one, and keys written twice - the last
		// one wins, and hides the first.
		{"6. jobs y and yes, read as one", "jobs:\n  y: { runs-on: [self-hosted, other-runner], steps: [{ run: \"true\" }] }\n" +
			"  yes: { runs-on: \"" + runsOn + "\", steps: [{ run: \"true\" }] }\n", true},
		{"6. jobs off and no, read as one", "jobs:\n  off: { runs-on: [self-hosted, other-runner] }\n  no: { runs-on: \"" + runsOn + "\" }\n", true},
		{"6. runs-on written twice", "jobs:\n  build:\n    runs-on: [self-hosted, other-runner]\n    runs-on: " + runsOn + "\n", true},
		{"6. a job id written twice", job("[self-hosted, other-runner]") + "  build:\n    runs-on: " + runsOn + "\n", true},
		{"the release's expression outside the release's files", job(releaseRunsOn), true},
		{"the expression", "on: push\n" + job(runsOn), false},
		{"the expression, as a flow mapping with a quoted key", "jobs: { build: { \"runs-on\": \"" + runsOn + "\" } }\n", false},
		{"a local reusable workflow", "jobs:\n  build:\n    uses: ./.github/workflows/ci.yml\n", false},
	} {
		problems, jobs := runsOnProblems("test.yml", []byte(tc.workflow), local)
		if jobs != 1 && !strings.HasPrefix(tc.name, "6.") {
			t.Errorf("%s: read %d jobs, want 1 (%v)", tc.name, jobs, problems)
		}
		if refused := len(problems) > 0; refused != tc.refused {
			t.Errorf("%s: refused=%v, want %v (%v)", tc.name, refused, tc.refused, problems)
		}
	}

	// The release's expression: required in the release's files, refused in
	// every other file; a hosted or self-hosted literal refused in both.
	for _, tc := range []struct {
		name, file, workflow string
		refused              bool
	}{
		{"release's verify with the release's expression", "release.yml", job(releaseRunsOn), false},
		{"dmg with the release's expression", "dmg.yml", job(releaseRunsOn), false},
		{"appimage with the release's expression", "appimage.yml", job(releaseRunsOn), false},
		{"linux-packages with the release's expression", "linux-packages.yml", job(releaseRunsOn), false},
		{"release's verify with the ordinary expression", "release.yml", job(runsOn), true},
		{"dmg with the ordinary expression", "dmg.yml", job(runsOn), true},
		{"release's image on ubuntu-latest", "release.yml", job("ubuntu-latest"), true},
		{"release's draft on a self-hosted label list", "release.yml", job("[self-hosted, linux, release]"), true},
		{"the release's expression in ci.yml", "ci.yml", job(releaseRunsOn), true},
		{"the release's expression in smoke.yml", "smoke.yml", job(releaseRunsOn), true},
	} {
		problems, _ := runsOnProblems(tc.file, []byte(tc.workflow), local)
		if refused := len(problems) > 0; refused != tc.refused {
			t.Errorf("%s: refused=%v, want %v (%v)", tc.name, refused, tc.refused, problems)
		}
	}

	// 2. A workflow in a `.yaml` file: GitHub runs it, so it is read.
	dir := t.TempDir()
	for name, body := range map[string]string{
		"ok.yml":    job(runsOn),
		"evil.yaml": job("[self-hosted, other-runner]"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var refused []string
	for _, file := range workflowFiles(t, dir) {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if problems, _ := runsOnProblems(filepath.Base(file), raw, nil); len(problems) > 0 {
			refused = append(refused, filepath.Base(file))
		}
	}
	if len(refused) != 1 || refused[0] != "evil.yaml" {
		t.Errorf("2. a .yaml workflow: refused %v, want evil.yaml alone", refused)
	}
}

// steps returns a job's steps.
func steps(job map[string]any) []map[string]any {
	raw, _ := job["steps"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, s := range raw {
		m, _ := s.(map[string]any)
		out = append(out, m)
	}
	return out
}

// stepWhere is the index of a job's first step for which match holds, or -1.
func stepWhere(job map[string]any, match func(step map[string]any) bool) int {
	for i, s := range steps(job) {
		if match(s) {
			return i
		}
	}
	return -1
}

// runs matches a step whose run: script carries want.
func runs(want string) func(map[string]any) bool {
	return func(s map[string]any) bool {
		run, _ := s["run"].(string)
		return strings.Contains(run, want)
	}
}

// moves matches an upload-artifact or download-artifact step (by action) for
// the artifact named name.
func moves(action, name string) func(map[string]any) bool {
	return func(s map[string]any) bool {
		uses, _ := s["uses"].(string)
		with, _ := s["with"].(map[string]any)
		return strings.HasPrefix(uses, "actions/"+action+"@") && fmt.Sprint(with["name"]) == name
	}
}

// TestEveryBinaryThatShipsEmbedsTheConsole. A kubetower.exe built on a checkout
// where nothing built the console embeds the web/dist/.gitkeep placeholder,
// passes every other read-back, and opens on "The front end is not in this
// binary". cmd/ktbuild refuses such a build (console.go); this holds the
// workflows to giving it the console, and to opening what they ship:
//
//   - ci.yml: the web job hands the console it built to `setup` and
//     `platforms`, both of which need it and download it before cmd/ktbuild
//     runs; the setup job writes the notices into it, and after building reads
//     back the kubetower.exe inside the setup;
//   - release.yml: the archives job builds the console before the first
//     binary; the setup job reads the zip's binary back for the console before
//     wrapping it, and the setup's own payload after.
func TestEveryBinaryThatShipsEmbedsTheConsole(t *testing.T) {
	const console = "console-${{ github.run_number }}"
	ci := workflowJobs(t, "ci.yml")

	web := ci["web"]
	build := stepWhere(web, func(s map[string]any) bool { return s["id"] == "build" && s["run"] == "pnpm run build" })
	upload := stepWhere(web, moves("upload-artifact", console))
	switch {
	case build < 0:
		t.Error("ci.yml's web job has no `pnpm run build` step with id build, which the hand-off is gated on")
	case upload < 0:
		t.Errorf("ci.yml's web job does not upload %s", console)
	case upload < build:
		t.Error("ci.yml's web job uploads the console before it builds it")
	default:
		with := steps(web)[upload]["with"].(map[string]any)
		path := fmt.Sprint(with["path"])
		for _, want := range []string{"web/dist/ui", "web/build/third-party-npm.json"} {
			if !strings.Contains(path, want) {
				t.Errorf("ci.yml's web job hands over %q, without %s", path, want)
			}
		}
		if cond := fmt.Sprint(steps(web)[upload]["if"]); !strings.Contains(cond, "steps.build.outcome == 'success'") ||
			!strings.Contains(cond, "needs.detect.outputs.full == 'yes'") {
			t.Errorf("ci.yml's web job hands the console over if %q: not on a build that worked, or not on the full "+
				"tier, whose setup and platforms take it", cond)
		}
		// A missing bundle fails here, by name, rather than as a download
		// error two jobs later.
		if got := fmt.Sprint(with["if-no-files-found"]); got != "error" {
			t.Errorf("ci.yml's web job hands the console over with if-no-files-found: %s, not error", got)
		}
		// As long as the setup it feeds: release.yml's verify answers a failed
		// full tier with `gh run rerun <id> --failed`, which does not re-run a
		// web job that passed, so a shorter-lived console breaks that advice.
		keep := fmt.Sprint(with["retention-days"])
		setupUpload := stepWhere(ci["setup"], moves("upload-artifact", "kubetower-setup-0.0.0-ci.${{ github.run_number }}"))
		if setupUpload < 0 {
			t.Error("ci.yml's setup job no longer uploads kubetower-setup-0.0.0-ci.<run>, which this compares against")
		} else if want := fmt.Sprint(steps(ci["setup"])[setupUpload]["with"].(map[string]any)["retention-days"]); keep != want {
			t.Errorf("ci.yml keeps the console %s days and the setup %s: a `gh run rerun --failed` of setup or "+
				"platforms after the console expires finds nothing to download", keep, want)
		}
	}

	for job, ktbuild := range map[string]string{
		"setup":     "go run ./cmd/ktbuild -os windows -arch amd64 -setup ",
		"platforms": "go run ./cmd/ktbuild -os ",
	} {
		j, ok := ci[job]
		if !ok {
			t.Errorf("ci.yml has no %s job", job)
			continue
		}
		if !slices.Contains(needs(j), "web") {
			t.Errorf("ci.yml's %s job does not need web, so the console may not exist yet: %v", job, needs(j))
		}
		down := stepWhere(j, func(s map[string]any) bool {
			with, _ := s["with"].(map[string]any)
			return moves("download-artifact", console)(s) && fmt.Sprint(with["path"]) == "web"
		})
		link := stepWhere(j, runs(ktbuild))
		switch {
		case down < 0:
			t.Errorf("ci.yml's %s job does not download %s into web/", job, console)
		case link < 0:
			t.Errorf("ci.yml's %s job no longer runs %q", job, ktbuild)
		case link < down:
			t.Errorf("ci.yml's %s job runs cmd/ktbuild before it has the console", job)
		}
	}

	// platforms needs web for the console, and not a green web: `!cancelled()`
	// keeps a lint failure there from skipping every target, and `needs.go.result`
	// keeps the success check on go that `!cancelled()` would otherwise drop.
	// setup's own `if` is pinned by TestTheSetupJobIsNotOnEveryPullRequest.
	const platformsIf = "${{ !cancelled() && needs.go.result == 'success' && " +
		"needs.detect.outputs.go == 'yes' && needs.detect.outputs.full == 'yes' }}"
	if got := fmt.Sprint(ci["platforms"]["if"]); got != platformsIf {
		t.Errorf("ci.yml's platforms job runs if %q, not %q", got, platformsIf)
	}

	setup := ci["setup"]
	down := stepWhere(setup, moves("download-artifact", console))
	notices := stepWhere(setup, runs("go run ./cmd/ktnotices -npm web/build/third-party-npm.json -o web/dist/ui/THIRD_PARTY_NOTICES.txt"))
	link := stepWhere(setup, runs("go run ./cmd/ktbuild -os windows -arch amd64 -setup "))
	payload := stepWhere(setup, runs(`go run ./cmd/ktbuild -check-console "$exe"`))
	switch {
	case notices < 0:
		t.Error("ci.yml's setup job does not write THIRD_PARTY_NOTICES.txt into the console")
	case !(down < notices && notices < link):
		t.Error("ci.yml's setup job writes the notices outside the window between the download and the build")
	case payload < 0:
		t.Error("ci.yml's setup job does not read the setup's own kubetower.exe back for the console")
	case payload < link:
		t.Error("ci.yml's setup job checks the setup's payload before it has built the setup")
	}

	release := workflowJobs(t, "release.yml")
	archives := release["archives"]
	spa := stepWhere(archives, runs("pnpm -C web run build\n"))
	index := stepWhere(archives, runs("test -f web/dist/ui/index.html"))
	first := stepWhere(archives, runs("go run ./cmd/ktbuild "))
	switch {
	case spa < 0 || index < 0:
		t.Error("release.yml's archives job does not build the console and prove index.html exists")
	case first < 0:
		t.Error("release.yml's archives job no longer builds through cmd/ktbuild")
	case !(spa < first && index < first):
		t.Error("release.yml's archives job builds a binary before the console exists")
	}

	text := jobText(release["setup"])
	stage := strings.Index(text, "go run ./cmd/ktbuild -check-console stage/kubetower.exe\n")
	wrap := strings.Index(text, "go run ./cmd/ktbuild -os windows -arch amd64 -setup -stage ")
	inside := strings.Index(text, `go run ./cmd/ktbuild -check-console "$exe"`)
	switch {
	case stage < 0:
		t.Error("release.yml's setup job does not read the zip's kubetower.exe back for the console")
	case wrap < 0:
		t.Error("release.yml's setup job no longer wraps the zip's binary with -stage")
	case stage > wrap:
		t.Error("release.yml's setup job checks the zip's binary for the console after wrapping it")
	case inside < 0:
		t.Error("release.yml's setup job does not read the setup's own kubetower.exe back for the console")
	case inside < wrap:
		t.Error("release.yml's setup job checks the setup's payload before it has built the setup")
	}
	for _, tool := range []string{"7z", "find", "grep", "awk"} {
		if !regexp.MustCompile(`TOOLS: .*\b` + tool + `\b`).MatchString(text) {
			t.Errorf("release.yml's setup job calls %s and its tool check does not list it", tool)
		}
	}

	// The listing of the setup is an assertion, name by name, in both
	// workflows: a grep for any one of the names would pass a setup that
	// lost the others. Each name is matched as a whole
	// last field of `7z l`, not as a substring.
	const each = "for f in kubetower.exe LICENSE LICENSING.md THIRD_PARTY_NOTICES.txt; do\n" +
		"  awk '{print $NF}' <<< \"$list\" | grep -qxF \"$f\" || {"
	for file, job := range map[string]map[string]any{"ci.yml": ci["setup"], "release.yml": release["setup"]} {
		text := jobText(job)
		listed := strings.Index(text, `list=$(7z l "`)
		asserted := strings.Index(text, each)
		opened := strings.Index(text, `go run ./cmd/ktbuild -check-console "$exe"`)
		switch {
		case listed < 0 || asserted < 0:
			t.Errorf("%s's setup job does not assert that the setup installs each of its five files", file)
		case !(listed < asserted && asserted < opened):
			t.Errorf("%s's setup job asserts the listing out of order with the payload check", file)
		}
	}
}

// TestTheSmokeJobKeepsItsPasswordToItself. smoke.yml draws a password for the
// run, starts the built binary with its hash, and signs a browser in against a
// kind cluster. What it must hold is held here, because a workflow is the one
// file no other test reads:
//
//   - a runner that may be self-hosted, and so a run that shares its VM: a kind
//     cluster named for the run and deleted by that name, a console port
//     drawn for the run rather than fixed; kind checked against its checksum;
//   - the binary built in `task build`'s order, so the SPA is embedded;
//   - the password masked before anything else touches it, exported by the e2e
//     step alone, never put in an `env:` block, which the log prints, and never
//     passed on through GITHUB_ENV or GITHUB_OUTPUT under any name;
//   - no trace recorded, because a trace holds the session cookie;
//   - nothing uploaded unless the search for the password and the session has
//     passed, and nothing uploaded that the search was not given;
//   - a search whose exit status is its finding, with no way round it;
//   - the kind cluster deleted whatever happened.
//
// TestTheSmokeRuleRefusesEachWayRoundIt holds this to a set of mutations.
func TestTheSmokeJobKeepsItsPasswordToItself(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "smoke.yml"))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("smoke.yml is not in this tree (the image's build context excludes .github), so it is UNCHECKED here: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range smokeProblems(raw) {
		t.Error(problem)
	}
}

// smokeProblems is what is wrong with smoke.yml, by the rules above.
func smokeProblems(raw []byte) []string {
	var doc struct {
		Jobs map[string]map[string]any `json:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return []string{fmt.Sprintf("smoke.yml: %v", err)}
	}
	job, ok := doc.Jobs["smoke"]
	if !ok {
		return []string{"smoke.yml has no smoke job"}
	}
	var problems []string
	fail := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if got := fmt.Sprint(job["runs-on"]); got != runsOn {
		fail("smoke.yml's job runs on %s, not where RUNS_ON says (%s)", got, runsOn)
	}
	jobEnv, _ := job["env"].(map[string]any)
	// The cluster is the run's own: two runs on one VM must never name the
	// same one, and the teardown deletes it by this name.
	if name := fmt.Sprint(jobEnv["KIND_CLUSTER"]); !strings.Contains(name, "${{ github.run_id }}") ||
		!strings.Contains(name, "${{ github.run_attempt }}") {
		fail("smoke.yml's KIND_CLUSTER (%s) is not named for the run and its attempt", name)
	}
	if jobEnv["KT_E2E_PASSWORD"] != nil {
		fail("smoke.yml sets KT_E2E_PASSWORD for the whole job")
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(fmt.Sprint(jobEnv["KIND_SHA256"])) {
		fail("smoke.yml's KIND_SHA256 is not a sha256: %v", jobEnv["KIND_SHA256"])
	}

	// Which steps hold a secret: they name it, read the file it is kept in, or
	// the file the session values are kept in.
	holds := regexp.MustCompile(`\$word\b|KT_E2E_PASSWORD|\$SMOKE/password|session-values`)
	passesOn := regexp.MustCompile(`GITHUB_ENV|GITHUB_OUTPUT`)

	steps, _ := job["steps"].([]any)
	var holders, scanRoots []string
	var uploadPaths []string
	drawn, checked, untraced, scanned, uploadGated, teardown := false, false, false, false, false, false
	for _, step := range steps {
		m, _ := step.(map[string]any)
		run, _ := m["run"].(string)
		name := fmt.Sprint(m["name"])
		when := fmt.Sprint(m["if"])
		env, _ := m["env"].(map[string]any)
		if env["KT_E2E_PASSWORD"] != nil {
			fail("smoke.yml's step %q puts KT_E2E_PASSWORD in an env: block, which the log prints", name)
		}
		if strings.Contains(run, "KT_E2E_PASSWORD") {
			holders = append(holders, fmt.Sprint(m["id"]))
		}
		if holds.MatchString(run) && passesOn.MatchString(run) {
			fail("smoke.yml's step %q holds the password or the session and writes GITHUB_ENV or "+
				"GITHUB_OUTPUT, which would hand it to every later step", name)
		}

		if draw := strings.Index(run, "word=$(openssl rand"); draw >= 0 {
			drawn = true
			const mask = `echo "::add-mask::$word"`
			at := strings.Index(run, mask)
			first := strings.Index(run[draw+len("word="):], "$word")
			if at < 0 || first < 0 || draw+len("word=")+first != at+len(`echo "::add-mask::`) {
				fail("smoke.yml does something with the password before `%s`, or never masks it", mask)
			}
		}
		if strings.Contains(run, "sha256sum --check --strict") && strings.Contains(run, "KIND_SHA256") {
			fetch, check, place := strings.Index(run, "curl "), strings.Index(run, "sha256sum --check"), strings.Index(run, "install ")
			checked = fetch >= 0 && fetch < check && check < place
		}
		if strings.Contains(run, "go run ./cmd/ktbuild") {
			web, notices, api := strings.Index(run, "pnpm build"), strings.Index(run, "./cmd/ktnotices"),
				strings.Index(run, "./cmd/ktbuild")
			if web < 0 || notices < 0 || !(web < notices && notices < api) {
				fail("smoke.yml does not build in `task build`'s order (build:web, build:notices, build:api)")
			}
		}
		if m["id"] == "e2e" {
			untraced = fmt.Sprint(env["KT_E2E_TRACE"]) == "off" && env["KT_E2E_SESSION_VALUES"] != nil
			// The path's status is kept across the line that tidies its log, and
			// is the step's: a red path must not read green.
			if !strings.Contains(run, `| tee "$SMOKE/e2e.log" || status=$?`) ||
				!strings.HasSuffix(strings.TrimSpace(run), `exit "$status"`) {
				fail("smoke.yml's e2e step does not end on the browser path's own exit status")
			}
		}
		if m["id"] == "scan" {
			scanned = true
			scanRoots = scanArguments(run)
			if when != "always()" {
				fail("smoke.yml's scan runs if %q, not always()", when)
			}
			if m["continue-on-error"] != nil {
				fail("smoke.yml's scan has continue-on-error, so a hit would not fail the job")
			}
			if !strings.HasPrefix(run, "set -eu\n") {
				fail("smoke.yml's scan does not start with `set -eu`")
			}
			if n := strings.Count(run, "sys.exit("); n != 1 || !strings.Contains(run, "sys.exit(1 if found else 0)") {
				fail("smoke.yml's scan exits other than by `sys.exit(1 if found else 0)` (%d exits)", n)
			}
			if n := strings.Count(run, "exit 0"); n != 1 ||
				!strings.Contains(run, `[ -s "$SMOKE/password" ] || { echo "no password was drawn, so there is nothing to search for"; exit 0; }`) {
				fail("smoke.yml's scan has an `exit 0` other than the one for a run that drew no password")
			}
			if strings.Contains(run, "|| true") || strings.Contains(run, "set +e") {
				fail("smoke.yml's scan swallows its own exit status")
			}
			for _, want := range []string{`"$SMOKE/password"`, `"$SMOKE/session-values"`} {
				if !strings.Contains(run, want) {
					fail("smoke.yml's scan is not given %s", want)
				}
			}
		}
		if strings.HasPrefix(fmt.Sprint(m["uses"]), "actions/upload-artifact@") {
			uploadGated = strings.Contains(when, "steps.scan.outcome == 'success'")
			with, _ := m["with"].(map[string]any)
			for _, line := range strings.Split(fmt.Sprint(with["path"]), "\n") {
				if line = strings.TrimSpace(line); line != "" {
					uploadPaths = append(uploadPaths, line)
				}
			}
		}
		if when == "always()" && strings.Contains(run, `kind delete cluster --name "$KIND_CLUSTER"`) {
			teardown = true
		}
		if strings.Contains(run, "kind create cluster") && !strings.Contains(run, `kind create cluster --name "$KIND_CLUSTER"`) {
			fail("smoke.yml's step %q creates a kind cluster under a name that is not this run's", name)
		}
		if fixedPort.MatchString(run) {
			fail("smoke.yml's step %q starts the console on a fixed port, which a second run on the VM would hold", name)
		}
	}
	if len(holders) != 1 || holders[0] != "e2e" {
		fail("KT_E2E_PASSWORD is handled by the steps %v; want the e2e step alone", holders)
	}
	if !drawn {
		fail("smoke.yml draws no password with `word=$(openssl rand`")
	}
	if !checked {
		fail("smoke.yml does not check kind against KIND_SHA256 between downloading and installing it")
	}
	if !untraced {
		fail("smoke.yml's e2e step does not set KT_E2E_TRACE=off and KT_E2E_SESSION_VALUES: a trace holds the session")
	}
	if !scanned {
		fail("smoke.yml has no step with id scan")
	}
	if !uploadGated {
		fail("smoke.yml uploads an artifact without waiting for the search to pass")
	}
	for _, path := range uploadPaths {
		if !withinAny(path, scanRoots) {
			fail("smoke.yml uploads %q, which the search (%v) was not given", path, scanRoots)
		}
	}
	if !teardown {
		fail("smoke.yml does not delete this run's kind cluster ($KIND_CLUSTER) in an always() step")
	}
	return problems
}

// fixedPort is a console started on a port written into the workflow: given
// to KT_LISTEN on any host spelling, or to the CONSOLE it would be derived from.
var fixedPort = regexp.MustCompile(`(KT_LISTEN|CONSOLE)=["']?[^"'\s$]*:[0-9]+`)

// scanArguments is what the scan's `python3 -` is given, unquoted, less the
// first two (the password and the session values, which are what it looks for).
func scanArguments(run string) []string {
	for _, line := range strings.Split(run, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "python3 - ") {
			continue
		}
		fields := strings.Fields(strings.TrimSuffix(strings.TrimPrefix(line, "python3 - "), "<<'PY'"))
		var out []string
		for _, f := range fields {
			out = append(out, strings.Trim(f, `"`))
		}
		if len(out) > 2 {
			return out[2:]
		}
	}
	return nil
}

func withinAny(path string, roots []string) bool {
	path = strings.TrimSuffix(path, "/")
	for _, root := range roots {
		root = strings.TrimSuffix(root, "/")
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

// TestTheSmokeRuleRefusesEachWayRoundIt. Thirteen mutations of smoke.yml, each
// applied here to the file as it stands, and
// each has to be refused. The file itself has to pass, so that refusing
// everything does not.
func TestTheSmokeRuleRefusesEachWayRoundIt(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "smoke.yml"))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("smoke.yml is not in this tree (the image's build context excludes .github): %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if problems := smokeProblems(raw); len(problems) > 0 {
		t.Fatalf("smoke.yml itself is refused: %v", problems)
	}
	const mask = "          echo \"::add-mask::$word\"\n"
	const devpassword = "          printf '%s' \"$word\" > \"$SMOKE/password\"\n" +
		"          printf \"KT_STATE_DIR='%s'\\n\" \"$SMOKE/state\" > \"$SMOKE/console.env\"\n" +
		"          printf '%s\\n' \"$word\" | go run ./cmd/devpassword -env \"$SMOKE/console.env\"\n"
	for _, tc := range []struct{ name, old, new string }{
		{"1. the mask moved after devpassword", mask + devpassword, devpassword + mask},
		{"2. KT_E2E_PASSWORD in the job's env", "    env:\n      # This run's own cluster", "    env:\n      KT_E2E_PASSWORD: x\n      # This run's own cluster"},
		{"3. KT_E2E_PASSWORD through GITHUB_ENV", mask, mask + "          echo \"KT_E2E_PASSWORD=$word\" >> \"$GITHUB_ENV\"\n"},
		{"4. the upload not waiting for the search", "if: failure() && steps.scan.outcome == 'success'", "if: failure()"},
		{"5. a teardown with no if", "        if: always()\n        env:\n          SCAN:", "        env:\n          SCAN:"},
		{"6. a teardown skipped on cancel", "        if: always()\n        env:\n          SCAN:", "        if: success() || failure()\n        env:\n          SCAN:"},
		{"7. the build without pnpm build", "(cd web && pnpm build)", "true"},
		{"8. another runner", "runs-on: " + runsOn, "runs-on: ubuntu-latest"},
		{"9. the password echoed before the mask", mask, "          echo \"drew $word\"\n" + mask},
		{"10. the password through GITHUB_ENV under another name", mask, mask + "          echo \"PW=$word\" >> \"$GITHUB_ENV\"\n"},
		{"11. the search forced to pass", "sys.exit(1 if found else 0)", "sys.exit(0)"},
		{"12. an upload wider than the search", "path: web/test-results/", "path: web/"},
		{"13. no checksum on kind", "          echo \"${KIND_SHA256}  $RUNNER_TEMP/kind\" | sha256sum --check --strict\n", ""},
		// And the ways round the other rules, beside those thirteen.
		{"traces recorded again", "          KT_E2E_TRACE: 'off'\n", ""},
		{"the search's status swallowed", "<<'PY'\n", "<<'PY' || true\n"},
		{"the search not run on a failure", "        id: scan\n        if: always()", "        id: scan"},
		{"the browser path's status dropped", `exit "$status"`, "exit 0"},
		{"the session values not searched", `"$SMOKE/session-values" web/test-results`, `web/test-results`},
		// And the ways round sharing the pool's VM.
		{"one cluster name for every run", "KIND_CLUSTER: kt-smoke-${{ github.run_id }}-${{ github.run_attempt }}", "KIND_CLUSTER: kind"},
		{"a cluster named without the attempt", "KIND_CLUSTER: kt-smoke-${{ github.run_id }}-${{ github.run_attempt }}", "KIND_CLUSTER: kt-smoke-${{ github.run_id }}"},
		{"the cluster created under a fixed name", `kind create cluster --name "$KIND_CLUSTER"`, "kind create cluster --name kind"},
		{"the teardown deleting another cluster", `kind delete cluster --name "$KIND_CLUSTER" --kubeconfig`, "kind delete cluster --name kind --kubeconfig"},
		{"the console on a fixed port", `KT_LISTEN="127.0.0.1:$port"`, "KT_LISTEN=127.0.0.1:5823"},
		{"the console on a fixed port, another spelling", `KT_LISTEN="127.0.0.1:$port"`, "KT_LISTEN=:5823"},
		{"the console's address fixed", `CONSOLE="http://127.0.0.1:$port"`, `CONSOLE="http://localhost:5823"`},
	} {
		if strings.Count(text, tc.old) != 1 {
			t.Errorf("%s: %q is not in smoke.yml exactly once, so the mutation was not applied", tc.name, tc.old)
			continue
		}
		mutated := strings.Replace(text, tc.old, tc.new, 1)
		problems := smokeProblems([]byte(mutated))
		if len(problems) == 0 {
			t.Errorf("%s: not refused", tc.name)
			continue
		}
		t.Logf("%s: refused - %s", tc.name, problems[0])
	}
}

// TestTheCIImageIsTheRunsOwn. The pool's runners share one docker daemon, so
// a fixed image name like `kubetower:ci` is every job's to remove or overwrite,
// and a run can lose its own image minutes after loading it. So ci.yml's image
// job:
//
//   - names its image and its container after the run and the attempt, in the
//     job's env, and builds under that name;
//   - refers to them by those variables in every step, and to no fixed
//     `kubetower:ci` or `kt-ci` name, in a script, an env (the workflow's, the
//     job's or a step's) or a with: value;
//   - runs the image with --pull never, so a missing image is never looked
//     for on Docker Hub;
//   - removes both itself, in a last step that runs whatever happened, and
//     whose removal is a command rather than a comment;
//
// and no workflow script prunes the daemon or removes an image, a container,
// a volume or a network its own job did not name (dockerRemovalProblems).
// TestTheCIImageRuleRefusesEachWayRoundIt holds the ways round it.
func TestTheCIImageIsTheRunsOwn(t *testing.T) {
	ci := false
	for _, file := range workflowFiles(t, filepath.Join("..", "..", ".github", "workflows")) {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(file)
		ci = ci || name == "ci.yml"
		for _, problem := range ciImageFileProblems(name, raw) {
			t.Errorf("%s: %s", name, problem)
		}
	}
	if !ci {
		t.Error("there is no ci.yml among the workflows, so its image job is unchecked")
	}
}

// workflowDoc is the part of a workflow these rules read: its env and its jobs.
type workflowDoc struct {
	Env  map[string]any            `json:"env"`
	Jobs map[string]map[string]any `json:"jobs"`
}

// ciImageFileProblems is what TestTheCIImageIsTheRunsOwn refuses in one
// workflow file: ci.yml's image job against ciImageProblems, and in every
// file, each job's docker removals against dockerRemovalProblems.
func ciImageFileProblems(file string, raw []byte) []string {
	var doc workflowDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return []string{err.Error()}
	}
	var out []string
	if file == "ci.yml" {
		out = append(out, ciImageProblems(doc)...)
	}
	for _, name := range slices.Sorted(maps.Keys(doc.Jobs)) {
		job := doc.Jobs[name]
		for _, problem := range dockerRemovalProblems(ownNames[file+" "+name], dockerBinaries(doc.Env, job), scripts(job)) {
			out = append(out, fmt.Sprintf("job %s: %s", name, problem))
		}
	}
	return out
}

// ownNames are the only arguments a workflow may hand a docker removal, by
// workflow file and job: each names something that job created, under a name
// no other run shares, so the same variable set to a fixed name in another job
// or another workflow is refused. KT_CI_IMAGE and KT_CI_CONTAINER are ci.yml's
// image job's (run id and attempt, held by ciImageProblems), KT_CI_VALKEY_NAME
// ci.yml's Go job's (run id, attempt and runner), and REF the image
// release.yml's image job built and pushed, under a name no other run shares.
// A new one is added here, under its job, with its reason.
var ownNames = map[string]map[string]bool{
	"ci.yml image":      {`"$KT_CI_IMAGE"`: true, `"$KT_CI_CONTAINER"`: true},
	"ci.yml go":         {`"$KT_CI_VALKEY_NAME"`: true},
	"release.yml image": {`"$REF"`: true},
}

var (
	// dockerCommand is where a docker command can start: the binary, by name
	// or by path, or a variable that may hold it ($D, ${D}, "${D[@]}").
	dockerCommand = regexp.MustCompile(`\bdocker\s+|\$\{?([A-Za-z_][A-Za-z0-9_]*)(?:\[[@*]\])?\}?"?\s+`)
	// dockerValueOptions are docker's global options that take a value, so
	// that `docker -H host rmi x` is read past the host to the subcommand.
	dockerValueOptions = map[string]bool{
		"-H": true, "--host": true, "-c": true, "--context": true, "--config": true,
		"-l": true, "--log-level": true, "--tlscacert": true, "--tlscert": true, "--tlskey": true,
	}
	// dockerObjects are removed by `docker <object> rm` or `remove`: the
	// daemon's own objects, a buildx builder, and a context, a plugin and the
	// swarm's services, stacks and secrets.
	dockerObjects = map[string]bool{
		"image": true, "container": true, "volume": true, "network": true,
		"buildx": true, "context": true, "plugin": true, "service": true, "stack": true, "secret": true,
	}
	// composeValueOptions are `docker compose`'s options that take a value, so
	// that `docker compose -p name down` is read past the name to `down`.
	composeValueOptions = map[string]bool{
		"-f": true, "--file": true, "-p": true, "--project-name": true, "--project-directory": true,
		"--env-file": true, "--profile": true, "--ansi": true, "--progress": true, "--parallel": true,
	}
	// removalValueOptions are a removal's options that take a value, so that
	// the value is not read as what it removes: `compose down --rmi all`,
	// `--timeout 5`, `buildx rm --builder b`.
	removalValueOptions = map[string]bool{"--rmi": true, "-t": true, "--timeout": true, "--builder": true}
	// redirection is a word that opens a shell redirection: `>`, `2>&1`,
	// `&>`, `>>`, `<`, a here-document. Its target is the rest of the word, or
	// the next word when the rest is empty, and neither is an argument.
	redirection = regexp.MustCompile(`^(?:[0-9]+|&)?(?:<<<|<<|<>|<&|<|>>|>&|>\||>)`)
	// dockerOnly are subcommands no other tool takes first, so a variable
	// followed by one is docker, whatever it was set to.
	dockerOnly = map[string]bool{
		"rmi": true, "image": true, "container": true, "volume": true, "network": true,
		"system": true, "builder": true, "buildx": true,
	}
	// assignment is `NAME=value` in a script, and holdsDocker a value that is
	// the docker binary: `docker`, a path to it, `$(command -v docker)`, or an
	// array or a string that starts with it.
	assignment  = regexp.MustCompile(`(?m)(?:^|[\s;&|(])([A-Za-z_][A-Za-z0-9_]*)=([^;&|\n]*)`)
	holdsDocker = regexp.MustCompile(`^["'(]*(?:\$\((?:command\s+-v|which|type\s+-P)\s+)?(?:[\w./-]*/)?docker(?:$|[\s"')])`)
)

// dockerBinaries are the variables a job sets to the docker binary, in the
// workflow's, the job's or a step's env:, or by assignment in a script, so
// that `$D rm x` is read as the `docker rm x` it is.
func dockerBinaries(workflowEnv map[string]any, job map[string]any) map[string]bool {
	out := map[string]bool{}
	envs := []any{workflowEnv, job["env"]}
	for _, s := range steps(job) {
		envs = append(envs, s["env"])
	}
	for _, env := range envs {
		m, _ := env.(map[string]any)
		for name, value := range m {
			if holdsDocker.MatchString(fmt.Sprint(value)) {
				out[name] = true
			}
		}
	}
	for _, script := range scripts(job) {
		for _, m := range assignment.FindAllStringSubmatch(script, -1) {
			if holdsDocker.MatchString(strings.TrimSpace(m[2])) {
				out[m[1]] = true
			}
		}
	}
	return out
}

// dockerRemovalProblems is every prune, and every removal whose argument is
// not one of own, in the given scripts (as scripts returns them, with
// comments dropped). A docker command is read past its global options, under
// the binary's name or a variable holding it (binaries), with its
// redirections and their targets taken out; a removal is `rm`, `rmi`, `rm`
// and `remove` on one of dockerObjects, and `compose down` or `compose rm`. A
// removal with no argument at all is the `| xargs docker rm` form, or a
// compose project's every object, and is refused with the rest.
func dockerRemovalProblems(own, binaries map[string]bool, scripts []string) []string {
	bare := func(word string) string { return strings.Trim(word, `"'`) }
	var out []string
	for _, script := range scripts {
		for _, line := range strings.Split(script, "\n") {
			for _, loc := range dockerCommand.FindAllStringSubmatchIndex(line, -1) {
				rest := line[loc[1]:commandEnd(line, loc[1])]
				words := withoutRedirections(strings.Fields(rest))
				command := words[skipOptions(words, dockerValueOptions):]
				first := func(n int) string {
					if n < len(command) {
						return bare(command[n])
					}
					return ""
				}
				if loc[2] >= 0 {
					name := line[loc[2]:loc[3]]
					if !binaries[name] && !strings.EqualFold(name, "docker") && !dockerOnly[first(0)] {
						continue
					}
				}
				what := strings.TrimSpace(line[loc[0]:loc[1]] + rest)
				if slices.ContainsFunc(command, func(w string) bool { return bare(w) == "prune" }) {
					out = append(out, fmt.Sprintf("%q prunes the daemon the pool shares, taking other runs' images", what))
					continue
				}
				var args []string
				switch {
				case first(0) == "rm" || first(0) == "rmi":
					args = command[1:]
				case dockerObjects[first(0)] && (first(1) == "rm" || first(1) == "remove"):
					args = command[2:]
				case first(0) == "compose":
					sub := command[1+skipOptions(command[1:], composeValueOptions):]
					if len(sub) == 0 || (bare(sub[0]) != "down" && bare(sub[0]) != "rm") {
						continue
					}
					args = sub[1:]
				default:
					continue
				}
				targets := 0
				bad := ""
				for i := 0; i < len(args); i++ {
					arg := args[i]
					if strings.HasPrefix(arg, "-") {
						option, _, inline := strings.Cut(bare(arg), "=")
						if !inline && removalValueOptions[option] {
							i++
						}
						continue
					}
					targets++
					if !own[arg] {
						bad = arg
						break
					}
				}
				if bad != "" || targets == 0 {
					out = append(out, fmt.Sprintf("%q removes what this job did not name itself (%s)", what, cmp.Or(bad, "no argument")))
				}
			}
		}
	}
	return out
}

// commandEnd is where the command that starts at line[start:] ends: at the
// first |, ;, &, ) or backquote, except the & of a redirection (`2>&1`,
// `&>`), which joins a file descriptor and ends nothing. Quotes are not read,
// so that a removal inside `sh -c '...'` is still a removal.
func commandEnd(line string, start int) int {
	for i := start; i < len(line); i++ {
		switch line[i] {
		case '|', ';', ')', '`':
			return i
		case '&':
			if (i > 0 && (line[i-1] == '>' || line[i-1] == '<')) || (i+1 < len(line) && line[i+1] == '>') {
				continue
			}
			return i
		}
	}
	return len(line)
}

// withoutRedirections is a command's words without its redirections and
// their targets, wherever they stand: the shell takes them out before the
// command sees its arguments.
func withoutRedirections(words []string) []string {
	var out []string
	for i := 0; i < len(words); i++ {
		if m := redirection.FindString(words[i]); m != "" {
			if m == words[i] {
				i++ // the target is the next word
			}
			continue
		}
		out = append(out, words[i])
	}
	return out
}

// skipOptions is how many of words are options, read with valueOptions taking
// the word after them unless written --option=value.
func skipOptions(words []string, valueOptions map[string]bool) int {
	i := 0
	for i < len(words) && strings.HasPrefix(strings.Trim(words[i], `"'`), "-") {
		option, _, inline := strings.Cut(strings.Trim(words[i], `"'`), "=")
		i++
		if !inline && valueOptions[option] {
			i++
		}
	}
	return min(i, len(words))
}

// runsCommand is whether script runs want as a command of its own: at the
// start of a line or after an operator, outside quotes, followed by a blank,
// a redirection or nothing. `echo 'docker image rm x'` runs echo.
func runsCommand(script, want string) bool {
	for _, cmd := range commands(script) {
		if rest, ok := strings.CutPrefix(cmd, want); ok && (rest == "" || strings.ContainsRune(" \t<>", rune(rest[0]))) {
			return true
		}
	}
	return false
}

// commands splits a script into its simple commands, trimmed: at a newline
// or an unquoted ;, |, &, ( or ), with quotes and backslashes read as
// stripComment reads them, and the & of a redirection left where it is.
func commands(script string) []string {
	var out []string
	var quote rune
	escaped := false
	start := 0
	for i, r := range script {
		switch {
		case escaped:
			escaped = false
		case r == '\\' && quote != '\'':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
		case r == '&' && ((i > 0 && (script[i-1] == '>' || script[i-1] == '<')) || (i+1 < len(script) && script[i+1] == '>')):
		case strings.ContainsRune("\n;|&()", r):
			out = append(out, strings.TrimSpace(script[start:i]))
			start = i + 1
		}
	}
	out = append(out, strings.TrimSpace(script[start:]))
	return slices.DeleteFunc(out, func(s string) bool { return s == "" })
}

// scripts is a job's run: scripts, each as script reads it.
func scripts(job map[string]any) []string {
	var out []string
	for _, s := range steps(job) {
		if run, ok := s["run"].(string); ok {
			out = append(out, script(run))
		}
	}
	return out
}

// script is one run: script as the shell reads it: comments dropped and
// backslash-continued lines joined, so a word in a comment is not a command
// and a command split over lines is read whole.
func script(run string) string {
	var b strings.Builder
	for _, line := range strings.Split(run, "\n") {
		line = strings.TrimRight(stripComment(line), " \t")
		if cont, ok := strings.CutSuffix(line, `\`); ok {
			b.WriteString(cont + " ")
			continue
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// stripComment drops a shell comment from one line: a # that starts a word -
// at the start of the line, or after a blank or an operator - outside quotes
// and not escaped. A backslash escapes the next character outside quotes and
// inside double quotes, never inside single quotes.
func stripComment(line string) string {
	var quote rune
	escaped := false
	prev := ' '
	for i, r := range line {
		switch {
		case escaped:
			escaped = false
			prev = 'x' // an escaped character is part of a word, whatever it is
			continue
		case r == '\\' && quote != '\'':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
		case r == '#' && strings.ContainsRune(" \t;&|()<>", prev):
			return line[:i]
		}
		prev = r
	}
	return line
}

func TestStripCommentReadsTheShellsQuoting(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{`# all of it`, ``},
		{`docker rmi x # why`, `docker rmi x `},
		{`true;# docker rmi x`, `true;`},
		{`true&&# docker rmi x`, `true&&`},
		{`(# docker rmi x`, `(`},
		{`echo "#"; docker rmi x`, `echo "#"; docker rmi x`},
		{`echo '#'; docker rmi x`, `echo '#'; docker rmi x`},
		{`echo "\" # "; docker rmi x`, `echo "\" # "; docker rmi x`},
		{`echo \"# x`, `echo \"# x`},
		{`echo \# docker rmi x`, `echo \# docker rmi x`},
		{`echo a\ #b`, `echo a\ #b`},
		{`echo '\' # c`, `echo '\' `},
		{`echo "\\" # c`, `echo "\\" `},
		{`echo $# ${#x}`, `echo $# ${#x}`},
	} {
		if got := stripComment(tc.line); got != tc.want {
			t.Errorf("stripComment(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

// ciImageProblems is what is wrong with ci.yml's image job against
// TestTheCIImageIsTheRunsOwn.
func ciImageProblems(doc workflowDoc) []string {
	job, ok := doc.Jobs["image"]
	if !ok {
		return []string{"ci.yml has no image job"}
	}
	var out []string
	env, _ := job["env"].(map[string]any)
	for name, prefix := range map[string]string{"KT_CI_IMAGE": "kubetower:ci-", "KT_CI_CONTAINER": "kt-ci-"} {
		v := fmt.Sprint(env[name])
		if !strings.HasPrefix(v, prefix) || !strings.Contains(v, "${{ github.run_id }}") || !strings.Contains(v, "${{ github.run_attempt }}") {
			out = append(out, fmt.Sprintf("the image job's env %s is %q, not %s<run id>-<attempt>", name, v, prefix))
		}
	}
	// Everything the job hands a command: its scripts without their comments,
	// the workflow's and the job's env: values, and every env: and with: value
	// of its steps.
	run := scripts(job)
	handed := slices.Clone(run)
	for _, m := range []map[string]any{doc.Env, env} {
		for _, v := range m {
			handed = append(handed, fmt.Sprint(v))
		}
	}
	built := false
	for _, s := range steps(job) {
		for _, key := range []string{"env", "with"} {
			m, _ := s[key].(map[string]any)
			for _, v := range m {
				handed = append(handed, fmt.Sprint(v))
			}
		}
		uses, _ := s["uses"].(string)
		if !strings.HasPrefix(uses, "docker/build-push-action@") {
			continue
		}
		built = true
		with, _ := s["with"].(map[string]any)
		if tags := fmt.Sprint(with["tags"]); tags != "${{ env.KT_CI_IMAGE }}" {
			out = append(out, fmt.Sprintf("the image job builds under %q, not ${{ env.KT_CI_IMAGE }}", tags))
		}
	}
	if !built {
		out = append(out, "the image job has no docker/build-push-action step")
	}
	fixed := regexp.MustCompile(`(?m)kubetower:ci(?:[^-]|$)|\bkt-ci(?:[^-]|$)`)
	for _, text := range handed {
		for _, m := range fixed.FindAllString(text, -1) {
			out = append(out, fmt.Sprintf("the image job names %q, a fixed name another job can remove or take", strings.TrimSpace(m)))
		}
	}
	dockerRun := regexp.MustCompile(`\bdocker\s+run\b[^|;&)\n]*`)
	runs := 0
	for _, script := range run {
		for _, m := range dockerRun.FindAllString(script, -1) {
			runs++
			if !strings.Contains(m, `"$KT_CI_IMAGE"`) {
				out = append(out, fmt.Sprintf("a docker run in the image job does not name \"$KT_CI_IMAGE\": %s", m))
			}
			if !strings.Contains(m, "--pull never") {
				out = append(out, fmt.Sprintf("a docker run in the image job does not say --pull never: %s", m))
			}
		}
	}
	if runs == 0 {
		out = append(out, "the image job runs its image nowhere")
	}
	all := steps(job)
	if len(all) == 0 {
		return append(out, "the image job has no steps")
	}
	// The clean-up as the shell reads it: a removal commented out, or
	// quoted as another command's argument, removes nothing.
	last := all[len(all)-1]
	lastRun, _ := last["run"].(string)
	cleanup := script(lastRun)
	cond := strings.TrimSpace(fmt.Sprint(last["if"]))
	if inner, ok := strings.CutPrefix(cond, "${{"); ok {
		cond = strings.TrimSpace(strings.TrimSuffix(inner, "}}"))
	}
	if cond != "always()" ||
		!runsCommand(cleanup, `docker image rm "$KT_CI_IMAGE"`) ||
		!runsCommand(cleanup, `docker rm -f "$KT_CI_CONTAINER"`) {
		out = append(out, "the image job's last step is not an if: always() step removing \"$KT_CI_IMAGE\" and \"$KT_CI_CONTAINER\"")
	}
	return out
}

// TestTheCIImageRuleRefusesEachWayRoundIt. Each way round
// TestTheCIImageIsTheRunsOwn, written into a copy of a real workflow, is
// refused; and each ordinary spelling beside them is accepted, since a guard
// that refused everything would pass the first half alone.
func TestTheCIImageRuleRefusesEachWayRoundIt(t *testing.T) {
	text := map[string]string{}
	for _, file := range []string{"ci.yml", "release.yml"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", file))
		if errors.Is(err, fs.ErrNotExist) {
			t.Skipf("%s is not in this tree (the image's build context excludes .github): %v", file, err)
		}
		if err != nil {
			t.Fatal(err)
		}
		text[file] = string(raw)
		if problems := ciImageFileProblems(file, raw); len(problems) > 0 {
			t.Fatalf("%s itself is refused: %v", file, problems)
		}
	}
	// Most cases add lines to the image job's clean-up, beside its own
	// removal; the others edit what they name.
	const cleanup = "          docker image rm \"$KT_CI_IMAGE\" >/dev/null 2>&1 || true\n"
	add := func(lines ...string) []string {
		s := cleanup
		for _, line := range lines {
			s += "          " + line + "\n"
		}
		return []string{cleanup, s}
	}
	const jobEnv = "      KT_CI_CONTAINER: kt-ci-${{ github.run_id }}-${{ github.run_attempt }}\n"
	type mutation struct {
		name, file string
		edits      []string // old, new, old, new...
	}
	// A refusal names the reason it is refused for: a guard that refused it
	// for another reason would pass with the hole this row is about open.
	type refusal struct {
		name, file, reason string
		edits              []string
	}
	notNamed := func(what string) string { return "removes what this job did not name itself (" + what + ")" }
	const (
		prunes    = "prunes the daemon the pool shares"
		fixedName = "a fixed name another job can remove or take"
		noCleanup = "the image job's last step is not an if: always() step"
	)
	refused := []refusal{
		// The removal forms.
		{"image remove", "ci.yml", notNamed("$(docker"), add(`docker image remove $(docker images -q)`)},
		{"container remove", "ci.yml", notNamed("x"), add(`docker container remove x`)},
		{"a host before rmi", "ci.yml", notNamed("x"), add(`docker -H tcp://127.0.0.1:2375 rmi x`)},
		{"a host before image rm", "ci.yml", notNamed("x"), add(`docker --host tcp://127.0.0.1:2375 image rm x`)},
		{"a host= before rmi", "ci.yml", notNamed("x"), add(`docker --host=tcp://127.0.0.1:2375 rmi x`)},
		{"a context and debug before rm", "ci.yml", notNamed("x"), add(`docker -D --context other rm -f x`)},
		{"volume rm", "ci.yml", notNamed("$(docker"), add(`docker volume rm $(docker volume ls -q)`)},
		{"volume remove", "ci.yml", notNamed("v"), add(`docker volume remove v`)},
		{"network rm", "ci.yml", notNamed("n"), add(`docker network rm n`)},
		{"a variable's rmi", "ci.yml", notNamed("x"), add(`D=docker`, `$D rmi x`)},
		{"a variable's image rm, set nowhere", "ci.yml", notNamed("x"), add(`"$D" image rm x`)},
		{"a variable's rm", "ci.yml", notNamed("x"), add(`D=docker`, `${D} rm -f x`)},
		{"a variable's rm, set by command -v", "ci.yml", notNamed("x"), add(`D=$(command -v docker)`, `"$D" rm -f x`)},
		{"an array's rm", "ci.yml", notNamed("x"), add(`D=(docker -H tcp://127.0.0.1:2375)`, `"${D[@]}" rm -f x`)},
		{"a variable's rm, set in the job's env", "ci.yml", notNamed("x"),
			append([]string{jobEnv, jobEnv + "      DK: docker\n"}, add(`$DK rm -f x`)...)},
		{"a variable's prune", "ci.yml", prunes, add(`$D system prune -af`)},
		// Each job's own names, and nobody else's.
		{"$REF in ci.yml", "ci.yml", notNamed(`"$REF"`), add(`docker image rm "$REF"`)},
		{"$REF in another job of release.yml", "release.yml", notNamed(`"$REF"`),
			[]string{"            case \"$REF\" in\n", "            docker image rm \"$REF\"\n            case \"$REF\" in\n"}},
		{"the Go job's valkey in the image job", "ci.yml", notNamed(`"$KT_CI_VALKEY_NAME"`), add(`docker rm -f "$KT_CI_VALKEY_NAME"`)},
		// A fixed name in any env the image job reads.
		{"a fixed name in the job's env", "ci.yml", fixedName, []string{jobEnv, jobEnv + "      OTHER: kubetower:ci\n"}},
		{"a fixed name in the workflow's env", "ci.yml", fixedName, []string{"\njobs:\n", "\nenv:\n  OTHER: kubetower:ci\njobs:\n"}},
		{"a fixed container name in the job's env", "ci.yml", fixedName, []string{jobEnv, jobEnv + "      OTHER: kt-ci\n"}},
		// The clean-up's removal commented out.
		{"the image removal commented out", "ci.yml", noCleanup, []string{cleanup, "          # docker image rm \"$KT_CI_IMAGE\" >/dev/null 2>&1 || true\n"}},
		{"the image removal commented out after a ;", "ci.yml", noCleanup, []string{cleanup, "          true;# docker image rm \"$KT_CI_IMAGE\" >/dev/null 2>&1 || true\n"}},
		// A quote escaped inside double quotes does not end them.
		{"a removal after an escaped quote", "ci.yml", notNamed("x"), add(`echo "\" # "; docker rmi x`)},
		// And the forms refused before, still refused.
		{"image rm of every image", "ci.yml", notNamed("$(docker"), add(`docker image rm $(docker images -q)`)},
		{"rm of every container", "ci.yml", notNamed("$(docker"), add(`docker rm -f $(docker ps -aq)`)},
		{"rmi -f of every image", "ci.yml", notNamed("$(docker"), add(`docker rmi -f $(docker images -q)`)},
		{"xargs rmi", "ci.yml", notNamed("no argument"), add(`docker images -q | xargs -r docker rmi`)},
		{"an own name, then every image", "ci.yml", notNamed("$(docker"), add(`docker rmi "$KT_CI_IMAGE" $(docker images -q)`)},
		{"a continued rmi", "ci.yml", notNamed("x"), add(`docker \`, `  rmi x`)},
		{"rmi through sh -c", "ci.yml", notNamed("x'"), add(`sh -c 'docker rmi x'`)},
		{"system prune", "ci.yml", prunes, add(`docker system prune -af`)},
		{"image prune", "ci.yml", prunes, add(`docker image prune -af`)},
		{"container prune", "ci.yml", prunes, add(`docker container prune -f`)},
		{"builder prune", "ci.yml", prunes, add(`docker builder prune -af`)},
		{"a removal after a quoted #", "ci.yml", notNamed("x"), add(`echo "#"; docker rmi x`)},
		// A redirection and its target are not where the arguments end.
		{"a removal after a redirection", "ci.yml", notNamed("x"), add(`docker rmi "$KT_CI_IMAGE" >/dev/null x`)},
		{"a removal after 2>&1", "ci.yml", notNamed("x"), add(`docker rmi "$KT_CI_IMAGE" 2>&1 x`)},
		// The compose and buildx removals, and the other objects' rm.
		{"compose down", "ci.yml", notNamed("no argument"), add(`docker compose down --rmi all -v`)},
		{"compose rm", "ci.yml", notNamed("no argument"), add(`docker compose -p other rm -f`)},
		{"buildx rm", "ci.yml", notNamed("x"), add(`docker buildx rm x`)},
		{"context rm", "ci.yml", notNamed("x"), add(`docker context rm x`)},
		{"plugin rm", "ci.yml", notNamed("x"), add(`docker plugin rm x`)},
		{"service rm", "ci.yml", notNamed("x"), add(`docker service rm x`)},
		{"stack rm", "ci.yml", notNamed("x"), add(`docker stack rm x`)},
		{"secret remove", "ci.yml", notNamed("x"), add(`docker secret remove x`)},
		// The clean-up's removal echoed rather than run.
		{"the image removal echoed", "ci.yml", noCleanup, []string{cleanup, "          echo 'docker image rm \"$KT_CI_IMAGE\"'\n"}},
	}
	accepted := []mutation{
		{"an own name, forced, with a host", "ci.yml", add(`docker -H unix:///var/run/docker.sock rm --force "$KT_CI_CONTAINER"`)},
		{"an own name through a variable", "ci.yml", add(`D=docker`, `"$D" image remove "$KT_CI_IMAGE"`)},
		{"a variable that is not docker, removing a file", "ci.yml", add(`$SUDO rm -rf "$RUNNER_TEMP/x"`)},
		{"a removal in a comment", "ci.yml", add(`echo "a # b" # docker rmi x`)},
		{"docker run --rm", "ci.yml", add(`docker run --rm --pull never "$KT_CI_IMAGE" --version`)},
		{"docker --config, not a removal", "ci.yml", add(`docker --config "$RUNNER_TEMP/cfg" image inspect "$KT_CI_IMAGE"`)},
		{"a directory named after docker", "ci.yml", add(`cfg=$(mktemp -d "$RUNNER_TEMP/docker-config.XXXXXX")`, `rm -rf "$cfg"`)},
		{"an own name, a redirection's target apart", "ci.yml", add(`docker rm -f "$KT_CI_CONTAINER" 2> /dev/null`)},
		{"an own name, &> before it", "ci.yml", add(`docker &>/dev/null image rm "$KT_CI_IMAGE"`)},
	}
	apply := func(tc mutation) (string, bool) {
		mutated := text[tc.file]
		for i := 0; i+1 < len(tc.edits); i += 2 {
			if strings.Count(mutated, tc.edits[i]) != 1 {
				t.Errorf("%s: %q is not in %s exactly once, so the mutation was not applied", tc.name, tc.edits[i], tc.file)
				return "", false
			}
			mutated = strings.Replace(mutated, tc.edits[i], tc.edits[i+1], 1)
		}
		return mutated, true
	}
	for _, tc := range refused {
		mutated, ok := apply(mutation{tc.name, tc.file, tc.edits})
		if !ok {
			continue
		}
		problems := ciImageFileProblems(tc.file, []byte(mutated))
		if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, tc.reason) }) {
			t.Errorf("%s: not refused with %q: %q", tc.name, tc.reason, problems)
			continue
		}
		t.Logf("%s: refused - %q", tc.name, problems)
	}
	for _, tc := range accepted {
		mutated, ok := apply(tc)
		if !ok {
			continue
		}
		if problems := ciImageFileProblems(tc.file, []byte(mutated)); len(problems) > 0 {
			t.Errorf("%s: refused, though it removes nothing of another run's: %v", tc.name, problems)
		}
	}
}

// TestAReleaseTagIsBareSemver. A release tag is `0.1.0` or `0.1.0-rc.4`, never
// `v0.1.0`: the v is not part of a semantic version.
// release.yml holds that in two places, and both only ever run from a tag, so
// this is the one check on them before the day of a release:
//
//  1. its push trigger starts on a tag that begins with a digit and not on a
//     `v` tag, matched as GitHub matches it (`*` stops at `/`, like path.Match);
//  2. verify's own tag check - the `if ! [[ "$tag" =~ ... ]]` block, run as it
//     stands in the file, by bash - accepts the two bare forms, refuses `v0.1.0`
//     with a sentence that names `0.1.0`, and refuses `0.1`.
func TestAReleaseTagIsBareSemver(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("release.yml is not in this tree (the image's build context excludes .github), so its tag form is UNCHECKED here: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}

	// 1. The trigger. `on:` reads as the key `true` (YAML 1.1), so both.
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	on, _ := doc["on"].(map[string]any)
	if on == nil {
		on, _ = doc["true"].(map[string]any)
	}
	push, _ := on["push"].(map[string]any)
	patterns, _ := push["tags"].([]any)
	if len(patterns) == 0 {
		t.Fatalf("release.yml's push trigger names no tag pattern (%v), so what starts a release is unchecked", on)
	}
	if len(push) != 1 {
		t.Errorf("release.yml's push trigger has %v besides tags; a release starts from a tag only", push)
	}
	starts := func(tag string) bool {
		for _, p := range patterns {
			if ok, err := path.Match(fmt.Sprint(p), tag); err != nil {
				t.Fatalf("tag pattern %q: %v", p, err)
			} else if ok {
				return true
			}
		}
		return false
	}
	for tag, want := range map[string]bool{"0.1.0": true, "0.1.0-rc.4": true, "v0.1.0": false, "v0.1.0-rc.4": false} {
		if got := starts(tag); got != want {
			t.Errorf("release.yml's push trigger %v on tag %q starts a run: %v, want %v", patterns, tag, got, want)
		}
	}

	// 2. The check itself, cut out of verify's step and run.
	jobs := workflowJobs(t, "release.yml")
	i := stepWhere(jobs["verify"], runs(`[[ "$tag" =~ `))
	if i < 0 {
		t.Fatal(`release.yml's verify job has no step matching the tag with [[ "$tag" =~ ... ]]`)
	}
	script, _ := steps(jobs["verify"])[i]["run"].(string)
	lines := strings.Split(script, "\n")
	from, to := -1, -1
	for n, line := range lines {
		trimmed := strings.TrimSpace(line)
		if from < 0 && strings.HasPrefix(trimmed, `if ! [[ "$tag" =~ `) {
			from = n
		}
		if from >= 0 && n > from && line == lines[from][:len(lines[from])-len(strings.TrimLeft(lines[from], " "))]+"fi" {
			to = n
			break
		}
	}
	if from < 0 || to < 0 {
		t.Fatalf("could not cut the tag check out of verify's step (from %d, to %d):\n%s", from, to, script)
	}
	check := strings.Join(lines[from:to+1], "\n")
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("no bash here, so release.yml's tag check is UNCHECKED: %v", err)
	}
	for _, c := range []struct {
		tag    string
		accept bool
		names  string
	}{
		{"0.1.0", true, ""},
		{"0.1.0-rc.4", true, ""},
		{"v0.1.0", false, "bare semver, 0.1.0,"},
		{"0.1", false, ""},
		{"0.1.0 -X main.x=y", false, ""},
		{"1.2.3\n", false, ""},
	} {
		out, err := exec.Command(bash, "-c", "set -eu\ntag=$1\n"+check+"\necho ACCEPTED", "check", c.tag).CombinedOutput()
		accepted := err == nil && strings.Contains(string(out), "ACCEPTED")
		if accepted != c.accept {
			t.Errorf("release.yml's tag check on %q: accepted %v, want %v (%v): %s", c.tag, accepted, c.accept, err, out)
		}
		if !c.accept && !strings.Contains(string(out), "::error::") {
			t.Errorf("release.yml's tag check refused %q without an ::error:: line: %s", c.tag, out)
		}
		if c.names != "" && !strings.Contains(string(out), c.names) {
			t.Errorf("release.yml's tag check refused %q without naming the bare form (%q): %s", c.tag, c.names, out)
		}
	}
}

// TestTheReleaseBuildsFromCachesMadeForIt. The pool keeps the runner user's Go
// module and build caches between jobs, any job on the machine may write to
// them, and Go does not check a cached entry again when it reuses it. So every
// release job that compiles what ships, or the tool that wraps it, points
// GOMODCACHE and GOCACHE at fresh directories under the job's temp before its
// first `go` command, checks every module against go.sum before it builds, and
// removes those directories in an always() step at its end.
func TestTheReleaseBuildsFromCachesMadeForIt(t *testing.T) {
	jobs := workflowJobs(t, "release.yml")
	for _, name := range []string{"archives", "setup"} {
		job, ok := jobs[name]
		if !ok {
			t.Errorf("release.yml has no %s job", name)
			continue
		}
		for _, problem := range freshCacheProblems(job) {
			t.Errorf("release.yml %s: %s", name, problem)
		}
	}
}

// freshCacheProblems says what a job lacks of the fresh-cache rule above.
func freshCacheProblems(job map[string]any) []string {
	var out []string
	fresh := stepWhere(job, func(s map[string]any) bool {
		run, _ := s["run"].(string)
		return strings.Contains(run, `GOMODCACHE=$root/mod`) && strings.Contains(run, `GOCACHE=$root/build`) &&
			strings.Contains(run, `root="$RUNNER_TEMP/`) && strings.Contains(run, "$GITHUB_ENV")
	})
	verify := stepWhere(job, runs("go mod verify"))
	firstGo := stepWhere(job, func(s map[string]any) bool {
		run, _ := s["run"].(string)
		return strings.Contains(run, "go run ") || strings.Contains(run, "go build ")
	})
	switch {
	case fresh < 0:
		out = append(out, "no step points GOMODCACHE and GOCACHE under $RUNNER_TEMP through $GITHUB_ENV")
	case firstGo >= 0 && fresh > firstGo:
		out = append(out, "the caches are made after the first go command")
	}
	switch {
	case verify < 0:
		out = append(out, "no step runs `go mod verify`")
	case fresh >= 0 && verify < fresh:
		out = append(out, "`go mod verify` runs before the caches are made, so it checks the shared ones")
	case firstGo >= 0 && verify > firstGo:
		out = append(out, "`go mod verify` runs after the first go command")
	}
	all := steps(job)
	if len(all) == 0 {
		return append(out, "no steps")
	}
	last := all[len(all)-1]
	run, _ := last["run"].(string)
	if fmt.Sprint(last["if"]) != "always()" || !strings.Contains(run, `root="$RUNNER_TEMP/`) || !strings.Contains(run, "rm -rf") {
		out = append(out, "its last step is not an always() step removing the caches under $RUNNER_TEMP")
	}
	return out
}

// The rule refuses each way round it: no fresh caches, a verify against the
// shared ones, and no removal at the end.
func TestTheFreshCacheRuleRefusesEachWayRoundIt(t *testing.T) {
	fresh := map[string]any{"run": "root=\"$RUNNER_TEMP/go-x\"\necho \"GOMODCACHE=$root/mod\"\necho \"GOCACHE=$root/build\" >> \"$GITHUB_ENV\""}
	verify := map[string]any{"run": "go mod download\ngo mod verify"}
	build := map[string]any{"run": "go run ./cmd/ktbuild -os linux"}
	clean := map[string]any{"if": "always()", "run": "root=\"$RUNNER_TEMP/go-x\"\nrm -rf \"$root\""}
	job := func(s ...map[string]any) map[string]any {
		raw := make([]any, len(s))
		for i := range s {
			raw[i] = s[i]
		}
		return map[string]any{"steps": raw}
	}
	if got := freshCacheProblems(job(fresh, verify, build, clean)); len(got) != 0 {
		t.Fatalf("a job that keeps the rule is refused: %v", got)
	}
	for name, j := range map[string]map[string]any{
		"no fresh caches":         job(verify, build, clean),
		"verify on shared caches": job(verify, fresh, build, clean),
		"no verify":               job(fresh, build, clean),
		"verify after the build":  job(fresh, build, verify, clean),
		"no removal":              job(fresh, verify, build),
		"removal not always":      job(fresh, verify, build, map[string]any{"run": clean["run"]}),
	} {
		if len(freshCacheProblems(j)) == 0 {
			t.Errorf("%s: the rule let it through", name)
		}
	}
}
