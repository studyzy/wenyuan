package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
)

// readBody decodes the request body into a generic map so a stub can assert on
// the fields a member must send.
func readBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("cannot decode request body: %v", err)
	}
	return body
}

// writeJSON answers a request with a fixed JSON body.
func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

// TestOpenAIEmbedPostsModelAndInputAndReturnsOneVectorPerInput asserts the
// OpenAI member POSTs /embeddings with the model and input, and reassembles the
// response into one vector per input in input order.
func TestOpenAIEmbedPostsModelAndInputAndReturnsOneVectorPerInput(t *testing.T) {
	var gotPath, gotModel string
	var gotInput []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body := readBody(t, r)
		gotModel, _ = body["model"].(string)
		if raw, ok := body["input"].([]any); ok {
			for _, item := range raw {
				gotInput = append(gotInput, item.(string))
			}
		}
		// Returned out of order to prove reassembly is by index, not arrival.
		writeJSON(w, `{"data":[{"index":2,"embedding":[0.3]},{"index":0,"embedding":[0.1]},{"index":1,"embedding":[0.2]}]}`)
	}))
	defer srv.Close()

	emb, err := New(Config{Provider: "openai", APIKey: "k", BaseURL: srv.URL, Model: "text-embed-3", BatchSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	result, err := emb.Embed(context.Background(), provider.EmbeddingRequest{Texts: []string{"a", "b", "c"}})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/embeddings" {
		t.Fatalf("path = %q, want /embeddings", gotPath)
	}
	if gotModel != "text-embed-3" {
		t.Fatalf("model = %q, want text-embed-3", gotModel)
	}
	if strings.Join(gotInput, ",") != "a,b,c" {
		t.Fatalf("input = %v, want [a b c]", gotInput)
	}
	if len(result.Vectors) != 3 {
		t.Fatalf("got %d vectors, want 3", len(result.Vectors))
	}
	if result.Vectors[0][0] != 0.1 || result.Vectors[1][0] != 0.2 || result.Vectors[2][0] != 0.3 {
		t.Fatalf("vectors not in input order: %v", result.Vectors)
	}
	if result.Dimensions != provider.DeclaredDimensions() {
		t.Fatalf("dimensions = %d, want %d", result.Dimensions, provider.DeclaredDimensions())
	}
}

