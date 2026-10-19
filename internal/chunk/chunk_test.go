package chunk

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/wanderingtilldeath/docs-rag-ingest/internal/parse"
)

func TestSplit(t *testing.T) {
	long := strings.Repeat("Sentence number one. ", 30) // ~630 chars, one paragraph

	tests := []struct {
		name     string
		sections []parse.Section
		max      int
		want     int // chunk count
	}{
		{"empty", nil, 100, 0},
		{"blank text dropped", []parse.Section{{Heading: "H", Text: "  "}}, 100, 0},
		{"fits in one", []parse.Section{{Heading: "H", Text: "short"}}, 100, 1},
		{"two sections", []parse.Section{{Text: "a"}, {Text: "b"}}, 100, 2},
		{"long paragraph split on sentences", []parse.Section{{Heading: "H", Text: long}}, 200, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Split(tt.sections, tt.max)
			if len(got) != tt.want {
				t.Fatalf("got %d chunks, want %d: %#v", len(got), tt.want, got)
			}
			for i, c := range got {
				if c.Seq != i {
					t.Errorf("chunk %d has Seq %d", i, c.Seq)
				}
				if utf8.RuneCountInString(c.Text) > tt.max {
					t.Errorf("chunk %d is %d runes, over %d", i, utf8.RuneCountInString(c.Text), tt.max)
				}
				if len(tt.sections) > 0 && c.Heading != tt.sections[0].Heading && len(tt.sections) == 1 {
					t.Errorf("chunk %d lost its heading: %q", i, c.Heading)
				}
			}
		})
	}
}

func TestSplit_HardCutsUnbrokenText(t *testing.T) {
	got := Split([]parse.Section{{Text: strings.Repeat("x", 250)}}, 100)
	if len(got) != 3 {
		t.Fatalf("got %d chunks, want 3", len(got))
	}
}
