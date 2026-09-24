#!/usr/bin/env bash
# Query terminal capabilities and write raw replies (escaped) to $1.
# Run inside the terminal under test, e.g. foot -e bash probe-replies.sh out.txt
out=${1:?output file}
old=$(stty -g); stty raw -echo min 0 time 5
q() { # label, query
  printf '%s' "$2" > /dev/tty
  r=$(dd bs=4096 count=1 2>/dev/null </dev/tty)
  printf '%s\t%q\n' "$1" "$r" >> "$out"
}
: > "$out"
q DA1 $'\e[c'
q XTVERSION $'\e[>0q'
q KITTY_GFX_QUERY $'\e_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\e\\\e[c'
q DECRQM_2026 $'\e[?2026$p'
# OSC 66 detection (kitty docs): CR, CPR, width-2 space, CPR.
q OSC66_W2 $'\r\e[6n\e]66;w=2; \a\e[6n'
q OSC66_S2 $'\r\e[6n\e]66;s=2; \a\e[6n'
q DECDWL_CPR $'\r\e#6\e[6nX\e[6n\e#5'
stty "$old"
