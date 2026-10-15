package memoryops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/studyzy/wenyuan/internal/fileparse"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// attachmentShortIDLength is how much of the content digest identifies an
// attachment inside document text. It matches the reference, so a placeholder
// written by one side resolves on the other.
const attachmentShortIDLength = 12

// FileRetainResponse is the file-route acceptance body: the queued operation
// identifiers, one per file group.
type FileRetainResponse struct {
	OperationIDs []string `json:"operation_ids"`
}

// FileRetainRequest is the optional `request` form field: a parser selection
// and per-file metadata keyed by position.
type FileRetainRequest struct {
	Parser        ParserSelection  `json:"parser"`
	FilesMetadata []FileRetainMeta `json:"files_metadata"`
}

// FileRetainMeta is per-file metadata on the file route.
type FileRetainMeta struct {
	DocumentID string          `json:"document_id"`
	Context    string          `json:"context"`
	Metadata   map[string]any  `json:"metadata"`
	Tags       []string        `json:"tags"`
	Timestamp  string          `json:"timestamp"`
	Strategy   string          `json:"strategy"`
	Parser     ParserSelection `json:"parser"`
}

// ParserSelection is the `parser` value on the file route. The reference
// accepts a single parser name or an ordered fallback chain, so this decodes
// either a JSON string or a JSON array of strings into one slice.
type ParserSelection []string

// UnmarshalJSON accepts a bare string or an array of strings.
func (p *ParserSelection) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if strings.HasPrefix(trimmed, `"`) {
		var single string
		if err := json.Unmarshal(data, &single); err != nil {
			return err
		}
		if single == "" {
			return nil
		}
		*p = ParserSelection{single}
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	*p = ParserSelection(list)
	return nil
}

// defaultParserChainName is the shipped fallback chain when no parser is
// configured or requested: the built-in local parser.

// fileParserEnvSettingsOnce guards the one read of the environment-backed
// parser settings, so every handler set shares the same registry.
var (
	fileParserEnvSettingsOnce sync.Once
	fileParserEnvSettings     fileparse.Settings
)

// fileParserDefaults resolves the parser settings from the process environment
// once. The composition root normally wires the registry and chain explicitly;
// this is the fallback that keeps a directly-constructed handler set working.
func fileParserDefaults() fileparse.Settings {
	fileParserEnvSettingsOnce.Do(func() {
		fileParserEnvSettings = fileparse.FromEnv(os.Getenv)
	})
	return fileParserEnvSettings
}

// parserRegistry is the registry the file route converts with.
func (h *Handlers) parserRegistry() *fileparse.Registry {
	if h.Parsers != nil {
		return h.Parsers
	}
	return fileParserDefaults().Registry()
}

// effectiveParserChain is the chain applied when a request names no parser.
func (h *Handlers) effectiveParserChain() []string {
	if len(h.ParserChain) > 0 {
		return h.ParserChain
	}
	return fileParserDefaults().Chain
}

// parserAllowlist returns the parser names a client may request. An empty
// configured allowlist means every registered parser, as in the reference.
func (h *Handlers) parserAllowlist() []string {
	if len(h.ParserAllowlist) > 0 {
		return h.ParserAllowlist
	}
	return h.parserRegistry().List()
}

// allowedParser reports whether a requested parser name is in the allowlist.
func (h *Handlers) allowedParser(name string) bool {
	for _, allowed := range h.parserAllowlist() {
		if allowed == name {
			return true
		}
	}
	return false
}

// imageUploadExtensions are uploads handled as attachments: the bytes are
// stored and the retained text carries the attachment placeholder, because the
// bytes are an image rather than prose.
var imageUploadExtensions = map[string]struct{}{
	".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {}, ".webp": {}, ".bmp": {}, ".tif": {}, ".tiff": {},
}

// unsupportedUploadError reports an upload no configured parser could convert
// to text. The HTTP layer maps it to a 400.
type unsupportedUploadError struct {
	filename string
	format   string
}

func (e unsupportedUploadError) Error() string {
	return "file " + e.filename + " is a " + e.format +
		" document; no configured file parser could convert it to text"
}

// filepathExt is the extension of a filename including its leading dot, or ""
// when it carries none. Callers lowercase the result before a lookup.
func filepathExt(filename string) string {
	if dot := strings.LastIndexByte(filename, '.'); dot >= 0 {
		return filename[dot:]
	}
	return ""
}

