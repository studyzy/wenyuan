package rerank

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/studyzy/wenyuan/internal/provider"
)

// TypeSafe is not a Cohere-shaped /rerank endpoint: it evaluates typed
// *questions* against a *state*. Ranking is one Choice question whose options
// are the candidates; the cut is a second Score question asked over the ranked
// shortlist when pruning is on.
const (
	typeSafeSystemOnePath = "/v1/systemone"

	// rankInstructionsPrefix and cutInstructions are the two prompt templates.
	// They are part of the provider's contract, not tunable wording.
	rankInstructionsPrefix = "Which candidate answers the question: "
	cutInstructions        = "How far down this ranked list does genuine relevance to the question extend? " +
		"Count a candidate as relevant only if it helps answer the question."

	// optionKeyOverhead estimates the JSON envelope around one choice option
	// ('"c249": "' plus its separator).
	optionKeyOverhead = 7
	// listingItemOverhead estimates the formatting around one cut listing item
	// ("[1] ..." plus its blank-line separator).
	listingItemOverhead = 10
	// maxQueryTokens is a defensive ceiling on the query's token count.
	maxQueryTokens = 2000
)

// cutLevels are the ordered depths the cut question offers, coarsest last.
// There is deliberately no "nothing is relevant" level, so at least one
// candidate always survives.
var cutLevels = []string{
	"Only the first candidate is relevant",
	"The first two are relevant",
	"The first three are relevant",
	"The first five are relevant",
	"The first ten are relevant",
	"All of the listed candidates are relevant",
}

// cutDepths maps a cut level to how many candidates it keeps; 0 means the
// whole shortlist.
var cutDepths = []int{1, 2, 3, 5, 10, 0}

type systemOneQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	// Criteria carries an option map for a choice question and a level list
	// for a score question.
	Criteria any `json:"criteria"`
}

type systemOneRequest struct {
	State     string                       `json:"state"`
	Model     string                       `json:"model"`
	Questions map[string]systemOneQuestion `json:"questions"`
}

type systemOneAnswer struct {
	Type          string             `json:"type"`
	Score         float64            `json:"score"`
	Probabilities map[string]float64 `json:"probabilities"`
}

type systemOneResponse struct {
	Answers map[string]systemOneAnswer `json:"answers"`
}

// typesafeClient ranks candidates with TypeSafe's typed questions.
//
// The scores it returns are positions, not confidences: a choice probability is
// a share of one pool, so two pools are not comparable. Candidates below the
// cut score exactly 0.0.
type typesafeClient struct {
	poster           *poster
	url              string
	apiKey           string
	model            string
	prune            bool
	maxOptions       int
	shortlist        int
	maxQuestionToken int
}

// ScoresByRankPosition reports that this backend's scores are rank positions.
func (c *typesafeClient) ScoresByRankPosition() bool { return true }

// PrunesCandidates reports whether this backend cuts its pool, marking the
// dropped candidates with a score of exactly 0.0.
func (c *typesafeClient) PrunesCandidates() bool { return c.prune }

func (c *typesafeClient) Rerank(ctx context.Context, req provider.RerankRequest) (provider.RerankResult, error) {
	if len(req.Documents) == 0 {
		return provider.RerankResult{}, nil
	}
	docs := append([]string(nil), req.Documents...)
	query := c.boundQuery(req.Query)

	indices := make([]int, len(docs))
	for i := range indices {
		indices[i] = i
	}
	order, err := c.rank(ctx, query, docs, indices)
	if err != nil {
		return provider.RerankResult{}, err
	}
	keep := len(order)
	if c.prune {
		keep, err = c.cut(ctx, query, docs, order)
		if err != nil {
			return provider.RerankResult{}, err
		}
	}
	scores := make([]float64, len(docs))
	for position, index := range order {
		if position < keep {
			scores[index] = float64(len(order)-position) / float64(len(order))
		}
	}
	return provider.RerankResult{Scores: scores}, nil
}

// boundQuery keeps the query inside the token budget the two questions share.
func (c *typesafeClient) boundQuery(query string) string {
	maxAllowed := max(50, (c.maxQuestionToken-200)/2)
	limit := min(maxQueryTokens, maxAllowed)
	if tokenEstimate(query) > limit {
		return truncateTokens(query, limit)
	}
	return query
}

