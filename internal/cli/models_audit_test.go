package cli

import (
	"strings"
	"testing"
)

// TestAuditCommands walks the audit surface, the one family with filter and
// paging flags.
func TestAuditCommands(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		format    Format
		body      string
		wantCalls int
		want      dRequest
		out       []string
		wantUsage bool
		usageText string
	}{
		{
			name:      "list without flags sends no query",
			args:      []string{"audit", "list", "b1"},
			body:      `{"bank_id":"b1","total":0,"limit":50,"offset":0,"items":[]}`,
			wantCalls: 1,
			want:      dRequest{Method: "GET", Path: "/v1/default/banks/b1/audit-logs", Query: ""},
			out: []string{
				"Audit logs: b1",
				"  Showing: 0 (0 total)",
				"No audit log entries.",
			},
		},
		{
			name: "list forwards the given filters and paging",
			args: []string{"audit", "list", "b1", "--action", "recall", "--transport", "http",
				"--start-date", "2026-01-01T00:00:00Z", "--end-date", "2026-01-02T00:00:00Z",
				"--limit", "10", "--offset", "5"},
			body: `{"bank_id":"b1","total":5,"limit":10,"offset":5,"items":[` +
				`{"id":"a-1","action":"recall","transport":"http","bank_id":"b1","started_at":"2026-01-01T00:00:00Z",` +
				`"ended_at":"2026-01-01T00:00:02Z","duration_ms":2000,"request":{},"response":{},"metadata":{}},` +
				`{"id":"a-2","action":"retain","transport":"mcp","bank_id":"b1","started_at":null,` +
				`"ended_at":null,"duration_ms":null,"request":{},"response":{},"metadata":{}}]}`,
			wantCalls: 1,
			want: dRequest{Method: "GET", Path: "/v1/default/banks/b1/audit-logs",
				Query: "action=recall&end_date=2026-01-02T00%3A00%3A00Z&limit=10&offset=5&start_date=2026-01-01T00%3A00%3A00Z&transport=http"},
			out: []string{
				"Audit logs: b1",
				"  Showing: 2 (5 total)",
				"  2026-01-01T00:00:00Z recall [http] 2000ms",
				"  - retain [mcp] -",
			},
		},
		{
			name:      "list accepts the -l and -s paging shorts",
			args:      []string{"audit", "list", "b1", "-l", "10", "-s", "5"},
			body:      `{"bank_id":"b1","total":5,"limit":10,"offset":5,"items":[]}`,
			wantCalls: 1,
			want:      dRequest{Method: "GET", Path: "/v1/default/banks/b1/audit-logs", Query: "limit=10&offset=5"},
			out:       []string{"Audit logs: b1"},
		},
		{
			name:      "stats forwards action and period",
			args:      []string{"audit", "stats", "b1", "--action", "recall", "--period", "day"},
			body:      `{"bank_id":"b1","period":"day","trunc":"hour","start":"2026-01-01T00:00:00Z","buckets":[{"time":"2026-01-01T01:00:00Z","actions":{"recall":2},"total":2},{"time":"2026-01-01T02:00:00Z","actions":{},"total":0}]}`,
			wantCalls: 1,
			want:      dRequest{Method: "GET", Path: "/v1/default/banks/b1/audit-logs/stats", Query: "action=recall&period=day"},
			out: []string{
				"Audit stats: b1",
				"  Period: day",
				"  Start: 2026-01-01T00:00:00Z",
				"  Bucket: hour",
				`{"time":"2026-01-01T01:00:00Z","actions":{"recall":2},"total":2}`,
				`{"time":"2026-01-01T02:00:00Z","actions":{},"total":0}`,
			},
		},
		{
			name:      "stats empty prints the exact message",
			args:      []string{"audit", "stats", "b1"},
			body:      `{"bank_id":"b1","period":"day","trunc":"hour","start":"2026-01-01T00:00:00Z","buckets":[]}`,
			wantCalls: 1,
			want:      dRequest{Method: "GET", Path: "/v1/default/banks/b1/audit-logs/stats", Query: ""},
			out:       []string{"No activity in this period."},
		},
		{
			name:      "list requires one positional",
			args:      []string{"audit", "list"},
			wantUsage: true,
			usageText: "audit list requires",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newDstub(t, 200, tt.body)
			stdout, _, err := runDCommand(t, stub, tt.format, "", tt.args...)
			if tt.wantUsage {
				wantUsage(t, err, tt.usageText)
				return
			}
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			got := stub.requests()
			if len(got) != tt.wantCalls {
				t.Fatalf("requests = %d, want %d: %+v", len(got), tt.wantCalls, got)
			}
			if len(got) > 0 {
				r := got[0]
				if r.Method != tt.want.Method || r.Path != tt.want.Path || r.Query != tt.want.Query || r.Body != tt.want.Body {
					t.Fatalf("request = %+v, want %+v", r, tt.want)
				}
			}
			for _, sub := range tt.out {
				if !strings.Contains(stdout, sub) {
					t.Errorf("stdout missing %q\ngot:\n%s", sub, stdout)
				}
			}
		})
	}
}

// TestAuditListJSON pins the machine-mode envelope of the listing.
func TestAuditListJSON(t *testing.T) {
	stub := newDstub(t, 200, `{"bank_id":"b1","total":1,"limit":50,"offset":0,"items":[`+
		`{"id":"a-1","action":"recall","transport":"http","bank_id":"b1","started_at":"2026-01-01T00:00:00Z",`+
		`"ended_at":"2026-01-01T00:00:02Z","duration_ms":2000,"request":{},"response":{},"metadata":{}}]}`)
	stdout, _, err := runDCommand(t, stub, FormatJSON, "", "audit", "list", "b1")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	for _, want := range []string{`"bank_id": "b1"`, `"action": "recall"`, `"duration_ms": 2000`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q\ngot:\n%s", want, stdout)
		}
	}
}