// isImageUpload reports whether the upload should be stored as an attachment
// rather than read as text.
func isImageUpload(filename, contentType string) bool {
	if _, ok := imageUploadExtensions[strings.ToLower(filepathExt(filename))]; ok {
		return true
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "image/")
}

// FileRetain serves the file-to-memory write route. The route always processes
// asynchronously and returns the queued operation identifiers.
func (h *Handlers) FileRetain(w http.ResponseWriter, r *http.Request) {
	bankID := r.PathValue("bank_id")
	if h.FileUploadDisabled {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "file upload is not enabled"))
		return
	}
	// The body is bounded before it is parsed: a multipart handler reads the
	// upload into memory, so an unbounded body is a way to exhaust the process.
	// The bound admits the configured attachment count at the configured size,
	// plus room for the surrounding form fields.
	if limit := h.uploadBodyLimit(); limit > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, limit)
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		if errors.As(err, new(*http.MaxBytesError)) {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
				h.uploadTooLargeMessage()))
			return
		}
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, "request body must be a multipart upload"))
		return
	}
	if err := rejectRequestLevelFileFields(r); err != nil {
		httpx.WriteValidationFor(w, r, err)
		return
	}
	fileRequest, failure := h.parseFileRetainRequest(r)
	if failure != nil {
		httpx.WriteValidationFor(w, r, failure)
		return
	}
	files := r.MultipartForm.File["files"]
	if fileRequest.metadata != nil && len(fileRequest.metadata) != len(files) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			fmt.Sprintf("files_metadata count (%d) must match files count (%d)",
				len(fileRequest.metadata), len(files))))
		return
	}
	if h.MaxAttachments > 0 && len(files) > h.MaxAttachments {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			fmt.Sprintf("the request carries %d files, more than the %d-file limit. Split the upload across several requests.",
				len(files), h.MaxAttachments)))
		return
	}
	if h.MaxFileBatchCount > 0 && len(files) > h.MaxFileBatchCount {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			fmt.Sprintf("Too many files. Maximum %d files per request.", h.MaxFileBatchCount)))
		return
	}
	if h.MaxFileBatchBytes > 0 {
		var total int64
		for _, header := range files {
			total += header.Size
		}
		if total > h.MaxFileBatchBytes {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
				fmt.Sprintf("Total batch size (%.1fMB) exceeds maximum of %dMB",
					float64(total)/(1<<20), h.MaxFileBatchBytes>>20)))
			return
		}
	}
	items := make([]RetainItem, 0, len(files))
	for index, header := range files {
		meta := fileRequest.filesMeta(index)
		// The document is decided before the bytes are stored: an attachment row
		// keys on the document that owns it, so the id has to exist first. It is
		// the caller's when they named one, and a minted identifier otherwise —
		// the same rule the inline-attachment ingress follows.
		documentID := meta.DocumentID
		if documentID == "" {
			documentID = storage.NewDocumentID()
		}
		item, err := h.itemFromUpload(r, bankID, documentID, header, resolveParserChain(meta.Parser, fileRequest.parser, h.effectiveParserChain()))
		if err != nil {
			var oversize oversizeUploadError
			if errors.As(err, &oversize) {
				httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, oversize.Error()))
				return
			}
			var unsupported unsupportedUploadError
			if errors.As(err, &unsupported) {
				httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, unsupported.Error()))
				return
			}
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindInternal, httpx.DetailInternal))
			return
		}
		applyFileMetadata(&item, meta)
		items = append(items, item)
	}
	if len(items) == 0 {
		httpx.WriteValidationFor(w, r, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.Missing([]string{"body", "files"}, map[string]any{}),
		}})
		return
	}
	req := RetainRequest{Items: items, Async: true}
	// Reject an update mode placed per file, which the file route does not accept.
	if field := updateModeInItems(items); field != "" {
		httpx.WriteValidationFor(w, r, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.ValueError([]string{"body"}, field,
				"'update_mode' is not supported by /files/retain, which always processes asynchronously"),
		}})
		return
	}
	write := toStorageRetain(bankID, req)
	// The bank's retain configuration applies to a converted upload too, so the
	// file route extracts with the same prompt and chunking as the JSON route.
	// The tenant's overrides layer under the bank's own. As on the JSON route, a
	// configuration that cannot be read refuses the upload rather than storing
	// it without the bank's Memory Defense policy.
	result, bank, err := h.executeRetain(r.Context(), bankID, httpx.TenantOverridesFrom(r), write)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
		return
	}
	h.enqueuePostRetainConsolidation(r.Context(), bankID, bank)
	httpx.WriteJSONFor(w, r, http.StatusOK, FileRetainResponse{
		OperationIDs: result.OperationIDs,
	})
}

