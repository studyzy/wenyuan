package explore

// Tests for the data layer: every fetch is exercised against an httptest
// server through cli.Client, asserting the endpoint, the query parameters,
// the request body and the decoded rows. No test reaches a live endpoint.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/cli"
)

// recorded is one request the test server saw.
type recorded struct {
	Method string
	URI    string // request URI including the query
	Body   string
}

// recorder collects the requests the test server receives.
type recorder struct {
	requests []recorded
}

// count returns how many requests hit the server.
func (r *recorder) count() int { return len(r.requests) }

// last returns the most recent request.
func (r *recorder) last() recorded { return r.requests[len(r.requests)-1] }

// serve starts an httptest server whose requests are recorded and answered
// by handler, plus an apiClient pointed at it.
func serve(t *testing.T, handler http.HandlerFunc) (apiClient, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.requests = append(rec.requests, recorded{Method: r.Method, URI: r.URL.RequestURI(), Body: string(body)})
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return apiClient{c: cli.NewClient(srv.URL, "")}, rec
}

// writeJSON is a handler helper that answers with a JSON body.
func writeJSON(w http.ResponseWriter, body string) {
	_, _ = w.Write([]byte(body))
}

func TestListBanksWalksPages(t *testing.T) {
	api, rec := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("offset") {
		case "0":
			writeJSON(w, `{"banks":[{"bank_id":"a","name":"Alpha"},{"bank_id":"b","name":""}],"total":3}`)
		case "2":
			writeJSON(w, `{"banks":[{"bank_id":"c","name":"Gamma"}],"total":3}`)
		default:
			t.Errorf("unexpected offset %q", r.URL.Query().Get("offset"))
		}
	})
	rows, err := api.listBanks(context.Background())
	if err != nil {
		t.Fatalf("listBanks: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	if rows[1].Name != "" {
		t.Errorf("rows[1].Name = %q, want empty (renders Unnamed)", rows[1].Name)
	}
	if rec.count() != 2 {
		t.Fatalf("got %d requests, want 2", rec.count())
	}
	// The offset is the rows fetched so far, not a fixed page stride.
	if got, want := rec.requests[1].URI, "/v1/default/banks?limit=100&offset=2"; got != want {
		t.Errorf("second request URI = %q, want %q", got, want)
	}
}

func TestListBanksStopsOnEmptyPage(t *testing.T) {
	api, rec := serve(t, func(w http.ResponseWriter, r *http.Request) {
		// The offset is the rows fetched so far: 1-row pages advance by 1.
		switch r.URL.Query().Get("offset") {
		case "0", "1":
			writeJSON(w, `{"banks":[{"bank_id":"a","name":"A"}],"total":99999}`)
		default:
			writeJSON(w, `{"banks":[],"total":99999}`)
		}
	})
	rows, err := api.listBanks(context.Background())
	if err != nil {
		t.Fatalf("listBanks: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rec.count() != 3 {
		t.Fatalf("got %d requests, want 3 (walk stops on the empty page)", rec.count())
	}
}

func TestListMemoriesParamsAndDecode(t *testing.T) {
	api, rec := serve(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"items":[{"id":"m1","text":"hello world","fact_type":"world",
			"mentioned_at":"2026-01-02T03:04:05Z","occurred_start":"2026-01-01T00:00:00Z",
			"occurred_end":"2026-01-02T00:00:00Z","proof_count":2}],"total":1}`)
	})
	rows, err := api.listMemories(context.Background(), "b1", 250)
	if err != nil {
		t.Fatalf("listMemories: %v", err)
	}
	want := "/v1/default/banks/b1/memories/list?limit=500&offset=250"
	if got := rec.last().URI; got != want {
		t.Errorf("URI = %q, want %q", got, want)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	row := rows[0]
	if row.ID != "m1" || row.FactType != "world" || row.Text != "hello world" {
		t.Errorf("decoded row = %+v", row)
	}
	if row.MentionedAt != "2026-01-02T03:04:05Z" || row.OccStart != "2026-01-01T00:00:00Z" || row.OccEnd != "2026-01-02T00:00:00Z" {
		t.Errorf("decoded timestamps = %+v", row)
	}
	// The whole list-row object is retained for the detail overlay's JSON pane.
	if !strings.Contains(string(row.Raw), "proof_count") {
		t.Errorf("raw row lost extra fields: %s", row.Raw)
	}
}

func TestListEntitiesParamsAndDecode(t *testing.T) {
	api, rec := serve(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"items":[
			{"id":"e1","canonical_name":"Alice","mention_count":7,
			 "first_seen":"2026-01-01T00:00:00Z","last_seen":null,
			 "metadata":{"type":"person"}},
			{"id":"e2","canonical_name":"Bob","mention_count":1}]}`)
	})
	rows, err := api.listEntities(context.Background(), "b1")
	if err != nil {
		t.Fatalf("listEntities: %v", err)
	}
	if got, want := rec.last().URI, "/v1/default/banks/b1/entities?limit=100"; got != want {
		t.Errorf("URI = %q, want %q", got, want)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0].Name != "Alice" || rows[0].Type != "person" || rows[0].Mentions != 7 {
		t.Errorf("rows[0] = %+v", rows[0])
	}
	if rows[0].First != "2026-01-01T00:00:00Z" || rows[0].Last != "" {
		t.Errorf("rows[0] seen timestamps = %+v", rows[0])
	}
	if rows[1].Type != "unknown" {
		t.Errorf("rows[1].Type = %q, want unknown (no metadata)", rows[1].Type)
	}
}

