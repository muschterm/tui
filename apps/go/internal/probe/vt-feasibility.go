//go:build ignore

// Run explicitly from an isolated module with the version in the research note.
package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/vt"
)

func main() {
	e := vt.NewEmulator(20, 4)
	write := func(s string) {
		for _, b := range []byte(s) { // Deliberately split UTF-8 and escape sequences.
			if _, err := e.Write([]byte{b}); err != nil {
				panic(err)
			}
		}
	}
	check := func(want string) {
		if !strings.Contains(e.String(), want) {
			panic(fmt.Sprintf("wanted %q in %q", want, e.String()))
		}
	}
	write("hello界")
	check("hello界")
	write("\x1b[2J\x1b[Hbase")
	check("base")
	write("\x1b[?1049halt")
	check("alt")
	write("\x1b[?1049l")
	check("base")
	e.Resize(12, 3)
	if e.CellAt(12, 0) != nil || e.CellAt(0, 3) != nil {
		panic("out-of-bounds cells returned")
	}
	fmt.Println("PASS x/vt: split UTF-8/escape parsing, erase/home, alternate-screen restoration, bounded resize")
}
