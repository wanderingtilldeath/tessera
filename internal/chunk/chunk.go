// Package chunk splits parsed page sections into pieces sized for embedding.
package chunk

import (
	"strings"
	"unicode/utf8"

	"github.com/wanderingtilldeath/tessera/internal/parse"
)

// DefaultMaxChars is the chunk size cap used by the crawler.
const DefaultMaxChars = 1500

// Chunk is one embeddable piece of a page.
type Chunk struct {
	Seq     int
	Heading string
	Text    string
}

// Split turns sections into chunks of at most maxChars runes (a single
// sentence longer than that is cut at the limit). A section that fits becomes
// one chunk; a longer one is packed paragraph by paragraph, then sentence by
// sentence, so a chunk never starts mid-sentence unless it has to.
func Split(sections []parse.Section, maxChars int) []Chunk {
	if maxChars <= 0 {
		maxChars = DefaultMaxChars
	}

	var out []Chunk
	add := func(heading, text string) {
		if text = strings.TrimSpace(text); text != "" {
			out = append(out, Chunk{Seq: len(out), Heading: heading, Text: text})
		}
	}

	for _, s := range sections {
		if utf8.RuneCountInString(s.Text) <= maxChars {
			add(s.Heading, s.Text)
			continue
		}
		var cur strings.Builder
		flush := func() { add(s.Heading, cur.String()); cur.Reset() }
		for _, unit := range units(s.Text, maxChars) {
			if cur.Len() > 0 && utf8.RuneCountInString(cur.String())+2+utf8.RuneCountInString(unit) > maxChars {
				flush()
			}
			if cur.Len() > 0 {
				cur.WriteString("\n\n")
			}
			cur.WriteString(unit)
		}
		flush()
	}

	return out
}

// units breaks text into paragraphs, and paragraphs that are too long into
// sentences, then hard-cuts anything still over the limit.
func units(text string, maxChars int) []string {
	var out []string
	for _, para := range strings.Split(text, "\n\n") {
		if utf8.RuneCountInString(para) <= maxChars {
			out = append(out, para)
			continue
		}
		for _, sent := range sentences(para) {
			for utf8.RuneCountInString(sent) > maxChars {
				r := []rune(sent)
				out = append(out, string(r[:maxChars]))
				sent = string(r[maxChars:])
			}
			out = append(out, sent)
		}
	}
	return out
}

func sentences(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s)-1; i++ {
		if (s[i] == '.' || s[i] == '!' || s[i] == '?') && s[i+1] == ' ' {
			out = append(out, s[start:i+1])
			start = i + 2
		}
	}
	return append(out, s[start:])
}
