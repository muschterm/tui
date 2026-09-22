package acpbridge

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

func TestCodexStartGuardRejectsStaleAndCancelledDispatch(t *testing.T) {
	for _, mode := range []string{"live", "cancelled", "caller-cancelled", "stale"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			b := newCodex(&host{ctx: context.Background()}).(*codexBridge)
			a := &codexTurn{callCtx: ctx, startToken: "one", threadID: "thread", starting: true}
			b.active = a
			if mode == "cancelled" {
				a.cancel = true
			}
			if mode == "caller-cancelled" {
				cancel()
			}
			if mode == "stale" {
				a.startToken = "two"
			}
			frame := []byte(`{"id":1,"method":"turn/start","params":{"threadId":"thread","_tuiCodexStartGuard":"one","input":[]}}`)
			clean, locked, err := b.lockStartForWrite(frame)
			if locked != nil {
				locked.dispatchMu.Unlock()
			}
			if mode == "live" {
				if err != nil || locked != a || !a.startAttempted || strings.Contains(string(clean), codexStartKey) {
					t.Fatalf("live dispatch guard: locked=%v attempted=%v err=%v frame=%s", locked != nil, a.startAttempted, err, clean)
				}
			} else if err == nil || locked != nil || a.startAttempted {
				t.Fatal("stale or cancelled prompt crossed the dispatch guard")
			}
		})
	}
}

func TestCodexWithdrawalBeforeCallbackRegistrationRejectsLateGrant(t *testing.T) {
	b := newCodex(&host{ctx: context.Background()}).(*codexBridge)
	a := &codexTurn{threadID: "thread", turnID: "turn", started: make(chan struct{}), done: make(chan codexOutcome, 1)}
	b.active = a
	// No pending callback has registered yet: withdrawal must still retire it.
	b.retirePendingApprovals()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := b.guardApprovalChoice(ctx, ctx, cancel, 1, 0, "thread", "turn", &approvalChoice{})
	if err == nil {
		t.Fatal("pre-registration withdrawal allowed a late approval")
	}
	b.mu.Lock()
	retired := b.retired
	b.mu.Unlock()
	if !retired {
		t.Fatal("withdrawn connection remained reusable")
	}
}

func TestCodexLostStartResponseAfterStopRemainsUncertain(t *testing.T) {
	f := startCodexFixture(t, "lost-start")
	f.selectModelAndEffort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := f.prompt(ctx); done <- err }()
	entries := waitCodexRuntimeLog(t, f.logPath, func(entries []codexRuntimeEntry) bool { return codexRequestCount(entries, "turn/start") == 1 })
	for _, entry := range entries {
		if entry.Method == "turn/start" {
			var params map[string]json.RawMessage
			if err := json.Unmarshal(entry.Params, &params); err != nil {
				t.Fatal(err)
			}
			if _, exists := params[codexStartKey]; exists {
				t.Fatal("internal guard leaked to native runtime")
			}
		}
	}
	if err := f.conn.Cancel(ctx, acp.CancelNotification{SessionId: f.session}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.release, []byte("exit without response"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("lost start response became confirmed cancellation")
		}
	case <-ctx.Done():
		t.Fatal("lost native start did not settle behind an error")
	}
}
