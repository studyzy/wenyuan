package fileparse

import (
	"path"
	"strings"

	"github.com/studyzy/wenyuan/internal/provider/vlm"
)

// extOf is the lowercased extension of a filename including its leading dot, or
// "" when it carries none.
func extOf(filename string) string {
	return strings.ToLower(path.Ext(filename))
}

// formatNames maps a file extension onto the human-readable format the refusal
// for an unreadable document names. It covers the document formats the file
// route advertises as accepted (PDF, DOCX, PPTX, XLSX) plus the legacy binary
// Office formats this build cannot read.
var formatNames = map[string]string{
	".pdf": "PDF", ".docx": "Word", ".doc": "Word",
	".pptx": "PowerPoint", ".ppt": "PowerPoint",
	".xlsx": "Excel", ".xls": "Excel",
}

// formatNameOf names the format a filename names, falling back to the
// uppercased extension so a refusal still says what it could not read.
func formatNameOf(filename string) string {
	ext := extOf(filename)
	if name, ok := formatNames[ext]; ok {
		return name
	}
	if ext == "" {
		return "document"
	}
	return strings.ToUpper(strings.TrimPrefix(ext, "."))
}

// FormatName is the human-readable format a filename names (for example "PDF"
// or "Word"), used to name the format in a refusal.
func FormatName(filename string) string { return formatNameOf(filename) }

// markitdownLocalExtensions are the formats the built-in markitdown parser
// reads. A name here is one the local converters below can turn into markdown;
// a format copied from the reference but not readable here (the legacy binary
// Office formats, images without OCR, audio) is deliberately absent so the
// chain treats it as unsupported rather than storing binary noise as text.
var markitdownLocalExtensions = map[string]struct{}{
	".pdf": {}, ".docx": {}, ".pptx": {}, ".xlsx": {},
	".html": {}, ".htm": {},
	".txt": {}, ".text": {}, ".md": {}, ".markdown": {},
	".csv": {}, ".tsv": {},
	".json": {}, ".jsonl": {}, ".ndjson": {}, ".ipynb": {},
}

// markitdownParser is the built-in local parser. It carries the reference's
// "markitdown" name and role — the default member of the parser chain — but
// reads the supported formats with standard-library code instead of the
// markitdown Python package, which the Go build does not depend on. When OCR is
// configured it also transcribes the images a document embeds.
type markitdownParser struct {
	// ocr transcribes a document's images; nil when OCR is disabled.
	ocr imageTranscriber
}

// NewMarkitdownParser returns the built-in local parser with OCR disabled. It
// always succeeds: the formats it cannot read are reported per file at convert
// time.
func NewMarkitdownParser() Parser { return markitdownParser{} }

// newMarkitdownParser builds the parser, attaching the vision endpoint when OCR
// is enabled. A configuration that cannot work fails here, at construction, so
// a misconfigured OCR deployment refuses to start rather than parsing without
// OCR.
func newMarkitdownParser(cfg vlm.Config, enabled bool) (Parser, error) {
	if !enabled {
		return markitdownParser{}, nil
	}
	transcriber, err := vlm.New(cfg)
	if err != nil {
		return nil, err
	}
	return markitdownParser{ocr: transcriber}, nil
}

func (markitdownParser) Name() string { return "markitdown" }

func (p markitdownParser) Supports(filename, _ string) bool {
	ext := extOf(filename)
	if _, ok := markitdownLocalExtensions[ext]; ok {
		return true
	}
	if p.ocr != nil {
		_, ok := ocrImageMediaTypes[ext]
		return ok
	}
	return false
}

// Convert dispatches on the file extension to the standard-library converter
// for that format. With OCR configured it also transcribes the document's
// embedded images — or, for an image file, the image itself — and appends the
// transcription to the parsed markdown.
func (p markitdownParser) Convert(data []byte, filename string) (string, error) {
	ext := extOf(filename)
	if mediaType, ok := ocrImageMediaTypes[ext]; ok {
		if p.ocr == nil {
			return "", unsupported(p.Name(), filename,
				"image OCR is disabled; enable "+vlm.EnvOCREnabled+" and configure an OCR/vision endpoint, or choose an OCR-capable parser")
		}
		return p.transcribeImages([]vlm.Image{{Data: data, MediaType: mediaType}})
	}
	markdown, err := convertLocal(data, filename)
	if err != nil {
		return "", err
	}
	if p.ocr != nil {
		if images := embeddedImages(ext, data); len(images) > 0 {
			text, err := p.transcribeImages(images)
			if err != nil {
				return "", err
			}
			markdown = appendTranscription(markdown, text)
		}
	}
	return markdown, nil
}

// convertLocal runs the standard-library converter for a document format.
func convertLocal(data []byte, filename string) (string, error) {
	switch extOf(filename) {
	case ".pdf":
		return pdfToMarkdown(data, filename)
	case ".docx":
		return docxToMarkdown(data, filename)
	case ".pptx":
		return pptxToMarkdown(data, filename)
	case ".xlsx":
		return xlsxToMarkdown(data, filename)
	case ".html", ".htm":
		return htmlToMarkdown(data), nil
	case ".csv":
		return delimitedToMarkdown(data, ','), nil
	case ".tsv":
		return delimitedToMarkdown(data, '\t'), nil
	case ".json", ".jsonl", ".ndjson", ".ipynb":
		return jsonToMarkdown(data), nil
	case ".txt", ".text", ".md", ".markdown":
		return string(data), nil
	default:
		return "", unsupported("markitdown", filename,
			"the "+formatNameOf(filename)+" format needs a file parser this build does not ship")
	}
}
