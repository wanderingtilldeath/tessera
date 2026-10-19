package ingest

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"unicode"

	"github.com/wanderingtilldeath/docs-rag-ingest/internal/batch"
	"github.com/wanderingtilldeath/docs-rag-ingest/internal/store"
)

// pdfFrom assembles a PDF from numbered object bodies (object 1 must be the catalog).
func pdfFrom(objects map[int]string) []byte {
	var out strings.Builder
	out.WriteString("%PDF-1.4\n")

	nums := make([]int, 0, len(objects))
	for n := range objects {
		nums = append(nums, n)
	}
	sort.Ints(nums)

	offsets := map[int]int{}
	for _, n := range nums {
		offsets[n] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", n, objects[n])
	}

	last := nums[len(nums)-1]
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", last+1)
	for n := 1; n <= last; n++ {
		fmt.Fprintf(&out, "%010d 00000 n \n", offsets[n])
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", last+1, xref)

	return []byte(out.String())
}

func stream(body string) string {
	return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(body), body)
}

// shifted encodes text the way the PDF in the bug report stores it: every character is a font
// code 29 below its real value, so "this page" is "WKLV\x03SDJH". Only a font's /ToUnicode map says
// what such codes mean.
func shifted(text string) string {
	b := []byte(text)
	for i := range b {
		b[i] -= 29
	}
	return string(b)
}

