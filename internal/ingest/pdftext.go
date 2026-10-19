package ingest

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/ledongthuc/pdf"
)

// Text in a PDF is a string of font codes, and what a code means is up to the font. The library's
// own extractor reads a font's /ToUnicode map for most fonts but not for Type3 fonts, which name
// their codes with a /Differences list of glyph names such as /g57 (meaningless as text). Those
// PDFs, common from converters, extract as gibberish: "this page" comes out as "WKLV\x03SDJH".
//
// The PDF specification says /ToUnicode is the authority for text extraction, so pageText follows
// it: any font that has one is decoded through it. Fonts without one keep the library's decoding.

// extractVersion is part of an uploaded file's content hash. Re-uploading a file replaces its stored
// text only when the hash changes, so improving extraction has to change the hash too; bump this
// whenever pdfSections or the other extractors would produce different text for the same file.
const extractVersion = "2"

// toUnicode is a font's code-to-text map, read from its /ToUnicode CMap.
type toUnicode struct {
	width int // bytes per code, from the codespace range: 1 for simple and Type3 fonts, 2 for CID fonts
	text  map[uint32]string
}

// Decode turns font codes into text. A code the map does not know is dropped rather than shown
// as junk, and a trailing partial code is ignored.
func (m *toUnicode) Decode(raw string) string {
	var b strings.Builder
	for i := 0; i+m.width <= len(raw); i += m.width {
		var code uint32
		for _, c := range []byte(raw[i : i+m.width]) {
			code = code<<8 | uint32(c)
		}
		b.WriteString(m.text[code])
	}
	return b.String()
}

// parseToUnicode reads the bfchar and bfrange sections of a ToUnicode CMap. It returns nil when the
// CMap is malformed or maps nothing, so the caller falls back to the library's decoding.
func parseToUnicode(data []byte) *toUnicode {
	src := string(data)
	m := &toUnicode{text: map[uint32]string{}}

	for _, sec := range sections(src, "begincodespacerange", "endcodespacerange") {
		toks, ok := tokens(sec)
		if !ok {
			return nil
		}
		if len(toks) > 0 && toks[0].hex != nil && m.width == 0 {
			m.width = len(toks[0].hex)
		}
	}

	for _, sec := range sections(src, "beginbfchar", "endbfchar") {
		toks, ok := tokens(sec)
		if !ok || len(toks)%2 != 0 {
			return nil
		}
		for i := 0; i < len(toks); i += 2 {
			text, ok := utf16Text(toks[i+1].hex)
			if toks[i].hex == nil || !ok {
				return nil
			}
			m.setWidth(len(toks[i].hex))
			m.text[code(toks[i].hex)] = text
		}
	}

	for _, sec := range sections(src, "beginbfrange", "endbfrange") {
		toks, ok := tokens(sec)
		if !ok || !m.addRanges(toks) {
			return nil
		}
	}

	if len(m.text) == 0 {
		return nil
	}
	if m.width == 0 {
		m.width = 1
	}

	return m
}

func (m *toUnicode) setWidth(n int) {
	if m.width == 0 {
		m.width = n
	}
}

// addRanges handles "<lo> <hi> <dst>" (dst counts up from its last code unit) and
// "<lo> <hi> [<d0> <d1> ...]" (one destination per code).
func (m *toUnicode) addRanges(toks []token) bool {
	for i := 0; i < len(toks); {
		if i+2 >= len(toks) || toks[i].hex == nil || toks[i+1].hex == nil {
			return false
		}
		lo, hi := code(toks[i].hex), code(toks[i+1].hex)
		m.setWidth(len(toks[i].hex))
		if hi < lo || hi-lo > 0xFFFF {
			return false
		}

		dst := toks[i+2]
		switch {
		case dst.hex != nil:
			units, ok := utf16Units(dst.hex)
			if !ok {
				return false
			}
			for c := lo; c <= hi; c++ {
				u := append([]uint16(nil), units...)
				u[len(u)-1] += uint16(c - lo)
				m.text[c] = string(utf16.Decode(u))
			}
			i += 3
		case dst.punct == '[':
			j := i + 3
			for c := lo; c <= hi; c++ {
				if j >= len(toks) || toks[j].hex == nil {
					return false
				}
				text, ok := utf16Text(toks[j].hex)
				if !ok {
					return false
				}
				m.text[c] = text
				j++
			}
			if j >= len(toks) || toks[j].punct != ']' {
				return false
			}
			i = j + 1
		default:
			return false
		}
	}

	return true
}

