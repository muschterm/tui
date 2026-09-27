#!/bin/sh
# git fetch --porcelain output across all remotes with prune; changes pushed from another clone.
. "$(dirname "$0")/lib.sh"; repo a; git init -q --bare "$T/o.git"; git init -q --bare "$T/u.git"
git remote add origin "$T/o.git"; git remote add up "$T/u.git"
git push -q origin main main:gone main:forced; git push -q up main; git fetch -q --all
git clone -q -b main "$T/o.git" "$T/other"; (cd "$T/other"; git commit -q --allow-empty -m b; git push -q origin HEAD:main HEAD:newbr :gone; git reset -q --hard HEAD~1; git commit -q --allow-empty -m x; git push -qf origin HEAD:forced)
echo "== dry-run porcelain:"; git fetch --all --prune --porcelain --dry-run; echo "exit=$?"
echo "== fetch --all --prune --porcelain (stdout; stderr to file):"; git fetch --all --prune --porcelain 2>"$T/err"; echo "exit=$? stderr bytes=$(wc -c <"$T/err")"
echo "== second run (nothing to do):"; git fetch --all --prune --porcelain; echo "exit=$?"
