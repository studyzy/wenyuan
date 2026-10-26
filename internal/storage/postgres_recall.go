package storage

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"
)

// keywordArmLimit bounds how many lexical neighbours one arm considers.
const keywordArmLimit = 400

// RecallQueryTokenLimit reports the process-level cap on a recall query's
// length in tokens, so the HTTP ingress can bound the query before it reaches
// the embedder. A non-positive value means the cap is off.
func (s *PostgresStore) RecallQueryTokenLimit() int { return s.tuning.MaxQueryTokens }

// Recall runs recall against the database-backed store with the same multi-arm
// pipeline the in-process store uses: a semantic arm, a BM25 keyword arm and a
// graph-expansion arm, fused by reciprocal rank fusion and then scored. It
// threads the caller's tag scoping, temporal window, strategy boosts, score
// floors and reranker through the shared scoring path.
func (s *PostgresStore) Recall(ctx context.Context, q RecallQuery) (RecallResult, error) {
	// Recalls are bounded at the engine level, separately from admission
	// control: more than the configured number in flight wait for a permit
	// rather than all running at once.
	release, ok := s.acquireRecall(ctx)
	if !ok {
		return RecallResult{}, ctx.Err()
	}
	defer release()
	// The diagnostic accounting measures this recall's phases unless the
	// process turned it off or the sampling rate passed this recall over.
	phases := s.beginRecallPhases(q.BankID)
	defer phases.finish()
	stop := phases.measure("bank_lookup", false)
	if _, err := s.GetBank(ctx, q.BankID); err != nil {
		stop()
		return RecallResult{}, err
	}
	stop()
	q = s.tuning.withDefaultFloors(q)
	// The query is bounded at ingress, before any arm reads it: a degenerate
	// query would otherwise fan out into an unbounded OR tsquery. Truncation,
	// not rejection, so an over-long query still answers.
	q.Query = TruncateQueryToTokens(q.Query, s.tuning.MaxQueryTokens)
	types := resolvedTypes(q.Types)
	arms := activeArms(q)
	term := strings.TrimSpace(q.Query)
	limit := q.Limit
	if limit <= 0 {
		limit = s.tuning.BudgetLimitForQuery(q)
	}
	// Fuzzy resolution folds a request's tags against the bank's own vocabulary,
	// so an over-large vocabulary (or a request whose fuzzy leaves multiply past
	// the cap) is refused before any retrieval runs. Only a request that
	// resolves fuzzily pays for the vocabulary read.
	if usesFuzzyTagResolution(q.TagGroups) {
		stop = phases.measure("fuzzy_tags", false)
		vocabulary, err := s.tagVocabulary(ctx, q.BankID)
		if err != nil {
			stop()
			return RecallResult{}, err
		}
		resolved, err := ResolveRecallTagScope(q, vocabulary)
		stop()
		if err != nil {
			return RecallResult{}, err
		}
		q = resolved
	}

	candidates := map[string]*recallCandidate{}
	armTraces := make([]recallArmTrace, 0, 3)
	// The arms are one phase as a whole, and each arm's own time is a subset of
	// it. Each arm measures itself, and the arms of one wave overlap: the
	// per-arm timings are subsets of the fan-out rather than terms that add up
	// to it, which is how the reference reports them too.
	stop = phases.measure("parallel_retrieval", false)
	fanOutStop := stop
	armFanOut := newRecallArmFanOut(s.tuning.ConnectionBudget)
	// The dense and lexical arms read the bank independently, so they share one
	// wave: the recall then pays the slower of the two rather than their sum,
	// with the budget bounding how many of them hold a connection at once.
	denseAndLexical := []recallArmJob{
		{name: ArmSemantic, run: func(ctx context.Context) ([]recallHit, error) {
			return s.semanticArm(ctx, q, types, term)
		}},
		{name: ArmKeyword, run: func(ctx context.Context) ([]recallHit, error) {
			return s.keywordArm(ctx, q, term)
		}},
	}
	armOutcomes, err := runRecallArms(ctx, armFanOut, phases, denseAndLexical)
	if err != nil {
		fanOutStop()
		return RecallResult{}, err
	}
	semanticHits := prepareArm(q, ArmSemantic, armOutcomes[0].hits)
	recordArm(candidates, ArmSemantic, semanticHits)
	armTraces = append(armTraces, recallArmTrace{arm: ArmSemantic, hits: semanticHits})
	keywordHits := prepareArm(q, ArmKeyword, armOutcomes[1].hits)
	recordArm(candidates, ArmKeyword, keywordHits)
	armTraces = append(armTraces, recallArmTrace{arm: ArmKeyword, hits: keywordHits})
	// The graph arm walks out from the dense hits alone, so it is its own wave.
	// The lexical arm never seeds it: the reference derives the entry points
	// from the semantic arm's ordered rows (or re-queries the dense index when
	// the caller's own semantic floor sits above the seed floor), so a query
	// whose dense arm found nothing has no seeds and the arm contributes
	// nothing — a lexical hit does not fan the walk out. A query that names no
	// words and carries no vector still recalls — an empty query returns the
	// bank's newest units — and that list arm shares the wave, because a graph
	// arm with no seeds cannot answer it.
	graphEnabled := containsArm(arms, ArmGraph)
	var graphSeeds []string
	if graphEnabled {
		graphSeeds = graphSeedsFrom(armOutcomes[0].hits, s.tuning.GraphSeedMinSimilarity)
	}
	listArmNeeded := len(candidates) == 0 && term == "" && len(q.QueryEmbedding) == 0
	secondWave := make([]recallArmJob, 0, 2)
	if graphEnabled {
		secondWave = append(secondWave, recallArmJob{name: ArmGraph, run: func(ctx context.Context) ([]recallHit, error) {
			return s.graphArm(ctx, q, types, graphSeeds)
		}})
	}
	if listArmNeeded {
		secondWave = append(secondWave, recallArmJob{name: "list", run: func(ctx context.Context) ([]recallHit, error) {
			return s.listArm(ctx, q, types, limit)
		}})
	}
	if len(secondWave) > 0 {
		armOutcomes, err = runRecallArms(ctx, armFanOut, phases, secondWave)
		if err != nil {
			fanOutStop()
			return RecallResult{}, err
		}
	}
	waveIndex := 0
	if graphEnabled {
		graphHits := prepareArm(q, ArmGraph, armOutcomes[waveIndex].hits)
		recordArm(candidates, ArmGraph, graphHits)
		armTraces = append(armTraces, recallArmTrace{arm: ArmGraph, hits: graphHits})
		waveIndex++
	}
	if listArmNeeded {
		recordArm(candidates, ArmKeyword, prepareArm(q, ArmKeyword, armOutcomes[waveIndex].hits))
	}
	fanOutStop()

	stop = phases.measure("score", false)
	ranked := scoreCandidates(ctx, candidates, arms, q)
	ranked = orderByFinal(ranked)
	ranked = filterFloors(ranked, q.MinScores)
	// The trace reports the SCORED list, which is what the reference hands its
	// tracer (`scored_results` after the post-rank floors,
	// engine/memory_engine.py:10060-10076); the dedup and the budget below are
	// the narrowing the returned list alone gets.
	scored := append([]*recallCandidate(nil), ranked...)
	ranked = preferObservations(ranked, q)
	ranked = capCandidates(ranked, limit)
	stop()

	result := RecallResult{Matches: make([]RecallMatch, 0, len(ranked))}
	for _, cand := range ranked {
		result.Matches = append(result.Matches, cand.toMatch())
	}
	// The chunk and source-fact channels are assembled before the trace so a
	// caller that asked for source text gets it, capped independently of the
	// fact budget. Attachments are resolved on the ranked matches either way.
	stop = phases.measure("side_channels", false)
	err = s.attachRecallSideChannels(ctx, q, &result)
	stop()
	if err != nil {
		return RecallResult{}, err
	}
	stop = phases.measure("hydrate_results", false)
	s.enrichRecallAttachments(ctx, q.BankID, result.Matches)
	stop()
	// The budgets are applied after the channels are assembled, so a channel is
	// capped by its own budget rather than by the fact budget. A zero budget is
	// unbounded, which is what a caller that names none gets.
	stop = phases.measure("token_filter", false)
	result.Matches = capMatchesToTokens(result.Matches, q.MaxTokens)
	result.ChunkEntries = capChunksToTokens(result.ChunkEntries, q.MaxChunkTokens)
	stop()
	// Entities are resolved after the fact budget has selected the returned
	// matches: the entity channel is a view of those matches, so a fact the
	// budget dropped must not leave its entities behind.
	if q.IncludeEntities {
		stop = phases.measure("entity_build", false)
		err = s.attachRecallEntities(ctx, q, &result)
		stop()
		if err != nil {
			return RecallResult{}, err
		}
	}
	stop = phases.measure("trace", false)
	result.Trace = buildRecallTrace(candidates, scored, ranked, armTraces, q, limit)
	stop()
	attachRecallTracePhases(&result.Trace, phases, q)
	return result, nil
}

