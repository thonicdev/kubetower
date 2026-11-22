package launch

import (
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/extras"
)

// The start-up line for "no extra is on" says how to turn one on in the words
// the refusal in internal/web ends with - extras.HowToTurnOn - and in no others.
//
// Two sentences that each spell the instruction on their own drift apart when
// one is corrected. Equality with the helper, not a list of words, is what
// keeps the two together.
func TestTheStartupLineSaysHowToTurnOnInTheHelpersWords(t *testing.T) {
	// The core registers no extra, and with an empty catalog the line says so
	// instead; the sentence under test needs at least one page that could be on.
	extras.Register(extras.Extra{Key: "alfa", Label: "Alfa page", Summary: "A fixture page.", Needs: "nothing"})
	t.Cleanup(extras.Reset)
	t.Setenv(extras.EnvExtras, "")

	contexts := []string{"alpha", "beta"}
	store := extras.New(t.TempDir(), contexts, nil)

	for _, profile := range []extras.Profile{extras.Desktop, extras.Server, extras.Replicated, extras.Unkept} {
		got := extrasSummary(store, contexts, profile)
		want := "none. " + extras.HowToTurnOn("<cluster>", "", profile)
		if got != want {
			t.Errorf("profile %d: the start-up line is\n  %q\nwant\n  %q", profile, got, want)
		}
		if strings.Contains(got, "docker-compose") {
			t.Errorf("profile %d: the start-up line names docker-compose, which does not exist: %q", profile, got)
		}
	}
}

// The start-up line names the profile of the start-up's own inputs, and the
// last of them is KT_STATE_PERSISTENT's answer - persistent, the opposite of
// ProfileOf's ephemeral. Spelled out for all eight combinations rather than
// through ProfileOf, and once more with the variable unset (persistent).
//
// What openProfile passes is inCluster, the replicas and readStatePersistent's
// answer, so the polarity is extrasStartupProfile's alone, and this test holds
// it there - from the environment, through the same reader openProfile uses: a
// helper that passed the state's persistence where ProfileOf wants its
// ephemerality, or always false, fails here - the server profile with no
// state volume would name a switch that is not registered, and with one would
// say there is none.
func TestTheStartupLineNamesTheProfileOfItsInputs(t *testing.T) {
	for _, c := range startupCombinations {
		if got := extrasStartupProfile(c.inCluster, c.several, readStatePersistent(c.env.get)); got != c.want {
			t.Errorf("extrasStartupProfile(inCluster=%v, several=%v, %s) = %d, want %d",
				c.inCluster, c.several, c.env, got, c.want)
		}
	}
}

// The line openProfile logs is extrasStartupLine's, and it goes through
// extrasStartupProfile: a line built from any other profile - ProfileOf called
// directly with a constant, or a polarity of its own - says a different
// sentence for at least one combination below, and fails here. That openProfile
// logs this function's line, and not one it builds itself, is
// openprofile_test.go's.
func TestTheStartupLineSaysTheSentenceOfThatProfile(t *testing.T) {
	extras.Register(extras.Extra{Key: "alfa", Label: "Alfa page", Summary: "A fixture page.", Needs: "nothing"})
	t.Cleanup(extras.Reset)
	t.Setenv(extras.EnvExtras, "")

	contexts := []string{"alpha"}
	store := extras.New(t.TempDir(), contexts, nil)

	for _, c := range startupCombinations {
		got := extrasStartupLine(store, contexts, c.inCluster, c.several, readStatePersistent(c.env.get))
		want := "none. " + extras.HowToTurnOn("<cluster>", "", c.want)
		if got != want {
			t.Errorf("extrasStartupLine(inCluster=%v, several=%v, %s) is\n  %q\nwant\n  %q",
				c.inCluster, c.several, c.env, got, want)
		}
	}
}

// stateEnv is an environment holding KT_STATE_PERSISTENT, or not holding it
// at all when empty.
type stateEnv string

func (e stateEnv) get(key string) string {
	if key == envStatePersistent {
		return string(e)
	}
	return ""
}

func (e stateEnv) String() string {
	if e == "" {
		return envStatePersistent + " unset"
	}
	return envStatePersistent + "=" + string(e)
}

var startupCombinations = []struct {
	inCluster, several bool
	env                stateEnv
	want               extras.Profile
}{
	{false, false, "true", extras.Desktop},
	{false, false, "false", extras.Desktop},
	{false, true, "true", extras.Replicated},
	{false, true, "false", extras.Replicated},
	{true, false, "true", extras.Server},
	{true, false, "false", extras.Unkept},
	{true, true, "true", extras.Replicated},
	{true, true, "false", extras.Replicated},
	{true, false, "", extras.Server},
}

// **A fresh context has every extra off on every profile**, asserted on the
// store each profile actually gets - one
// assertion per extra registered - and under the server profile KT_EXTRAS
// names what may be turned on rather than pinning it on: an extra the values
// name is still off until somebody turns it on.
func TestEveryProfileOpensTheStoreWithEveryExtraOff(t *testing.T) {
	keys := []string{"alfa", "bravo", "charlie"}
	for _, key := range keys {
		extras.Register(extras.Extra{Key: key, Label: key + " page", Summary: "A fixture page.", Needs: "nothing"})
	}
	t.Cleanup(extras.Reset)
	contexts := []string{"alpha"}

	for name, inCluster := range map[string]bool{"desktop and container": false, "server": true} {
		t.Run(name, func(t *testing.T) {
			if inCluster {
				// The chart's values name every extra: allowed is not on.
				t.Setenv(extras.EnvExtras, strings.Join(keys, ","))
			} else {
				t.Setenv(extras.EnvExtras, "")
			}
			store := extras.New(t.TempDir(), contexts, nil, extrasOptions(inCluster)...)
			if store.IsInCluster() != inCluster {
				t.Errorf("IsInCluster = %v, want %v", store.IsInCluster(), inCluster)
			}
			for _, key := range keys {
				if store.EnabledOn("alpha", key) {
					t.Errorf("%s is on on a cluster nobody chose anything on", key)
				}
				if store.Pinned(key) {
					t.Errorf("%s is pinned", key)
				}
			}
		})
	}
}
