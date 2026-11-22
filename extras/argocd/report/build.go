package report

import (
	"encoding/json"
	"time"
)

// Building the report from three lists the caller read through
// `extension.Host`, for the person.

// Kind is one of Argo CD's three kinds as the caller read it.
type Kind struct {
	// Served is whether the cluster serves the kind at all.
	Served bool
	// Unavailable is why there is nothing: not served, or the read failed and
	// what it said. Empty means Items is the answer.
	Unavailable string
	// Items are the objects as JSON, decoded one at a time so a malformed one
	// is left out and counted without losing the rest.
	Items []json.RawMessage
	// Note is what the read itself left out (a page cap, a timeout).
	Note string
	// Plural names the kind in a sentence: "applications".
	Plural string
}

// NotServed is the sentence for a kind this cluster does not serve.
func NotServed(slug string) string {
	return "This cluster does not serve " + slug + ", so nothing of that kind exists here."
}

// Build is the page, from the three kinds and where Argo CD was found.
func Build(cluster string, now time.Time, apps, sets, projects Kind, install Install) Report {
	report := Report{Cluster: cluster, Collected: now}

	if !apps.Served && !sets.Served && !projects.Served {
		report.State = StateAbsent
		report.Detail = "This cluster serves none of Argo CD's custom resource kinds, so nothing here is deployed by Argo."
		report.Notes = notes(nil)
		return report
	}

	appArea := area("Applications", ApplicationKind, apps)
	setArea := area("ApplicationSets", ApplicationSetKind, sets)
	projectArea := area("AppProjects", AppProjectKind, projects)

	var (
		list        []Application
		appTallies  tallies
		setItems    []rawSet
		projItems   []rawProject
		appOffCount int
		appDelCount int
	)
	if apps.Served && apps.Unavailable == "" {
		items, refused := decodeItems[rawApplication](apps.Items)
		appArea.Objects, appArea.Refused = len(items), refused
		appArea.Note = joinNotes(apps.Note, refusalNote(refused, apps.Plural))
		list, appTallies = readApplications(items)
		for _, a := range list {
			if a.Offending() {
				appOffCount++
			}
			if a.Deleting {
				appDelCount++
			}
		}
	}
	if sets.Served && sets.Unavailable == "" {
		items, refused := decodeItems[rawSet](sets.Items)
		setArea.Objects, setArea.Refused = len(items), refused
		setArea.Note = joinNotes(sets.Note, refusalNote(refused, sets.Plural))
		setItems = items
	}
	if projects.Served && projects.Unavailable == "" {
		items, refused := decodeItems[rawProject](projects.Items)
		projectArea.Objects, projectArea.Refused = len(items), refused
		projectArea.Note = joinNotes(projects.Note, refusalNote(refused, projects.Plural))
		projItems = items
	}

	report.Areas = []Area{appArea, setArea, projectArea}
	report.Applications, report.Offending, report.Deleting = len(list), appOffCount, appDelCount
	report.Sync = appTallies.sync
	report.Health = appTallies.health
	report.Offenders, report.OffendersNote = offenders(list, appArea.Note)
	report.Causes, report.CausesNote = causes(list)
	report.Sets, report.SetsNote, report.SetTotal, report.SetFailing = failingSets(setItems)
	report.Projects, report.ProjectsNote, report.ProjectTotal = projectRows(projItems, list)
	report.Install = install.completedBy(projItems, list)
	report.State, report.Detail = state(report.Areas)
	report.Notes = notes(report.Areas)
	return report
}

func area(name, kind string, k Kind) Area {
	a := Area{Name: name, Kind: kind, Served: k.Served}
	switch {
	case !k.Served:
		a.Unavailable = NotServed(kind)
	case k.Unavailable != "":
		a.Unavailable = k.Unavailable
	}
	return a
}
