package readsurface

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
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
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
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
	handlers := NewHandlers(store)
	// The read surface delegates a reprocess to the write-side pipeline, which
	// the composition root normally supplies. Here the store's own retain write
	// stands in, so the route exercises a real re-extraction.
	handlers.Reprocess = reprocessor{store: store}
	handlers.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
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

	if rec := send(mux, http.MethodDelete, base+"/documents/"+id, ""); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := send(mux, http.MethodGet, base+"/documents/"+id, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: %d, want 404", rec.Code)
	}
}

// A malformed update body is rejected with the shared validation envelope.
func TestDocumentUpdateRejectsBadBody(t *testing.T) {
	mux := newServer(t)
	id := docID(t, mux)
	rec := send(mux, http.MethodPatch, "/v1/default/banks/"+bank+"/documents/"+id, "not json")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed body: %d, want 422", rec.Code)
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
	if regenerated := send(mux, http.MethodPost, base+"/entities/"+entityID+"/regenerate", ""); regenerated.Code != http.StatusGone {
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

// A chunk read shows the attachments its units were drawn from.
func TestDocumentChunksCarryAttachments(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetainWrite(ctx, storage.RetainRequest{
		BankID: bank,
		Items:  []storage.RetainItem{{Content: "Alice joined the team.", DocumentID: "doc-1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutAttachment(ctx, storage.Attachment{
		BankID: bank, DocumentID: "doc-1", AttachmentHash: "hash-1", ShortID: "att-1",
		MediaType: "image/png", ByteSize: 3, StorageKey: "banks/b1/doc-1/hash-1", Kind: "image",
		Filename: "team.png",
	}); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListChunks(ctx, storage.ChunkQuery{BankID: bank, DocumentID: "doc-1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Chunks) == 0 {
		t.Fatal("fixture produced no chunks")
	}
	// A unit drawn from the first chunk carries the attachment.
	if _, err := store.UpsertUnit(ctx, storage.MemoryUnit{
		BankID: bank, DocumentID: "doc-1", ChunkID: page.Chunks[0].ChunkID,
		Text: "Alice joined the team.", AttachmentIDs: []string{"att-1"},
	}); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", bank)
			for _, param := range []string{"document_id", "chunk_id"} {
				if strings.Contains(pattern, "{"+param+"}") {
					r.SetPathValue(param, lastSegment(r.URL.Path))
				}
			}
			handler(w, r)
		})
	})
	rec := send(mux, http.MethodGet, "/v1/default/banks/b1/documents/doc-1/chunks", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []struct {
			ChunkID     string `json:"chunk_id"`
			Attachments []struct {
				ID        string `json:"id"`
				Hash      string `json:"hash"`
				Kind      string `json:"kind"`
				MediaType string `json:"media_type"`
				ByteSize  int64  `json:"byte_size"`
				Filename  string `json:"filename"`
				URL       string `json:"url"`
			} `json:"attachments"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range payload.Items {
		for _, attachment := range item.Attachments {
			if attachment.ID != "att-1" {
				continue
			}
			// The shape is the reference's ChunkAttachment, not the stored
			// record: the content hash, media type, byte size and fetch URL are
			// public surface, the storage key is not.
			if attachment.Filename != "team.png" || attachment.Kind != "image" ||
				attachment.Hash != "hash-1" || attachment.MediaType != "image/png" ||
				attachment.ByteSize != 3 ||
				attachment.URL != "/v1/default/banks/b1/attachments/att-1" {
				t.Fatalf("attachment = %+v", attachment)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("chunk attachments missing: %s", rec.Body.String())
	}
}

// A document read reports the observation-scope spec it was retained under.
func TestDocumentDetailReportsObservationScopes(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	scopes := json.RawMessage(`[["work"],["home"]]`)
	if _, err := store.RetainWrite(ctx, storage.RetainRequest{
		BankID: bank,
		Items: []storage.RetainItem{{
			Content: "Alice joined the team.", DocumentID: "doc-1", ObservationScopes: scopes,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", bank)
			for _, param := range []string{"document_id", "chunk_id"} {
				if strings.Contains(pattern, "{"+param+"}") {
					r.SetPathValue(param, lastSegment(r.URL.Path))
				}
			}
			handler(w, r)
		})
	})
	rec := send(mux, http.MethodGet, "/v1/default/banks/b1/documents/doc-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload DocumentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ObservationScopes == nil {
		t.Fatalf("observation_scopes missing: %s", rec.Body.String())
	}
	groups, ok := payload.ObservationScopes.([]any)
	if !ok || len(groups) != 2 {
		t.Fatalf("observation_scopes = %#v", payload.ObservationScopes)
	}
}

// serve builds the route table over an already-seeded store, resolving the
// entity id from the path the way the real router does.
func serve(t *testing.T, store *storage.MemoryStore) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", bank)
			if strings.Contains(pattern, "{entity_id}") {
				r.SetPathValue("entity_id", lastSegment(r.URL.Path))
			}
			handler(w, r)
		})
	})
	return mux
}

// newBankStore builds a memory store that holds the fixture bank.
func newBankStore(t *testing.T) *storage.MemoryStore {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	return store
}

// seededStore retains the Alice/Google fixture document used by several tests.
func seededStore(t *testing.T) *storage.MemoryStore {
	t.Helper()
	store := newBankStore(t)
	if _, err := store.RetainWrite(context.Background(), storage.RetainRequest{
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
	return store
}

func decodeInto(t *testing.T, rec *httptest.ResponseRecorder, target any) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), target); err != nil {
		t.Fatalf("decode: %v (body = %s)", err, rec.Body.String())
	}
}

// The retired regenerate route always answers a permanent gone with the
// reference's deprecation note, never regenerating.
func TestRegenerateEntityIsGone(t *testing.T) {
	mux := newServer(t)
	rec := send(mux, http.MethodPost, "/v1/default/banks/"+bank+"/entities/anything/regenerate", "")
	if rec.Code != http.StatusGone {
		t.Fatalf("regenerate: %d, want 410; body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Detail, "deprecated") {
		t.Fatalf("detail = %q, want a deprecation note", body.Detail)
	}
}

// The memory graph honors the type, text, tag, document and chunk filters and
// defaults its limit to 1000.
func TestGraphHonorsFilters(t *testing.T) {
	store := newBankStore(t)
	ctx := context.Background()
	for _, unit := range []storage.MemoryUnit{
		{ID: "u-world", BankID: bank, Text: "alpha event", FactType: storage.TypeWorld,
			Tags: []string{"t1"}, DocumentID: "doc-a", ChunkID: "chunk-a"},
		{ID: "u-exp", BankID: bank, Text: "beta event", FactType: storage.TypeExperience,
			Tags: []string{"t2"}, DocumentID: "doc-b", ChunkID: "chunk-b"},
	} {
		if _, err := store.UpsertUnit(ctx, unit); err != nil {
			t.Fatal(err)
		}
	}
	mux := serve(t, store)
	base := "/v1/default/banks/" + bank

	var all GraphResponse
	decodeInto(t, send(mux, http.MethodGet, base+"/graph", ""), &all)
	if all.Limit != 1000 {
		t.Fatalf("default limit = %d, want 1000", all.Limit)
	}
	if len(all.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(all.Nodes))
	}

	cases := []struct {
		query string
		want  string
	}{
		{"?type=world", "u-world"},
		{"?type=experience", "u-exp"},
		{"?q=beta", "u-exp"},
		{"?tags=t1", "u-world"},
		{"?document_id=doc-b", "u-exp"},
		{"?chunk_id=chunk-a", "u-world"},
	}
	for _, tc := range cases {
		var got GraphResponse
		decodeInto(t, send(mux, http.MethodGet, base+"/graph"+tc.query, ""), &got)
		if len(got.Nodes) != 1 || got.Nodes[0].Data.ID != tc.want {
			t.Fatalf("graph%s nodes = %+v, want [%s]", tc.query, got.Nodes, tc.want)
		}
	}
}

// The entity listing defaults its limit to 100, ignores the retired q/kind
// parameters, and rejects an out-of-range page with the reference envelope.
func TestEntityListDefaultsAndValidation(t *testing.T) {
	store := seededStore(t)
	mux := serve(t, store)
	base := "/v1/default/banks/" + bank

	var all EntityListResponse
	decodeInto(t, send(mux, http.MethodGet, base+"/entities", ""), &all)
	if all.Limit != 100 {
		t.Fatalf("default limit = %d, want 100", all.Limit)
	}
	if len(all.Items) != 2 {
		t.Fatalf("entities = %d, want 2", len(all.Items))
	}

	// q and kind are not part of the contract for this route; they must not
	// narrow the result.
	var filtered EntityListResponse
	decodeInto(t, send(mux, http.MethodGet, base+"/entities?kind=ORG&q=zzz", ""), &filtered)
	if len(filtered.Items) != len(all.Items) {
		t.Fatalf("non-contract filters changed the result: %d vs %d", len(filtered.Items), len(all.Items))
	}

	for _, query := range []string{"?limit=-1", "?offset=-1", "?limit=abc"} {
		if rec := send(mux, http.MethodGet, base+"/entities"+query, ""); rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("entities%s: %d, want 422", query, rec.Code)
		}
	}
}

// The entity graph defaults its limit to 1000 and drops edges below min_count.
// The fixture's own retain produces a single co-occurring pair (count 1), so a
// min_count of 2 must filter it out.
func TestEntityGraphDefaultsAndMinCount(t *testing.T) {
	store := seededStore(t)
	mux := serve(t, store)
	base := "/v1/default/banks/" + bank

	var graph EntityGraphResponse
	decodeInto(t, send(mux, http.MethodGet, base+"/entities/graph", ""), &graph)
	if graph.Limit != 1000 {
		t.Fatalf("default limit = %d, want 1000", graph.Limit)
	}
	if len(graph.Edges) == 0 {
		t.Fatal("the fixture's cooccurrence produced no edge")
	}

	var limited EntityGraphResponse
	decodeInto(t, send(mux, http.MethodGet, base+"/entities/graph?min_count=2", ""), &limited)
	if len(limited.Edges) != 0 {
		t.Fatalf("min_count=2 edges = %d, want 0", len(limited.Edges))
	}
}

// The entity detail read honors a tag filter: an entity no matching memory
// mentions is unknown to the filtered read.
func TestGetEntityHonorsTagFilter(t *testing.T) {
	store := seededStore(t)
	mux := serve(t, store)
	base := "/v1/default/banks/" + bank
	page, err := store.ListEntities(context.Background(), storage.EntityQuery{BankID: bank, Limit: 100})
	if err != nil || len(page.Entities) == 0 {
		t.Fatalf("fixture entities: %v", err)
	}
	id := page.Entities[0].ID

	if rec := send(mux, http.MethodGet, base+"/entities/"+id+"?tags=work", ""); rec.Code != http.StatusOK {
		t.Fatalf("tagged entity: %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := send(mux, http.MethodGet, base+"/entities/"+id+"?tags=absent", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unmatched tag filter: %d, want 404", rec.Code)
	}
	// tags and tag_groups are mutually exclusive.
	compound := "%5B%7B%22tags%22%3A%5B%22x%22%5D%7D%5D"
	if rec := send(mux, http.MethodGet, base+"/entities/"+id+"?tags=work&tag_groups="+compound, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("tags plus tag_groups: %d, want 400", rec.Code)
	}
}

// The document and chunk listings default their limit to 100; q filters the
// document id (not its text), and tags_match defaults to any_strict.
func TestDocumentListDefaultsAndQuery(t *testing.T) {
	store := seededStore(t)
	mux := serve(t, store)
	base := "/v1/default/banks/" + bank

	var list DocumentListResponse
	decodeInto(t, send(mux, http.MethodGet, base+"/documents", ""), &list)
	if list.Limit != 100 {
		t.Fatalf("documents default limit = %d, want 100", list.Limit)
	}
	if len(list.Items) != 1 {
		t.Fatalf("documents = %d, want 1", len(list.Items))
	}

	// q is a substring filter on the document id.
	var byID DocumentListResponse
	decodeInto(t, send(mux, http.MethodGet, base+"/documents?q=doc", ""), &byID)
	if len(byID.Items) != 1 {
		t.Fatalf("q=doc documents = %d, want 1", len(byID.Items))
	}
	var byText DocumentListResponse
	decodeInto(t, send(mux, http.MethodGet, base+"/documents?q=Alice", ""), &byText)
	if len(byText.Items) != 0 {
		t.Fatalf("q=Alice documents = %d, want 0 (q must not match the body text)", len(byText.Items))
	}

	// tags_match defaults to any_strict: any one tag matching is enough.
	var anyMatch DocumentListResponse
	decodeInto(t, send(mux, http.MethodGet, base+"/documents?tags=work&tags=absent", ""), &anyMatch)
	if len(anyMatch.Items) != 1 {
		t.Fatalf("multi-tag documents = %d, want 1 with the any_strict default", len(anyMatch.Items))
	}

	var chunks ChunkListResponse
	decodeInto(t, send(mux, http.MethodGet, base+"/documents/doc-1/chunks", ""), &chunks)
	if chunks.Limit != 100 {
		t.Fatalf("chunks default limit = %d, want 100", chunks.Limit)
	}
	var tags TagListResponse
	decodeInto(t, send(mux, http.MethodGet, base+"/tags", ""), &tags)
	if tags.Limit != 100 {
		t.Fatalf("tags default limit = %d, want 100", tags.Limit)
	}

	if rec := send(mux, http.MethodGet, base+"/documents?limit=-1", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("documents?limit=-1: %d, want 422", rec.Code)
	}
	if rec := send(mux, http.MethodGet, base+"/documents/doc-1/chunks?limit=0", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("chunks?limit=0: %d, want 422 (ge=1)", rec.Code)
	}
}

// The tag listing is sourced from the memory units, not the documents table.
func TestTagsSourcedFromMemoryUnits(t *testing.T) {
	store := newBankStore(t)
	ctx := context.Background()
	if _, err := store.PutDocument(ctx, bank, "doc-1", storage.Document{
		OriginalText: "text", Tags: []string{"doctag"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUnit(ctx, storage.MemoryUnit{
		ID: "u1", BankID: bank, Text: "text", Tags: []string{"unittag"},
	}); err != nil {
		t.Fatal(err)
	}
	mux := serve(t, store)

	var list TagListResponse
	decodeInto(t, send(mux, http.MethodGet, "/v1/default/banks/"+bank+"/tags", ""), &list)
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].Tag != "unittag" {
		t.Fatalf("tags = %+v, want just the memory-unit tag", list)
	}
}

// A document whose raw text was not kept reads back original_text as null, the
// shape the contract declares for it, rather than an empty string.
func TestDocumentTextAbsentWhenNotStored(t *testing.T) {
	store := newBankStore(t)
	if _, err := store.PutDocument(context.Background(), bank, "d1", storage.Document{
		ContentHash: storage.ContentHash("the raw secret"),
	}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", bank)
			r.SetPathValue("document_id", "d1")
			handler(w, r)
		})
	})
	rec := send(mux, http.MethodGet, "/v1/default/banks/"+bank+"/documents/d1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if value, ok := body["original_text"]; !ok || value != nil {
		t.Fatalf("original_text must be present and null, got %v (body %s)", value, rec.Body.String())
	}
}

// reprocessor stands in for the write-side pipeline the composition root wires
// into a real read surface: it re-extracts a document from its stored text.
type reprocessor struct{ store *storage.MemoryStore }

// ReprocessDocument re-reads a document's stored text and re-runs the write.
func (r reprocessor) ReprocessDocument(ctx context.Context, bankID, documentID string) (storage.RetainOutcome, error) {
	doc, err := r.store.GetDocument(ctx, bankID, documentID)
	if err != nil {
		return storage.RetainOutcome{}, err
	}
	return r.store.RetainWrite(ctx, storage.RetainRequest{
		BankID: bankID,
		Items: []storage.RetainItem{{
			Content:        doc.OriginalText,
			DocumentID:     documentID,
			UpdateMode:     "replace",
			ForceReextract: true,
			Tags:           doc.Tags,
		}},
	})
}

// scopedHandler wraps the family's routes with the real tag-scope middleware,
// which resolves an API key through the reference's UserTagScope validator into
// the read scope the caller is confined to.
func scopedHandler(t *testing.T, store *storage.MemoryStore) http.Handler {
	t.Helper()
	host := httpx.NewExtensionHost()
	host.RegisterOperationValidator(httpx.UserTagScopeValidator{})
	return httpx.OperationValidationMiddleware(host)(serve(t, store))
}

// sendAs issues a request carrying an API key, so the middleware resolves the
// caller's tag scope from it.
func sendAs(t *testing.T, handler http.Handler, apiKey, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// graphRowTexts maps each returned table row's identifier to whether it is present.
func graphRowTexts(graph GraphResponse) map[string]bool {
	present := map[string]bool{}
	for _, row := range graph.TableRows {
		present[row.ID] = true
	}
	return present
}

// The memory graph is gated by the caller's resolved read scope: a scoped reader
// never sees the text of a memory outside its scope — neither one tagged for
// another user nor one whose source document is another user's — while an
// unscoped caller sees the whole graph.
func TestGraphHonorsTagScope(t *testing.T) {
	store := newBankStore(t)
	ctx := context.Background()
	for _, doc := range []struct {
		id   string
		tags []string
	}{
		{"kate-doc", []string{"user:kate"}},
		{"dan-doc", []string{"user:dan"}},
	} {
		if _, err := store.PutDocument(ctx, bank, doc.id, storage.Document{Tags: doc.tags}); err != nil {
			t.Fatal(err)
		}
	}
	for _, unit := range []storage.MemoryUnit{
		{ID: "u-kate", BankID: bank, Text: "Kate is interviewing at another company",
			Tags: []string{"user:kate"}, DocumentID: "kate-doc"},
		{ID: "u-dan", BankID: bank, Text: "Dan is planning a production deploy",
			Tags: []string{"user:dan"}, DocumentID: "dan-doc"},
		// A fact shared into the rule scope, but sourced from Kate's document.
		{ID: "u-rule", BankID: bank, Text: "Nobody deploys on Fridays",
			Tags: []string{"user:kate", "kind:rule"}, DocumentID: "kate-doc"},
	} {
		if _, err := store.UpsertUnit(ctx, unit); err != nil {
			t.Fatal(err)
		}
	}
	handler := scopedHandler(t, store)
	base := "/v1/default/banks/" + bank

	var admin GraphResponse
	decodeInto(t, sendAs(t, handler, "", http.MethodGet, base+"/graph"), &admin)
	if present := graphRowTexts(admin); len(present) != 3 {
		t.Fatalf("unscoped graph rows = %+v, want all three memories", admin.TableRows)
	}

	var dan GraphResponse
	decodeInto(t, sendAs(t, handler, "dan", http.MethodGet, base+"/graph"), &dan)
	present := graphRowTexts(dan)
	if present["u-kate"] {
		t.Fatalf("dan graph leaked kate's private memory: %+v", dan.TableRows)
	}
	if present["u-rule"] {
		t.Fatalf("dan graph leaked a memory whose document is kate's: %+v", dan.TableRows)
	}
	if !present["u-dan"] {
		t.Fatalf("dan's own memory is missing from his graph: %+v", dan.TableRows)
	}
	if dan.TotalUnits != len(dan.TableRows) {
		t.Fatalf("dan total_units = %d, rows = %d; they must agree", dan.TotalUnits, len(dan.TableRows))
	}
}
