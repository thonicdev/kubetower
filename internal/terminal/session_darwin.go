//go:build darwin

package terminal

import "golang.org/x/sys/unix"

// sessionMembers is every process whose session is leader's, read with sysctl
// kern.proc.all since macOS has no /proc: the jobs a shell started, whatever
// their process group.
func sessionMembers(leader int) []int {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil
	}
	var members []int
	for _, p := range procs {
		pid := int(p.Proc.P_pid)
		if pid <= 0 || pid == leader {
			continue
		}
		if sid, err := unix.Getsid(pid); err == nil && sid == leader {
			members = append(members, pid)
		}
	}
	return members
}
