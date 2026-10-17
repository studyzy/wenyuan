package transfer

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// retiredDetail explains where a caller should go instead, naming the endpoints
// and the fields a client needs. The bank id is interpolated so the URLs are
// directly usable, exactly as the reference words it.
func retiredDetail(bankID string) string {
	return "Synchronous document export has been removed because it could take down the shared API on " +
		"large banks. Submit an async export via POST /v1/default/banks/" + bankID + "/document-transfer/export, " +
		"poll GET /v1/default/banks/" + bankID + "/operations/{operation_id}, then download the archive from " +
		"the download_url in the operation's result_metadata."
}

// ServeRetiredDocumentTransfer answers the removed synchronous document
// transfer read. It performs no export work and always reports the terminal
// gone status.
func (h *Handlers) ServeRetiredDocumentTransfer(w http.ResponseWriter, r *http.Request) {
	httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindGone, retiredDetail(bankIDFromRequest(r))))
}
