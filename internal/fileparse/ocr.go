package fileparse

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"path"
	"strings"

	"github.com/studyzy/wenyuan/internal/provider/vlm"
)

// imageTranscriber turns one embedded image into text. *vlm.Transcriber is the
// production implementation; tests substitute a stub, so the parser's
// extraction and joining are testable without a live vision endpoint.
type imageTranscriber interface {
	Transcribe(ctx context.Context, image vlm.Image) (string, error)
}

// maxOCRImageBytes bounds one extracted image, so a hostile document cannot
// make the parser base64-encode an unbounded buffer into a request body.
const maxOCRImageBytes = 32 << 20

// ocrImageMediaTypes are the standalone image formats the parser reads once OCR
// is enabled. Without OCR they are reported as unsupported, mirroring the
// reference, which refuses an image file when OCR is off.
var ocrImageMediaTypes = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png",
}

// mediaTypeByExtension maps an embedded image's extension to its IANA type. A
// format absent here (a vector EMF/WMF on Windows, say) cannot be handed to the
// vision endpoint, so it is skipped rather than sent as a mislabelled blob.
var mediaTypeByExtension = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".bmp": "image/bmp",
	".tif": "image/tiff", ".tiff": "image/tiff",
}

// ooxmlMediaPrefixes are the archive paths under which each OOXML format stores
// the images its document embeds.
var ooxmlMediaPrefixes = map[string]string{
	".docx": "word/media/",
	".pptx": "ppt/media/",
	".xlsx": "xl/media/",
}

// transcribeImages transcribes each image in order and joins the transcriptions
// into one section, skipping an image the model returned nothing for.
func (p markitdownParser) transcribeImages(images []vlm.Image) (string, error) {
	var out strings.Builder
	for _, image := range images {
		text, err := p.ocr.Transcribe(context.Background(), image)
		if err != nil {
			return "", err
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if out.Len() > 0 {
			out.WriteString("\n\n")
		}
		out.WriteString(text)
	}
	return out.String(), nil
}

// embeddedImages returns the images a document embeds that this build can hand
// to a vision model: the media parts of an OOXML container, or the JPEG image
// XObjects of a PDF. A format with no extractable embedded images returns nil.
func embeddedImages(ext string, data []byte) []vlm.Image {
	if _, ok := ooxmlMediaPrefixes[ext]; ok {
		return ooxmlImages(ext, data)
	}
	if ext == ".pdf" {
		return pdfImages(data)
	}
	return nil
}

// ooxmlImages reads the media members of an OOXML container in archive order,
// so the transcription order is stable across runs.
func ooxmlImages(ext string, data []byte) []vlm.Image {
	prefix := ooxmlMediaPrefixes[ext]
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil
	}
	var out []vlm.Image
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, prefix) {
			continue
		}
		mediaType := mediaTypeByExtension[strings.ToLower(path.Ext(f.Name))]
		if mediaType == "" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(rc, maxOCRImageBytes+1))
		_ = rc.Close()
		if err != nil || len(raw) == 0 || len(raw) > maxOCRImageBytes {
			continue
		}
		out = append(out, vlm.Image{Data: raw, MediaType: mediaType})
	}
	return out
}

// appendTranscription joins a document's parsed markdown with the transcribed
// text of its images, so the transcription reads as a trailing section.
func appendTranscription(markdown, transcription string) string {
	transcription = strings.TrimSpace(transcription)
	if transcription == "" {
		return markdown
	}
	if strings.TrimSpace(markdown) == "" {
		return transcription
	}
	if !strings.HasSuffix(markdown, "\n") {
		markdown += "\n"
	}
	return markdown + "\n" + transcription + "\n"
}
