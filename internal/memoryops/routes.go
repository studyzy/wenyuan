package memoryops

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// BindRoutes registers the memory operation routes on the shared registrar.
func (h *Handlers) BindRoutes(handle func(method, pattern string, handler http.HandlerFunc)) {
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixMemories), h.Retain)
	handle(http.MethodDelete, httpx.BankPath(httpx.SuffixMemories), httpx.QueryParams(h.ClearMemories, "type"))
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixFileRetain), h.FileRetain)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixRecall), h.Recall)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixReflect), h.Reflect)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixOperations), httpx.QueryParams(h.ListOperations,
		"status", "type", "limit", "offset", "exclude_parents"))
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixOperations)+"/{"+OperationIDPathParam+"}",
		httpx.QueryParams(h.GetOperation, "include_payload", "include"))
	handle(http.MethodDelete, httpx.BankPath(httpx.SuffixOperations)+"/{"+OperationIDPathParam+"}", h.CancelOperation)
	handle(http.MethodDelete, httpx.BankPath(httpx.SuffixOperationMemberDelete), h.DeleteOperation)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixOperations)+"/{"+OperationIDPathParam+"}/retry", h.RetryOperation)
}
