package fileparse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// The remote parsers delegate conversion to a hosted extraction service over
// HTTP, the same two the reference ships (LlamaParse and Vectorize Iris). They
// need no local runtime, which is why they are reasonable to implement here;
// each is registered only when its credentials are configured.

// remotePollInterval is how long each remote parser waits between status polls.
const remotePollInterval = 2 * time.Second

// remoteTimeout bounds how long a remote conversion may take before it is
// reported as a failure.
const remoteTimeout = 300 * time.Second

// contentTypeFor is the MIME type sent with an upload, guessed from the
// filename and defaulting to a generic stream.
func contentTypeFor(filename string) string {
	if ct := mime.TypeByExtension(extOf(filename)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// llamaParseBaseURL is the LlamaIndex Cloud parse endpoint (API v2).
const llamaParseBaseURL = "https://api.cloud.llamaindex.ai/api/v2/parse"

// llamaParseConfiguration is the explicit tier/version v2 requires.
const llamaParseConfiguration = `{"tier": "cost_effective", "version": "latest"}`

// llamaUnsupportedStatuses are the HTTP statuses that mean the service rejected
// the file type rather than that the request failed.
var llamaUnsupportedStatuses = map[int]struct{}{400: {}, 415: {}, 422: {}}

// LlamaParseParser converts a file through the LlamaParse API: it uploads the
// file, polls the parse job, and returns the markdown the job produced.
type LlamaParseParser struct {
	apiKey       string
	baseURL      string
	pollInterval time.Duration
	timeout      time.Duration
	client       *http.Client
}

// NewLlamaParseParser builds a parser for the given LlamaCloud API key.
func NewLlamaParseParser(apiKey string) *LlamaParseParser {
	return &LlamaParseParser{
		apiKey:       apiKey,
		baseURL:      llamaParseBaseURL,
		pollInterval: remotePollInterval,
		timeout:      remoteTimeout,
		client:       &http.Client{},
	}
}

func (p *LlamaParseParser) Name() string { return "llama_parse" }

// Supports is true for every file: the service decides which types it accepts,
// and a rejection surfaces as an UnsupportedFileTypeError from Convert.
func (p *LlamaParseParser) Supports(string, string) bool { return true }

// Convert uploads the file, waits for the parse job, and returns its markdown.
func (p *LlamaParseParser) Convert(data []byte, filename string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()

	jobID, err := p.upload(ctx, data, filename)
	if err != nil {
		return "", err
	}
	if err := p.wait(ctx, jobID, filename); err != nil {
		return "", err
	}
	return p.fetch(ctx, jobID, filename)
}

// upload starts a parse job and returns its id.
func (p *LlamaParseParser) upload(ctx context.Context, data []byte, filename string) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := writer.WriteField("configuration", llamaParseConfiguration); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/upload", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	payload, err := p.doJSON(req, filename, "upload")
	if err != nil {
		return "", err
	}
	id, _ := payload["id"].(string)
	if id == "" {
		return "", fmt.Errorf("LlamaParse upload for '%s' returned no job id", filename)
	}
	return id, nil
}

// wait polls the job until it completes.
func (p *LlamaParseParser) wait(ctx context.Context, jobID, filename string) error {
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/"+jobID, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
		payload, err := p.doJSON(req, filename, "poll job status")
		if err != nil {
			return err
		}
		job, _ := payload["job"].(map[string]any)
		status, _ := job["status"].(string)
		switch status {
		case "COMPLETED":
			return nil
		case "FAILED", "CANCELLED":
			message, _ := job["error_message"].(string)
			if message == "" {
				message = strings.ToLower(status)
			}
			return fmt.Errorf("LlamaParse job failed for '%s': %s", filename, message)
		}
		if err := sleepWithContext(ctx, p.pollInterval); err != nil {
			return fmt.Errorf("LlamaParse job timed out after %s for '%s'", p.timeout, filename)
		}
	}
}

// fetch requests the completed job's markdown.
func (p *LlamaParseParser) fetch(ctx context.Context, jobID, filename string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/"+jobID+"?expand=markdown_full", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	payload, err := p.doJSON(req, filename, "fetch markdown result")
	if err != nil {
		return "", err
	}
	markdown, _ := payload["markdown_full"].(string)
	if markdown == "" {
		return "", fmt.Errorf("no content extracted from '%s'", filename)
	}
	return markdown, nil
}

// doJSON runs a request and decodes a JSON object, mapping the service's status
// codes onto the parser error contract.
func (p *LlamaParseParser) doJSON(req *http.Request, filename, step string) (map[string]any, error) {
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("LlamaParse %s for '%s': %w", step, filename, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		message := fmt.Sprintf("LlamaParse API error during %s for '%s': %d %s — %s",
			step, filename, resp.StatusCode, http.StatusText(resp.StatusCode), strings.TrimSpace(string(body)))
		if _, ok := llamaUnsupportedStatuses[resp.StatusCode]; ok {
			return nil, &UnsupportedFileTypeError{Parser: p.Name(), Filename: filename, Reason: message}
		}
		return nil, fmt.Errorf("%s", message)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("LlamaParse %s for '%s': %w", step, filename, err)
	}
	return payload, nil
}

// irisBaseURL is the Vectorize Iris API root.
const irisBaseURL = "https://api.vectorize.io/v1"

