package web

import (
	"testing"

	"github.com/thonicdev/kubetower/internal/detail"
)

// A fact's copy value reaches the payload's `copy`, which is what draws the
// button; a fact without one arrives without it.
func TestAFactsCopyValueReachesThePayload(t *testing.T) {
	sections := sectionsOf(detail.Object{Facts: []detail.Fact{
		{Label: "Pod IP", Value: "10.244.1.7", Copy: "10.244.1.7"},
		{Label: "Node IP", Value: ""},
		{Label: "Name", Value: "web-0"},
	}})
	fields := sections[0].Fields
	if fields[0].Copy != "10.244.1.7" {
		t.Errorf("Pod IP arrived with copy %q", fields[0].Copy)
	}
	if fields[1].Copy != "" || fields[1].Value != "" {
		t.Errorf("an empty address arrived as %+v", fields[1])
	}
	if fields[2].Copy != "" {
		t.Errorf("Name arrived copyable: %q", fields[2].Copy)
	}
}
