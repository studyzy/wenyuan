package app

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/audit"
	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// NewAuditLogger builds the deployment's audit logger from configuration. The
// switch defaults off, matching the reference; a bank may override it.
func NewAuditLogger(cfg config.Config, store storage.Store) *audit.Logger {
	return audit.NewLogger(store, cfg.AuditLogEnabled, cfg.AuditLogActions)
}

// auditedRoute maps a matched route to its audit action and whether the request
// body is recorded.
type auditedRoute struct {
	action  string
	capture bool
}

// httpAuditRoutes is the HTTP route table's mirror of the reference's
// @audited decorators: the same action names, keyed by the method and path
// template the router matches.
var httpAuditRoutes = buildHTTPAuditRoutes()

func buildHTTPAuditRoutes() map[string]auditedRoute {
	bankMember := httpx.BankMemberPath
	bank := httpx.BankPath

	routes := map[string]auditedRoute{}
	add := func(method, pattern, action string, capture bool) {
		routes[method+" "+pattern] = auditedRoute{action: action, capture: capture}
	}

	// Memory operations.
	add(http.MethodPost, bank(httpx.SuffixMemories), audit.ActionRetain, true)
	add(http.MethodDelete, bank(httpx.SuffixMemories), audit.ActionClearMemories, false)
	add(http.MethodPost, bank(httpx.SuffixFileRetain), audit.ActionFileRetain, false)
	add(http.MethodPost, bank(httpx.SuffixRecall), audit.ActionRecall, true)
	add(http.MethodPost, bank(httpx.SuffixReflect), audit.ActionReflect, true)

	// Async operations.
	add(http.MethodDelete, bank(httpx.SuffixOperations)+"/{operation_id}", audit.ActionCancelOperation, false)
	add(http.MethodDelete, bank(httpx.SuffixOperationMemberDelete), audit.ActionDeleteOperation, false)
	add(http.MethodPost, bank(httpx.SuffixOperations)+"/{operation_id}/retry", audit.ActionRetryOperation, false)

	// Bank lifecycle.
	add(http.MethodPut, bankMember(), audit.ActionCreateBank, true)
	add(http.MethodPatch, bankMember(), audit.ActionUpdateBank, true)
	add(http.MethodDelete, bankMember(), audit.ActionDeleteBank, false)
	add(http.MethodPost, bank(httpx.SuffixAliases), audit.ActionCreateBankAlias, true)
	add(http.MethodPatch, bank(httpx.SuffixAliasMember), audit.ActionSetBankAliasPrimary, true)
	add(http.MethodDelete, bank(httpx.SuffixAliasMember), audit.ActionDeleteBankAlias, false)
	add(http.MethodPatch, bank(httpx.SuffixConfig), audit.ActionUpdateBankConfig, true)
	add(http.MethodDelete, bank(httpx.SuffixConfig), audit.ActionResetBankConfig, false)

	// Mental models.
	add(http.MethodPost, bank(httpx.SuffixMentalModels), audit.ActionCreateMentalModel, true)
	add(http.MethodPatch, bank(httpx.SuffixMentalModelMember), audit.ActionUpdateMentalModel, true)
	add(http.MethodDelete, bank(httpx.SuffixMentalModelMember), audit.ActionDeleteMentalModel, false)
	add(http.MethodPost, bank(httpx.SuffixMentalModelRefresh), audit.ActionRefreshMentalModel, false)
	add(http.MethodPost, bank(httpx.SuffixMentalModelDryRun), audit.ActionDryRunRefreshMentalMod, false)
	add(http.MethodPost, bank(httpx.SuffixMentalModelClear), audit.ActionClearMentalModel, false)

	// Directives.
	add(http.MethodPost, bank(httpx.SuffixDirectives), audit.ActionCreateDirective, true)
	add(http.MethodPatch, bank(httpx.SuffixDirectiveMember), audit.ActionUpdateDirective, true)
	add(http.MethodDelete, bank(httpx.SuffixDirectiveMember), audit.ActionDeleteDirective, false)

	// Documents. The member subtree is registered as one pattern per method.
	add(http.MethodPatch, bank(httpx.SuffixDocumentMember), audit.ActionUpdateDocument, true)
	add(http.MethodDelete, bank(httpx.SuffixDocumentMember), audit.ActionDeleteDocument, false)
	add(http.MethodPost, bank(httpx.SuffixDocumentMember), audit.ActionReprocessDocument, false)

	// Observations and consolidation.
	add(http.MethodDelete, bank(httpx.SuffixObservations), audit.ActionClearObservations, false)
	add(http.MethodDelete, bank(httpx.SuffixMemoryObservations), audit.ActionClearMemoryObservations, false)
	add(http.MethodPost, bank(httpx.SuffixConsolidateRecover), audit.ActionRecoverConsolidation, false)
	add(http.MethodPost, bank(httpx.SuffixConsolidate), audit.ActionConsolidation, true)

	// Webhooks.
	add(http.MethodPost, bank(httpx.SuffixWebhooks), audit.ActionCreateWebhook, true)
	add(http.MethodPatch, bank(httpx.SuffixWebhookMember), audit.ActionUpdateWebhook, true)
	add(http.MethodDelete, bank(httpx.SuffixWebhookMember), audit.ActionDeleteWebhook, false)

	// Archive transfer.
	add(http.MethodPost, bank(httpx.SuffixImport), audit.ActionImportBankTemplate, false)
	add(http.MethodPost, bank(httpx.SuffixDocumentTransfer), audit.ActionImportDocuments, false)
	add(http.MethodPost, bank(httpx.SuffixTransferImport), audit.ActionImportBankTransfer, false)
	add(http.MethodPost, bank(httpx.SuffixClone), audit.ActionCloneBank, false)

	return routes
}

// httpAuditAction resolves a matched route to its audit action.
func httpAuditAction(method, pattern string) (string, bool, bool) {
	route, ok := httpAuditRoutes[method+" "+pattern]
	if !ok {
		return "", false, false
	}
	return route.action, route.capture, true
}
