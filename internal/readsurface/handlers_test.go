package readsurface

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	seedFixture(t, store)
	return familyRoutes(store)
}

// seedFixture stores the one document the read routes are exercised against.
func seedFixture(t *testing.T, store *storage.MemoryStore) {
	t.Helper()
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
}

// familyRoutes builds the family's route table over a store, translating path
// patterns into the path values the real router sets.
func familyRoutes(store *storage.MemoryStore) *http.ServeMux {
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

// A member route answers the RESOURCE's own not-found, never the bank's: the
// reference does no bank pre-check there, so a caller cannot tell an absent
// bank from an absent document, entity or page. Collection routes keep the bank
// check and answer about the bank.
func TestMemberRoutesAnswerResourceNotFoundOnUnknownBank(t *testing.T) {
	store := storage.NewMemoryStore("public")
	handlers := NewHandlers(store)
	handlers.Reprocess = reprocessor{store: store}
	mux := http.NewServeMux()
	handlers.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", "ghostbank")
			for _, param := range []string{"document_id", "entity_id", "chunk_id"} {
				if strings.Contains(pattern, "{"+param+"}") {
					r.SetPathValue(param, lastSegment(r.URL.Path))
				}
			}
			handler(w, r)
		})
	})
	base := "/v1/default/banks/ghostbank"
	entityID := "00000000-0000-0000-0000-000000000000"
	cases := []struct {
		method, path, body, detail string
	}{
		{http.MethodGet, base + "/documents/some-doc", "", "Document not found"},
		{http.MethodPatch, base + "/documents/some-doc", `{"tags":["a"]}`, "Document not found"},
		{http.MethodDelete, base + "/documents/some-doc", "", "Document not found"},
		{http.MethodGet, base + "/documents/some-doc/chunks", "", "Document not found"},
		{http.MethodPost, base + "/documents/some-doc/reprocess", "", "Document not found"},
		{http.MethodGet, base + "/entities/" + entityID, "", "Entity " + entityID + " not found"},
	}
	for _, tc := range cases {
		rec := send(mux, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s: %d, want 404 (body %s)", tc.method, tc.path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), tc.detail) {
			t.Fatalf("%s %s detail = %s, want %q", tc.method, tc.path, rec.Body.String(), tc.detail)
		}
	}
	for _, path := range []string{base + "/documents", base + "/tags", base + "/entities", base + "/graph"} {
		rec := send(mux, http.MethodGet, path, "")
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Bank 'ghostbank' not found") {
			t.Fatalf("collection %s: %d %s, want the bank's not-found", path, rec.Code, rec.Body.String())
		}
	}
}

