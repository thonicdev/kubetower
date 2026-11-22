//go:build !race

package netinspect

// raceEnabled: the load test's time ceiling grows under -race.
const raceEnabled = false
