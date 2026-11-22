package launch

import (
	"testing"

	"github.com/thonicdev/kubetower/internal/statefile"
)

// The kinds column's choices are opened where they last, the extras switch's
// rule, and nowhere else.
func TestTheKindPreferencesAreOpenedOnlyWhereTheyLast(t *testing.T) {
	t.Setenv(statefile.EnvDir, t.TempDir())
	for _, tc := range []struct {
		name                          string
		inCluster, several, persisted bool
		want                          bool
	}{
		{"desktop", false, false, true, true},
		{"desktop told the directory is not kept", false, false, false, true},
		{"one replica with a volume", true, false, true, true},
		{"one replica without a volume", true, false, false, false},
		{"several replicas", true, true, true, false},
	} {
		if got := openKindPrefs(tc.inCluster, tc.several, tc.persisted) != nil; got != tc.want {
			t.Errorf("%s: opened %v, want %v", tc.name, got, tc.want)
		}
	}
}
