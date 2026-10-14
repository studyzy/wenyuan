package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// refreshFixture seeds a bank with one memory and one mental model.
func refreshFixture(t *testing.T, sourceQuery string) (*storage.MemoryStore, string) {
	t.Helper()
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	store.SeedMemory("b1", storage.RecallMatch{
		ID: "m1", Text: "deployments happen on tuesdays", Type: "world",
	})
	model, err := store.PutMentalModel(ctx, storage.MentalModel{
		ID: "model-1", BankID: "b1", Subtype: storage.SubtypePinned,
		Name: "deploy days", SourceQuery: sourceQuery,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, model.ID
}

// A refresh rewrites the model's content from the reflection's answer and
// stamps the watermark it observed, so the model stops looking stale.
func TestRefreshWritesContentAndWatermark(t *testing.T) {
	store, modelID := refreshFixture(t, "deployment")
	runner := newMentalModelRefresh(store, nil, nil)

	updated, err := runner.refresh(context.Background(), "b1", modelID)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if updated.Content == "" {
		t.Error("the refresh must write content, since that is what it exists to regenerate")
	}
	stored, err := store.GetMentalModel(context.Background(), "b1", modelID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastRefreshedAt.IsZero() {
		t.Error("a successful refresh must stamp the refresh time")
	}
	if stored.LastRefreshFailedAt != nil {
		t.Error("a successful refresh must clear any park")
	}
}

// A model with no source query has nothing to re-run, so the refresh refuses
// rather than inventing content or wiping what is there.
func TestRefreshWithoutSourceQueryIsRefused(t *testing.T) {
	store, modelID := refreshFixture(t, "   ")
	runner := newMentalModelRefresh(store, nil, nil)

	_, err := runner.refresh(context.Background(), "b1", modelID)
	if err == nil {
		t.Fatal("a model with no source query must refuse")
	}
	if !permanentRefreshFailure(err) {
		t.Errorf("err = %v, must classify as permanent so it is not retried forever", err)
	}
}

// A failed refresh leaves the watermark and the content untouched, so the next
// attempt re-reads the same window rather than skipping past memories it never
// saw.
func TestFailedRefreshDoesNotAdvanceTheWatermark(t *testing.T) {
	store, modelID := refreshFixture(t, "deployment")
	ctx := context.Background()
	before, err := store.GetMentalModel(ctx, "b1", modelID)
	if err != nil {
		t.Fatal(err)
	}

	// A reflection that cannot produce an answer stands in for a model failure.
	runner := newMentalModelRefresh(store, nil, failingReflector{})
	if _, err := runner.refresh(ctx, "b1", modelID); err == nil {
		t.Fatal("a reflection that fails must fail the refresh")
	}

	after, err := store.GetMentalModel(ctx, "b1", modelID)
	if err != nil {
		t.Fatal(err)
	}
	if after.LastMemorySeenAt != nil {
		t.Errorf("the watermark advanced to %v despite the failure", after.LastMemorySeenAt)
	}
	if after.Content != before.Content {
		t.Error("a failed refresh must not overwrite the content")
	}
}

// The permanence classification is what decides retry behaviour: a refusal the
// same input would earn again must not consume the attempt budget.
func TestPermanentRefreshFailureClassification(t *testing.T) {
	if permanentRefreshFailure(errors.New("some other failure")) {
		t.Error("an unrelated failure must be retried")
	}
	if !permanentRefreshFailure(errRefreshUnavailable) {
		t.Error("a refusal must be permanent")
	}
	wrapped := errors.New("outer: " + errRefreshUnavailable.Error())
	if permanentRefreshFailure(wrapped) {
		t.Error("a message that merely mentions the reason is not a classified failure")
	}
}

// A failure recorded against a model parks it, and a later success clears it.
func TestRefreshFailureParksAndSuccessClears(t *testing.T) {
	store, modelID := refreshFixture(t, "deployment")
	ctx := context.Background()
	failedAt := time.Now().UTC()

	if err := store.RecordMentalModelRefreshFailure(ctx, "b1", modelID, failedAt); err != nil {
		t.Fatal(err)
	}
	parked, err := store.GetMentalModel(ctx, "b1", modelID)
	if err != nil {
		t.Fatal(err)
	}
	if parked.LastRefreshFailedAt == nil {
		t.Fatal("a recorded failure must park the model")
	}
	if err := store.MarkMentalModelSeen(ctx, "b1", modelID, failedAt); err != nil {
		t.Fatal(err)
	}
	cleared, err := store.GetMentalModel(ctx, "b1", modelID)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.LastRefreshFailedAt != nil {
		t.Error("a successful refresh must clear the park")
	}
}

// A failing reflector reports an error, standing in for a model call that could
// not answer.
type failingReflector struct{}

func (failingReflector) Run(context.Context, storage.AgenticReflectRequest) (storage.ReflectOutcome, error) {
	return storage.ReflectOutcome{}, errors.New("the model could not answer")
}
