package events

import (
	"errors"
	"fmt"
	"net/http"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
)

// SuggestAt is how many Warning Events in the last hour make the banner offer
// this page (study Q6).
const SuggestAt = 20

// suggestStop is where the read stops counting: past it the figure is "at
// least", and the rest of a large cluster's Events is not paged for a banner.
const suggestStop = 200

var errEnough = errors.New("enough events counted")

// Suggest is whether the cluster has had many Warning Events this hour, for the
// banner that offers to turn the page on (extension.Suggester). It counts
// Events, not their repeat counts: one pod in a crash loop is one Event.
func (e *Events) Suggest(r *http.Request, h extension.Host, cluster string) (string, error) {
	since := e.now().Add(-lineWindow)
	warnings := 0
	page, err := h.List(r, cluster, extension.ListQuery{GVR: eventsGVR,
		Each: func(u unstructured.Unstructured) error {
			ev := readEvent(&u)
			if ev.typ == "Warning" && !ev.last.Before(since) {
				if warnings++; warnings >= suggestStop {
					return errEnough
				}
			}
			return nil
		}})
	stopped := errors.Is(err, errEnough)
	if err != nil && !stopped {
		return "", err
	}
	if warnings < SuggestAt {
		return "", nil
	}
	floor := ""
	if stopped || !page.Complete {
		floor = "At least "
	}
	return fmt.Sprintf("%s%d Warning events in the last hour. Event Intelligence groups them by cause and shows what changed just before.", floor, warnings), nil
}
