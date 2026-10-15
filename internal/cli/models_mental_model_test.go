package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// mmFixtureBody builds a single-model response, optionally carrying a stored
// trigger JSON object.
func mmFixtureBody(trigger string) string {
	var triggerPart string
	if trigger != "" {
		triggerPart = `"trigger":` + trigger + `,`
	}
	return `{"id":"mm-1","bank_id":"b1","subtype":"user","name":"Notes","description":"",` +
		`"source_query":"q","content":"hello world","tags":[],"max_tokens":2048,` + triggerPart +
		`"observations":{"observations":[]},"is_stale":false,"created_at":"2026-01-01T00:00:00Z"}`
}

// decodeBody unmarshals a captured request body into a generic object.
func decodeBody(t *testing.T, raw string) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("request body is not JSON: %v\n%s", err, raw)
	}
	return body
}

// wantTriggerJSON unmarshals the expected trigger shape for comparison.
func wantTriggerJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var want map[string]any
	if err := json.Unmarshal([]byte(raw), &want); err != nil {
		t.Fatalf("expected trigger is not JSON: %v", err)
	}
	return want
}

// TestMentalModelCommands walks the mental-model surface: methods, paths,
// query strings, bodies and rendering for every action.
func TestMentalModelCommands(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		stdin     string
		format    Format
		verbose   bool
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
			name:      "list pins detail=content",
			args:      []string{"mental-model", "list", "b1"},
			body:      `{"items":[` + mmFixtureBody("") + `],"total":1,"limit":100,"offset":0}`,
			wantCalls: 1,
			want:      dRequest{Method: "GET", Path: "/v1/default/banks/b1/mental-models", Query: "detail=content"},
			out:       []string{"Mental Models: b1", "  mm-1 Notes", "    hello world"},
		},
		{
			name:      "list with --verbose pins detail=full",
			args:      []string{"mental-model", "list", "b1"},
			verbose:   true,
			body:      `{"items":[],"total":0,"limit":100,"offset":0}`,
			wantCalls: 1,
			want:      dRequest{Method: "GET", Path: "/v1/default/banks/b1/mental-models", Query: "detail=full"},
			out:       []string{"No mental models found."},
		},
		{
			name:      "get renders name, id, source query and content",
			args:      []string{"mental-model", "get", "b1", "mm-1"},
			body:      mmFixtureBody(""),
			wantCalls: 1,
			want:      dRequest{Method: "GET", Path: "/v1/default/banks/b1/mental-models/mm-1"},
			out:       []string{"Notes", "  ID: mm-1", "  Source Query: q", "─── Content ───", "hello world"},
		},
		{
			name:      "create sends defaults and no trigger",
			args:      []string{"mental-model", "create", "b1", "Notes", "some query"},
			body:      `{"mental_model_id":"mm-1","operation_id":"op-1"}`,
			wantCalls: 1,
			want: dRequest{Method: "POST", Path: "/v1/default/banks/b1/mental-models",
				Body: `{"name":"Notes","source_query":"some query","max_tokens":2048,"tags":null}`},
			out: []string{"Mental model created, operation_id: op-1"},
		},
		{
			name:      "create with --id and --tags sends them",
			args:      []string{"mental-model", "create", "b1", "Notes", "q", "--id", "custom-1", "--tags", "a, b"},
			body:      `{"mental_model_id":"custom-1","operation_id":"op-1"}`,
			wantCalls: 1,
			want: dRequest{Method: "POST", Path: "/v1/default/banks/b1/mental-models",
				Body: `{"id":"custom-1","name":"Notes","source_query":"q","max_tokens":2048,"tags":["a","b"]}`},
			out: []string{"Mental model created, operation_id: op-1"},
		},
		{
			name:      "create with --trigger-mode sends the trigger",
			args:      []string{"mental-model", "create", "b1", "Notes", "q", "--trigger-mode", "DELTA"},
			body:      `{"mental_model_id":"mm-1","operation_id":"op-1"}`,
			wantCalls: 1,
			want: dRequest{Method: "POST", Path: "/v1/default/banks/b1/mental-models",
				Body: `{"name":"Notes","source_query":"q","max_tokens":2048,"tags":null,"trigger":{"mode":"delta","refresh_after_consolidation":false}}`},
			out: []string{"Mental model created, operation_id: op-1"},
		},
		{
			name:      "create with --tags-match alone gates the trigger without carrying it",
			args:      []string{"mental-model", "create", "b1", "Notes", "q", "--tags-match", "ALL"},
			body:      `{"mental_model_id":"mm-1","operation_id":"op-1"}`,
			wantCalls: 1,
			want: dRequest{Method: "POST", Path: "/v1/default/banks/b1/mental-models",
				Body: `{"name":"Notes","source_query":"q","max_tokens":2048,"tags":null,"trigger":{"mode":"full","refresh_after_consolidation":false,"tags_match":"all"}}`},
			out: []string{"Mental model created, operation_id: op-1"},
		},
		{
			name:        "create rejects an invalid tags-match before any call",
			args:        []string{"mental-model", "create", "b1", "Notes", "q", "--tags-match", "bogus"},
			wantCalls:   0,
			wantCommand: true,
			usageText:   "tags-match",
		},
		{
			name:        "update without any flag is a hard CLI error",
			args:        []string{"mental-model", "update", "b1", "mm-1"},
			wantCalls:   0,
			wantCommand: true,
			usageText:   "At least one of --name, --source-query, --max-tokens, --tags, or a --trigger-* flag must be provided",
		},
		{
			name:      "update with plain fields sends no trigger and no read-back",
			args:      []string{"mental-model", "update", "b1", "mm-1", "--name", "Renamed"},
			body:      mmFixtureBody(""),
			wantCalls: 1,
			want: dRequest{Method: "PATCH", Path: "/v1/default/banks/b1/mental-models/mm-1",
				Body: `{"name":"Renamed"}`},
			out: []string{"Mental model 'mm-1' updated successfully", "Notes"},
		},
		{
			name:      "delete confirms then deletes",
			args:      []string{"mental-model", "delete", "b1", "mm-1"},
			stdin:     "y\n",
			body:      `{"status":"deleted"}`,
			wantCalls: 1,
			want:      dRequest{Method: "DELETE", Path: "/v1/default/banks/b1/mental-models/mm-1"},
			out: []string{
				"Are you sure you want to delete mental model 'mm-1'? This cannot be undone.",
				"Mental model 'mm-1' deleted successfully",
			},
		},
		{
			name:      "delete declined makes no HTTP call",
			args:      []string{"mental-model", "delete", "b1", "mm-1"},
			stdin:     "n\n",
			wantCalls: 0,
			out:       []string{"Operation cancelled"},
		},
		{
			name:      "delete in json mode prints the synthesized acknowledgement",
			args:      []string{"mental-model", "delete", "b1", "mm-1"},
			format:    FormatJSON,
			body:      `{"status":"deleted"}`,
			wantCalls: 1,
			want:      dRequest{Method: "DELETE", Path: "/v1/default/banks/b1/mental-models/mm-1"},
			out:       []string{"{\"success\": true}"},
		},
		{
			name:      "refresh posts to the refresh route",
			args:      []string{"mental-model", "refresh", "b1", "mm-1"},
			body:      `{"operation_id":"op-1","status":"queued"}`,
			wantCalls: 1,
			want:      dRequest{Method: "POST", Path: "/v1/default/banks/b1/mental-models/mm-1/refresh", Body: ""},
			out: []string{
				"Mental model refresh submitted. Operation ID: op-1",
				"  Status: queued",
				"Use 'wenyuan operations get' to check the operation status.",
			},
		},
		{
			name: "dry-run renders the full preview",
			args: []string{"mental-model", "dry-run-refresh", "b1", "mm-1"},
			body: `{"requested_mode":"full","effective_mode":"delta","mode_fallback_reason":"budget","outcome":"changed",` +
				`"would_persist":true,"facts":{"retrieved":{"world":3,"observation":12},"used":{"observation":4}},` +
				`"delta_operations":{"applied":[{}],"skipped":[]},"duration_ms":42,` +
				`"warnings":["window empty"],"diff":"- old\n+ new"}`,
			wantCalls: 1,
			want:      dRequest{Method: "POST", Path: "/v1/default/banks/b1/mental-models/mm-1/dry-run-refresh", Body: ""},
			out: []string{
				"  Mode: full → delta",
				"  Fell back: budget",
				"  Outcome: changed",
				"  Would save: true",
				"  Retrieved: observation: 12, world: 3",
				"  Used: observation: 4",
				"  Delta ops: 1 applied, 0 skipped",
				"  Duration: 42 ms",
				"warning: window empty",
				"- old",
				"+ new",
				"Nothing was saved. Use 'mental-model refresh' to apply.",
			},
		},
		{
			name:      "dry-run with an empty diff prints the no-change line",
			args:      []string{"mental-model", "dry-run-refresh", "b1", "mm-1"},
			body:      `{"requested_mode":"full","effective_mode":"full","outcome":"unchanged","would_persist":false,"facts":{"retrieved":{},"used":{}},"duration_ms":1,"warnings":[],"diff":""}`,
			wantCalls: 1,
			want:      dRequest{Method: "POST", Path: "/v1/default/banks/b1/mental-models/mm-1/dry-run-refresh", Body: ""},
			out:       []string{"  Mode: full", "No content change."},
		},
		{
			name:      "history renders entries newest first",
			args:      []string{"mental-model", "history", "b1", "mm-1"},
			body:      `[{"previous_content":"old content","previous_reflect_response":{},"changed_at":"2026-01-02T00:00:00Z"},{"previous_content":null,"previous_reflect_response":null,"changed_at":"2026-01-01T00:00:00Z","kind":"failure","outcome":"refused","failure_reason":"no facts","error_message":"empty"}]`,
			wantCalls: 1,
			want:      dRequest{Method: "GET", Path: "/v1/default/banks/b1/mental-models/mm-1/history"},
			out: []string{
				"History: mm-1",
				"  Changed at: 2026-01-02T00:00:00Z",
				"  Previous: old content",
				"  Changed at: 2026-01-01T00:00:00Z",
				"  Previous: (none)",
			},
		},
		{
			name:      "history empty prints the exact message",
			args:      []string{"mental-model", "history", "b1", "mm-1"},
			body:      `[]`,
			wantCalls: 1,
			want:      dRequest{Method: "GET", Path: "/v1/default/banks/b1/mental-models/mm-1/history"},
			out:       []string{"No history entries found."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newDstub(t, 200, tt.body)
			stdout, _, err := runDCommandVerbose(t, stub, tt.format, tt.stdin, tt.verbose, tt.args...)
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

// TestMentalModelUpdateTriggerMerge is the GET-then-merge matrix: a
// trigger-bearing update reads the model back, applies the flags on top of
// the stored trigger, and resolves the consolidation/cron mutual exclusion
// before the PATCH.
func TestMentalModelUpdateTriggerMerge(t *testing.T) {
	fullDefaults := `"mode":"full","refresh_after_consolidation":false,"exclude_mental_models":false,"keep_trace":false`
	tests := []struct {
		name         string
		storedJSON   string // stored trigger object, "" when the model has none
		args         []string
		wantTrigger  string // expected PATCH trigger object
		wantMutually bool   // expect the mutual-exclusion hard error, PATCH never sent
	}{
		{
			name:        "plain fields pass no trigger and no GET",
			args:        []string{"--name", "Renamed"},
			wantTrigger: "",
		},
		{
			name:        "flags over a stored trigger preserve the unconditional members",
			storedJSON:  `{"mode":"full","refresh_after_consolidation":true,"exclude_mental_models":false,"keep_trace":true,"min_refresh_interval_seconds":60}`,
			args:        []string{"--trigger-mode", "delta"},
			wantTrigger: `{"mode":"delta","refresh_after_consolidation":true,"exclude_mental_models":false,"keep_trace":true,"min_refresh_interval_seconds":60}`,
		},
		{
			name:        "no stored trigger falls back to the API defaults",
			args:        []string{"--trigger-refresh-cron", "0 9 * * *"},
			wantTrigger: `{` + fullDefaults + `,"refresh_cron":"0 9 * * *"}`,
		},
		{
			name:        "naming cron over a consolidation model drops consolidation",
			storedJSON:  `{"mode":"full","refresh_after_consolidation":true,"exclude_mental_models":false,"keep_trace":false}`,
			args:        []string{"--trigger-refresh-cron", "0 9 * * *"},
			wantTrigger: `{"mode":"full","refresh_after_consolidation":false,"exclude_mental_models":false,"keep_trace":false,"refresh_cron":"0 9 * * *"}`,
		},
		{
			name:        "naming consolidation over a cron model clears the cron",
			storedJSON:  `{"mode":"full","refresh_after_consolidation":false,"exclude_mental_models":false,"keep_trace":false,"refresh_cron":"0 9 * * *"}`,
			args:        []string{"--trigger-refresh-after-consolidation", "true"},
			wantTrigger: `{"mode":"full","refresh_after_consolidation":true,"exclude_mental_models":false,"keep_trace":false,"refresh_cron":null}`,
		},
		{
			name:         "naming both in one command is a hard error",
			storedJSON:   `{"mode":"full","refresh_after_consolidation":true,"exclude_mental_models":false,"keep_trace":false}`,
			args:         []string{"--trigger-refresh-cron", "0 9 * * *", "--trigger-refresh-after-consolidation", "true"},
			wantMutually: true,
		},
		{
			name:        "empty cron clears the schedule",
			storedJSON:  `{"mode":"full","refresh_after_consolidation":false,"exclude_mental_models":false,"keep_trace":false,"refresh_cron":"0 9 * * *"}`,
			args:        []string{"--trigger-refresh-cron", ""},
			wantTrigger: `{"mode":"full","refresh_after_consolidation":false,"exclude_mental_models":false,"keep_trace":false,"refresh_cron":null}`,
		},
		{
			name:        "whitespace tags-match clears the stored value",
			storedJSON:  `{"mode":"full","refresh_after_consolidation":false,"exclude_mental_models":false,"keep_trace":false,"tags_match":"all"}`,
			args:        []string{"--trigger-tags-match", "  "},
			wantTrigger: `{"mode":"full","refresh_after_consolidation":false,"exclude_mental_models":false,"keep_trace":false,"tags_match":null}`,
		},
		{
			name:        "tags-match is parsed case-insensitively",
			args:        []string{"--trigger-tags-match", "EXACT"},
			wantTrigger: `{` + fullDefaults + `,"tags_match":"exact"}`,
		},
		{
			name:        "keep-trace and exclude-mental-models apply as given",
			args:        []string{"--trigger-keep-trace", "false", "--trigger-exclude-mental-models", "true"},
			wantTrigger: `{"mode":"full","refresh_after_consolidation":false,"exclude_mental_models":true,"keep_trace":false}`,
		},
		{
			name:        "disabling consolidation over a cron model keeps the cron",
			storedJSON:  `{"mode":"full","refresh_after_consolidation":false,"exclude_mental_models":false,"keep_trace":false,"refresh_cron":"0 9 * * *"}`,
			args:        []string{"--trigger-refresh-after-consolidation", "false"},
			wantTrigger: `{"mode":"full","refresh_after_consolidation":false,"exclude_mental_models":false,"keep_trace":false,"refresh_cron":"0 9 * * *"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"mental-model", "update", "b1", "mm-1"}, tt.args...)
			var body string
			if tt.storedJSON == "" {
				body = mmFixtureBody("")
			} else {
				body = mmFixtureBody(tt.storedJSON)
			}
			stub := newDstub(t, 200, body)
			_, _, err := runDCommand(t, stub, FormatPretty, "", args...)
			if tt.wantMutually {
				wantCommand(t, err, "mutually exclusive")
				got := stub.requests()
				if len(got) != 1 || got[0].Method != "GET" {
					t.Fatalf("expected only the GET read-back, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			got := stub.requests()
			wantCalls := 1
			if tt.wantTrigger != "" {
				wantCalls = 2
			}
			if len(got) != wantCalls {
				t.Fatalf("requests = %d, want %d: %+v", len(got), wantCalls, got)
			}
			patch := got[len(got)-1]
			if patch.Method != "PATCH" {
				t.Fatalf("last request = %s, want PATCH", patch.Method)
			}
			if wantCalls == 2 && (got[0].Method != "GET" || got[0].Path != "/v1/default/banks/b1/mental-models/mm-1") {
				t.Fatalf("read-back = %+v, want GET of the model", got[0])
			}
			fields := decodeBody(t, patch.Body)
			if tt.wantTrigger == "" {
				if _, ok := fields["trigger"]; ok {
					t.Fatalf("patch body carries a trigger without trigger flags: %s", patch.Body)
				}
				return
			}
			gotTrig, ok := fields["trigger"].(map[string]any)
			if !ok {
				t.Fatalf("patch body carries no trigger object: %s", patch.Body)
			}
			if !reflect.DeepEqual(gotTrig, wantTriggerJSON(t, tt.wantTrigger)) {
				t.Fatalf("trigger = %v, want %s", gotTrig, tt.wantTrigger)
			}
		})
	}
}
