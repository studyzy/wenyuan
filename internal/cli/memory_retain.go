package cli

// The `memory retain` and `memory retain-files` actions. Retain posts one
// item synchronously or asynchronously; retain-files walks a path, batches
// the files ten per multipart request, and polls the bank operations list
// until every upload settles.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// retainPollInterval is the delay between operation-list polls. A variable
// so tests do not wait real time.
var retainPollInterval = 500 * time.Millisecond

// retainFileBatchSize is how many files one multipart request carries.
const retainFileBatchSize = 10

// retainFileExtensions is the case-insensitive extension filter for
// directory walks. Extension-less files never match.
var retainFileExtensions = map[string]bool{
	"pdf": true, "docx": true, "doc": true, "pptx": true, "ppt": true,
	"xlsx": true, "xls": true, "jpg": true, "jpeg": true, "png": true,
	"gif": true, "bmp": true, "webp": true, "tiff": true, "html": true,
	"htm": true, "txt": true, "md": true, "csv": true, "json": true,
	"yaml": true, "yml": true, "toml": true, "xml": true, "rst": true,
	"adoc": true, "log": true, "mp3": true, "wav": true, "ogg": true,
	"flac": true,
}

func registerMemoryRetain(parent *SubCmd) {
	f := parent.Flags()
	docID := f.String("doc-id", "", "document id (default: a clock-derived cli_put id)")
	f.StringVar(docID, "d", "", "document id (short)")
	contextText := f.String("context", "", "context for the memory")
	f.StringVar(contextText, "c", "", "context (short)")
	timestamp := f.String("timestamp", "", "ISO 8601 timestamp, or 'unset' to store without one")
	f.StringVar(timestamp, "t", "", "timestamp (short)")
	async := f.Bool("async", false, "queue the write for background processing")
	documentTags := f.String("document-tags", "", "document-level tags (comma list, deprecated)")
	parent.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 2 {
			return NewUsageError("memory retain requires a bank id and a content argument")
		}
		return memoryRetain(ctx, d, retainOptions{
			bankID:       d.Args[0],
			content:      d.Args[1],
			docID:        *docID,
			context:      *contextText,
			timestamp:    *timestamp,
			timestampSet: flagWasProvided(d.Flags, "timestamp") || flagWasProvided(d.Flags, "t"),
			async:        *async,
			documentTags: commaList(*documentTags),
		})
	})
}

type retainOptions struct {
	bankID       string
	content      string
	docID        string
	context      string
	timestamp    string
	timestampSet bool
	async        bool
	documentTags []string
}

// retainRequestBody pins the wire shape. The single item is a map because an
// explicit "" timestamp must travel as "" while an absent one must be absent.
type retainRequestBody struct {
	Items        []map[string]any `json:"items"`
	Async        bool             `json:"async"`
	DocumentTags []string         `json:"document_tags,omitempty"`
}

