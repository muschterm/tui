package cli

import (
	"os"
	"runtime"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
)

// probeReport is the diagnostic environment summary printed by probe.
type probeReport struct {
	Version      string `json:"version"`
	Runtime      string `json:"runtime"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	Term         string `json:"term"`
	TTY          bool   `json:"tty"`
	Columns      int    `json:"columns,omitempty"`
	Rows         int    `json:"rows,omitempty"`
	Enhancements string `json:"enhancements"`
}

func newProbeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "probe",
		Short: "Print runtime and terminal environment diagnostics as JSON",
		Long: `Print the build, Go runtime, platform, TERM and, when standard output is a
terminal, its size. Values come from the environment only; they do not show
that any terminal feature is supported.`,
		Args: usage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return printJSON(cmd.OutOrStdout(), probe(os.Stdout))
		},
	}
}

func probe(out *os.File) probeReport {
	result := probeReport{
		Version:      Version(),
		Runtime:      runtime.Version(),
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		Term:         os.Getenv("TERM"),
		Enhancements: "not negotiated; environment values are diagnostic only",
	}
	if size, err := unix.IoctlGetWinsize(int(out.Fd()), unix.TIOCGWINSZ); err == nil {
		result.TTY, result.Columns, result.Rows = true, int(size.Col), int(size.Row)
	}
	return result
}
