package web

import (
	"fmt"
	"strings"

	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/api"
)

// A confirmation leads with one line about this object, from facts the preview
// already read: its owner (`actions.Subject`), whether Argo CD or Helm holds its
// manifest, a CronJob's runs in flight and suspension, a field already at the
// value the verb writes. A warning appears only where it is true of this object;
// a warning true of every object teaches the eye to skip it. The verb's general
// paragraph and warning stay in the plan (`explanation`, `warning`) under
// "Details".
//
// The line travels as `PlanFacts.Headline` and `sentenceFor` returns it as the
// plan's sentence, so the dialog composes nothing. A plan with no line (a bulk
// run, an apply, a data write) keeps the sentence composed from its facts.
//
// "Cannot be undone." and the plan's `irreversible` are per object: only where
// nothing would make the object again (no counting controller, Argo CD
// application or Helm release). The menus' red is the verb's, since a menu has
// not read the object. A single delete names what goes and never says "Cannot be
// undone.".

// planLine is a plan's line and what goes with it.
type planLine struct {
	// Text is the line.
	Text string
	// Warnings are true of this object, and only of it.
	Warnings []string
	// Irreversible says nothing will make the object again: the line says so and is
	// drawn red.
	Irreversible bool
}

// lineFor composes the line for one verb on one object. kind is the URL's plural
// slug; the object's own Kind, in recap.Subject, is preferred. Every verb
// `specFor` covers has an arm (TestEveryVerbTheSpecCoversHasALine).
func lineFor(verb actions.Verb, kind, name string, recap actions.Recap) planLine {
	subject := recap.Subject
	pod := isPodKind(kind)
	switch verb {
	case actions.Delete:
		// One resource: what goes, and nothing else - no owner, no warning.
		// A namespace takes everything in it, which its name alone does not say.
		if kind == "namespaces" {
			return planLine{Text: fmt.Sprintf("Deletes %s %s and everything in it.", kindName(kind, subject), name)}
		}
		return planLine{Text: fmt.Sprintf("Deletes %s %s.", kindName(kind, subject), name)}

	case actions.Restart:
		if pod {
			// The same line as a delete, because it is the same act: the pod is deleted and
			// something else may make another.
			return podGoes("Deletes", name, subject)
		}
		return planLine{Text: fmt.Sprintf("Replaces the pods of %s %s, a few at a time.", kindName(kind, subject), name)}

	case actions.Evict:
		return podGoes("Evicts", name, subject)

	case actions.Cordon:
		if recap.Unchanged {
			return planLine{Text: fmt.Sprintf("Node %s is already cordoned: this changes nothing.", name)}
		}
		return planLine{Text: fmt.Sprintf("No new pod is scheduled onto node %s; the ones on it stay.", name)}

	case actions.Uncordon:
		if recap.Unchanged {
			return planLine{Text: fmt.Sprintf("Node %s is not cordoned: this changes nothing.", name)}
		}
		return planLine{Text: fmt.Sprintf("Pods can be scheduled onto node %s again.", name)}

	case actions.Drain:
		return planLine{Text: fmt.Sprintf("Cordons node %s and lists its pods. Nothing is evicted yet.", name)}

	case actions.Suspend:
		if recap.Unchanged {
			return planLine{Text: fmt.Sprintf("CronJob %s is already suspended: this changes nothing.", name)}
		}
		line := planLine{Text: fmt.Sprintf("Stops CronJob %s starting new runs.", name)}
		if len(subject.Active) > 0 {
			line.Warnings = append(line.Warnings,
				fmt.Sprintf("A run is in progress (%s): suspending does not stop it.", runs(subject.Active)))
		}
		return line

	case actions.Resume:
		if recap.Unchanged {
			return planLine{Text: fmt.Sprintf("CronJob %s is not suspended: this changes nothing.", name)}
		}
		return planLine{Text: fmt.Sprintf("CronJob %s runs on its schedule again.", name)}

	case actions.Trigger:
		// No Job name: the runner names the Job at the click (`jobName`), so a name read
		// here would be a different Job's.
		line := planLine{Text: fmt.Sprintf("Starts a Job from %s, now.", name)}
		if len(subject.Active) > 0 {
			line.Warnings = append(line.Warnings,
				fmt.Sprintf("A run is already in progress (%s).", runs(subject.Active)))
		}
		if subject.Suspended {
			line.Warnings = append(line.Warnings,
				fmt.Sprintf("%s is suspended: this run starts anyway.", name))
		}
		return line

	case actions.Scale:
		// The count in effect is already the gesture's label next to the input.
		return planLine{Text: fmt.Sprintf("Sets the replica count of %s %s.", kindName(kind, subject), name)}
	}
	return planLine{}
}

// podGoes is the line for a verb that removes a pod: what happens to it, then
// what - if anything - makes another.
func podGoes(does, name string, subject actions.Subject) planLine {
	head := fmt.Sprintf("%s pod %s.", does, name)
	if owner := subject.Remaker(); owner.Known() {
		if owner.Kind == "Job" {
			// A Job replaces a pod lost while it runs and counts it against backoffLimit,
			// which is why `actions.RestartsAPod` does not call it a restart.
			return planLine{Text: fmt.Sprintf("%s Job %s starts another, spending one of its retries.", head, owner.Name)}
		}
		return planLine{Text: fmt.Sprintf("%s %s %s creates a new one.", head, owner.Kind, owner.Name)}
	}
	if subject.Manager.Known() {
		return planLine{Text: fmt.Sprintf("%s %s %s can put it back.", head, subject.Manager.By, subject.Manager.Name)}
	}
	return planLine{Text: head + " Nothing will recreate it.", Irreversible: true}
}

// kindName is how the line names a kind: the object's own Kind where the read
// carried one, the slug's singular (`api.KindWord`) otherwise.
func kindName(slug string, subject actions.Subject) string {
	if subject.Kind != "" {
		return subject.Kind
	}
	return api.KindWord(slug, 1)
}

// runs names a CronJob's runs in flight: up to three, then how many more, so the
// line stays on the dialog.
func runs(names []string) string {
	const shown = 3
	if len(names) <= shown {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:shown], ", "), len(names)-shown)
}

// buttonFor is the control that commits, naming the act and the object ("Delete
// api-0", "Run nightly now"), so the last thing read before the click is a check.
// A delete's button is "Delete" and the dialog's title names the object; a bulk
// plan's phrase is composed in PreviewBulk.
func buttonFor(spec actionSpec, name string) string {
	return strings.Replace(spec.Button, "{name}", name, 1)
}
