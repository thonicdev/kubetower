package edit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/thonicdev/kubetower/internal/detail"
	"github.com/thonicdev/kubetower/textutil"
)

// The manifest editor: any kind, one document, and a confirmation in between.
//
// Preview computes the patch, dry-runs it and returns the changed lines;
// Apply redoes all of it from scratch (re-reads the object, re-checks the
// guardrail, the precondition and the dry run) and commits. Nothing Preview
// said is trusted: the confirmation is a browser between two requests, and a
// stale tab can carry anything back.

// ManifestChange is a document as the operator edited it.
type ManifestChange struct {
	Target
	// YAML is the whole edited document. It is never sent: only its
	// difference from what the drawer showed is (mergePatch).
	YAML string
	// ResourceVersion is what the editor was seeded with.
	ResourceVersion string
}

// Preview is what the operator confirms.
type Preview struct {
	Target
	// Title is the kind as a person writes it: "Deployment".
	Title string
	// ResourceVersion is what Apply must be given back: the version the diff
	// was computed against, so a preview left open while somebody else saves
	// fails rather than applying to an object it never saw.
	ResourceVersion string
	// YAML is the edited document, carried back so Apply recomputes the patch
	// rather than trusting one that traveled through a browser.
	YAML string

	Diff    []DiffLine
	Added   int
	Removed int
	// Patch is the JSON merge patch, indented: literally what leaves the
	// process.
	Patch  string
	Fields []string
	// Validated is what the dry run said.
	Validated string
}

// PreviewManifest computes the change and validates it, and sends nothing.
func (w *Writer) PreviewManifest(ctx context.Context, change ManifestChange) (Preview, error) {
	ready, err := w.prepare(ctx, change)
	if err != nil {
		return Preview{}, err
	}

	lines, stat := diffLines(ready.beforeYAML, ready.afterYAML)
	return Preview{
		Target:          change.Target,
		Title:           ready.where.Kind,
		ResourceVersion: ready.object.GetResourceVersion(),
		YAML:            change.YAML,
		Diff:            lines,
		Added:           stat.Added,
		Removed:         stat.Removed,
		Patch:           pretty(ready.patch),
		Fields:          ready.touched,
		Validated: fmt.Sprintf("The API server was asked to apply this as a dry run and accepted it. "+
			"%d field(s) will be sent; nothing else is in the request.", len(ready.touched)),
	}, nil
}

// ApplyManifest commits the change, having done all of Preview's work again.
func (w *Writer) ApplyManifest(ctx context.Context, change ManifestChange) (Applied, error) {
	ready, err := w.prepare(ctx, change)
	if err != nil {
		return Applied{}, err
	}

	// prepare has already dry-run this exact body; this is the commit.
	applied, decision, err := send(ctx, ready.where, change.Target, ready.body, false)
	if err != nil {
		return Applied{}, err
	}

	// The same `write:` shape as every write. `fields` is quoted: its paths
	// are keys of the YAML the person sent. Logf escapes control characters.
	w.Logf(ctx, "write: cluster=%q kind=%s namespace=%s name=%s fields=%q decision=%s outcome=applied",
		ready.where.Cluster, ready.where.Resource.Resource, change.Namespace, change.Name,
		strings.Join(ready.touched, ","), decision)

	return Applied{
		Target:          change.Target,
		ResourceVersion: applied.GetResourceVersion(),
		Fields:          ready.touched,
	}, nil
}

// prepared is everything both calls need, computed once.
type prepared struct {
	where  Location
	object *unstructured.Unstructured
	patch  map[string]any
	body   []byte
	// touched are the paths the operator changed, computed before the
	// precondition is added, so the confirmation and the log name their edit.
	touched []string

	beforeYAML string
	afterYAML  string
}

