package storage

import (
	"context"
	"encoding/json"
	"time"

	"github.com/studyzy/wenyuan/internal/entitylabels"
)

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

// FactExtractionOptions is the per-item context an extractor may use beyond the
// text itself: the bank's retain mission and custom instructions, the strategy
// the item named, and the reference time a relative phrase resolves against.
//
// It is a separate interface rather than more parameters on Extract because an
// extractor that ignores these is still a valid extractor: the storage area asks
// for the richer call only when the request actually carries something.
type FactExtractionOptions struct {
	Mission       string
	Instructions  string
	Strategy      string
	ReferenceTime time.Time
	// Mode is the bank's retain_extraction_mode: concise (the default),
	// verbose, custom, verbatim or chunks. Empty is the concise default.
	Mode string
	// OptionalFactDimensions lets the four descriptive dimensions be omitted in
	// the prompt and schema.
	OptionalFactDimensions bool
	// Vocabulary is the bank's controlled label vocabulary, so an extractor can
	// describe it in the prompt and read a fact's labels. Nil means none.
	Vocabulary *entitylabels.Vocabulary
	// Attachments are the item's inline attachments' decoded bytes. They let
	// the extractor put each image into the prompt where its placeholder stands,
	// so a fact is read off the picture rather than its caption alone.
	Attachments []RetainAttachment
	// ExtractCausalLinks turns causal-relation extraction on or off. A nil
	// value keeps the reference's shipped default (on); storage sets it from
	// the deployment tuning, so an unset key changes nothing.
	ExtractCausalLinks *bool
	// AllowFreeFormEntities lets regular named entities ride alongside the
	// controlled label vocabulary. A nil value keeps the reference's default
	// (on); it only matters when Vocabulary is set.
	AllowFreeFormEntities *bool
	// OutputLanguage forces the generated facts' language, replacing the
	// prompt's preserve-the-source-language rule. Empty leaves that rule, which
	// is what a deployment that configured no language sends.
	OutputLanguage string
}

// ContextualFactExtraction is the optional capability an extractor implements
// when it can use the request's retain configuration.
type ContextualFactExtraction interface {
	ExtractWithOptions(ctx context.Context, text string, opts FactExtractionOptions) ([]ExtractedFact, error)
}

// TokenUsage is the token accounting a dry-run extraction reports.
type TokenUsage struct {
	InputTokens    int
	OutputTokens   int
	TotalTokens    int
	CachedTokens   int
	ThoughtsTokens int
}

// DryRunExtraction is the outcome of a dry-run extraction: the candidate facts
// and the token accounting the model reported for them.
type DryRunExtraction struct {
	Facts []ExtractedFact
	Usage TokenUsage
}

// DryRunFactExtraction is the optional capability an extractor implements when
// it can report the token accounting a dry-run preview shows alongside the
// facts. The preview path asks for it and falls back to a facts-only extraction
// when the configured extractor cannot answer.
type DryRunFactExtraction interface {
	ExtractDryRun(ctx context.Context, text string, opts FactExtractionOptions) (DryRunExtraction, error)
}

// ExtractedFact is one fact an extractor produced. Everything but the text is
// optional, because a model may not supply it and an absent value must not
// become a wrong one.
type ExtractedFact struct {
	Text string
	// Where is the place the fact is tied to, when the extractor named one.
	Where string
	// Kind is the extractor's free-form classification of the fact.
	Kind string
	// Type is the model's perspective: world, assistant or experience. The
	// stored unit type is resolved from it by the retain path; empty means world.
	Type string
	// FromAttachments names the attachments the fact was drawn from, as the
	// extractor reported them: an attachment's position in the item, or its
	// identifier.
	FromAttachments []string
	// Entities are the named things the fact mentions. The type is the
	// extractor's label for the name.
	Entities []RetainEntity
	// Labels are the canonical key:value classifications the extractor selected
	// from the bank's controlled vocabulary. Each becomes an entity on the
	// unit.
	Labels []string
	// LabelTags is the subset of Labels whose group is flagged tag:true. It is
	// derived from Labels and the request vocabulary during extraction, and is
	// carried separately because those labels also join the unit's tags.
	LabelTags []string
	// ObservationScopes is the observation scoping the fact folds under, carried
	// from its item onto the unit so consolidation never merges across the
	// scopes the caller asked to keep apart. It is raw JSON because the request
	// is either a mode name ("per_tag") or a list of tag groups.
	ObservationScopes json.RawMessage
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
	// ChunkIndex is the position of the item chunk the fact was extracted from.
	// Extraction runs one call per chunk, so a fact is anchored to the chunk
	// that produced it rather than to the item's first chunk.
	ChunkIndex int
}

