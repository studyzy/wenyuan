package readsurface

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// The document detail's member order is the wire contract: the reference's
// DocumentResponse declares id, bank_id, original_text, content_hash,
// created_at, updated_at, memory_unit_count, nodes_by_fact_type, tags,
// document_metadata, retain_params, observation_scopes, attachments
// (api/http.py:2798-2821), and pydantic serialises a model in declaration
// order. A struct field reordered back to any other arrangement fails here.
func TestDocumentDetailServesTheReferenceMemberOrder(t *testing.T) {
	store := newBankStore(t)
	ctx := context.Background()
	if _, err := store.PutDocument(ctx, bank, "doc-1", storage.Document{OriginalText: "text"}); err != nil {
		t.Fatal(err)
	}
	mux := serve(t, store)

	rec := send(mux, http.MethodGet, "/v1/default/banks/"+bank+"/documents/doc-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d (body %s)", rec.Code, rec.Body.String())
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	order := jsonMemberOrder(t, rec.Body.Bytes())
	want := []string{
		"id", "bank_id", "original_text", "content_hash", "created_at", "updated_at",
		"memory_unit_count", "nodes_by_fact_type", "tags", "document_metadata",
		"retain_params", "observation_scopes", "attachments",
	}
	if len(order) != len(want) {
		t.Fatalf("got %d members %v, want %d %v", len(order), order, len(want), want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("member %d = %q, want %q (full order %v)", i, order[i], want[i], order)
		}
	}
}

// jsonMemberOrder lists a JSON object's top-level member names in the order the
// bytes carry them.
func jsonMemberOrder(t *testing.T, body []byte) []string {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		t.Fatalf("not an object: %v", err)
	}
	var order []string
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			t.Fatalf("key: %v", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			t.Fatalf("key is not a string: %v", keyToken)
		}
		order = append(order, key)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			t.Fatalf("value: %v", err)
		}
	}
	return order
}
