package storage

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode"
)

// ListKnowledgeNodes returns every knowledge node of one bank, ordered the way
// the reference orders the flat tree: by sort_order then name, so siblings
// appear in display order and the nest needs no second sort.
func (s *MemoryStore) ListKnowledgeNodes(ctx context.Context, bankID string) ([]KnowledgeNode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return nil, s.readErr
	}
	out := make([]KnowledgeNode, 0)
	for _, node := range s.knowledgeNodes {
		if node.BankID == bankID {
			out = append(out, cloneKnowledgeNode(node))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		if out[i].Title != out[j].Title {
			return out[i].Title < out[j].Title
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// GetKnowledgeNode returns one knowledge node by identifier.
func (s *MemoryStore) GetKnowledgeNode(ctx context.Context, bankID, nodeID string) (KnowledgeNode, error) {
	if err := ctx.Err(); err != nil {
		return KnowledgeNode{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	node, ok := s.knowledgeNodes[bankID+"\x00"+nodeID]
	if !ok {
		return KnowledgeNode{}, ErrKnowledgeNodeNotFound
	}
	return cloneKnowledgeNode(node), nil
}

// PutKnowledgeNode inserts or replaces a knowledge node.
func (s *MemoryStore) PutKnowledgeNode(ctx context.Context, node KnowledgeNode) (KnowledgeNode, error) {
	if err := ctx.Err(); err != nil {
		return KnowledgeNode{}, err
	}
	if node.ID == "" || node.BankID == "" {
		return KnowledgeNode{}, ErrInvalidOperationID
	}
	if node.Kind != KnowledgeKindFolder && node.Kind != KnowledgeKindPage {
		return KnowledgeNode{}, ErrInvalidOperationID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return KnowledgeNode{}, s.writeErr
	}
	if s.knowledgeNodes == nil {
		s.knowledgeNodes = map[string]KnowledgeNode{}
	}
	key := node.BankID + "\x00" + node.ID
	now := time.Now().UTC()
	if existing, ok := s.knowledgeNodes[key]; ok {
		node.CreatedAt = existing.CreatedAt
	}
	if node.CreatedAt.IsZero() {
		node.CreatedAt = now
	}
	node.UpdatedAt = now
	s.knowledgeNodes[key] = cloneKnowledgeNode(node)
	return cloneKnowledgeNode(node), nil
}

// applyKnowledgeNodePatch overwrites the members a patch states, so the memory
// and postgres backends apply the same partial update field for field.
func applyKnowledgeNodePatch(node *KnowledgeNode, patch KnowledgeNodePatch) {
	if patch.Title != nil {
		node.Title = *patch.Title
	}
	if patch.Text != nil {
		node.Text = *patch.Text
	}
	if patch.ParentID != nil {
		node.ParentID = *patch.ParentID
	}
	if patch.Tags != nil {
		node.Tags = append([]string(nil), (*patch.Tags)...)
	}
}

// UpdateKnowledgeNode applies a partial update.
func (s *MemoryStore) UpdateKnowledgeNode(ctx context.Context, bankID, nodeID string, patch KnowledgeNodePatch) (KnowledgeNode, error) {
	return s.UpdateKnowledgeNodeWithModel(ctx, bankID, nodeID, patch, nil)
}

// UpdateKnowledgeNodeWithModel applies a node patch and, when one is given, a
// patch to the model behind the node under one lock, so the two rows change as
// a unit. The reference runs the whole patch in one transaction for the same
// reason: a rename must not survive the option update that fails after it, and
// a reader must never see the node renamed while its model still carries the
// old name.
//
// Both rows are built first and stored together, so a model write that refuses
// (a vector the deployment's dimension rejects, say) leaves the node as it was
// rather than half-applied.
func (s *MemoryStore) UpdateKnowledgeNodeWithModel(ctx context.Context, bankID, nodeID string, patch KnowledgeNodePatch, modelPatch *MentalModelPatch) (KnowledgeNode, error) {
	if err := ctx.Err(); err != nil {
		return KnowledgeNode{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return KnowledgeNode{}, s.writeErr
	}
	key := bankID + "\x00" + nodeID
	node, ok := s.knowledgeNodes[key]
	if !ok {
		return KnowledgeNode{}, ErrKnowledgeNodeNotFound
	}
	node = cloneKnowledgeNode(node)
	applyKnowledgeNodePatch(&node, patch)
	node.UpdatedAt = time.Now().UTC()

	// A page's searchable document lives on its backing model, so a rename has to
	// reach the model and rebuild its vector from the new name: without it the
	// stored embedding keeps describing the old title and search matches on text
	// the page no longer has. Mirrors the postgres write, which carries the node's
	// name onto the model in PutKnowledgeNode.
	model, hasModel := s.knowledgePageModelLocked(ctx, node)
	if modelPatch != nil {
		if !hasModel {
			return KnowledgeNode{}, ErrMentalModelNotFound
		}
		applyMentalModelPatch(&model, *modelPatch)
		// The reference's update re-embeds from the new name and the stored
		// content, so a rename or content change moves the page's vector too.
		if generator := s.observationEmbedder; generator != nil {
			if vector := embedMentalModelText(ctx, generator, model.Name, model.Content); len(vector) > 0 {
				if err := ValidateEmbedding(vector); err != nil {
					return KnowledgeNode{}, err
				}
				model.Embedding = vector
			}
		}
		model.LastUpdated = time.Now().UTC()
	}

	s.knowledgeNodes[key] = node
	if hasModel {
		s.mentalModelRecords[mentalModelKey(node.BankID, node.MentalModelID)] = model
	}
	return cloneKnowledgeNode(node), nil
}

// knowledgePageModelLocked returns the model backing a page after a node write,
// with the node's name carried onto it and its vector rebuilt from that name.
// Called with s.mu held. The model is returned rather than stored so a caller
// that also patches the model can commit both rows at once; a folder, or a page
// with no model, comes back with false. The model's content and tags are not
// taken from the node — a read overlays the model onto the node, not the reverse
// — so only the name and its vector change here.
func (s *MemoryStore) knowledgePageModelLocked(ctx context.Context, node KnowledgeNode) (MentalModel, bool) {
	if node.Kind != KnowledgeKindPage || node.MentalModelID == "" {
		return MentalModel{}, false
	}
	stored, ok := s.mentalModelRecords[mentalModelKey(node.BankID, node.MentalModelID)]
	if !ok {
		return MentalModel{}, false
	}
	model := cloneMentalModel(stored)
	if model.Name != node.Title {
		model.Name = node.Title
		model.Embedding = embedMentalModelText(ctx, s.observationEmbedder, model.Name, model.Content)
		model.LastUpdated = time.Now().UTC()
	}
	return model, true
}

// DeleteKnowledgeNode removes a node and its descendants.
func (s *MemoryStore) DeleteKnowledgeNode(ctx context.Context, bankID, nodeID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	key := bankID + "\x00" + nodeID
	if _, ok := s.knowledgeNodes[key]; !ok {
		return ErrKnowledgeNodeNotFound
	}
	// A descendant whose parent is gone would be unreachable through the tree,
	// so it is removed with its ancestor.
	doomed := map[string]bool{nodeID: true}
	for changed := true; changed; {
		changed = false
		for _, node := range s.knowledgeNodes {
			if node.BankID != bankID || doomed[node.ID] {
				continue
			}
			if doomed[node.ParentID] {
				doomed[node.ID] = true
				changed = true
			}
		}
	}
	for id := range doomed {
		delete(s.knowledgeNodes, bankID+"\x00"+id)
	}
	return nil
}

// SearchKnowledgeNodes searches a bank's pages by text.
//
// The memory backend keeps no page embeddings, so it cannot serve the vector
// arm and refuses it rather than faking a similarity: a query embedding is
// accepted and ignored, and every hit is ranked on the text arm alone. That is
// exactly the Postgres text-only path, with the same normalised RRF score, so
// the two backends agree on ordering for a corpus neither has embedded.
//
// The text arm approximates the Postgres ts_rank_cd arm. That arm, with the
// reference's default ranking weights and no normalisation, scales with how
// often the query's terms occur in the document the generated search_vector
// covers — the page name and its content — and not with the document's length.
// This one counts the same occurrences, so the two backends order a corpus the
// same way. The approximation is a plain term count: unlike the reference's
// tsvector it does no stemming and knows no stopwords, so a corpus that relies
// on either will rank differently.
func (s *MemoryStore) SearchKnowledgeNodes(ctx context.Context, query KnowledgeSearchQuery) ([]KnowledgeHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := knowledgeSearchLimit(query.Limit)
	term := strings.TrimSpace(query.Query)
	if term == "" {
		return []KnowledgeHit{}, nil
	}
	terms := knowledgeQueryTerms(term)
	if len(terms) == 0 {
		return []KnowledgeHit{}, nil
	}
	// A bank with the keyword arm switched off has no arm left here: this
	// backend keeps no page embeddings, so there is no vector arm to fall back
	// on and the answer is nothing rather than a ranking off a disabled index.
	if query.DisableTextArm {
		return []KnowledgeHit{}, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	type scored struct {
		node KnowledgeNode
		raw  float64
	}
	matches := make([]scored, 0)
	for _, node := range s.knowledgeNodes {
		if node.BankID != query.BankID || node.Kind != KnowledgeKindPage {
			continue
		}
		// The caller's tag filter is applied here, before ranking, so the top
		// `limit` hits are always the highest-ranked matching pages rather than
		// a short set left by pages that ranked above them.
		if !knowledgeTagsMatch(node.Tags, query.Tags, query.TagsMatch, query.TagGroups) {
			continue
		}
		raw := knowledgeTermFrequency(node, terms)
		if raw <= 0 {
			continue
		}
		matches = append(matches, scored{node: cloneKnowledgeNode(node), raw: raw})
	}
	// Rank by the text score, and break ties by identifier, so the ordering is
	// stable and independent of the underlying map's iteration order.
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].raw != matches[j].raw {
			return matches[i].raw > matches[j].raw
		}
		return matches[i].node.ID < matches[j].node.ID
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	hits := make([]KnowledgeHit, 0, len(matches))
	for rank, match := range matches {
		hits = append(hits, KnowledgeHit{
			Node: match.node,
			// Same single-arm RRF curve the Postgres text-only path reports.
			Score: float64(knowledgeRRFNorm) / float64(knowledgeRRFK+1+rank),
		})
	}
	return hits, nil
}

// knowledgeTagsMatch applies a knowledge search's tag filter to one page's
// tags, the memory-backend counterpart of knowledgeTagClause. A flat tag set
// filters only when it is non-empty or the mode is `exact`; compound groups are
// AND-ed on top, matching the reference's KnowledgeTagFilter.clause.
func knowledgeTagsMatch(present, tags []string, mode string, groups []TagGroup) bool {
	if len(tags) > 0 || mode == TagsMatchExact {
		if !matchFlatTags(present, tags, mode) {
			return false
		}
	}
	if len(groups) > 0 && !matchTagGroups(present, groups) {
		return false
	}
	return true
}

// knowledgeQueryTerms splits a query into the lowercase terms the text arm
// matches on: the in-memory stand-in for the text-search parser. Punctuation is
// dropped so a term matches the way a tsvector would tokenize it.
func knowledgeQueryTerms(query string) []string {
	return strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// knowledgeTermFrequency counts how often the query's terms occur in one page's
// document — the name followed by the body, the same text the generated
// search_vector covers.
func knowledgeTermFrequency(node KnowledgeNode, terms []string) float64 {
	document := strings.FieldsFunc(strings.ToLower(node.Title+" "+node.Text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(document) == 0 {
		return 0
	}
	occurrences := 0
	for _, term := range terms {
		for _, word := range document {
			if word == term {
				occurrences++
			}
		}
	}
	return float64(occurrences)
}

// cloneKnowledgeNode copies the mutable fields.
func cloneKnowledgeNode(node KnowledgeNode) KnowledgeNode {
	out := node
	out.Tags = append([]string(nil), node.Tags...)
	return out
}