// recordArm merges one arm's best-first hits into the candidate set, keeping
// the first rank observed per fact.
func recordArm(candidates map[string]*recallCandidate, arm string, hits []recallHit) {
	if !containsArm(activeFallbackArms, arm) {
		return
	}
	for rank, hit := range hits {
		cand := candidates[hit.match.ID]
		if cand == nil {
			cand = &recallCandidate{
				match:     hit.match,
				armRanks:  map[string]int{},
				armScores: map[string]float64{},
			}
			candidates[hit.match.ID] = cand
		}
		if _, seen := cand.armRanks[arm]; !seen {
			cand.armRanks[arm] = rank + 1
			cand.armScores[arm] = hit.score
		}
	}
}

// activeFallbackArms gates recordArm to known arm names.
var activeFallbackArms = []string{ArmSemantic, ArmKeyword, ArmGraph}

func containsArm(arms []string, target string) bool {
	for _, arm := range arms {
		if arm == target {
			return true
		}
	}
	return false
}

// recallPredicates builds the shared WHERE fragment for the arm queries. The
// parameter positions are fixed and CONTIGUOUS: $1 bank, $2 types.
//
// Tag scoping is rendered as SQL rather than passed as a parameter. The
// positional contract has to be contiguously numbered, because a gap leaves
// PostgreSQL with a parameter whose type it cannot infer; keeping the tag
// filter out of the parameter list is what lets every arm query share one
// argument list (`armArgs`) while the tag tree's shape stays free.
//
// The tag values are the only client-supplied text in the fragment and are
// rendered as escaped SQL literals by sqlTextLiteral.
//
// The temporal window is deliberately NOT part of this fragment. It names the
// temporal arm's own selection, and the reference applies it there — the dense,
// lexical and graph arms are never filtered by it (see
// engine/search/retrieval.py, where the window becomes the temporal arm's
// argument and nothing else). The in-process store reads it the same way: a
// ranking signal through temporalProximity, never a row filter. Rendering it
// here made a query whose text merely mentioned a date ("Can I deploy on
// Friday?") exclude every unit dated outside that day, so all three arms
// returned nothing.
func recallPredicates(q RecallQuery) string {
	return "(" + recallTagPredicate(q) + ")"
}

