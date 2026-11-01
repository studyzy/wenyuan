package memoryops

import (
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"strings"
	"sync"

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
			"File upload API is disabled. Set WENYUAN_API_ENABLE_FILE_UPLOAD_API=true to enable."))
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
	// The same write-scope checks a text retain runs, and before the batch gates
	// the reference runs them first too ("Same checks as a text retain: the
	// worker that converts and retains these files runs as internal work, so it
	// would never see the caller's scope", memory_engine.py:23567-23570): each
	// file's tags must be writable, and the scopes it would be stamped with are
	// a write by this caller as well.
	scopeItems := make([]storage.RetainItem, len(files))
	for index := range files {
		meta := fileRequest.filesMeta(index)
		scopeItems[index] = storage.RetainItem{Tags: meta.Tags, Strategy: meta.Strategy}
	}
	if err := h.retainWriteScopeFailure(r, bankID, RetainRequest{}, storage.RetainRequest{Items: scopeItems}); err != nil {
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
		item.visionEnabled = h.VisionEnabled
		// A file upload always carries a metadata dict: the reference builds the
		// item as `file_meta.metadata or {}` and its content dict records that
		// value unconditionally, so a document retained from an upload with no
		// metadata stores `{"metadata": {}}` rather than an empty retain_params.
		if item.Metadata == nil {
			item.Metadata = map[string]any{}
		}
		items = append(items, item)
	}
	// The reference's submission path refuses an unknown bank rather than
	// declaring one: `_submit_async_operation` raises `Bank '<id>' not found`,
	// which the handler turns into a 404. The upload is never stored, so the
	// bytes do not outlive the refusal.
	if _, err := h.Store.GetBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
		return
	}
	// One operation per file: the reference submits each file as its own
	// conversion operation, so a caller tracks each upload separately.
	overrides := httpx.TenantOverridesFrom(r)
	operationIDs := make([]string, 0, len(items))
	for index, item := range items {
		filename := files[index].Filename
		write := toStorageRetain(bankID, RetainRequest{Items: []RetainItem{item}, Async: true})
		// The bank's retain configuration applies to a converted upload too, so
		// the file route extracts with the same prompt and chunking as the JSON
		// route. The tenant's overrides layer under the bank's own. As on the
		// JSON route, a configuration that cannot be read refuses the upload
		// rather than storing it without the bank's Memory Defense policy.
		//
		// The identifier returned is this operation's: the conversion already
		// ran, but it is the first half of one unit of work, so a caller polls
		// the row the worker will run rather than a record that is already done
		// while the retain it belongs to has not started.
		operationID, err := h.queueFileRetain(r.Context(), bankID, overrides, write, item.DocumentID, filename)
		if err != nil {
			httpx.WriteErrorFor(w, r, h.mapWriteError(err))
			return
		}
		operationIDs = append(operationIDs, operationID)
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

// fileRetainPerFileFields are the FileRetainMetadata members that are not
// members of FileRetainRequest: naming one at the request's JSON root is a
// misplaced per-file field. `parser` belongs to both models, so it is legal in
// either position.
var fileRetainPerFileFields = []string{"context", "document_id", "metadata", "strategy", "tags", "timestamp"}

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
