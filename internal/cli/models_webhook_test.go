package cli

import (
	"strings"
	"testing"
)

// TestWebhookCommands walks the webhook surface. There is no single-webhook
// GET, so no get case exists.
func TestWebhookCommands(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		stdin     string
		format    Format
		body      string
		wantCalls int
		want      dRequest
		out       []string
		errOut    []string
		wantUsage bool
		usageText string
		// wantCommand marks a failure raised inside the command body, which
		// exits 1 rather than 2.
		wantCommand bool
	}{
		{
			name: "list renders rows and the paging line",
			args: []string{"webhook", "list", "b1"},
			body: `{"items":[{"id":"wh-1","bank_id":"b1","url":"https://example.test/hook","secret":null,` +
				`"event_types":["consolidation.completed"],"enabled":true,` +
				`"http_config":{"method":"POST","timeout_seconds":30,"headers":{},"params":{}},` +
				`"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}],"total":3,"limit":50,"offset":0}`,
			wantCalls: 1,
			want:      dRequest{Method: "GET", Path: "/v1/default/banks/b1/webhooks", Query: ""},
			out: []string{
				"Webhooks: b1",
				"  wh-1 [enabled] https://example.test/hook",
				"    events: consolidation.completed",
				"Showing 1 of 3 webhooks.",
			},
		},
		{
			name:      "list without paging shortfall prints no paging line",
			args:      []string{"webhook", "list", "b1"},
			body:      `{"items":[],"total":0,"limit":50,"offset":0}`,
			wantCalls: 1,
			want:      dRequest{Method: "GET", Path: "/v1/default/banks/b1/webhooks", Query: ""},
			out:       []string{"No webhooks configured."},
		},
		{
			name:      "create substitutes the default event type",
			args:      []string{"webhook", "create", "b1", "https://example.test/hook"},
			body:      `{"id":"wh-1","bank_id":"b1","url":"https://example.test/hook","secret":null,"event_types":["consolidation.completed"],"enabled":true,"http_config":{"method":"POST","timeout_seconds":30,"headers":{},"params":{}},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
			wantCalls: 1,
			want: dRequest{Method: "POST", Path: "/v1/default/banks/b1/webhooks",
				Body: `{"url":"https://example.test/hook","event_types":["consolidation.completed"],"enabled":true,"http_config":null}`},
			out: []string{"Webhook 'wh-1' created", "  URL: https://example.test/hook", "  Events: consolidation.completed"},
		},
		{
			name:      "create forwards event types, disabled and secret",
			args:      []string{"webhook", "create", "b1", "https://example.test/hook", "--event-types", "retain.completed, memory_defense.triggered", "--disabled", "--secret", "s3cret"},
			body:      `{"id":"wh-1","bank_id":"b1","url":"https://example.test/hook","secret":null,"event_types":["retain.completed","memory_defense.triggered"],"enabled":false,"http_config":{"method":"POST","timeout_seconds":30,"headers":{},"params":{}},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
			wantCalls: 1,
			want: dRequest{Method: "POST", Path: "/v1/default/banks/b1/webhooks",
				Body: `{"url":"https://example.test/hook","secret":"s3cret","event_types":["retain.completed","memory_defense.triggered"],"enabled":false,"http_config":null}`},
			out: []string{"Webhook 'wh-1' created"},
		},
		{
			name:      "update sends only the passed fields",
			args:      []string{"webhook", "update", "b1", "wh-1", "--enabled", "true"},
			body:      `{"id":"wh-1","bank_id":"b1","url":"https://example.test/hook","secret":null,"event_types":["consolidation.completed"],"enabled":true,"http_config":{"method":"POST","timeout_seconds":30,"headers":{},"params":{}},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
			wantCalls: 1,
			want:      dRequest{Method: "PATCH", Path: "/v1/default/banks/b1/webhooks/wh-1", Body: `{"enabled":true}`},
			out:       []string{"Webhook 'wh-1' updated"},
		},
		{
			name:      "update with url and event types replaces the set",
			args:      []string{"webhook", "update", "b1", "wh-1", "--url", "https://other.test/h", "--event-types", "retain.completed"},
			body:      `{"id":"wh-1","bank_id":"b1","url":"https://other.test/h","secret":null,"event_types":["retain.completed"],"enabled":true,"http_config":{"method":"POST","timeout_seconds":30,"headers":{},"params":{}},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
			wantCalls: 1,
			want:      dRequest{Method: "PATCH", Path: "/v1/default/banks/b1/webhooks/wh-1", Body: `{"url":"https://other.test/h","event_types":["retain.completed"]}`},
			out:       []string{"Webhook 'wh-1' updated"},
		},
		{
			name:        "update without any flag is a hard CLI error",
			args:        []string{"webhook", "update", "b1", "wh-1"},
			wantCalls:   0,
			wantCommand: true,
			usageText:   "At least one of --url, --event-types, --enabled, or --secret must be provided",
		},
		{
			name:      "delete confirms then deletes",
			args:      []string{"webhook", "delete", "b1", "wh-1"},
			stdin:     "y\n",
			body:      `{"success":true,"message":"Deleted successfully"}`,
			wantCalls: 1,
			want:      dRequest{Method: "DELETE", Path: "/v1/default/banks/b1/webhooks/wh-1"},
			out: []string{
				"Are you sure you want to delete webhook 'wh-1'? This cannot be undone.",
				"Webhook 'wh-1' deleted",
			},
		},
		{
			name:      "delete declined makes no HTTP call",
			args:      []string{"webhook", "delete", "b1", "wh-1"},
			stdin:     "n\n",
			wantCalls: 0,
			out:       []string{"Operation cancelled"},
		},
		{
			name:      "delete in json mode prints the server response",
			args:      []string{"webhook", "delete", "b1", "wh-1"},
			format:    FormatJSON,
			body:      `{"success":true,"message":"Deleted successfully"}`,
			wantCalls: 1,
			want:      dRequest{Method: "DELETE", Path: "/v1/default/banks/b1/webhooks/wh-1"},
			out:       []string{`"success": true`},
		},
		{
			name:      "delete failure prints the failure line",
			args:      []string{"webhook", "delete", "b1", "wh-1"},
			stdin:     "y\n",
			body:      `{"success":false,"message":"still referenced"}`,
			wantCalls: 1,
			want:      dRequest{Method: "DELETE", Path: "/v1/default/banks/b1/webhooks/wh-1"},
			errOut:    []string{"Failed to delete webhook"},
		},
		{
			name: "deliveries omits an unset limit",
			args: []string{"webhook", "deliveries", "b1", "wh-1"},
			body: `{"items":[{"id":"dl-1","webhook_id":"wh-1","url":"https://example.test/hook","event_type":"consolidation.completed",` +
				`"status":"delivered","attempts":1,"next_retry_at":null,"last_error":null,"last_response_status":200,` +
				`"last_response_body":null,"last_attempt_at":"2026-01-01T00:00:00Z","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}],` +
				`"next_cursor":"2026-01-01T00:00:00.5Z"}`,
			wantCalls: 1,
			want:      dRequest{Method: "GET", Path: "/v1/default/banks/b1/webhooks/wh-1/deliveries"},
			out: []string{
				"Deliveries for wh-1",
				"  dl-1 [consolidation.completed] 200 — attempts: 1",
				"  next cursor: 2026-01-01T00:00:00.5Z",
			},
		},
		{
			name:      "deliveries forwards the cursor and an explicit limit",
			args:      []string{"webhook", "deliveries", "b1", "wh-1", "--cursor", "c1", "-l", "500"},
			body:      `{"items":[],"next_cursor":null}`,
			wantCalls: 1,
			want:      dRequest{Method: "GET", Path: "/v1/default/banks/b1/webhooks/wh-1/deliveries", Query: "cursor=c1&limit=500"},
			out:       []string{"No delivery attempts recorded."},
		},
		{
			name: "deliveries renders errors and the missing-status dash",
			args: []string{"webhook", "deliveries", "b1", "wh-1"},
			body: `{"items":[{"id":"dl-2","webhook_id":"wh-1","url":"https://example.test/hook","event_type":"retain.completed",` +
				`"status":"failed","attempts":3,"next_retry_at":null,"last_error":"connection refused","last_response_status":null,` +
				`"last_response_body":null,"last_attempt_at":null,"created_at":null,"updated_at":null}],"next_cursor":null}`,
			wantCalls: 1,
			want:      dRequest{Method: "GET", Path: "/v1/default/banks/b1/webhooks/wh-1/deliveries"},
			out: []string{
				"  dl-2 [retain.completed] - — attempts: 3",
				"    error: connection refused",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newDstub(t, 200, tt.body)
			stdout, stderr, err := runDCommand(t, stub, tt.format, tt.stdin, tt.args...)
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
			for _, sub := range tt.errOut {
				if !strings.Contains(stderr, sub) {
					t.Errorf("stderr missing %q\ngot:\n%s", sub, stderr)
				}
			}
		})
	}
}
