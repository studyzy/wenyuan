package cli

// The retain and retain-files tests: wire shapes for the single-item write,
// the batch slicing and multipart form of the bulk import, and the operation
// polling outcomes.

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const retainResponse = `{"success":true,"bank_id":"b1","items_count":1,"async":false,"usage":{"input_tokens":10,"output_tokens":0,"total_tokens":10},"processed_content_tokens":9}`

func TestRetainRequestShape(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		assert func(t *testing.T, body map[string]any, call capturedRequest)
	}{
		{
			name: "defaults derive a cli_put document id",
			args: nil,
			assert: func(t *testing.T, body map[string]any, call capturedRequest) {
				items, _ := body["items"].([]any)
				if len(items) != 1 {
					t.Fatalf("items = %v", body["items"])
				}
				item, _ := items[0].(map[string]any)
				docID, _ := item["document_id"].(string)
				if !strings.HasPrefix(docID, "cli_put_") {
					t.Fatalf("document_id = %q, want a cli_put id", docID)
				}
				if len(docID) != len("cli_put_20060102_150405") {
					t.Errorf("document_id = %q, want the local-clock shape", docID)
				}
				if item["content"] != "some content" {
					t.Errorf("content = %v", item["content"])
				}
				if item["resolve_entities"] != true {
					t.Errorf("resolve_entities = %v", item["resolve_entities"])
				}
				for _, absent := range []string{"timestamp", "context"} {
					if _, ok := item[absent]; ok {
						t.Errorf("%s must be absent: %v", absent, item)
					}
				}
				if body["async"] != false {
					t.Errorf("async = %v", body["async"])
				}
				if _, ok := body["document_tags"]; ok {
					t.Errorf("document_tags must be absent: %v", body)
				}
				if _, ok := body["operation_id"]; ok {
					t.Errorf("operation_id must never be sent: %v", body)
				}
			},
		},
		{
			name: "explicit doc id and context",
			args: []string{"-d", "my-doc", "-c", "a context"},
			assert: func(t *testing.T, body map[string]any, call capturedRequest) {
				item := body["items"].([]any)[0].(map[string]any)
				if item["document_id"] != "my-doc" || item["context"] != "a context" {
					t.Errorf("item = %v", item)
				}
			},
		},
		{
			name: "empty timestamp travels as empty string",
			args: []string{"-t", ""},
			assert: func(t *testing.T, body map[string]any, call capturedRequest) {
				item := body["items"].([]any)[0].(map[string]any)
				v, ok := item["timestamp"]
				if !ok || v != "" {
					t.Errorf("timestamp = %v (present=%v), want \"\"", v, ok)
				}
			},
		},
		{
			name: "unset timestamp travels verbatim",
			args: []string{"--timestamp", "unset"},
			assert: func(t *testing.T, body map[string]any, call capturedRequest) {
				item := body["items"].([]any)[0].(map[string]any)
				if item["timestamp"] != "unset" {
					t.Errorf("timestamp = %v", item["timestamp"])
				}
			},
		},
		{
			name: "iso timestamp travels verbatim",
			args: []string{"--timestamp", "2024-01-15T10:30:00Z"},
			assert: func(t *testing.T, body map[string]any, call capturedRequest) {
				item := body["items"].([]any)[0].(map[string]any)
				if item["timestamp"] != "2024-01-15T10:30:00Z" {
					t.Errorf("timestamp = %v", item["timestamp"])
				}
			},
		},
		{
			name: "async and document tags",
			args: []string{"--async", "--document-tags", "a,b"},
			assert: func(t *testing.T, body map[string]any, call capturedRequest) {
				if body["async"] != true {
					t.Errorf("async = %v", body["async"])
				}
				tags, _ := body["document_tags"].([]any)
				if len(tags) != 2 {
					t.Errorf("document_tags = %v", body["document_tags"])
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
				return 200, retainResponse
			})
			args := append([]string{"memory", "retain", "b1", "some content"}, tc.args...)
			r := runCommand(t, srv.URL, FormatPretty, args, nil)
			if r.err != nil {
				t.Fatalf("err = %v", r.err)
			}
			call := calls()[0]
			if call.Method != "POST" || call.Path != "/v1/default/banks/b1/memories" {
				t.Fatalf("request = %s %s", call.Method, call.Path)
			}
			tc.assert(t, decodeRecallBody(t, call.Body), call)
		})
	}
}

func TestRetainMissingContentIsUsageError(t *testing.T) {
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, retainResponse
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"memory", "retain", "b1"}, nil)
	if r.err == nil {
		t.Fatal("expected a usage error")
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("requests = %d", len(got))
	}
}

