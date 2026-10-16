package fileparse

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider/vlm"
)

// stubTranscriber records the images it was handed and replies with a scripted
// transcription, so the parser's extraction and joining are testable without a
// live vision endpoint.
type stubTranscriber struct {
	images []vlm.Image
	reply  string
	err    error
}

func (s *stubTranscriber) Transcribe(_ context.Context, image vlm.Image) (string, error) {
	s.images = append(s.images, image)
	if s.err != nil {
		return "", s.err
	}
	return s.reply, nil
}

// docxWithMedia builds a minimal .docx whose only image is the given media
// member, so the embedded-image path is exercised without a committed binary.
func docxWithMedia(t *testing.T, name string, image []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(member string, data []byte) {
		w, err := zw.Create(member)
		if err != nil {
			t.Fatalf("create %s: %v", member, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("write %s: %v", member, err)
		}
	}
	write("word/document.xml", []byte(`<?xml version="1.0"?><w:document><w:body><w:p><w:r><w:t>Quarterly report</w:t></w:r></w:p></w:body></w:document>`))
	write("word/media/"+name, image)
	if err := zw.Close(); err != nil {
		t.Fatalf("close docx: %v", err)
	}
	return buf.Bytes()
}

// TestOCRDisabledLeavesConversionUnchanged asserts the OCR-off parser produces
// exactly the text it produced before OCR existed and still refuses an image.
func TestOCRDisabledLeavesConversionUnchanged(t *testing.T) {
	parser := NewMarkitdownParser()
	data := readFixture(t, "sample.docx")
	got, err := parser.Convert(data, "sample.docx")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	want, err := convertLocal(data, "sample.docx")
	if err != nil {
		t.Fatalf("convertLocal: %v", err)
	}
	if got != want {
		t.Fatalf("OCR-off conversion changed:\n got %q\nwant %q", got, want)
	}
	if parser.Supports("photo.png", "") {
		t.Fatal("an image must not be supported while OCR is disabled")
	}
	if _, err := parser.Convert([]byte("png"), "photo.png"); !IsUnsupported(err) {
		t.Fatalf("converting an image with OCR off should be unsupported, got %v", err)
	}
}

// TestOCREnabledAppendsEmbeddedImageTranscription asserts a document's embedded
// image is handed to the transcriber and its transcription joins the markdown.
func TestOCREnabledAppendsEmbeddedImageTranscription(t *testing.T) {
	stub := &stubTranscriber{reply: "Invoice total: 42 EUR"}
	parser := markitdownParser{ocr: stub}
	docx := docxWithMedia(t, "image1.png", []byte("\x89PNG\r\n\x1a\npixels"))
	out, err := parser.Convert(docx, "report.docx")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !strings.Contains(out, "Quarterly report") {
		t.Fatalf("parsed markdown lost the document text:\n%s", out)
	}
	if !strings.Contains(out, "Invoice total: 42 EUR") {
		t.Fatalf("transcription missing from parsed text:\n%s", out)
	}
	if len(stub.images) != 1 {
		t.Fatalf("images handed to the transcriber = %d, want 1", len(stub.images))
	}
	if stub.images[0].MediaType != "image/png" {
		t.Fatalf("image media type = %q", stub.images[0].MediaType)
	}
	if string(stub.images[0].Data) != "\x89PNG\r\n\x1a\npixels" {
		t.Fatalf("image bytes = %q", stub.images[0].Data)
	}
}

// TestOCREnabledTranscribesStandaloneImage asserts an image file is transcribed
// rather than refused once OCR is on.
func TestOCREnabledTranscribesStandaloneImage(t *testing.T) {
	stub := &stubTranscriber{reply: "hello from the picture"}
	parser := markitdownParser{ocr: stub}
	if !parser.Supports("photo.jpeg", "") {
		t.Fatal("an image must be supported once OCR is enabled")
	}
	out, err := parser.Convert([]byte("jpegbytes"), "photo.jpeg")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if out != "hello from the picture" {
		t.Fatalf("transcription = %q", out)
	}
	if len(stub.images) != 1 || stub.images[0].MediaType != "image/jpeg" {
		t.Fatalf("images = %+v", stub.images)
	}
}

// TestOCREnabledThroughStubVisionEndpoint asserts the whole chain: a parser
// built from valid settings sends a document's embedded image to the configured
// endpoint and the endpoint's transcription lands in the parsed text.
func TestOCREnabledThroughStubVisionEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"TRANSCRIBED PAGE"}}]}`))
	}))
	defer server.Close()

	parser, err := newMarkitdownParser(vlm.Config{BaseURL: server.URL, APIKey: "k", Model: "m"}, true)
	if err != nil {
		t.Fatalf("newMarkitdownParser: %v", err)
	}
	docx := docxWithMedia(t, "scan.jpg", []byte("jpegbytes"))
	out, err := parser.Convert(docx, "scan.docx")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !strings.Contains(out, "TRANSCRIBED PAGE") {
		t.Fatalf("transcription missing from parsed text:\n%s", out)
	}
}

// TestRegistryFailsOnIncompleteOCR asserts a registry that would silently parse
// without OCR refuses construction and names the missing setting.
func TestRegistryFailsOnIncompleteOCR(t *testing.T) {
	base := Settings{OCREnabled: true, OCRAPIKey: "k", OCRBaseURL: "https://example.test/v1", OCRModel: "m"}
	cases := []struct {
		name    string
		mutate  func(*Settings)
		missing string
	}{
		{"model", func(s *Settings) { s.OCRModel = "" }, vlm.EnvOCRModel},
		{"api key", func(s *Settings) { s.OCRAPIKey = "" }, vlm.EnvOCRAPIKey},
		{"base url", func(s *Settings) { s.OCRBaseURL = "" }, vlm.EnvOCRBaseURL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			settings := base
			tc.mutate(&settings)
			_, err := settings.Registry()
			if err == nil {
				t.Fatalf("Registry with no %s should fail", tc.name)
			}
			if !strings.Contains(err.Error(), tc.missing) {
				t.Fatalf("error %q does not name %q", err, tc.missing)
			}
		})
	}
}

// TestOCREnabledTranscribesPDFImage asserts a PDF's embedded JPEG image is
// transcribed and appended, which is the scanned-document case.
func TestOCREnabledTranscribesPDFImage(t *testing.T) {
	stub := &stubTranscriber{reply: "scanned paragraph"}
	parser := markitdownParser{ocr: stub}
	out, err := parser.Convert(pdfWithJPEG(t), "scan.pdf")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !strings.Contains(out, "scanned paragraph") {
		t.Fatalf("transcription missing from parsed text:\n%s", out)
	}
	if len(stub.images) != 1 || stub.images[0].MediaType != "image/jpeg" {
		t.Fatalf("images = %+v", stub.images)
	}
	if !bytes.HasPrefix(stub.images[0].Data, []byte{0xFF, 0xD8}) {
		t.Fatal("extracted image is not a JPEG")
	}
}

// pdfWithJPEG builds a minimal PDF embedding one DCTDecode image XObject.
func pdfWithJPEG(t *testing.T) []byte {
	t.Helper()
	var jpegBuf bytes.Buffer
	if err := jpeg.Encode(&jpegBuf, image.NewRGBA(image.Rect(0, 0, 1, 1)), nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	jpeg := jpegBuf.Bytes()
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	buf.WriteString("1 0 obj\n<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode /Length ")
	buf.WriteString(strconv.Itoa(len(jpeg)))
	buf.WriteString(" >>\nstream\n")
	buf.Write(jpeg)
	buf.WriteString("\nendstream\nendobj\n%%EOF\n")
	return buf.Bytes()
}

// TestFromEnvResolvesOCRSettings asserts the OCR settings are read from the
// reference's environment keys, including the JSON header map.
func TestFromEnvResolvesOCRSettings(t *testing.T) {
	env := map[string]string{
		EnvFileParserMarkitdownOCREnabled: "true",
		EnvFileParserMarkitdownOCRAPIKey:  "key",
		EnvFileParserMarkitdownOCRBaseURL: "https://example.test/v1",
		EnvFileParserMarkitdownOCRModel:   "vision-1",
		EnvFileParserMarkitdownOCRPrompt:  "transcribe",
		EnvFileParserMarkitdownOCRHeaders: `{"X-Tenant":"acme"}`,
	}
	settings := FromEnv(func(key string) string { return env[key] })
	if !settings.OCREnabled || settings.OCRAPIKey != "key" || settings.OCRBaseURL != "https://example.test/v1" ||
		settings.OCRModel != "vision-1" || settings.OCRPrompt != "transcribe" {
		t.Fatalf("ocr settings = %+v", settings)
	}
	if settings.OCRHeaders["X-Tenant"] != "acme" {
		t.Fatalf("headers = %v", settings.OCRHeaders)
	}
	if _, err := settings.Registry(); err != nil {
		t.Fatalf("Registry: %v", err)
	}
}
