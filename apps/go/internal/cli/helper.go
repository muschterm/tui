package cli

import (
	"github.com/spf13/cobra"

	"github.com/muschterm/tui/apps/go/internal/server"
)

// newRebaseHelperCommand is the hidden editor helper that Git runs during an
// application interactive rebase (ADR 0026). It is not a user command: it
// refuses to run without the server's per-operation environment.
func newRebaseHelperCommand() *cobra.Command {
	return &cobra.Command{
		Use:                "git-rebase-helper MODE FILE",
		Short:              "Editor helper for application interactive rebases (internal)",
		Hidden:             true,
		DisableFlagParsing: true,
		SilenceUsage:       true,
		RunE: func(_ *cobra.Command, args []string) error {
			return server.RunRebaseHelper(args)
		},
	}
}
