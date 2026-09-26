#!/usr/bin/env bash
# Partial-staging feasibility probe (2026-09-26). Evidence, not app code.
# Usage: probe.sh [workdir]   (default: a fresh mktemp dir). Prints one
# "== case" block per scenario with the commands' observable results.
set -u
W=${1:-$(mktemp -d)}
export GIT_CONFIG_NOSYSTEM=1 HOME=$W/home GIT_AUTHOR_NAME=p GIT_AUTHOR_EMAIL=p@x \
  GIT_COMMITTER_NAME=p GIT_COMMITTER_EMAIL=p@x
mkdir -p "$HOME"
git --version
LOG=$W/hooklog

newrepo() { # newrepo name -> cd into fresh repo with logging hooks
  R=$W/$1; rm -rf "$R"; git init -q -b main "$R"; cd "$R" || exit 1
  for h in pre-commit post-index-change post-checkout reference-transaction pre-applypatch post-applypatch; do
    printf '#!/bin/sh\necho "%s $*" >>%s\n' "$h" "$LOG" >.git/hooks/$h; chmod +x .git/hooks/$h
  done
}
hooks() { if [ -s "$LOG" ]; then echo "hooks: $(tr '\n' ';' <"$LOG")"; else echo "hooks: none"; fi; : >"$LOG"; }
case_() { echo; echo "== $*"; : >"$LOG"; }
st() { git ls-files -s -- "$@"; }
seq10() { for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do echo "l$i"; done; }

# Keep header + the Nth hunk (1-based) of a patch on stdin.
hunk() { awk -v n="$1" '/^@@/{k++} k==0||k==n'; }

case_ "A1 whole-hunk stage: apply --cached of hunk 2 only"
newrepo a1; seq10 >f; git add f; git commit -qm i
sed -i 's/^l2$/L2/; s/^l18$/L18/' f
git diff f | hunk 2 >p; : >"$LOG"; git apply --cached --check p && git apply --cached p; echo "exit=$?"; hooks
git diff --cached --stat; git diff --cached | grep '^[-+]l\|^[-+]L'; hooks

case_ "B1 same result via hash-object -w --no-filters + update-index --cacheinfo"
git reset -q; seq10 | sed 's/^l18$/L18/' >../b1.txt; : >"$LOG"
oid=$(git hash-object -w --no-filters --stdin <../b1.txt); echo "predicted=$oid"; hooks
mode=$(st f | cut -d' ' -f1)
git update-index --cacheinfo "$mode,$oid,f"; st f; hooks
echo "-- no compare-and-swap: a concurrent change to the entry is overwritten silently"
echo other >>f; git add f; git update-index --cacheinfo "$mode,$oid,f"; echo "exit=$?"; st f

case_ "A2 line selection inside a hunk: stage only the +L4 of a -l4/+L4 -l5/+L5 block"
newrepo a2; seq10 >f; git add f; git commit -qm i
sed -i 's/^l4$/L4/; s/^l5$/L5/' f
git diff f >full
# unselected '-l5' becomes context ' l5', unselected '+L5' is dropped; counts now wrong
awk '/^-l5$/{print " l5";next} /^\+L5$/{next} {print}' full >sel
echo "-- without --recount:"; git apply --cached --check sel 2>&1; echo "exit=$?"
echo "-- with --recount:"; git apply --cached --recount sel; echo "exit=$?"
git show :f | sed -n 3,6p | tr '\n' ' '; echo " <- l5 before L4: naive in-place context conversion reorders lines"
git reset -q; awk '/^-l5$/{held=" l5";next} /^\+L5$/{next} {print} /^\+L4$/&&held{print held;held=""}' full >sel2
git apply --cached sel2; echo "context moved after the block's + lines: exit=$?"; git show :f | sed -n 3,6p | tr '\n' ' '; echo

case_ "A3 stale selection: index/worktree changed after the diff was computed"
newrepo a3; seq10 >f; git add f; git commit -qm i
sed -i 's/^l10$/L10/' f; git diff f >p
sed -i 's/^l9$/X9/' f; git add f           # another client stages a neighbouring edit
echo "-- default (exact context):"; git apply --cached --check p 2>&1; echo "exit=$?"
echo "-- -C1 (reduced context):";  git apply --cached --check -C1 p 2>&1; echo "exit=$?"
echo "-- --unidiff-zero -U0 patch built earlier would ignore context entirely:"
git reset -q --hard HEAD; sed -i 's/^l10$/L10/' f; git diff -U0 f >p0
git checkout -q f; seq10 | sed '1i NEW' >f; git add f # lines shift by one
git apply --cached --unidiff-zero --check p0 2>&1; echo "exit=$?"
git apply --cached --unidiff-zero p0 2>&1; git show :f | sed -n 10,12p

