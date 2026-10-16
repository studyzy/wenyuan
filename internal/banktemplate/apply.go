package banktemplate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"reflect"
	"strings"

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
					Tags:        &model.Tags,
					MaxTokens:   &maxTokens,
				}
				if model.Trigger != nil {
					trigger := model.Trigger
					patch.Trigger = &trigger
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
					Trigger:     model.Trigger,
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
				if directiveMatchesManifest(current, directive, tags) {
					// Unchanged: nothing to write and nothing to report.
					continue
				}
				content, priority, active := directive.Content, directive.Priority, directive.IsActive
				if _, err := store.UpdateDirective(ctx, bankID, current.ID, storage.DirectivePatch{
					Content: &content, Priority: &priority, IsActive: &active, Tags: &tags,
				}); err != nil {
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
	kept := make(map[string]any, len(values))
	for key, value := range values {
		if _, taken := bank.Config[key]; taken {
			continue
		}
		// The disposition traits and the reflect mission live on the bank row
		// rather than in its override map, so they are compared there.
		switch key {
		case "disposition_skepticism", "disposition_literalism", "disposition_empathy":
			if _, taken := bank.Disposition[strings.TrimPrefix(key, "disposition_")]; taken {
				continue
			}
		case "reflect_mission":
			if bank.Mission != "" {
				continue
			}
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
func modelMatchesManifest(current storage.MentalModel, model MentalModel, maxTokens int) bool {
	if current.Name != model.Name || current.SourceQuery != model.SourceQuery {
		return false
	}
	if current.MaxTokens != maxTokens {
		return false
	}
	if !equalStringSlices(current.Tags, model.Tags) {
		return false
	}
	return sameTrigger(current.Trigger, model.Trigger)
}

// directiveMatchesManifest reports whether a stored directive already carries
// the definition a manifest names.
func directiveMatchesManifest(current storage.Directive, directive Directive, tags []string) bool {
	return current.Content == directive.Content &&
		current.Priority == directive.Priority &&
		current.IsActive == directive.IsActive &&
		equalStringSlices(current.Tags, tags)
}

// sameTrigger compares two trigger maps treating an absent map as empty, so a
// model stored without a trigger matches a manifest that names none.
func sameTrigger(a, b map[string]any) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

// equalStringSlices compares two string slices treating nil and empty as equal,
// since a stored empty list and an omitted one mean the same thing.
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
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
