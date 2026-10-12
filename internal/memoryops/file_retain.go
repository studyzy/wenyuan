package memoryops

import (
	"io"
	"mime/multipart"
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// FileRetain serves the file-to-memory write route. The route always processes
// asynchronously, rejects per-file fields placed at the request level, and
// rejects an update mode.
func (h *Handlers) FileRetain(w http.ResponseWriter, r *http.Request) {
	bankID := r.PathValue("bank_id")
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, "request body must be a multipart upload"))
		return
	}
	if err := rejectRequestLevelFileFields(r); err != nil {
		httpx.WriteValidationFor(w, r, err)
		return
	}
	files := r.MultipartForm.File["files"]
	items := make([]RetainItem, 0, len(files))
	for _, header := range files {
		item, err := itemFromUpload(header)
		if err != nil {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindInternal, httpx.DetailInternal))
			return
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		httpx.WriteValidationFor(w, r, &httpx.ValidationFailure{Errors: []httpx.ValidationError{{
			Loc: []string{"body", "files"}, Msg: "at least one file is required",
			Type: "value_error.missing",
		}}})
		return
	}
	req := RetainRequest{Items: items, Async: true}
	// Reject an update mode placed per file, which the file route does not accept.
	if field := updateModeInItems(items); field != "" {
		httpx.WriteValidationFor(w, r, &httpx.ValidationFailure{Errors: []httpx.ValidationError{{
			Loc: []string{"body", "files", "update_mode"}, Msg: "update mode is not accepted on the file route",
			Type: "value_error.mode",
		}}})
		return
	}
	result, err := h.Store.RetainWrite(r.Context(), toStorageRetain(bankID, req))
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, BuildRetainResponse(bankID, req, result))
}

// rejectRequestLevelFileFields rejects per-file fields placed at the request
// level instead of nested per file.
func rejectRequestLevelFileFields(r *http.Request) *httpx.ValidationFailure {
	for _, name := range []string{"content", "document_id", "timestamp", "metadata"} {
		if r.FormValue(name) != "" {
			return &httpx.ValidationFailure{Errors: []httpx.ValidationError{{
				Loc: []string{"body", name}, Msg: "per-file fields must be nested per file",
				Type: "value_error.placement",
			}}}
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

func itemFromUpload(header *multipart.FileHeader) (RetainItem, error) {
	file, err := header.Open()
	if err != nil {
		return RetainItem{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return RetainItem{}, err
	}
	return RetainItem{
		Content:    Content{Text: string(data)},
		DocumentID: header.Filename,
	}, nil
}
