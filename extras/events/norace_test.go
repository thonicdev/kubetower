//go:build !race

package events

// raceEnabled: the load test's time and heap ceilings do not hold under -race,
// whose instrumentation costs several times both.
const raceEnabled = false
