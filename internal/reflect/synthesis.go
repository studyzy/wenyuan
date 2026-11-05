package reflect

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// splitChunkFloorTokens keeps a very small configured budget from shredding the
// evidence into one chunk per line. A floor this size leaves room for the rest
// of the prompt on any real model.
const splitChunkFloorTokens = 1024

// splitChunkFraction is the share of the context budget one evidence chunk may
// use; the rest is reserved for the system prompt, the question and the answer
// instructions that travel with it.
const splitChunkFraction = 0.8

// evidenceLines renders every gathered item for synthesis. Unlike the loop's
// bounded digest, this keeps the full set: the splitter, not a truncation, is
// what decides how much evidence reaches the answer.
func (l *Loop) evidenceLines() []string {
	lines := make([]string, 0, len(l.evidence))
	for _, item := range l.evidence {
		lines = append(lines, l.renderEvidenceLine(item))
	}
	return lines
}

// chunkBudget is the per-chunk evidence budget for a split synthesis.
func (l *Loop) chunkBudget() int {
	budget := int(float64(l.cfg.ContextBudget) * splitChunkFraction)
	if budget < splitChunkFloorTokens {
		budget = splitChunkFloorTokens
	}
	return budget
}

// splitEvidenceChunks partitions rendered evidence lines into chunks that each
// fit the budget, keeping chronological order. An over-budget line is cut to
// the budget rather than dropped, so evidence beyond the old truncation still
// reaches a map call. The caller synthesizes from one chunk directly and from
// several through a map/reduce pass.
func splitEvidenceChunks(lines []string, budget int) [][]string {
	if len(lines) == 0 {
		return nil
	}
	if budget < 1 {
		budget = 1
	}
	chunks := make([][]string, 0, 1)
	current := make([]string, 0, len(lines))
	used := 0
	for _, line := range lines {
		cost := storage.CountTokens(line)
		if cost > budget {
			line = cutToBudget(line, budget)
			cost = storage.CountTokens(line)
		}
		if len(current) > 0 && used+cost > budget {
			chunks = append(chunks, current)
			current = make([]string, 0, len(lines))
			used = 0
		}
		current = append(current, line)
		used += cost
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

// cutToBudget trims one indivisible line until its token count fits, so a single
// large result is shortened rather than lost.
func cutToBudget(text string, budget int) string {
	for storage.CountTokens(text) > budget && len(text) > 1 {
		tokens := storage.CountTokens(text)
		if tokens <= 0 {
			break
		}
		keep := len(text) * budget / tokens
		if keep < 1 {
			keep = 1
		}
		if keep >= len(text) {
			keep = len(text) - 1
		}
		text = text[:keep]
	}
	return text
}

// claimsSystemPrompt is the system prompt of a map pass. It extracts evidence
// rather than answering: only the reduce pass sees every chunk, so each claim
// must carry the date and the handles it came from.
const claimsSystemPrompt = `You are an evidence-extraction pass. Work only from the data supplied; never add facts, names, dates or identities that are not present.

Return a bullet list of the claims in the data that bear on the question. Write each claim as one sentence in the question's language, then tag it with when it was recorded, when it happened, and the ids it came from, like:
(mentioned_at: <date or unknown>; occurred: <date/range or unknown>; memory_ids: <ids>)

Guidelines:
- Include all relevant entries; drop obviously unrelated ones.
- Do not summarize, decide between conflicting entries, or answer the question; report conflicts as separate bullets and keep their dates.
- Reproduce ids verbatim.
- With nothing relevant, reply with exactly: (no relevant evidence)`

// buildChunkClaimsPrompt is one map call's user message: its chunk of the
// evidence and the question, with instructions to list claims only.
func buildChunkClaimsPrompt(question, chunk string) string {
	return strings.Join([]string{
		"## Retrieved Data (extract the claims relevant to the question)",
		chunk,
		"\n## Question\n" + question,
		"\n## Instructions\nList each relevant claim from the data above as a bullet with its provenance tag. Do not answer the question.",
	}, "\n")
}

// buildReducePrompt is the final synthesis user message when the evidence was
// split: every chunk's claims are one evidence pool, and a later-dated claim
// supersedes an earlier one across sections.
func buildReducePrompt(profile bankProfile, question string, claims []string, maxTokens int) string {
	parts := finalIdentitySection(profile)
	parts = append(parts,
		"\n## Retrieved Evidence (synthesize the answer from these claim sets)",
		"The retrieved data was processed in parallel passes; each section below holds one pass's extracted claims with their dates and ids. Treat the sections as one evidence pool: related and conflicting claims may sit in different sections.")
	for index, section := range claims {
		parts = append(parts, fmt.Sprintf("\n### Evidence pass %d:\n%s", index+1, section))
	}
	parts = append(parts, "\n## Question\n"+question)
	parts = append(parts, "\n## Instructions\n"+
		"When two claims about the same fact disagree, the one with the latest mentioned_at wins, whichever section it came from. Equally recent claims that still conflict should be reported as a disagreement rather than resolved by picking one.\n\n"+
		finalInstructions)
	if directive := lengthDirective(maxTokens); directive != "" {
		parts = append(parts, directive)
	}
	return strings.Join(parts, "\n")
}

// answerFromEvidence writes the answer from the gathered evidence. When it fits
// one chunk this is a single synthesis call; otherwise each chunk is compressed
// into claims in parallel and one reduce call synthesizes the answer from all
// of them. It returns the raw answer text and the final call's error, leaving
// the empty-answer and output-limit handling to compose.
func (l *Loop) answerFromEvidence(ctx context.Context, outcome *Outcome) (string, error) {
	chunks := splitEvidenceChunks(l.evidenceLines(), l.chunkBudget())
	system := buildFinalSystemPrompt(l.retrieval.Directives(ctx), l.profile())
	if len(chunks) <= 1 {
		evidence := ""
		if len(chunks) == 1 {
			evidence = strings.Join(chunks[0], "\n")
		}
		response, err := l.callModel(ctx, "final", provider.CompletionRequest{
			Messages: []provider.Message{
				{Role: provider.RoleSystem, Content: system},
				{Role: provider.RoleUser, Content: buildFinalPrompt(l.profile(), l.cfg.Question, evidence, l.cfg.MaxTokens)},
			},
			Scope:     "reflect",
			MaxTokens: l.synthesisMaxTokens(),
		})
		outcome.Usage = addUsage(outcome.Usage, response.Usage)
		return strings.TrimSpace(response.Content), err
	}
	claims, usage := l.mapEvidenceChunks(ctx, chunks)
	outcome.Usage = addUsage(outcome.Usage, usage)
	response, err := l.callModel(ctx, "final", provider.CompletionRequest{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: system},
			{Role: provider.RoleUser, Content: buildReducePrompt(l.profile(), l.cfg.Question, claims, l.cfg.MaxTokens)},
		},
		Scope:     "reflect",
		MaxTokens: l.synthesisMaxTokens(),
	})
	outcome.Usage = addUsage(outcome.Usage, response.Usage)
	return strings.TrimSpace(response.Content), err
}

