package pending

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// timeCeiling is a load test's time ceiling on this run: its base, measured
// without -race on an idle machine and given room, scaled by what -race costs
// and by how loaded the machine is. A wall-clock ceiling on a machine running
// other work measures the other work; scaling it keeps the ceiling about the
// code without turning it off.
func timeCeiling(t *testing.T, base time.Duration) time.Duration {
	t.Helper()
	load := loadFactor()
	ceiling := time.Duration(float64(base) * float64(raceSlowdown) * load)
	t.Logf("time ceiling %s: base %s x %d for -race x %.2f for the load", ceiling, base, raceSlowdown, load)
	return ceiling
}

// loadFactor is the 1-minute load average per CPU, never below 1: a machine
// with more runnable work than CPUs gives each a share of its time. Where the
// load cannot be read (no /proc), it is 1.
func loadFactor() float64 {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 1
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 1
	}
	load, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 1
	}
	return max(1, load/float64(runtime.NumCPU()))
}
