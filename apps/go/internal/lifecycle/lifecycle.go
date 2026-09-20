// Package lifecycle starts and discovers one background server per application home.
package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func Home() (string, error) {
	home := os.Getenv("TUI_GO_HOME")
	if home == "" {
		u, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		home = filepath.Join(u, ".tui-go")
	}
	return filepath.Abs(home)
}
func Status(ctx context.Context, home string) (*client.Client, error) {
	d, err := client.Discover(home)
	if err != nil {
		return nil, err
	}
	c := client.New(d)
	_, err = c.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return c, nil
}
func Ensure(ctx context.Context, home, executable string) (*client.Client, error) {
	if c, err := Status(ctx, home); err == nil {
		return c, nil
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		return nil, err
	}
	log, err := os.OpenFile(filepath.Join(home, "server.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	defer log.Close()
	cmd := exec.Command(executable, "server", "run")
	cmd.Env = append(os.Environ(), "TUI_GO_HOME="+home)
	cmd.Stdin = nil
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	go func() { _ = cmd.Wait() }()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, fmt.Errorf("server did not become ready; inspect %s", filepath.Join(home, "server.log"))
		case <-ticker.C:
			probe, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
			c, err := Status(probe, home)
			cancel()
			if err == nil {
				return c, nil
			}
		}
	}
}
func Stop(ctx context.Context, home string) error {
	c, err := Status(ctx, home)
	if err != nil {
		return err
	}
	if err = c.Stop(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("shutdown completion not confirmed")
		case <-ticker.C:
			d, err := client.Discover(home)
			if os.IsNotExist(err) {
				return confirmedShutdown(home, c.Discovery.InstanceID)
			}
			if err == nil && d.InstanceID != c.Discovery.InstanceID {
				return confirmedShutdown(home, c.Discovery.InstanceID)
			}
		}
	}
}

func confirmedShutdown(home, instance string) error {
	b, err := os.ReadFile(filepath.Join(home, "shutdown-"+instance+".json"))
	if err != nil {
		return fmt.Errorf("shutdown outcome uncertain: %w", err)
	}
	var result protocol.ShutdownOutcome
	if err = json.Unmarshal(b, &result); err != nil {
		return fmt.Errorf("shutdown outcome uncertain: %w", err)
	}
	if result.InstanceID != instance {
		return fmt.Errorf("shutdown outcome uncertain: server identity mismatch")
	}
	if !result.Success {
		return fmt.Errorf("server shutdown failed: %s", result.Error)
	}
	return nil
}