// rank orders a whole pool best first, in rounds when it exceeds the option or
// token limits. Probabilities are normalised within one call, so the winners of
// each round compete in a finals round; candidates that miss the finals keep
// their caller order behind the finalists.
func (c *typesafeClient) rank(ctx context.Context, query string, docs []string, indices []int) ([]int, error) {
	if len(indices) == 0 {
		return nil, nil
	}

	// Token budget left for candidate options in one choice question.
	stateTokens := tokenEstimate("Question: " + query)
	instrTokens := tokenEstimate(rankInstructionsPrefix+query) + 30
	netBudget := max(50, c.maxQuestionToken-stateTokens-instrTokens)

	// Pre-truncate outliers that alone exceed the question budget, on a copy so
	// the caller's list is untouched.
	effective := append([]string(nil), docs...)
	docTokens := make(map[int]int, len(indices))
	for _, index := range indices {
		tokens := tokenEstimate(effective[index])
		if tokens+optionKeyOverhead > netBudget {
			cap := max(10, netBudget-optionKeyOverhead)
			effective[index] = truncateTokens(effective[index], cap)
			tokens = tokenEstimate(effective[index])
		}
		docTokens[index] = tokens
	}

	groups := c.pack(indices, docTokens, netBudget)

	// Fast path: the whole pool fits in one choice question.
	if len(groups) == 1 {
		return c.rankOnce(ctx, query, effective, groups[0])
	}

	// Groups with two or more candidates are ranked by their own choice
	// question; a one-candidate choice is rejected by the endpoint and the
	// winner is trivial anyway.
	rankedGroups := make([][]int, len(groups))
	copy(rankedGroups, groups)
	for i, group := range groups {
		if len(group) < 2 {
			continue
		}
		ranked, err := c.rankOnce(ctx, query, effective, group)
		if err != nil {
			return nil, err
		}
		rankedGroups[i] = ranked
	}

	// Advance each group's top to the finals; the rest fall back to the caller
	// order. The finals is one choice, so its finalists must fit the option cap.
	quota := max(1, min(c.shortlist, c.maxOptions/len(rankedGroups)))
	var finalists, rest []int
	for _, group := range rankedGroups {
		take := min(quota, len(group))
		finalists = append(finalists, group[:take]...)
		rest = append(rest, group[take:]...)
	}
	if len(finalists) > c.maxOptions {
		rest = append(rest, finalists[c.maxOptions:]...)
		finalists = finalists[:c.maxOptions]
	}
	position := make(map[int]int, len(indices))
	for pos, index := range indices {
		position[index] = pos
	}
	sort.SliceStable(rest, func(a, b int) bool { return position[rest[a]] < position[rest[b]] })

	// Finals: cap every document evenly when the finalists exceed the budget.
	finalistTokens := 0
	for _, index := range finalists {
		finalistTokens += docTokens[index] + optionKeyOverhead
	}
	if finalistTokens > netBudget && len(finalists) > 0 {
		cap := max(10, (netBudget-len(finalists)*optionKeyOverhead)/len(finalists))
		for _, index := range finalists {
			if docTokens[index] > cap {
				effective[index] = truncateTokens(effective[index], cap)
			}
		}
	}
	rankedFinalists, err := c.rankOnce(ctx, query, effective, finalists)
	if err != nil {
		return nil, err
	}
	return append(rankedFinalists, rest...), nil
}

// pack splits the candidates into choice-sized groups bounded by the option cap
// and the token budget.
func (c *typesafeClient) pack(indices []int, docTokens map[int]int, netBudget int) [][]int {
	var groups [][]int
	var current []int
	currentTokens := 0
	for _, index := range indices {
		itemTokens := docTokens[index] + optionKeyOverhead
		if len(current) > 0 && (len(current) >= c.maxOptions || currentTokens+itemTokens > netBudget) {
			groups = append(groups, current)
			current = []int{index}
			currentTokens = itemTokens
			continue
		}
		current = append(current, index)
		currentTokens += itemTokens
	}
	if len(current) > 0 {
		groups = append(groups, current)
	}
	return groups
}

