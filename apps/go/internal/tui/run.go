package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/lifecycle"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Run owns only client I/O. Leaving it never stops the background server.
func Run(ctx context.Context, c *client.Client, home, id string) error {
	snapshot, err := c.Snapshot(ctx)
	if err != nil {
		return err
	}
	stored, err := c.LoadView(ctx, id)
	if err != nil {
		return fmt.Errorf("load client view: %w", err)
	}
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := New(c, id, snapshot, stored.Data)
	m.setEnvIcons(os.Getenv("TUI_GO_ICONS"))
	m.ctx = watchCtx
	m.writer = &viewWriter{id: id, revision: stored.Revision, generation: m.state.Generation, acknowledged: append([]byte(nil), stored.Data...)}
	m.writer.connection.Store(c)
	options := m.terminalColorOptions(os.Getenv)
	options = append(options, tea.WithContext(ctx))
	p := tea.NewProgram(pace(m), options...)
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection := c
		for {
			if watchCtx.Err() != nil {
				return
			}
			err := connection.Watch(watchCtx, func(s protocol.Snapshot) { p.Send(snapshotMsg(s)) })
			if watchCtx.Err() != nil {
				return
			}
			p.Send(connectionMsg{err: err})
			for {
				select {
				case <-watchCtx.Done():
					return
				case <-time.After(time.Second):
				}
				probe, stop := context.WithTimeout(watchCtx, 2*time.Second)
				next, e := lifecycle.Status(probe, home)
				stop()
				if e == nil {
					connection = next
					p.Send(connectionMsg{client: next})
					break
				}
			}
		}
	}()
	final, runErr := p.Run()
	cancel()
	<-done
	// Final synchronous flush follows cancellation of background saves. Saved
	// state is scoped to this client, so another client's navigation is untouched.
	// Every exit, including cancellation, a program error or a recovered
	// panic (final may then not be the model), deletes each kitty image id
	// this client may have transmitted. The terminal is restored by now and
	// the APC deletes are invisible; repeating one after Quit is harmless.
	if seq := m.imgs().Close(); seq != "" {
		_, _ = os.Stdout.WriteString(seq)
	}
	if result, ok := final.(*pacedModel); ok {
		state := result.model
		state.viewState().Draft = state.prompt.Value()
		state.markDirty()
		encoded, e := json.Marshal(state.state)
		if e != nil {
			return e
		}
		if e = state.writer.save(encoded); e != nil {
			path, exportErr := exportRecovery(home, encoded)
			if exportErr != nil {
				return fmt.Errorf("final save: %w; recovery export also failed: %v", e, exportErr)
			}
			return fmt.Errorf("final draft/layout save failed: %w; recoverable view exported to %s", e, path)
		}
	}
	return runErr
}

// This export is client recovery data, not an authoritative server write.
func exportRecovery(home string, data []byte) (string, error) {
	dir := filepath.Join(home, "recovery")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "view-*.json")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if _, err = f.Write(data); err != nil {
		f.Close()
		return name, err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return name, err
	}
	return name, f.Close()
}
