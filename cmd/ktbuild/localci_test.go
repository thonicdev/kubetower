package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// localCIMarker is a line of test/ci-local.sh naming what it replays
// (`# step: ci/go: gofmt`) or leaves to the runners, a whole job
// (`# runner-only: ci/dmg - why`) or one step of a replayed one
// (`# runner-only: ci/web: <step> - why`).
var localCIMarker = regexp.MustCompile(`^\s*# (step|runner-only): ([a-z-]+/[a-z0-9-]+)(?:: (.+?))?(?: - (.+))?\s*$`)

type workflowFile struct {
	Jobs map[string]struct {
		Steps []map[string]any `json:"steps"`
	} `json:"jobs"`
}

// stepKey is how a step is named in a marker: its name, or the first line of
// its `run:`. A `uses:` step with no `run:` is what a runner provides
// (checkout, a toolchain, an artifact) and is not named.
func stepKey(step map[string]any) (string, bool) {
	if name, ok := step["name"].(string); ok && name != "" {
		return name, true
	}
	if run, ok := step["run"].(string); ok {
		return strings.TrimSpace(strings.SplitN(strings.TrimSpace(run), "\n", 2)[0]), true
	}
	return "", false
}

// TestTaskCICoversEveryJobOfGuardsAndCI keeps `task ci` from drifting from the
// workflows it replays: every job of guards.yml and ci.yml is either replayed
// by test/ci-local.sh or listed there as runner-only with its reason, and every
// step of a replayed job is named there. A job or step added to a workflow
// fails here until the script says what it does with it; a marker naming a
// job or step that no longer exists fails too.
//
// Skipped only where .github is absent: the image's build context drops it.
func TestTaskCICoversEveryJobOfGuardsAndCI(t *testing.T) {
	root := filepath.Join("..", "..")
	script, err := os.ReadFile(filepath.Join(root, "test", "ci-local.sh"))
	if err != nil {
		t.Fatal(err)
	}
	steps := map[string]map[string]bool{} // job -> step keys named
	runnerOnly := map[string]string{}     // job -> reason
	for i, line := range strings.Split(string(script), "\n") {
		m := localCIMarker.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		kind, job, step, why := m[1], m[2], m[3], m[4]
		switch kind {
		case "step":
			if step == "" {
				t.Errorf("ci-local.sh:%d: a step marker names no step: %q", i+1, line)
				continue
			}
			if steps[job] == nil {
				steps[job] = map[string]bool{}
			}
			steps[job][step] = true
		case "runner-only":
			if why == "" {
				t.Errorf("ci-local.sh:%d: runner-only %s gives no reason", i+1, job)
			}
			if step == "" {
				runnerOnly[job] = why
				continue
			}
			// One step of a replayed job that only a runner can do.
			if steps[job] == nil {
				steps[job] = map[string]bool{}
			}
			steps[job][step] = true
		}
	}

	for _, name := range []string{"guards", "ci"} {
		raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", name+".yml"))
		if errors.Is(err, fs.ErrNotExist) {
			t.Skipf("%s.yml is not in this tree (the image's build context excludes .github), so task ci's "+
				"coverage is UNCHECKED here: %v", name, err)
		}
		if err != nil {
			t.Fatal(err)
		}
		var wf workflowFile
		if err := yaml.Unmarshal(raw, &wf); err != nil {
			t.Fatalf("%s.yml: %v", name, err)
		}
		if len(wf.Jobs) == 0 {
			t.Fatalf("%s.yml: no jobs read, so nothing would be checked", name)
		}
		for id, job := range wf.Jobs {
			key := name + "/" + id
			_, skipped := runnerOnly[key]
			named := steps[key]
			switch {
			case skipped && named != nil:
				t.Errorf("%s is both replayed and runner-only in test/ci-local.sh", key)
			case !skipped && named == nil:
				t.Errorf("%s is neither replayed by test/ci-local.sh nor listed there as `# runner-only: %s - <why>`", key, key)
			case skipped:
				delete(runnerOnly, key)
				continue
			}
			for _, step := range job.Steps {
				k, ok := stepKey(step)
				if !ok {
					continue
				}
				if !named[k] {
					t.Errorf("%s's step %q is not replayed by test/ci-local.sh: add `# step: %s: %s` where it is, "+
						"or make the job runner-only", key, k, key, k)
				}
				delete(named, k)
			}
			for k := range named {
				t.Errorf("test/ci-local.sh names %s's step %q, which %s.yml no longer has", key, k, name)
			}
			delete(steps, key)
		}
	}
	for _, job := range slices.Sorted(mapKeys(steps)) {
		t.Errorf("test/ci-local.sh replays %s, which is in neither guards.yml nor ci.yml", job)
	}
	for _, job := range slices.Sorted(mapKeys(runnerOnly)) {
		t.Errorf("test/ci-local.sh lists %s as runner-only, which is in neither guards.yml nor ci.yml", job)
	}
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// The marker grammar itself: a test that parsed nothing would pass on any
// script.
func TestLocalCIMarkersParse(t *testing.T) {
	for line, want := range map[string][]string{
		"# step: ci/go: gofmt":                              {"step", "ci/go", "gofmt", ""},
		"# step: guards/pr-title: Lint the title":           {"step", "guards/pr-title", "Lint the title", ""},
		"# runner-only: ci/dmg - the full tier":             {"runner-only", "ci/dmg", "", "the full tier"},
		"# runner-only: ci/web: hand it over - needs a run": {"runner-only", "ci/web", "hand it over", "needs a run"},
		"# step: ci/go: go vet, Windows, tests compiled":    {"step", "ci/go", "go vet, Windows, tests compiled", ""},
	} {
		m := localCIMarker.FindStringSubmatch(line)
		if m == nil || !slices.Equal(m[1:], want) {
			t.Errorf("%q parsed as %q, want %q", line, m, want)
		}
	}
	if localCIMarker.MatchString("# named below, by a `# step:` line") {
		t.Error("prose mentioning a marker is read as one")
	}
}
