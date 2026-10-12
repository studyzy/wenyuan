package bankapi

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// BindRoutes registers the bank collection and lifecycle routes on the shared
// registrar, plus the alias sub-routes and the configuration read/write pair.
func (h *Handlers) BindRoutes(handle func(method, pattern string, handler http.HandlerFunc)) {
	collection := httpx.BanksCollectionPath()
	member := httpx.BankMemberPath()

	handle(http.MethodGet, collection, h.ListBanks)
	handle(http.MethodPut, member, h.ReplaceBank)
	handle(http.MethodPatch, member, h.UpdateBank)
	handle(http.MethodDelete, member, h.DeleteBank)

	handle(http.MethodGet, httpx.BankPath(httpx.SuffixAliases), h.ListAliases)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixAliases), h.AddAlias)
	handle(http.MethodPatch, httpx.BankPath(httpx.SuffixAliasMember), h.PromoteAlias)
	handle(http.MethodDelete, httpx.BankPath(httpx.SuffixAliasMember), h.RemoveAlias)

	handle(http.MethodGet, httpx.BankPath(httpx.SuffixConfig), h.GetBankConfig)
	handle(http.MethodPatch, httpx.BankPath(httpx.SuffixConfig), h.UpdateBankConfig)
}

// BindConfigRoutes registers only the bank configuration sub-routes. They are
// kept separate so a composition root can mount them when the configuration
// API feature flag is enabled.
func (h *Handlers) BindConfigRoutes(handle func(method, pattern string, handler http.HandlerFunc)) {
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixConfig), h.GetBankConfig)
	handle(http.MethodPatch, httpx.BankPath(httpx.SuffixConfig), h.UpdateBankConfig)
}