// memoryRetain posts one memory item. A missing --doc-id derives
// cli_put_<YYYYMMDD>_<HHMMSS> from the local clock.
func memoryRetain(ctx context.Context, d *Deps, o retainOptions) error {
	docID := o.docID
	if docID == "" {
		docID = time.Now().Format("cli_put_20060102_150405")
	}
	item := map[string]any{
		"content":          o.content,
		"document_id":      docID,
		"resolve_entities": true,
	}
	if o.context != "" {
		item["context"] = o.context
	}
	if o.timestampSet {
		item["timestamp"] = o.timestamp
	}
	body := retainRequestBody{
		Items:        []map[string]any{item},
		Async:        o.async,
		DocumentTags: o.documentTags,
	}
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "POST", bankPath(o.bankID, "/memories"), &body, &raw); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return renderRaw(d, raw)
	}
	var resp struct {
		ItemsCount int  `json:"items_count"`
		Async      bool `json:"async"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	ui := d.UI
	ui.Infof("Memory retained successfully (document: %s)", docID)
	if resp.Async {
		ui.Plain("  Status: queued for background processing")
		ui.Plain("  Items: %d", resp.ItemsCount)
	} else {
		ui.Plain("  Stored count: %d", resp.ItemsCount)
	}
	return nil
}

func registerMemoryRetainFiles(parent *SubCmd) {
	f := parent.Flags()
	recursive := f.Bool("recursive", true, "recurse into subdirectories")
	f.BoolVar(recursive, "r", true, "recurse into subdirectories (short)")
	contextText := f.String("context", "", "context applied to every file")
	f.StringVar(contextText, "c", "", "context (short)")
	async := f.Bool("async", false, "report the operation ids without polling")
	strategy := f.String("strategy", "", "retain strategy for every file")
	f.StringVar(strategy, "s", "", "retain strategy (short)")
	parent.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 2 {
			return NewUsageError("memory retain-files requires a bank id and a path")
		}
		return memoryRetainFiles(ctx, d, retainFilesOptions{
			bankID:    d.Args[0],
			path:      d.Args[1],
			recursive: *recursive,
			context:   *contextText,
			async:     *async,
			strategy:  *strategy,
		})
	})
}

type retainFilesOptions struct {
	bankID    string
	path      string
	recursive bool
	context   string
	async     bool
	strategy  string
}

type retainFilesAsyncOut struct {
	OperationIDs []string `json:"operation_ids"`
}

type retainFilesSyncOut struct {
	Success      bool     `json:"success"`
	FilesCount   int      `json:"files_count"`
	OperationIDs []string `json:"operation_ids"`
}

// memoryRetainFiles imports files in batches of ten. Without --async every
// operation is polled to completion; any failure fails the command after all
// operations were polled.
func memoryRetainFiles(ctx context.Context, d *Deps, o retainFilesOptions) error {
	if _, err := os.Stat(o.path); err != nil {
		return fmt.Errorf("Path does not exist: %s", o.path) //nolint:staticcheck // exact copy text of the CLI contract
	}
	files, err := collectRetainFiles(o.path, o.recursive)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		d.UI.Warningf("No supported files found. Supported formats: pdf, docx, pptx, xlsx, jpg, png, html, txt, md, csv, mp3, wav, and more.")
		return nil
	}
	d.UI.Infof("Found %d file(s) to import", len(files))

	var operationIDs []string
	for start := 0; start < len(files); start += retainFileBatchSize {
		end := start + retainFileBatchSize
		if end > len(files) {
			end = len(files)
		}
		batch := files[start:end]
		ids, err := uploadRetainBatch(ctx, d, o, batch)
		if err != nil {
			return err
		}
		operationIDs = append(operationIDs, ids...)
	}

	if d.Format.IsMachine() {
		if o.async {
			return d.Format.Render(d.UI.Stdout, retainFilesAsyncOut{OperationIDs: operationIDs})
		}
		return d.Format.Render(d.UI.Stdout, retainFilesSyncOut{Success: true, FilesCount: len(files), OperationIDs: operationIDs})
	}
	if o.async {
		ui := d.UI
		ui.Infof("Files queued for processing")
		ui.Plain("  Files: %d", len(files))
		for _, id := range operationIDs {
			ui.Plain("  Operation ID: %s", id)
		}
		return nil
	}
	return awaitRetainOperations(ctx, d, o.bankID, operationIDs, len(files))
}

// collectRetainFiles resolves the upload set: a single file is used directly,
// a directory is walked and filtered by extension.
func collectRetainFiles(path string, recursive bool) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("Path does not exist: %s", path) //nolint:staticcheck // exact copy text of the CLI contract
	}
	if !info.IsDir() {
		return []string{path}, nil
	}
	var files []string
	err = filepath.WalkDir(path, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if !recursive && p != path {
				return fs.SkipDir
			}
			return nil
		}
		ext := strings.TrimPrefix(filepath.Ext(entry.Name()), ".")
		if ext == "" || !retainFileExtensions[strings.ToLower(ext)] {
			return nil
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// uploadRetainBatch posts one multipart request. The `request` field carries
// the per-file metadata; each file travels as an octet-stream part.
func uploadRetainBatch(ctx context.Context, d *Deps, o retainFilesOptions, batch []string) ([]string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	metadata := make([]map[string]any, 0, len(batch))
	for _, path := range batch {
		entry := map[string]any{
			"document_id": strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
		}
		if o.context != "" {
			entry["context"] = o.context
		}
		if o.strategy != "" {
			entry["strategy"] = o.strategy
		}
		metadata = append(metadata, entry)
	}
	requestJSON, err := json.Marshal(map[string]any{"files_metadata": metadata})
	if err != nil {
		return nil, err
	}
	if err := mw.WriteField("request", string(requestJSON)); err != nil {
		return nil, err
	}
	for _, path := range batch {
		part, err := mw.CreateFormFile("files", filepath.Base(path))
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if _, err := part.Write(data); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	if d.Verbose {
		_, _ = fmt.Fprintf(d.UI.Stderr, "POST %s\n", joinURL(d.Client.BaseURL, bankPath(o.bankID, "/files/retain")))
	}
	var raw json.RawMessage
	if err := postRaw(ctx, d.Client, bankPath(o.bankID, "/files/retain"), mw.FormDataContentType(), buf.Bytes(), &raw); err != nil {
		return nil, err
	}
	var resp struct {
		OperationIDs []string `json:"operation_ids"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	return resp.OperationIDs, nil
}

