package fileparse

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestLlamaParser points a LlamaParse parser at a local stand-in server.
func newTestLlamaParser(t *testing.T, handler http.Handler) *LlamaParseParser {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &LlamaParseParser{
		apiKey:       "test-key",
		baseURL:      server.URL,
		pollInterval: time.Millisecond,
		timeout:      time.Second,
		client:       server.Client(),
	}
}

// The LlamaParse parser uploads the file, polls the job to completion, and
// returns the job's markdown.
func TestLlamaParseConverts(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"id":"job-1"}`))
	})
	mux.HandleFunc("/job-1", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("expand") == "markdown_full" {
			_, _ = w.Write([]byte(`{"markdown_full":"# Converted"}`))
			return
		}
		_, _ = w.Write([]byte(`{"job":{"status":"COMPLETED"}}`))
	})
	parser := newTestLlamaParser(t, mux)
	out, err := parser.Convert([]byte("bytes"), "doc.pdf")
	if err != nil {
		t.Fatalf("llama convert: %v", err)
	}
	if out != "# Converted" {
		t.Fatalf("llama output = %q", out)
	}
	if !parser.Supports("anything", "") {
		t.Fatal("a remote parser accepts every file for detection")
	}
}

// A 4xx from LlamaParse is reported as unsupported, so the chain moves on.
func TestLlamaParseUnsupportedStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unsupported", http.StatusUnprocessableEntity)
	})
	parser := newTestLlamaParser(t, mux)
	_, err := parser.Convert([]byte("bytes"), "doc.bin")
	if !IsUnsupported(err) {
		t.Fatalf("a 422 must map to unsupported, got %v", err)
	}
}

// A failed job is a hard error, not an unsupported-file fallthrough.
func TestLlamaParseJobFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/upload", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"job-2"}`))
	})
	mux.HandleFunc("/job-2", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"job":{"status":"FAILED","error_message":"nope"}}`))
	})
	parser := newTestLlamaParser(t, mux)
	_, err := parser.Convert([]byte("bytes"), "doc.pdf")
	if err == nil || IsUnsupported(err) {
		t.Fatalf("a failed job = %v, want a hard error", err)
	}
}

// The Iris parser uploads to the presigned URL, starts extraction, polls, and
// returns the extracted text.
func TestIrisConverts(t *testing.T) {
	var uploaded atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/org/org-1/files", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"fileId": "f1", "uploadUrl": "http://" + r.Host + "/upload/f1",
		})
	})
	mux.HandleFunc("/upload/f1", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			http.Error(w, "presigned upload must not carry auth", http.StatusUnauthorized)
			return
		}
		uploaded.Store(true)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/org/org-1/extraction", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"extractionId":"e1"}`))
	})
	mux.HandleFunc("/org/org-1/extraction/e1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ready":true,"data":{"success":true,"text":"iris text"}}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	parser := &IrisParser{
		token: "tok", orgID: "org-1", baseURL: server.URL,
		pollInterval: time.Millisecond, timeout: time.Second, client: server.Client(),
	}
	out, err := parser.Convert([]byte("bytes"), "doc.pdf")
	if err != nil {
		t.Fatalf("iris convert: %v", err)
	}
	if out != "iris text" {
		t.Fatalf("iris output = %q", out)
	}
	if !uploaded.Load() {
		t.Fatal("the presigned upload never happened")
	}
}

// A 4xx from Iris is reported as unsupported.
func TestIrisUnsupportedStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/org/org-1/files", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad type", http.StatusBadRequest)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	parser := &IrisParser{
		token: "tok", orgID: "org-1", baseURL: server.URL,
		pollInterval: time.Millisecond, timeout: time.Second, client: server.Client(),
	}
	_, err := parser.Convert([]byte("bytes"), "doc.bin")
	if !IsUnsupported(err) {
		t.Fatalf("a 400 must map to unsupported, got %v", err)
	}
	if !strings.Contains(err.Error(), "bad type") {
		t.Fatalf("the refusal should carry the service message: %v", err)
	}
}
