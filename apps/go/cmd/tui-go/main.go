package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"unicode"

	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/lifecycle"
	"github.com/muschterm/tui/apps/go/internal/server"
	"github.com/muschterm/tui/apps/go/internal/tui"
	"golang.org/x/sys/unix"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "tui-go:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("tui-go", flag.ContinueOnError)
	clientName := flags.String("client", "", "saved client view name (default: independent random identity)")
	flags.Usage = func() {
		fmt.Fprint(flags.Output(), `Usage: tui-go [--client name]
       tui-go server start|status|stop|run
       tui-go snapshot
       tui-go probe

Starting the TUI attaches to a background server, starting it if needed.
Exiting the TUI leaves the server running. Use server stop to shut it down.
Use --client name to restore that client's saved view; use distinct names
for independent clients. TUI_GO_HOME overrides the default ~/.tui-go home.
The reference currently uses synthetic fixture data, not live agents.

Options:
`)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	args = flags.Args()
	if len(args) == 1 && args[0] == "help" {
		flags.Usage()
		return nil
	}
	if len(args) == 1 && args[0] == "probe" {
		return probe()
	}
	home, err := lifecycle.Home()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		id := *clientName
		if id == "" {
			id = client.ID()
		}
		if len(id) > 128 || strings.ContainsAny(id, "/\\") || strings.IndexFunc(id, unicode.IsControl) >= 0 || id == "." || id == ".." {
			return fmt.Errorf("client name must be 1–128 bytes without path separators or control characters")
		}
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		c, err := lifecycle.Ensure(ctx, home, executable)
		if err != nil {
			return err
		}
		return tui.Run(ctx, c, home, id)
	}
	if len(args) == 1 && args[0] == "snapshot" {
		c, err := lifecycle.Status(ctx, home)
		if err != nil {
			return err
		}
		snapshot, err := c.Snapshot(ctx)
		if err != nil {
			return err
		}
		return printJSON(snapshot)
	}
	if len(args) != 2 || args[0] != "server" {
		return fmt.Errorf("unknown command; use --help")
	}
	switch args[1] {
	case "run":
		return server.Serve(ctx, home)
	case "stop":
		if err := lifecycle.Stop(ctx, home); err != nil {
			return err
		}
		fmt.Println("Server stopped.")
		return nil
	case "start", "status":
		var c *client.Client
		if args[1] == "start" {
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			c, err = lifecycle.Ensure(ctx, home, executable)
			if err != nil {
				return err
			}
		} else {
			c, err = lifecycle.Status(ctx, home)
			if err != nil {
				return err
			}
		}
		// Deliberately whitelist status fields: discovery also holds credentials.
		return printJSON(struct {
			State      string `json:"state"`
			PID        int    `json:"pid"`
			URL        string `json:"url"`
			Home       string `json:"home"`
			InstanceID string `json:"instance_id"`
		}{"running", c.Discovery.PID, c.Discovery.URL, home, c.Discovery.InstanceID})
	default:
		return fmt.Errorf("unknown server command %q; use --help", args[1])
	}
}

func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func probe() error {
	result := struct {
		Runtime      string `json:"runtime"`
		OS           string `json:"os"`
		Arch         string `json:"arch"`
		Term         string `json:"term"`
		TTY          bool   `json:"tty"`
		Columns      int    `json:"columns,omitempty"`
		Rows         int    `json:"rows,omitempty"`
		Enhancements string `json:"enhancements"`
	}{Runtime: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH, Term: os.Getenv("TERM"), Enhancements: "not negotiated; environment values are diagnostic only"}
	if size, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ); err == nil {
		result.TTY, result.Columns, result.Rows = true, int(size.Col), int(size.Row)
	}
	return printJSON(result)
}
