package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func execute(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = Main(context.Background(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestHelpListsCommands(t *testing.T) {
	code, out, errOut := execute(t, "--help")
	if code != ExitSuccess || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	for _, want := range []string{"server", "snapshot", "probe", "version", "completion", "--client", "--home", "TUI_GO_HOME"} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q:\n%s", want, out)
		}
	}
}

func TestServerHelpListsLifecycle(t *testing.T) {
	code, out, _ := execute(t, "server", "--help")
	if code != ExitSuccess {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"start", "status", "stop", "run"} {
		if !strings.Contains(out, "\n  "+want) {
			t.Errorf("server help lacks %q:\n%s", want, out)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{"unknown command", []string{"bogus"}, []string{`unknown command "bogus"`, "Run 'tui-go --help'"}},
		{"unknown flag", []string{"--bogus"}, []string{"unknown flag: --bogus", "Run 'tui-go --help'"}},
		{"unknown server command", []string{"server", "bogus"}, []string{`unknown command "bogus" for "tui-go server"`, "Run 'tui-go server --help'"}},
		{"extra argument", []string{"version", "extra"}, []string{`unknown command "extra" for "tui-go version"`}},
		{"invalid client name", []string{"--client", "a/b"}, []string{"path separators", "Run 'tui-go --help'"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := execute(t, tc.args...)
			if code != ExitUsage {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
			}
			if !strings.HasPrefix(errOut, "tui-go: ") {
				t.Errorf("stderr lacks program prefix: %q", errOut)
			}
			for _, want := range tc.want {
				if !strings.Contains(errOut, want) {
					t.Errorf("stderr lacks %q: %q", want, errOut)
				}
			}
		})
	}
}

func TestVersion(t *testing.T) {
	code, out, _ := execute(t, "version")
	if code != ExitSuccess || !strings.HasPrefix(out, "tui-go version ") || !strings.Contains(out, "go1.") {
		t.Fatalf("exit %d, output %q", code, out)
	}
	flagCode, flagOut, _ := execute(t, "--version")
	if flagCode != ExitSuccess || flagOut != out {
		t.Fatalf("--version differs: exit %d, %q vs %q", flagCode, flagOut, out)
	}
}

func TestCompletionScripts(t *testing.T) {
	for shell, marker := range map[string]string{"bash": "tui-go", "zsh": "#compdef tui-go", "fish": "complete -c tui-go", "powershell": "tui-go"} {
		code, out, errOut := execute(t, "completion", shell)
		if code != ExitSuccess || errOut != "" || !strings.Contains(out, marker) {
			t.Errorf("%s: exit %d, stderr %q, marker %q missing in %d bytes", shell, code, errOut, marker, len(out))
		}
	}
}

func TestStatusWithoutServerFails(t *testing.T) {
	home := t.TempDir()
	for _, args := range [][]string{{"server", "status"}, {"server", "stop"}, {"snapshot"}} {
		code, out, errOut := execute(t, append([]string{"--home", home}, args...)...)
		if code != ExitFailure || out != "" || !strings.Contains(errOut, "no server is running for "+home) {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, code, out, errOut)
		}
	}
}

func TestProbeIsJSON(t *testing.T) {
	code, out, _ := execute(t, "probe")
	if code != ExitSuccess || !strings.HasPrefix(out, "{\n") || !strings.Contains(out, `"runtime": "go1.`) {
		t.Fatalf("exit %d, output %q", code, out)
	}
}

func TestResolveHome(t *testing.T) {
	t.Setenv("TUI_GO_HOME", "/tmp/from-env")
	got, err := (&options{}).resolveHome()
	if err != nil || got != "/tmp/from-env" {
		t.Fatalf("env home %q, %v", got, err)
	}
	got, err = (&options{home: "relative"}).resolveHome()
	if err != nil || !filepath.IsAbs(got) || filepath.Base(got) != "relative" {
		t.Fatalf("flag home %q, %v", got, err)
	}
}

func TestValidateClientName(t *testing.T) {
	for _, ok := range []string{"desk", "laptop-2", "ünïcödé", strings.Repeat("a", 128)} {
		if err := validateClientName(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, "tab\there", strings.Repeat("a", 129)} {
		if validateClientName(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