// tagPredicate renders the request's tag scoping as a SQL condition on a row's
// `tags` column.
//
// A compound group tree takes precedence over a flat tag set, exactly as the
// in-process evaluator does, so the two backends answer the same request the
// same way.
func recallTagPredicate(q RecallQuery) string {
	if len(q.TagGroups) > 0 {
		clauses := make([]string, 0, len(q.TagGroups))
		for _, group := range q.TagGroups {
			clauses = append(clauses, tagGroupClause(group))
		}
		// The top level is an AND across the groups.
		return strings.Join(clauses, " AND ")
	}
	return flatTagClause(q.Tags, q.TagsMatch)
}

// tagGroupClause renders one group node.
func tagGroupClause(group TagGroup) string {
	switch {
	case group.Not != nil:
		// Not matches when its one child does not, which is that child negated.
		return "NOT (" + tagGroupClause(*group.Not) + ")"
	case len(group.And) > 0:
		clauses := make([]string, 0, len(group.And))
		for _, child := range group.And {
			clauses = append(clauses, tagGroupClause(child))
		}
		return "(" + strings.Join(clauses, " AND ") + ")"
	case len(group.Or) > 0:
		clauses := make([]string, 0, len(group.Or))
		for _, child := range group.Or {
			clauses = append(clauses, tagGroupClause(child))
		}
		return "(" + strings.Join(clauses, " OR ") + ")"
	default:
		return tagLeafClause(group)
	}
}