func TestListDocumentsParamsAndTypeFallback(t *testing.T) {
	api, rec := serve(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"items":[
			{"id":"d1","created_at":"2026-02-03T04:05:06Z","document_metadata":{"type":"note"}},
			{"id":"d2","created_at":"","content_type":"text/plain"}]}`)
	})
	rows, err := api.listDocuments(context.Background(), "b1")
	if err != nil {
		t.Fatalf("listDocuments: %v", err)
	}
	if got, want := rec.last().URI, "/v1/default/banks/b1/documents?limit=100&offset=0"; got != want {
		t.Errorf("URI = %q, want %q", got, want)
	}
	// content_type is absent from the server payload, so the TYPE column
	// falls back to document_metadata.type.
	if rows[0].Type != "note" {
		t.Errorf("rows[0].Type = %q, want note", rows[0].Type)
	}
	if rows[1].Type != "text/plain" {
		t.Errorf("rows[1].Type = %q, want text/plain (explicit field wins)", rows[1].Type)
	}
	if rows[0].Created != "2026-02-03T04:05:06Z" {
		t.Errorf("rows[0].Created = %q", rows[0].Created)
	}
}

func TestGetDocumentMapsOriginalText(t *testing.T) {
	api, rec := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("method = %s, want GET", r.Method)
		}
		writeJSON(w, `{"id":"d1","created_at":"2026-02-03T04:05:06Z","original_text":"the source text"}`)
	})
	detail, err := api.getDocument(context.Background(), "b1", "d1")
	if err != nil {
		t.Fatalf("getDocument: %v", err)
	}
	if got, want := rec.last().URI, "/v1/default/banks/b1/documents/d1"; got != want {
		t.Errorf("URI = %q, want %q", got, want)
	}
	if detail.Content != "the source text" || detail.ID != "d1" {
		t.Errorf("detail = %+v", detail)
	}
}

func TestDeleteDocumentUsesDelete(t *testing.T) {
	api, rec := serve(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"success":true}`)
	})
	if err := api.deleteDocument(context.Background(), "b1", "d1"); err != nil {
		t.Fatalf("deleteDocument: %v", err)
	}
	last := rec.last()
	if last.Method != "DELETE" {
		t.Errorf("method = %s, want DELETE", last.Method)
	}
	if last.URI != "/v1/default/banks/b1/documents/d1" {
		t.Errorf("URI = %q", last.URI)
	}
}

func TestRecallRequestBodyAndResults(t *testing.T) {
	api, rec := serve(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"results":[
			{"id":"r1","text":"found it","type":"world",
			 "occurred_start":"2026-01-01T00:00:00Z","occurred_end":null,
			 "mentioned_at":"2026-01-02T00:00:00Z"}]}`)
	})
	rows, err := api.recall(context.Background(), "b1", "find it", "mid", 4096)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	last := rec.last()
	if last.Method != "POST" || last.URI != "/v1/default/banks/b1/memories/recall" {
		t.Errorf("request = %s %s", last.Method, last.URI)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(last.Body), &body); err != nil {
		t.Fatalf("request body: %v", err)
	}
	if body["query"] != "find it" || body["budget"] != "mid" || body["tags_match"] != "any" {
		t.Errorf("body = %v", body)
	}
	if body["max_tokens"].(float64) != 4096 {
		t.Errorf("max_tokens = %v, want 4096", body["max_tokens"])
	}
	if body["trace"] != false {
		t.Errorf("trace = %v, want false", body["trace"])
	}
	// Everything else is omitted and left at the server default.
	for _, key := range []string{"types", "tags", "include", "temporal_window", "query_timestamp"} {
		if _, ok := body[key]; ok {
			t.Errorf("body carries %q; it must be omitted", key)
		}
	}
	if len(rows) != 1 || rows[0].Text != "found it" || rows[0].Type != "world" {
		t.Errorf("rows = %+v", rows)
	}
	if rows[0].OccStart != "2026-01-01T00:00:00Z" || rows[0].OccEnd != "" {
		t.Errorf("rows[0] timestamps = %+v", rows[0])
	}
}

func TestReflectBodyPinsTokens(t *testing.T) {
	api, rec := serve(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"text":"the reflection"}`)
	})
	text, err := api.reflect(context.Background(), "b1", "summarize", "high")
	if err != nil {
		t.Fatalf("reflect: %v", err)
	}
	if text != "the reflection" {
		t.Errorf("text = %q", text)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(rec.last().Body), &body); err != nil {
		t.Fatalf("request body: %v", err)
	}
	// reflect pins max_tokens at 4096 regardless of the recall control.
	if body["max_tokens"].(float64) != 4096 {
		t.Errorf("max_tokens = %v, want 4096", body["max_tokens"])
	}
	if body["query"] != "summarize" || body["budget"] != "high" {
		t.Errorf("body = %v", body)
	}
}

func TestFetchErrorsCarryStatusAndBody(t *testing.T) {
	api, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, `boom`)
	})
	_, err := api.listBanks(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	// The message feeds the footer error verbatim: it must carry both the
	// status and the response body.
	if !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %v, want status and body", err)
	}
}

func TestDatePart(t *testing.T) {
	cases := []struct {
		in, missing, want string
	}{
		{"2026-01-02T03:04:05Z", "-", "2026-01-02"},
		{"2026-01-02", "-", "2026-01-02"},
		{"", "-", "-"},
		{"", "unknown", "unknown"},
	}
	for _, c := range cases {
		if got := datePart(c.in, c.missing); got != c.want {
			t.Errorf("datePart(%q, %q) = %q, want %q", c.in, c.missing, got, c.want)
		}
	}
}
