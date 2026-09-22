// Package cli defines the tui-go command line: the terminal UI launcher,
// server lifecycle commands, diagnostics, help and shell completion. It owns
// argument parsing, output and exit codes; behavior lives in the packages it
// calls.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/lifecycle"
	"github.com/muschterm/tui/apps/go/internal/tui"
)

// Process exit codes. Usage errors exit 2, as the Go flag package and most
// Unix tools do, so scripts can tell a mistyped invocation from a failure.
const (
	ExitSuccess = 0
	ExitFailure = 1
	ExitUsage   = 2
)

const name = "tui-go"

// Main executes the command line with args (excluding the program name) and
// returns the process exit code. Errors go to stderr prefixed with the program
// name; usage errors add a pointer to the relevant --help.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if args == nil {
		args = []string{}
	}
	root := New()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitSuccess
	}
	var usage *usageError
	if errors.As(err, &usage) {
		fmt.Fprintf(stderr, "%s: %v\nRun '%s --help' for usage.\n", name, err, usage.command.CommandPath())
		return ExitUsage
	}
	fmt.Fprintf(stderr, "%s: %v\n", name, err)
	return ExitFailure
}

// options carries the parsed global and launcher flags.
type options struct {
	home   string
	client string
}

// New builds the root command with its subcommands. The root itself opens the
// TUI; cobra adds help and completion commands.
func New() *cobra.Command {
	opts := &options{}
	root := &cobra.Command{
		Use:   name,
		Short: "Go reference terminal UI with a background server",
		Long: `tui-go is the Go reference application: one background server per
application home that owns projects, threads and fixture activity, plus a
terminal UI that attaches to it.

Running tui-go with no command starts the server if needed and opens the TUI.
Exiting the TUI leaves the server running; "tui-go server stop" shuts it down.
Simultaneous launches get independent views; pass --client NAME to restore a
named client's saved view across launches.

The application home defaults to ~/.tui-go and holds state.sqlite, discovery
and log files. Set --home or TUI_GO_HOME to use another directory; different
homes are separate servers. The reference currently uses synthetic fixture
data, not live agents.`,
		Version:       versionLine(),
		Args:          usage(cobra.NoArgs),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTUI(cmd, opts)
		},
	}
	root.PersistentFlags().StringVar(&opts.home, "home", "", "application home directory (default $TUI_GO_HOME, else ~/.tui-go)")
	root.Flags().StringVarP(&opts.client, "client", "c", "", "saved client view name (default: a new independent identity)")
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return &usageError{command: cmd, err: err}
	})
	root.AddCommand(newServerCommand(opts), newSnapshotCommand(opts), newProbeCommand(), newVersionCommand())
	return root
}

// runTUI ensures the home's server is running and attaches the terminal UI.
func runTUI(cmd *cobra.Command, opts *options) error {
	home, err := opts.resolveHome()
	if err != nil {
		return err
	}
	id := opts.client
	if id == "" {
		id = client.ID()
	} else if err := validateClientName(id); err != nil {
		return &usageError{command: cmd, err: err}
	}
	if !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()) {
		return fmt.Errorf("the TUI needs an interactive terminal on stdin and stdout; use '%s server start' or '%s snapshot' from scripts", name, name)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	c, err := lifecycle.Ensure(cmd.Context(), home, executable)
	if err != nil {
		return err
	}
	return tui.Run(cmd.Context(), c, home, id)
}

// resolveHome returns the absolute application home: the --home flag, else
// TUI_GO_HOME, else ~/.tui-go.
func (o *options) resolveHome() (string, error) {
	if o.home != "" {
		return filepath.Abs(o.home)
	}
	return lifecycle.Home()
}

// validateClientName rejects names that cannot serve as a stored view key or
// appear safely in file names and logs.
func validateClientName(id string) error {
	switch {
	case id == "" || id == "." || id == "..":
		return fmt.Errorf("client name %q is empty or reserved", id)
	case len(id) > 128:
		return errors.New("client name is longer than 128 bytes")
	case strings.ContainsAny(id, `/\`) || strings.IndexFunc(id, unicode.IsControl) >= 0:
		return errors.New("client name must not contain path separators or control characters")
	}
	return nil
}

// usageError marks a mistyped invocation so Main exits with ExitUsage and
// points at that command's help.
type usageError struct {
	command *cobra.Command
	err     error
}

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

// usage wraps a positional-argument validator so its failures become usage
// errors. cobra's own validators report unknown subcommands through it.
func usage(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := validate(cmd, args); err != nil {
			return &usageError{command: cmd, err: err}
		}
		return nil
	}
}

// printJSON writes value as indented JSON followed by a newline.
func printJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