// parsedFileRetainRequest carries the optional request field.
type parsedFileRetainRequest struct {
	parser   ParserSelection
	metadata []FileRetainMeta
}

// filesMeta returns the metadata for one file position, or a zero value.
func (p parsedFileRetainRequest) filesMeta(index int) FileRetainMeta {
	if index < len(p.metadata) {
		return p.metadata[index]
	}
	return FileRetainMeta{}
}

// resolveParserChain applies the reference's precedence: a per-file parser, then
// the request-level parser, then the server's configured default chain.
func resolveParserChain(perFile, requestLevel ParserSelection, fallback []string) []string {
	if len(perFile) > 0 {
		return perFile
	}
	if len(requestLevel) > 0 {
		return requestLevel
	}
	return fallback
}

// parseFileRetainRequest reads the optional `request` form field and validates
// every requested parser against the allowlist, reporting a validation failure
// for a parser the deployment does not offer.
func (h *Handlers) parseFileRetainRequest(r *http.Request) (parsedFileRetainRequest, *httpx.ValidationFailure) {
	raw := strings.TrimSpace(r.FormValue("request"))
	if raw == "" {
		return parsedFileRetainRequest{}, nil
	}
	var body FileRetainRequest
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return parsedFileRetainRequest{}, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.ValidationItem("json_invalid", []string{"body", "request"}, "JSON decode error", nil,
				map[string]any{"error": err.Error()}),
		}}
	}
	selections := []ParserSelection{body.Parser}
	for _, meta := range body.FilesMetadata {
		selections = append(selections, meta.Parser)
	}
	for _, selection := range selections {
		for _, parser := range selection {
			if parser == "" || h.allowedParser(parser) {
				continue
			}
			return parsedFileRetainRequest{}, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
				httpx.ValueError([]string{"body", "request", "parser"}, parser,
					"Parser(s) not available: "+parser),
			}}
		}
	}
	return parsedFileRetainRequest{parser: body.Parser, metadata: body.FilesMetadata}, nil
}

// applyFileMetadata overlays per-file metadata onto the retain item.
func applyFileMetadata(item *RetainItem, meta FileRetainMeta) {
	if meta.DocumentID != "" {
		item.DocumentID = meta.DocumentID
	}
	if meta.Context != "" {
		item.Context = meta.Context
	}
	if meta.Metadata != nil {
		item.Metadata = meta.Metadata
	}
	if len(meta.Tags) > 0 {
		item.Tags = TagList(meta.Tags)
	}
	if meta.Strategy != "" {
		item.Strategy = meta.Strategy
	}
	if meta.Timestamp != "" {
		if parsed, err := time.Parse(time.RFC3339, meta.Timestamp); err == nil {
			utc := parsed.UTC()
			item.Timestamp = TimestampField{Set: true, Value: &utc}
		}
	}
}

// rejectRequestLevelFileFields rejects per-file fields placed at the request
// level instead of nested per file.
func rejectRequestLevelFileFields(r *http.Request) *httpx.ValidationFailure {
	for _, name := range []string{"content", "document_id", "timestamp", "metadata"} {
		if r.FormValue(name) != "" {
			return &httpx.ValidationFailure{Errors: []httpx.ValidationError{
				httpx.ValueError([]string{"body", name}, nil,
					"per-file fields must be placed in the corresponding 'files_metadata' entry"),
			}}
		}
	}
	return nil
}

func updateModeInItems(items []RetainItem) string {
	for _, item := range items {
		if item.UpdateMode != "" {
			return item.UpdateMode
		}
	}
	return ""
}