// IrisParser converts a file through the Vectorize Iris extraction service.
type IrisParser struct {
	token        string
	orgID        string
	baseURL      string
	pollInterval time.Duration
	timeout      time.Duration
	client       *http.Client
}

// NewIrisParser builds a parser for the given Vectorize token and organization.
func NewIrisParser(token, orgID string) *IrisParser {
	return &IrisParser{
		token:        token,
		orgID:        orgID,
		baseURL:      irisBaseURL,
		pollInterval: remotePollInterval,
		timeout:      remoteTimeout,
		client:       &http.Client{},
	}
}

func (p *IrisParser) Name() string { return "iris" }

// Supports is true for every file: the service decides which types it accepts.
func (p *IrisParser) Supports(string, string) bool { return true }

// Convert uploads the file, starts an extraction, and waits for its text.
func (p *IrisParser) Convert(data []byte, filename string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()

	fileID, uploadURL, err := p.initUpload(ctx, filename)
	if err != nil {
		return "", err
	}
	if err := p.putUpload(ctx, uploadURL, data, filename); err != nil {
		return "", err
	}
	extractionID, err := p.startExtraction(ctx, fileID, filename)
	if err != nil {
		return "", err
	}
	return p.waitExtraction(ctx, extractionID, filename)
}

// initUpload requests a presigned upload URL and returns the file id and URL.
func (p *IrisParser) initUpload(ctx context.Context, filename string) (string, string, error) {
	payload, err := p.postJSON(ctx, p.baseURL+"/org/"+p.orgID+"/files",
		map[string]string{"name": filename, "contentType": contentTypeFor(filename)}, filename, "file upload init")
	if err != nil {
		return "", "", err
	}
	fileID, _ := payload["fileId"].(string)
	uploadURL, _ := payload["uploadUrl"].(string)
	if fileID == "" || uploadURL == "" {
		return "", "", fmt.Errorf("iris upload init for '%s' returned no file id or URL", filename)
	}
	return fileID, uploadURL, nil
}

// putUpload sends the file bytes to the presigned URL, without the auth header.
func (p *IrisParser) putUpload(ctx context.Context, uploadURL string, data []byte, filename string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentTypeFor(filename))
	return p.do(req, filename, "file upload")
}

// startExtraction starts an extraction job for an uploaded file.
func (p *IrisParser) startExtraction(ctx context.Context, fileID, filename string) (string, error) {
	payload, err := p.postJSON(ctx, p.baseURL+"/org/"+p.orgID+"/extraction",
		map[string]string{"fileId": fileID}, filename, "start extraction")
	if err != nil {
		return "", err
	}
	id, _ := payload["extractionId"].(string)
	if id == "" {
		return "", fmt.Errorf("iris start extraction for '%s' returned no extraction id", filename)
	}
	return id, nil
}

// waitExtraction polls until the extraction is ready and returns its text.
func (p *IrisParser) waitExtraction(ctx context.Context, extractionID, filename string) (string, error) {
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			p.baseURL+"/org/"+p.orgID+"/extraction/"+extractionID, nil)
		if err != nil {
			return "", err
		}
		payload, err := p.doJSON(req, filename, "poll extraction status")
		if err != nil {
			return "", err
		}
		if ready, _ := payload["ready"].(bool); ready {
			data, _ := payload["data"].(map[string]any)
			if success, _ := data["success"].(bool); !success {
				message, _ := data["error"].(string)
				if message == "" {
					message = "unknown error"
				}
				return "", fmt.Errorf("iris extraction failed for '%s': %s", filename, message)
			}
			text, _ := data["text"].(string)
			if text == "" {
				return "", fmt.Errorf("no content extracted from '%s'", filename)
			}
			return text, nil
		}
		if err := sleepWithContext(ctx, p.pollInterval); err != nil {
			return "", fmt.Errorf("iris extraction timed out after %s for '%s'", p.timeout, filename)
		}
	}
}

// postJSON posts a JSON body to the authenticated Iris API and decodes a JSON
// object response.
func (p *IrisParser) postJSON(ctx context.Context, url string, body any, filename, step string) (map[string]any, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.token)
	return p.doJSON(req, filename, step)
}

// do runs a request and reports a non-2xx status through the parser contract.
func (p *IrisParser) do(req *http.Request, filename, step string) error {
	_, err := p.doJSON(req, filename, step)
	return err
}

// doJSON runs a request and decodes a JSON body. A 4xx maps onto an
// UnsupportedFileTypeError (the service rejected the file), a 5xx onto a hard
// error.
func (p *IrisParser) doJSON(req *http.Request, filename, step string) (map[string]any, error) {
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("iris %s for '%s': %w", step, filename, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		message := fmt.Sprintf("Iris API error during %s for '%s': %d %s — %s",
			step, filename, resp.StatusCode, http.StatusText(resp.StatusCode), strings.TrimSpace(string(body)))
		if resp.StatusCode < 500 {
			return nil, &UnsupportedFileTypeError{Parser: p.Name(), Filename: filename, Reason: message}
		}
		return nil, fmt.Errorf("%s", message)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return map[string]any{}, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("iris %s for '%s': %w", step, filename, err)
	}
	return payload, nil
}

// sleepWithContext waits for d, returning the context's error when it expires
// first so a poll loop can report a timeout.
func sleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
