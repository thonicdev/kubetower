package kubeletstats

// Trustworthy decides whether the kubelet's filesystem figures describe a claim
// of the declared size.
//
// The kubelet reports `statfs()` of the mount point, which on a network
// filesystem is the whole share: a 5 GiB claim on a 40 TiB NFS export would
// read as that export's figures. So they are used only when the measured size
// is within 10 % of the declared one, in either direction (smaller is a subpath
// mount onto a sized partition). Not an exact match, because a filesystem's own
// overhead (ext4 reserves 5 %) makes an honest volume report slightly less.
func Trustworthy(declared, measured int64) bool {
	if declared <= 0 || measured <= 0 {
		return false
	}
	gap := declared - measured
	if gap < 0 {
		gap = -gap
	}
	return gap*10 <= declared
}
