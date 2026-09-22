package server

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func TestCaptureInterruptionReportsAttachmentFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes"), []byte("text"), 0600); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for message, ctx := range map[string]context.Context{"capture cancelled": cancelled, "capture timed out": expired} {
		e := testEngine(t)
		e.snap.Projects[0].Path = root
		command := initialSend(e.snap)
		command.Attachments = []protocol.Attachment{{Kind: "workspace-file", Source: "notes"}}
		before := clone(e.snap)
		_, err := e.commandContext(ctx, command)
		if !isCode(err, "attachment") || !strings.Contains(err.Error(), message) {
			t.Fatalf("%s reported as %v", message, err)
		}
		if r, lookupErr := e.store.Lookup(command); lookupErr != nil || r != nil || !reflect.DeepEqual(before, e.snap) {
			t.Fatal("interrupted capture changed state")
		}
		// The preserved draft can be sent again under the same identity.
		if _, err = e.commandContext(context.Background(), command); err != nil {
			t.Fatal(err)
		}
	}
}
