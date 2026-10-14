package storage

import "context"

// FactExtraction turns an item's text into the facts a write should store.
//
// It is an interface so the storage area neither imports a model provider nor
// knows how facts are produced: the composition root adapts whatever extractor
// it configured onto this shape, and a deployment with no model leaves it nil,
// which selects the verbatim path the service has always used.
type FactExtraction interface {
	// Extract returns the facts stated by text. An empty result is a
	// legitimate "nothing found", not an error.
	Extract(ctx context.Context, text string) ([]ExtractedFact, error)
}

// ExtractedFact is one fact an extractor produced. Everything but the text is
// optional, because a model may not supply it and an absent value must not
// become a wrong one.
type ExtractedFact struct {
	Text string
	// Entities are the named things the fact mentions. The type is the
	// extractor's label for the name.
	Entities []RetainEntity
	// OccurredStart and OccurredEnd are RFC 3339 timestamps when the fact is
	// tied to a time, or empty when it is not.
	OccurredStart string
	OccurredEnd   string
	// Confidence is the extractor's certainty in the fact, in the unit
	// interval. Zero means it did not report one.
	Confidence float64
	// CausedByIndexes names the earlier facts in the same item that caused
	// this one, by their position in the item's fact list. They are resolved
	// to stored units once the units exist, and become caused_by links.
	CausedByIndexes []int
}

// extractForItems runs the configured extractor for every item, one entry per
// item in item order. A nil entry means the item's text is stored verbatim.
//
// The whole batch fails if any item's extraction fails, because a partially
// extracted write would leave the caller unable to tell which items were
// processed a second way.
func extractForItems(ctx context.Context, req RetainRequest) ([][]ExtractedFact, error) {
	extracted := make([][]ExtractedFact, len(req.Items))
	if req.Extraction == nil {
		return extracted, nil
	}
	for index, item := range req.Items {
		facts, err := extractItemFacts(ctx, req.Extraction, item.Content)
		if err != nil {
			return nil, err
		}
		extracted[index] = facts
	}
	return extracted, nil
}

// extractItemFacts runs one item's extraction and drops facts that carry no
// text, because a fact with nothing to store cannot become a unit.
func extractItemFacts(ctx context.Context, extractor FactExtraction, text string) ([]ExtractedFact, error) {
	facts, err := extractor.Extract(ctx, text)
	if err != nil {
		return nil, err
	}
	usable := make([]ExtractedFact, 0, len(facts))
	for _, fact := range facts {
		if fact.Text != "" {
			usable = append(usable, fact)
		}
	}
	return usable, nil
}
