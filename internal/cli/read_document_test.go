package cli

// The document family tests: plain and date-grouped listing, subtree member
// routes, the no-confirmation delete, and the --tags "" clearing semantics.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func documentItem(id, createdAt string, units int) string {
	return fmt.Sprintf(`{"id":%q,"bank_id":"b1","content_hash":"h","created_at":%q,"updated_at":%q,"text_length":5,"memory_unit_count":%d,"retain_params":{},"document_metadata":{},"tags":["x"]}`,
		id, createdAt, createdAt, units)
}

func TestDocumentListPlain(t *testing.T) {
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, fmt.Sprintf(`{"items":[%s,%s],"total":2,"limit":100,"offset":0}`,
			documentItem("d1", "2024-01-15T10:00:00Z", 3), documentItem("d2", "2024-01-16T10:00:00Z", 0))
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"document", "list", "b1", "-q", "find", "-l", "50", "-s", "5"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	call := calls()[0]
	if call.Path != "/v1/default/banks/b1/documents" {
		t.Fatalf("path = %s", call.Path)
	}
	if call.RawQuery != "limit=50&offset=5&q=find" {
		t.Fatalf("query = %q", call.RawQuery)
	}
	for _, want := range []string{
		"Documents for bank 'b1' (total: 2)",
		"Document ID: d1",
		"Created: 2024-01-15T10:00:00Z",
		"Updated: 2024-01-15T10:00:00Z",
		"Text Length: 5",
		"Memory Units: 3",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, r.stdout)
		}
	}
}

func TestDocumentListOmitsEmptyQuery(t *testing.T) {
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"items":[],"total":0,"limit":100,"offset":0}`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"document", "list", "b1"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if got := calls()[0].RawQuery; got != "limit=100&offset=0" {
		t.Fatalf("query = %q, want no q parameter", got)
	}
}

func TestDocumentListDateMode(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	// Two pages exercise the client-side pagination loop.
	pages := map[string]string{
		"limit=500&offset=0": fmt.Sprintf(`{"items":[%s,%s],"total":3,"limit":500,"offset":0}`,
			documentItem("old", yesterday+"T05:00:00Z", 1), documentItem("new1", today+"T05:00:00Z", 2)),
		"limit=500&offset=500": fmt.Sprintf(`{"items":[%s],"total":3,"limit":500,"offset":500}`,
			documentItem("new2", today+"T09:00:00Z", 4)),
	}
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		if call.Path != "/v1/default/banks/b1/documents" {
			t.Errorf("unexpected path %s", call.Path)
		}
		if body, ok := pages[call.RawQuery]; ok {
			return 200, body
		}
		t.Errorf("unexpected query %q", call.RawQuery)
		return 200, `{"items":[],"total":0,"limit":500,"offset":0}`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"document", "list", "b1", "--date", "all"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if got := calls(); len(got) != 2 {
		t.Fatalf("page calls = %d, want 2", len(got))
	}
	if !strings.Contains(r.stdout, "Documents for bank 'b1' (filter: all dates, showing: 3)") {
		t.Errorf("stdout:\n%s", r.stdout)
	}
	// Newest date group first; per-document lines carry the unit counts.
	todayIdx := strings.Index(r.stdout, today+" (2 documents)")
	yesterdayIdx := strings.Index(r.stdout, yesterday+" (1 documents)")
	if todayIdx < 0 || yesterdayIdx < 0 || todayIdx > yesterdayIdx {
		t.Errorf("groups not newest-first:\n%s", r.stdout)
	}
	for _, want := range []string{"    - new1 (2 memories)", "    - new2 (4 memories)", "    - old (1 memories)"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, r.stdout)
		}
	}
}

func TestDocumentListDateFiltersByPrefix(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, fmt.Sprintf(`{"items":[%s,%s],"total":2,"limit":500,"offset":0}`,
			documentItem("old", "2020-01-01T05:00:00Z", 1), documentItem("new", today+"T05:00:00Z", 2))
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"document", "list", "b1", "--date", "today"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if got := calls()[0].RawQuery; got != "limit=500&offset=0" {
		t.Fatalf("date mode must page without q: %q", got)
	}
	if !strings.Contains(r.stdout, "showing: 1") || !strings.Contains(r.stdout, "- new (2 memories)") {
		t.Errorf("stdout:\n%s", r.stdout)
	}
	if strings.Contains(r.stdout, "old") {
		t.Errorf("unfiltered document leaked:\n%s", r.stdout)
	}
}

func TestDocumentListDateInvalid(t *testing.T) {
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		t.Error("an invalid date must fail before any request")
		return 200, `{}`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"document", "list", "b1", "-d", "bogus"}, nil)
	if r.err == nil || !strings.HasPrefix(r.err.Error(), "invalid date format 'bogus': ") ||
		!strings.HasSuffix(r.err.Error(), ". use YYYY-MM-DD, 'yesterday', 'today', or 'all'") {
		t.Fatalf("err = %v", r.err)
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("requests = %d", len(got))
	}
}

func TestDocumentListEmptyDateValueIsInvalid(t *testing.T) {
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		t.Error("an empty --date must fail before any request")
		return 200, `{}`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"document", "list", "b1", "--date", ""}, nil)
	if r.err == nil || !strings.HasPrefix(r.err.Error(), "invalid date format '': ") {
		t.Fatalf("err = %v", r.err)
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("requests = %d", len(got))
	}
}

func TestDocumentListDateMachineOutputIsFlatArray(t *testing.T) {
	srv, _ := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, fmt.Sprintf(`{"items":[%s],"total":1,"limit":500,"offset":0}`,
			documentItem("d1", "2024-01-15T10:00:00Z", 3))
	})
	r := runCommand(t, srv.URL, FormatJSON, []string{"document", "list", "b1", "--date", "2024-01-15"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	trimmed := strings.TrimSpace(r.stdout)
	if !strings.HasPrefix(trimmed, "[") || !strings.HasSuffix(trimmed, "]") {
		t.Fatalf("json output is not a flat array:\n%s", r.stdout)
	}
	var docs []map[string]any
	if err := json.Unmarshal([]byte(trimmed), &docs); err != nil || len(docs) != 1 || docs[0]["id"] != "d1" {
		t.Fatalf("docs = %v (%v)", docs, err)
	}
}

func TestDocumentGetSubtreeID(t *testing.T) {
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"id":"dir/sub/doc1","bank_id":"b1","original_text":"the stored text","content_hash":"h","created_at":"2024-01-15T10:00:00Z","updated_at":"2024-01-16T10:00:00Z","memory_unit_count":2,"tags":[]}`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"document", "get", "b1", "dir/sub/doc1"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if got := calls()[0]; got.Path != "/v1/default/banks/b1/documents/dir/sub/doc1" {
		t.Fatalf("path = %s, the id must span the subtree", got.Path)
	}
	for _, want := range []string{
		"Document: dir/sub/doc1",
		"  Bank ID: b1",
		"  Created: 2024-01-15T10:00:00Z",
		"  Updated: 2024-01-16T10:00:00Z",
		"  Memory Units: 2",
		"  Text:",
		"the stored text",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, r.stdout)
		}
	}
}

