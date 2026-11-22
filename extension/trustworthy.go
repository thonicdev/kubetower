package extension

import "github.com/thonicdev/kubetower/internal/kubeletstats"

// Trustworthy decides whether the kubelet's filesystem figures describe a claim
// of the declared size.
//
// The kubelet reports what statfs said about the mount point, and on a network
// filesystem that is the whole share: a 5 GiB claim on a 40 TiB export comes
// back as 30 % full, a stable number about somebody else's disk. Usage's
// figure alone cannot say so, so every page that draws a gauge must ask this.
// It delegates to `internal/kubeletstats`, where the rule and its 10 %
// tolerance live beside the field they qualify, so there is one copy.
func Trustworthy(declaredBytes, measuredBytes int64) bool {
	return kubeletstats.Trustworthy(declaredBytes, measuredBytes)
}