// modelExtractionEnabled reports whether a write asked a model to extract facts.
// A nil extractor (no model configured) and the "chunks" mode both store an
// item's text verbatim; only a configured model turns an empty answer into
// "the model found nothing here", which is what tells the storage layer to
// store no unit rather than fall back to the raw text. The mode is compared
// exactly, as the reference does ("CHUNKS" is an unrecognised mode, not chunks).
func modelExtractionEnabled(req RetainRequest) bool {
	return req.Extraction != nil && req.ExtractionMode != "chunks"
}

// extractForItems runs the configured extractor for every item, one entry per
// item in item order. A nil entry means the item's text is stored verbatim.
//
// failOnExtractionErrors selects what a failed extraction means. When it is
// set, the first item whose extraction failed fails the whole write: the caller
// learns the write did not happen rather than discovering later that it was
// stored a second way. When it is off — the reference's shipped default — the
// failed item yields no facts, so its document text and chunks are still
// stored while it contributes no memory unit, and the write proceeds over the
// items that extracted. That is the reference's own rule: a chunk whose
// extraction failed loses its facts, not the rest of the retain.
func extractForItems(ctx context.Context, req RetainRequest, extractCausalLinks bool) ([][]ExtractedFact, error) {
	extracted := make([][]ExtractedFact, len(req.Items))
	if req.Extraction == nil {
		return extracted, nil
	}
	for index, item := range req.Items {
		facts, err := extractItemFacts(ctx, req.Extraction, item, req, extractCausalLinks)
		if err != nil {
			// A failed extraction fails the write. The reference's
			// HINDSIGHT_API_FAIL_ON_EXTRACTION_ERRORS escalates *batch-API*
			// extraction errors, where a single request cannot be re-asked; this
			// build has no batch path, and the streaming path it does have
			// surfaces the failure here so the operation records it.
			return nil, err
		}
		extracted[index] = facts
	}
	return extracted, nil
}

