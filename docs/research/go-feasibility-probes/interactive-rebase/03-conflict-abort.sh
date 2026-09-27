#!/bin/sh
# Reorder causing conflict: state files, then abort restores branch and HEAD.
. "$(dirname "$0")/lib.sh"; repo a b c
before=$(git rev-parse HEAD); B=$(sha HEAD~1); C=$(sha HEAD)
printf 'pick %s\npick %s\n' $C $B > "$T/todo"
rebase HEAD~2 || echo "rebase exit nonzero"; echo "== conflict stop"; state; git diff --name-only --diff-filter=U
git rebase --abort; echo "== after abort: branch=$(git symbolic-ref --short HEAD) head_restored=$([ $(git rev-parse HEAD) = $before ] && echo yes || echo no)"
echo "== squash of first line is rejected:"; printf 'squash %s\npick %s\n' $B $C > "$T/todo"; rebase HEAD~2 2>&1 | tail -3 || true
git rebase --abort 2>/dev/null || true; echo "HEAD unchanged=$([ $(git rev-parse HEAD) = $before ] && echo yes || echo no)"
echo "== unknown commit in todo:"; printf 'pick deadbeef\npick %s\npick %s\n' $B $C > "$T/todo"; rebase HEAD~2 2>&1 | tail -3 || true
git rebase --abort 2>/dev/null || true