// tagLeafClause renders one leaf as a literal comparison against the requested
// tags. A `resolve:"fuzzy"` leaf never reaches here: the request's fuzzy leaves
// are rewritten into exact ones against the bank's tag vocabulary before recall,
// so the leaf that is rendered always names real tags.
func tagLeafClause(group TagGroup) string {
	requested := group.Tags
	if group.Tag != "" {
		requested = append([]string{group.Tag}, requested...)
	}
	if len(requested) == 0 {
		// A leaf naming no tag still filters: the reference renders an empty leaf
		// under `exact` as "untagged rows only" and under every other mode as "at
		// least one tag" (engine/search/tags.py:390-403). Rendering TRUE would
		// turn the filter off, the one direction a filter must never fail in, so
		// both readings are spelled out. The model layer accepts an empty `tags`
		// array — only an absent one is refused — so this branch is reachable from
		// an ordinary request.
		if leafMatchMode(group.Match) == TagsMatchExact {
			return "(tags IS NULL OR tags = '{}')"
		}
		return "(tags IS NOT NULL AND tags != '{}')"
	}
	// A leaf's own default differs from the flat field's: a compound filter is
	// written to narrow, so an unset mode excludes untagged rows rather than
	// admitting them. This mirrors the in-process evaluator.
	return flatTagClause(requested, leafMatchMode(group.Match))
}

// flatTagClause renders a flat tag-match mode. The base overlap and contains
// modes include untagged rows; the strict variants exclude them; the
// set-equality mode matches only rows whose full tag set equals the request,
// with an empty request selecting the untagged scope.
func flatTagClause(tags []string, mode string) string {
	if len(tags) == 0 && mode != TagsMatchExact {
		return "TRUE"
	}
	literal := sqlTextArrayLiteral(tags)
	switch mode {
	case TagsMatchExact:
		return "(tags::text[] <@ " + literal + " AND tags::text[] @> " + literal + ")"
	case TagsMatchAll:
		return "(cardinality(tags) = 0 OR tags::text[] @> " + literal + ")"
	case TagsMatchAnyStrict:
		// Overlap, excluding untagged rows — the same operator the memories and
		// read-surface predicates use for this mode. Rendering it as containment
		// would require a row to carry *every* requested tag.
		return "tags::text[] && " + literal
	case TagsMatchAllStrict:
		return "tags::text[] @> " + literal
	default: // TagsMatchAny and the empty default: overlap, includes untagged.
		return "(cardinality(tags) = 0 OR tags::text[] && " + literal + ")"
	}
}

// sqlTextArrayLiteral renders a text array literal with its elements escaped.
func sqlTextArrayLiteral(values []string) string {
	if len(values) == 0 {
		return "ARRAY[]::text[]"
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, sqlTextLiteral(value))
	}
	return "ARRAY[" + strings.Join(parts, ",") + "]::text[]"
}