// extractItemFacts runs one item's extraction and drops facts that carry no
// text, because a fact with nothing to store cannot become a unit.
func extractItemFacts(ctx context.Context, extractor FactExtraction, item RetainItem, req RetainRequest, extractCausalLinks bool) ([]ExtractedFact, error) {
	// Chunks mode stores each segment as its own memory and asks the model
	// nothing, so the extractor is never called: leaving the item's facts empty
	// makes the caller store its segments verbatim, one unit per chunk. The
	// comparison is exact, matching the reference's `== "chunks"`.
	if req.ExtractionMode == "chunks" {
		return nil, nil
	}
	opts := FactExtractionOptions{
		Mission:                req.ExtractionMission,
		Instructions:           req.ExtractionInstructions,
		Strategy:               item.Strategy,
		Mode:                   req.ExtractionMode,
		OptionalFactDimensions: req.OptionalFactDimensions,
		Vocabulary:             req.Vocabulary,
		Attachments:            item.Attachments,
	}
	// The causal-links switch is carried only when it is off: a nil option is
	// the reference's default (on), so a request that configures nothing keeps
	// taking the plain call and only a disabled deployment hands the extractor
	// the richer one.
	if !extractCausalLinks {
		disabled := false
		opts.ExtractCausalLinks = &disabled
	}
	if item.Timestamp != nil {
		opts.ReferenceTime = *item.Timestamp
	} else if !item.HasTimestamp {
		// An item that named no timestamp is dated to the write clock, and that
		// clock is also the reference the extraction resolves relative phrases
		// against. The reference builds the prompt's event date the same way
		// (utcnow()), so "yesterday" resolves against the day of the write
		// rather than going unanswered for want of a date. An item that asked
		// for no timestamp keeps none here either, so its content stays
		// timeless exactly as it is stored.
		opts.ReferenceTime = time.Now().UTC()
	}
	// Extraction runs one call per chunk, so every piece of a long document is
	// asked about exactly once and a fact names the piece it came from. A chunk
	// that produces nothing simply contributes no facts.
	//
	// The item's content blocks are cut separately and numbered from 0, the way
	// the reference cuts and numbers each content dict (orchestrator.py:1968-1987,
	// fact_extraction.py:2985), and blocks after the first continue the numbering
	// the way the chunk rows do (see RetainItem.Parts), so a fact's ChunkIndex
	// still names the chunk it came from.
	type piece struct {
		text  string
		index int
	}
	pieces := make([]piece, 0)
	next := 0
	for _, part := range item.ContentParts() {
		for _, segment := range segmentsPerContent(part, segmentOptionsFor(req)) {
			pieces = append(pieces, piece{text: segment, index: next})
			next++
		}
	}
	usable := make([]ExtractedFact, 0)
	for _, chunk := range pieces {
		chunkIndex, segment := chunk.index, chunk.text
		var (
			facts []ExtractedFact
			err   error
		)
		// The richer call is asked for only when the request carries something it
		// would use, so an extractor that ignores configuration is never handed a
		// call it cannot answer.
		if contextual, ok := extractor.(ContextualFactExtraction); ok && !opts.empty() {
			facts, err = contextual.ExtractWithOptions(ctx, segment, opts)
		} else {
			facts, err = extractor.Extract(ctx, segment)
		}
		if err != nil {
			return nil, err
		}
		base := len(usable)
		for _, fact := range facts {
			if fact.Text == "" {
				continue
			}
			// A label whose group is flagged tag:true is projected into the unit's
			// tags as well as its entities. The projection is derived here, where
			// the request's vocabulary is in hand, so both backends see it. A label
			// reaches the fact either through the extractor's labels object or as a
			// key:value entity the extractor named directly; both are offered, so a
			// fact that names the label as an entity is tagged the same way.
			labelSources := fact.Labels
			if len(fact.Entities) > 0 {
				labelSources = make([]string, 0, len(fact.Labels)+len(fact.Entities))
				labelSources = append(labelSources, fact.Labels...)
				for _, entity := range fact.Entities {
					labelSources = append(labelSources, entity.Text)
				}
			}
			fact.LabelTags = req.Vocabulary.TagLabels(labelSources)
			// The item's observation scoping rides with every fact it produced; an
			// extractor that named its own scopes for a fact keeps them.
			if len(fact.ObservationScopes) == 0 {
				fact.ObservationScopes = item.ObservationScopes
			}
			fact.ChunkIndex = chunkIndex
			// The extractor numbers the facts it caused by their position in its
			// own response; shifting them by the facts already collected keeps
			// those indexes addressing the item's fact list as a whole.
			if len(fact.CausedByIndexes) > 0 {
				shifted := make([]int, 0, len(fact.CausedByIndexes))
				for _, caused := range fact.CausedByIndexes {
					shifted = append(shifted, caused+base)
				}
				fact.CausedByIndexes = shifted
			}
			usable = append(usable, fact)
		}
	}
	return usable, nil
}

// empty reports whether the options carry nothing an extractor could use.
func (o FactExtractionOptions) empty() bool {
	return o.Mission == "" && o.Instructions == "" && o.Strategy == "" &&
		o.Mode == "" && !o.OptionalFactDimensions &&
		o.ReferenceTime.IsZero() && o.Vocabulary.Empty() && len(o.Attachments) == 0 &&
		o.ExtractCausalLinks == nil && o.AllowFreeFormEntities == nil && o.OutputLanguage == ""
}
