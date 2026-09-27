package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// The editor helpers of an application interactive rebase (ADR 0026).
//
// Git runs its sequence editor once, with the todo list it generated, and
// its commit editor for reword, the last squash of a chain, `fixup -c`, and
// when a continue commits a stopped step. The server points
// GIT_SEQUENCE_EDITOR and GIT_EDITOR at this binary in a hidden mode
// (`tui-go git-rebase-helper sequence|message <file>`), and passes the
// operation's state file (in the application home, never the repository)
// and a random token in the environment. The helper refuses to run unless
// the token matches the state file and the file Git hands it lies in that
// operation's Git directory.
//
//   - sequence: Git's generated todo must name exactly the commits and
//     update-ref branches the server pinned (abbreviated hashes are
//     matched by prefix, since Git abbreviates them for the editor); the
//     helper then replaces the todo with the pinned one (full hashes, the
//     user's actions and order). On any mismatch it fails, and Git does not
//     start the rebase.
//   - message: the step is the last line of rebase-merge/done, which must
//     be the pinned todo line at that position. The message is the
//     server's override for that step (written before every Continue:
//     the Continue's Message, else the message Git would have used
//     without the stop), else the plan's stored message for that line.
//     Git's prepared editor text is never used, since removing its
//     comments would also remove the user's own `#` lines; without a
//     message the step fails.
//
// A failure is written to helper-error next to the state file, which the
// server reads after Git returns, and the helper exits non-zero, which Git
// reports as a problem with the editor.

const (
	rebaseHelperCommand = "git-rebase-helper"
	rebaseStateEnv      = "TUI_GO_REBASE_STATE"
	rebaseTokenEnv      = "TUI_GO_REBASE_TOKEN"
	rebaseStateVersion  = 1
	rebaseStateName     = "plan.json"
	rebaseOverrideName  = "override.json"
	rebaseHelperError   = "helper-error"
	rebaseStateMax      = 128 << 20
	rebaseTodoFileMax   = 16 << 20
)

// rebaseState is the per-operation file the helpers read.
type rebaseState struct {
	Version      int          `json:"version"`
	OperationID  string       `json:"operation_id"`
	Token        string       `json:"token"`
	GitDir       string       `json:"git_dir"`
	Expected     []string     `json:"expected"`
	ExpectedRefs []string     `json:"expected_refs,omitempty"`
	Todo         string       `json:"todo"`
	Lines        []rebaseLine `json:"lines"`
}

// rebaseLine is one pinned todo line: Command is the full command
// ("fixup -C" included), Oid the commit (or the ref of an update-ref), Entry
// the plan entry (-1 for update-ref).
type rebaseLine struct {
	Command    string `json:"command"`
	Oid        string `json:"oid,omitempty"`
	Entry      int    `json:"entry"`
	EditMode   string `json:"edit_mode,omitempty"`
	Message    string `json:"message,omitempty"`
	HasMessage bool   `json:"has_message,omitempty"`
}

// rebaseOverride is a Continue's message for one step.
type rebaseOverride struct {
	Step    int    `json:"step"`
	Oid     string `json:"oid"`
	Message string `json:"message"`
}

// RunRebaseHelper is the hidden helper mode; args are the mode and the file
// Git passes. It reads its state from the environment.
func RunRebaseHelper(args []string) error {
	statePath, token := os.Getenv(rebaseStateEnv), os.Getenv(rebaseTokenEnv)
	if statePath == "" || token == "" || !filepath.IsAbs(statePath) {
		return errors.New("git-rebase-helper runs only under an application rebase")
	}
	st, err := readRebaseState(statePath)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(st.Token), []byte(token)) != 1 {
		return errors.New("git-rebase-helper: the token does not match this rebase")
	}
	fail := func(err error) error {
		if err == nil {
			return nil
		}
		_ = os.WriteFile(filepath.Join(filepath.Dir(statePath), rebaseHelperError), []byte(err.Error()), 0o600)
		return err
	}
	if len(args) != 2 {
		return fail(errors.New("the helper needs a mode and a file"))
	}
	file, err := filepath.Abs(args[1])
	if err != nil {
		return fail(err)
	}
	switch args[0] {
	case "sequence":
		return fail(helperSequence(st, file))
	case "message":
		return fail(helperMessage(st, filepath.Dir(statePath), file))
	}
	return fail(fmt.Errorf("unknown helper mode %q", args[0]))
}

func readRebaseState(p string) (*rebaseState, error) {
	b, err := readBounded(p, rebaseStateMax)
	if err != nil {
		return nil, fmt.Errorf("the rebase plan could not be read: %w", err)
	}
	var st rebaseState
	if err := json.Unmarshal(b, &st); err != nil || st.Version != rebaseStateVersion || st.GitDir == "" {
		return nil, errors.New("the rebase plan is damaged or from another version")
	}
	return &st, nil
}

// readBounded reads a regular file of at most limit bytes without
// following a FIFO.
func readBounded(p string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > limit {
		return nil, errors.New("not a regular file of bounded size")
	}
	return io.ReadAll(io.LimitReader(f, limit))
}

