package web

import (
	"strconv"
	"time"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/archive"
	"github.com/thonicdev/kubetower/internal/nodeshell"
	"github.com/thonicdev/kubetower/internal/updater"
)

// settingsOf is the whole payload.
func settingsOf(s *Server) api.Settings {
	out := api.Settings{
		Info: settingsInfoOf(s),
	}
	// Absent rather than disabled where it cannot work: no terminal layer, a node
	// shell channel not registered (the server profile), or a state directory wiped
	// at every restart, where a saved image would not be kept. The node shell itself
	// still opens with the default image.
	offered := apiChannels{s}.Offers(api.ChannelNodeShell) && !s.setup.StateEphemeral
	out.NodeShell = api.NodeShellSettings{
		Available:    offered,
		DefaultImage: nodeshell.DefaultImage,
	}
	if offered {
		out.NodeShell.Enabled = true
		out.NodeShell.Image = s.claude.NodeShellImage()
	}
	if s.setup.StateEphemeral {
		fillEphemeralArchive(&out)
		return out
	}
	fillArchiveSettings(&out, s.archive)
	return out
}

// fillEphemeralArchive is the archive on a state directory that does not
// survive a restart: absent, and saying why. Not `fillArchiveSettings(nil)`,
// whose "this build has no archive" names a missing feature where the deployment
// is missing a volume (`not_supported_here`, not `not_installed`).
// internal/launch does not open the archive in this case.
func fillEphemeralArchive(out *api.Settings) {
	out.Archive = api.ValueFailed[api.ArchiveFacts](api.StateAbsent,
		api.Because(api.CodeNotSupportedHere, ephemeralArchive).Retry(false))
}

// ephemeralArchive is why a console on a state directory wiped at every restart
// has no archive: one sentence for the settings read and for the preview and act
// of emptying it.
const ephemeralArchive = "this console's state directory does not survive a restart, so it keeps no " +
	"archive: a window of events on a disk wiped at the next start would be " +
	"a promise nothing keeps"

// fillArchiveSettings is `general`, the retention list and the archive envelope.
// A build with no archive is `absent`, not empty: those are different answers.
func fillArchiveSettings(out *api.Settings, store Archives) {
	if store == nil {
		out.Archive = api.ValueFailed[api.ArchiveFacts](api.StateAbsent,
			api.Because(api.CodeNotInstalled, "this build has no archive").Retry(false))
		return
	}

	settings := store.Settings()
	enabled, logs, days := settings.Enabled, settings.Logs, settings.RetentionDays
	out.General = api.SettingsWrite{
		ArchiveEnabled: &enabled,
		CaptureLogs:    &logs,
		RetentionDays:  &days,
	}

	// The dropdown's list, with the value in effect inserted in order if the list
	// lacks it (retentionChoicesFor).
	for _, choice := range retentionChoicesFor(days) {
		out.RetentionChoices = append(out.RetentionChoices,
			api.RetentionChoice{Days: choice.Days, Label: choice.Label})
	}

	out.Archive = archiveFactsOf(store)
}

// archiveFactsOf is what the archive holds and where. With nowhere to write
// (`Status.Root` empty) it is `absent`, not zero bytes. A refused write stays on
// the `ok` arm: the archive holds what it holds, and only writing stopped.
func archiveFactsOf(store Archives) api.Value[api.ArchiveFacts] {
	status := store.Status()
	if status.Root == "" {
		return api.ValueFailed[api.ArchiveFacts](api.StateAbsent,
			api.Because(api.CodeNotInstalled,
				"this archive has nowhere to write, so it keeps nothing").Retry(false))
	}

	usage := store.Usage()
	facts := api.ArchiveFacts{
		Path:  status.Root,
		Bytes: usage.Bytes,
		Days:  len(usage.Days),
		// Not `LogsSuspended`, which is the size ceiling pausing log capture and calls
		// for a shorter window or Empty, not a look for a full volume.
		WritesRefused: !status.Writable,
		Note:          archiveNote(status, usage),
	}
	if len(usage.Days) > 0 {
		oldest := usage.Days[0].Day
		if parsed, err := time.Parse("2006-01-02", oldest); err == nil {
			facts.OldestAt = &parsed
		}
	}
	return api.Data(facts)
}

// archiveNote is the one sentence beside the figures, worst first: a refused
// write, a suspended capture, then the approximation.
func archiveNote(status archive.Status, usage archive.Usage) string {
	switch {
	case !status.Writable && status.Note != "":
		return status.Note
	case status.LogsSuspended && status.LogsNote != "":
		return status.LogsNote
	case usage.Approximate:
		// The figure a retention change is decided on: a measurement adjusted by writes
		// since, so it says so.
		return "This figure is approximate: it was measured at " +
			usage.ComputedAt.Format("15:04:05") + " and adjusted by writes since."
	case status.LastPruneNote != "":
		return status.LastPruneNote
	}
	return ""
}