// sqlTextLiteral renders one string as a SQL text literal. It is the only way
// client-supplied text reaches a predicate, so the escaping is not optional.
func sqlTextLiteral(value string) string {
	// A NUL cannot appear in a PostgreSQL text value at all.
	value = strings.ReplaceAll(value, "\x00", "")
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// armArgs returns the shared arm-query arguments in parameter order.
func armArgs(q RecallQuery) []any {
	return []any{q.BankID, resolvedTypes(q.Types)}
}

// semanticArm ranks stored memories by cosine similarity to the query vector.
// It returns nothing when no vector was supplied.
func (s *PostgresStore) semanticArm(ctx context.Context, q RecallQuery, types []string, _ string) ([]recallHit, error) {
	if containsArm(q.DisabledArms, ArmSemantic) || len(q.QueryEmbedding) == 0 {
		return nil, nil
	}
	return s.semanticHits(ctx, q, types, semanticArmLimit)
}

// keywordArm ranks stored memories by BM25 over the maintained search vector.
//
// The native backend has no IDF and ranks every match, so the query text is
// first reduced to its most selective terms and OR-joined into one tsquery (see
// boundedBM25Text), which bounds both the match set and the per-row rank cost.
// A backend with its own query parser — ParadeDB's pg_search — is handed the
// raw text: it tokenizes the query itself, and re-joining it would reintroduce
// the whitespace terms its tokenizer discards.
func (s *PostgresStore) keywordArm(ctx context.Context, q RecallQuery, term string) ([]recallHit, error) {
	if containsArm(q.DisabledArms, ArmKeyword) || term == "" {
		return nil, nil
	}
	bound := term
	if s.textSearchExtension != TextSearchPGSearch {
		bound = s.boundedBM25Text(ctx, term)
		if bound == "" {
			return nil, nil
		}
	}
	return s.keywordHits(ctx, q, bound, keywordArmLimit)
}

// listArm returns the bank's newest units of the requested types, for a query
// with no words and no vector.
func (s *PostgresStore) listArm(ctx context.Context, q RecallQuery, types []string, limit int) ([]recallHit, error) {
	// The score column is part of the shared arm-row shape the scanner reads; a
	// list hit carries no lexical relevance, so its score is 0.
	//
	// The context is NULLIF-ed to an empty string the same way the semantic and
	// keyword arms do it: an observation is written without one, and the
	// reference's fact object is built straight from its column, so an empty
	// string there is only ever the fact type's default showing through.
	query := `SELECT id::text, text, fact_type, occurred_start, occurred_end, mentioned_at, chunk_id, document_id, NULLIF(context, ''), metadata, tags,
	                 COALESCE(source_memory_ids, '{}'::uuid[])::text,
	                 0::float8 AS score
	          FROM ` + quoteIdent(s.schema) + `.memory_units
	          WHERE bank_id = $1 AND fact_type = ANY($2) AND ` + recallPredicates(q) + `
	          ORDER BY mentioned_at DESC NULLS LAST, ctid ASC LIMIT $3`
	args := append(armArgs(q), limit)
	return s.scanArm(ctx, q, query, args)
}

// keywordHits runs the BM25 arm with a server-side statement timeout, so one
// broad query cannot hold the connection for the multi-second rank scan the
// term selection could not prevent.
//
// Rows whose rank ties are ordered by physical position, which is what the
// reference gets: its arm orders by the rank alone and lets PostgreSQL break
// ties, and the plan it uses returns them in the order the rows were written.
// Naming ctid makes that explicit and reproducible, so an arm's reported rank
// is a fact about the pipeline rather than about the plan the planner chose that
// day. A uuid would be the wrong stand-in - it is assigned before the insert and
// says nothing about which fact was written first.
//
// The statement itself comes from bm25ArmQuery, which renders the native
// tsvector form or the configured pg_search form.
func (s *PostgresStore) keywordHits(ctx context.Context, q RecallQuery, term string, limit int) ([]recallHit, error) {
	query, args := s.bm25ArmQuery(q, term, limit)
	return s.queryWithStatementTimeout(ctx, query, args, bm25StatementTimeout)
}

// queryWithStatementTimeout runs one arm statement inside a transaction that
// first sets a local statement timeout. SET LOCAL requires a transaction, and
// the timeout is the server-side backstop the connection pool cannot express
// per statement through database/sql.
func (s *PostgresStore) queryWithStatementTimeout(ctx context.Context, query string, args []any, timeout string) ([]recallHit, error) {
	tx, err := s.reader().BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SET LOCAL statement_timeout = "+sqlTextLiteral(timeout)); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	hits, scanErr := scanRecallHits(rows)
	_ = rows.Close()
	if scanErr != nil {
		return nil, scanErr
	}
	// The rows are fully consumed, so the read-only transaction can commit; a
	// commit failure still leaves the caller with the hits it already read.
	_ = tx.Commit()
	return hits, nil
}

// graphArm expands the candidate set along the three first-class signals from
// the strongest seeds: stored memory links and entity co-occurrence through
// unit_entities (which needs no explicit link). Both arms are folded into one
// roundtrip and summed per unit, so a neighbour reached by more than one signal
// carries the combined weight, matching the in-process walk's additive hop
// weight. The entity arm is weighted tanh(shared_entities * 0.5), the upstream
// saturation curve.
func (s *PostgresStore) graphArm(ctx context.Context, q RecallQuery, types []string, seeds []string) ([]recallHit, error) {
	if containsArm(q.DisabledArms, ArmGraph) {
		return nil, nil
	}
	if len(seeds) == 0 {
		return nil, nil
	}
	// The entity branch takes at most perEntityLimit targets per seed entity
	// before it is weighted, which is the reference's per-entity cap: without it
	// one ubiquitous entity drags in the whole bank.
	query := `WITH seed_entities AS (
	              SELECT DISTINCT ue.entity_id
	              FROM ` + quoteIdent(s.schema) + `.unit_entities ue
	              WHERE ue.unit_id::text = ANY($3::text[])
	          ),
	          entity_targets AS (
	              SELECT se.entity_id, target.unit_id
	              FROM seed_entities se
	              JOIN LATERAL (
	                  SELECT ue_target.unit_id
	                  FROM ` + quoteIdent(s.schema) + `.unit_entities ue_target
	                  WHERE ue_target.entity_id = se.entity_id
	                    AND ue_target.unit_id::text <> ALL($3::text[])
	                  ORDER BY ue_target.unit_id
	                  LIMIT $5
	              ) target ON true
	          ),
	          expanded AS (
	              SELECT l.to_unit_id AS unit_id, l.weight AS contrib
	              FROM ` + quoteIdent(s.schema) + `.memory_links l
	              WHERE l.bank_id = $1 AND l.from_unit_id::text = ANY($3::text[])
	                AND l.to_unit_id::text <> ALL($3::text[])
	              UNION ALL
	              SELECT entity_targets.unit_id,
	                     tanh(COUNT(DISTINCT entity_targets.entity_id)::float8 * 0.5) AS contrib
	              FROM entity_targets
	              GROUP BY entity_targets.unit_id
	          )
	          SELECT u.id::text, u.text, u.fact_type, u.occurred_start, u.occurred_end, u.mentioned_at,
	                 u.chunk_id, u.document_id, u.context, u.metadata, u.tags,
	                 COALESCE(u.source_memory_ids, '{}'::uuid[])::text, SUM(e.contrib) AS weight
	          FROM expanded e
	          JOIN ` + quoteIdent(s.schema) + `.memory_units u ON u.id = e.unit_id
	          WHERE u.fact_type = ANY($2) AND ` + recallPredicates(q) + `
	          GROUP BY u.id, u.text, u.fact_type, u.occurred_start, u.occurred_end, u.mentioned_at, u.chunk_id, u.document_id, u.context, u.metadata, u.tags, u.source_memory_ids
	          ORDER BY weight DESC, u.ctid ASC LIMIT $4`
	args := append(armArgs(q), seeds, graphArmLimit, s.perEntityLimit())
	return s.scanArmWithTimeout(ctx, q, query, args, s.tuning.LinkExpansionTimeout)
}

// perEntityLimit is how many targets one seed entity contributes to the graph
// expansion: the resolved tuning when it was configured, and the reference's
// default when it was not.
func (s *PostgresStore) perEntityLimit() int {
	if s.tuning.LinkExpansionPerEntityLimit > 0 {
		return s.tuning.LinkExpansionPerEntityLimit
	}
	return DefaultLinkExpansionPerEntityLimit
}

// scanArmWithTimeout runs an arm query inside a transaction that first sets a
// local statement timeout, so one pathological expansion cannot hold the
// connection. A zero timeout runs the statement unbounded, which is what a
// deployment that configured none asks for.
func (s *PostgresStore) scanArmWithTimeout(ctx context.Context, q RecallQuery, query string, args []any, timeout time.Duration) ([]recallHit, error) {
	if timeout <= 0 {
		return s.scanArm(ctx, q, query, args)
	}
	hits, err := s.queryWithStatementTimeout(ctx, query, args, formatPostgresMilliseconds(timeout))
	if err != nil {
		return nil, err
	}
	return hits, nil
}

// graphArmLimit bounds the graph arm's fan-out.
const graphArmLimit = 64

// graphSeedsFrom picks the graph arm's entry points: the dense (semantic) hits
// that clear the configured seed floor, strongest first, capped at
// graphSeedLimit.
//
// The reference derives them from the semantic arm's similarity-ordered rows
// (engine/memories/pg/recall.py:299-304 — `similarity is not None and
// similarity >= graph_seed_threshold`, truncated to GRAPH_SEED_LIMIT), and a
// query with no dense hits has no entry points at all. The lexical arm never
// seeds: a keyword-only match is not a graph entry point, or a unit sharing an
// entity with it would be pulled in on evidence the reference does not have.
//
// The floor is the arm's own configured threshold, not the caller's semantic
// floor: when a caller floors the semantic arm above it, the reference re-runs
// the dense query for the arm rather than starving it (link_expansion.py:154-172
// with `preselected_semantic_seeds is None`), so the entry points are the same
// either way. The hits handed in are the arm's raw rows, before the caller's
// floor trimmed them, which is what makes that independent of the floor.
func graphSeedsFrom(hits []recallHit, floor float64) []string {
	ranked := make([]recallHit, 0, len(hits))
	for _, hit := range hits {
		if hit.score >= floor {
			ranked = append(ranked, hit)
		}
	}
	// Strongest similarity first, keeping the arm's own order for ties so the
	// seed set follows the dense ranking rather than a hash order.
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	if len(ranked) > graphSeedLimit {
		ranked = ranked[:graphSeedLimit]
	}
	ids := make([]string, 0, len(ranked))
	for _, seed := range ranked {
		ids = append(ids, seed.match.ID)
	}
	return ids
}

// scanArm runs an arm query and reads its hits.
func (s *PostgresStore) scanArm(ctx context.Context, q RecallQuery, query string, args []any) ([]recallHit, error) {
	rows, err := s.reader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanRecallHits(rows)
}

// scanRecallHits reads the rows of an arm query into hits. It is shared by the
// plain arm path and the statement-timeout path so the two read a row the same
// way. A read error is returned rather than turned into an empty arm: an arm
// whose statement is broken must fail the recall, not look like an empty bank.
func scanRecallHits(rows *sql.Rows) ([]recallHit, error) {
	hits := make([]recallHit, 0)
	for rows.Next() {
		var m RecallMatch
		var occurredStart, occurredEnd, mentionedAt sql.NullTime
		var chunkID, documentID, context sql.NullString
		var metadataRaw, tagsRaw, sourceRaw []byte
		var score float64
		if err := rows.Scan(&m.ID, &m.Text, &m.Type, &occurredStart, &occurredEnd, &mentionedAt,
			&chunkID, &documentID, &context, &metadataRaw, &tagsRaw, &sourceRaw, &score); err != nil {
			return nil, err
		}
		m.OccurredStart = utcTimePtr(nullTimePtr(occurredStart))
		m.OccurredEnd = utcTimePtr(nullTimePtr(occurredEnd))
		m.MentionedAt = utcTimePtr(nullTimePtr(mentionedAt))
		m.ChunkID = chunkID.String
		m.DocumentID = documentID.String
		// A NULL context is the observation's "no context of its own"; an empty
		// string is a fact whose context is genuinely empty. They render
		// differently, so the absence is carried, not folded away.
		m.Context, m.ContextAbsent = context.String, !context.Valid
		m.Metadata = decodeJSONMap(metadataRaw)
		m.Tags = decodePostgresTextArray(tagsRaw)
		m.SourceMemoryIDs = decodePostgresTextArray(sourceRaw)
		hits = append(hits, recallHit{match: m, score: score})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return hits, nil
}

// utcTimePtr rewrites a decoded timestamp into UTC, so a response never renders
// it in the process's local zone. The reference returns every timestamp in UTC;
// a local-zone render also pushes a date at the edge of the calendar (year
// 9999) past the range the wire format can carry.
func utcTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}

// tagVocabulary returns the distinct tags the bank's memories carry, which is
// the vocabulary fuzzy tag resolution folds against.
//
// It is read here rather than derived from the candidate set because the cap is
// about what the fold would have to consider, not about what a particular query
// happened to surface.
func (s *PostgresStore) tagVocabulary(ctx context.Context, bankID string) ([]string, error) {
	query := `SELECT DISTINCT tag
	          FROM ` + quoteIdent(s.schema) + `.memory_units, unnest(tags) AS tag
	          WHERE bank_id = $1`
	rows, err := s.reader().QueryContext(ctx, query, bankID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var vocabulary []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		vocabulary = append(vocabulary, tag)
	}
	return vocabulary, rows.Err()
}
