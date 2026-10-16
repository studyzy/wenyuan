package fileparse

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/studyzy/wenyuan/internal/provider/vlm"
)

// PDF text extraction.
//
// The build is standard-library only, so this is a small extractor rather than
// a full PDF renderer: it locates each content stream, reverses the
// FlateDecode compression the streams almost always carry, and reads the text
// show operators (`Tj`, `TJ`, `'`, `"`) out of them. Text drawn with an
// embedded font whose encoding is described only by a /ToUnicode CMap or a
// difference encoding is decoded as Latin-1, which is correct for the ASCII and
// Latin text the file route is asked to read and degrades to mojibake rather
// than failing otherwise. A scanned image PDF carries no text operators and
// yields no content, which the chain treats as a parser that produced nothing.

// pdfToMarkdown extracts the text of a PDF. It returns an error only when the
// payload is not a readable PDF; a PDF with no extractable text returns "".
func pdfToMarkdown(data []byte, filename string) (string, error) {
	if !bytes.Contains(data[:min(len(data), 1024)], []byte("%PDF")) {
		return "", fmt.Errorf("'%s' is not a PDF", filename)
	}
	var out strings.Builder
	for _, stream := range pdfStreams(data) {
		content, ok := decodePDFStream(data, stream)
		if !ok || !looksLikeContentStream(content) {
			continue
		}
		text := extractPDFText(content)
		if strings.TrimSpace(text) != "" {
			out.WriteString(text)
			out.WriteString("\n")
		}
	}
	return collapseBlankLines(collapseSpaces(out.String())), nil
}

// looksLikeContentStream reports whether a decoded stream is a page content
// stream rather than a font program, image or embed. A content stream is
// printable ASCII with text operators; a font program is binary and a name
// table carries NUL-interleaved UTF-16, so requiring a mostly-printable body
// keeps their noise out of the extracted text.
func looksLikeContentStream(content []byte) bool {
	if !bytes.Contains(content, []byte("BT")) {
		return false
	}
	if !bytes.Contains(content, []byte("Tj")) && !bytes.Contains(content, []byte("TJ")) {
		return false
	}
	if len(content) == 0 {
		return false
	}
	printable := 0
	for _, b := range content {
		if (b >= 0x20 && b <= 0x7e) || b == '\n' || b == '\r' || b == '\t' {
			printable++
		}
	}
	return printable*10 >= len(content)*9
}

// collapseSpaces folds runs of spaces introduced between text runs into one.
func collapseSpaces(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	space := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if space {
				continue
			}
			space = true
			out.WriteByte(' ')
			continue
		}
		space = false
		out.WriteRune(r)
	}
	return out.String()
}

// pdfStream is the byte range of one stream's payload within the file.
type pdfStream struct {
	dataStart int
	dataEnd   int
}

// pdfStreams locates every `stream ... endstream` payload, skipping the EOL that
// follows the `stream` keyword.
func pdfStreams(data []byte) []pdfStream {
	var streams []pdfStream
	pos := 0
	for {
		idx := bytes.Index(data[pos:], []byte("stream"))
		if idx < 0 {
			break
		}
		start := pos + idx
		// The keyword must stand alone: `endstream` must not match, and the
		// preceding byte must not be part of a name.
		if start > 0 {
			if prev := data[start-1]; isPDFNameByte(prev) {
				pos = start + len("stream")
				continue
			}
		}
		after := start + len("stream")
		if bytes.HasPrefix(data[after:], []byte("\r\n")) {
			after += 2
		} else if bytes.HasPrefix(data[after:], []byte("\n")) {
			after++
		} else {
			pos = after
			continue
		}
		end := bytes.Index(data[after:], []byte("endstream"))
		if end < 0 {
			break
		}
		end += after
		// Trim the EOL that precedes `endstream`.
		trimmed := end
		if trimmed > after && data[trimmed-1] == '\n' {
			trimmed--
		}
		if trimmed > after && data[trimmed-1] == '\r' {
			trimmed--
		}
		streams = append(streams, pdfStream{dataStart: after, dataEnd: trimmed})
		pos = end + len("endstream")
	}
	return streams
}

// isPDFNameByte reports whether b can appear inside a PDF name/token, used to
// tell `stream` from `endstream`.
func isPDFNameByte(b byte) bool {
	return b == 'e' || b == 'd' || b == 'n' || b == 's' || b == 't' || b == 'r' || b == 'a' || b == 'm'
}

