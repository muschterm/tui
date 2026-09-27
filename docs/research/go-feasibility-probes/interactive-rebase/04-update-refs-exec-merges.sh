#!/bin/sh
# --update-refs todo lines; exec; --rebase-merges todo shape; hooks.
. "$(dirname "$0")/lib.sh"; repo a b; git branch stack; git commit -q --allow-empty -m c-empty; echo d>d.txt; git add d.txt; git commit -qm d
echo "== default todo with --update-refs (identity todo):"; printf '' > "$T/todo"
GIT_SEQUENCE_EDITOR="$T/show-editor" git $NEUTRAL rebase -i --update-refs HEAD~3 2>&1 | grep -v '^#' | sed '/^$/d' | head; git rebase --abort 2>/dev/null || true
echo "== note: c-empty shown above? (empty commits kept as pick unless --empty/--no-keep-empty)"
echo "== update-refs run: drop b, stack must follow"; A=$(sha HEAD~3)
printf 'label onto\n' >/dev/null; printf 'drop %s\nupdate-ref refs/heads/stack\npick %s\npick %s\n' $(sha HEAD~2) $(sha HEAD~1) $(sha HEAD) > "$T/todo"
rebase --update-refs --keep-empty HEAD~3 2>&1 | tail -2; echo "stack now: $(git log -1 --format=%s stack) ($(sha stack)) main=$(git log --format=%s | tr '\n' ,)"
echo "== exec line (runs arbitrary shell):"; printf 'pick %s\nexec echo EXEC-RAN pwd=$(pwd)\n' $(sha HEAD) > "$T/todo"; rebase HEAD~1 2>&1 | grep EXEC
echo "== hooks: pre-rebase / post-rewrite / prepare-commit-msg / commit-msg"
for h in pre-rebase post-rewrite prepare-commit-msg commit-msg; do printf '#!/bin/sh\necho HOOK %s "$@" >&2\n' $h > .git/hooks/$h; chmod +x .git/hooks/$h; done
printf 'reword %s\n' $(sha HEAD) > "$T/todo"; echo msg > "$T/msgs/000"; rebase HEAD~1 2>&1 | grep HOOK
echo "== with core.hooksPath=/dev/null:"; echo msg2 > "$T/msgs/000"; printf "reword %s\n" $(sha HEAD) > "$T/todo"; GIT_SEQUENCE_EDITOR="$T/seq-editor" GIT_EDITOR="$T/msg-editor" git $NEUTRAL -c core.hooksPath=/dev/null rebase -i HEAD~1 >"$T/out" 2>&1 || true; tail -2 "$T/out"; echo "hook lines: $(grep -c HOOK "$T/out" || true)"; git rebase --abort 2>/dev/null || true; rm -f .git/hooks/*
echo "== --rebase-merges todo shape:"; git checkout -q -b side HEAD~1; echo s>s.txt; git add s.txt; git commit -qm side; git checkout -q main; git merge -q --no-ff -m merge side
GIT_SEQUENCE_EDITOR="$T/show-editor" git $NEUTRAL rebase -i --rebase-merges HEAD~3 2>&1 | grep -v '^#' | sed '/^$/d'; git rebase --abort 2>/dev/null || true
echo "== without --rebase-merges, merge commit is linearised away:"; GIT_SEQUENCE_EDITOR="$T/show-editor" git $NEUTRAL rebase -i HEAD~3 2>&1 | grep -v '^#' | sed '/^$/d'; git rebase --abort 2>/dev/null || true
