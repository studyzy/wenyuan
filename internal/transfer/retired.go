package transfer

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// retiredDetail explains where a caller should go instead. The synchronous
// document export was removed, so clients must be redirected rather than
// left guessing.
const retiredDetail = "the synchronous document export was removed; " +
	"submit an asynchronous export, poll the operation, then fetch the archive from the download route"

// ServeRetiredDocumentTransfer answers the removed synchronous document
// transfer read. It performs no export work and always reports the terminal
// gone status.
func (h *Handlers) ServeRetiredDocumentTransfer(w http.ResponseWriter, r *http.Request) {
	httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindGone, retiredDetail))
}
