//go:build !unix

package health

import "os/exec"

// Outside Unix there is no process group to kill here: the re-run is bounded by
// cmd.WaitDelay alone, and a child the plugin left behind keeps running.

func ownGroup(*exec.Cmd) {}

func killGroup(*exec.Cmd) {}
