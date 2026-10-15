package cli

// The recall test matrix: window validation, request-body field names under
// every flag combination, and the pretty rendering blocks.

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeRecallBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("request body is not JSON: %v\n%s", err, body)
	}
	return out
}

func TestRecallWindowValidation(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "start alone",
			args:    []string{"--window-start", "2024-01-01"},
			wantErr: "--window-start requires --window-end",
		},
		{
			name:    "end alone",
			args:    []string{"--window-end", "2024-01-01"},
			wantErr: "--window-end requires --window-start",
		},
		{
			name:    "end before start",
			args:    []string{"--window-start", "2024-01-02", "--window-end", "2024-01-01"},
			wantErr: "--window-end must not be earlier than --window-start",
		},
		{
			name:    "unparseable start",
			args:    []string{"--window-start", "not-a-date", "--window-end", "2024-01-01"},
			wantErr: `--window-start must be an ISO 8601 datetime (e.g. 2023-04-01T00:00:00Z or 2023-04-01), got "not-a-date"`,
		},
		{
			name:    "unparseable end",
			args:    []string{"--window-start", "2024-01-01", "--window-end", "15/03/2024"},
			wantErr: `--window-end must be an ISO 8601 datetime (e.g. 2023-04-01T00:00:00Z or 2023-04-01), got "15/03/2024"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
				return 200, `{"results":[]}`
			})
			r := runCommand(t, srv.URL, FormatPretty, append([]string{"memory", "recall", "b1", "q"}, tc.args...), nil)
			if r.err == nil {
				t.Fatal("expected a validation error")
			}
			if r.err.Error() != tc.wantErr {
				t.Errorf("err = %q, want %q", r.err.Error(), tc.wantErr)
			}
			if got := calls(); len(got) != 0 {
				t.Fatalf("requests = %d, want 0", len(got))
			}
		})
	}
}

func TestRecallWindowNormalization(t *testing.T) {
	cases := []struct {
		name      string
		start     string
		end       string
		wantStart string
		wantEnd   string
	}{
		{name: "equal degenerate bounds", start: "2024-01-01", end: "2024-01-01", wantStart: "2024-01-01T00:00:00Z", wantEnd: "2024-01-01T00:00:00Z"},
		{name: "naive minute precision", start: "2024-01-01T10:30", end: "2024-01-02", wantStart: "2024-01-01T10:30:00Z", wantEnd: "2024-01-02T00:00:00Z"},
		{name: "offset normalized to utc", start: "2024-01-01T12:00:00+02:00", end: "2024-01-01T18:00:00+02:00", wantStart: "2024-01-01T10:00:00Z", wantEnd: "2024-01-01T16:00:00Z"},
		{name: "utc with z stays", start: "2024-01-01T00:00:00Z", end: "2024-01-01T00:00:00Z", wantStart: "2024-01-01T00:00:00Z", wantEnd: "2024-01-01T00:00:00Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
				return 200, `{"results":[]}`
			})
			r := runCommand(t, srv.URL, FormatPretty, []string{
				"memory", "recall", "b1", "q", "--window-start", tc.start, "--window-end", tc.end,
			}, nil)
			if r.err != nil {
				t.Fatalf("err = %v", r.err)
			}
			body := decodeRecallBody(t, calls()[0].Body)
			window, ok := body["temporal_window"].(map[string]any)
			if !ok {
				t.Fatalf("temporal_window missing:\n%s", calls()[0].Body)
			}
			if window["start"] != tc.wantStart || window["end"] != tc.wantEnd {
				t.Errorf("window = %v, want %s..%s", window, tc.wantStart, tc.wantEnd)
			}
		})
	}
}

func TestRecallRequestBody(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		assert func(t *testing.T, body map[string]any)
	}{
		{
			name: "defaults",
			args: nil,
			assert: func(t *testing.T, body map[string]any) {
				types, _ := body["types"].([]any)
				if len(types) != 3 {
					t.Errorf("types = %v, want the three defaults", body["types"])
				}
				if body["budget"] != "mid" || body["max_tokens"] != float64(4096) {
					t.Errorf("budget/max_tokens = %v/%v", body["budget"], body["max_tokens"])
				}
				if body["trace"] != false || body["prefer_observations"] != false {
					t.Errorf("booleans must travel explicitly: %v", body)
				}
				if body["tags_match"] != "any" {
					t.Errorf("tags_match = %v", body["tags_match"])
				}
				for _, absent := range []string{"tags", "include", "temporal_window", "query_timestamp"} {
					if _, ok := body[absent]; ok {
						t.Errorf("%s must be omitted: %v", absent, body)
					}
				}
			},
		},
		{
			name: "empty type list is omitted",
			args: []string{"-t", ""},
			assert: func(t *testing.T, body map[string]any) {
				if _, ok := body["types"]; ok {
					t.Errorf("types must be omitted: %v", body)
				}
			},
		},
		{
			name: "tags travel as an array",
			args: []string{"--tags", "a,b"},
			assert: func(t *testing.T, body map[string]any) {
				tags, _ := body["tags"].([]any)
				if len(tags) != 2 || tags[0] != "a" {
					t.Errorf("tags = %v", body["tags"])
				}
			},
		},
		{
			name: "empty tags are omitted",
			args: []string{"--tags", ""},
			assert: func(t *testing.T, body map[string]any) {
				if _, ok := body["tags"]; ok {
					t.Errorf("tags must be omitted: %v", body)
				}
			},
		},
		{
			name: "budget is lenient",
			args: []string{"-b", "HIGH"},
			assert: func(t *testing.T, body map[string]any) {
				if body["budget"] != "high" {
					t.Errorf("budget = %v", body["budget"])
				}
			},
		},
		{
			name: "unknown budget becomes mid",
			args: []string{"-b", "enormous"},
			assert: func(t *testing.T, body map[string]any) {
				if body["budget"] != "mid" {
					t.Errorf("budget = %v", body["budget"])
				}
			},
		},
		{
			name: "tags-match is lenient",
			args: []string{"--tags-match", "ALL_STRICT"},
			assert: func(t *testing.T, body map[string]any) {
				if body["tags_match"] != "all_strict" {
					t.Errorf("tags_match = %v", body["tags_match"])
				}
			},
		},
		{
			name: "unknown tags-match becomes any",
			args: []string{"--tags-match", "bogus"},
			assert: func(t *testing.T, body map[string]any) {
				if body["tags_match"] != "any" {
					t.Errorf("tags_match = %v", body["tags_match"])
				}
			},
		},
		{
			name: "include chunks with token budget",
			args: []string{"--include-chunks", "--chunk-max-tokens", "99"},
			assert: func(t *testing.T, body map[string]any) {
				include, ok := body["include"].(map[string]any)
				if !ok {
					t.Fatalf("include missing: %v", body)
				}
				chunks, _ := include["chunks"].(map[string]any)
				if chunks["max_tokens"] != float64(99) {
					t.Errorf("include.chunks = %v", include["chunks"])
				}
			},
		},
		{
			name: "query timestamp travels verbatim",
			args: []string{"--query-timestamp", "2024-03-01T05:06:07+03:00"},
			assert: func(t *testing.T, body map[string]any) {
				if body["query_timestamp"] != "2024-03-01T05:06:07+03:00" {
					t.Errorf("query_timestamp = %v", body["query_timestamp"])
				}
			},
		},
		{
			name: "trace and prefer flags",
			args: []string{"--trace", "--prefer-observations"},
			assert: func(t *testing.T, body map[string]any) {
				if body["trace"] != true || body["prefer_observations"] != true {
					t.Errorf("booleans = %v/%v", body["trace"], body["prefer_observations"])
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
				return 200, `{"results":[]}`
			})
			args := append([]string{"memory", "recall", "b1", "the question"}, tc.args...)
			r := runCommand(t, srv.URL, FormatPretty, args, nil)
			if r.err != nil {
				t.Fatalf("err = %v", r.err)
			}
			call := calls()[0]
			if call.Method != "POST" || call.Path != "/v1/default/banks/b1/memories/recall" {
				t.Fatalf("request = %s %s", call.Method, call.Path)
			}
			tc.assert(t, decodeRecallBody(t, call.Body))
		})
	}
}

func TestRecallRendering(t *testing.T) {
	const page = `{
	  "results": [
	    {"id":"r1","text":"first result text","type":"world","context":"ctx one","occurred_start":"2024-01-01T00:00:00Z","occurred_end":"2024-01-02T00:00:00Z","chunk_id":"c1","document_id":"d1"},
	    {"id":"r2","text":"second","type":"experience","occurred_start":"2024-02-01T00:00:00Z"}
	  ],
	  "chunks": {"c1": {"id":"c1","text":"chunk line 1\nchunk line 2","chunk_index":2,"truncated":true}},
	  "trace": {"summary": {"total_duration_ms": 12.456}, "activation_count": 7}
	}`
	srv, _ := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, page
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{
		"memory", "recall", "b1", "q", "--include-chunks", "--trace",
	}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	for _, want := range []string{
		"Search Results (2)",
		"Result #1",
		"[WORLD]",
		"  first result text",
		"  context: ctx one",
		"  date: 2024-01-01T00:00:00Z - 2024-01-02T00:00:00Z",
		"  document: d1",
		"Result #2",
		"[EXPERIENCE]",
		"  date: 2024-02-01T00:00:00Z",
		"  ─── Source Chunk ───",
		"  chunk line 1",
		"  chunk line 2",
		"  [Truncated due to token limit]",
		"  Chunk ID: c1 | Index: 2",
		"Trace",
		"  total time: 12.46ms",
		"  activation count: 7",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, r.stdout)
		}
	}
	// Scores are not rendered in pretty mode.
	if strings.Contains(r.stdout, "score") {
		t.Errorf("scores leaked:\n%s", r.stdout)
	}
}

func TestRecallEmptyResults(t *testing.T) {
	srv, _ := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"results":[]}`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"memory", "recall", "b1", "q"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if !strings.Contains(r.stdout, "Search Results (0)") || !strings.Contains(r.stdout, "No results found.") {
		t.Errorf("stdout:\n%s", r.stdout)
	}
}

func TestRecallVerbosePrintsRequestBody(t *testing.T) {
	srv, _ := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"results":[]}`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"memory", "recall", "b1", "q", "-v", "--include-chunks"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if !strings.Contains(r.stderr, "Request body: {") {
		t.Errorf("stderr:\n%s", r.stderr)
	}
	if !strings.Contains(r.stderr, "\"max_tokens\": 4096") {
		t.Errorf("stderr missing pretty-printed body:\n%s", r.stderr)
	}
}

func TestRecallMachineOutputPassesResponseThrough(t *testing.T) {
	srv, _ := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"results":[{"id":"r1","scores":{"final":0.5}}],"source_facts_truncated":true}`
	})
	r := runCommand(t, srv.URL, FormatJSON, []string{"memory", "recall", "b1", "q"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if !strings.Contains(r.stdout, "\"source_facts_truncated\": true") {
		t.Errorf("json output:\n%s", r.stdout)
	}
}
