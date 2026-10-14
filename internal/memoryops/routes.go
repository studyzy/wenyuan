package memoryops

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// FileRetainSuffix is the bank-scoped file-write route suffix.
const FileRetainSuffix = "/files/retain"

// BindRoutes registers the memory operation routes on the shared registrar.
func (h *Handlers) BindRoutes(handle func(method, pattern string, handler http.HandlerFunc)) {
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixMemories), h.Retain)
	handle(http.MethodDelete, httpx.BankPath(httpx.SuffixMemories), h.ClearMemories)
	handle(http.MethodPost, httpx.BankPath(FileRetainSuffix), h.FileRetain)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixRecall), h.Recall)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixReflect), h.Reflect)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixOperations), h.ListOperations)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixOperations)+"/{"+OperationIDPathParam+"}", h.GetOperation)
	handle(http.MethodDelete, httpx.BankPath(httpx.SuffixOperations)+"/{"+OperationIDPathParam+"}", h.CancelOperation)
	handle(http.MethodDelete, httpx.BankPath(httpx.SuffixOperationMemberDelete), h.DeleteOperation)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixOperations)+"/{"+OperationIDPathParam+"}/retry", h.RetryOperation)
}