func TestRetainPrettyRendering(t *testing.T) {
	t.Run("synchronous", func(t *testing.T) {
		srv, _ := stubAPI(t, func(call capturedRequest) (int, string) {
			return 200, retainResponse
		})
		r := runCommand(t, srv.URL, FormatPretty, []string{"memory", "retain", "b1", "text"}, nil)
		if r.err != nil {
			t.Fatalf("err = %v", r.err)
		}
		if !strings.Contains(r.stdout, "Memory retained successfully (document: ") {
			t.Errorf("stdout:\n%s", r.stdout)
		}
		if !strings.Contains(r.stdout, "  Stored count: 1") {
			t.Errorf("stdout:\n%s", r.stdout)
		}
	})
	t.Run("asynchronous", func(t *testing.T) {
		srv, _ := stubAPI(t, func(call capturedRequest) (int, string) {
			return 200, `{"success":true,"bank_id":"b1","items_count":1,"async":true,"operation_id":"op1"}`
		})
		r := runCommand(t, srv.URL, FormatPretty, []string{"memory", "retain", "b1", "text", "--async"}, nil)
		if r.err != nil {
			t.Fatalf("err = %v", r.err)
		}
		if !strings.Contains(r.stdout, "  Status: queued for background processing") ||
			!strings.Contains(r.stdout, "  Items: 1") {
			t.Errorf("stdout:\n%s", r.stdout)
		}
	})
}

// writeTempFile creates a file with content under dir.
func writeTempFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// parseMultipartRequest extracts the request field and the file part names
// from a recorded multipart body.
func parseMultipartRequest(t *testing.T, call capturedRequest) (requestField string, fileNames []string) {
	t.Helper()
	_, params, err := mime.ParseMediaType(call.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("multipart content type: %v", err)
	}
	reader := multipart.NewReader(strings.NewReader(call.Body), params["boundary"])
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			return requestField, fileNames
		}
		if err != nil {
			t.Fatalf("multipart parse: %v", err)
		}
		data, _ := io.ReadAll(part)
		_ = part.Close()
		switch part.FormName() {
		case "request":
			requestField = string(data)
		case "files":
			fileNames = append(fileNames, part.FileName())
		}
	}
}

