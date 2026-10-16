package bankapi

import (
	"github.com/studyzy/wenyuan/internal/banktemplate"
	"github.com/studyzy/wenyuan/internal/storage"
)

// WithDefaultBankTemplate installs the server-owned default template as the
// store's freshly-created-bank hook. Provisioning then lives in one place —
// the hook — so every bank created through this handler set, and through every
// other lazy-create path that shares the store, is provisioned exactly once.
//
// A store that does not expose the hook (a test double) is left unchanged.
func (h *Handlers) WithDefaultBankTemplate(template map[string]any) *Handlers {
	if setter, ok := h.Store.(interface {
		SetBankCreateHook(storage.BankCreateHook)
	}); ok {
		setter.SetBankCreateHook(banktemplate.CreateHook(h.Store, template))
	}
	return h
}
