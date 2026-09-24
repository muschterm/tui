// Throwaway probe: does the Bubble Tea v2 cell renderer carry OSC 66 and
// DECDHL/DECDWL? Writes emitted bytes (quoted) to stdout; with -live it runs
// in the real terminal instead.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	osc66 = "\x1b]66;s=2;Wi-Fi\x07"
	osc66w = "\x1b]66;w=2;AB\x07" // explicit width, foot supports w=
	dhlTop = "\x1b#3Heading"
)

type model struct{ frame int }
type tick struct{}

func (m model) Init() tea.Cmd { return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return tick{} }) }
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(tick); ok {
		m.frame++
		if m.frame > 2 {
			return m, tea.Quit
		}
		return m, m.Init()
	}
	return m, nil
}
func (m model) View() tea.View {
	s := osc66 + " after\n" + osc66w + "|\n" + dhlTop + "\nneighbour frame " + strconv.Itoa(m.frame) + "\n"
	return tea.NewView(s)
}

func main() {
	live := flag.Bool("live", false, "render in the real terminal")
	flag.Parse()
	fmt.Println("lipgloss.Width(osc66+\" after\") =", lipgloss.Width(osc66+" after"))
	fmt.Println("lipgloss.Width(osc66w+\"|\") =", lipgloss.Width(osc66w+"|"))
	fmt.Println("lipgloss.Width(dhlTop) =", lipgloss.Width(dhlTop))
	if *live {
		if _, err := tea.NewProgram(model{}).Run(); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		time.Sleep(3 * time.Second)
		return
	}
	var out bytes.Buffer
	p := tea.NewProgram(model{}, tea.WithOutput(&out), tea.WithInput(nil), tea.WithWindowSize(40, 6))
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("%q\n", out.String())
}