case_ "A4 -3 on a failed context: falls back to 3-way and can leave a conflicted index"
newrepo a4; seq10 >f; git add f; git commit -qm i
sed -i 's/^l10$/L10/' f; git diff f >p; git checkout -q f
sed -i 's/^l10$/Z10/' f; git add f
git apply --cached -3 p 2>&1; echo "exit=$?"; st f; git reset -q

case_ "A5 CRLF with core.autocrlf=true (worktree CRLF, index LF)"
newrepo a5; git config core.autocrlf true; seq10 | sed 's/$/\r/' >f; git add f; git commit -qm i
st f; git cat-file -p :f | head -1 | od -c | head -2
sed -i 's/^l3\r$/L3\r/; s/^l17\r$/L17\r/' f
git diff f | hunk 1 >p; grep -c $'\r' p || true
git apply --cached p; echo "stage exit=$?"; git show :f | sed -n 3p | od -c | head -1
echo "-- discard hunk 2 via apply -R to worktree:"
git diff f | hunk 1 >../q; git apply -R ../q 2>&1; echo "exit=$?"; sed -n 17p f | od -c | head -1
echo "-- hash-object --path on worktree-space vs index-space bytes:"
printf 'a\r\n' | git hash-object --stdin --path=f; printf 'a\n' | git hash-object --stdin --no-filters

case_ "A6 .gitattributes text eol=crlf and a mixed-EOL file without attributes"
newrepo a6; echo '*.txt text eol=crlf' >.gitattributes; printf 'a\nb\nc\n' >t.txt; git add .; git commit -qm i
rm t.txt; git checkout -q t.txt; od -c t.txt | head -1; sed -i 's/^b\r$/B\r/' t.txt; git diff t.txt | grep -c $'\r' || true
git diff t.txt >p; git apply -R p; echo "discard exit=$?"; git status --short t.txt; od -c t.txt | head -1
printf 'x\r\ny\nz\r\n' >m; git add m; git commit -qm m; sed -i 's/^y$/Y/' m; git diff m | cat -A | tail -3
git diff m >pm; git apply --cached pm; echo "stage exit=$?"; git show :m | od -c | head -1

case_ "A7 clean/smudge filter: which commands invoke it"
newrepo a7; git config filter.up.clean "sh -c 'echo clean >>$W/filterlog; tr a-z A-Z'"
git config filter.up.smudge "sh -c 'echo smudge >>$W/filterlog; tr A-Z a-z'"
echo '*.u filter=up' >.gitattributes; printf 'one\ntwo\nthree\n' >x.u; git add .; git commit -qm i
: >$W/filterlog; git show :x.u | tr '\n' ' '; echo
sed -i 's/two/deux/; s/three/trois/' x.u
: >$W/filterlog; git diff x.u >p; echo "diff: $(tr '\n' , <$W/filterlog)"; cat p | grep '^[-+][A-Za-z]'
git diff x.u | hunk 1 | awk '/^-THREE/{h=" THREE";next} /^\+TROIS/{next}{print} /^\+DEUX/&&h{print h;h=""}' >s; : >$W/filterlog
git apply --cached --recount s; echo "apply --cached exit=$? filter: $(tr '\n' , <$W/filterlog)"; git show :x.u | tr '\n' ' '; echo
: >$W/filterlog; printf 'ONE\nDEUX\nTHREE\n' | git hash-object --stdin --no-filters; echo "hash --no-filters filter: $(tr '\n' , <$W/filterlog)"
: >$W/filterlog; printf 'ONE\nDEUX\nTHREE\n' | git hash-object --stdin --path=x.u; echo "hash --path filter: $(tr '\n' , <$W/filterlog) (re-cleans index-space bytes)"
git reset -q; git diff x.u >r; : >$W/filterlog; git apply -R r 2>&1; echo "discard (apply -R) exit=$? filter: $(tr '\n' , <$W/filterlog)"; cat x.u | tr '\n' ' '; echo
hooks

case_ "A8 LFS-like pointer filter: diff shows pointer text, so partial selection is meaningless"
newrepo a8; git config filter.ptr.clean "sh -c 'echo ptr-oid-\$(sha1sum | cut -c1-8)'"; git config filter.ptr.smudge cat
echo '*.bin filter=ptr' >.gitattributes; seq 1 50 >d.bin; git add .; git commit -qm i; echo 99 >>d.bin; git diff d.bin | tail -3

case_ "A9 no trailing newline"
newrepo a9; printf 'a\nb\nc' >f; git add f; git commit -qm i; printf 'A\nb\nC' >f; git diff f | tail -6
git diff f | awk '/^-c$/{print " c";skip=1;next} /^\+C$/{next} {print}' >p; cat -A p | tail -4
git apply --cached --recount p 2>&1; echo "exit=$?"; git show :f | od -c | head -1

case_ "A10 mode change plus content: header-only mode, hunk-only content"
newrepo a10; seq10 >f; git add f; git commit -qm i; chmod +x f; sed -i 's/l1$/L1/' f; git diff f | head -4
git diff f | grep -v '^old mode\|^new mode' >p; git apply --cached p; echo "content-only exit=$?"; st f

