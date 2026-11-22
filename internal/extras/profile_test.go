package extras

import "testing"

// Which switches exist, for every combination the console can start in: the
// server profile's with one replica and a state volume, none with several
// replicas or with no state volume, the desktop's everywhere else. Several
// replicas outside a cluster are refused at start-up, and are named Replicated
// here all the same, since no per-cluster write is registered there either.
func TestProfileOfNamesTheSwitchesOfEachStart(t *testing.T) {
	for _, c := range []struct {
		inCluster, several, ephemeral bool
		want                          Profile
	}{
		{false, false, false, Desktop},
		{false, false, true, Desktop},
		{false, true, false, Replicated},
		{false, true, true, Replicated},
		{true, false, false, Server},
		{true, false, true, Unkept},
		{true, true, false, Replicated},
		{true, true, true, Replicated},
	} {
		if got := ProfileOf(c.inCluster, c.several, c.ephemeral); got != c.want {
			t.Errorf("ProfileOf(inCluster=%v, several=%v, ephemeral=%v) = %d, want %d",
				c.inCluster, c.several, c.ephemeral, got, c.want)
		}
	}
}

// The per-cluster write exists on the desktop and under the server profile
// with one replica and its state volume, and nowhere else. The router and the
// web listing both read Switchable, so the rule is written once in production
// code. It is spelled again, on purpose, in the api and web tests
// (TestTheExtensionWritesFollowTheProfile in internal/api,
// TestTheExtensionSwitchIsTheProfiles in internal/web), so that a wrong
// Switchable fails them as well as this one.
func TestOnlyTheDesktopAndTheServerProfileHaveTheSwitch(t *testing.T) {
	for p, want := range map[Profile]bool{Desktop: true, Server: true, Replicated: false, Unkept: false} {
		if got := p.Switchable(); got != want {
			t.Errorf("Profile(%d).Switchable() = %t, want %t", p, got, want)
		}
	}
}