// sections returns the text between each begin...end keyword pair.
func sections(s, begin, end string) []string {
	var out []string
	for {
		i := strings.Index(s, begin)
		if i < 0 {
			return out
		}
		s = s[i+len(begin):]
		j := strings.Index(s, end)
		if j < 0 {
			return out
		}
		out = append(out, s[:j])
		s = s[j+len(end):]
	}
}

// token is a <hex string> or a [ or ] bracket.
type token struct {
	hex   []byte
	punct byte
}

func tokens(s string) ([]token, bool) {
	var out []token
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '<':
			end := strings.IndexByte(s[i:], '>')
			if end < 0 {
				return nil, false
			}
			digits := strings.Join(strings.Fields(s[i+1:i+end]), "")
			if len(digits)%2 != 0 || len(digits) == 0 {
				return nil, false
			}
			b := make([]byte, len(digits)/2)
			for k := range b {
				if _, err := fmt.Sscanf(digits[2*k:2*k+2], "%02x", &b[k]); err != nil {
					return nil, false
				}
			}
			out = append(out, token{hex: b})
			i += end
		case c == '[' || c == ']':
			out = append(out, token{punct: c})
		}
	}
	return out, true
}

func code(b []byte) uint32 {
	var c uint32
	for _, x := range b {
		c = c<<8 | uint32(x)
	}
	return c
}

func utf16Units(b []byte) ([]uint16, bool) {
	if len(b) == 0 || len(b)%2 != 0 {
		return nil, false
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.BigEndian.Uint16(b[2*i:])
	}
	return u, true
}

func utf16Text(b []byte) (string, bool) {
	u, ok := utf16Units(b)
	return string(utf16.Decode(u)), ok
}

// decoderFor picks how a font's codes become text: through its ToUnicode map when it has one and
// the library would not use it, otherwise the library's own decoding.
func decoderFor(f *pdf.Font) pdf.TextEncoding {
	if f.V.Key("Encoding").Kind() == pdf.Dict && f.V.Key("ToUnicode").Kind() == pdf.Stream {
		rd := f.V.Key("ToUnicode").Reader()
		data, err := io.ReadAll(io.LimitReader(rd, 4<<20))
		rd.Close()
		if err == nil {
			if m := parseToUnicode(data); m != nil {
				return m
			}
		}
	}

	return f.Encoder()
}

type rawDecoder struct{}

func (rawDecoder) Decode(raw string) string { return raw }

// pageText returns a page's text in content-stream order, one line per text object, mirroring the
// library's GetPlainText except for how fonts are decoded.
func pageText(p pdf.Page) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			text, err = "", fmt.Errorf("%v", r)
		}
	}()

	if p.V.IsNull() || p.V.Key("Contents").Kind() == pdf.Null {
		return "", nil
	}

	decoders := map[string]pdf.TextEncoding{}
	for _, name := range p.Fonts() {
		f := p.Font(name)
		decoders[name] = decoderFor(&f)
	}

	var (
		current pdf.TextEncoding = rawDecoder{}
		out     strings.Builder
	)
	show := func(v pdf.Value) { out.WriteString(current.Decode(v.RawString())) }

	pdf.Interpret(p.V.Key("Contents"), func(stk *pdf.Stack, op string) {
		args := make([]pdf.Value, stk.Len())
		for i := len(args) - 1; i >= 0; i-- {
			args[i] = stk.Pop()
		}

		switch op {
		case "BT", "T*":
			out.WriteString("\n")
		case "Tf":
			if len(args) != 2 {
				panic("bad Tf")
			}
			if d, ok := decoders[args[0].Name()]; ok {
				current = d
			} else {
				current = rawDecoder{}
			}
		case "Tj", "'", "\"":
			if len(args) == 0 {
				panic("bad text operator")
			}
			show(args[len(args)-1])
		case "TJ":
			if len(args) == 0 {
				panic("bad TJ")
			}
			for i := 0; i < args[0].Len(); i++ {
				if x := args[0].Index(i); x.Kind() == pdf.String {
					show(x)
				}
			}
		}
	})

	return out.String(), nil
}

// ligatures are single glyphs for letter pairs; a search for "justification" must find "justiﬁcation".
var ligatures = strings.NewReplacer("ﬀ", "ff", "ﬁ", "fi", "ﬂ", "fl", "ﬃ", "ffi", "ﬄ", "ffl", "ﬅ", "st", "ﬆ", "st")

// tidy expands ligatures, turns stray control characters (never meaningful in text) into spaces and
// collapses whitespace.
func tidy(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, ligatures.Replace(s))

	return strings.Join(strings.Fields(s), " ")
}
