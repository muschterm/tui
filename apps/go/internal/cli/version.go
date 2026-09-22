package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is set by the linker for release builds:
// go build -ldflags "-X github.com/muschterm/tui/apps/go/internal/cli.version=1.2.3".
var version string

// Version reports the build version: the linker-supplied value, else the main
// module version recorded by the Go toolchain, with the VCS revision and a
// -dirty marker when the toolchain stamped them.
func Version() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	v := info.Main.Version
	if v == "" || v == "(devel)" {
		v = "devel"
	}
	var revision, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				modified = "-dirty"
			}
		}
	}
	if revision != "" {
		if len(revision) > 12 {
			revision = revision[:12]
		}
		v += "+" + revision + modified
	}
	return v
}

// versionLine is the version with the Go runtime and platform, shared by
// --version and the version command.
func versionLine() string {
	return fmt.Sprintf("%s (%s %s/%s)", Version(), runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the build version",
		Args:  usage(cobra.NoArgs),
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "%s version %s\n", name, versionLine())
		},
	}
}
