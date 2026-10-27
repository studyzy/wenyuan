package rerank

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
)

// fastRetry is a retry policy that neither sleeps nor retries, so failover
// tests exercise the chain rather than the backoff.
func fastRetry() provider.RetryPolicy {
	return provider.RetryPolicy{InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}
}

func TestSiliconflowMemberRequestShapeAndScores(t *testing.T) {
	var sawRequest bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRequest = true
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/rerank" {
			t.Errorf("path = %s, want /rerank", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sf-key" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer sf-key")
		}
		var body struct {
			Model           string   `json:"model"`
			Query           string   `json:"query"`
			Documents       []string `json:"documents"`
			TopN            *int     `json:"top_n"`
			ReturnDocuments *bool    `json:"return_documents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body.Model != "my-model" {
			t.Errorf("model = %q, want my-model", body.Model)
		}
		if body.Query != "capital of France" {
			t.Errorf("query = %q", body.Query)
		}
		if len(body.Documents) != 3 || body.Documents[0] != "a" || body.Documents[2] != "c" {
			t.Errorf("documents = %v", body.Documents)
		}
		if body.TopN == nil || *body.TopN != 3 {
			t.Errorf("top_n = %v, want 3", body.TopN)
		}
		if body.ReturnDocuments == nil || *body.ReturnDocuments {
			t.Errorf("return_documents = %v, want false", body.ReturnDocuments)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"index":2,"relevance_score":0.2},{"index":0,"relevance_score":0.9},{"index":1,"relevance_score":0.5}]}`))
	}))
	defer server.Close()

	reranker, err := New(Config{
		Provider: "siliconflow",
		Primary: Member{
			Provider: "siliconflow",
			APIKey:   "sf-key",
			BaseURL:  server.URL,
			Model:    "my-model",
		},
		Retry: fastRetry(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	result, err := reranker.Rerank(context.Background(), provider.RerankRequest{
		Query:     "capital of France",
		Documents: []string{"a", "b", "c"},
	})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if !sawRequest {
		t.Fatal("stub never received a request")
	}
	want := []float64{0.9, 0.5, 0.2}
	if len(result.Scores) != len(want) {
		t.Fatalf("scores = %v, want one per document", result.Scores)
	}
	for i, score := range want {
		if result.Scores[i] != score {
			t.Errorf("scores[%d] = %v, want %v (input order)", i, result.Scores[i], score)
		}
	}
	if scorer, ok := reranker.(RankPositionReranker); !ok || scorer.ScoresByRankPosition() {
		t.Errorf("siliconflow must report relevance scores, not rank positions")
	}
}

func TestTEIMemberRequestShapeAndScores(t *testing.T) {
	var sawRequest bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRequest = true
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/rerank" {
			t.Errorf("path = %s, want /rerank", r.URL.Path)
		}
		var body struct {
			Query string   `json:"query"`
			Texts []string `json:"texts"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body.Query != "q" || len(body.Texts) != 2 || body.Texts[0] != "first" {
			t.Errorf("body = %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"index":1,"score":0.1},{"index":0,"score":0.7}]`))
	}))
	defer server.Close()

	reranker, err := New(Config{
		Provider: "tei",
		Primary:  Member{Provider: "tei", BaseURL: server.URL},
		Retry:    fastRetry(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	result, err := reranker.Rerank(context.Background(), provider.RerankRequest{
		Query:     "q",
		Documents: []string{"first", "second"},
	})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if !sawRequest {
		t.Fatal("stub never received a request")
	}
	if len(result.Scores) != 2 || result.Scores[0] != 0.7 || result.Scores[1] != 0.1 {
		t.Errorf("scores = %v, want [0.7 0.1]", result.Scores)
	}
}

func TestTypeSafePruningOrdersAndCuts(t *testing.T) {
	var sawRank, sawCut bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("path = %s, want /v1/systemone", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer ts-key" {
			t.Errorf("Authorization = %q", got)
		}
		var body struct {
			State     string `json:"state"`
			Model     string `json:"model"`
			Questions map[string]struct {
				Type         string          `json:"type"`
				Instructions string          `json:"instructions"`
				Criteria     json.RawMessage `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body.Model != "jev" {
			t.Errorf("model = %q, want jev", body.Model)
		}
		if question, ok := body.Questions["rank"]; ok {
			sawRank = true
			if question.Type != "choice" {
				t.Errorf("rank type = %q, want choice", question.Type)
			}
			if question.Instructions != "Which candidate answers the question: cat" {
				t.Errorf("rank instructions = %q", question.Instructions)
			}
			if body.State != "Question: cat" {
				t.Errorf("state = %q", body.State)
			}
			var criteria map[string]string
			if err := json.Unmarshal(question.Criteria, &criteria); err != nil {
				t.Errorf("criteria: %v", err)
			}
			if criteria["c0"] != "dog runs" || criteria["c2"] != "cat cat cat" {
				t.Errorf("criteria = %v", criteria)
			}
			// Lexical stub: score each option by how often the query word occurs.
			total := 0.0
			raw := make(map[string]float64, len(criteria))
			for key, text := range criteria {
				score := float64(strings.Count(text, "cat"))
				raw[key] = score
				total += score
			}
			if total == 0 {
				total = 1
			}
			probabilities := map[string]float64{}
			for key, score := range raw {
				probabilities[key] = score / total
			}
			writeJSON(t, w, map[string]any{
				"answers": map[string]any{"rank": map[string]any{"type": "choice", "probabilities": probabilities}},
			})
			return
		}
		if question, ok := body.Questions["depth"]; ok {
			sawCut = true
			if question.Type != "score" {
				t.Errorf("depth type = %q, want score", question.Type)
			}
			if question.Instructions != cutInstructions {
				t.Errorf("depth instructions = %q", question.Instructions)
			}
			var criteria []string
			if err := json.Unmarshal(question.Criteria, &criteria); err != nil {
				t.Errorf("depth criteria: %v", err)
			}
			if len(criteria) != len(cutLevels) {
				t.Fatalf("depth criteria = %v, want the %d cut levels", criteria, len(cutLevels))
			}
			for i, level := range cutLevels {
				if criteria[i] != level {
					t.Errorf("cut level %d = %q, want %q", i, criteria[i], level)
				}
			}
			writeJSON(t, w, map[string]any{
				"answers": map[string]any{"depth": map[string]any{"type": "score", "score": 2.0}},
			})
			return
		}
		t.Errorf("unexpected questions: %v", body.Questions)
		writeJSON(t, w, map[string]any{"answers": map[string]any{}})
	}))
	defer server.Close()

	reranker, err := New(Config{
		Provider: "typesafe",
		Primary: Member{
			Provider:        "typesafe",
			APIKey:          "ts-key",
			BaseURL:         server.URL,
			PruneCandidates: true,
		},
		Retry: fastRetry(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	scorer, ok := reranker.(RankPositionReranker)
	if !ok || !scorer.ScoresByRankPosition() {
		t.Fatalf("typesafe primary must report rank positions")
	}
	if pruner, ok := reranker.(interface{ PrunesCandidates() bool }); !ok || !pruner.PrunesCandidates() {
		t.Fatalf("typesafe primary with pruning must report that it prunes candidates")
	}

	result, err := reranker.Rerank(context.Background(), provider.RerankRequest{
		Query:     "cat",
		Documents: []string{"dog runs", "cat sits", "cat cat cat", "bird flies"},
	})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if !sawRank || !sawCut {
		t.Fatalf("rank=%v cut=%v, want both questions asked", sawRank, sawCut)
	}
	// The lexically best candidate must come first, so it earns the top score.
	if result.Scores[2] != 1.0 {
		t.Errorf("scores = %v, want the lexically best candidate first with 1.0", result.Scores)
	}
	for i, score := range result.Scores {
		if score > result.Scores[2] {
			t.Errorf("scores[%d] = %v exceeds the best candidate's score", i, score)
		}
	}
	// Cut level 2 keeps the first three of four; everything at or after the cut
	// is exactly 0.0.
	if result.Scores[3] != 0.0 {
		t.Errorf("scores[3] = %v, want exactly 0.0 past the cut", result.Scores[3])
	}
	for i := 0; i < 3; i++ {
		if result.Scores[i] == 0.0 {
			t.Errorf("scores[%d] = 0.0, want a kept candidate above the cut", i)
		}
	}
}

func TestChainFailsOverToSecondMember(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"index":0,"relevance_score":0.8},{"index":1,"relevance_score":0.2}]}`))
	}))
	defer fallback.Close()

	reranker, err := New(Config{
		Provider: "siliconflow",
		Primary: Member{
			Provider: "siliconflow",
			APIKey:   "sf-key",
			BaseURL:  primary.URL,
		},
		Members: []Member{
			{Provider: "siliconflow", APIKey: "sf-key", BaseURL: fallback.URL},
		},
		Retry: fastRetry(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	result, err := reranker.Rerank(context.Background(), provider.RerankRequest{
		Query:     "q",
		Documents: []string{"a", "b"},
	})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(result.Scores) != 2 || result.Scores[0] != 0.8 || result.Scores[1] != 0.2 {
		t.Fatalf("scores = %v, want the fallback member's scores", result.Scores)
	}
}

func TestChainSurfacesLastErrorWhenEveryMemberFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer server.Close()

	reranker, err := New(Config{
		Provider: "siliconflow",
		Primary:  Member{Provider: "siliconflow", APIKey: "sf-key", BaseURL: server.URL},
		Retry:    fastRetry(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := reranker.Rerank(context.Background(), provider.RerankRequest{Query: "q", Documents: []string{"a"}}); err == nil {
		t.Fatal("Rerank succeeded, want the endpoint's error")
	}
}

func TestUnsupportedMembersAreRefusedByName(t *testing.T) {
	for _, name := range []string{"local", "flashrank", "jina-mlx", "litellm", "litellm-sdk", "google", "alibaba"} {
		t.Run(name, func(t *testing.T) {
			_, err := New(Config{Provider: name, Primary: Member{Provider: name, APIKey: "key"}})
			if err == nil {
				t.Fatalf("New(%q) succeeded, want a refusal", name)
			}
			if !strings.Contains(err.Error(), "not supported by this build") {
				t.Errorf("error %q does not say it is unsupported by this build", err)
			}
			if !strings.Contains(err.Error(), "WENYUAN_API_RERANKER_PROVIDER") {
				t.Errorf("error %q does not name WENYUAN_API_RERANKER_PROVIDER", err)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not name the member %q", err, name)
			}
		})
	}
}

func TestMissingCredentialAndEndpointErrorsNameTheirKeys(t *testing.T) {
	cases := []struct {
		name    string
		member  Member
		wantKey string
	}{
		{
			name:    "siliconflow key",
			member:  Member{Provider: "siliconflow"},
			wantKey: "WENYUAN_API_RERANKER_SILICONFLOW_API_KEY",
		},
		{
			name:    "zeroentropy key",
			member:  Member{Provider: "zeroentropy"},
			wantKey: "WENYUAN_API_RERANKER_ZEROENTROPY_API_KEY",
		},
		{
			name:    "openrouter key",
			member:  Member{Provider: "openrouter"},
			wantKey: "WENYUAN_API_RERANKER_OPENROUTER_API_KEY",
		},
		{
			name:    "typesafe key",
			member:  Member{Provider: "typesafe"},
			wantKey: "WENYUAN_API_RERANKER_TYPESAFE_API_KEY",
		},
		{
			name:    "cohere key",
			member:  Member{Provider: "cohere"},
			wantKey: "WENYUAN_API_RERANKER_COHERE_API_KEY",
		},
		{
			name:    "cohere base url",
			member:  Member{Provider: "cohere", APIKey: "key"},
			wantKey: "WENYUAN_API_RERANKER_COHERE_BASE_URL",
		},
		{
			name:    "tei url",
			member:  Member{Provider: "tei"},
			wantKey: "WENYUAN_API_RERANKER_TEI_URL",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := New(Config{Provider: testCase.member.Provider, Primary: testCase.member})
			if err == nil {
				t.Fatalf("New succeeded, want an error naming %s", testCase.wantKey)
			}
			if !strings.Contains(err.Error(), testCase.wantKey) {
				t.Errorf("error %q does not name %s", err, testCase.wantKey)
			}
		})
	}
}

func TestIndexedMemberErrorsNameIndexedKeys(t *testing.T) {
	_, err := New(Config{
		Provider: "siliconflow",
		Primary:  Member{Provider: "siliconflow", APIKey: "sf-key"},
		Members:  []Member{{Provider: "zeroentropy"}},
	})
	if err == nil {
		t.Fatal("New succeeded, want a missing-key error for member 1")
	}
	if !strings.Contains(err.Error(), "WENYUAN_API_RERANKER_1_ZEROENTROPY_API_KEY") {
		t.Errorf("error %q does not name the indexed member's key", err)
	}
}

func TestRRFPassthroughPreservesOrderWithoutCredential(t *testing.T) {
	reranker, err := New(Config{Provider: "rrf", Primary: Member{Provider: "rrf"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	result, err := reranker.Rerank(context.Background(), provider.RerankRequest{
		Query:     "q",
		Documents: []string{"a", "b", "c"},
	})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	want := []float64{1.0, 2.0 / 3.0, 1.0 / 3.0}
	for i, score := range want {
		if result.Scores[i] != score {
			t.Errorf("scores[%d] = %v, want %v", i, result.Scores[i], score)
		}
	}
}

func TestCohereMemberUsesBaseURLAsFullPath(t *testing.T) {
	var sawTopN bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models/rerank/invoke" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if _, ok := body["top_n"]; ok {
			sawTopN = true
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"index":0,"relevance_score":0.4}]}`))
	}))
	defer server.Close()

	reranker, err := New(Config{
		Provider: "cohere",
		Primary:  Member{Provider: "cohere", APIKey: "co-key", BaseURL: server.URL + "/models/rerank/invoke"},
		Retry:    fastRetry(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	result, err := reranker.Rerank(context.Background(), provider.RerankRequest{Query: "q", Documents: []string{"a"}})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if sawTopN {
		t.Error("cohere-compatible host must not receive top_n")
	}
	if len(result.Scores) != 1 || result.Scores[0] != 0.4 {
		t.Errorf("scores = %v", result.Scores)
	}
}

func TestEmptyProviderReturnsNoReranker(t *testing.T) {
	reranker, err := New(Config{})
	if err != nil || reranker != nil {
		t.Fatalf("New(Config{}) = %v, %v; want nil, nil", reranker, err)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Errorf("encode response: %v", err)
	}
}
