package banktemplate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"reflect"

	"github.com/studyzy/wenyuan/internal/mentalmodels"
	"github.com/studyzy/wenyuan/internal/storage"
)

// Result is the observable outcome of applying a manifest to a bank.
type Result struct {
	ConfigApplied       bool
	MentalModelsCreated []string
	MentalModelsUpdated []string
	DirectivesCreated   []string
	DirectivesUpdated   []string
	OperationIDs        []string
}

// Apply writes the manifest's configuration and its mental models and
// directives to an existing bank. It creates nothing: the bank must already
// exist, so the caller owns the create/lazy-create decision (the reference
// applies the resources of a default template only to a bank it has just
// provisioned).
func Apply(ctx context.Context, store storage.Store, bankID string, manifest Manifest) (Result, error) {
	result := newResult()
	if len(ConfigValues(manifest.Bank)) > 0 {
		if _, err := store.UpdateBank(ctx, bankID, BankPatch(manifest.Bank)); err != nil {
			return Result{}, err
		}
		result.ConfigApplied = true
	}
	resources, err := ApplyResources(ctx, store, bankID, manifest)
	if err != nil {
		return Result{}, err
	}
	resources.ConfigApplied = result.ConfigApplied
	return resources, nil
}

// ApplyResources writes only the mental models and directives the manifest
// names, matched by id and by name respectively. A definition already stored
// unchanged is skipped, so re-applying the same manifest is a sync rather than
// a rewrite; a changed one is updated and its content regenerated.
func ApplyResources(ctx context.Context, store storage.Store, bankID string, manifest Manifest) (Result, error) {
	result := newResult()

	if len(manifest.MentalModels) > 0 {
		existing, err := store.ListMentalModels(ctx, bankID, "")
		if err != nil {
			return Result{}, err
		}
		byID := make(map[string]storage.MentalModel, len(existing))
		for _, model := range existing {
			byID[model.ID] = model
		}
		for _, model := range manifest.MentalModels {
			maxTokens := model.MaxTokens
			if maxTokens == 0 {
				maxTokens = 2048
			}
			trigger := normalizedTrigger(model.Trigger)
			if current, ok := byID[model.ID]; ok {
				if modelMatchesManifest(current, model, maxTokens) {
					// An unchanged definition is left alone and, crucially, not
					// re-queued: applying the same manifest again is a sync, not
					// a rewrite, and regenerating the model would spend a reflect
					// loop on a document that has not changed.
					continue
				}
				// A resource already on the bank is updated in place; the
				// manifest's id is both the match key and the model's key.
				patch := storage.MentalModelPatch{
					Name:        &model.Name,
					SourceQuery: &model.SourceQuery,
					MaxTokens:   &maxTokens,
					Trigger:     &trigger,
				}
				// An omitted or empty manifest tag list leaves the stored tags
				// alone, exactly as the reference writes `tags=mm.tags if mm.tags
				// else None`: the manifest says nothing about tags, so it must not
				// clear them.
				if len(model.Tags) > 0 {
					patch.Tags = &model.Tags
				}
				if _, err := store.UpdateMentalModel(ctx, bankID, model.ID, patch); err != nil {
					return Result{}, err
				}
				result.MentalModelsUpdated = append(result.MentalModelsUpdated, model.ID)
			} else {
				if _, err := store.PutMentalModel(ctx, storage.MentalModel{
					ID:          model.ID,
					BankID:      bankID,
					Subtype:     storage.SubtypePinned,
					Name:        model.Name,
					SourceQuery: model.SourceQuery,
					Tags:        model.Tags,
					MaxTokens:   maxTokens,
					Trigger:     trigger,
				}); err != nil {
					return Result{}, err
				}
				result.MentalModelsCreated = append(result.MentalModelsCreated, model.ID)
			}
			opID, err := enqueueModelRefresh(ctx, store, bankID, model.ID)
			if err != nil {
				return Result{}, err
			}
			result.OperationIDs = append(result.OperationIDs, opID)
		}
	}

	if len(manifest.Directives) > 0 {
		existing, err := store.ListDirectives(ctx, bankID)
		if err != nil {
			return Result{}, err
		}
		byName := make(map[string]storage.Directive, len(existing))
		for _, directive := range existing {
			byName[directive.Name] = directive
		}
		for _, directive := range manifest.Directives {
			tags := directive.Tags
			if tags == nil {
				tags = []string{}
			}
			if current, ok := byName[directive.Name]; ok {
				if directiveMatchesManifest(current, directive) {
					// Unchanged: nothing to write and nothing to report.
					continue
				}
				content, priority, active := directive.Content, directive.Priority, directive.IsActive
				patch := storage.DirectivePatch{
					Content: &content, Priority: &priority, IsActive: &active,
				}
				// An omitted or empty manifest tag list leaves the stored tags
				// alone, exactly as the reference writes `tags=directive.tags if
				// directive.tags else None`: the manifest says nothing about
				// tags, so it must not clear them.
				if len(directive.Tags) > 0 {
					patch.Tags = &directive.Tags
				}
				if _, err := store.UpdateDirective(ctx, bankID, current.ID, patch); err != nil {
					return Result{}, err
				}
				result.DirectivesUpdated = append(result.DirectivesUpdated, directive.Name)
			} else {
				if _, err := store.PutDirective(ctx, storage.Directive{
					ID:       newTemplateID(),
					BankID:   bankID,
					Name:     directive.Name,
					Content:  directive.Content,
					Priority: directive.Priority,
					IsActive: directive.IsActive,
					Tags:     tags,
				}); err != nil {
					return Result{}, err
				}
				result.DirectivesCreated = append(result.DirectivesCreated, directive.Name)
			}
		}
	}

	return result, nil
}

