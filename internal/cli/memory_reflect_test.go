package cli

// The reflect tests: request field shapes, schema handling, lenient enum
// parsing, and the pretty rendering of the reflection.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const reflectResponse = `{
  "text": "the answer",
  "based_on": {"memories": [{"id":"m1"},{"id":"m2"}], "mental_models": [], "directives": []},
  "structured_output": {"kind": "table", "rows": 3},
  "usage": {"input_tokens": 5, "output_tokens": 5, "total_tokens": 10}
}`

func TestReflectRequestBody(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		assert func(t *testing.T, body map[string]any)
	}{
		{
			name: "defaults substitute 4096 max tokens",
			args: nil,
			assert: func(t *testing.T, body map[string]any) {
				if body["query"] != "the question" || body["budget"] != "mid" || body["max_tokens"] != float64(4096) {
					t.Errorf("core fields = %v/%v/%v", body["query"], body["budget"], body["max_tokens"])
				}
				if body["apply_all_directives"] != false {
					t.Errorf("apply_all_directives = %v", body["apply_all_directives"])
				}
				if body["tags_match"] != "any" {
					t.Errorf("tags_match = %v", body["tags_match"])
				}
				for _, absent := range []string{"context", "include", "response_schema", "tags", "fact_types", "exclude_mental_model_ids"} {
					if _, ok := body[absent]; ok {
						t.Errorf("%s must be absent: %v", absent, body)
					}
				}
			},
		},
		{
			name: "explicit max tokens wins",
			args: []string{"-m", "55"},
			assert: func(t *testing.T, body map[string]any) {
				if body["max_tokens"] != float64(55) {
					t.Errorf("max_tokens = %v", body["max_tokens"])
				}
			},
		},
		{
			name: "an explicit max tokens of zero travels as given",
			args: []string{"--max-tokens", "0"},
			assert: func(t *testing.T, body map[string]any) {
				if body["max_tokens"] != float64(0) {
					t.Errorf("max_tokens = %v, want 0 (only an omitted flag defaults)", body["max_tokens"])
				}
			},
		},
		{
			name: "context and budget",
			args: []string{"-c", "meeting notes", "-b", "LOW"},
			assert: func(t *testing.T, body map[string]any) {
				if body["context"] != "meeting notes" || body["budget"] != "low" {
					t.Errorf("context/budget = %v/%v", body["context"], body["budget"])
				}
			},
		},
		{
			name: "include facts is an empty options object",
			args: []string{"--include-facts"},
			assert: func(t *testing.T, body map[string]any) {
				include, ok := body["include"].(map[string]any)
				if !ok {
					t.Fatalf("include missing: %v", body)
				}
				facts, ok := include["facts"].(map[string]any)
				if !ok || len(facts) != 0 {
					t.Errorf("include.facts = %v", include["facts"])
				}
			},
		},
		{
			name: "fact types are lowercased, unknown entries dropped",
			args: []string{"--fact-types", "World,experiences,OBSERVATION,bogus"},
			assert: func(t *testing.T, body map[string]any) {
				types, _ := body["fact_types"].([]any)
				if len(types) != 2 || types[0] != "world" || types[1] != "observation" {
					t.Errorf("fact_types = %v", body["fact_types"])
				}
			},
		},
		{
			name: "fact types with no mapped entry still travel",
			args: []string{"--fact-types", "bogus"},
			assert: func(t *testing.T, body map[string]any) {
				types, ok := body["fact_types"].([]any)
				if !ok || len(types) != 0 {
					t.Errorf("fact_types = %v (present=%v), want []", body["fact_types"], ok)
				}
			},
		},
		{
			name: "exclude flags",
			args: []string{"--exclude-mental-models", "--exclude-mental-model-ids", "mm1,mm2", "--tags", "t1"},
			assert: func(t *testing.T, body map[string]any) {
				if body["exclude_mental_models"] != true {
					t.Errorf("exclude_mental_models = %v", body["exclude_mental_models"])
				}
				ids, _ := body["exclude_mental_model_ids"].([]any)
				if len(ids) != 2 {
					t.Errorf("exclude_mental_model_ids = %v", body["exclude_mental_model_ids"])
				}
				tags, _ := body["tags"].([]any)
				if len(tags) != 1 {
					t.Errorf("tags = %v", body["tags"])
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
				return 200, reflectResponse
			})
			args := append([]string{"memory", "reflect", "b1", "the question"}, tc.args...)
			r := runCommand(t, srv.URL, FormatPretty, args, nil)
			if r.err != nil {
				t.Fatalf("err = %v", r.err)
			}
			call := calls()[0]
			if call.Method != "POST" || call.Path != "/v1/default/banks/b1/reflect" {
				t.Fatalf("request = %s %s", call.Method, call.Path)
			}
			tc.assert(t, decodeRecallBody(t, call.Body))
		})
	}
}

func TestReflectSchemaHandling(t *testing.T) {
	t.Run("schema travels as the response schema", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "schema.json")
		if err := os.WriteFile(path, []byte(`{"type":"object"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
			return 200, reflectResponse
		})
		r := runCommand(t, srv.URL, FormatPretty, []string{"memory", "reflect", "b1", "q", "-s", path}, nil)
		if r.err != nil {
			t.Fatalf("err = %v", r.err)
		}
		body := decodeRecallBody(t, calls()[0].Body)
		schema, ok := body["response_schema"].(map[string]any)
		if !ok || schema["type"] != "object" {
			t.Errorf("response_schema = %v", body["response_schema"])
		}
	})
	t.Run("missing file", func(t *testing.T) {
		srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
			return 200, reflectResponse
		})
		r := runCommand(t, srv.URL, FormatPretty, []string{"memory", "reflect", "b1", "q", "-s", "/nope/schema.json"}, nil)
		if r.err == nil || r.err.Error() != "Failed to read schema file: /nope/schema.json" {
			t.Fatalf("err = %v", r.err)
		}
		if got := calls(); len(got) != 0 {
			t.Fatalf("requests = %d", len(got))
		}
	})
	t.Run("malformed json", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "schema.json")
		if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
			return 200, reflectResponse
		})
		r := runCommand(t, srv.URL, FormatPretty, []string{"memory", "reflect", "b1", "q", "-s", path}, nil)
		if r.err == nil || r.err.Error() != "Failed to parse JSON schema from: "+path {
			t.Fatalf("err = %v", r.err)
		}
		if got := calls(); len(got) != 0 {
			t.Fatalf("requests = %d", len(got))
		}
	})
}

func TestReflectPrettyRendering(t *testing.T) {
	srv, _ := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, reflectResponse
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"memory", "reflect", "b1", "q"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	for _, want := range []string{
		"Reflection",
		"the answer",
		"Based on 2 memory units",
		"─── Structured Output ───",
		"\"kind\": \"table\"",
		"\"rows\": 3",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, r.stdout)
		}
	}
	// Usage and trace are not rendered in pretty mode.
	if strings.Contains(r.stdout, "tokens") || strings.Contains(r.stdout, "usage") {
		t.Errorf("usage leaked:\n%s", r.stdout)
	}
}

func TestReflectMachineOutputIsRawResponse(t *testing.T) {
	srv, _ := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, reflectResponse
	})
	r := runCommand(t, srv.URL, FormatJSON, []string{"memory", "reflect", "b1", "q"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	for _, want := range []string{"\"usage\"", "\"total_tokens\": 10", "\"structured_output\""} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, "Structured Output") {
		t.Errorf("pretty rendering leaked into json output:\n%s", r.stdout)
	}
}
