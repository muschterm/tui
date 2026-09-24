#!/usr/bin/env bash
# Visual test screen; stays up for ${1:-6}s for capture.
E=$'\e'
printf '%s[2J%s[H' "$E" "$E"
printf '%s]66;s=2;Wi-Fi%s\\\n\n' "$E" "$E"
printf '%s]66;n=1:d=2;smaller fractional n=1 d=2 line%s\\\n' "$E" "$E"
printf 'OSC66 plain after\n'
printf '%s#3DECDHL Heading\n%s#4DECDHL Heading\n' "$E" "$E"
printf '%s#6DECDWL wide line\n' "$E"
bg=$'\e[48;2;60;90;160m'; fg=$'\e[38;2;60;90;160m'; r=$'\e[0m'
printf ' %s▄▄▄▄▄▄▄▄▄▄▄▄%s\n' "$fg" "$r"
printf ' %s%s  CONNECT   %s\n' "$bg" $'\e[1;38;2;255;255;255m' "$r"
printf ' %s▀▀▀▀▀▀▀▀▀▀▀▀%s\n' "$fg" "$r"
printf ' %s▁▁▁▁▁▁▁▁▁▁%s\n' "$fg" "$r"
printf "%s▕%s%s  Eighths  %s%s▏%s\n" "$fg" "$bg" $'\e[97m' "$r" "$fg" "$r"
printf ' %s▔▔▔▔▔▔▔▔▔▔%s\n' "$fg" "$r"
on=$'\e[48;2;80;180;120m'; off=$'\e[48;2;90;90;90m'; k=$'\e[48;2;240;240;240m'
printf 'toggle on  %s   %s %s   off %s %s%s   %s\n' "$on" "$k" "$r" "$k" "$off" "" "$r"
printf 'sextants: 🬀🬁🬂🬃🬄🬅🬆🬇🬈🬉🬊🬋 🭨🭩🭪🭫 quadrants ▖▗▘▝▞▚\n'
printf 'letter-spaced: %sA V A I L A B L E   N E T W O R K S%s\n' $'\e[2m' "$r"
# tiny sixel: 12x12 red square
printf '%sPq"1;1;24;18#0;2;100;0;0#0!24~-!24~-!24~%s\\\n\nsixel above\n' "$E" "$E"
sleep "${1:-6}"