// CreateHook builds the store's freshly-created-bank hook from a configured
// manifest object: it provisions each new bank with the manifest's config and
// its mental models and directives. A malformed manifest is logged and ignored
// — the reference swallows a bad server-level template rather than wedging bank
// creation for every caller — so the returned hook is nil and nothing is
// applied. An empty manifest also yields a nil hook, leaving creation
// unchanged.
func CreateHook(store storage.Store, raw map[string]any) storage.BankCreateHook {
	if len(raw) == 0 {
		return nil
	}
	manifest, err := Load(raw)
	if err != nil {
		slog.Default().Error(
			"the default bank template failed validation and will be ignored",
			"error", err)
		return nil
	}
	return func(ctx context.Context, bankID string) error {
		_, err := applyUnderExistingConfig(ctx, store, bankID, manifest)
		return err
	}
}

// applyUnderExistingConfig applies the manifest to a bank that has just been
// created, leaving the fields the create request already wrote alone.
//
// The reference applies its default template while the bank is being
// provisioned, so the caller's own fields win where the two overlap. This hook
// runs after the row exists, so those values are already stored: the template's
// fields that the bank already carries are dropped before the patch, which
// reproduces the reference's precedence instead of letting the template
// overwrite the client.
func applyUnderExistingConfig(ctx context.Context, store storage.Store, bankID string, manifest Manifest) (Result, error) {
	values := ConfigValues(manifest.Bank)
	if len(values) == 0 {
		return Apply(ctx, store, bankID, manifest)
	}
	bank, err := store.GetBank(ctx, bankID)
	if err != nil {
		return Result{}, err
	}
	// Every template field — the disposition traits and the reflect mission
	// included — is now an override in the bank's config column, so a field the
	// create request already stored there is the one the template must leave
	// alone. A manifest value that is explicit JSON null never reaches here:
	// ConfigValues dropped it, so it can neither claim a key nor clobber one.
	kept := make(map[string]any, len(values))
	for key, value := range values {
		if _, taken := bank.Config[key]; taken {
			continue
		}
		kept[key] = value
	}
	scoped := manifest
	scoped.Bank = kept
	return Apply(ctx, store, bankID, scoped)
}

