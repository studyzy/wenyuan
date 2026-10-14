package app

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// seedReadSurface creates a bank with one document, one chunk, one unit and one
// entity, so every route in the family has something to report.
func seedReadSurface(t *testing.T, app *App) {
	t.Helper()
	ctx := context.Background()
	if _, err := app.Store.CreateBank(ctx, storage.Bank{BankID: "rs-bank", Name: "rs-bank"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Store.PutDocument(ctx, "rs-bank", "doc-1", storage.Document{
		OriginalText: "the deployment runs on Tuesdays",
		Tags:         []string{"ops"},
		Metadata:     map[string]any{"source": "slack"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Store.UpsertUnit(ctx, storage.MemoryUnit{
		ID: "unit-1", BankID: "rs-bank", Text: "the deployment runs on Tuesdays",
		FactType: storage.TypeWorld, DocumentID: "doc-1", ChunkID: "chunk-1",
		Tags: []string{"ops"},
	}); err != nil {
		t.Fatal(err)
	}
	entity, err := app.Store.ResolveEntity(ctx, "rs-bank", "Tuesday", storage.EntityKindRegular, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Store.UpsertUnitEntity(ctx, "unit-1", entity.ID); err != nil {
		t.Fatal(err)
	}
}

// readApp composes an in-memory application with the read-surface fixture.
func readApp(t *testing.T) *App {
	t.Helper()
	app := newParityApp(t)
	seedReadSurface(t, app)
	return app
}

// test-read-surface-routes-resolve: every route in the document, chunk, tag,
// entity and graph family answers through the served handler with the expected
// success status.
func TestReadSurfaceRoutesResolve(t *testing.T) {
	app := readApp(t)
	base := "/v1/default/banks/rs-bank"

	cases := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"documents list", http.MethodGet, base + "/documents", http.StatusOK},
		{"document detail", http.MethodGet, base + "/documents/doc-1", http.StatusOK},
		{"document update", http.MethodPatch, base + "/documents/doc-1", http.StatusOK},
		{"document chunks", http.MethodGet, base + "/documents/doc-1/chunks", http.StatusOK},
		{"document reprocess", http.MethodPost, base + "/documents/doc-1/reprocess", http.StatusOK},
		{"tags", http.MethodGet, base + "/tags", http.StatusOK},
		{"graph", http.MethodGet, base + "/graph", http.StatusOK},
		{"entities list", http.MethodGet, base + "/entities", http.StatusOK},
		{"entity graph", http.MethodGet, base + "/entities/graph", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := ""
			if tc.method == http.MethodPatch {
				body = `{"tags":["ops","updated"]}`
			}
			rec := send(app, tc.method, tc.path, body)
			if rec.Code != tc.want {
				t.Fatalf("%s %s = %d, want %d (body %s)", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// test-documents-list-shape: the listing reports the public envelope and the
// metadata that was actually stored.
func TestReadSurfaceDocumentListShape(t *testing.T) {
	app := readApp(t)
	rec := send(app, http.MethodGet, "/v1/default/banks/rs-bank/documents", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d", rec.Code)
	}
	var body struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
		Limit int              `json:"limit"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 1 || len(body.Items) != 1 {
		t.Fatalf("listing returned %d of %d", len(body.Items), body.Total)
	}
	item := body.Items[0]
	if item["id"] != "doc-1" {
		t.Fatalf("item id = %v", item["id"])
	}
	if item["memory_unit_count"] != float64(1) {
		t.Fatalf("the listing must derive the unit count, got %v", item["memory_unit_count"])
	}
	if item["text_length"] != float64(len("the deployment runs on Tuesdays")) {
		t.Fatalf("text length = %v", item["text_length"])
	}
}

// test-graph-shape: the memory graph reports nodes, edges and flat rows in the
// shape a client renders.
func TestReadSurfaceGraphShape(t *testing.T) {
	app := readApp(t)
	rec := send(app, http.MethodGet, "/v1/default/banks/rs-bank/graph", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("graph = %d", rec.Code)
	}
	var body struct {
		Nodes      []map[string]any `json:"nodes"`
		Edges      []map[string]any `json:"edges"`
		TableRows  []map[string]any `json:"table_rows"`
		TotalUnits int              `json:"total_units"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Nodes) != 1 || len(body.TableRows) != 1 {
		t.Fatalf("graph returned %d nodes and %d rows", len(body.Nodes), len(body.TableRows))
	}
	// A node carries its payload under a data key, which is the shape clients
	// walk.
	if _, ok := body.Nodes[0]["data"]; !ok {
		t.Fatalf("a graph node must carry a data payload: %v", body.Nodes[0])
	}
}

// test-entity-graph-shape: the entity graph reports its nodes and totals.
func TestReadSurfaceEntityGraphShape(t *testing.T) {
	app := readApp(t)
	rec := send(app, http.MethodGet, "/v1/default/banks/rs-bank/entities/graph", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("entity graph = %d", rec.Code)
	}
	var body struct {
		Nodes         []map[string]any `json:"nodes"`
		TotalEntities int              `json:"total_entities"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.TotalEntities != 1 || len(body.Nodes) != 1 {
		t.Fatalf("entity graph returned %d nodes of %d", len(body.Nodes), body.TotalEntities)
	}
	if _, ok := body.Nodes[0]["data"]; !ok {
		t.Fatalf("an entity node must carry a data payload: %v", body.Nodes[0])
	}
}

// test-read-surface-not-found: every member route reports not-found for an
// unknown identifier, and for an unknown bank.
func TestReadSurfaceNotFound(t *testing.T) {
	app := readApp(t)
	base := "/v1/default/banks/rs-bank"
	unknownBank := "/v1/default/banks/ghost"

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"unknown document", http.MethodGet, base + "/documents/absent"},
		{"unknown document chunks", http.MethodGet, base + "/documents/absent/chunks"},
		{"unknown entity", http.MethodGet, base + "/entities/absent"},
		{"unknown chunk", http.MethodGet, "/v1/default/chunks/absent"},
		{"unknown bank documents", http.MethodGet, unknownBank + "/documents"},
		{"unknown bank tags", http.MethodGet, unknownBank + "/tags"},
		{"unknown bank graph", http.MethodGet, unknownBank + "/graph"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := send(app, tc.method, tc.path, "")
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s %s = %d, want %d (body %s)", tc.method, tc.path, rec.Code, http.StatusNotFound, rec.Body.String())
			}
		})
	}
}

// test-entity-regenerate-refreshes: regenerating an entity returns its detail
// rather than an empty body.
func TestReadSurfaceEntityRegenerate(t *testing.T) {
	app := readApp(t)
	page := send(app, http.MethodGet, "/v1/default/banks/rs-bank/entities", "")
	var listed struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(page.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("expected one entity, got %d", len(listed.Items))
	}
	id, _ := listed.Items[0]["id"].(string)

	rec := send(app, http.MethodPost, "/v1/default/banks/rs-bank/entities/"+id+"/regenerate", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("regenerate = %d (body %s)", rec.Code, rec.Body.String())
	}
	var detail map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail["canonical_name"] != "Tuesday" {
		t.Fatalf("regenerated detail = %v", detail)
	}
}

// test-document-delete-removes-its-units: deleting a document must not leave
// retrievable evidence whose source is gone.
func TestReadSurfaceDocumentDeleteRemovesUnits(t *testing.T) {
	app := readApp(t)
	rec := send(app, http.MethodDelete, "/v1/default/banks/rs-bank/documents/doc-1", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d (body %s)", rec.Code, rec.Body.String())
	}
	// The document is gone...
	rec = send(app, http.MethodGet, "/v1/default/banks/rs-bank/documents/doc-1", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("deleted document = %d", rec.Code)
	}
	// ...and so is the evidence extracted from it.
	rec = send(app, http.MethodGet, "/v1/default/banks/rs-bank/graph", "")
	var graph struct {
		TotalUnits int `json:"total_units"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &graph); err != nil {
		t.Fatal(err)
	}
	if graph.TotalUnits != 0 {
		t.Fatalf("deleting a document left %d units retrievable", graph.TotalUnits)
	}
}
