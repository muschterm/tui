package protocol

import (
	"strings"
	"testing"
)

func TestValidateRebasePlan(t *testing.T) {
	oid := func(c byte) string { return strings.Repeat(string(c), 40) }
	a, b, c, m := oid('a'), oid('b'), oid('c'), oid('d')
	plan := GitRebasePlan{Commits: []GitRebasePlanCommit{{Oid: a, Subject: "A"}, {Oid: m, Subject: "merge", Merge: true}, {Oid: b, Subject: "B"}, {Oid: c, Subject: "C"}}}
	e := func(action, commit, message string) GitRebaseEntry {
		return GitRebaseEntry{Action: action, Commit: commit, Message: message}
	}
	fixup := func(commit, variant, message string) GitRebaseEntry {
		return GitRebaseEntry{Action: GitRebaseFixup, Commit: commit, Fixup: variant, Message: message}
	}
	for _, tc := range []struct {
		name    string
		entries []GitRebaseEntry
		code    string
	}{
		{"all picks", []GitRebaseEntry{e("pick", a, ""), e("pick", b, ""), e("pick", c, "")}, ""},
		{"reorder, reword, drop, break", []GitRebaseEntry{e("pick", c, ""), {Action: "break"}, e("reword", a, "A2"), e("drop", b, "")}, ""},
		{"squash chain message on the last", []GitRebaseEntry{e("pick", a, ""), e("squash", b, ""), fixup(c, "", "ABC")}, ""},
		{"fixup chain without message", []GitRebaseEntry{e("edit", a, ""), fixup(b, "", ""), fixup(c, "C", "")}, ""},
		{"fixup -c needs the message", []GitRebaseEntry{e("pick", a, ""), fixup(b, "c", ""), e("pick", c, "")}, "message_required"},
		{"fixup -c with message", []GitRebaseEntry{e("pick", a, ""), fixup(b, "c", "AB"), e("pick", c, "")}, ""},
		{"message on a non-final squash", []GitRebaseEntry{e("pick", a, ""), e("squash", b, "x"), e("squash", c, "y")}, "invalid_plan"},
		{"message on a fixup-only chain", []GitRebaseEntry{e("pick", a, ""), fixup(b, "", "x"), e("pick", c, "")}, "invalid_plan"},
		{"message on pick", []GitRebaseEntry{e("pick", a, "x"), e("pick", b, ""), e("pick", c, "")}, "invalid_plan"},
		{"reword without message", []GitRebaseEntry{e("reword", a, ""), e("pick", b, ""), e("pick", c, "")}, "message_required"},
		{"squash first", []GitRebaseEntry{e("squash", a, "x"), e("pick", b, ""), e("pick", c, "")}, "invalid_plan"},
		{"squash after drop", []GitRebaseEntry{e("pick", a, ""), e("drop", b, ""), e("squash", c, "x")}, "invalid_plan"},
		{"squash after break", []GitRebaseEntry{e("pick", a, ""), {Action: "break"}, e("squash", b, "x"), e("pick", c, "")}, "invalid_plan"},
		{"missing commit", []GitRebaseEntry{e("pick", a, ""), e("pick", b, "")}, "invalid_plan"},
		{"duplicate commit", []GitRebaseEntry{e("pick", a, ""), e("pick", b, ""), e("pick", c, ""), e("pick", a, "")}, "invalid_plan"},
		{"merge commit entry", []GitRebaseEntry{e("pick", a, ""), e("pick", m, ""), e("pick", b, ""), e("pick", c, "")}, "invalid_plan"},
		{"unknown commit", []GitRebaseEntry{e("pick", a, ""), e("pick", b, ""), e("pick", c, ""), e("pick", oid('e'), "")}, "invalid_plan"},
		{"abbreviated hash", []GitRebaseEntry{e("pick", a[:12], ""), e("pick", b, ""), e("pick", c, "")}, "invalid_plan"},
		{"exec is not supported", []GitRebaseEntry{e("exec", "", ""), e("pick", a, ""), e("pick", b, ""), e("pick", c, "")}, "invalid_plan"},
		{"break with a commit", []GitRebaseEntry{e("break", a, ""), e("pick", b, ""), e("pick", c, "")}, "invalid_plan"},
		{"edit mode on pick", []GitRebaseEntry{{Action: "pick", Commit: a, EditMode: "amend"}, e("pick", b, ""), e("pick", c, "")}, "invalid_plan"},
		{"whitespace message", []GitRebaseEntry{e("reword", a, " \n"), e("pick", b, ""), e("pick", c, "")}, "invalid_plan"},
		{"empty plan", nil, "invalid_plan"},
		{"break first", []GitRebaseEntry{{Action: "break"}, e("pick", a, ""), e("pick", b, ""), e("pick", c, "")}, ""},
		{"drop everything", []GitRebaseEntry{e("drop", a, ""), e("drop", b, ""), e("drop", c, "")}, ""},
		{"fixup -C takes no message", []GitRebaseEntry{e("pick", a, ""), fixup(b, "C", "x"), e("pick", c, "")}, "invalid_plan"},
		{"reword head and squash end", []GitRebaseEntry{e("reword", a, "A2"), e("squash", b, ""), e("squash", c, "ABC")}, ""},
		{"fixup -c mid, squash end", []GitRebaseEntry{e("pick", a, ""), fixup(b, "c", ""), e("squash", c, "ABC")}, ""},
		{"fixup -c mid needs the end message", []GitRebaseEntry{e("pick", a, ""), fixup(b, "c", ""), fixup(c, "", "")}, "message_required"},
		{"two chains", []GitRebaseEntry{e("pick", a, ""), fixup(b, "", ""), e("edit", c, "")}, ""},
		{"message on drop", []GitRebaseEntry{e("pick", a, ""), e("drop", b, "x"), e("pick", c, "")}, "invalid_plan"},
		{"fixup variant on squash", []GitRebaseEntry{e("pick", a, ""), {Action: "squash", Commit: b, Fixup: "C", Message: "x"}, e("pick", c, "")}, "invalid_plan"},
		{"unknown edit mode", []GitRebaseEntry{{Action: "edit", Commit: a, EditMode: "split"}, e("pick", b, ""), e("pick", c, "")}, "invalid_plan"},
		{"uppercase hash", []GitRebaseEntry{e("pick", strings.ToUpper(a), ""), e("pick", b, ""), e("pick", c, "")}, "invalid_plan"},
		{"NUL in message", []GitRebaseEntry{e("reword", a, "x\x00y"), e("pick", b, ""), e("pick", c, "")}, "invalid_plan"},
		{"invalid UTF-8 message", []GitRebaseEntry{e("reword", a, "x\xffy"), e("pick", b, ""), e("pick", c, "")}, "invalid_plan"},
		{"message too long", []GitRebaseEntry{e("reword", a, strings.Repeat("x", GitRebaseMessageMax+1)), e("pick", b, ""), e("pick", c, "")}, "invalid_plan"},
	} {
		err := ValidateRebasePlan(plan, tc.entries)
		switch {
		case tc.code == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.code != "" && (err == nil || err.Code != tc.code):
			t.Errorf("%s: want %s, got %v", tc.name, tc.code, err)
		}
	}
}

func TestValidateRebasePlanLimitsBreaks(t *testing.T) {
	a := strings.Repeat("a", 40)
	plan := GitRebasePlan{Commits: []GitRebasePlanCommit{{Oid: a}}}
	entries := []GitRebaseEntry{{Action: "pick", Commit: a}}
	for range GitRebaseBreaksMax + 1 {
		entries = append(entries, GitRebaseEntry{Action: "break"})
	}
	if err := ValidateRebasePlan(plan, entries); err == nil || err.Code != "invalid_plan" {
		t.Fatalf("too many breaks: %v", err)
	}
	if err := ValidateRebasePlan(plan, entries[:GitRebaseBreaksMax+1]); err != nil {
		t.Fatalf("breaks at the limit: %v", err)
	}
}
