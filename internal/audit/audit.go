// Package audit records feature-usage entries for the service's mutating and
// core operations (retain, recall, reflect, bank CRUD, and the rest of the
// audited roster) across the HTTP, MCP and system transports.
//
// The trail is a fire-and-forget side channel: a write that fails is dropped
// rather than failing the operation it describes. Whether an action is recorded
// is decided by a deployment-wide switch, further narrowed by an optional
// action allowlist, and overridable per bank through the bank's
// “audit_log_enabled“ configuration.
package audit

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// The audit action names. They are the reference's decorator arguments, so a
// client that filters the trail by action sees the same vocabulary the
// reference emits.
const (
	ActionRetain                  = "retain"
	ActionFileRetain              = "file_retain"
	ActionRecall                  = "recall"
	ActionReflect                 = "reflect"
	ActionUpdateMemory            = "update_memory"
	ActionClearMemories           = "clear_memories"
	ActionCreateBank              = "create_bank"
	ActionUpdateBank              = "update_bank"
	ActionDeleteBank              = "delete_bank"
	ActionCreateBankAlias         = "create_bank_alias"
	ActionSetBankAliasPrimary     = "set_bank_alias_primary"
	ActionDeleteBankAlias         = "delete_bank_alias"
	ActionUpdateBankConfig        = "update_bank_config"
	ActionResetBankConfig         = "reset_bank_config"
	ActionCreateMentalModel       = "create_mental_model"
	ActionUpdateMentalModel       = "update_mental_model"
	ActionDeleteMentalModel       = "delete_mental_model"
	ActionRefreshMentalModel      = "refresh_mental_model"
	ActionDryRunRefreshMentalMod  = "dry_run_refresh_mental_model"
	ActionClearMentalModel        = "clear_mental_model"
	ActionCreateDirective         = "create_directive"
	ActionUpdateDirective         = "update_directive"
	ActionDeleteDirective         = "delete_directive"
	ActionReprocessDocument       = "reprocess_document"
	ActionUpdateDocument          = "update_document"
	ActionDeleteDocument          = "delete_document"
	ActionCancelOperation         = "cancel_operation"
	ActionRetryOperation          = "retry_operation"
	ActionDeleteOperation         = "delete_operation"
	ActionImportBankTemplate      = "import_bank_template"
	ActionImportDocuments         = "import_documents"
	ActionImportBankTransfer      = "import_bank_transfer"
	ActionCloneBank               = "clone_bank"
	ActionClearObservations       = "clear_observations"
	ActionRecoverConsolidation    = "recover_consolidation"
	ActionClearMemoryObservations = "clear_memory_observations"
	ActionConsolidation           = "consolidation"
	ActionCreateWebhook           = "create_webhook"
	ActionUpdateWebhook           = "update_webhook"
	ActionDeleteWebhook           = "delete_webhook"
	ActionMemoryDefense           = "memory_defense"
	ActionCreateKnowledgeFolder   = "create_knowledge_folder"
	ActionCreateKnowledgePage     = "create_knowledge_page"
	ActionUpdateKnowledgeNode     = "update_knowledge_node"
	ActionDeleteKnowledgeNode     = "delete_knowledge_node"
)

// Actions is the deployment-wide enumeration of auditable actions. The HTTP
// route table, the MCP dispatch and the worker's per-operation execution all
// draw their action names from this list, so an action is either part of the
// roster or it is not recorded at all.
var Actions = []string{
	ActionRetain, ActionFileRetain, ActionRecall, ActionReflect, ActionUpdateMemory,
	ActionClearMemories, ActionCreateBank, ActionUpdateBank, ActionDeleteBank,
	ActionCreateBankAlias, ActionSetBankAliasPrimary, ActionDeleteBankAlias,
	ActionUpdateBankConfig, ActionResetBankConfig,
	ActionCreateMentalModel, ActionUpdateMentalModel, ActionDeleteMentalModel,
	ActionRefreshMentalModel, ActionDryRunRefreshMentalMod, ActionClearMentalModel,
	ActionCreateDirective, ActionUpdateDirective, ActionDeleteDirective,
	ActionReprocessDocument, ActionUpdateDocument, ActionDeleteDocument,
	ActionCancelOperation, ActionRetryOperation, ActionDeleteOperation,
	ActionImportBankTemplate, ActionImportDocuments, ActionImportBankTransfer,
	ActionCloneBank, ActionClearObservations, ActionRecoverConsolidation,
	ActionClearMemoryObservations, ActionConsolidation,
	ActionCreateWebhook, ActionUpdateWebhook, ActionDeleteWebhook, ActionMemoryDefense,
	ActionCreateKnowledgeFolder, ActionCreateKnowledgePage, ActionUpdateKnowledgeNode, ActionDeleteKnowledgeNode,
}

// The transports the roster is recorded under. They mirror the reference's
// AuditEntry.transport vocabulary.
const (
	TransportHTTP   = "http"
	TransportMCP    = "mcp"
	TransportSystem = "system"
)

