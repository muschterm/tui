package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/muschterm/tui/apps/go/internal/storage"
	"golang.org/x/sys/unix"
)

// The durable command retains the original request for identity comparison;
// only the candidate snapshot receives captures. Lookup precedes this function,
// so retries never read a source a second time. Artifact references are
// resolved from the store's records; nothing is bound until the command's
// transaction commits.
func captureCommand(parent context.Context, s protocol.Snapshot, c protocol.Command, store *storage.Store) (protocol.Command, error) {
	if !slices.Contains([]string{"thread.start", "prompt.send", "prompt.reopen-send"}, c.Kind) {
		return c, nil
	}
	if len(c.Attachments) > 8 {
		return c, failure("capacity", "at most eight captured attachments")
	}
	if usesArtifacts(c) {
		var err error
		if c, err = captureArtifacts(s, c, store); err != nil {
			return c, err
		}
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
		if a.Content != "" {
			return c, attachmentFailure(a.Source, "select a relative workspace file without supplied content")
		}
		content, err := readWorkspaceFile(root, a.Source)
		if err != nil {
			return c, err
		}
		sum := sha256.Sum256(content)
		digest := hex.EncodeToString(sum[:])
		a.Kind, a.Name, a.Content = "file", filepath.Base(a.Source), string(content)
		a.Size, a.SHA256 = int64(len(content)), digest
		// A differing preview is identified on the accepted capture, not blocked.
		a.ChangedSincePreview = a.PreviewSHA256 != "" && a.PreviewSHA256 != digest
		c.Attachments[i] = a
	}
	if err := ctx.Err(); err != nil {
		return c, captureInterrupted(err)
	}
	return c, nil
}

// readWorkspaceFile applies the Send capture rules shared with draft preview:
// a confined relative path outside .git naming a regular UTF-8 text file up to
// 64 KiB without NUL. It only reads.
func readWorkspaceFile(root *os.Root, source string) ([]byte, error) {
	if source == "" || !validRelativePath(source) {
		return nil, attachmentFailure(source, "select a relative workspace file without supplied content")
	}
	// O_NONBLOCK avoids hanging if a selected file becomes a FIFO before open.
	file, err := root.OpenFile(source, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, attachmentFailure(source, "cannot capture selected file: "+err.Error())
	}
	if insideGit(root, source) {
		_ = file.Close()
		return nil, attachmentFailure(source, "select a workspace file outside .git")
	}
	info, statErr := file.Stat()
	if statErr != nil || !info.Mode().IsRegular() || info.Size() > captureLimit {
		_ = file.Close()
		return nil, attachmentFailure(source, "attachments must be regular text files up to 64 KiB")
	}
	content, readErr := io.ReadAll(io.LimitReader(file, captureLimit+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, attachmentFailure(source, "cannot read selected file")
	}
	if !capturableText(content) {
		return nil, attachmentFailure(source, "attachments must be UTF-8 text up to 64 KiB")
	}
	return content, nil
}

// captureLimit bounds inline text captures in snapshots.
const captureLimit = 65536

func capturableText(content []byte) bool {
	return len(content) <= captureLimit && utf8.Valid(content) && !bytes.ContainsRune(content, 0)
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
