//go:build !unix

package shellenv

import "os/exec"

func ownGroup(*exec.Cmd) {}

func killGroup(*exec.Cmd) {}
