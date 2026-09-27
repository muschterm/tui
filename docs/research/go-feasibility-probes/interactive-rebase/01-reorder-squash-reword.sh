#!/bin/sh
# Reorder + squash + fixup -C + reword + drop via sequence/message helpers.
SHARED=0; . "$(dirname "$0")/lib.sh"; repo a b c d e
A=$(sha HEAD~4); B=$(sha HEAD~3); C=$(sha HEAD~2); D=$(sha HEAD~1); E=$(sha HEAD)
printf 'pick %s\nreword %s\nsquash %s\npick %s\nfixup -C %s\n' $A $D $B $C $E > "$T/todo"  # e dropped? no: fixup -C keeps E message
printf 'reworded d\n' > "$T/msgs/000"; printf 'd+b squashed\n' > "$T/msgs/001"
rebase --root && echo "exit=0"; git log --format='%h %s' ; ls "$T/msgs" | wc -l | sed 's/^/messages left: /'
echo "--- drop via missing line with missingCommitsCheck=error:"
printf 'pick %s\n' $(git rev-parse --short HEAD~2) > "$T/todo"
rebase HEAD~2 2>&1 | tail -6 || true; git rebase --abort 2>/dev/null || true; git log --oneline | head -3