// Entry is one operation to record. StartedAt and EndedAt bracket the work;
// Request and Response are the payloads as the transport shaped them; Error is
// the failure text when the operation failed, empty on success.
type Entry struct {
	Action    string
	Transport string
	BankID    string
	StartedAt time.Time
	EndedAt   time.Time
	Request   map[string]any
	Response  map[string]any
	Metadata  map[string]any
	Error     string
}

// Logger decides which operations to record and writes them to the store.
type Logger struct {
	store   storage.Store
	enabled bool
	// allowed, when non-nil, narrows recording to those actions. A nil set
	// means every action in the roster is eligible.
	allowed map[string]struct{}

	// BankConfig reads a bank's stored configuration overrides. A nil reader
	// disables per-bank resolution, leaving the deployment switch to decide.
	BankConfig func(ctx context.Context, bankID string) (map[string]any, error)
}

// NewLogger builds an audit logger. enabled is the deployment-wide switch (the
// reference's WENYUAN_API_AUDIT_LOG_ENABLED); allowedActions is the optional
// allowlist (empty means every action); the store is where rows land.
func NewLogger(store storage.Store, enabled bool, allowedActions []string) *Logger {
	logger := &Logger{store: store, enabled: enabled}
	if len(allowedActions) > 0 {
		logger.allowed = make(map[string]struct{}, len(allowedActions))
		for _, action := range allowedActions {
			logger.allowed[action] = struct{}{}
		}
	}
	if store != nil {
		logger.BankConfig = store.BankConfig
	}
	return logger
}

// ActionAllowed reports whether an action may be recorded at all, ignoring the
// deployment switch. The switch is overridable per bank, so a bank may turn
// auditing on even when the deployment default is off; this pre-filter lets a
// caller skip work for actions that can never be audited.
func (l *Logger) ActionAllowed(action string) bool {
	if l == nil {
		return false
	}
	if l.allowed == nil {
		return true
	}
	_, ok := l.allowed[action]
	return ok
}

// ShouldAudit is the full decision: the action allowlist AND the bank's
// resolved switch. The switch is hierarchical (deployment default overridden
// by the bank's audit_log_enabled), so the effective value depends on which
// bank the action targets. With no bank in scope, or no reader wired, the
// deployment default decides.
func (l *Logger) ShouldAudit(ctx context.Context, action, bankID string) bool {
	if l == nil || !l.ActionAllowed(action) {
		return false
	}
	if bankID == "" || l.BankConfig == nil {
		return l.enabled
	}
	switchValue, present, err := l.bankOverride(ctx, bankID)
	if err != nil {
		// A config-resolution failure must not create an audit gap for a bank
		// meant to be audited, so it falls back to the deployment default.
		//
		// A bank that does not exist yet is the ordinary case — the reference
		// audits the very request that creates it — so that note is debug
		// rather than warn, which would otherwise warn on every create.
		level := slog.LevelWarn
		if errors.Is(err, storage.ErrBankNotFound) {
			level = slog.LevelDebug
		}
		slog.Log(ctx, level, "audit config resolution failed", "bank_id", bankID, "error", err)
		return l.enabled
	}
	if !present {
		return l.enabled
	}
	return switchValue
}

// bankOverride reads a bank's audit_log_enabled override. It reports whether
// the bank set the field at all, so an unset bank falls through to the
// deployment default rather than to a zero value.
func (l *Logger) bankOverride(ctx context.Context, bankID string) (bool, bool, error) {
	config, err := l.BankConfig(ctx, bankID)
	if err != nil {
		return false, false, err
	}
	raw, ok := config["audit_log_enabled"]
	if !ok {
		return false, false, nil
	}
	value, ok := asBool(raw)
	if !ok {
		return false, false, nil
	}
	return value, true, nil
}

// Record writes one entry. Errors are logged and swallowed: recording an
// operation must never fail the operation it describes.
func (l *Logger) Record(ctx context.Context, entry Entry) {
	if l == nil || l.store == nil {
		return
	}
	metadata := entry.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	if entry.Error != "" {
		// The trail has no error column, so the failure text rides in the
		// metadata object alongside whatever else the caller recorded. The
		// metadata is copied first so the caller's map is left untouched.
		copied := make(map[string]any, len(metadata)+1)
		for k, v := range metadata {
			copied[k] = v
		}
		copied["error"] = entry.Error
		metadata = copied
	}
	if entry.EndedAt.IsZero() {
		entry.EndedAt = time.Now().UTC()
	}
	if entry.StartedAt.IsZero() {
		entry.StartedAt = entry.EndedAt
	}
	stored := storage.AuditEntry{
		Action:    entry.Action,
		Transport: entry.Transport,
		BankID:    entry.BankID,
		StartedAt: entry.StartedAt,
		EndedAt:   &entry.EndedAt,
		Request:   entry.Request,
		Response:  entry.Response,
		Metadata:  metadata,
	}
	if err := l.store.RecordAuditLog(ctx, stored); err != nil {
		slog.Warn("audit log write failed", "action", entry.Action, "error", err)
	}
}

// asBool coerces a stored configuration value to a bool. A jsonb round-trip
// keeps a real bool, but a value written as a string is accepted too.
func asBool(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		switch typed {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
}
