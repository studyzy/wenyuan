package memoryops

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
)

// The `request` form part is validated by the reference's own model, whose
// failure is the model library's multi-line report wrapped in the handler's
// "Invalid request JSON: " sentence. The wording, the member order and the
// `[type=..., input_value=..., input_type=...]` suffix are on the wire, so they
// are pinned here.
func TestFileRetainRequestReportMatchesTheLibrary(t *testing.T) {
	mux, _ := newServer(t)
	cases := []struct {
		name     string
		request  string
		wantBody string
	}{
		{
			name:    "per-file fields at the root",
			request: `{"tags":["x"]}`,
			wantBody: "Invalid request JSON: 1 validation error for FileRetainRequest\n" +
				"  Value error, Per-file fields (tags) must be placed in the corresponding " +
				"'files_metadata' entry [type=value_error, input_value={'tags': ['x']}, input_type=dict]\n" +
				"    For further information visit https://errors.pydantic.dev/2.12/v/value_error",
		},
		{
			name:    "misplaced fields are sorted and joined",
			request: `{"document_id":"d","context":"c"}`,
			wantBody: "Invalid request JSON: 1 validation error for FileRetainRequest\n" +
				"  Value error, Per-file fields (context, document_id) must be placed in the corresponding " +
				"'files_metadata' entry [type=value_error, input_value={'document_id': 'd', 'context': 'c'}, " +
				"input_type=dict]\n" +
				"    For further information visit https://errors.pydantic.dev/2.12/v/value_error",
		},
		{
			name:    "a body that is not an object",
			request: `[]`,
			wantBody: "Invalid request JSON: 1 validation error for FileRetainRequest\n" +
				"  Input should be an object [type=model_type, input_value=[], input_type=list]\n" +
				"    For further information visit https://errors.pydantic.dev/2.12/v/model_type",
		},
		{
			name:    "a parser of the wrong type reports both union arms",
			request: `{"parser":5}`,
			wantBody: "Invalid request JSON: 2 validation errors for FileRetainRequest\n" +
				"parser.str\n  Input should be a valid string [type=string_type, input_value=5, input_type=int]\n" +
				"    For further information visit https://errors.pydantic.dev/2.12/v/string_type\n" +
				"parser.list[str]\n  Input should be a valid array [type=list_type, input_value=5, input_type=int]\n" +
				"    For further information visit https://errors.pydantic.dev/2.12/v/list_type",
		},
		{
			name:    "a metadata entry that is null",
			request: `{"files_metadata":[null]}`,
			wantBody: "Invalid request JSON: 1 validation error for FileRetainRequest\n" +
				"files_metadata.0\n  Input should be an object [type=model_type, input_value=None, " +
				"input_type=NoneType]\n" +
				"    For further information visit https://errors.pydantic.dev/2.12/v/model_type",
		},
		{
			name:    "malformed JSON names the position",
			request: `{bad`,
			wantBody: "Invalid request JSON: 1 validation error for FileRetainRequest\n" +
				"  Invalid JSON: key must be a string at line 1 column 2 " +
				"[type=json_invalid, input_value='{bad', input_type=str]\n" +
				"    For further information visit https://errors.pydantic.dev/2.12/v/json_invalid",
		},
		{
			name:    "a truncated document says what was open",
			request: `{"a":`,
			wantBody: "Invalid request JSON: 1 validation error for FileRetainRequest\n" +
				"  Invalid JSON: EOF while parsing a value at line 1 column 5 " +
				"[type=json_invalid, input_value='{\"a\":', input_type=str]\n" +
				"    For further information visit https://errors.pydantic.dev/2.12/v/json_invalid",
		},
		{
			name:    "text after the document",
			request: `{"a":1}x`,
			wantBody: "Invalid request JSON: 1 validation error for FileRetainRequest\n" +
				"  Invalid JSON: trailing characters at line 1 column 8 " +
				"[type=json_invalid, input_value='{\"a\":1}x', input_type=str]\n" +
				"    For further information visit https://errors.pydantic.dev/2.12/v/json_invalid",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := uploadFileRequest(t, mux, "a.txt", "hello", tc.request)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
			}
			want := `{"detail":` + jsonString(tc.wantBody) + `}`
			if got := strings.TrimSuffix(rec.Body.String(), "\n"); got != want {
				t.Fatalf("body = %s\nwant %s", got, want)
			}
		})
	}
}

// jsonString renders s as a JSON string literal, which is how the envelope
// carries the report.
func jsonString(s string) string {
	encoded, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// uploadFileRequest posts one file with a raw `request` form value.
func uploadFileRequest(t *testing.T, mux *http.ServeMux, filename, body, request string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="files"; filename="`+filename+`"`)
	header.Set("Content-Type", "text/plain")
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("request", request); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/files/retain", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}
