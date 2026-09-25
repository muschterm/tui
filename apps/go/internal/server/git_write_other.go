//go:build !unix

package server

import (
	"os"
	"os/exec"
)

func configureGitWriteProcess(*exec.Cmd) {}

func endGitGroup(*exec.Cmd) {}

const (
	tokenAbsent      = "absent"
	tokenBlocked     = "blocked"
	tokenUnavailable = "unavailable"
)

func tokenKind(token string) string { return token }

func worktreeStat(string, string) string { return tokenUnavailable }

type worktreeContent struct {
	file    *os.File
	link    []byte
	symlink bool
}

func openWorktreeContent(string, string, string) (worktreeContent, error) {
	return worktreeContent{}, failure("not_supported", "Git writes are unsupported on this platform")
}

func removeUntracked(string, string, string) error {
	return failure("not_supported", "Git writes are unsupported on this platform")
}
