package archive

// What the settings page costs, against the size of the archive.
//
// `RefreshUsage` is a full walk of the tree, and the settings page calls it on
// every render, and the number that matters is how the walk
// scales with the *number of files* rather than with the bytes in them. Events
// are one file per (day, cluster); logs are one file per (day, cluster,
// container), which is where the count comes from once `KT_ARCHIVE_LOGS` is on.
//
//	go test ./internal/archive -run '^$' -bench UsageWalk -benchmem
//
// The shape below is deliberate: 14 clusters, the retention windows the settings
// page offers, and a plausible number of followed containers per cluster per
// day. The bytes are token — a walk stats files, it does not read them.

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// plantArchive builds a tree of the shape the archive really writes: day at the
// top, cluster under it, one events file and `logs` log files per cluster-day.
func plantArchive(b *testing.B, root string, days, clusters, logs int) {
	b.Helper()
	stamp := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	line := []byte("2026-08-18T09:14:22Z something happened in a container\n")

	for d := 0; d < days; d++ {
		day := stamp.AddDate(0, 0, -d).Format("2006-01-02")
		for c := 0; c < clusters; c++ {
			dir := filepath.Join(root, day, "cluster-"+strconv.Itoa(c))
			if err := os.MkdirAll(dir, 0o700); err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, clusterFileName), []byte("CLUSTER-"+strconv.Itoa(c)), 0o600); err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, eventsFileName), line, 0o600); err != nil {
				b.Fatal(err)
			}
			for l := 0; l < logs; l++ {
				name := "ns" + strconv.Itoa(l%20) + "_pod-" + strconv.Itoa(l) + "_app.log"
				if err := os.WriteFile(filepath.Join(dir, name), line, 0o600); err != nil {
					b.Fatal(err)
				}
			}
		}
	}
}

func BenchmarkUsageWalk(b *testing.B) {
	cases := []struct {
		name                 string
		days, clusters, logs int
	}{
		// Events only, which is the default: one file per cluster-day.
		{"events-7d-14c", 7, 14, 0},
		{"events-30d-14c", 30, 14, 0},
		// With log capture on. Twenty followed containers is a busy afternoon;
		// two hundred is a console left open on a large estate.
		{"logs-7d-14c-20", 7, 14, 20},
		{"logs-30d-14c-20", 30, 14, 20},
		{"logs-30d-14c-200", 30, 14, 200},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			root := b.TempDir()
			plantArchive(b, root, tc.days, tc.clusters, tc.logs)
			a := Open(Options{Root: root})
			defer func() { _ = a.Close() }()

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if u := a.RefreshUsage(); u.Files == 0 {
					b.Fatal("the walk found nothing")
				}
			}
		})
	}
}