func pdfString(s string) string {
	return "(" + strings.NewReplacer(`\`, `\\`, "(", `\(`, ")", `\)`).Replace(s) + ")"
}

// A Type3 font like the one in the report: codes are named by a /Differences list of glyph names
// (/g57, ...), which means nothing as text, plus a /ToUnicode map that does.
const type3Font = `<< /Type /Font /Subtype /Type3 /FontBBox [0 0 1 1] /FontMatrix [1 0 0 1 0 0] /FirstChar 0 /LastChar 255
/Encoding << /Type /Encoding /Differences [3 /g3 /g0 /g0 /g0 /g0 /g0 /g0 /g0 /g0 /g0 /g0 /gF /g10 /g11] >> /ToUnicode 5 0 R >>`

func toUnicodeCMap(body string) string {
	return stream("/CIDInit /ProcSet findresource begin\n12 dict begin\nbegincmap\n1 begincodespacerange\n<00> <FF>\nendcodespacerange\n" + body + "\nendcmap\nend\nend")
}

// shiftedCMap maps codes (real character - 29) back to real characters.
const shiftedCMap = "1 beginbfchar\n<03> <0020>\nendbfchar\n3 beginbfrange\n<0F> <11> <002C>\n<24> <3D> <0041>\n<44> <5D> <0061>\nendbfrange"

func onePagePDF(fonts string, content string, extra map[int]string) []byte {
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << " + fonts + " >> >> >>",
		4: stream(content),
	}
	for n, body := range extra {
		objs[n] = body
	}
	return pdfFrom(objs)
}

func extractText(t *testing.T, pdf []byte) string {
	t.Helper()
	_, sections, err := Extract("book.pdf", pdf)
	if err != nil {
		t.Fatal(err)
	}
	var parts []string
	for _, s := range sections {
		parts = append(parts, s.Text)
	}
	return strings.Join(parts, "\n")
}

func TestPDFFontWithToUnicodeIsDecodedThroughIt(t *testing.T) {
	pdf := onePagePDF("/F1 6 0 R",
		"BT /F1 12 Tf 72 720 Td "+pdfString(shifted("Effort Justification, this page."))+" Tj ET",
		map[int]string{5: toUnicodeCMap(shiftedCMap), 6: type3Font})

	got := extractText(t, pdf)
	if got != "Effort Justification, this page." {
		t.Fatalf("text = %q", got)
	}
}

func TestPDFTextHasNoControlCharacters(t *testing.T) {
	pdf := onePagePDF("/F1 6 0 R",
		"BT /F1 12 Tf [ "+pdfString(shifted("Stories, "))+" -20 "+pdfString(shifted("this page."))+" ] TJ ET",
		map[int]string{5: toUnicodeCMap(shiftedCMap), 6: type3Font})

	for _, r := range extractText(t, pdf) {
		if unicode.IsControl(r) && r != '\n' {
			t.Fatalf("control character %U in extracted text", r)
		}
	}
}

func TestPDFMixedFontsOnOnePage(t *testing.T) {
	// A page that mixes a plain font and a ToUnicode-only one must read correctly in both.
	pdf := onePagePDF("/F1 6 0 R /F2 7 0 R",
		"BT /F2 12 Tf 72 740 Td (Plain Helvetica text.) Tj /F1 12 Tf 0 -20 Td "+pdfString(shifted("Coded text."))+" Tj ET",
		map[int]string{5: toUnicodeCMap(shiftedCMap), 6: type3Font, 7: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"})

	got := extractText(t, pdf)
	if !strings.Contains(got, "Plain Helvetica text.") || !strings.Contains(got, "Coded text.") {
		t.Fatalf("text = %q", got)
	}
}

func TestPDFLigaturesBecomeLetters(t *testing.T) {
	// "justiﬁcation" with the single ﬁ glyph must be searchable as "justification".
	cmap := "2 beginbfchar\n<01> <FB01>\n<02> <0020>\nendbfchar\n1 beginbfrange\n<44> <5D> <0061>\nendbfrange"
	font := strings.Replace(type3Font, "[3 /g3", "[1 /g1 /g2 /g0", 1)
	pdf := onePagePDF("/F1 6 0 R",
		"BT /F1 12 Tf 72 720 Td "+pdfString(shifted("justi")+"\x01"+shifted("cation"))+" Tj ET",
		map[int]string{5: toUnicodeCMap(cmap), 6: font})

	if got := extractText(t, pdf); !strings.HasPrefix(got, "justification") {
		t.Fatalf("text = %q, want the fi ligature expanded", got)
	}
}

func TestParseToUnicode(t *testing.T) {
	m := parseToUnicode([]byte(`
1 begincodespacerange <0000> <FFFF> endcodespacerange
2 beginbfchar
<0003> <0020>
<0024> <D83DDE00>
endbfchar
3 beginbfrange
<0010> <0012> <0041>
<0020> <0021> [<0061> <00620063>]
<0030> <0031> <0058>
endbfrange`))
	if m == nil {
		t.Fatal("nil map")
	}

	for _, c := range []struct{ raw, want string }{
		{"\x00\x03", " "},
		{"\x00\x24", "😀"},                   // a UTF-16 surrogate pair
		{"\x00\x10\x00\x11\x00\x12", "ABC"}, // a range increments the last code unit
		{"\x00\x20\x00\x21", "abc"},         // a range may list each destination
		{"\x00\x30\x00\x31", "XY"},
		{"\x00\x99", ""}, // unmapped codes are dropped, not shown as junk
		{"\x00", ""},     // a truncated code is ignored
	} {
		if got := m.Decode(c.raw); got != c.want {
			t.Errorf("Decode(%q) = %q, want %q", c.raw, got, c.want)
		}
	}

	if parseToUnicode([]byte("garbage with no cmap sections")) != nil {
		t.Error("a CMap that maps nothing must be reported as unusable, so the font falls back")
	}
	if parseToUnicode([]byte("1 beginbfchar <00 <01 endbfchar 1 beginbfrange <0 endbfrange")) != nil {
		t.Error("a malformed CMap must not panic or half-parse")
	}
}

// Re-uploading the same file must pick up an improved extractor. The chunks were stored under the
// file's hash, so unless the extractor's version is part of that hash they would be kept as "unchanged".
func TestReuploadAfterExtractorChangeReplacesOldText(t *testing.T) {
	ctx := context.Background()
	st, reg := store.NewMemory(), batch.NewMemory()
	b, _ := reg.CreateBatch(ctx, "docs")
	data := []byte("# Title\n\nThe real text.\n")

	// What an earlier version stored for the identical file: hashed on the bytes alone, garbled text.
	old, err := build(b.ID, "notes.md", data)
	if err != nil {
		t.Fatal(err)
	}
	old.ContentHash = store.ContentHash(data)
	old.Chunks[0].Text = "7KH\x03UHDO\x03WH[W"
	if err := st.SaveChunks(ctx, old); err != nil {
		t.Fatal(err)
	}

	if _, err := Process(ctx, st, reg, b.ID, "notes.md", data); err != nil {
		t.Fatal(err)
	}

	var embedded []string
	if _, err := st.EmbedPending(ctx, 10, func(_ context.Context, texts []string) ([][]float32, error) {
		embedded = texts
		return make([][]float32, len(texts)), nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(embedded) != 1 || !strings.Contains(embedded[0], "The real text.") || strings.Contains(embedded[0], "\x03") {
		t.Fatalf("stored text after re-upload = %q; the old garbled chunk was kept", embedded)
	}
}
