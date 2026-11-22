package kubeletstats

import "testing"

// The NFS trap, stated as the case that must not regress.
//
// Every row here is a real storage shape rather than an arithmetic edge. On one
// cluster, five claims of 4Gi, 5Gi, 8Gi, 8Gi and 10Gi can all have their kubelets
// report the identical `2.92 GiB used of 9.76 GiB` — one NFS export, five names,
// one stable and entirely plausible 30 %. A claim on a block device
// (Longhorn, 8Gi declared against a 7.78 GiB
// filesystem, a 2.75 % gap) is believed, which is the row below called "ext4's
// own overhead".
func TestASharedExportIsNotThisClaimsFilesystem(t *testing.T) {
	const (
		gib = int64(1) << 30
		tib = int64(1) << 40
	)
	for name, c := range map[string]struct {
		declared, measured int64
		want               bool
		why                string
	}{
		"a 500 MiB claim on a 40 TiB NAS": {
			500 << 20, 40 * tib, false,
			"this is the whole point: statfs() returned the share, and 30% of a NAS " +
				"drawn under a 500 MiB claim is stable, plausible and about somebody else's disk",
		},
		"a 5 GiB claim on a 40 TiB pool": {5 * gib, 40 * tib, false, "the CephFS shape"},
		"a block device of the size asked for": {
			5 * gib, 5 * gib, true,
			"an exact match is the easy case and must still pass",
		},
		"ext4's own overhead": {
			5 * gib, 5*gib - 260*(1<<20), true,
			"ext4 reserves 5% for root and the inode tables are not free, so an " +
				"equality test would reject every honest volume there is",
		},
		"just inside 10%": {1000, 901, true, "the boundary is inclusive"},
		"just outside 10%": {
			1000, 899, false,
			"one byte past the tolerance is a different filesystem",
		},
		"measured smaller by more than a tenth": {
			10 * gib, 1 * gib, false,
			"a subpath onto a sized partition: as much a mismatch as one that is larger, " +
				"and the direction is why the comparison is on the absolute gap",
		},
		"nothing declared": {
			0, 5 * gib, false,
			"an unbound claim has no size to compare against, so there is nothing to believe",
		},
		"nothing measured": {
			5 * gib, 0, false,
			"a kubelet that gave no capacity has not measured anything; a zero whole is " +
				"the one value that would make a gauge divide by nothing",
		},
		"both absent": {0, 0, false, ""},
		"negative":    {-1, -1, false, "not a size"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := Trustworthy(c.declared, c.measured); got != c.want {
				t.Errorf("Trustworthy(%d declared, %d measured) = %v, want %v — %s",
					c.declared, c.measured, got, c.want, c.why)
			}
		})
	}
}