// prepare does every check there is, and stops at the first that fails.
func (w *Writer) prepare(ctx context.Context, change ManifestChange) (*prepared, error) {
	where, err := w.locate(ctx, change.Target)
	if err != nil {
		return nil, err
	}

	edited, err := parseDocument(change.YAML)
	if err != nil {
		return nil, err
	}
	// The kind is read out of the parsed document before anything is
	// reviewed or sent. The patch and the review both use the route's
	// resource, so a document naming another kind is refused: reviewing the
	// route's kind and writing the body's would be a full bypass that looks
	// correct from both sides.
	apiVersion, _ := edited["apiVersion"].(string)
	kind, _ := edited["kind"].(string)
	if err := sameKind(where, apiVersion, kind); err != nil {
		return nil, err
	}

	// Reviewed before the object is read, since the version check, the
	// immutability prediction and a missing object all answer from it.
	if err := reviewBeforeReading(ctx, where, change.Target); err != nil {
		return nil, err
	}

	object, err := live(ctx, where, change.Target)
	if err != nil {
		return nil, wrapGet(err, where.Kind)
	}
	if change.ResourceVersion == "" {
		return nil, refuse(RefusedInput,
			"this editor was opened without a resourceVersion, so a save could not be made safe. Reload the panel")
	}
	if object.GetResourceVersion() != change.ResourceVersion {
		return nil, refuse(RefusedConflict, conflictSentence, strings.ToLower(where.Kind))
	}

	// The base is the trimmed object the drawer showed, not the stored one:
	// what was hidden is identical on both sides, so it cannot appear in the
	// patch and cannot be removed by an edit that never mentioned it.
	base, err := normalise(detail.Trimmed(object).Object)
	if err != nil {
		return nil, err
	}

	patch := mergePatch(base, edited)
	if len(patch) == 0 {
		return nil, refuse(RefusedNoChange, "nothing changed in this manifest, so there is nothing to send")
	}

	immutable, _, _ := unstructured.NestedBool(object.Object, "immutable")
	if err := predictRefusal(where.Kind, immutable, patch); err != nil {
		return nil, err
	}

	touched := paths(patch)

	// The precondition, added after the guard so the guard judges the
	// operator's edit. The check above closes the window a person can see;
	// this closes the milliseconds they cannot.
	metadata, _ := patch["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["resourceVersion"] = object.GetResourceVersion()
	patch["metadata"] = metadata

	body, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}

	// The dry run: the API server validates and runs admission, changing
	// nothing, before the operator has committed to anything.
	if _, _, err := send(ctx, where, change.Target, body, true); err != nil {
		return nil, err
	}

	// Both sides are rendered from the normalized documents, so re-indenting,
	// reordering keys or dropping a comment shows as no change, which is true:
	// none of them produces a patch.
	beforeYAML, err := renderYAML(base)
	if err != nil {
		return nil, err
	}
	afterYAML, err := renderYAML(edited)
	if err != nil {
		return nil, err
	}

	return &prepared{
		where: where, object: object,
		patch: patch, body: body, touched: touched,
		beforeYAML: beforeYAML, afterYAML: afterYAML,
	}, nil
}

// parseDocument reads the edited YAML, and refuses anything that is not one
// object.
func parseDocument(doc string) (map[string]any, error) {
	if strings.TrimSpace(doc) == "" {
		return nil, refuse(RefusedInput, "the manifest is empty")
	}
	if n := countDocuments(doc); n > 1 {
		return nil, refuse(RefusedInput,
			"this file holds %d documents; the console creates or saves one object at a time. "+
				"Split it and send each document on its own", n)
	}
	raw, err := yaml.YAMLToJSON([]byte(doc))
	if err != nil {
		return nil, refuse(RefusedInput, "this is not valid YAML: %s", textutil.FirstLineN(err.Error(), refusalLine))
	}
	edited, err := decodeJSON(raw)
	if err != nil {
		return nil, refuse(RefusedInput,
			"a manifest has to be one object; this is %s", textutil.FirstLineN(err.Error(), refusalLine))
	}
	return edited, nil
}

// countDocuments counts the YAML documents of a stream that hold anything but
// comments and blank lines, so a leading or trailing `---` does not count.
func countDocuments(doc string) int {
	count, live := 0, false
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "---" || strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "---\t") {
			if live {
				count++
			}
			live = false
			// "--- text" opens a document with content on the marker line.
			rest := strings.TrimSpace(strings.TrimPrefix(line, "---"))
			live = rest != "" && !strings.HasPrefix(rest, "#")
			continue
		}
		if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "#") {
			live = true
		}
	}
	if live {
		count++
	}
	return count
}

func renderYAML(document map[string]any) (string, error) {
	out, err := yaml.Marshal(document)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