// decodePDFStream returns the stream payload, decompressing it when the
// surrounding dictionary names the FlateDecode filter.
func decodePDFStream(file []byte, stream pdfStream) ([]byte, bool) {
	raw := file[stream.dataStart:stream.dataEnd]
	// The filter sits in the dictionary just before the `stream` keyword.
	dictStart := stream.dataStart - 512
	if dictStart < 0 {
		dictStart = 0
	}
	if !bytes.Contains(file[dictStart:stream.dataStart], []byte("FlateDecode")) {
		return raw, true
	}
	if content, ok := inflatePDF(raw); ok {
		return content, true
	}
	return nil, false
}

// inflatePDF reverses a zlib-wrapped or raw DEFLATE stream.
func inflatePDF(raw []byte) ([]byte, bool) {
	if reader, err := zlib.NewReader(bytes.NewReader(raw)); err == nil {
		if content, err := io.ReadAll(reader); err == nil {
			_ = reader.Close()
			return content, true
		}
		_ = reader.Close()
	}
	if reader := flate.NewReader(bytes.NewReader(raw)); reader != nil {
		if content, err := io.ReadAll(reader); err == nil {
			_ = reader.Close()
			return content, true
		}
		_ = reader.Close()
	}
	return nil, false
}

// extractPDFText reads the text show operators out of one content stream.
func extractPDFText(content []byte) string {
	var out strings.Builder
	i := 0
	for i < len(content) {
		switch c := content[i]; {
		case c == '(':
			text, next := readPDFLiteralString(content, i)
			out.WriteString(pdfBytesToText(text))
			i = next
		case c == '<' && i+1 < len(content) && content[i+1] == '<':
			// A nested dictionary is not text.
			i = skipTo(content, i+2, ">>")
		case c == '<':
			text, next := readPDFHexString(content, i)
			out.WriteString(pdfBytesToText(text))
			i = next
		case isPDFNameStart(c):
			word, next := readPDFWord(content, i)
			switch {
			case isPDFLineBreakOperator(word):
				out.WriteString("\n")
			case isPDFSpaceOperator(word):
				out.WriteString(" ")
			}
			i = next
		default:
			i++
		}
	}
	return out.String()
}

// pdfBytesToText renders the bytes of a PDF string as text. A string whose even
// bytes are mostly NUL is a UTF-16BE encoding of a text run — which is what a
// subset font's identity encoding produces — so it is decoded as such; anything
// else is read as single-byte text.
func pdfBytesToText(raw string) string {
	b := []byte(raw)
	if len(b) >= 2 && len(b)%2 == 0 {
		zeros := 0
		for i := 0; i < len(b); i += 2 {
			if b[i] == 0 {
				zeros++
			}
		}
		if zeros*2 >= len(b)/2 {
			units := make([]uint16, 0, len(b)/2)
			for i := 0; i+1 < len(b); i += 2 {
				units = append(units, uint16(b[i])<<8|uint16(b[i+1]))
			}
			return string(utf16.Decode(units))
		}
	}
	return raw
}

// isPDFLineBreakOperator reports whether an operator begins a new text line.
func isPDFLineBreakOperator(op string) bool {
	switch op {
	case "ET", "BT", "T*":
		return true
	}
	return false
}

// isPDFSpaceOperator reports whether an operator separates words on the same
// line. A text-position move usually steps horizontally between words, so it
// reads as a space rather than a line break; line breaks come from the
// text-object and text-line operators above.
func isPDFSpaceOperator(op string) bool {
	switch op {
	case "Td", "TD", "Tm", "TL", "Tc", "Tw", "Tf", "Tz":
		return true
	}
	return false
}

