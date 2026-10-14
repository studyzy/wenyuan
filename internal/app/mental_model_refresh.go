package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// mentalModelRefresh performs one queued refresh of a mental model.
//
// It regenerates the model's *content* by running its source query through the
// reflection loop, which is what makes a refresh produce current prose rather
// than a new list of ids. The previous content and the staleness watermark are
// left untouched on any failure, so a retry reads the same window again and the
// model does not silently advance past memories it never saw.
//
// It lives in the composition root because it needs both the storage area
// (recall and reflect) and the provider boundary (the model that writes the
// prose), and neither area may import the other.
type mentalModelRefresh struct {
	store      storage.Store
	provider   provider.Provider
	reflection storage.ReflectRunner
}

// newMentalModelRefresh builds the runner. A nil provider still refreshes: the
// reflection falls back to its deterministic form, so a deployment with no
// model configured keeps refreshing rather than silently ceasing to.
func newMentalModelRefresh(store storage.Store, model provider.Provider, reflection storage.ReflectRunner) mentalModelRefresh {
	return mentalModelRefresh{store: store, provider: model, reflection: reflection}
}

// refresh runs one refresh and returns the updated model.
func (r mentalModelRefresh) refresh(ctx context.Context, bankID, modelID string) (storage.MentalModel, error) {
	model, err := r.store.GetMentalModel(ctx, bankID, modelID)
	if err != nil {
		return storage.MentalModel{}, err
	}
	if strings.TrimSpace(model.SourceQuery) == "" {
		return storage.MentalModel{}, fmt.Errorf("%w: the mental model has no source query to re-run", errRefreshUnavailable)
	}

	// The watermark is captured before the run, not after: it records what this
	// refresh could have seen, so a memory written while the run was in flight
	// is still treated as unseen and triggers the next refresh.
	seenAt := r.store.BankWriteWatermark(ctx, bankID)

	// The refresh reads through the same reflection path the API exposes, so a
	// refreshed note reflects current content and reasoning rather than a
	// snapshot taken when the note was created.
	outcome, err := r.store.Reflect(ctx, storage.ReflectQuery{
		BankID:     bankID,
		Query:      model.SourceQuery,
		Reflection: r.reflection,
	})
	if err != nil {
		// The previous content stands. A refresh that cannot produce something
		// safe to store must not overwrite what is there with nothing.
		return storage.MentalModel{}, fmt.Errorf("%w: %s", errRefreshUnavailable, err)
	}
	if strings.TrimSpace(outcome.Answer) == "" {
		return storage.MentalModel{}, fmt.Errorf("%w: the reflection produced no answer", errRefreshUnavailable)
	}

	observations := map[string]any{"observations": citedObservations(outcome)}
	content := outcome.Answer
	updated, err := r.store.UpdateMentalModel(ctx, bankID, modelID, storage.MentalModelPatch{
		Content:      &content,
		Observations: &observations,
	})
	if err != nil {
		return storage.MentalModel{}, err
	}
	if err := r.store.RecordMentalModelHistory(ctx, bankID, modelID, storage.MentalModelHistoryEntry{
		RefreshedAt: time.Now().UTC(),
		SourceQuery: model.SourceQuery,
		Content:     content,
		Reason:      "refresh",
	}); err != nil {
		return storage.MentalModel{}, err
	}
	// Stamping the watermark only after a successful write is what makes a
	// failed refresh retryable: the model still looks stale, so the next tick
	// tries again instead of waiting for new memories.
	if err := r.store.MarkMentalModelSeen(ctx, bankID, modelID, seenAt); err != nil {
		return storage.MentalModel{}, err
	}
	return updated, nil
}

// citedObservations renders the evidence a refresh rested on, so the model
// records what it was built from and not only what it says.
func citedObservations(outcome storage.ReflectOutcome) []any {
	observations := make([]any, 0, len(outcome.Facts))
	for _, fact := range outcome.Facts {
		observations = append(observations, map[string]any{
			"id":   fact.ID,
			"text": fact.Text,
			"type": fact.Type,
		})
	}
	return observations
}

// errRefreshUnavailable marks a refresh that could not produce a document safe
// to store. It is distinct from a transport failure so the worker can record
// the reason on the model without retrying forever on a permanent refusal.
var errRefreshUnavailable = errors.New("mental model refresh unavailable")

// permanentRefreshFailure reports whether a refresh failure must not be retried.
//
// A model with no source query, or a reflection that produced no answer, will
// behave the same way on the next attempt. Retrying only spends the attempt
// budget and, for an automatic refresh, parks the model later than it needed
// to be parked.
func permanentRefreshFailure(err error) bool {
	return errors.Is(err, errRefreshUnavailable)
}
