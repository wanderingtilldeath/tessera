// Package ingest turns uploaded files into searchable chunks: it extracts
// text (PDF, Markdown, HTML, plain text), splits it with the same chunker the
// crawler uses and stores it in a batch.
package ingest

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/ledongthuc/pdf"

	"github.com/wanderingtilldeath/docs-rag-ingest/internal/parse"
)

// Kinds of file Extract understands.
const (
	KindPDF      = "pdf"
	KindMarkdown = "markdown"
	KindHTML     = "html"
	KindText     = "text"
)

// ErrUnsupported is returned for a file type Extract does not handle.
var ErrUnsupported = errors.New("unsupported file type (use .pdf, .md, .html or .txt)")

// KindOf maps a file name to a Kind by extension, or "" if unsupported.
func KindOf(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".pdf":
		return KindPDF
	case ".md", ".markdown":
		return KindMarkdown
	case ".html", ".htm":
		return KindHTML
	case ".txt", ".text":
		return KindText
	}
	return ""
}

// Extract returns the document title and its sections. An empty section list
// means the file holds no extractable text.
func Extract(name string, data []byte) (title string, sections []parse.Section, err error) {
	fallback := strings.TrimSuffix(name, path.Ext(name))

	switch KindOf(name) {
	case KindMarkdown:
		title, sections = markdown(string(data))
	case KindText:
		sections = plainText(string(data))
	case KindHTML:
		page, perr := parse.HTMLParser{}.Parse(bytes.NewReader(data), "upload:///"+name)
		if perr != nil {
			return "", nil, fmt.Errorf("parse html: %w", perr)
		}
		title, sections = page.Title, page.Sections
	case KindPDF:
		sections, err = pdfSections(data)
		if err != nil {
			return "", nil, err
		}
	default:
		return "", nil, ErrUnsupported
	}

	if title == "" {
		title = fallback
	}

	return title, sections, nil
}

// markdown splits at #, ## and ### headings, building the same "A > B" path
// the HTML extractor uses. Headings inside code fences are text, and a YAML
// front matter block is dropped.
func markdown(src string) (title string, sections []Section) {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	if rest, ok := strings.CutPrefix(src, "---\n"); ok {
		if _, after, found := strings.Cut(rest, "\n---\n"); found {
			src = after
		}
	}

	var (
		levels  [3]string
		heading string
		paras   []string
		cur     []string
		fenced  bool
	)
	flushPara := func() {
		if len(cur) > 0 {
			paras = append(paras, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	closeSection := func() {
		flushPara()
		if len(paras) > 0 {
			sections = append(sections, Section{Heading: heading, Text: strings.Join(paras, "\n\n")})
		}
		paras = nil
	}

	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fenced = !fenced
			cur = append(cur, line)
			continue
		}
		if !fenced {
			if level, text := atxHeading(trimmed); level > 0 && level <= 3 {
				closeSection()
				levels[level-1] = text
				for i := level; i < 3; i++ {
					levels[i] = ""
				}
				heading = joinPath(levels)
				if level == 1 && title == "" {
					title = text
				}
				continue
			}
			if trimmed == "" {
				flushPara()
				continue
			}
		}
		cur = append(cur, line)
	}
	closeSection()

	return title, sections
}

// Section aliases parse.Section so callers need only this package.
type Section = parse.Section

// atxHeading parses "## Title" and returns its level (0 if not a heading).
func atxHeading(line string) (int, string) {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || n >= len(line) || line[n] != ' ' {
		return 0, ""
	}

	return n, strings.TrimSpace(strings.TrimRight(strings.TrimSpace(line[n+1:]), "#"))
}

func joinPath(levels [3]string) string {
	var parts []string
	for _, l := range levels {
		if l != "" {
			parts = append(parts, l)
		}
	}

	return strings.Join(parts, " > ")
}

// plainText is one section; blank lines separate paragraphs.
func plainText(src string) []Section {
	src = strings.ReplaceAll(src, "\r\n", "\n")

	var paras []string
	for _, p := range strings.Split(src, "\n\n") {
		if p = strings.TrimSpace(p); p != "" {
			paras = append(paras, p)
		}
	}
	if len(paras) == 0 {
		return nil
	}

	return []Section{{Text: strings.Join(paras, "\n\n")}}
}

// pdfSections returns one section per page that has text, headed "Page N" so
// a hit can point the reader at the page. Line breaks inside a page are
// layout, not meaning, so a page's lines are joined into running text. The
// PDF reader panics on some malformed files; that becomes an error.
func pdfSections(data []byte) (sections []Section, err error) {
	defer func() {
		if r := recover(); r != nil {
			sections, err = nil, fmt.Errorf("read pdf: %v", r)
		}
	}()

	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("read pdf: %w", err)
	}

	for i := 1; i <= r.NumPage(); i++ {
		text, perr := pageText(r.Page(i))
		if perr != nil {
			return nil, fmt.Errorf("read pdf page %d: %w", i, perr)
		}
		if text = tidy(text); text != "" {
			sections = append(sections, Section{Heading: fmt.Sprintf("Page %d", i), Text: text})
		}
	}

	return sections, nil
}
