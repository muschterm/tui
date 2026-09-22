package acpbridge

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

type dispatchCounter struct{ writes atomic.Int32 }

func (w *dispatchCounter) Write(p []byte) (int, error) { w.writes.Add(1); return len(p), nil }
func (*dispatchCounter) Close() error                  { return nil }

func TestClaudeCallerCancellationBeforeDispatchSendsNothing(t *testing.T) {
	for _, prompt := range []bool{false, true} {
		t.Run(map[bool]string{false: "configuration", true: "prompt"}[prompt], func(t *testing.T) {
			writer := &dispatchCounter{}
			c := newClaude(&host{ctx: context.Background()}).(*claude)
			c.p = &process{stdin: writer, ctx: context.Background()}
			c.model = "test-model"
			ctx, cancel := context.WithCancel(context.Background())
			c.writeMu.Lock()
			done := make(chan error, 1)
			go func() {
				if prompt {
					_, err := c.prompt(ctx, []json.RawMessage{json.RawMessage(`{"type":"text","text":"must not execute"}`)})
					if err != nil {
						done <- err
					} else {
						done <- nil
					}
				} else {
					_, err := c.control(ctx, map[string]any{"subtype": "set_model", "model": "test-model"})
					done <- err
				}
			}()
			cancel()
			c.writeMu.Unlock()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cancelled call succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled call remained blocked")
			}
			if writer.writes.Load() != 0 {
				t.Fatal("cancelled work crossed native dispatch")
			}
		})
	}
}
