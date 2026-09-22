package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/muschterm/tui/apps/go/internal/protocol"
	"golang.org/x/sys/unix"
)

// The durable command retains the original request for identity comparison;
// only the candidate snapshot receives captures. Lookup precedes this function,
// so retries never read a source a second time.
func captureCommand(parent context.Context, s protocol.Snapshot, c protocol.Command) (protocol.Command, error) {
	if !slices.Contains([]string{"thread.start", "prompt.send", "prompt.reopen-send"}, c.Kind) {
		return c, nil
	}
	if len(c.Attachments) > 8 {
		return c, failure("capacity", "at most eight captured attachments")
	}
	if !usesWorkspaceFiles(c) {
		return c, nil
	}
	base, err := captureRoot(s, c)
	if err != nil {
		return c, err
	}
	if strings.HasPrefix(base, "fixture://") {
		return c, failure("unavailable", "fixture workspace has no local files")
	}
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return c, captureInterrupted(err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return c, failure("attachment", "cannot open checkout: "+err.Error())
	}
	defer root.Close()
	c.Attachments = slices.Clone(c.Attachments)
	for i, a := range c.Attachments {
		if a.Kind != "workspace-file" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return c, captureInterrupted(err)
		}
		if a.Source == "" || !validRelativePath(a.Source) || a.Content != "" {
			return c, attachmentFailure(a.Source, "select a relative workspace file without supplied content")
		}
		// O_NONBLOCK avoids hanging if a selected file becomes a FIFO before open.
		file, err := root.OpenFile(a.Source, os.O_RDONLY|unix.O_NONBLOCK, 0)
		if err != nil {
			return c, attachmentFailure(a.Source, "cannot capture selected file: "+err.Error())
		}
		if insideGit(root, a.Source) {
			_ = file.Close()
			return c, attachmentFailure(a.Source, "select a workspace file outside .git")
		}
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
			_ = file.Close()
			return c, attachmentFailure(a.Source, "attachments must be regular text files up to 64 KiB")
		}
		content, readErr := io.ReadAll(io.LimitReader(file, 65537))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return c, attachmentFailure(a.Source, "cannot read selected file")
		}
		if len(content) > 65536 || !utf8.Valid(content) || strings.ContainsRune(string(content), 0) {
			return c, attachmentFailure(a.Source, "attachments must be UTF-8 text up to 64 KiB")
		}
		a.Kind, a.Name, a.Content = "file", filepath.Base(a.Source), string(content)
		c.Attachments[i] = a
	}
	if err := ctx.Err(); err != nil {
		return c, captureInterrupted(err)
	}
	return c, nil
}

func usesWorkspaceFiles(c protocol.Command) bool {
	return slices.Contains([]string{"thread.start", "prompt.send", "prompt.reopen-send"}, c.Kind) && slices.ContainsFunc(c.Attachments, func(a protocol.Attachment) bool { return a.Kind == "workspace-file" })
}

func captureRoot(s protocol.Snapshot, c protocol.Command) (string, error) {
	req := protocol.BrowseRequest{Scope: "files", ThreadID: c.ThreadID}
	if c.Kind == "thread.start" {
		req.ProjectID, req.ThreadID = c.ProjectID, ""
	}
	return browseRoot(s, req)
}

// A bare context error would surface as a storage failure.
func captureInterrupted(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return failure("attachment", "capture timed out; nothing was sent")
	}
	return failure("attachment", "capture cancelled; nothing was sent")
}

func attachmentFailure(source, message string) error {
	return failure("attachment", fmt.Sprintf("%q: %s", source, message))
}
