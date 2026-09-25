//go:build !unix

package server

import (
	"context"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// filesSupported gates the files-read capability.
const filesSupported = false

var errFilesUnsupported = failure("unavailable", "file browsing is unsupported on this platform")

func listFiles(context.Context, string, string, string, bool) (protocol.FileList, error) {
	return protocol.FileList{}, errFilesUnsupported
}

func readFile(context.Context, string, string) (protocol.FileRead, error) {
	return protocol.FileRead{}, errFilesUnsupported
}

func statFile(context.Context, string, string) (protocol.FileStat, error) {
	return protocol.FileStat{}, errFilesUnsupported
}
