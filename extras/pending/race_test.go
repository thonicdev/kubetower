//go:build race

package pending

// raceSlowdown: what -race multiplies a load test's time by. Measured on
// 2026-11-19 on a 4-core, 8-thread i7: the placement page in 1.8 s without
// it and 9.2 s with it, the large page in 0.5 s and 3.1 s.
const raceSlowdown = 6