func TestRetainFilesBatchesAndUploads(t *testing.T) {
	dir := t.TempDir()
	var wantStems []string
	for i := 0; i < 23; i++ {
		stem := fmt.Sprintf("file%02d", i)
		writeTempFile(t, filepath.Join(dir, stem+".txt"), fmt.Sprintf("content %d", i))
		wantStems = append(wantStems, stem)
	}
	var batchCounts []int
	var allStems []string
	srv, _ := stubAPI(t, func(call capturedRequest) (int, string) {
		if call.Method != "POST" || call.Path != "/v1/default/banks/b1/files/retain" {
			t.Errorf("unexpected request %s %s", call.Method, call.Path)
			return 400, `{}`
		}
		requestField, files := parseMultipartRequest(t, call)
		var meta struct {
			FilesMetadata []map[string]any `json:"files_metadata"`
		}
		if err := json.Unmarshal([]byte(requestField), &meta); err != nil {
			t.Errorf("request field is not JSON: %v", err)
		}
		if len(meta.FilesMetadata) != len(files) {
			t.Errorf("files_metadata count %d != files count %d", len(meta.FilesMetadata), len(files))
		}
		for _, entry := range meta.FilesMetadata {
			stem, _ := entry["document_id"].(string)
			allStems = append(allStems, stem)
		}
		batchCounts = append(batchCounts, len(files))
		return 200, `{"operation_ids":["op1","op2"]}`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{
		"memory", "retain-files", "b1", dir, "--async",
	}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if len(batchCounts) != 3 || batchCounts[0] != 10 || batchCounts[1] != 10 || batchCounts[2] != 3 {
		t.Errorf("batch sizes = %v, want 10/10/3", batchCounts)
	}
	if len(allStems) != 23 {
		t.Fatalf("stems = %d", len(allStems))
	}
	for i, stem := range wantStems {
		if allStems[i] != stem {
			t.Errorf("stem[%d] = %q, want %q", i, allStems[i], stem)
		}
	}
	if !strings.Contains(r.stdout, "Found 23 file(s) to import") {
		t.Errorf("stdout:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout, "Files queued for processing") || !strings.Contains(r.stdout, "  Files: 23") {
		t.Errorf("stdout:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout, "  Operation ID: op1") || !strings.Contains(r.stdout, "  Operation ID: op2") {
		t.Errorf("stdout:\n%s", r.stdout)
	}
}

func TestRetainFilesMetadataCarriesContextAndStrategy(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, filepath.Join(dir, "note.md"), "hello")
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"operation_ids":["op1"]}`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{
		"memory", "retain-files", "b1", dir, "--async", "-c", "shared context", "-s", "fresh",
	}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	requestField, _ := parseMultipartRequest(t, calls()[0])
	var meta struct {
		FilesMetadata []map[string]any `json:"files_metadata"`
	}
	if err := json.Unmarshal([]byte(requestField), &meta); err != nil {
		t.Fatal(err)
	}
	if len(meta.FilesMetadata) != 1 {
		t.Fatalf("files_metadata = %v", meta.FilesMetadata)
	}
	entry := meta.FilesMetadata[0]
	if entry["context"] != "shared context" || entry["strategy"] != "fresh" || entry["document_id"] != "note" {
		t.Errorf("entry = %v", entry)
	}
}

func TestRetainFilesPollingOutcomes(t *testing.T) {
	cases := []struct {
		name     string
		statuses []string // served per operations-list poll, last one repeats
		errorMsg string
		wantOut  string
		wantErr  string
	}{
		{
			name:     "processing then completed",
			statuses: []string{"processing", "completed"},
			wantOut:  "Files retained successfully",
		},
		{
			name:     "missing operation is settled",
			statuses: []string{"pending", "gone"},
			wantOut:  "Files retained successfully",
		},
		{
			name:     "failed carries the operation message",
			statuses: []string{"failed"},
			errorMsg: "conversion blew up",
			wantOut:  "Retain operation failed: conversion blew up",
			wantErr:  "1 operation(s) failed",
		},
		{
			name:     "cancelled message",
			statuses: []string{"cancelled"},
			wantOut:  "Retain operation failed: Operation was cancelled",
			wantErr:  "1 operation(s) failed",
		},
		{
			name:     "unknown status",
			statuses: []string{"weird"},
			wantOut:  "Retain operation failed: Unknown status: weird",
			wantErr:  "1 operation(s) failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := retainPollInterval
			retainPollInterval = time.Millisecond
			t.Cleanup(func() { retainPollInterval = old })
			dir := t.TempDir()
			writeTempFile(t, filepath.Join(dir, "a.txt"), "x")
			polls := 0
			srv, _ := stubAPI(t, func(call capturedRequest) (int, string) {
				if call.Method == "POST" {
					return 200, `{"operation_ids":["op1"]}`
				}
				status := tc.statuses[len(tc.statuses)-1]
				if polls < len(tc.statuses) {
					status = tc.statuses[polls]
				}
				polls++
				if status == "gone" {
					return 200, `{"operations":[]}`
				}
				errMsg := "null"
				if tc.errorMsg != "" {
					errMsg = fmt.Sprintf("%q", tc.errorMsg)
				}
				return 200, fmt.Sprintf(`{"operations":[{"id":"op1","status":%q,"error_message":%s}]}`, status, errMsg)
			})
			r := runCommand(t, srv.URL, FormatPretty, []string{"memory", "retain-files", "b1", dir}, nil)
			if tc.wantErr == "" {
				if r.err != nil {
					t.Fatalf("err = %v", r.err)
				}
				if !strings.Contains(r.stdout, "  Files processed: 1") {
					t.Errorf("stdout:\n%s", r.stdout)
				}
				if !strings.Contains(r.stdout, tc.wantOut) {
					t.Errorf("stdout missing %q:\n%s", tc.wantOut, r.stdout)
				}
			} else {
				if r.err == nil || r.err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", r.err, tc.wantErr)
				}
				// Per-operation failures go to stderr, as print_error does.
				if !strings.Contains(r.stderr, tc.wantOut) {
					t.Errorf("stderr missing %q:\n%s", tc.wantOut, r.stderr)
				}
			}
		})
	}
}

func TestRetainFilesMachineOutput(t *testing.T) {
	t.Run("async json is the synthesized body", func(t *testing.T) {
		dir := t.TempDir()
		writeTempFile(t, filepath.Join(dir, "a.txt"), "x")
		srv, _ := stubAPI(t, func(call capturedRequest) (int, string) {
			return 200, `{"operation_ids":["op1"]}`
		})
		r := runCommand(t, srv.URL, FormatJSON, []string{"memory", "retain-files", "b1", dir, "--async"}, nil)
		if r.err != nil {
			t.Fatalf("err = %v", r.err)
		}
		if !strings.Contains(r.stdout, "\"operation_ids\": [\n    \"op1\"\n  ]") {
			t.Errorf("stdout:\n%s", r.stdout)
		}
	})
	t.Run("sync json carries files count", func(t *testing.T) {
		old := retainPollInterval
		retainPollInterval = time.Millisecond
		t.Cleanup(func() { retainPollInterval = old })
		dir := t.TempDir()
		writeTempFile(t, filepath.Join(dir, "a.txt"), "x")
		srv, _ := stubAPI(t, func(call capturedRequest) (int, string) {
			if call.Method == "POST" {
				return 200, `{"operation_ids":["op1"]}`
			}
			return 200, `{"operations":[{"id":"op1","status":"completed"}]}`
		})
		r := runCommand(t, srv.URL, FormatJSON, []string{"memory", "retain-files", "b1", dir}, nil)
		if r.err != nil {
			t.Fatalf("err = %v", r.err)
		}
		for _, want := range []string{"\"success\": true", "\"files_count\": 1", "\"operation_ids\""} {
			if !strings.Contains(r.stdout, want) {
				t.Errorf("stdout missing %q:\n%s", want, r.stdout)
			}
		}
	})
}

func TestRetainFilesPathRules(t *testing.T) {
	t.Run("missing path", func(t *testing.T) {
		srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
			return 200, `{}`
		})
		missing := filepath.Join(t.TempDir(), "nope")
		r := runCommand(t, srv.URL, FormatPretty, []string{"memory", "retain-files", "b1", missing}, nil)
		if r.err == nil || r.err.Error() != "Path does not exist: "+missing {
			t.Fatalf("err = %v", r.err)
		}
		if got := calls(); len(got) != 0 {
			t.Fatalf("requests = %d", len(got))
		}
	})
	t.Run("single file is used directly regardless of extension", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "data.weird")
		writeTempFile(t, file, "x")
		srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
			return 200, `{"operation_ids":["op1"]}`
		})
		r := runCommand(t, srv.URL, FormatPretty, []string{"memory", "retain-files", "b1", file, "--async"}, nil)
		if r.err != nil {
			t.Fatalf("err = %v", r.err)
		}
		_, files := parseMultipartRequest(t, calls()[0])
		if len(files) != 1 || files[0] != "data.weird" {
			t.Errorf("files = %v", files)
		}
	})
	t.Run("directory walk filters extensions", func(t *testing.T) {
		dir := t.TempDir()
		writeTempFile(t, filepath.Join(dir, "keep.txt"), "x")
		writeTempFile(t, filepath.Join(dir, "keep.MD"), "x")
		writeTempFile(t, filepath.Join(dir, "skip.exe"), "x")
		writeTempFile(t, filepath.Join(dir, "noext"), "x")
		writeTempFile(t, filepath.Join(dir, "sub", "nested.md"), "x")
		srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
			return 200, `{"operation_ids":["op1"]}`
		})
		r := runCommand(t, srv.URL, FormatPretty, []string{"memory", "retain-files", "b1", dir, "--async"}, nil)
		if r.err != nil {
			t.Fatalf("err = %v", r.err)
		}
		_, files := parseMultipartRequest(t, calls()[0])
		if len(files) != 3 {
			t.Fatalf("files = %v, want txt, MD and nested md", files)
		}
		// Without --recursive the subdirectory is skipped.
		srv2, calls2 := stubAPI(t, func(call capturedRequest) (int, string) {
			return 200, `{"operation_ids":["op1"]}`
		})
		r = runCommand(t, srv2.URL, FormatPretty, []string{"memory", "retain-files", "b1", dir, "--async", "-r=false"}, nil)
		if r.err != nil {
			t.Fatalf("err = %v", r.err)
		}
		_, files = parseMultipartRequest(t, calls2()[0])
		if len(files) != 2 {
			t.Fatalf("files = %v, want only the top-level matches", files)
		}
	})
	t.Run("empty set exits zero", func(t *testing.T) {
		dir := t.TempDir()
		writeTempFile(t, filepath.Join(dir, "skip.exe"), "x")
		srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
			return 200, `{}`
		})
		r := runCommand(t, srv.URL, FormatPretty, []string{"memory", "retain-files", "b1", dir}, nil)
		if r.err != nil {
			t.Fatalf("err = %v", r.err)
		}
		if !strings.Contains(r.stdout, "No supported files found. Supported formats: pdf, docx, pptx, xlsx, jpg, png, html, txt, md, csv, mp3, wav, and more.") {
			t.Errorf("stdout:\n%s", r.stdout)
		}
		if got := calls(); len(got) != 0 {
			t.Fatalf("requests = %d", len(got))
		}
	})
}

func TestRetainFilesVerbosePrintsPostURL(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, filepath.Join(dir, "a.txt"), "x")
	srv, _ := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"operation_ids":["op1"]}`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"memory", "retain-files", "b1", dir, "--async", "-v"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if !strings.HasPrefix(strings.TrimSpace(r.stderr), "POST ") {
		t.Errorf("stderr:\n%s", r.stderr)
	}
}
