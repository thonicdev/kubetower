//go:build !windows

package launch

// restrictToOwner is a no-op off Windows: the file is created 0600, and the
// mode bits are the permission (tooOpen checks them on every read).
func restrictToOwner(string) error { return nil }
