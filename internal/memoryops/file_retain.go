package memoryops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
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
		// An empty string is a name like any other: the reference resolves it to
		// a one-element chain ([""]) that then fails the allowlist, rather than
		// treating it as "no parser requested".
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
func (h *Handlers) parserRegistry() (*fileparse.Registry, error) {
	if h.Parsers != nil {
		return h.Parsers, nil
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
// configured allowlist means every registered parser, as in the reference. A
// registry that cannot be built leaves no parser requestable, which the route
// reports as a refusal rather than converting with a parser it cannot name.
func (h *Handlers) parserAllowlist() []string {
	if len(h.ParserAllowlist) > 0 {
		return h.ParserAllowlist
	}
	registry, err := h.parserRegistry()
	if err != nil {
		return nil
	}
	return registry.List()
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
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound,
			"File upload API is disabled. Set HINDSIGHT_API_ENABLE_FILE_UPLOAD_API=true to enable."))
		return
	}
	// A body that is not multipart cannot carry the two required parts, which
	// the reference reports as a validation error rather than a sentence.
	if !isMultipartForm(r) {
		httpx.WriteValidationFailure(w, missingFileRetainParts())
		return
	}
	// The body is bounded before it is parsed: a multipart handler reads the
	// upload into memory, so an unbounded body is a way to exhaust the process.
	if limit := h.uploadBodyLimit(); limit > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, limit)
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		if errors.As(err, new(*http.MaxBytesError)) {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
				h.uploadTooLargeMessage()))
			return
		}
		httpx.WriteValidationFailure(w, missingFileRetainParts())
		return
	}
	// Both form parts are required and the framework validates them together,
	// in declaration order (`files` before `request`), before the handler runs:
	// a body missing both reports both, and one missing either reports that one.
	files := r.MultipartForm.File["files"]
	rawRequest, present := firstFormValue(r, "request")
	if len(files) == 0 || !present {
		var missing []httpx.ValidationError
		if len(files) == 0 {
			missing = append(missing, httpx.Missing([]string{"body", "files"}, nil))
		}
		if !present {
			missing = append(missing, httpx.Missing([]string{"body", "request"}, nil))
		}
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: missing})
		return
	}
	fileRequest, err := h.parseFileRetainRequest(rawRequest)
	if err != nil {
		httpx.WriteErrorFor(w, r, err)
		return
	}
	// The reference's request-layer gates run in a fixed order — file count,
	// then the files_metadata count, then the request-level parser — so a request
	// that trips several names the first one the reference would name.
	if h.MaxFileBatchCount > 0 && len(files) > h.MaxFileBatchCount {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			fmt.Sprintf("Too many files. Maximum %d files per request.", h.MaxFileBatchCount)))
		return
	}
	// An empty files_metadata list is absent, not a zero-length alignment: the
	// reference's truthiness test skips the count check for it and every file
	// keeps its defaults.
	if len(fileRequest.metadata) > 0 && len(fileRequest.metadata) != len(files) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			fmt.Sprintf("files_metadata count (%d) must match files count (%d)",
				len(fileRequest.metadata), len(files))))
		return
	}
	if len(fileRequest.parser) > 0 {
		if err := h.validateParserChain(fileRequest.parser, "request-level parser"); err != nil {
			httpx.WriteErrorFor(w, r, err)
			return
		}
	}
	// Every file's parser chain is resolved and validated before the batch-size
	// bound, so a request that both overflows the batch and names a bad parser
	// reports the parser, as the reference does: it validates the chain inside
	// its per-file loop and only checks the total afterwards.
	chains := make([][]string, len(files))
	for index, header := range files {
		meta := fileRequest.filesMeta(index)
		chain := resolveParserChain(meta.Parser, fileRequest.parser, h.effectiveParserChain())
		if err := h.validateParserChain(chain, "file '"+header.Filename+"'"); err != nil {
			httpx.WriteErrorFor(w, r, err)
			return
		}
		chains[index] = chain
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
		chain := chains[index]
		// The document is decided before the bytes are stored: an attachment row
		// keys on the document that owns it, so the id has to exist first. It is
		// the caller's when they named one, and a minted `file_<uuid>` otherwise.
		documentID := meta.DocumentID
		if documentID == "" {
			documentID = "file_" + storage.NewDocumentID()
		}
		item, err := h.itemFromUpload(r, bankID, documentID, header, chain)
		if err != nil {
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
	// One operation per file: the reference submits each file as its own
	// conversion operation, so a caller tracks each upload separately.
	overrides := httpx.TenantOverridesFrom(r)
	operationIDs := make([]string, 0, len(items))
	for _, item := range items {
		write := toStorageRetain(bankID, RetainRequest{Items: []RetainItem{item}, Async: true})
		// The bank's retain configuration applies to a converted upload too, so
		// the file route extracts with the same prompt and chunking as the JSON
		// route. The tenant's overrides layer under the bank's own. As on the
		// JSON route, a configuration that cannot be read refuses the upload
		// rather than storing it without the bank's Memory Defense policy.
		result, bank, err := h.executeRetain(r.Context(), bankID, overrides, write)
		if err != nil {
			httpx.WriteErrorFor(w, r, h.mapWriteError(err))
			return
		}
		operationIDs = append(operationIDs, result.OperationID)
		h.enqueuePostRetainConsolidation(r.Context(), bankID, bank)
	}
	httpx.WriteJSON(w, http.StatusOK, FileRetainResponse{OperationIDs: operationIDs})
}

// missingFileRetainParts is the validation failure for a body that cannot
// carry the route's two required parts.
func missingFileRetainParts() *httpx.ValidationFailure {
	return &httpx.ValidationFailure{Errors: []httpx.ValidationError{
		httpx.Missing([]string{"body", "files"}, nil),
		httpx.Missing([]string{"body", "request"}, nil),
	}}
}

// isMultipartForm reports whether the request declares a multipart body.
func isMultipartForm(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "multipart/form-data"
}

// firstFormValue returns a form field's value and whether the field was present
// at all, so a required-but-empty part is distinguishable from a missing one.
func firstFormValue(r *http.Request, name string) (string, bool) {
	values, ok := r.MultipartForm.Value[name]
	if !ok || len(values) == 0 {
		return "", false
	}
	return values[0], true
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

// parseFileRetainRequest decodes the required `request` form field. The field
// is a validated JSON string on the reference, so a JSON or shape error is its
// 400 "Invalid request JSON" refusal rather than a nested-model validation
// item, and per-file fields placed at the JSON root are refused the same way.
func (h *Handlers) parseFileRetainRequest(raw string) (parsedFileRetainRequest, *httpx.HTTPError) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return parsedFileRetainRequest{}, invalidRequestJSONError("Invalid JSON", err.Error())
	}
	if root == nil {
		return parsedFileRetainRequest{}, invalidRequestJSONError("Value error",
			"Input should be a valid dictionary or instance of FileRetainRequest")
	}
	if message := misplacedFileFieldMessage(root); message != "" {
		return parsedFileRetainRequest{}, invalidRequestJSONError("Value error", message)
	}
	var body FileRetainRequest
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return parsedFileRetainRequest{}, invalidRequestJSONError("Value error", err.Error())
	}
	return parsedFileRetainRequest{parser: body.Parser, metadata: body.FilesMetadata}, nil
}

