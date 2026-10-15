package cli

import (
	"strings"
	"testing"
)

// TestDirectiveCommands walks the directive surface: methods, paths, bodies
// and pretty rendering for every action.
func TestDirectiveCommands(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		stdin     string
		format    Format
		body      string
		wantCalls int
		want      dRequest
		out       []string
		wantUsage bool
		usageText string
		// wantCommand marks a failure raised inside the command body, which
		// exits 1 rather than 2.
		wantCommand bool
	}{
		{
			name:      "list sends no filters and renders rows",
			args:      []string{"directive", "list", "b1"},
			body:      `{"items":[{"id":"d-1","bank_id":"b1","name":"Be terse","content":"Answer in one paragraph whenever possible, never exceeding three sentences.","priority":10,"is_active":true,"tags":["style"],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"},{"id":"d-2","bank_id":"b1","name":"Archived","content":"old rule","priority":0,"is_active":false,"tags":[],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}],"total":2,"limit":100,"offset":0}`,
			wantCalls: 1,
			want:      dRequest{Method: "GET", Path: "/v1/default/banks/b1/directives", Query: ""},
			out: []string{
				"Directives: b1",
				"  d-1 Be terse [active]",
				"    Answer in one paragraph whenever possible, never exceeding three sentences.",
				"  d-2 Archived [inactive]",
			},
		},
		{
			name:      "list empty prints the exact message",
			args:      []string{"directive", "list", "b1"},
			body:      `{"items":[],"total":0,"limit":100,"offset":0}`,
			wantCalls: 1,
			want:      dRequest{Method: "GET", Path: "/v1/default/banks/b1/directives", Query: ""},
			out:       []string{"No directives found."},
		},
		{
			name:      "get renders the full detail",
			args:      []string{"directive", "get", "b1", "d-1"},
			body:      `{"id":"d-1","bank_id":"b1","name":"Be terse","content":"one paragraph","priority":10,"is_active":true,"tags":["style","tone"],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
			wantCalls: 1,
			want:      dRequest{Method: "GET", Path: "/v1/default/banks/b1/directives/d-1"},
			out: []string{
				"Be terse",
				"  ID: d-1",
				"  Status: active",
				"  Priority: 10",
				"  Tags: style, tone",
				"─── Content ───",
				"one paragraph",
			},
		},
		{
			name:      "create always sends is_active true and empty tags",
			args:      []string{"directive", "create", "b1", "Be terse", "one paragraph"},
			body:      `{"id":"d-1","bank_id":"b1","name":"Be terse","content":"one paragraph","priority":0,"is_active":true,"tags":[],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
			wantCalls: 1,
			want: dRequest{Method: "POST", Path: "/v1/default/banks/b1/directives",
				Body: `{"name":"Be terse","content":"one paragraph","is_active":true,"priority":0,"tags":[]}`},
			out: []string{"Directive 'd-1' created successfully", "Be terse", "one paragraph"},
		},
		{
			name:      "create forwards --priority",
			args:      []string{"directive", "create", "b1", "Be terse", "one paragraph", "--priority", "5"},
			body:      `{"id":"d-1","bank_id":"b1","name":"Be terse","content":"one paragraph","priority":5,"is_active":true,"tags":[],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
			wantCalls: 1,
			want: dRequest{Method: "POST", Path: "/v1/default/banks/b1/directives",
				Body: `{"name":"Be terse","content":"one paragraph","is_active":true,"priority":5,"tags":[]}`},
			out: []string{"Directive 'd-1' created successfully"},
		},
		{
			name:      "update sends only the passed fields",
			args:      []string{"directive", "update", "b1", "d-1", "--is-active", "false"},
			body:      `{"id":"d-1","bank_id":"b1","name":"Be terse","content":"one paragraph","priority":0,"is_active":false,"tags":[],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
			wantCalls: 1,
			want:      dRequest{Method: "PATCH", Path: "/v1/default/banks/b1/directives/d-1", Body: `{"is_active":false}`},
			out:       []string{"Directive 'd-1' updated successfully"},
		},
		{
			name:      "update with name and priority",
			args:      []string{"directive", "update", "b1", "d-1", "--name", "Renamed", "--priority", "7"},
			body:      `{"id":"d-1","bank_id":"b1","name":"Renamed","content":"one paragraph","priority":7,"is_active":true,"tags":[],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
			wantCalls: 1,
			want:      dRequest{Method: "PATCH", Path: "/v1/default/banks/b1/directives/d-1", Body: `{"name":"Renamed","priority":7}`},
			out:       []string{"Directive 'd-1' updated successfully"},
		},
		{
			name:      "update sends an explicit priority of zero",
			args:      []string{"directive", "update", "b1", "d-1", "--priority", "0"},
			body:      `{"id":"d-1","bank_id":"b1","name":"Be terse","content":"one paragraph","priority":0,"is_active":true,"tags":[],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
			wantCalls: 1,
			want:      dRequest{Method: "PATCH", Path: "/v1/default/banks/b1/directives/d-1", Body: `{"priority":0}`},
			out:       []string{"Directive 'd-1' updated successfully"},
		},
		{
			name:        "update without any flag is a hard CLI error",
			args:        []string{"directive", "update", "b1", "d-1"},
			wantCalls:   0,
			wantCommand: true,
			usageText:   "At least one of --name, --content, --is-active, or --priority must be provided",
		},
		{
			name:      "delete confirms then deletes",
			args:      []string{"directive", "delete", "b1", "d-1"},
			stdin:     "y\n",
			body:      `{"status":"deleted"}`,
			wantCalls: 1,
			want:      dRequest{Method: "DELETE", Path: "/v1/default/banks/b1/directives/d-1"},
			out: []string{
				"Are you sure you want to delete directive 'd-1'? This cannot be undone.",
				"Directive 'd-1' deleted successfully",
			},
		},
		{
			name:      "delete declined makes no HTTP call",
			args:      []string{"directive", "delete", "b1", "d-1"},
			stdin:     "n\n",
			wantCalls: 0,
			out:       []string{"Operation cancelled"},
		},
		{
			name:      "delete in json mode prints the synthesized acknowledgement",
			args:      []string{"directive", "delete", "b1", "d-1"},
			format:    FormatJSON,
			body:      `{"status":"deleted"}`,
			wantCalls: 1,
			want:      dRequest{Method: "DELETE", Path: "/v1/default/banks/b1/directives/d-1"},
			out:       []string{"{\"success\": true}"},
		},
		{
			name:      "get requires two positionals",
			args:      []string{"directive", "get", "b1"},
			wantUsage: true,
			usageText: "directive get requires",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newDstub(t, 200, tt.body)
			stdout, _, err := runDCommand(t, stub, tt.format, tt.stdin, tt.args...)
			if tt.wantUsage {
				wantUsage(t, err, tt.usageText)
				return
			}
			if tt.wantCommand {
				wantCommand(t, err, tt.usageText)
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
