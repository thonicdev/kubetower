package web

import (
	"testing"

	"github.com/thonicdev/kubetower/internal/detail"
)

// On a controller the Events panel carries the events of what it owns,
// and each such row has to say which object it is about,
// or a pod's BackOff reads as the Deployment's own. internal/detail fills
// `About`; this is the mapping that puts it on the wire, and the half of
// `TestTheDetailPageCarriesEveryMemberTheContractDeclares` that a pod fixture
// cannot reach.
func TestAnOwnedObjectsEventSaysWhichObjectItIsAbout(t *testing.T) {
	events := eventsOf([]detail.Event{
		{Reason: "ScalingReplicaSet", Message: "Scaled up replica set api-7d9f to 1"},
		{Reason: "BackOff", Message: "Back-off restarting failed container",
			About: &detail.Owned{Kind: "Pod", Resource: "pods", Name: "api-7d9f-x2k"}},
	})
	if len(events) != 2 {
		t.Fatalf("%d events mapped, want 2", len(events))
	}
	if events[0].About != nil {
		t.Errorf("the object's own event carries about = %+v; it is about the object on screen", events[0].About)
	}
	about := events[1].About
	if about == nil {
		t.Fatal("the owned pod's event carries no about, so the panel draws a pod's BackOff as the Deployment's")
	}
	if about.Kind != "pods" || about.Title != "Pod" || about.Name != "api-7d9f-x2k" {
		t.Errorf("about = %+v, want kind pods (the address), title Pod (the reading), name api-7d9f-x2k", *about)
	}
}