case_ "A11 intent-to-add: git diff shows new-file patch; partial apply --cached"
newrepo a11; echo base >b; git add b; git commit -qm i; printf 'n1\nn2\nn3\n' >n; git add -N n; git diff n | head -6
git diff n | awk '/^\+n2$/{next}{print}' >p; git apply --cached --recount p 2>&1; echo "exit=$?"; st n; git show :n 2>&1

case_ "A12 unmerged path: apply --cached and update-index --cacheinfo"
newrepo a12; seq10 >f; git add f; git commit -qm i; git checkout -qb o; sed -i 's/l5/O5/' f; git commit -qam o
git checkout -q main; sed -i 's/l5/M5/' f; git commit -qam m; git merge -q o >/dev/null 2>&1; st f
git diff f >p 2>&1; head -3 p; git apply --cached p 2>&1; echo "apply exit=$?"
oid=$(seq10 | git hash-object -w --stdin); git update-index --cacheinfo 100644,$oid,f; echo "cacheinfo exit=$?"; st f; echo "(cacheinfo silently resolved the conflict)"

case_ "A13 binary file"
newrepo a13; printf '\0\1\2' >z; git add z; git commit -qm i; printf '\0\1\3' >z; git diff z; git diff --binary z | head -3

case_ "A14 non-UTF-8 (Latin-1) bytes"
newrepo a14; printf 'caf\xe9\nna\xefve\nx\n' >f; git add f; git commit -qm i; printf 'CAF\xe9\nna\xefve\nX\n' >f
git diff -U0 f | hunk 1 >p; git apply --cached --unidiff-zero p; echo "exit=$?"; git show :f | od -c | head -1

case_ "A15 staged rename: unstage one hunk with apply --cached -R"
newrepo a15; seq10 >old; git add old; git commit -qm i; git mv old new; sed -i 's/l2$/L2/; s/l19/L19/' new; git add new
git diff --cached -M | head -8
git diff --cached -M | hunk 2 >../p; git apply --cached -R ../p 2>&1; echo "exit=$?"; git status --short -uno; echo "(rename headers reversed the whole rename)"
git reset -q; git add -A; git diff --cached --no-renames | grep '^diff\|^new\|^deleted'
git diff --cached --no-renames -- new | hunk 2 >../p; git apply --cached -R ../p; echo "no-renames partial unstage exit=$?"; git status --short -uno

echo "-- B: unstage L19 from the renamed entry by writing the computed blob"
o=$(seq10 | sed 's/^l2$/L2/' | git hash-object -w --no-filters --stdin); git update-index --cacheinfo 100644,$o,new; git status --short -uno; git diff --cached -M --stat | tail -1

case_ "B2 intent-to-add entry replaced by --cacheinfo"
newrepo b2; echo base >b; git add b; git commit -qm i; printf 'n1\nn2\nn3\n' >n; git add -N n
o=$(printf 'n1\nn3\n' | git hash-object -w --no-filters --stdin); git update-index --cacheinfo 100644,$o,n; git status --short n; git ls-files --debug n | grep flags

case_ "A16 submodule entry"
newrepo a16s; echo s >s; git add s; git commit -qm s; newrepo a16; git -c protocol.file.allow=always submodule -q add ../a16s sub; git commit -qm i
(cd sub; echo t >>s; git commit -qam t); git diff sub

case_ "A17 adjacent hunks: stage hunk 2 of a -U3 diff whose hunks are merged; split needs -U0/-U1"
newrepo a17; seq10 >f; git add f; git commit -qm i; sed -i 's/^l5$/L5/; s/^l9$/L9/' f
echo "hunks at -U3: $(git diff f | grep -c '^@@')  at -U1: $(git diff -U1 f | grep -c '^@@')"
git diff -U1 f | hunk 2 >p; git apply --cached p; echo "exit=$?"; git show :f | sed -n 5p\;9p

case_ "A18 large diff timing (100k lines, every 10th changed; stage half via apply vs cacheinfo)"
newrepo a18; seq 1 100000 >f; git add f; git commit -qm i; awk 'NR%10==0{$0="X"$0}1' f >g; mv g f
git diff -U0 f >p; echo "patch bytes $(wc -c <p) hunks $(grep -c '^@@' p)"
awk '/^@@/{k++} k==0||k%2==1' p >half
TIMEFORMAT="%Rs"; echo -n "apply --cached: "; time git apply --cached --unidiff-zero half
git show :f >viaapply; git reset -q; awk 'NR%20==10{$0="X"$0}1' <(seq 1 100000) >../tgt
echo -n "hash-object+cacheinfo: "; time { o=$(git hash-object -w --no-filters ../tgt); git update-index --cacheinfo 100644,$o,f; }
echo "same result: $(git show :f | cmp - viaapply && echo yes)"
hooks
echo; echo "probe dir: $W"
