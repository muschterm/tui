//go:build !unix

package server

import "os/exec"

func configureGitProcess(*exec.Cmd) {}

func readUntracked(string, string, int) (untrackedFile, error) {
	return untrackedFile{}, failure("unavailable", "untracked file preview is unsupported on this platform")
}
