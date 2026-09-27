# Shared helpers for interactive-rebase probes. Source only.
set -eu
HERE=$(cd "$(dirname "$0")" && pwd)
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT
export GIT_CONFIG_NOSYSTEM=1 HOME="$T/home" XDG_CONFIG_HOME="$T/home/.config"; mkdir -p "$HOME"
export GIT_AUTHOR_NAME=p GIT_AUTHOR_EMAIL=p@x GIT_COMMITTER_NAME=p GIT_COMMITTER_EMAIL=p@x
# Neutralising overrides a server would pass on every rebase invocation.
NEUTRAL="-c core.editor=false -c sequence.editor= -c rebase.autoSquash=false -c rebase.autoStash=false \
 -c rebase.updateRefs=false -c rebase.missingCommitsCheck=error -c rebase.abbreviateCommands=false \
 -c rebase.instructionFormat=%s -c commit.gpgSign=false -c rebase.rescheduleFailedExec=false -c advice.waitingForEditor=false -c rerere.enabled=false"
repo() { : "${SHARED:=1}"; git init -q -b main "$T/r"; cd "$T/r"; for n in "$@"; do [ "$SHARED" = 1 ] && echo "$n" >> f; echo "$n" > "$n.txt"; git add -A; git commit -qm "$n"; done; }
sha() { git rev-parse --short "$1"; }
# Sequence-editor helper: replaces the todo with the file named by $PIN_TODO.
cat > "$T/seq-editor" <<'H'
#!/bin/sh
printf '%s\n' "--- git proposed todo:" >&2; grep -v '^#' "$1" | sed '/^$/d' >&2
cp "$PIN_TODO" "$1"
H
# Message helper: pops the next stored message from $MSG_DIR (000, 001, ...).
cat > "$T/msg-editor" <<'H'
#!/bin/sh
n=$(ls "$MSG_DIR" | head -1); [ -n "$n" ] || { echo "no stored message" >&2; exit 1; }
printf '%s\n' "--- editor invoked for $(basename "$1"); first lines:" >&2; head -3 "$1" >&2
cp "$MSG_DIR/$n" "$1"; rm "$MSG_DIR/$n"
H
chmod +x "$T/seq-editor" "$T/msg-editor"; mkdir -p "$T/msgs"
export MSG_DIR="$T/msgs" PIN_TODO="$T/todo"
rebase() { GIT_SEQUENCE_EDITOR="$T/seq-editor" GIT_EDITOR="$T/msg-editor" git $NEUTRAL rebase -i "$@"; }
state() { d=$(git rev-parse --git-path rebase-merge); echo "--- state files:"; [ -d "$d" ] && ls "$d" | tr '\n' ' '; echo
  for f in stopped-sha amend done git-rebase-todo; do [ -f "$d/$f" ] && { echo "[$f]"; sed 's/^/  /' "$d/$f"; }; done
  echo "REBASE_HEAD: $(git rev-parse --short -q --verify REBASE_HEAD || echo none)"; git status --porcelain=v2 --branch | head -3; }
# Show-only sequence editor: prints Git's generated todo and fails (rebase does not start).
printf '#!/bin/sh\ngrep -v "^#" "$1" | sed "/^$/d" >&2; exit 1\n' > "$T/show-editor"; chmod +x "$T/show-editor"
