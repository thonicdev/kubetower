package web

import (
	"io"

	"github.com/thonicdev/kubetower/internal/archive"
	"github.com/thonicdev/kubetower/textutil"
)

// Archives is the slice of the archive layer the settings need, as an interface
// so they do not depend on a file format. Nil is valid: the archive was never
// built.
type Archives interface {
	Status() archive.Status
	Settings() archive.Settings
	SetSettings(archive.Settings) (archive.Settings, error)
	Usage() archive.Usage
	RefreshUsage() archive.Usage
	Clear() (archive.PruneResult, error)
	// What the log streamer tees into. CapturesLogs is asked per stream, so the
	// setting takes effect on the next panel opened rather than the next restart.
	CapturesLogs() bool
	LogWriter(cluster, namespace, pod, container string) io.WriteCloser
}

const ()

type retentionChoice struct {
	Days    int
	Label   string
	Current bool
}

var retentionOptions = []retentionChoice{
	{Days: 1, Label: "1 day"},
	{Days: 3, Label: "3 days"},
	{Days: 7, Label: "7 days"},
	{Days: 14, Label: "14 days"},
	{Days: 30, Label: "30 days"},
}

// retentionChoicesFor is the dropdown, and it always contains the value it is
// editing. `KT_ARCHIVE_RETENTION_DAYS` may be any of 1 to 90 and the list offers
// five; with no option selected a browser submits the first, and an unrelated
// save would cut the retention to one day. So a value the list lacks is inserted
// in order and marked.
func retentionChoicesFor(current int) []retentionChoice {
	out := make([]retentionChoice, 0, len(retentionOptions)+1)
	inserted := false
	for _, choice := range retentionOptions {
		if !inserted && current > 0 && current < choice.Days {
			out = append(out, retentionChoice{
				Days:    current,
				Label:   plural(current, "day", "days"),
				Current: true,
			})
			inserted = true
		}
		choice.Current = choice.Days == current
		if choice.Current {
			inserted = true
		}
		out = append(out, choice)
	}
	if !inserted && current > 0 {
		out = append(out, retentionChoice{
			Days:    current,
			Label:   plural(current, "day", "days"),
			Current: true,
		})
	}
	return out
}

// humanBytes is textutil.Bytes under this package's name for it.
func humanBytes(n int64) string { return textutil.Bytes(n) }