// within reports whether file is a regular file (not a symlink) in dir
// (both made absolute, symlinks in dir and file's parent resolved).
func within(dir, file string) bool {
	if fi, err := os.Lstat(file); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	d, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(file))
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(d, filepath.Join(parent, filepath.Base(file)))
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

// todoCommand splits a todo line into its full command and argument;
// abbreviated commands are expanded. ok is false for blank and comment
// lines.
func todoCommand(line string) (cmd, arg string, ok bool) {
	f := strings.Fields(line)
	if len(f) == 0 || strings.HasPrefix(f[0], "#") {
		return "", "", false
	}
	full := map[string]string{"p": "pick", "r": "reword", "e": "edit", "s": "squash", "f": "fixup", "d": "drop", "b": "break", "x": "exec", "u": "update-ref", "l": "label", "t": "reset", "m": "merge"}
	cmd = f[0]
	if long, abbreviated := full[cmd]; abbreviated {
		cmd = long
	}
	rest := f[1:]
	if cmd == "fixup" && len(rest) > 0 && (rest[0] == "-C" || rest[0] == "-c") {
		cmd, rest = "fixup "+rest[0], rest[1:]
	}
	if len(rest) > 0 {
		arg = rest[0]
	}
	return cmd, arg, true
}

// oidMatches reports whether a (possibly abbreviated) hash names full.
func oidMatches(abbrev, full string) bool {
	return len(abbrev) >= 4 && strings.HasPrefix(full, strings.ToLower(abbrev))
}

func helperSequence(st *rebaseState, file string) error {
	want := filepath.Join(st.GitDir, "rebase-merge", "git-rebase-todo")
	if !within(filepath.Join(st.GitDir, "rebase-merge"), file) || filepath.Base(file) != filepath.Base(want) {
		return errors.New("the sequence helper was handed an unexpected file")
	}
	b, err := readBounded(file, rebaseTodoFileMax)
	if err != nil {
		return fmt.Errorf("the todo list could not be read: %w", err)
	}
	remaining := map[string]bool{}
	for _, oid := range st.Expected {
		remaining[oid] = true
	}
	refs := map[string]bool{}
	for _, r := range st.ExpectedRefs {
		refs[r] = true
	}
	for _, line := range strings.Split(string(b), "\n") {
		cmd, arg, ok := todoCommand(line)
		if !ok {
			continue
		}
		switch cmd {
		case "pick":
			match := ""
			for oid := range remaining {
				if oidMatches(arg, oid) {
					if match != "" {
						return fmt.Errorf("the generated todo names %s ambiguously", arg)
					}
					match = oid
				}
			}
			if match == "" {
				return fmt.Errorf("the generated todo names %s, which is not a pinned commit (or appears twice); the branch or its commits changed", arg)
			}
			delete(remaining, match)
		case "update-ref":
			if !refs[arg] {
				return fmt.Errorf("the rebase would also move %s, which the plan did not list", arg)
			}
			delete(refs, arg)
		default:
			return fmt.Errorf("the generated todo contains an unexpected %q line", cmd)
		}
	}
	if len(remaining) > 0 || len(refs) > 0 {
		return fmt.Errorf("the generated todo lacks %d pinned commits and %d branches; the branch or its commits changed", len(remaining), len(refs))
	}
	return os.WriteFile(file, []byte(st.Todo), 0o600)
}

func helperMessage(st *rebaseState, stateDir, file string) error {
	if !within(st.GitDir, file) {
		return errors.New("the message helper was handed a file outside the repository's Git directory")
	}
	done, err := readBounded(filepath.Join(st.GitDir, "rebase-merge", "done"), rebaseTodoFileMax)
	if err != nil {
		return errors.New("the rebase's progress could not be read")
	}
	step, cmd, arg := 0, "", ""
	for _, line := range strings.Split(string(done), "\n") {
		if c, a, ok := todoCommand(line); ok {
			step, cmd, arg = step+1, c, a
		}
	}
	if step < 1 || step > len(st.Lines) {
		return fmt.Errorf("the rebase is at step %d, which the plan does not have", step)
	}
	line := st.Lines[step-1]
	if line.Command != cmd || !oidMatches(arg, line.Oid) {
		return fmt.Errorf("the rebase runs %q at step %d, not the planned %q", cmd+" "+arg, step, line.Command+" "+line.Oid)
	}
	var message string
	if b, err := readBounded(filepath.Join(stateDir, rebaseOverrideName), rebaseStateMax); err == nil {
		var o rebaseOverride
		if json.Unmarshal(b, &o) == nil && o.Step == step && o.Oid == line.Oid && o.Message != "" {
			message = o.Message
		}
	}
	if message == "" && line.HasMessage {
		message = line.Message
	}
	if strings.TrimSpace(message) == "" {
		// Git accepts a blank message under verbatim cleanup; a step never
		// gets one here.
		return fmt.Errorf("no message is stored for step %d (%s %s)", step, line.Command, shortOid(line.Oid))
	}
	if !strings.HasSuffix(message, "\n") {
		message += "\n"
	}
	return os.WriteFile(file, []byte(message), 0o600)
}