// fileRetainPerFileFields are the FileRetainMetadata members that are not
// members of FileRetainRequest: naming one at the request's JSON root is a
// misplaced per-file field. `parser` belongs to both models, so it is legal in
// either position.
var fileRetainPerFileFields = []string{"context", "document_id", "metadata", "strategy", "tags", "timestamp"}

// misplacedFileFieldMessage reports the per-file fields a request JSON placed at
// its root, in the reference's message shape, or "" when the root is clean.
func misplacedFileFieldMessage(root map[string]json.RawMessage) string {
	misplaced := make([]string, 0, len(fileRetainPerFileFields))
	for _, name := range fileRetainPerFileFields {
		if raw, ok := root[name]; ok && !isJSONNull(raw) {
			misplaced = append(misplaced, name)
		}
	}
	problems := make([]string, 0, 2)
	if len(misplaced) > 0 {
		problems = append(problems, fmt.Sprintf(
			"Per-file fields (%s) must be placed in the corresponding 'files_metadata' entry",
			strings.Join(misplaced, ", ")))
	}
	if raw, ok := root["update_mode"]; ok && !isJSONNull(raw) {
		problems = append(problems, "'update_mode' is not supported by /files/retain, which always processes asynchronously")
	}
	return strings.Join(problems, "; ")
}

// isJSONNull reports whether a raw JSON value is the literal null.
func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

// invalidRequestJSONError renders the reference's refusal for the `request`
// form field: a pydantic validation error string wrapped in a 400 detail.
func invalidRequestJSONError(kind, message string) *httpx.HTTPError {
	return httpx.NewHTTPError(httpx.KindBadRequest,
		"Invalid request JSON: 1 validation error for FileRetainRequest\n  "+kind+", "+message)
}

// validateParserChain refuses a parser chain naming a parser the deployment does
// not offer, in the reference's 400 shape: the scope the chain came from and the
// available parsers.
func (h *Handlers) validateParserChain(chain []string, context string) *httpx.HTTPError {
	disallowed := make([]string, 0, len(chain))
	for _, parser := range chain {
		if h.allowedParser(parser) {
			continue
		}
		disallowed = append(disallowed, parser)
	}
	if len(disallowed) == 0 {
		return nil
	}
	return httpx.NewHTTPError(httpx.KindBadRequest,
		fmt.Sprintf("Parser(s) not available (%s): %s. Available: %s",
			context, pythonStringList(disallowed), pythonStringList(h.parserAllowlist())))
}

// pythonStringList renders a string list the way the reference's f-string does,
// so the refusal text carries the same names and quoting.
func pythonStringList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, "'"+value+"'")
	}
	return "[" + strings.Join(quoted, ", ") + "]"
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

// itemFromUpload reads one uploaded file, converts it to markdown through the
// requested parser chain, records the original bytes content-addressed under
// the owning bank and document, and returns the retain item that carries the
// converted text. The write is idempotent on the content hash, so re-uploading
// an unchanged file rewrites nothing observable. A retain that later fails
// leaves a blob cached by key, which the next upload of the same content
// reuses rather than duplicating.
func (h *Handlers) itemFromUpload(r *http.Request, bankID, documentID string, header *multipart.FileHeader, parserChain []string) (RetainItem, error) {
	// The reference bounds only the whole batch on this route (the conversion
	// batch size and byte total, enforced by the caller); it places no per-file
	// size cap, so a single large upload is accepted as long as the batch bound
	// holds.
	file, err := header.Open()
	if err != nil {
		return RetainItem{}, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	if err != nil {
		return RetainItem{}, err
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
		registry, err := h.parserRegistry()
		if err != nil {
			return RetainItem{}, err
		}
		result, err := registry.ConvertWithFallback(parserChain, data, header.Filename, contentType)
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

// humanBytes renders a byte count in the units the limit was configured in.
func humanBytes(size int64) string {
	const mega = 1 << 20
	if size%mega == 0 {
		return fmt.Sprintf("%dMB", size/mega)
	}
	return fmt.Sprintf("%.1fMB", float64(size)/mega)
}
