package reporting

// The knowledge page create core, lifted out of the HTTP handler set so every
// surface that makes a page shares one implementation: the HTTP route, the MCP
// tool, and the seeder the administration plane runs when it creates a
// project's bank.
//
// A page is a tree node plus the mental model that answers it. Building the
// pair in one place is what keeps the resolved trigger, the budget default and
// the queued first refresh from drifting between the surfaces.

import (
	"context"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/mentalmodels"
	"github.com/studyzy/wenyuan/internal/storage"
)

// CreateKnowledgePageParams are the page options a create supplies: the question
// the page answers, the tags scoping it, its content budget and the trigger the
// caller stated. Every member is the zero value when the caller named none.
type CreateKnowledgePageParams struct {
	SourceQuery string
	Tags        []string
	MaxTokens   int
	Trigger     map[string]any
}

// CreateKnowledgePageResult is what a create did: the stored tree node, the id
// of the mental model backing it, and the refresh operation queued for it.
type CreateKnowledgePageResult struct {
	Node        storage.KnowledgeNode
	ModelID     string
	OperationID string
}

// PageBuilder stores a knowledge page and its backing mental model and queues
// the page's first refresh.
//
// It carries only the three things a create needs. The bank-config layers a
// trigger resolves through are `Store.BankConfig`, so the same builder answers
// the same trigger for a route, a tool and a seeder.
type PageBuilder struct {
	Store storage.Store
	// Embeddings, when non-nil, embeds the page's searchable text so a
	// knowledge search can fuse a vector arm with the text arm. A nil
	// generator leaves the page without a vector, which is what a deployment
	// with no embedding model gets.
	Embeddings storage.EmbeddingGenerator
	// ProcessDefaults is the process-wide layer of
	// `knowledge_page_default_trigger`.
	ProcessDefaults map[string]any
}

// BuildPage stores a page and its backing mental model and queues the page's
// first refresh.
//
// A caller that already knows which tenant it acts for passes its overrides
// directly; the HTTP route reads them off the request, the MCP tools and the
// seeder pass none.
func (b *PageBuilder) BuildPage(ctx context.Context, tenantOverrides map[string]any, bankID string, parentID *string, name string, params CreateKnowledgePageParams) (CreateKnowledgePageResult, *httpx.HTTPError) {
	trigger, failure := b.knowledgePageTrigger(ctx, bankID, tenantOverrides, params.Trigger)
	if failure != nil {
		// A trigger the reference's model refuses is a bad request on every
		// surface; the message is the item's own sentence.
		return CreateKnowledgePageResult{}, httpx.NewHTTPError(httpx.KindUnprocessable, failure.Error())
	}
	modelID := knowledgeMentalModelID()
	if _, err := b.Store.PutMentalModel(ctx, storage.MentalModel{
		ID: modelID, BankID: bankID, Subtype: storage.SubtypePinned,
		Name: name, SourceQuery: params.SourceQuery, Content: "",
		Tags: params.Tags, MaxTokens: knowledgePageMaxTokens(params.MaxTokens), Trigger: trigger,
		Embedding: b.pageEmbedding(ctx, name+" "),
	}); err != nil {
		return CreateKnowledgePageResult{}, asHTTPError(err)
	}
	node, err := b.Store.PutKnowledgeNode(ctx, storage.KnowledgeNode{
		ID: knowledgePageID(), BankID: bankID, ParentID: derefString(parentID), Kind: knowledgeKindPage,
		Title: name, MentalModelID: modelID, Tags: params.Tags,
	})
	if err != nil {
		// The model and the node share one lifecycle, so a refused node write
		// takes the model back rather than leaving a page nobody can see.
		_ = b.Store.DeleteMentalModel(ctx, bankID, modelID)
		return CreateKnowledgePageResult{}, asHTTPError(err)
	}
	operationID, err := b.enqueuePageRefresh(ctx, bankID, modelID)
	if err != nil {
		return CreateKnowledgePageResult{}, asHTTPError(err)
	}
	return CreateKnowledgePageResult{Node: node, ModelID: modelID, OperationID: operationID}, nil
}

// knowledgePageMaxTokens falls back to the reference's page default when the
// create names none.
func knowledgePageMaxTokens(requested int) int {
	if requested > 0 {
		return requested
	}
	return knowledgePageDefaultMaxTokens
}

// knowledgePageTrigger resolves the refresh policy a new page is created with:
// the built-in page default, overridden by the process, tenant and bank layers
// of `knowledge_page_default_trigger`, overridden by whatever the request named.
func (b *PageBuilder) knowledgePageTrigger(ctx context.Context, bankID string, tenantOverrides map[string]any, supplied map[string]any) (map[string]any, *httpx.ValidationFailure) {
	if failure := mentalmodels.ValidateTriggerObject(supplied); failure != nil {
		return nil, failure
	}
	// The layers, deepest first: the engine's built-in page default, the process
	// default, the caller's tenant override, the bank's own override, and
	// finally what the request named — the reference's own order.
	base := mentalmodels.KnowledgePageDefaultTrigger
	base = overlayTrigger(base, b.ProcessDefaults[bankConfigKnowledgePageTrigger])
	base = overlayTrigger(base, tenantOverrides[bankConfigKnowledgePageTrigger])
	if config, err := b.Store.BankConfig(ctx, bankID); err == nil {
		base = overlayTrigger(base, config[bankConfigKnowledgePageTrigger])
	}
	return mentalmodels.MergeTrigger(supplied, base), nil
}

// overlayTrigger layers one `knowledge_page_default_trigger` value over a base.
// A layer that names nothing, or that is not an object, leaves the base alone:
// the value is validated on write, so a stored shape this layer cannot read is
// the bank's business rather than a reason to fail the create.
func overlayTrigger(base map[string]any, raw any) map[string]any {
	trigger, ok := raw.(map[string]any)
	if !ok || trigger == nil {
		return base
	}
	return mentalmodels.MergeTrigger(trigger, base)
}

// pageEmbedding embeds a page's searchable text — its name and content, the
// same text its BM25 index covers — so a knowledge search can fuse a vector arm
// with the text arm. A deployment with no embedding model stores no vector, and
// the search falls back to the text arm.
func (b *PageBuilder) pageEmbedding(ctx context.Context, text string) []float32 {
	if b.Embeddings == nil {
		return nil
	}
	vectors, err := b.Embeddings.EmbedPassages(ctx, []string{text})
	if err != nil || len(vectors) == 0 {
		return nil
	}
	return vectors[0]
}

// enqueuePageRefresh queues a page's first refresh and returns the operation
// identifier.
func (b *PageBuilder) enqueuePageRefresh(ctx context.Context, bankID, modelID string) (string, error) {
	result, err := b.Store.EnqueueOperation(ctx, storage.EnqueueRequest{
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