// postRaw performs one non-JSON request with the same auth, user agent,
// debug-dump and error shapes the JSON client uses.
func postRaw(ctx context.Context, c *Client, path, contentType string, payload []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL(c.BaseURL, path), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	req.Header.Set("User-Agent", UserAgent)
	c.debugRequest(req, payload)
	resp, err := c.doer().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	c.debugResponse(resp, data)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := &APIError{StatusCode: resp.StatusCode, Status: resp.Status}
		if trimmed := strings.TrimSpace(string(data)); trimmed != "" {
			apiErr.Body = trimmed
		}
		return apiErr
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(data)}
	}
	return nil
}

type operationListEntry struct {
	ID           string  `json:"id"`
	Status       string  `json:"status"`
	ErrorMessage *string `json:"error_message"`
}

type operationListResponse struct {
	Operations []operationListEntry `json:"operations"`
}

// awaitRetainOperations polls the bank operations list until every operation
// settles, then reports success or the aggregated failure.
func awaitRetainOperations(ctx context.Context, d *Deps, bankID string, operationIDs []string, fileCount int) error {
	failures := 0
	for _, id := range operationIDs {
		status, errMsg, err := pollRetainOperation(ctx, d, bankID, id)
		if err != nil {
			return err
		}
		if status == "" {
			continue
		}
		d.UI.Errorf("Retain operation failed: %s", errMsg)
		failures++
	}
	if failures > 0 {
		return fmt.Errorf("%d operation(s) failed", failures)
	}
	d.UI.Infof("Files retained successfully")
	d.UI.Plain("  Files processed: %d", fileCount)
	return nil
}

// pollRetainOperation polls one operation. An empty status means success;
// the other return is the failure message.
func pollRetainOperation(ctx context.Context, d *Deps, bankID, id string) (status, errMsg string, err error) {
	for {
		var raw json.RawMessage
		if err := d.Client.Do(ctx, "GET", bankPath(bankID, "/operations"), nil, &raw); err != nil {
			return "", "", err
		}
		var page operationListResponse
		if err := json.Unmarshal(raw, &page); err != nil {
			return "", "", &DecodeError{Reason: err.Error(), Body: string(raw)}
		}
		entry, found := findOperation(page.Operations, id)
		if !found {
			// An operation that vanished from the list is settled.
			return "", "", nil
		}
		switch entry.Status {
		case "pending", "processing":
			select {
			case <-ctx.Done():
				return "", "", ctx.Err()
			case <-time.After(retainPollInterval):
			}
		case "completed":
			return "", "", nil
		case "failed":
			msg := ""
			if entry.ErrorMessage != nil {
				msg = *entry.ErrorMessage
			}
			return "failed", msg, nil
		case "cancelled":
			return "cancelled", "Operation was cancelled", nil
		default:
			return "unknown", fmt.Sprintf("Unknown status: %s", entry.Status), nil
		}
	}
}

func findOperation(ops []operationListEntry, id string) (operationListEntry, bool) {
	for _, op := range ops {
		if op.ID == id {
			return op, true
		}
	}
	return operationListEntry{}, false
}
