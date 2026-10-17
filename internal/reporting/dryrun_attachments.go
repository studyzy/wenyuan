package reporting

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/memoryops"
	"github.com/studyzy/wenyuan/internal/storage"
)

// dryRunPlaceholderRe matches a well-formed inline-attachment placeholder and
// captures its short id.
var dryRunPlaceholderRe = regexp.MustCompile(`\x{27e6}hs-att:([0-9a-fA-F]+)\x{27e7}`)

// dryRunOccurrence is one inline attachment occurrence in a dry-run's canonical
// body, in placeholder order.
type dryRunOccurrence struct {
	blockIndex int
	kind       string
	mediaType  string
}

// dryRunOccurrences projects the decoded inline attachments into occurrence
// order, keeping each block's original index for attribution.
func dryRunOccurrences(inline []memoryops.InlineAttachment) []dryRunOccurrence {
	out := make([]dryRunOccurrence, 0, len(inline))
	for _, attachment := range inline {
		out = append(out, dryRunOccurrence{
			blockIndex: attachment.BlockIndex,
			kind:       attachment.Kind,
			mediaType:  attachment.MediaType,
		})
	}
	return out
}

// occurrencesBySegment partitions the ordered occurrences across the segments,
// using each segment's placeholder count, so a fact is attributed to the
// occurrences of the chunk it came from.
func occurrencesBySegment(segments []string, occurrences []dryRunOccurrence) [][]dryRunOccurrence {
	out := make([][]dryRunOccurrence, 0, len(segments))
	offset := 0
	for _, segment := range segments {
		count := len(dryRunPlaceholderRe.FindAllString(segment, -1))
		end := offset + count
		if end > len(occurrences) {
			end = len(occurrences)
		}
		if offset > len(occurrences) {
			offset = len(occurrences)
		}
		out = append(out, occurrences[offset:end])
		offset = end
	}
	return out
}

// renderChunkPlaceholders replaces each placeholder in chunk text with the
// human-readable "[Block #n: kind (media_type)]" note the preview shows.
func renderChunkPlaceholders(text string, occurrences []dryRunOccurrence) string {
	if len(occurrences) == 0 || !dryRunPlaceholderRe.MatchString(text) {
		return text
	}
	index := 0
	return dryRunPlaceholderRe.ReplaceAllStringFunc(text, func(token string) string {
		if index >= len(occurrences) {
			// Occurrences are exhausted: keep the original placeholder token,
			// exactly as the reference leaves an unmatched placeholder intact
			// rather than dropping it.
			return token
		}
		occurrence := occurrences[index]
		index++
		return "[Block #" + strconv.Itoa(occurrence.blockIndex) + ": " + occurrence.kind +
			" (" + occurrence.mediaType + ")]"
	})
}

// attachmentsForSegment returns the storage-shaped attachments whose
// placeholders appear in one segment, so the extractor can interleave their
// bytes into that segment's prompt.
func attachmentsForSegment(occurrences []dryRunOccurrence, byBlock map[int]memoryops.InlineAttachment) []storage.RetainAttachment {
	if len(occurrences) == 0 {
		return nil
	}
	out := make([]storage.RetainAttachment, 0, len(occurrences))
	for _, occurrence := range occurrences {
		attachment, ok := byBlock[occurrence.blockIndex]
		if !ok {
			continue
		}
		out = append(out, storage.RetainAttachment{
			ShortID:   attachment.ShortID,
			MediaType: attachment.MediaType,
			Kind:      attachment.Kind,
			Data:      attachment.Data,
		})
	}
	return out
}

// attachmentsForFact resolves a fact's from_attachments — one-based positions
// into the chunk's occurrences — to the attachment models the preview reports,
// in order and deduplicated by block.
func attachmentsForFact(fromAttachments []string, occurrences []dryRunOccurrence) []ExtractedFactAttachmentModel {
	out := make([]ExtractedFactAttachmentModel, 0, len(fromAttachments))
	seen := map[int]bool{}
	for _, entry := range fromAttachments {
		position, err := strconv.Atoi(strings.TrimSpace(entry))
		if err != nil || position < 1 || position > len(occurrences) {
			continue
		}
		occurrence := occurrences[position-1]
		if seen[occurrence.blockIndex] {
			continue
		}
		seen[occurrence.blockIndex] = true
		out = append(out, ExtractedFactAttachmentModel{
			BlockIndex: occurrence.blockIndex,
			Type:       occurrence.kind,
			MediaType:  occurrence.mediaType,
		})
	}
	return out
}

// addTokenUsage sums two dry-run usages field by field.
func addTokenUsage(base, extra storage.TokenUsage) storage.TokenUsage {
	return storage.TokenUsage{
		InputTokens:    base.InputTokens + extra.InputTokens,
		OutputTokens:   base.OutputTokens + extra.OutputTokens,
		TotalTokens:    base.TotalTokens + extra.TotalTokens,
		CachedTokens:   base.CachedTokens + extra.CachedTokens,
		ThoughtsTokens: base.ThoughtsTokens + extra.ThoughtsTokens,
	}
}