// The chunk identifier is the raw path remainder: a trailing slash is part of
// the id, so it addresses no chunk rather than being trimmed to one. (A doubled
// "//" never reaches the handler: the shared router redirects an unclean path
// before dispatch, which is outside this family.)
func TestChunkIdentifierKeepsTrailingSlash(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	id := docID(t, mux)
	chunks := send(mux, http.MethodGet, base+"/documents/"+id+"/chunks", "")
	var chunkList ChunkListResponse
	if err := json.Unmarshal(chunks.Body.Bytes(), &chunkList); err != nil {
		t.Fatal(err)
	}
	chunkID := chunkList.Items[0].ChunkID
	path := "/v1/default/chunks/" + chunkID + "/"
	rec := send(mux, http.MethodGet, path, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("%s: %d, want 404 (body %s)", path, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Chunk not found") {
		t.Fatalf("%s detail = %s", path, rec.Body.String())
	}
}

// A chunk is addressed by its own identifier, which embeds the bank it belongs
// to, so the read resolves it wherever it is stored. The route used to locate a
// chunk by paging the bank listing, which made every chunk of a bank past the
// first page unreachable (404).
func TestChunkReadResolvesPastTheBankPageWindow(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	seedFixture(t, store)
	page, err := store.ListChunks(ctx, storage.ChunkQuery{BankID: bank, DocumentID: "doc-1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Chunks) == 0 {
		t.Fatal("the retained document has no chunks")
	}
	chunkID := page.Chunks[0].ChunkID

	// Enough banks created after the fixture to push it out of the first page
	// of the bank listing — the window the route used to scan.
	for i := range 500 {
		filler := fmt.Sprintf("filler-%03d", i)
		if _, err := store.CreateBank(ctx, storage.Bank{BankID: filler, Name: "Filler"}); err != nil {
			t.Fatal(err)
		}
	}
	banks, err := store.ListBanks(ctx, storage.BankQuery{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	for _, listed := range banks.Banks {
		if listed.BankID == bank {
			t.Fatal("the fixture bank is still inside the first page of the bank listing")
		}
	}

	rec := send(familyRoutes(store), http.MethodGet, "/v1/default/chunks/"+chunkID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("chunk of a bank past the page window: %d, body = %s", rec.Code, rec.Body.String())
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
	// An entity identifier is parsed as a UUID before the bank or the store is
	// consulted, so a malformed one is a bad request, not a miss.
	if rec := send(seeded, http.MethodGet, base+"/entities/nope", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed entity id: %d, want 400", rec.Code)
	}
	if rec := send(seeded, http.MethodGet, "/v1/default/chunks/", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("empty chunk id: %d, want 404", rec.Code)
	}
}

// A chunk read shows the attachments its own text references, in
// first-appearance order, regardless of which units were drawn from it.
func TestDocumentChunksCarryAttachments(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	// Two attachments referenced in reverse hash order, so a hash-ordered
	// result is distinguishable from the text order the reference returns.
	const first, second = "002222222222", "001111111111"
	content := "Shot one ⟦hs-att:" + first + "⟧ then shot two ⟦hs-att:" + second + "⟧."
	if _, err := store.RetainWrite(ctx, storage.RetainRequest{
		BankID: bank,
		Items:  []storage.RetainItem{{Content: content, DocumentID: "doc-1"}},
	}); err != nil {
		t.Fatal(err)
	}
	for hash, id := range map[string]string{"hash-2": first, "hash-1": second} {
		if err := store.PutAttachment(ctx, storage.Attachment{
			BankID: bank, DocumentID: "doc-1", AttachmentHash: hash, ShortID: id,
			MediaType: "image/png", ByteSize: 3, StorageKey: "banks/b1/doc-1/" + hash, Kind: "image",
			Filename: "team.png",
		}); err != nil {
			t.Fatal(err)
		}
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
	if len(payload.Items) != 1 {
		t.Fatalf("chunks = %d, want 1", len(payload.Items))
	}
	attachments := payload.Items[0].Attachments
	if len(attachments) != 2 {
		t.Fatalf("attachments = %+v, want both referenced", attachments)
	}
	// First-appearance order, not content-hash order.
	if attachments[0].ID != first || attachments[1].ID != second {
		t.Fatalf("attachment order = %s, %s; want %s, %s", attachments[0].ID, attachments[1].ID, first, second)
	}
	// The shape is the reference's ChunkAttachment, not the stored record: the
	// content hash, media type, byte size and fetch URL are public surface, the
	// storage key is not.
	got := attachments[0]
	if got.Filename != "team.png" || got.Kind != "image" ||
		got.Hash != "hash-2" || got.MediaType != "image/png" ||
		got.ByteSize != 3 ||
		got.URL != "/v1/default/banks/b1/attachments/"+first {
		t.Fatalf("attachment = %+v", got)
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

// sendAsBody is sendAs with a JSON body.
func sendAsBody(t *testing.T, handler http.Handler, apiKey, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
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
	if dan.TotalUnits != 2 {
		t.Fatalf("dan total_units = %d, want the scoped match count (2)", dan.TotalUnits)
	}
	if len(dan.TableRows) != 1 {
		t.Fatalf("dan graph rows = %d, want only his own (u-rule's document is kate's)", len(dan.TableRows))
	}
}

// asHTTPError names the storage sentinels a route can hit — including the
// write-unavailable condition a document mutation can meet — and treats
// anything it does not name as a server fault.
func TestReadSurfaceErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		expect int
	}{
		{storage.ErrBankNotFound, http.StatusNotFound},
		{storage.ErrDocumentNotFound, http.StatusNotFound},
		{storage.ErrChunkNotFound, http.StatusNotFound},
		{storage.ErrEntityNotFound, http.StatusNotFound},
		{storage.ErrWritesUnavailable, http.StatusServiceUnavailable},
		{errors.New("storage exploded"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		if got := asHTTPError(tc.err); got == nil || got.Status != tc.expect {
			t.Fatalf("error %v mapped to %+v, want status %d", tc.err, got, tc.expect)
		}
	}
	if asHTTPError(nil) != nil {
		t.Fatal("a nil error must map to nil")
	}
}

// The document listing orders by last write and filters on the window axis the
// reference selects: updated_at by default, with rows outside the axis excluded
// and the upper bound exclusive.
func TestDocumentListWindowAndOrder(t *testing.T) {
	store := newBankStore(t)
	ctx := context.Background()
	early := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	later := time.Date(2024, 2, 15, 0, 0, 0, 0, time.UTC)
	for _, doc := range []struct {
		id      string
		created time.Time
		updated time.Time
		tags    []string
	}{
		{"old-but-rewritten", early, later, []string{"team-a"}},
		{"new-but-stale", later, early, nil},
	} {
		if _, err := store.PutDocument(ctx, bank, doc.id, storage.Document{
			CreatedAt: doc.created, UpdatedAt: doc.updated, Tags: doc.tags,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mux := serve(t, store)
	base := "/v1/default/banks/" + bank

	// Most recently written first, regardless of creation order.
	var list DocumentListResponse
	decodeInto(t, send(mux, http.MethodGet, base+"/documents", ""), &list)
	if len(list.Items) != 2 || list.Items[0].ID != "old-but-rewritten" {
		t.Fatalf("default order = %+v, want the last-written document first", list.Items)
	}

	// A window filters and orders on updated_at by default, upper bound exclusive.
	var windowed DocumentListResponse
	decodeInto(t, send(mux, http.MethodGet,
		base+"/documents?start_date=2024-02-01T00:00:00Z&end_date=2024-03-01T00:00:00Z", ""), &windowed)
	if len(windowed.Items) != 1 || windowed.Items[0].ID != "old-but-rewritten" {
		t.Fatalf("updated_at window = %+v, want just the rewritten document", windowed.Items)
	}

	// The any mode includes an untagged document.
	var anyTags DocumentListResponse
	decodeInto(t, send(mux, http.MethodGet, base+"/documents?tags=team-a&tags_match=any", ""), &anyTags)
	if len(anyTags.Items) != 2 {
		t.Fatalf("tags_match=any items = %d, want the tagged and the untagged document", len(anyTags.Items))
	}

	// An unknown axis is rejected, not silently answered on another column.
	if rec := send(mux, http.MethodGet, base+"/documents?time_field=bogus", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("time_field=bogus: %d, want 422", rec.Code)
	}
	// An inverted window is a bad request.
	if rec := send(mux, http.MethodGet,
		base+"/documents?start_date=2024-02-01T00:00:00Z&end_date=2024-01-01T00:00:00Z", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("inverted window: %d, want 400", rec.Code)
	}
	// A naive timestamp is read as UTC rather than rejected.
	if rec := send(mux, http.MethodGet, base+"/documents?start_date=2024-01-15T10:30:00", ""); rec.Code != http.StatusOK {
		t.Fatalf("naive start_date: %d, want 200", rec.Code)
	}
}

// The tag listing honours the source selector and applies the SQL ILIKE the
// reference does: anchored, with '*' the wildcard.
func TestTagsSourceAndPattern(t *testing.T) {
	store := newBankStore(t)
	ctx := context.Background()
	if _, err := store.UpsertUnit(ctx, storage.MemoryUnit{
		ID: "u1", BankID: bank, Text: "t", Tags: []string{"work", "workflow"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutMentalModel(ctx, storage.MentalModel{
		ID: "m1", BankID: bank, Subtype: storage.SubtypePinned, Name: "Model",
		Tags: []string{"alpha"},
	}); err != nil {
		t.Fatal(err)
	}
	mux := serve(t, store)
	base := "/v1/default/banks/" + bank

	var memoryTags TagListResponse
	decodeInto(t, send(mux, http.MethodGet, base+"/tags", ""), &memoryTags)
	if memoryTags.Total != 2 {
		t.Fatalf("memory tags total = %d, want 2", memoryTags.Total)
	}

	var modelTags TagListResponse
	decodeInto(t, send(mux, http.MethodGet, base+"/tags?source=mental_models", ""), &modelTags)
	if modelTags.Total != 1 || modelTags.Items[0].Tag != "alpha" {
		t.Fatalf("mental-model tags = %+v, want just alpha", modelTags)
	}

	// q is anchored: 'work' does not match 'workflow'.
	var anchored TagListResponse
	decodeInto(t, send(mux, http.MethodGet, base+"/tags?q=work", ""), &anchored)
	if anchored.Total != 1 || anchored.Items[0].Tag != "work" {
		t.Fatalf("q=work = %+v, want the anchored match only", anchored)
	}
	var wildcard TagListResponse
	decodeInto(t, send(mux, http.MethodGet, base+"/tags?q=work*", ""), &wildcard)
	if wildcard.Total != 2 {
		t.Fatalf("q=work* total = %d, want 2", wildcard.Total)
	}

	if rec := send(mux, http.MethodGet, base+"/tags?source=bogus", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("source=bogus: %d, want 422", rec.Code)
	}
}

// A document update requires the tags field: an empty or metadata-only body is
// rejected before the document is consulted.
func TestUpdateDocumentRequiresTags(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	id := docID(t, mux)

	for _, body := range []string{`{}`, `{"tags": null}`, `{"document_metadata": {"k": "v"}}`} {
		rec := send(mux, http.MethodPatch, base+"/documents/"+id, body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("PATCH %s: %d, want 422 (body %s)", body, rec.Code, rec.Body.String())
		}
		var failure struct {
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &failure); err != nil {
			t.Fatal(err)
		}
		if failure.Detail != "At least one field (tags) must be provided" {
			t.Fatalf("detail = %q", failure.Detail)
		}
	}
	// An empty array is valid and clears the tags.
	if rec := send(mux, http.MethodPatch, base+"/documents/"+id, `{"tags": []}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH empty tags: %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	// A missing document is rejected as unprocessable before it is looked up.
	if rec := send(mux, http.MethodPatch, base+"/documents/missing", `{}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing document with empty body: %d, want 422", rec.Code)
	}
}

// The entity detail carries an always-empty observations list and an always
// present metadata object, and validates the identifier and tag mode.
func TestGetEntityShapeAndValidation(t *testing.T) {
	store := seededStore(t)
	mux := serve(t, store)
	base := "/v1/default/banks/" + bank
	page, err := store.ListEntities(context.Background(), storage.EntityQuery{BankID: bank, Limit: 100})
	if err != nil || len(page.Entities) == 0 {
		t.Fatalf("fixture entities: %v", err)
	}
	id := page.Entities[0].ID

	rec := send(mux, http.MethodGet, base+"/entities/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("entity detail: %d, body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	observations, ok := body["observations"].([]any)
	if !ok || len(observations) != 0 {
		t.Fatalf("observations = %v, want an empty list", body["observations"])
	}
	metadata, ok := body["metadata"].(map[string]any)
	if !ok || metadata == nil {
		t.Fatalf("metadata = %v, want an object", body["metadata"])
	}

	if rec := send(mux, http.MethodGet, base+"/entities/not-a-uuid", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad uuid: %d, want 400", rec.Code)
	}
	if rec := send(mux, http.MethodGet, base+"/entities/"+id+"?tags_match=bogus", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad tags_match: %d, want 422", rec.Code)
	}
	if rec := send(mux, http.MethodGet, base+"/entities?tags_match=bogus", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("list bad tags_match: %d, want 422", rec.Code)
	}
	if rec := send(mux, http.MethodGet, base+"/entities/graph?tags_match=bogus", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("graph bad tags_match: %d, want 422", rec.Code)
	}
}

// The memory graph renders the reference's node and edge presentation and
// derives entity edges for units that share an entity without a stored link.
func TestGraphPresentationAndDerivedEdges(t *testing.T) {
	store := newBankStore(t)
	ctx := context.Background()
	long := strings.Repeat("x", 40)
	// The node order — and so the direction of the derived edge — follows
	// mentioned_at DESC, so pin it: the store stamps a missing mention itself,
	// which would make the direction depend on insert timing.
	earlier := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	later := earlier.Add(time.Hour)
	for _, unit := range []storage.MemoryUnit{
		{ID: "11111111-1111-1111-1111-111111111111", BankID: bank, Text: long, MentionedAt: &earlier},
		{ID: "22222222-2222-2222-2222-222222222222", BankID: bank, Text: "short", MentionedAt: &later},
	} {
		if _, err := store.UpsertUnit(ctx, unit); err != nil {
			t.Fatal(err)
		}
		if err := store.SetUnitEntities(ctx, bank, unit.ID, []string{"Alice"}, false); err != nil {
			t.Fatal(err)
		}
	}
	mux := serve(t, store)
	rec := send(mux, http.MethodGet, "/v1/default/banks/"+bank+"/graph", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("graph: %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Nodes []struct {
			Data struct {
				ID       string `json:"id"`
				Label    string `json:"label"`
				Date     string `json:"date"`
				Context  string `json:"context"`
				Entities string `json:"entities"`
				Color    string `json:"color"`
			} `json:"data"`
		} `json:"nodes"`
		Edges []struct {
			Data struct {
				ID         string  `json:"id"`
				LinkType   string  `json:"linkType"`
				EntityName string  `json:"entityName"`
				Color      string  `json:"color"`
				LineStyle  string  `json:"lineStyle"`
				Weight     float64 `json:"weight"`
			} `json:"data"`
		} `json:"edges"`
		TableRows []struct {
			Context    string `json:"context"`
			Date       string `json:"date"`
			DocumentID any    `json:"document_id"`
			ProofCount any    `json:"proof_count"`
		} `json:"table_rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Nodes) != 2 {
		t.Fatalf("nodes = %+v", body.Nodes)
	}
	var longNode struct {
		Label, Date, Context, Entities, Color string
	}
	for _, node := range body.Nodes {
		if node.Data.ID == "11111111-1111-1111-1111-111111111111" {
			longNode = struct{ Label, Date, Context, Entities, Color string }{
				node.Data.Label, node.Data.Date, node.Data.Context, node.Data.Entities, node.Data.Color,
			}
		}
	}
	if longNode.Label != strings.Repeat("x", 30)+"..." {
		t.Fatalf("label = %q, want a truncated label", longNode.Label)
	}
	if longNode.Date != "" || longNode.Context != "" {
		t.Fatalf("date/context = %q/%q, want empty strings", longNode.Date, longNode.Context)
	}
	if longNode.Entities != "Alice" || longNode.Color != "#90caf9" {
		t.Fatalf("entities/color = %q/%q", longNode.Entities, longNode.Color)
	}

	// The two units share an entity, so an entity edge is derived even though no
	// link row exists.
	found := false
	for _, edge := range body.Edges {
		if edge.Data.LinkType != "entity" {
			continue
		}
		found = true
		if edge.Data.ID != "22222222-2222-2222-2222-222222222222-11111111-1111-1111-1111-111111111111-entity" {
			t.Fatalf("edge id = %q", edge.Data.ID)
		}
		if edge.Data.EntityName != "Alice" || edge.Data.Color != "#ffd700" || edge.Data.LineStyle != "solid" {
			t.Fatalf("entity edge = %+v", edge.Data)
		}
	}
	if !found {
		t.Fatalf("no derived entity edge in %+v", body.Edges)
	}

	if len(body.TableRows) == 0 {
		t.Fatal("no table rows")
	}
	row := body.TableRows[0]
	if row.Context != "N/A" || row.Date != "N/A" {
		t.Fatalf("row context/date = %q/%q, want N/A", row.Context, row.Date)
	}
	if row.DocumentID != nil {
		t.Fatalf("row document_id = %v, want null", row.DocumentID)
	}
	if row.ProofCount != nil {
		t.Fatalf("row proof_count = %v, want null for a zero count", row.ProofCount)
	}
}

// A document detail counts the observations built over its units, which carry
// no document id of their own, and reports the metadata the document was
// retained with.
func TestDocumentDetailObservationCountAndMetadata(t *testing.T) {
	store := newBankStore(t)
	ctx := context.Background()
	if _, err := store.PutDocument(ctx, bank, "doc-1", storage.Document{
		OriginalText: "Alice works at Google.",
		RetainParams: map[string]any{"metadata": map[string]any{"source": "slack"}},
		Metadata:     map[string]any{"source": "slack"},
	}); err != nil {
		t.Fatal(err)
	}
	source := storage.MemoryUnit{ID: "src-1", BankID: bank, Text: "Alice works at Google.",
		FactType: storage.TypeExperience, DocumentID: "doc-1"}
	if _, err := store.UpsertUnit(ctx, source); err != nil {
		t.Fatal(err)
	}
	observation := storage.MemoryUnit{ID: "obs-1", BankID: bank, Text: "Alice works at a search company.",
		FactType: storage.TypeObservation, SourceMemoryIDs: []string{"src-1"}}
	if _, err := store.UpsertUnit(ctx, observation); err != nil {
		t.Fatal(err)
	}
	mux := serve(t, store)
	rec := send(mux, http.MethodGet, "/v1/default/banks/"+bank+"/documents/doc-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("document detail: %d, body = %s", rec.Code, rec.Body.String())
	}
	var doc DocumentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.NodesByFactType["observation"] != 1 {
		t.Fatalf("observation count = %d, want 1", doc.NodesByFactType["observation"])
	}
	if doc.NodesByFactType["experience"] != 1 {
		t.Fatalf("experience count = %d, want 1", doc.NodesByFactType["experience"])
	}
	if metadata, ok := doc.Metadata.(map[string]any); !ok || metadata["source"] != "slack" {
		t.Fatalf("document_metadata = %v, want the retain-time metadata", doc.Metadata)
	}
}

// The document PATCH is gated by the caller's resolved scope: a document
// outside its read scope reads as the route's not-found, a readable document
// the caller may not write — by its current tags or by the tags the write would
// give it — is refused with 403, and an unscoped caller is unaffected.
func TestUpdateDocumentHonorsTagScope(t *testing.T) {
	store := newBankStore(t)
	ctx := context.Background()
	for _, doc := range []struct {
		id   string
		tags []string
	}{
		{"kate-doc", []string{"user:kate"}},
		{"rule-doc", []string{"kind:rule"}},
		{"dan-doc", []string{"user:dan"}},
	} {
		if _, err := store.PutDocument(ctx, bank, doc.id, storage.Document{Tags: doc.tags}); err != nil {
			t.Fatal(err)
		}
	}
	handler := scopedHandler(t, store)
	base := "/v1/default/banks/" + bank + "/documents"

	// dan cannot read kate's document: the scope hides it behind the 404, which
	// echoes the id the way _require_writable does.
	rec := sendAsBody(t, handler, "dan", http.MethodPatch, base+"/kate-doc", `{"tags":["user:dan"]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("dan patching kate's document = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Document 'kate-doc' not found") {
		t.Fatalf("404 detail = %s", rec.Body.String())
	}
	// dan can read the shared rule document but may not retag it, since his
	// write scope (user:dan) does not cover its current tags.
	rec = sendAsBody(t, handler, "dan", http.MethodPatch, base+"/rule-doc", `{"tags":["user:dan"]}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("dan retagging a rule document = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "You can't write document 'rule-doc' tagged kind:rule") {
		t.Fatalf("403 detail = %s", rec.Body.String())
	}
	// A document dan owns but would give a tag outside his write scope is
	// refused on the new tags.
	rec = sendAsBody(t, handler, "dan", http.MethodPatch, base+"/dan-doc", `{"tags":["user:kate"]}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("dan writing a foreign tag = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "You can't write an item tagged user:kate") {
		t.Fatalf("403 detail = %s", rec.Body.String())
	}
	// An unscoped caller (an administrator) retags any document unchanged.
	rec = sendAsBody(t, handler, "", http.MethodPatch, base+"/kate-doc", `{"tags":["user:dan"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("unscoped retag = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// A document's retain parameters echo exactly what the write recorded: the
// supplied context, and no empty defaults for the fields the caller left out.
// A write that named nothing replayable leaves the field null, the value the
// reference renders for an empty recorded object.
func TestDocumentRetainParamsEchoTheWrite(t *testing.T) {
	store := newBankStore(t)
	ctx := context.Background()
	write := func(documentID, context string) {
		t.Helper()
		if _, err := store.RetainWrite(ctx, storage.RetainRequest{
			BankID: bank,
			Items: []storage.RetainItem{{
				Content:    "Alice works at Google.",
				DocumentID: documentID,
				Context:    context,
				UpdateMode: "replace",
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	write("doc-plain", "")
	write("doc-context", "standup notes")
	mux := serve(t, store)

	rec := send(mux, http.MethodGet, "/v1/default/banks/"+bank+"/documents/doc-plain", "")
	var plain DocumentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &plain); err != nil {
		t.Fatal(err)
	}
	if plain.RetainParams != nil {
		t.Fatalf("retain_params = %v, want null", plain.RetainParams)
	}

	rec = send(mux, http.MethodGet, "/v1/default/banks/"+bank+"/documents/doc-context", "")
	var withContext DocumentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &withContext); err != nil {
		t.Fatal(err)
	}
	if len(withContext.RetainParams) != 1 || withContext.RetainParams["context"] != "standup notes" {
		t.Fatalf("retain_params = %v, want only context", withContext.RetainParams)
	}

	// The listing renders the same value.
	var list DocumentListResponse
	rec = send(mux, http.MethodGet, "/v1/default/banks/"+bank+"/documents", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, item := range list.Items {
		switch item.ID {
		case "doc-plain":
			if item.RetainParams != nil {
				t.Fatalf("list retain_params for doc-plain = %v, want null", item.RetainParams)
			}
		case "doc-context":
			if len(item.RetainParams) != 1 || item.RetainParams["context"] != "standup notes" {
				t.Fatalf("list retain_params for doc-context = %v, want only context", item.RetainParams)
			}
		}
	}
}

// A document retag that invalidates observations re-consolidates the bank only
// when auto consolidation is on — the deployment default, unless the bank turns
// it off. Without the gate the reference's behaviour (no pass submitted) is not
// reproduced.
func TestDocumentRetagConsolidationHonoursAutoConsolidation(t *testing.T) {
	build := func(t *testing.T, bankOverride *bool) (*http.ServeMux, storage.Store) {
		t.Helper()
		store := newBankStore(t)
		ctx := context.Background()
		if bankOverride != nil {
			config := map[string]any{"enable_auto_consolidation": *bankOverride}
			if _, err := store.UpdateBank(ctx, bank, storage.BankPatch{Config: &config}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.PutDocument(ctx, bank, "doc-1", storage.Document{
			OriginalText: "Alice works at Google.", Tags: []string{"work"},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.UpsertUnit(ctx, storage.MemoryUnit{
			ID: "src-1", BankID: bank, Text: "Alice works at Google.",
			FactType: storage.TypeExperience, DocumentID: "doc-1", Tags: []string{"work"},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateObservationInScope(ctx, bank, "Alice works at a search company.", []string{"src-1"}, []string{"work"}); err != nil {
			t.Fatal(err)
		}
		handler := NewHandlers(store)
		handler.AutoConsolidationEnabled = true
		mux := http.NewServeMux()
		handler.BindRoutes(func(method, pattern string, h http.HandlerFunc) {
			mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
				r.SetPathValue("bank_id", bank)
				h(w, r)
			})
		})
		return mux, store
	}
	consolidationOps := func(t *testing.T, store storage.Store) int {
		t.Helper()
		page, err := store.ListOperations(context.Background(), storage.OperationQuery{
			BankID: bank, Type: storage.OperationTypeConsolidate,
		})
		if err != nil {
			t.Fatal(err)
		}
		return len(page.Operations)
	}
	retag := func(t *testing.T, mux *http.ServeMux) {
		t.Helper()
		rec := send(mux, http.MethodPatch, "/v1/default/banks/"+bank+"/documents/doc-1", `{"tags":["other"]}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("retag = %d, want 200: %s", rec.Code, rec.Body.String())
		}
	}

	// The deployment default is on, so the retag submits a pass.
	mux, store := build(t, nil)
	retag(t, mux)
	if got := consolidationOps(t, store); got != 1 {
		t.Fatalf("consolidation ops with auto-consolidation on = %d, want 1", got)
	}

	// The bank turns it off: the observation is still invalidated, but no pass
	// is submitted.
	off := false
	mux, store = build(t, &off)
	retag(t, mux)
	if got := consolidationOps(t, store); got != 0 {
		t.Fatalf("consolidation ops with auto-consolidation off = %d, want 0", got)
	}
}

// A delete is a by-id write, so it carries the same read/write tag-scope gate a
// retag does: a document outside the caller's read scope reads as not-found
// (the id echoed, as _require_writable does), and a readable document whose
// tags the caller may not write is refused.
func TestDeleteDocumentHonorsTagScope(t *testing.T) {
	store := newBankStore(t)
	ctx := context.Background()
	for _, doc := range []struct {
		id   string
		tags []string
	}{
		{"kate-doc", []string{"user:kate"}},
		{"rule-doc", []string{"kind:rule"}},
		{"dan-doc", []string{"user:dan"}},
	} {
		if _, err := store.PutDocument(ctx, bank, doc.id, storage.Document{Tags: doc.tags}); err != nil {
			t.Fatal(err)
		}
	}
	handler := scopedHandler(t, store)
	base := "/v1/default/banks/" + bank + "/documents"

	// dan cannot read kate's document, so it answers not-found and nothing is
	// deleted.
	rec := sendAs(t, handler, "dan", http.MethodDelete, base+"/kate-doc")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Document 'kate-doc' not found") {
		t.Fatalf("dan deleting kate's document = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := store.GetDocument(ctx, bank, "kate-doc"); err != nil {
		t.Fatalf("kate's document was deleted: %v", err)
	}

	// A readable document dan may not write is refused with the 403.
	rec = sendAs(t, handler, "dan", http.MethodDelete, base+"/rule-doc")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "You can't write document 'rule-doc' tagged kind:rule") {
		t.Fatalf("dan deleting a rule document = %d: %s", rec.Code, rec.Body.String())
	}

	// A document dan owns deletes.
	rec = sendAs(t, handler, "dan", http.MethodDelete, base+"/dan-doc")
	if rec.Code != http.StatusOK {
		t.Fatalf("dan deleting his document = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// document_metadata is served as it stands: when a document's retain_params
// carry a metadata member that is present but not an object, the detail and the
// listing both return that value rather than reporting no metadata. The
// reference reads the member through a truth test, so a member that is present
// but empty ({} or []) reads as no metadata on either route.
func TestDocumentMetadataServesNonObjectValue(t *testing.T) {
	ctx := context.Background()
	store := newBankStore(t)
	cases := []struct {
		id     string
		retain map[string]any
		want   string // the JSON the route must return for document_metadata
	}{
		{"meta-string", map[string]any{"metadata": "slack"}, `"slack"`},
		{"meta-array", map[string]any{"metadata": []any{"a"}}, `["a"]`},
		{"meta-empty-object", map[string]any{"metadata": map[string]any{}}, `null`},
		{"meta-object", map[string]any{"metadata": map[string]any{"source": "slack"}}, `{"source":"slack"}`},
	}
	for _, tc := range cases {
		if _, err := store.PutDocument(ctx, bank, tc.id, storage.Document{OriginalText: "body", RetainParams: tc.retain}); err != nil {
			t.Fatal(err)
		}
	}
	mux := serve(t, store)
	for _, tc := range cases {
		rec := send(mux, http.MethodGet, "/v1/default/banks/"+bank+"/documents/"+tc.id, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s detail: %d, body = %s", tc.id, rec.Code, rec.Body.String())
		}
		var doc DocumentResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(doc.Metadata)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tc.want {
			t.Fatalf("%s detail metadata = %s, want %s", tc.id, got, tc.want)
		}

		list := send(mux, http.MethodGet, "/v1/default/banks/"+bank+"/documents?q="+tc.id, "")
		if list.Code != http.StatusOK {
			t.Fatalf("%s list: %d, body = %s", tc.id, list.Code, list.Body.String())
		}
		var page DocumentListResponse
		if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("%s list returned %d items, want 1", tc.id, len(page.Items))
		}
		listed, err := json.Marshal(page.Items[0].Metadata)
		if err != nil {
			t.Fatal(err)
		}
		if string(listed) != tc.want {
			t.Fatalf("%s listed metadata = %s, want %s", tc.id, listed, tc.want)
		}
	}
}