// mapEvidenceChunks compresses each chunk into claims in parallel. The calls do
// not go through callModel: their trace entries are recorded here, in chunk
// order, once every result has been gathered, so a parallel round leaves a
// deterministic list of final_map_<n> scopes.
func (l *Loop) mapEvidenceChunks(ctx context.Context, chunks [][]string) ([]string, provider.Usage) {
	type chunkResult struct {
		claims     string
		usage      provider.Usage
		durationMs int
	}
	results := make([]chunkResult, len(chunks))
	var wg sync.WaitGroup
	for index, chunk := range chunks {
		wg.Add(1)
		go func(i int, lines []string) {
			defer wg.Done()
			started := time.Now()
			response, err := l.provider.Complete(ctx, provider.CompletionRequest{
				Messages: []provider.Message{
					{Role: provider.RoleSystem, Content: claimsSystemPrompt},
					{Role: provider.RoleUser, Content: buildChunkClaimsPrompt(l.cfg.Question, strings.Join(lines, "\n"))},
				},
				Scope:       "reflect",
				MaxTokens:   l.synthesisMaxTokens(),
				Temperature: l.deterministicTemperature(),
			})
			_ = err
			results[i] = chunkResult{
				claims:     strings.TrimSpace(response.Content),
				usage:      response.Usage,
				durationMs: int(time.Since(started).Milliseconds()),
			}
		}(index, chunk)
	}
	wg.Wait()
	claims := make([]string, len(chunks))
	var usage provider.Usage
	for index, result := range results {
		l.recordCall(fmt.Sprintf("final_map_%d", index+1), result.durationMs)
		usage = addUsage(usage, result.usage)
		claims[index] = result.claims
	}
	return claims, usage
}
