//go:build !unix

package acpbridge

import "os/exec"

func configureProcess(cmd *exec.Cmd)             {}
func terminateProcess(cmd *exec.Cmd, force bool) { _ = cmd.Process.Kill() }