// itemFromUpload reads one uploaded file, converts it to markdown through the
// requested parser chain, records the original bytes content-addressed under
// the owning bank and document, and returns the retain item that carries the
// converted text. The write is idempotent on the content hash, so re-uploading
// an unchanged file rewrites nothing observable. A retain that later fails
// leaves a blob cached by key, which the next upload of the same content
// reuses rather than duplicating.
func (h *Handlers) itemFromUpload(r *http.Request, bankID, documentID string, header *multipart.FileHeader, parserChain []string) (RetainItem, error) {
	// The declared size is checked before the bytes are read, so an oversize
	// upload is refused rather than loaded and then rejected.
	if h.MaxAttachmentBytes > 0 && header.Size > h.MaxAttachmentBytes {
		return RetainItem{}, oversizeUploadError{
			filename: header.Filename, size: header.Size, limit: h.MaxAttachmentBytes,
		}
	}
	file, err := header.Open()
	if err != nil {
		return RetainItem{}, err
	}
	defer func() { _ = file.Close() }()
	// A lying Content-Length cannot get past the read either: the reader is
	// capped at the limit, so the slice never grows past it.
	var reader io.Reader = file
	if h.MaxAttachmentBytes > 0 {
		reader = io.LimitReader(file, h.MaxAttachmentBytes+1)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return RetainItem{}, err
	}
	if h.MaxAttachmentBytes > 0 && int64(len(data)) > h.MaxAttachmentBytes {
		return RetainItem{}, oversizeUploadError{
			filename: header.Filename, size: int64(len(data)), limit: h.MaxAttachmentBytes,
		}
	}
	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	image := isImageUpload(header.Filename, contentType)
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	shortID := hash[:attachmentShortIDLength]
	// An image is stored as bytes and referenced from the retained text by its
	// placeholder; anything else is converted to markdown by the parser chain.
	// A format no parser can convert is refused with a 4xx naming the format,
	// rather than storing binary noise as memory text.
	var text string
	if image {
		text = AttachmentPlaceholder(shortID)
	} else {
		result, err := h.parserRegistry().ConvertWithFallback(parserChain, data, header.Filename, contentType)
		if err != nil {
			return RetainItem{}, unsupportedUploadError{
				filename: header.Filename,
				format:   fileparse.FormatName(header.Filename),
			}
		}
		text = result.Content
	}

	key := storage.AttachmentStorageKey(bankID, documentID, hash)
	// The reference deletes the original file from file storage once it has been
	// converted and retained (HINDSIGHT_API_FILE_DELETE_AFTER_RETAIN), keeping
	// only the retained text; with the setting on this build never writes the
	// bytes in the first place, which reaches the same state without a
	// store-then-delete round trip. An image's bytes are its content, so they
	// are always kept.
	if !h.DeleteAfterRetain || image {
		if err := h.Store.PutStoredFile(r.Context(), storage.StoredFile{
			Key: key, ContentType: contentType, Data: data,
		}); err != nil {
			return RetainItem{}, err
		}
	}
	if err := h.Store.PutAttachment(r.Context(), storage.Attachment{
		BankID:         bankID,
		DocumentID:     documentID,
		AttachmentHash: hash,
		ShortID:        shortID,
		MediaType:      contentType,
		ByteSize:       int64(len(data)),
		StorageKey:     key,
		Kind:           "file",
		Filename:       header.Filename,
	}); err != nil {
		return RetainItem{}, err
	}
	return RetainItem{
		Content:       Content{Text: text},
		DocumentID:    documentID,
		AttachmentIDs: []string{shortID},
	}, nil
}

// uploadBodyLimit bounds one multipart request body: the configured attachment
// count at the configured size, plus a margin for the surrounding form fields
// and multipart framing. A zero limit leaves the body unbounded, which is what
// an unconfigured deployment had before the bound existed.
func (h *Handlers) uploadBodyLimit() int64 {
	if h.MaxAttachments <= 0 || h.MaxAttachmentBytes <= 0 {
		return 0
	}
	return h.MaxAttachmentBytes*int64(h.MaxAttachments) + multipartOverheadBytes
}

// multipartOverheadBytes is the margin added to the body bound for the form
// fields, boundaries and headers around the files.
const multipartOverheadBytes = 1 << 20

// uploadTooLargeMessage is the refusal a body over the bound gets.
func (h *Handlers) uploadTooLargeMessage() string {
	return fmt.Sprintf("the upload exceeds the %d-file limit of %s per file.",
		h.MaxAttachments, humanBytes(h.MaxAttachmentBytes))
}

// oversizeUploadError reports one upload larger than the configured per-file
// limit.
type oversizeUploadError struct {
	filename string
	size     int64
	limit    int64
}

func (e oversizeUploadError) Error() string {
	return fmt.Sprintf("file %s is %s, exceeding the %s limit for a single attachment.",
		e.filename, humanBytes(e.size), humanBytes(e.limit))
}

// humanBytes renders a byte count in the units the limit was configured in.
func humanBytes(size int64) string {
	const mega = 1 << 20
	if size%mega == 0 {
		return fmt.Sprintf("%dMB", size/mega)
	}
	return fmt.Sprintf("%.1fMB", float64(size)/mega)
}
