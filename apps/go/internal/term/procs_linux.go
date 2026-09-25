package term

import (
	"bytes"
	"os"
	"strconv"
)

// sessionMembers lists live processes (pid → process group) in session sid,
// excluding sid itself and zombies, from /proc/<pid>/stat.
func sessionMembers(sid int) (map[int]int, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, false
	}
	out := map[int]int{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == sid {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue // exited meanwhile
		}
		// pid (comm) state ppid pgrp session …; comm may contain spaces and ')'.
		i := bytes.LastIndexByte(b, ')')
		if i < 0 {
			continue
		}
		f := bytes.Fields(b[i+1:])
		if len(f) < 4 || string(f[0]) == "Z" || string(f[0]) == "X" {
			continue
		}
		pgrp, err1 := strconv.Atoi(string(f[2]))
		sess, err2 := strconv.Atoi(string(f[3]))
		if err1 == nil && err2 == nil && sess == sid {
			out[pid] = pgrp
		}
	}
	return out, true
}
