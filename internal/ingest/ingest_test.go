package ingest

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/wanderingtilldeath/tessera/internal/batch"
	"github.com/wanderingtilldeath/tessera/internal/store"
)

func TestKindOf(t *testing.T) {
	for name, want := range map[string]string{
		"a.pdf": KindPDF, "A.PDF": KindPDF, "notes.md": KindMarkdown, "x.markdown": KindMarkdown,
		"p.html": KindHTML, "p.htm": KindHTML, "r.txt": KindText, "r.exe": "", "noext": "",
	} {
		if got := KindOf(name); got != want {
			t.Errorf("KindOf(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestMarkdownSections(t *testing.T) {
	src := "---\nowner: hr\n---\n# Handbook\n\nIntro line.\n\n## Time off\n\nTwenty five days.\nCarry over to March.\n\n```sh\n# not a heading\necho hi\n```\n\n### Sick leave\n\nSee a doctor.\n\n## Expenses\n\nKeep receipts.\n\n#hashtag is not a heading\n"

	title, sections := markdown(src)

	if title != "Handbook" {
		t.Errorf("title = %q", title)
	}
	want := []Section{
		{Heading: "Handbook", Text: "Intro line."},
		{Heading: "Handbook > Time off", Text: "Twenty five days.\nCarry over to March.\n\n```sh\n# not a heading\necho hi\n```"},
		{Heading: "Handbook > Time off > Sick leave", Text: "See a doctor."},
		{Heading: "Handbook > Expenses", Text: "Keep receipts.\n\n#hashtag is not a heading"},
	}
	if len(sections) != len(want) {
		t.Fatalf("got %d sections, want %d: %+v", len(sections), len(want), sections)
	}
	for i := range want {
		if sections[i] != want[i] {
			t.Errorf("section %d = %+v, want %+v", i, sections[i], want[i])
		}
	}
}

func TestExtractTextAndHTML(t *testing.T) {
	title, sections, err := Extract("notes.txt", []byte("first para\r\n\r\nsecond para\n\n\n"))
	if err != nil || title != "notes" || len(sections) != 1 || sections[0].Text != "first para\n\nsecond para" {
		t.Fatalf("text: %q %+v %v", title, sections, err)
	}

	page := `<html><head><title>Guide</title></head><body><nav>menu</nav><main><h1>Setup</h1><p>Install it.</p></main><footer>bye</footer></body></html>`
	title, sections, err = Extract("guide.html", []byte(page))
	if err != nil || title != "Guide" || len(sections) != 1 || sections[0].Heading != "Setup" || strings.Contains(sections[0].Text, "menu") {
		t.Fatalf("html: %q %+v %v", title, sections, err)
	}

	if _, _, err := Extract("a.exe", []byte("x")); err != ErrUnsupported {
		t.Fatalf("unsupported: %v", err)
	}
}

// makePDF builds a minimal valid PDF with one line of text per page.
func makePDF(pages ...string) []byte {
	var buf bytes.Buffer
	offsets := map[int]int{}
	obj := func(n int, body string) {
		offsets[n] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", n, body)
	}

	buf.WriteString("%PDF-1.4\n")
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")

	kids := make([]string, len(pages))
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 4+2*i)
	}
	obj(2, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages)))
	obj(3, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")

	for i, text := range pages {
		page, content := 4+2*i, 5+2*i
		obj(page, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R /Resources << /Font << /F1 3 0 R >> >> >>", content))
		stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", strings.NewReplacer(`\`, `\\`, "(", `\(`, ")", `\)`).Replace(text))
		obj(content, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}

	total := 3 + 2*len(pages) + 1
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", total)
	for n := 1; n < total; n++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[n])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", total, xref)

	return buf.Bytes()
}

func TestExtractPDF(t *testing.T) {
	_, sections, err := Extract("report.pdf", makePDF("Quarterly revenue grew by ten percent.", "Headcount stayed flat."))
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 2 || sections[0].Heading != "Page 1" || !strings.Contains(sections[0].Text, "Quarterly revenue") || sections[1].Heading != "Page 2" {
		t.Fatalf("sections = %+v", sections)
	}

	if _, _, err := Extract("broken.pdf", []byte("%PDF-1.4 definitely not a pdf")); err == nil {
		t.Fatal("a corrupt PDF should be an error, not a panic or empty success")
	}
}

func TestProcess(t *testing.T) {
	ctx := context.Background()
	st, reg := store.NewMemory(), batch.NewMemory()
	b, _ := reg.CreateBatch(ctx, "docs")

	res, err := Process(ctx, st, reg, b.ID, `C:\fakepath\..\policy.md`, []byte("# Policy\n\nBe kind.\n"))
	if err != nil || res.Status != batch.Done || res.Chunks != 1 || res.Name != "policy.md" {
		t.Fatalf("Process = %+v, %v", res, err)
	}
	stats, _ := st.EmbeddingStats(ctx, b.ID)
	if stats.Total != 1 {
		t.Fatalf("chunks in batch = %+v", stats)
	}

	// Same file again: no duplicate chunks, but a second document row (an upload event).
	if _, err := Process(ctx, st, reg, b.ID, "policy.md", []byte("# Policy\n\nBe kind.\n")); err != nil {
		t.Fatal(err)
	}
	if stats, _ := st.EmbeddingStats(ctx, b.ID); stats.Total != 1 {
		t.Fatalf("re-upload duplicated chunks: %+v", stats)
	}

	// Failures are reported in the result and recorded, not returned as errors.
	for name, data := range map[string]string{"empty.txt": "  \n ", "tool.exe": "MZ", "scan.pdf": "not a pdf"} {
		res, err := Process(ctx, st, reg, b.ID, name, []byte(data))
		if err != nil || res.Status != batch.Failed || res.Error == "" || res.Chunks != 0 {
			t.Errorf("%s: %+v, %v", name, res, err)
		}
	}

	docs, _ := reg.Documents(ctx, b.ID)
	failed := 0
	for _, d := range docs {
		if d.Status == batch.Failed {
			failed++
			if d.Error == "" {
				t.Errorf("failed document %q has no reason", d.Name)
			}
		}
		if d.Status == batch.Processing {
			t.Errorf("document %q left in processing", d.Name)
		}
	}
	if len(docs) != 5 || failed != 3 {
		t.Errorf("documents = %d (failed %d), want 5 (3)", len(docs), failed)
	}
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"../../etc/passwd": "passwd", `a\b\c.md`: "c.md", "ok.md": "ok.md", "": "untitled", "..": "untitled", "x\x00y.txt": "xy.txt",
	} {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
}
