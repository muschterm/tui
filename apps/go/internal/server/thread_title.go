package server

import (
	"strings"

	"github.com/rivo/uniseg"
)

// defaultThreadTitle is the placeholder title assigned when a thread is
// created without an explicit title, and used to detect a thread whose
// title has not yet been derived from its first prompt.
const defaultThreadTitle = "New thread"

// titleFromPrompt derives a thread title from the first line of an accepted
// prompt, the way the sibling project's UCF server derives thread row titles
// from a first prompt: take the first non-empty line, strip leading
// markdown list/heading/quote markers, collapse internal whitespace, and cap
// the result at 60 grapheme clusters, preferring a word boundary. Never
// operates on bytes or runes when cutting, so multi-byte, combining and
// joined characters are preserved intact.
func titleFromPrompt(text string) string {
	var line string
	for _, candidate := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(candidate)
		if trimmed != "" {
			line = trimmed
			break
		}
	}
	if line == "" {
		return defaultThreadTitle
	}

	line = strings.TrimLeftFunc(line, func(r rune) bool {
		switch r {
		case '#', '*', '-', '>', '\t', ' ':
			return true
		}
		return false
	})

	fields := strings.Fields(line)
	if len(fields) == 0 {
		return defaultThreadTitle
	}
	line = strings.Join(fields, " ")

	const maxClusters = 60
	const minWordBoundary = 20
	if clusters := graphemeClusters(line); len(clusters) > maxClusters {
		cut := maxClusters
		for i := maxClusters; i >= minWordBoundary; i-- {
			if clusters[i-1] == " " {
				cut = i - 1
				break
			}
		}
		line = strings.TrimSpace(strings.Join(clusters[:cut], ""))
	}

	line = strings.TrimRight(line, ".,;:!?-")
	line = strings.TrimSpace(line)

	if line == "" {
		return defaultThreadTitle
	}
	return line
}

// graphemeClusters splits s into user-perceived characters.
func graphemeClusters(s string) []string {
	var clusters []string
	state := -1
	for len(s) > 0 {
		var cluster string
		cluster, s, _, state = uniseg.FirstGraphemeClusterInString(s, state)
		clusters = append(clusters, cluster)
	}
	return clusters
}
