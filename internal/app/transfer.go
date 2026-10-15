package app

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/transfer"
)

// mountTransfer wires the archive transfer route family into the existing
// composition root. The family mounts alongside the bank and memory operation
// areas and reuses the store the rest of the application already holds, so no
// parallel entrypoint or duplicate storage handle is introduced.
func (a *App) mountTransfer(handle func(method, pattern string, handler http.HandlerFunc)) {
	handlers := transfer.NewHandlers(
		a.Store,
		a.Config.EnableDocumentExportAPI,
		a.Config.EnableDocumentImportAPI,
	)

	bindTransferRoutes(handlers, handle)
}

// bindTransferRoutes registers the family's route table. The retired
// synchronous read is bound through the same table so the public URL surface
// is declared in exactly one place.
func bindTransferRoutes(handlers *transfer.Handlers, handle func(method, pattern string, handler http.HandlerFunc)) {
	for _, route := range transfer.Routes() {
		switch route.Pattern {
		case httpx.BankPath(httpx.SuffixImport):
			handle(route.Method, route.Pattern, httpx.QueryParams(handlers.ImportTemplate, "dry_run"))
		case httpx.BankPath(httpx.SuffixExport):
			handle(route.Method, route.Pattern, handlers.ExportTemplate)
		case httpx.BankPath(httpx.SuffixDocumentTransfer):
			if route.Method == http.MethodGet {
				handle(route.Method, route.Pattern, handlers.ServeRetiredDocumentTransfer)
				continue
			}
			handle(route.Method, route.Pattern, httpx.QueryParams(handlers.SubmitDocumentImport, "on_conflict"))
		case httpx.BankPath(httpx.SuffixDocumentTransfer + httpx.SuffixExport):
			handle(route.Method, route.Pattern, httpx.QueryParams(handlers.SubmitDocumentTransferExport,
				"document_id", "include_observations", "include_knowledge_base"))
		case httpx.BankPath(httpx.SuffixTransferExport):
			handle(route.Method, route.Pattern, httpx.QueryParams(handlers.SubmitTransferExport,
				"include_data", "include_bank_config", "include_history", "document_id"))
		case httpx.BankPath(httpx.SuffixTransferImport):
			handle(route.Method, route.Pattern, httpx.QueryParams(handlers.SubmitTransferImport,
				"mode", "target_bank_id", "document_conflict", "include_data", "include_bank_config", "include_history"))
		case httpx.BankPath(httpx.SuffixClone):
			handle(route.Method, route.Pattern, httpx.QueryParams(handlers.SubmitClone,
				"target_bank_id", "include_data", "include_bank_config", "include_history"))
		case httpx.BankPath(httpx.SuffixAttachmentMember):
			handle(route.Method, route.Pattern, handlers.ServeAttachment)
		}
	}

	// The download route is the only member without a bank path segment, so it
	// is matched with an explicit trailing-slash pattern and the key is
	// recovered from the request path.
	handle(http.MethodGet, transfer.DownloadPattern()+"{key...}", handlers.ServeDownload)
}