// rankOnce asks one choice question and returns the candidates best first.
func (c *typesafeClient) rankOnce(ctx context.Context, query string, docs []string, indices []int) ([]int, error) {
	criteria := make(map[string]string, len(indices))
	for pos, index := range indices {
		criteria[fmt.Sprintf("c%d", pos)] = docs[index]
	}
	response, err := c.ask(ctx, systemOneRequest{
		State: "Question: " + query,
		Model: c.model,
		Questions: map[string]systemOneQuestion{
			"rank": {Type: "choice", Instructions: rankInstructionsPrefix + query, Criteria: criteria},
		},
	})
	if err != nil {
		return nil, err
	}
	answer, ok := response.Answers["rank"]
	if !ok || len(answer.Probabilities) == 0 {
		return nil, &provider.MalformedResponseError{Detail: "systemone response carried no rank probabilities"}
	}
	// Sort the option positions, not the indices: the key encodes the position,
	// and two candidates can carry identical text. Keys are read once because the
	// comparator sees the slice mid-swap.
	positions := make([]int, len(indices))
	keys := make([]float64, len(indices))
	for pos := range positions {
		positions[pos] = pos
		keys[pos] = answer.Probabilities[fmt.Sprintf("c%d", pos)]
	}
	sort.SliceStable(positions, func(a, b int) bool { return keys[positions[a]] > keys[positions[b]] })
	ordered := make([]int, len(indices))
	for pos, source := range positions {
		ordered[pos] = indices[source]
	}
	return ordered, nil
}

// cut asks the score question how far down the ranked shortlist relevance
// extends and returns how many candidates to keep.
func (c *typesafeClient) cut(ctx context.Context, query string, docs []string, order []int) (int, error) {
	shortlist := order
	if len(shortlist) > c.shortlist {
		shortlist = shortlist[:c.shortlist]
	}
	if len(shortlist) == 0 {
		return 0, nil
	}

	// Instructions and levels are fixed, plus a cushion for the request's JSON
	// framing.
	overhead := tokenEstimate(cutInstructions) + 50
	for _, level := range cutLevels {
		overhead += tokenEstimate(level)
	}
	prefix := fmt.Sprintf("Question: %s\n\nCandidates, already ranked best first:\n", query)
	available := max(100, c.maxQuestionToken-overhead-tokenEstimate(prefix))

	shortDocs := make([]string, len(shortlist))
	tokens := make([]int, len(shortlist))
	total := len(shortlist) * listingItemOverhead
	for i, index := range shortlist {
		shortDocs[i] = docs[index]
		tokens[i] = tokenEstimate(docs[index])
		total += tokens[i]
	}
	if total > available {
		limit := max(10, (available-len(shortlist)*listingItemOverhead)/len(shortlist))
		for i := range shortDocs {
			if tokens[i] > limit {
				shortDocs[i] = truncateTokens(shortDocs[i], limit)
			}
		}
	}
	listing := make([]string, len(shortDocs))
	for i, doc := range shortDocs {
		listing[i] = fmt.Sprintf("[%d] %s", i+1, doc)
	}

	response, err := c.ask(ctx, systemOneRequest{
		State: prefix + strings.Join(listing, "\n\n"),
		Model: c.model,
		Questions: map[string]systemOneQuestion{
			"depth": {Type: "score", Instructions: cutInstructions, Criteria: cutLevels},
		},
	})
	if err != nil {
		return 0, err
	}
	answer, ok := response.Answers["depth"]
	if !ok {
		return 0, &provider.MalformedResponseError{Detail: "systemone response carried no depth score"}
	}
	level := int(math.Round(answer.Score))
	if level < 0 {
		level = 0
	}
	if level >= len(cutDepths) {
		level = len(cutDepths) - 1
	}
	depth := cutDepths[level]
	if depth == 0 {
		return len(shortlist), nil
	}
	return min(depth, len(shortlist)), nil
}

func (c *typesafeClient) ask(ctx context.Context, body systemOneRequest) (systemOneResponse, error) {
	raw, err := c.poster.post(ctx, c.url, c.poster.headers(ctx, c.apiKey), body)
	if err != nil {
		return systemOneResponse{}, err
	}
	var response systemOneResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return systemOneResponse{}, &provider.MalformedResponseError{Detail: "cannot decode systemone response", Cause: err}
	}
	return response, nil
}