// readPDFLiteralString reads a `( ... )` string starting at the opening paren,
// resolving the escapes the format defines.
func readPDFLiteralString(content []byte, start int) (string, int) {
	var out strings.Builder
	depth := 0
	i := start
	for i < len(content) {
		c := content[i]
		switch c {
		case '\\':
			if i+1 >= len(content) {
				i++
				continue
			}
			esc := content[i+1]
			switch esc {
			case 'n':
				out.WriteByte('\n')
			case 'r':
				out.WriteByte('\r')
			case 't':
				out.WriteByte('\t')
			case 'b':
				out.WriteByte('\b')
			case 'f':
				out.WriteByte('\f')
			case '(', ')', '\\':
				out.WriteByte(esc)
			case '\n':
				// A backslash before an EOL continues the line.
			case '\r':
				if i+2 < len(content) && content[i+2] == '\n' {
					i++
				}
			default:
				if esc >= '0' && esc <= '7' {
					j := i + 1
					octal := 0
					for k := 0; k < 3 && j < len(content) && content[j] >= '0' && content[j] <= '7'; k++ {
						octal = octal*8 + int(content[j]-'0')
						j++
					}
					out.WriteByte(byte(octal))
					i = j - 1
				} else {
					out.WriteByte(esc)
				}
			}
			i += 2
		case '(':
			depth++
			if depth > 1 {
				out.WriteByte(c)
			}
			i++
		case ')':
			depth--
			if depth == 0 {
				return out.String(), i + 1
			}
			out.WriteByte(c)
			i++
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String(), i
}

// readPDFHexString reads a `< ... >` hex string, decoding its pairs of hex
// digits. A string with an odd digit count is padded with a trailing zero, per
// the format.
func readPDFHexString(content []byte, start int) (string, int) {
	end := bytes.IndexByte(content[start+1:], '>')
	if end < 0 {
		return "", len(content)
	}
	end += start + 1
	hexDigits := make([]byte, 0, end-start)
	for _, b := range content[start+1 : end] {
		if isHexDigit(b) {
			hexDigits = append(hexDigits, b)
		}
	}
	if len(hexDigits)%2 == 1 {
		hexDigits = append(hexDigits, '0')
	}
	out := make([]byte, 0, len(hexDigits)/2)
	for i := 0; i+1 < len(hexDigits); i += 2 {
		value, err := strconv.ParseUint(string(hexDigits[i:i+2]), 16, 8)
		if err != nil {
			continue
		}
		out = append(out, byte(value))
	}
	return string(out), end + 1
}

func isHexDigit(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

// isPDFNameStart reports whether b can begin a PDF operator token.
func isPDFNameStart(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || b == '\''
}

// readPDFWord reads a run of operator characters.
func readPDFWord(content []byte, start int) (string, int) {
	i := start
	for i < len(content) {
		b := content[i]
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || b == '\'' || b == '"' || b == '*' {
			i++
			continue
		}
		break
	}
	return string(content[start:i]), i
}

// skipTo advances past the first occurrence of marker from start.
func skipTo(content []byte, start int, marker string) int {
	idx := bytes.Index(content[start:], []byte(marker))
	if idx < 0 {
		return len(content)
	}
	return start + idx + len(marker)
}

// pdfImages returns the images a PDF embeds that this build can hand to a
// vision model. Only DCTDecode (JPEG) image XObjects are returned: their stream
// payload is a self-contained JPEG file. An image stored as raw samples
// (FlateDecode) or in a codec this build has no decoder for (JPXDecode, JBIG2)
// is skipped rather than sent to the model as a mislabelled blob.
func pdfImages(data []byte) []vlm.Image {
	var out []vlm.Image
	for _, stream := range pdfStreams(data) {
		if !isJPEGImageXObject(data, stream) {
			continue
		}
		raw := data[stream.dataStart:stream.dataEnd]
		if len(raw) < 2 || raw[0] != 0xFF || raw[1] != 0xD8 {
			continue
		}
		out = append(out, vlm.Image{Data: raw, MediaType: "image/jpeg"})
	}
	return out
}

// isJPEGImageXObject reports whether a stream's dictionary describes a JPEG
// image XObject. The dictionary sits immediately before the `stream` keyword,
// so the last `<<` in the bytes preceding it opens it; requiring the /Subtype,
// /Image and /DCTDecode names together keeps a preceding object's dictionary
// from being mistaken for this stream's.
func isJPEGImageXObject(file []byte, stream pdfStream) bool {
	start := stream.dataStart - 2048
	if start < 0 {
		start = 0
	}
	before := file[start:stream.dataStart]
	open := bytes.LastIndex(before, []byte("<<"))
	if open < 0 {
		return false
	}
	dict := before[open:]
	return bytes.Contains(dict, []byte("/Subtype")) &&
		bytes.Contains(dict, []byte("/Image")) &&
		bytes.Contains(dict, []byte("/DCTDecode"))
}