// TestCohereEmbedSelectsInputType asserts the Cohere member sends
// search_query for a query and search_document for a passage, and decodes the
// {embeddings:{float}} envelope.
func TestCohereEmbedSelectsInputType(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embed" {
			t.Errorf("path = %q, want /embed", r.URL.Path)
		}
		body := readBody(t, r)
		inputType, _ := body["input_type"].(string)
		seen = append(seen, inputType)
		writeJSON(w, `{"embeddings":{"float":[[0.5],[0.6]]}}`)
	}))
	defer srv.Close()

	emb, err := New(Config{Provider: "cohere", APIKey: "k", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	query, err := emb.Embed(context.Background(), provider.EmbeddingRequest{Texts: []string{"q1", "q2"}, Kind: provider.EmbeddingQuery})
	if err != nil {
		t.Fatal(err)
	}
	if len(query.Vectors) != 2 {
		t.Fatalf("got %d vectors, want 2", len(query.Vectors))
	}
	if _, err := emb.Embed(context.Background(), provider.EmbeddingRequest{Texts: []string{"p1", "p2"}, Kind: provider.EmbeddingPassage}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "search_query" || seen[1] != "search_document" {
		t.Fatalf("input_types = %v, want [search_query search_document]", seen)
	}
}

// TestTEIEmbedPostsInputsAndDecodesBareArray asserts the TEI member POSTs
// {"inputs":[...]} and reads the bare JSON array of vectors.
func TestTEIEmbedPostsInputsAndDecodesBareArray(t *testing.T) {
	var gotInputs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embed" {
			t.Errorf("path = %q, want /embed", r.URL.Path)
		}
		body := readBody(t, r)
		if raw, ok := body["inputs"].([]any); ok {
			for _, item := range raw {
				gotInputs = append(gotInputs, item.(string))
			}
		}
		writeJSON(w, `[[0.1,0.1],[0.2,0.2]]`)
	}))
	defer srv.Close()

	emb, err := New(Config{Provider: "tei", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	result, err := emb.Embed(context.Background(), provider.EmbeddingRequest{Texts: []string{"x", "y"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(gotInputs, ",") != "x,y" {
		t.Fatalf("inputs = %v, want [x y]", gotInputs)
	}
	if len(result.Vectors) != 2 || result.Vectors[1][0] != 0.2 {
		t.Fatalf("vectors = %v", result.Vectors)
	}
}

// TestGeminiEmbedUsesBatchEmbedPathAndKey asserts the Gemini member reaches
// {model}:batchEmbedContents with the key query parameter and decodes
// {embeddings:[{values}]}.
func TestGeminiEmbedUsesBatchEmbedPathAndKey(t *testing.T) {
	var gotPath, gotKey string
	var gotRequests []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.URL.Query().Get("key")
		body := readBody(t, r)
		gotRequests, _ = body["requests"].([]any)
		writeJSON(w, `{"embeddings":[{"values":[0.7]},{"values":[0.8]}]}`)
	}))
	defer srv.Close()

	emb, err := New(Config{Provider: "gemini", APIKey: "secret", BaseURL: srv.URL, Model: "text-embedding-004"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := emb.Embed(context.Background(), provider.EmbeddingRequest{Texts: []string{"x", "y"}})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1beta/models/text-embedding-004:batchEmbedContents" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotKey != "secret" {
		t.Fatalf("key query = %q, want secret", gotKey)
	}
	if len(gotRequests) != 2 {
		t.Fatalf("requests = %v, want 2 items", gotRequests)
	}
	if len(result.Vectors) != 2 || result.Vectors[0][0] != 0.7 || result.Vectors[1][0] != 0.8 {
		t.Fatalf("vectors = %v", result.Vectors)
	}
}

// TestRefusedMembersNameProviderKey asserts the members needing a native
// runtime or an SDK are refused by name with the offending configuration key.
func TestRefusedMembersNameProviderKey(t *testing.T) {
	for _, name := range []string{"onnx", "local", "litellm-sdk", "openai-codex", "google"} {
		_, err := New(Config{Provider: name})
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}
		if !strings.Contains(err.Error(), "not supported by this build") {
			t.Fatalf("%s: error %q does not say it is unsupported", name, err)
		}
		if !strings.Contains(err.Error(), "WENYUAN_API_EMBEDDINGS_PROVIDER") {
			t.Fatalf("%s: error %q does not name the provider key", name, err)
		}
	}
}

// TestMissingCredentialNamesKey asserts a member that needs an API key names
// the key when it is absent.
func TestMissingCredentialNamesKey(t *testing.T) {
	cases := map[string]string{
		"openai":      "WENYUAN_API_EMBEDDINGS_OPENAI_API_KEY",
		"cohere":      "WENYUAN_API_EMBEDDINGS_COHERE_API_KEY",
		"zeroentropy": "WENYUAN_API_EMBEDDINGS_ZEROENTROPY_API_KEY",
		"gemini":      "WENYUAN_API_EMBEDDINGS_GEMINI_API_KEY",
		"openrouter":  "WENYUAN_API_EMBEDDINGS_OPENROUTER_API_KEY",
		"requesty":    "WENYUAN_API_EMBEDDINGS_REQUESTY_API_KEY",
	}
	for name, key := range cases {
		_, err := New(Config{Provider: name})
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}
		if !strings.Contains(err.Error(), key) {
			t.Fatalf("%s: error %q does not name %s", name, err, key)
		}
	}
}

// TestDimensionsPrefersConfiguredWidth asserts Dimensions reports the
// configured width when set and the boundary constant otherwise.
func TestDimensionsPrefersConfiguredWidth(t *testing.T) {
	cfg := Config{Provider: "tei", BaseURL: "http://127.0.0.1:1", Dimensions: 512}
	emb, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if emb.Dimensions() != 512 {
		t.Fatalf("dimensions = %d, want 512", emb.Dimensions())
	}
	emb, err = New(Config{Provider: "tei", BaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	if emb.Dimensions() != provider.DeclaredDimensions() {
		t.Fatalf("dimensions = %d, want %d", emb.Dimensions(), provider.DeclaredDimensions())
	}
}

// TestEmptyProviderIsNil asserts an unconfigured provider builds no backend.
func TestEmptyProviderIsNil(t *testing.T) {
	emb, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if emb != nil {
		t.Fatalf("got %T, want nil", emb)
	}
}
