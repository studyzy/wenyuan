package storage

import "context"

// Reflect runs the database-backed reflection.
//
// A supplied runner takes precedence: it is the model-driven loop, and the store
// is the retrieval surface that loop calls back into, exactly as the in-memory
// store is. Without a runner the deterministic in-process loop runs against the
// store's own recall results, so a deployment with no model is unchanged.
func (s *PostgresStore) Reflect(ctx context.Context, query ReflectQuery) (ReflectOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ReflectOutcome{}, err
	}
	// The model-driven branch resolves the bank lazily: the loop's retrieval
	// does it (recall enforces existence), so probing here would force a
	// database round trip before any model work, and would make the dispatch
	// untestable without a database. The deterministic branch below keeps the
	// up-front bank probe it has always done.
	if query.Reflection != nil {
		directives, err := s.reflectDirectives(ctx, query)
		if err != nil {
			return ReflectOutcome{}, err
		}
		// The profile is read only when the store has a database: the offline
		// dispatch test supplies a runner with no database, and a bank that
		// configured no identity produces the same prompt from an empty profile.
		var bankName, mission string
		var disposition map[string]int
		if s.db != nil {
			bank, err := s.GetBank(ctx, query.BankID)
			if err != nil {
				return ReflectOutcome{}, err
			}
			bankName, mission, disposition = ReflectProfile(bank)
		}
		return query.Reflection.Run(ctx, AgenticReflectRequest{
			BankID:                            query.BankID,
			Question:                          query.Query,
			MaxTokens:                         query.MaxTokens,
			MaxCompletionTokens:               s.tuning.ReflectMaxCompletionTokens,
			MaxIterations:                     resolvedIterations(s.tuning, query),
			Budget:                            query.Budget,
			Retrieval:                         s,
			Directives:                        directives,
			SearchObservationsMaxTokens:       query.SearchObservationsMaxTokens,
			SearchObservationsIncludeEntities: query.SearchObservationsIncludeEntities,
			ResponseSchema:                    query.ResponseSchema,
			BankName:                          bankName,
			Mission:                           mission,
			Disposition:                       disposition,
			AnswerAsDocument:                  query.AnswerAsDocument,
			Tags:                              query.Tags,
			TagsMatch:                         query.TagsMatch,
			TagGroups:                         query.TagGroups,
		})
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
	loop := newReflectLoop(query, facts, s.tuning)
	return loop.run(ctx)
}

// reflectDirectives resolves the directive candidates the agentic loop follows.
//
// The caller usually supplies the bank's directives on the query. When it does
// not — the PostgreSQL deployment has no seeded perspective/directive catalogue
// — they are read from the bank's own table, mirroring how the in-memory store
// defaults them from its seeded catalogue. An empty directive contributes
// nothing, exactly as on the in-memory path.
func (s *PostgresStore) reflectDirectives(ctx context.Context, query ReflectQuery) ([]ReflectDirective, error) {
	directives := query.Directives
	if directives == nil {
		stored, err := s.ListDirectives(ctx, query.BankID)
		if err != nil {
			return nil, err
		}
		directives = make([]ReflectDirective, 0, len(stored))
		for _, directive := range stored {
			directives = append(directives, ReflectDirective{
				ID:      directive.ID,
				Name:    directive.Name,
				Content: directive.Content,
				Tags:    directive.Tags,
				Active:  directive.IsActive,
			})
		}
	}
	return directives, nil
}
