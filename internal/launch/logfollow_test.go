package launch

import (
	"strings"
	"testing"
	"time"
)

func TestLogFollowMaxDefaultsPerProfile(t *testing.T) {
	t.Setenv("KT_LOG_FOLLOW_MAX", "")
	if got, err := logFollowMax(false); err != nil || got != 2*time.Hour {
		t.Errorf("desktop = %s, %v; want 2h", got, err)
	}
	if got, err := logFollowMax(true); err != nil || got != 30*time.Minute {
		t.Errorf("server = %s, %v; want 30m", got, err)
	}
}

func TestLogFollowMaxVariableAndBounds(t *testing.T) {
	for raw, want := range map[string]time.Duration{
		"90m": 90 * time.Minute, "1m": time.Minute, "24h": 24 * time.Hour, " 3h ": 3 * time.Hour,
	} {
		t.Setenv("KT_LOG_FOLLOW_MAX", raw)
		for _, inCluster := range []bool{false, true} {
			if got, err := logFollowMax(inCluster); err != nil || got != want {
				t.Errorf("%q (inCluster %v) = %s, %v; want %s", raw, inCluster, got, err, want)
			}
		}
	}
	for _, raw := range []string{"59s", "24h1s", "0", "-5m", "soon", "90"} {
		t.Setenv("KT_LOG_FOLLOW_MAX", raw)
		_, err := logFollowMax(false)
		if err == nil || !strings.Contains(err.Error(), "KT_LOG_FOLLOW_MAX") {
			t.Errorf("%q: error %v; want one naming KT_LOG_FOLLOW_MAX", raw, err)
		}
	}
}
