package storage

import "context"

// Reflect runs the database-backed reflection loop. It resolves the bank,
// retrieves candidate facts, then runs the same bounded retrieval-and-synthesis
// loop the in-process store uses, so the observable contract is identical
// across backends. The perspective and directive catalogues are supplied by the
// caller on the query, since those tables are owned by the model unit.
func (s *PostgresStore) Reflect(ctx context.Context, query ReflectQuery) (ReflectOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ReflectOutcome{}, err
	}
	if _, err := s.GetBank(ctx, query.BankID); err != nil {
		return ReflectOutcome{}, err
	}
	result, err := s.Search(ctx, RecallRequest{
		BankID: query.BankID,
		Query:  query.Query,
		Limit:  100,
	})
	if err != nil {
		return ReflectOutcome{}, err
	}
	facts := make([]ReflectCitation, 0, len(result.Matches))
	for _, m := range result.Matches {
		facts = append(facts, ReflectCitation{
			ID:            m.ID,
			Text:          m.Text,
			Type:          m.Type,
			Context:       m.Context,
			OccurredStart: m.OccurredStart,
			OccurredEnd:   m.OccurredEnd,
			MentionedAt:   m.MentionedAt,
			DocumentID:    m.DocumentID,
			ChunkID:       m.ChunkID,
			Metadata:      m.Metadata,
			Tags:          m.Tags,
			Attachments:   m.Attachments,
		})
	}
	loop := newReflectLoop(query, facts)
	return loop.run(ctx)
}
