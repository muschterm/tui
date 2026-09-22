// Command tui-go is the Go reference application: a background server that
// owns projects, threads and fixture activity, and a terminal UI that attaches
// to it. Package cli defines the command surface.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/muschterm/tui/apps/go/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Main(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
