//go:build !race

package pending

// raceSlowdown: without -race, a load test's time is held to its base ceiling.
const raceSlowdown = 1
