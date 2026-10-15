package cli

// The tag list and chunk get tests.

import (
	"strings"
	"testing"
)

func TestTagList(t *testing.T) {
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"items":[{"tag":"user:x","count":3},{"tag":"topic:y","count":1}],"total":4,"limit":100,"offset":0}`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"tag", "list", "b1", "-q", "user:*", "-l", "10", "-s", "2"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if got := calls()[0]; got.Path != "/v1/default/banks/b1/tags" || got.RawQuery != "limit=10&offset=2&q=user%3A%2A" {
		t.Fatalf("request = %s?%s", got.Path, got.RawQuery)
	}
	for _, want := range []string{
		"Tags: b1",
		"user:x (3)",
		"topic:y (1)",
		"Total: 4 total",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, r.stdout)
		}
	}
}

func TestTagListOmitsEmptyQuery(t *testing.T) {
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"items":[],"total":0,"limit":100,"offset":0}`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"tag", "list", "b1"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if got := calls()[0].RawQuery; got != "limit=100&offset=0" {
		t.Fatalf("query = %q", got)
	}
	if !strings.Contains(r.stdout, "No tags found.") {
		t.Errorf("stdout:\n%s", r.stdout)
	}
}

func TestTagListMachineOutput(t *testing.T) {
	srv, _ := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"items":[{"tag":"user:x","count":3}],"total":1,"limit":100,"offset":0}`
	})
	r := runCommand(t, srv.URL, FormatJSON, []string{"tag", "list", "b1"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	for _, want := range []string{"\"tag\": \"user:x\"", "\"count\": 3", "\"total\": 1"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, r.stdout)
		}
	}
}

func TestChunkGet(t *testing.T) {
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"chunk_id":"bank1/doc1/000","document_id":"doc1","bank_id":"bank1","chunk_index":0,"chunk_text":"first paragraph\nsecond paragraph","created_at":"2024-01-15T10:00:00Z"}`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"chunk", "get", "bank1/doc1/000"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	// The chunk route is not bank-scoped and the id spans the path remainder.
	if got := calls()[0]; got.Path != "/v1/default/chunks/bank1/doc1/000" {
		t.Fatalf("path = %s", got.Path)
	}
	for _, want := range []string{
		"Chunk: bank1/doc1/000",
		"  ID: bank1/doc1/000",
		"  Index: 0",
		"  Document: doc1",
		"  Bank: bank1",
		"  Created: 2024-01-15T10:00:00Z",
		"─── Content ───",
		"first paragraph",
		"second paragraph",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, r.stdout)
		}
	}
}

func TestChunkGetMachineOutput(t *testing.T) {
	srv, _ := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"chunk_id":"c1","document_id":"d1","bank_id":"b1","chunk_index":1,"chunk_text":"text","created_at":"2024-01-15T10:00:00Z"}`
	})
	r := runCommand(t, srv.URL, FormatYAML, []string{"chunk", "get", "c1"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if !strings.Contains(r.stdout, "chunk_id: c1") || !strings.Contains(r.stdout, "chunk_index: 1") {
		t.Errorf("stdout:\n%s", r.stdout)
	}
}