func TestDocumentGetNullText(t *testing.T) {
	srv, _ := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"id":"d1","bank_id":"b1","original_text":null,"created_at":"c","updated_at":"u","memory_unit_count":0,"tags":[]}`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"document", "get", "b1", "d1"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if !strings.Contains(r.stdout, "(not stored)") {
		t.Errorf("stdout:\n%s", r.stdout)
	}
}

func TestDocumentDelete(t *testing.T) {
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		if call.Method != "DELETE" || call.Path != "/v1/default/banks/b1/documents/d1" {
			t.Errorf("request = %s %s", call.Method, call.Path)
		}
		return 200, `{"success":true,"message":"deleted","document_id":"d1","memory_units_deleted":7}`
	})
	// No stdin: the command must not prompt.
	r := runCommand(t, srv.URL, FormatPretty, []string{"document", "delete", "b1", "d1"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if got := calls(); len(got) != 1 {
		t.Fatalf("requests = %d, want the unconditional delete", len(got))
	}
	if !strings.Contains(r.stdout, "Document deleted successfully") {
		t.Errorf("stdout:\n%s", r.stdout)
	}
}

func TestDocumentDeleteFailureBody(t *testing.T) {
	srv, _ := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"success":false,"message":"nope","document_id":"d1","memory_units_deleted":0}`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"document", "delete", "b1", "d1"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if !strings.Contains(r.stderr, "Failed to delete document") {
		t.Errorf("stderr:\n%s", r.stderr)
	}
}

func TestDocumentDeleteMachineOutputRenamesCount(t *testing.T) {
	srv, _ := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"success":true,"message":"deleted","document_id":"d1","memory_units_deleted":7}`
	})
	r := runCommand(t, srv.URL, FormatJSON, []string{"document", "delete", "b1", "d1"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
		t.Fatalf("stdout is not JSON:\n%s", r.stdout)
	}
	if out["deleted_count"] != float64(7) {
		t.Errorf("deleted_count = %v, want 7", out["deleted_count"])
	}
	if _, ok := out["memory_units_deleted"]; ok {
		t.Errorf("server field name leaked: %v", out)
	}
}

func TestDocumentUpdateTags(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantTags string
		wantErr  string
	}{
		{name: "tags list", args: []string{"--tags", "a,b"}, wantTags: `["a","b"]`},
		{name: "empty tags clear the set", args: []string{"--tags", ""}, wantTags: `[]`},
		{name: "missing tags is an error", wantErr: "At least one of --tags must be provided"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
				if call.Method != "PATCH" || call.Path != "/v1/default/banks/b1/documents/d1" {
					t.Errorf("request = %s %s", call.Method, call.Path)
				}
				return 200, `{"success":true}`
			})
			args := append([]string{"document", "update", "b1", "d1"}, tc.args...)
			r := runCommand(t, srv.URL, FormatPretty, args, nil)
			if tc.wantErr != "" {
				if r.err == nil || r.err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", r.err, tc.wantErr)
				}
				if got := calls(); len(got) != 0 {
					t.Fatalf("requests = %d", len(got))
				}
				return
			}
			if r.err != nil {
				t.Fatalf("err = %v", r.err)
			}
			body := decodeRecallBody(t, calls()[0].Body)
			tags, _ := body["tags"].([]any)
			if len(tags) == 0 && tc.wantTags != "[]" {
				t.Fatalf("tags = %v", body["tags"])
			}
			raw, _ := json.Marshal(body["tags"])
			if string(raw) != tc.wantTags {
				t.Errorf("tags = %s, want %s", raw, tc.wantTags)
			}
			if !strings.Contains(r.stdout, "Document 'd1' updated") {
				t.Errorf("stdout:\n%s", r.stdout)
			}
			if !strings.Contains(r.stdout, "\"success\": true") {
				t.Errorf("body not pretty-printed:\n%s", r.stdout)
			}
		})
	}
}
