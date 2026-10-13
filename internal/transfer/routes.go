// Package transfer serves the archive export, import, and transfer route
// family. It binds the destination's existing archive pipeline to the public
// HTTP surface rather than reimplementing the archive format.
package transfer

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// Route is one entry of the transfer route table. The table is the single
// place the family's public URL contract is declared.
type Route struct {
	Method  string
	Pattern string
}

// Routes returns the transfer route table in declaration order. The retired
// synchronous document-transfer read is deliberately present: it must answer
// the terminal gone status rather than being absent, because clients that
// still call it must be told where to go instead.
func Routes() []Route {
	return []Route{
		{Method: http.MethodPost, Pattern: httpx.BankPath(httpx.SuffixImport)},
		{Method: http.MethodGet, Pattern: httpx.BankPath(httpx.SuffixExport)},
		{Method: http.MethodGet, Pattern: httpx.BankPath(httpx.SuffixDocumentTransfer)},
		{Method: http.MethodPost, Pattern: httpx.BankPath(httpx.SuffixDocumentTransfer + httpx.SuffixExport)},
		{Method: http.MethodPost, Pattern: httpx.BankPath(httpx.SuffixDocumentTransfer)},
		{Method: http.MethodPost, Pattern: httpx.BankPath(httpx.SuffixTransferExport)},
		{Method: http.MethodPost, Pattern: httpx.BankPath(httpx.SuffixTransferImport)},
		{Method: http.MethodPost, Pattern: httpx.BankPath(httpx.SuffixClone)},
		{Method: http.MethodGet, Pattern: httpx.BankPath(httpx.SuffixAttachmentMember)},
	}
}

// DownloadPattern is the one route in the family without a bank path segment.
// The storage key is a multi-segment wildcard, so it is matched with an
// explicit trailing-slash pattern and the key is recovered by trimming the
// prefix.
func DownloadPattern() string { return httpx.PrefixFilesDownload }