// settingsInfoOf is the Infos tab: read-only facts about this installation, from
// `SetupInfo` and `cluster.KubeconfigSurvey`, both held since startup.
func settingsInfoOf(s *Server) api.SettingsInfo {
	// The survey taken at startup, which every client and URL was built from.
	survey := s.setup.Kubeconfig

	info := api.SettingsInfo{
		StatePath: s.setup.StateDir,
		// As the deployment said: there is nothing to measure.
		StatePersistent: !s.setup.StateEphemeral,
		// The whole `--version` line, as one opaque string.
		Version:        s.setup.Version,
		StateWrittenBy: s.setup.StateWrittenBy,
		StateIsNewer:   s.setup.StateIsNewer,
	}
	// A flag, not parsed from `version`: internal/launch is the one place that knows
	// the sentinel.
	info.Development = s.setup.Development

	// Always sent, even when the update check did not run: it is a link.
	info.RepositoryURL = s.setup.RepositoryURL
	info.DocumentationURL = s.setup.DocumentationURL

	info.ListenAddress = s.setup.ListenAddress
	info.WithoutPassword = s.setup.WithoutPassword

	// Asked afresh on every read: the check runs after the port opens, so a value
	// captured at startup would stay the pre-check answer. Nil when nothing checks.
	if s.setup.Updates != nil {
		info.Update = settingsUpdate(s.setup.Updates())
		if s.setup.Installer != nil {
			install := updateInstallOf(s, s.setup.Installer.Status())
			info.Update.Install = &install
		}
	}

	// Every file in precedence order ("first file wins"), the missing ones included,
	// their absence carried in the path since the contract's member is plain
	// strings.
	for _, file := range survey.Files {
		path := file.Path
		switch {
		case file.Absent:
			path += " (not there)"
		case file.Problem != "":
			path += " (" + file.Problem + ")"
		}
		info.KubeconfigFiles = append(info.KubeconfigFiles, path)
	}

	// One entry per shadowed name, worded by `cluster.KubeconfigShadow.Sentence()`,
	// the same sentence the start-up log writes. `keptFrom` and `shadowedIn` let a
	// client name the files without parsing it; of the `Losers`, the first is the
	// one the sentence is about.
	for _, shadow := range survey.Shadowed {
		entry := api.KubeconfigShadow{
			Context:  shadow.Name,
			KeptFrom: shadow.Winner,
			Sentence: shadow.Sentence(),
		}
		if len(shadow.Losers) > 0 {
			entry.ShadowedIn = shadow.Losers[0]
		}
		info.KubeconfigShadowed = append(info.KubeconfigShadowed, entry)
	}
	return info
}

// saveGeneral writes the General tab's three settings. Pointers throughout: a
// nil is "not mentioned", never `false`. The retention is clamped by the archive,
// whose bound it is.
func saveGeneral(store Archives, write api.SettingsWrite) (archive.Settings, error) {
	current := store.Settings()
	next := current
	if write.ArchiveEnabled != nil {
		next.Enabled = *write.ArchiveEnabled
	}
	if write.CaptureLogs != nil {
		next.Logs = *write.CaptureLogs
	}
	if write.RetentionDays != nil {
		next.RetentionDays = *write.RetentionDays
	}
	return store.SetSettings(next)
}

// appliedNote is the sentence about what the write did: the retention was
// clamped; the archive was turned off, which discards nothing (Empty deletes); or
// nothing of note, and then no sentence.
func appliedNote(asked api.SettingsWrite, got archive.Settings) string {
	if asked.RetentionDays != nil && *asked.RetentionDays != got.RetentionDays {
		return "Retention was set to " + strconv.Itoa(got.RetentionDays) +
			" days, which is this build's nearest allowed value to the " +
			strconv.Itoa(*asked.RetentionDays) + " asked for."
	}
	if asked.ArchiveEnabled != nil && !*asked.ArchiveEnabled {
		return "The archive is off. Nothing more will be written and nothing already " +
			"written has been discarded — emptying it is a separate, confirmed act."
	}
	return ""
}

// settingsUpdate is the contract's form of an UpdateReport, for the Infos read
// and Check now alike.
func settingsUpdate(report UpdateReport) *api.SettingsUpdate {
	out := &api.SettingsUpdate{
		State:      report.State,
		Latest:     report.Latest,
		ReleaseURL: report.ReleaseURL,
		Note:       report.Note,
		Checking:   report.Checking,
	}
	// Absent when no request was made, rather than a year-one timestamp.
	if !report.CheckedAt.IsZero() {
		at := report.CheckedAt
		out.CheckedAt = &at
	}
	return out
}

// updateInstallOf is the updater's status with what a restart would end.
func updateInstallOf(s *Server, status updater.Status) api.UpdateInstall {
	out := api.UpdateInstall{
		Phase:        string(status.Phase),
		Enabled:      status.Enabled,
		When:         status.When,
		Password:     status.Password,
		Supported:    status.Supported,
		Installation: string(status.Installation),
		Version:      status.Version,
		Asset:        status.Asset,
		Done:         status.Done,
		Total:        status.Total,
		Reason:       status.Reason,
		Folder:       status.Folder,
		HelperAlive:  status.HelperAlive,
	}
	if s.setup.OpenWork != nil {
		out.Ends.Terminals, out.Ends.Forwards = s.setup.OpenWork()
	}
	return out
}
