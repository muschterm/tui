package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/lifecycle"
	"github.com/muschterm/tui/apps/go/internal/server"
)

// statusReport is the JSON printed by server start and status. Discovery also
// holds the bearer token, so fields are listed explicitly rather than encoding
// the whole record.
type statusReport struct {
	State      string `json:"state"`
	PID        int    `json:"pid"`
	URL        string `json:"url"`
	Home       string `json:"home"`
	InstanceID string `json:"instance_id"`
}

func newServerCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Start, inspect or stop the background server",
		Long: `Manage the background server for the selected application home. One server
owns each home. The TUI starts it on demand, and it keeps running after every
client detaches until it is stopped explicitly.`,
		Args: usage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "start",
			Short: "Start the server if it is not running and report its status",
			Args:  usage(cobra.NoArgs),
			RunE: func(cmd *cobra.Command, _ []string) error {
				home, err := opts.resolveHome()
				if err != nil {
					return err
				}
				executable, err := os.Executable()
				if err != nil {
					return err
				}
				c, err := lifecycle.Ensure(cmd.Context(), home, executable)
				if err != nil {
					return err
				}
				return printJSON(cmd.OutOrStdout(), report(home, c))
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "Report whether the server for this home is reachable",
			Long: `Print the reachable server as JSON and exit 0. When no server answers, the
command fails and says whether discovery was absent or stale.`,
			Args: usage(cobra.NoArgs),
			RunE: func(cmd *cobra.Command, _ []string) error {
				home, err := opts.resolveHome()
				if err != nil {
					return err
				}
				c, err := lifecycle.Status(cmd.Context(), home)
				if err != nil {
					return describeUnreachable(home, err)
				}
				return printJSON(cmd.OutOrStdout(), report(home, c))
			},
		},
		&cobra.Command{
			Use:   "stop",
			Short: "Gracefully stop the server and confirm its shutdown",
			Long: `Ask the running server to cancel owned work, save state and exit, then wait
for its recorded shutdown outcome. Modified workspace files are never rolled
back.`,
			Args: usage(cobra.NoArgs),
			RunE: func(cmd *cobra.Command, _ []string) error {
				home, err := opts.resolveHome()
				if err != nil {
					return err
				}
				if err := lifecycle.Stop(cmd.Context(), home); err != nil {
					return describeStopError(home, err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Server stopped.")
				return nil
			},
		},
		&cobra.Command{
			Use:   "run",
			Short: "Run the server in the foreground",
			Long: `Run the server in this process until it is interrupted or stopped. The TUI
and "server start" launch this in the background with output in the home's
server.log; run it directly to watch that output.`,
			Args: usage(cobra.NoArgs),
			RunE: func(cmd *cobra.Command, _ []string) error {
				home, err := opts.resolveHome()
				if err != nil {
					return err
				}
				return server.Serve(cmd.Context(), home)
			},
		},
	)
	return cmd
}

func newSnapshotCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "snapshot",
		Short: "Print the running server's state snapshot as JSON",
		Long: `Fetch the authoritative state snapshot from the running server and print it
as JSON. This is a diagnostic view of server-owned state, not a client view.`,
		Args: usage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := opts.resolveHome()
			if err != nil {
				return err
			}
			c, err := lifecycle.Status(cmd.Context(), home)
			if err != nil {
				return describeUnreachable(home, err)
			}
			snapshot, err := c.Snapshot(cmd.Context())
			if err != nil {
				return err
			}
			return printJSON(cmd.OutOrStdout(), snapshot)
		},
	}
}

func report(home string, c *client.Client) statusReport {
	return statusReport{State: "running", PID: c.Discovery.PID, URL: c.Discovery.URL, Home: home, InstanceID: c.Discovery.InstanceID}
}

// describeUnreachable explains a failed server probe in terms of the home.
func describeUnreachable(home string, err error) error {
	if errors.Is(err, client.ErrNoServer) {
		return fmt.Errorf("no server is running for %s", home)
	}
	return fmt.Errorf("server for %s is not reachable (stale discovery or shutting down): %w", home, err)
}

func describeStopError(home string, err error) error {
	if errors.Is(err, client.ErrNoServer) {
		return describeUnreachable(home, err)
	}
	return fmt.Errorf("could not stop server for %s: %w", home, err)
}
