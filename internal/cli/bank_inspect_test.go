package cli

// Bank read-only view tests: disposition and graph.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestBankDispositionRendersView(t *testing.T) {
	stub := newBankStub(t,
		bankResponse{body: `{"bank_id":"b1","config":{"disposition_skepticism":4,"reflect_mission":"Be kind"},"overrides":{}}`},
		bankResponse{body: `{"banks":[{"bank_id":"b1","name":"Friendly","disposition":{"skepticism":3,"literalism":3,"empathy":3}}],"total":1,"limit":100,"offset":0}`},
	)
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "disposition", "b1")
	if err != nil {
		t.Fatal(err)
	}
	reqs := stub.requests()
	if len(reqs) != 2 || reqs[0].Path != "/v1/default/banks/b1/config" || reqs[1].Query != "limit=100&q=b1" {
		t.Fatalf("requests = %+v", reqs)
	}
	wantContains(t, out, "Disposition: b1")
	wantContains(t, out, "Name: Friendly")
	wantContains(t, out, "Mission:")
	wantContains(t, out, "Be kind")
	wantContains(t, out, "  Skepticism   ["+strings.Repeat("█", 30)+strings.Repeat("░", 10)+"] 4/5")
	wantContains(t, out, "    1=trusting, 5=skeptical")
	wantContains(t, out, "  Literalism   ["+strings.Repeat("█", 20)+strings.Repeat("░", 20)+"] 3/5")
	wantContains(t, out, "    1=flexible, 5=literal")
	wantContains(t, out, "  Empathy      ["+strings.Repeat("█", 20)+strings.Repeat("░", 20)+"] 3/5")
	wantContains(t, out, "    1=detached, 5=empathetic")
}

func TestBankDispositionJSONView(t *testing.T) {
	stub := newBankStub(t,
		bankResponse{body: `{"bank_id":"b1","config":{"disposition_skepticism":4},"overrides":{}}`},
		bankResponse{body: `{"banks":[{"bank_id":"b1","name":"Friendly","disposition":{"skepticism":3,"literalism":3,"empathy":3}}],"total":1,"limit":100,"offset":0}`},
	)
	out, _, err := runBankCommand(t, stub.Server.URL, FormatJSON, "", false, "bank", "disposition", "b1")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("output is not JSON: %v (%s)", err, out)
	}
	want := map[string]any{
		"bank_id": "b1", "name": "Friendly", "mission": "",
		"skepticism": float64(4), "literalism": float64(3), "empathy": float64(3),
	}
	for k, w := range want {
		if v[k] != w {
			t.Fatalf("%s = %v, want %v", k, v[k], w)
		}
	}
}

func TestBankDispositionDefaultsAndLookupFailure(t *testing.T) {
	stub := newBankStub(t,
		bankResponse{body: `{"bank_id":"b1","config":{},"overrides":{}}`},
		bankResponse{status: http.StatusNotFound, body: `{"detail":"nope"}`},
	)
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "disposition", "b1")
	if err != nil {
		t.Fatal(err)
	}
	// The lookup failure is tolerated and the traits read as the default 3.
	wantContains(t, out, "Name: b1")
	wantContains(t, out, "  Skepticism   ["+strings.Repeat("█", 20)+strings.Repeat("░", 20)+"] 3/5")
}

func TestBankGraphPretty(t *testing.T) {
	long := strings.Repeat("x", 70)
	body := `{"nodes":[` +
		`{"data":{"id":"n1","text":"` + long + `"}},` +
		`{"data":{"id":"n2","text":""}},` +
		`{"data":{"id":"n3"}},{"data":{"id":"n4"}},{"data":{"id":"n5"}},{"data":{"id":"n6"}},{"data":{"id":"n7"}}` +
		`],"edges":[],"table_rows":[],"total_units":7,"limit":1000}`
	stub := newBankStub(t, bankResponse{body: body})
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "graph", "b1")
	if err != nil {
		t.Fatal(err)
	}
	req := stub.requests()[0]
	if req.Query != "limit=1000" {
		t.Fatalf("query = %s", req.Query)
	}
	wantContains(t, out, "Memory Graph: b1")
	wantContains(t, out, "  Nodes: 7")
	wantContains(t, out, "  Edges: 0")
	wantContains(t, out, "  n1")
	wantContains(t, out, "    "+strings.Repeat("x", 60)+"...")
	wantContains(t, out, "  + 2 more...")
	wantContains(t, out, "Use JSON output for full graph data: -o json")
}

func TestBankGraphFactTypeAndJSON(t *testing.T) {
	body := `{"nodes":[],"edges":[{"data":{"id":"e1","source":"n1","target":"n2","linkType":"temporal","weight":1,"entityName":"A"}}],"table_rows":[],"total_units":0,"limit":5}`
	stub := newBankStub(t, bankResponse{body: body})
	out, _, err := runBankCommand(t, stub.Server.URL, FormatJSON, "", false,
		"bank", "graph", "b1", "--fact-type", "world", "--limit", "5")
	if err != nil {
		t.Fatal(err)
	}
	req := stub.requests()[0]
	if req.Query != "limit=5&type=world" {
		t.Fatalf("query = %s", req.Query)
	}
	if !strings.Contains(out, `"linkType"`) || !strings.Contains(out, `"table_rows"`) {
		t.Fatalf("machine output dropped graph fields: %s", out)
	}
}
