#!/bin/sh
# edit stops; detect state; amend content; continue. Also break.
. "$(dirname "$0")/lib.sh"; repo a b c
B=$(sha HEAD~1); C=$(sha HEAD)
printf 'edit %s\nbreak\npick %s\n' $B $C > "$T/todo"
rebase HEAD~2 || true; echo "== after edit stop"; state
echo extra >> b.txt; git add b.txt; git $NEUTRAL commit -q --amend --no-edit
git $NEUTRAL rebase --continue || true; echo "== after break stop"; state
git $NEUTRAL rebase --continue; echo "== done"; git log --format='%h %s' ; git show --stat --format= HEAD~1
