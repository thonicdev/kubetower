package actions

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/internal/edit"
)

// Running a CronJob now, which is `kubectl create job --from=cronjob/x`.
//
// The API has no "run now" verb: like kubectl, this copies
// `spec.jobTemplate` into a new Job. The panel says the consequences:
//
//   - The Job is not owned by the CronJob: not counted in the history
//     limits, not cleaned up by its controller, and `concurrencyPolicy:
//     Forbid` does not apply, so it can overlap a scheduled run.
//   - It runs even if the CronJob is suspended, which stops only the
//     schedule.

// jobNameLimit keeps the generated name within a Job name's 63 characters,
// with room for "-manual-", ten digits of a Unix second, and the five
// characters the Job's pods add.
const jobNameLimit = 38

func (r *Runner) trigger(ctx context.Context, where edit.Location, req Request) (Result, error) {
	if !isCronJob(where.Kind) {
		return Result{}, edit.Refuse(edit.RefusedInput,
			"only a CronJob can be run on demand; %s is not one", where.Kind)
	}

	// Reviewed for reading the CronJob before it is read: the template is
	// read with the console's rights, and `create jobs` alone says nothing
	// about whether this person may see it. Needs asks the same.
	if err := edit.Check(ctx, where, edit.Attributes(where, req.Target, "get", "")); err != nil {
		return Result{}, previewRefused(err)
	}
	cronjob, err := edit.Live(ctx, where, req.Target)
	if err != nil {
		return Result{}, edit.WrapGet(err, where.Kind)
	}
	// The template copied is the one on the CronJob now; the confirmation was
	// about the one the preview read.
	if err := stale(req, cronjob, where); err != nil {
		return Result{}, err
	}
	template, found, err := unstructured.NestedMap(cronjob.Object, "spec", "jobTemplate")
	if err != nil || !found {
		return Result{}, edit.Refuse(edit.RefusedInput,
			"this CronJob has no spec.jobTemplate, so there is nothing to run")
	}
	spec, found, err := unstructured.NestedMap(template, "spec")
	if err != nil || !found {
		return Result{}, edit.Refuse(edit.RefusedInput,
			"this CronJob's jobTemplate has no spec, so there is nothing to run")
	}

	name := jobName(req.Name, r.now())

	// A second Locate, so a second pass through the read-only guardrail: this
	// is the one action that writes to a resource other than the one on
	// screen.
	jobs, err := r.writer.Locate(ctx, edit.Target{
		Cluster: req.Cluster, Kind: "jobs", Namespace: req.Namespace, Name: name,
	})
	if err != nil {
		return Result{}, err
	}
	if !jobs.Namespaced {
		return Result{}, edit.Refuse(edit.RefusedInput, "this cluster serves Jobs as cluster-scoped, which this does not support")
	}

	job := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]any{
			"name":        name,
			"namespace":   req.Namespace,
			"annotations": triggerAnnotations(template),
			"labels":      copyStringMap(template, "metadata", "labels"),
		},
		"spec": spec,
	}}

	// Through edit.Create, not a ResourceInterface built from jobs.Dynamic:
	// only internal/edit can read the stamp that says a Location came from
	// Locate. Dry run first, so a quota or webhook refusing the Job is a
	// sentence rather than a half-made run.
	jobTarget := edit.Target{Cluster: req.Cluster, Kind: "jobs", Namespace: req.Namespace, Name: name}
	if _, _, err := edit.Create(ctx, jobs, jobTarget, job, true); err != nil {
		return Result{}, err
	}
	created, decision, err := edit.Create(ctx, jobs, jobTarget, job, false)
	if err != nil {
		return Result{}, err
	}
	r.writer.Logf(ctx, "write: cluster=%q kind=%s namespace=%s name=%s action=trigger job=%s decision=%s outcome=applied",
		where.Cluster, where.Resource.Resource, req.Namespace, req.Name, created.GetName(), decision)

	return Result{
		Target: req.Target, Verb: req.Verb,
		Message: fmt.Sprintf("Created Job %s from %s's jobTemplate. It is not owned by the CronJob, "+
			"so it does not count against the history limits and concurrencyPolicy does not apply to it.",
			created.GetName(), req.Name),
	}, nil
}

// triggerAnnotations copies the template's annotations and adds the one kubectl
// adds, so a manual run is distinguishable from a scheduled one afterwards.
func triggerAnnotations(template map[string]any) map[string]any {
	out := copyStringMap(template, "metadata", "annotations")
	out[InstantiateAnnotation] = "manual"
	return out
}

func copyStringMap(from map[string]any, path ...string) map[string]any {
	out := map[string]any{}
	values, found, err := unstructured.NestedStringMap(from, path...)
	if err != nil || !found {
		return out
	}
	for key, value := range values {
		out[key] = value
	}
	return out
}

// jobName is `<cronjob>-manual-<unix seconds>`.
//
// A timestamp rather than a random suffix: a double-click in the same
// second is refused as already existing rather than running twice.
func jobName(cronjob string, at time.Time) string {
	stem := cronjob
	if len(stem) > jobNameLimit {
		stem = strings.TrimRight(stem[:jobNameLimit], "-.")
	}
	return fmt.Sprintf("%s-manual-%d", stem, at.Unix())
}
