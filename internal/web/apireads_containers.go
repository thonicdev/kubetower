package web

import (
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/detail"
)

// containerViews carries what a pod is made of to the wire, unfiltered. Init
// containers stay first with their flag (the order they run in; the flag keeps
// one that will not start from reading as a crashing app container). Env and
// Imported stay apart: Env is written in the pod spec, Imported was learned by
// reading another object.
func containerViews(containers []detail.Container) []api.PodContainer {
	if len(containers) == 0 {
		return nil
	}
	out := make([]api.PodContainer, 0, len(containers))
	for _, container := range containers {
		out = append(out, api.PodContainer{
			Name:  container.Name,
			Image: container.Image,
			Init:  container.Init,
			// Set only where it is not the tag's default, so the surprising case stands out.
			PullPolicy: container.PullPolicy,
			// Element by element, as the spec gives them.
			Command:  container.Command,
			Args:     container.Args,
			Requests: amountsOf(container.Requests),
			Limits:   amountsOf(container.Limits),
			Env:      envVarsOf(container.Env),
			EnvFrom:  importsOf(container.EnvFrom),
			Imported: envVarsOf(container.Imported),
			// How it is doing; everything above is what it was told.
			Probes: probesOf(container.Probes),
			Status: statusOf(container.Status),
		})
	}
	return out
}

// probesOf carries all three probe declarations, always: a missing readiness
// probe is a finding, and an omitted entry would be an empty cell instead of that
// sentence.
func probesOf(probes []detail.Probe) []api.ContainerProbe {
	if len(probes) == 0 {
		return nil
	}
	out := make([]api.ContainerProbe, 0, len(probes))
	for _, probe := range probes {
		out = append(out, api.ContainerProbe{
			Kind:  probe.Kind,
			State: api.ProbeState(probe.State),
			// Two members, so a client draws the kind of check and its target without
			// parsing English.
			Handler:             probe.Handler,
			Target:              probe.Target,
			InitialDelaySeconds: probe.InitialDelaySeconds,
			PeriodSeconds:       probe.PeriodSeconds,
			TimeoutSeconds:      probe.TimeoutSeconds,
			FailureThreshold:    probe.FailureThreshold,
			// Keeps `unreadable` from being read as `absent`.
			Problem: probe.Problem,
		})
	}
	return out
}

// statusOf carries what the kubelet reported, including that it reported
// nothing: a nil status becomes `unreported` with its sentence, since a missing
// member cannot be told from one a mapper dropped.
func statusOf(status *detail.ContainerStatus) *api.ContainerStatus {
	if status == nil {
		return &api.ContainerStatus{
			State:   api.ContainerUnreported,
			Problem: "the kubelet has reported nothing about this container yet",
		}
	}
	return &api.ContainerStatus{
		State: api.ContainerRunState(status.State),
		// Always written, false included, so it cannot be mistaken for unfilled.
		Ready:    status.Ready,
		Reason:   status.Reason,
		Message:  status.Message,
		Restarts: status.Restarts,
		ExitCode: status.ExitCode,
		Problem:  status.Problem,

		LastTermination: terminationOf(status.Last),
	}
}

// terminationOf carries the previous run, or nothing where the kubelet keeps
// none. A zero time is left out rather than sent as year one.
func terminationOf(last *detail.Termination) *api.ContainerTermination {
	if last == nil {
		return nil
	}
	out := &api.ContainerTermination{
		ExitCode: last.ExitCode,
		Signal:   last.Signal,
		Reason:   last.Reason,
		Message:  last.Message,
	}
	if !last.StartedAt.IsZero() {
		at := last.StartedAt.UTC()
		out.StartedAt = &at
	}
	if !last.FinishedAt.IsZero() {
		at := last.FinishedAt.UTC()
		out.FinishedAt = &at
	}
	return out
}

func amountsOf(amounts []detail.Amount) []api.ResourceAmount {
	if len(amounts) == 0 {
		return nil
	}
	out := make([]api.ResourceAmount, 0, len(amounts))
	for _, amount := range amounts {
		out = append(out, api.ResourceAmount{Name: amount.Name, Value: amount.Value})
	}
	return out
}

// envVarsOf carries the environment rows. A Secret's value is never here:
// internal/detail does not fill Resolved from a Secret. It is revealed one key at
// a time through GET /api/c/{cluster}/env. Held is apart from Resolved because a
// key holding the empty string is not a key nobody read.
func envVarsOf(vars []detail.EnvVar) []api.EnvVar {
	if len(vars) == 0 {
		return nil
	}
	out := make([]api.EnvVar, 0, len(vars))
	for _, v := range vars {
		row := api.EnvVar{
			Name:     v.Name,
			Value:    v.Value,
			From:     v.From,
			Secret:   v.Secret,
			Resolved: v.Resolved,
			Held:     v.Held,
			Clipped:  v.Clipped,
			Problem:  v.Problem,
			// The kind of absence, four states. `expected` is kept apart because a healthy
			// pod can carry dozens of such rows, and a warning color on them stops being
			// seen.
			Absence: string(v.Absence),
		}
		if v.Source != nil {
			row.Source = &api.EnvSource{
				Kind: v.Source.Kind,
				Name: v.Source.Name,
				Key:  v.Source.Key,
				// Without it, Show on an `optional: true` reference would claim the pod cannot
				// start.
				Optional: v.Source.Optional,
			}
		}
		out = append(out, row)
	}
	return out
}

func importsOf(imports []detail.Whole) []api.EnvImport {
	if len(imports) == 0 {
		return nil
	}
	out := make([]api.EnvImport, 0, len(imports))
	for _, whole := range imports {
		out = append(out, api.EnvImport{
			Kind:     whole.Kind,
			Name:     whole.Name,
			Prefix:   whole.Prefix,
			Optional: whole.Optional,
			// Keys and Problem together: no keys, may not read, and nobody looked otherwise
			// look the same.
			Keys:    whole.Keys,
			Problem: whole.Problem,
			Absence: string(whole.Absence),
		})
	}
	return out
}

func sectionsOf(object detail.Object) []api.DetailSection {
	fields := make([]api.DetailField, 0, len(object.Facts))
	for _, fact := range object.Facts {
		fields = append(fields, api.DetailField{
			Label: fact.Label,
			Value: fact.Value,
			// Intent, not markup: a badge, colored text, plain text.
			Tone: toneOf(fact.Class),
			Pill: fact.Pill,
			// The one cell that lifts the fact table's inherited `nowrap`; a client guessing
			// it from the label would wrap the wrong cells.
			Prose: fact.Sentence,
			// What the copy button takes.
			Copy: fact.Copy,
		})
	}
	// The summary only: the keys are the data block's, and a Secret's type, key count
	// and immutability arrive in `object.Facts`.
	return []api.DetailSection{{Key: "summary", Label: "Summary", State: api.StateOK, Fields: fields}}
}
