package cli

// The entity family tests: list (with its empty warning), get, and the
// body-less regenerate POST.

import (
	"strings"
	"testing"
)

const entityPage = `{"items":[
  {"id":"e1","canonical_name":"Alice","mention_count":5,"first_seen":"2024-01-01T00:00:00Z","last_seen":"2024-02-01T00:00:00Z","metadata":{}},
  {"id":"e2","canonical_name":"Bob Inc","mention_count":2,"first_seen":null,"last_seen":null,"metadata":{}}
],"total":9,"limit":100,"offset":0}`

func TestEntityList(t *testing.T) {
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, entityPage
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"entity", "list", "b1", "-l", "50"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if got := calls()[0]; got.Path != "/v1/default/banks/b1/entities" || got.RawQuery != "limit=50" {
		t.Fatalf("request = %s?%s", got.Path, got.RawQuery)
	}
	for _, want := range []string{
		"Entities for Bank: b1",
		"Total entities: 2",
		"ID: e1",
		"  Name: Alice",
		"  Mentions: 5",
		"  First seen: 2024-01-01T00:00:00Z",
		"  Last seen: 2024-02-01T00:00:00Z",
		"ID: e2",
		"  Name: Bob Inc",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, r.stdout)
		}
	}
	// The absent timestamps render nothing, not "null".
	if strings.Contains(r.stdout, "seen: <nil>") || strings.Contains(r.stdout, "seen: null") {
		t.Errorf("null timestamps leaked:\n%s", r.stdout)
	}
	// The envelope total is not the rendered count.
	if strings.Contains(r.stdout, "Total entities: 9") {
		t.Errorf("total must be the returned-page count:\n%s", r.stdout)
	}
}

func TestEntityListEmptyWarns(t *testing.T) {
	srv, _ := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"items":[],"total":0,"limit":100,"offset":0}`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"entity", "list", "b1"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if !strings.Contains(r.stdout, "warning: No entities found") {
		t.Errorf("stdout:\n%s", r.stdout)
	}
}

func TestEntityGet(t *testing.T) {
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"id":"e1","canonical_name":"Alice","mention_count":5,"first_seen":"2024-01-01T00:00:00Z","last_seen":null,"metadata":{},"observations":[{"text":"saw her","mentioned_at":"2024-01-01T00:00:00Z"}]}`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"entity", "get", "b1", "e1"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if got := calls(); len(got) != 1 || got[0].Path != "/v1/default/banks/b1/entities/e1" {
		t.Fatalf("requests = %v", got)
	}
	for _, want := range []string{
		"Entity: e1",
		"ID: e1",
		"  Name: Alice",
		"  Mentions: 5",
		"  First seen: 2024-01-01T00:00:00Z",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, r.stdout)
		}
	}
	// Observations are carried but never rendered in pretty mode.
	if strings.Contains(r.stdout, "saw her") {
		t.Errorf("observations leaked:\n%s", r.stdout)
	}
}

func TestEntityRegenerate(t *testing.T) {
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		if call.Method != "POST" || call.Path != "/v1/default/banks/b1/entities/e1/regenerate" {
			t.Errorf("request = %s %s", call.Method, call.Path)
		}
		if call.Body != "" {
			t.Errorf("regenerate must send no body, got %q", call.Body)
		}
		return 200, `{"id":"e1","canonical_name":"Alice","mention_count":5,"first_seen":"2024-01-01T00:00:00Z","last_seen":null,"observations":[{"text":"a"},{"text":"b"}]}`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"entity", "regenerate", "b1", "e1"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if got := calls(); len(got) != 1 {
		t.Fatalf("requests = %d", len(got))
	}
	for _, want := range []string{
		"Successfully regenerated observations for entity: e1",
		"Updated entity:",
		"  Name: Alice",
		"  Mentions: 5",
		"  Observations: 2",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, r.stdout)
		}
	}
}
