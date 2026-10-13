package readsurface

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// bank is the identifier the fixture creates and every route is served under.
const bank = "b1"

// newServer builds the family's route table over a store seeded with one
// document, its units, entities and links, translating path patterns into the
// path values the real router sets.
func newServer(t *testing.T) *http.ServeMux {
	t.Helper()
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{ID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	// A document written through the retain pipeline brings its chunks, units
	// and entities with it, which is what the read routes surface.
	if _, err := store.RetainWrite(ctx, storage.RetainRequest{
		BankID:       bank,
		DocumentTags: []string{"work"},
		Items: []storage.RetainItem{{
			Content:    "Alice works at Google on the AI team.",
			DocumentID: "doc-1",
			Entities: []storage.RetainEntity{
				{Text: "Alice", Kind: "PERSON"},
				{Text: "Google", Kind: "ORG"},
			},
			Tags: []string{"work"},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", bank)
			for _, param := range []string{"document_id", "entity_id", "chunk_id"} {
				if strings.Contains(pattern, "{"+param+"}") {
					r.SetPathValue(param, lastSegment(r.URL.Path))
				}
			}
			handler(w, r)
		})
	})
	return mux
}

// lastSegment returns the final path segment, skipping a trailing action word.
func lastSegment(path string) string {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for i := len(segments) - 1; i >= 0; i-- {
		switch segments[i] {
		case "chunks", "reprocess", "regenerate":
			continue
		default:
			return segments[i]
		}
	}
	return ""
}

func send(mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// docID reads the one document the fixture wrote.
func docID(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	rec := send(mux, http.MethodGet, "/v1/default/banks/"+bank+"/documents", "")
	var list DocumentListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("documents = %d, want 1", len(list.Items))
	}
	return list.Items[0].ID
}

// The document read routes return the stored document and its chunks.
func TestDocumentReadRoutes(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	id := docID(t, mux)

	got := send(mux, http.MethodGet, base+"/documents/"+id, "")
	if got.Code != http.StatusOK {
		t.Fatalf("get document: %d, body = %s", got.Code, got.Body.String())
	}
	var doc DocumentResponse
	if err := json.Unmarshal(got.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.ID != id || doc.Tags == nil {
		t.Fatalf("document = %+v", doc)
	}

	chunks := send(mux, http.MethodGet, base+"/documents/"+id+"/chunks", "")
	if chunks.Code != http.StatusOK {
		t.Fatalf("list chunks: %d", chunks.Code)
	}
	var chunkList ChunkListResponse
	if err := json.Unmarshal(chunks.Body.Bytes(), &chunkList); err != nil {
		t.Fatal(err)
	}
	if len(chunkList.Items) == 0 {
		t.Fatal("the retained document has no chunks")
	}
	chunkID := chunkList.Items[0].ChunkID

	// A chunk is addressed in its own non-bank-scoped namespace.
	one := send(mux, http.MethodGet, "/v1/default/chunks/"+chunkID, "")
	if one.Code != http.StatusOK {
		t.Fatalf("get chunk: %d, body = %s", one.Code, one.Body.String())
	}

	reprocess := send(mux, http.MethodPost, base+"/documents/"+id+"/reprocess", "")
	if reprocess.Code != http.StatusOK {
		t.Fatalf("reprocess: %d, body = %s", reprocess.Code, reprocess.Body.String())
	}
	var result ReprocessDocumentResponse
	if err := json.Unmarshal(reprocess.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.OperationID == "" {
		t.Fatalf("reprocess result = %+v", result)
	}
}

// A document can be updated in place and then deleted.
func TestDocumentUpdateAndDelete(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	id := docID(t, mux)

	updated := send(mux, http.MethodPatch, base+"/documents/"+id, `{"tags":["fresh"],"metadata":{"k":"v"}}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("update: %d, body = %s", updated.Code, updated.Body.String())
	}
	after := send(mux, http.MethodGet, base+"/documents/"+id, "")
	var doc DocumentResponse
	if err := json.Unmarshal(after.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Tags) != 1 || doc.Tags[0] != "fresh" {
		t.Fatalf("tags after update = %v", doc.Tags)
	}

	if rec := send(mux, http.MethodDelete, base+"/documents/"+id, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := send(mux, http.MethodGet, base+"/documents/"+id, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: %d, want 404", rec.Code)
	}
}

// A malformed update body is rejected.
func TestDocumentUpdateRejectsBadBody(t *testing.T) {
	mux := newServer(t)
	id := docID(t, mux)
	rec := send(mux, http.MethodPatch, "/v1/default/banks/"+bank+"/documents/"+id, "not json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: %d, want 400", rec.Code)
	}
}

// The tags and graph routes return the bank's derived views.
func TestTagsAndGraphRoutes(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank

	tags := send(mux, http.MethodGet, base+"/tags", "")
	if tags.Code != http.StatusOK {
		t.Fatalf("tags: %d", tags.Code)
	}
	var tagList TagListResponse
	if err := json.Unmarshal(tags.Body.Bytes(), &tagList); err != nil {
		t.Fatal(err)
	}
	if tagList.Total != 1 || tagList.Items[0].Tag != "work" {
		t.Fatalf("tags = %+v", tagList)
	}

	graph := send(mux, http.MethodGet, base+"/graph", "")
	if graph.Code != http.StatusOK {
		t.Fatalf("graph: %d", graph.Code)
	}
	var graphBody GraphResponse
	if err := json.Unmarshal(graph.Body.Bytes(), &graphBody); err != nil {
		t.Fatal(err)
	}
	if len(graphBody.Nodes) == 0 || graphBody.TableRows == nil || graphBody.Nodes == nil {
		t.Fatalf("graph = %+v", graphBody)
	}

	entities := send(mux, http.MethodGet, base+"/entities", "")
	if entities.Code != http.StatusOK {
		t.Fatalf("entities: %d", entities.Code)
	}
	var entityList EntityListResponse
	if err := json.Unmarshal(entities.Body.Bytes(), &entityList); err != nil {
		t.Fatal(err)
	}
	if len(entityList.Items) == 0 {
		t.Fatal("the retained document resolved no entities")
	}
	entityID := entityList.Items[0].ID

	detail := send(mux, http.MethodGet, base+"/entities/"+entityID, "")
	if detail.Code != http.StatusOK {
		t.Fatalf("entity detail: %d, body = %s", detail.Code, detail.Body.String())
	}
	if regenerated := send(mux, http.MethodPost, base+"/entities/"+entityID+"/regenerate", ""); regenerated.Code != http.StatusOK {
		t.Fatalf("regenerate: %d, body = %s", regenerated.Code, regenerated.Body.String())
	}
	if entityGraph := send(mux, http.MethodGet, base+"/entities/graph", ""); entityGraph.Code != http.StatusOK {
		t.Fatalf("entity graph: %d", entityGraph.Code)
	}
}

// An unknown bank answers not-found, and an unknown member answers not-found.
func TestReadSurfaceNegativeCases(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", "missing")
			handler(w, r)
		})
	})
	if rec := send(mux, http.MethodGet, "/v1/default/banks/missing/documents", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown bank: %d, want 404", rec.Code)
	}

	seeded := newServer(t)
	base := "/v1/default/banks/" + bank
	if rec := send(seeded, http.MethodGet, base+"/documents/nope", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown document: %d, want 404", rec.Code)
	}
	if rec := send(seeded, http.MethodGet, base+"/entities/nope", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown entity: %d, want 404", rec.Code)
	}
	if rec := send(seeded, http.MethodGet, "/v1/default/chunks/", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("empty chunk id: %d, want 404", rec.Code)
	}
}