// newResult is the zero result with every list a non-nil empty slice, so a
// consumer never reads a null where the response model declares an array.
func newResult() Result {
	return Result{
		MentalModelsCreated: []string{},
		MentalModelsUpdated: []string{},
		DirectivesCreated:   []string{},
		DirectivesUpdated:   []string{},
		OperationIDs:        []string{},
	}
}

// enqueueModelRefresh queues a regeneration of one applied mental model and
// returns the operation identifier the caller polls.
func enqueueModelRefresh(ctx context.Context, store storage.Store, bankID, modelID string) (string, error) {
	result, err := store.EnqueueOperation(ctx, storage.EnqueueRequest{
		Operation: storage.Operation{
			BankID:           bankID,
			OperationType:    storage.OperationTypeRefreshMentalModel,
			Payload:          map[string]any{storage.MentalModelIDPayloadKey: modelID},
			SerializationKey: storage.MentalModelSerializationKey(modelID),
		},
		DedupeByPayloadKey: storage.MentalModelIDPayloadKey,
	})
	if err != nil {
		return "", err
	}
	return result.Operation.OperationID, nil
}

// modelMatchesManifest reports whether a stored mental model already carries
// the definition a manifest names, so an unchanged apply can leave it alone.
//
// Tags are compared as a set, and only when the manifest names any: an omitted
// or empty manifest tag list says nothing about tags, so it can never differ —
// matching the reference's `not mm.tags or set(stored) == set(mm.tags)`.
func modelMatchesManifest(current storage.MentalModel, model MentalModel, maxTokens int) bool {
	if current.Name != model.Name || current.SourceQuery != model.SourceQuery {
		return false
	}
	if current.MaxTokens != maxTokens {
		return false
	}
	if len(model.Tags) > 0 && !sameTagSet(current.Tags, model.Tags) {
		return false
	}
	return sameTrigger(current.Trigger, model.Trigger)
}

// directiveMatchesManifest reports whether a stored directive already carries
// the definition a manifest names. Tags follow the same rule as a mental
// model's: compared as a set, and only when the manifest names any.
func directiveMatchesManifest(current storage.Directive, directive Directive) bool {
	if current.Content != directive.Content ||
		current.Priority != directive.Priority ||
		current.IsActive != directive.IsActive {
		return false
	}
	if len(directive.Tags) > 0 && !sameTagSet(current.Tags, directive.Tags) {
		return false
	}
	return true
}

// sameTrigger compares two trigger maps through the reference's
// MentalModelTrigger model, so each side's unset members are filled with the
// model's defaults and members the model does not declare are dropped. A nil
// trigger reads as the model's own defaults — the reference validates
// `stored["trigger"] or {}` — so an absent trigger matches a manifest that
// names none.
func sameTrigger(a, b map[string]any) bool {
	return reflect.DeepEqual(normalizedTrigger(a), normalizedTrigger(b))
}

// normalizedTrigger projects a trigger through the reference's
// MentalModelTrigger model: every declared member carries the stored value or
// its default, and any member the model does not declare is dropped. A nil
// trigger becomes the model's full default object, which is what the reference
// stores (`mm.trigger.model_dump()`) when a manifest names no trigger.
func normalizedTrigger(raw map[string]any) map[string]any {
	if raw == nil {
		return mentalmodels.NormalizeTrigger(map[string]any{})
	}
	return mentalmodels.NormalizeTrigger(raw)
}

// sameTagSet reports whether two tag lists carry the same tags ignoring order,
// which is how the reference compares them (`set(stored) == set(manifest)`).
func sameTagSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, tag := range a {
		counts[tag]++
	}
	for _, tag := range b {
		counts[tag]--
		if counts[tag] < 0 {
			return false
		}
	}
	return true
}

// newTemplateID builds an opaque identifier for a directive the manifest did
// not name one for.
func newTemplateID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "tmpl-directive"
	}
	return hex.EncodeToString(raw[:])
}
