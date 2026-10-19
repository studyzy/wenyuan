package memoryops

import (
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// The reference's content value is a `str | list[ContentBlockItem]` union whose
// field validator rejects a value that carries no content, and whose list arm
// validates every block against the tagged union. Each expectation below is the
// body the live reference serves for the same request.
func TestRetainValidatesContentBlocks(t *testing.T) {
	mux, _ := newServer(t)
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			"a blank string carries no content",
			`{"items":[{"content":""}]}`,
			`{"detail":[{"type":"value_error","loc":["body","items",0,"content"],` +
				`"msg":"Value error, content cannot be empty","input":"","ctx":{"error":{}}}]}`,
		},
		{
			"a whitespace-only string carries no content",
			`{"items":[{"content":"  "}]}`,
			`{"detail":[{"type":"value_error","loc":["body","items",0,"content"],` +
				`"msg":"Value error, content cannot be empty","input":"  ","ctx":{"error":{}}}]}`,
		},
		{
			"an empty block list carries no content",
			`{"items":[{"content":[]}]}`,
			`{"detail":[{"type":"value_error","loc":["body","items",0,"content"],` +
				`"msg":"Value error, content cannot be empty","input":[],"ctx":{"error":{}}}]}`,
		},
		{
			"a block list of one blank text block carries no content",
			`{"items":[{"content":[{"type":"text","text":"  "}]}]}`,
			`{"detail":[{"type":"value_error","loc":["body","items",0,"content"],` +
				`"msg":"Value error, content cannot be empty",` +
				`"input":[{"text":"  ","type":"text"}],"ctx":{"error":{}}}]}`,
		},
		{
			"a block with no discriminator",
			`{"items":[{"content":[{"text":"x"}]}]}`,
			`{"detail":[` +
				`{"type":"string_type","loc":["body","items",0,"content","str"],` +
				`"msg":"Input should be a valid string","input":[{"text":"x"}]},` +
				`{"type":"union_tag_not_found","loc":["body","items",0,"content",` +
				`"list[tagged-union[TextContentBlock,ImageContentBlock,FileContentBlock]]",0],` +
				`"msg":"Unable to extract tag using discriminator 'type'","input":{"text":"x"},` +
				`"ctx":{"discriminator":"'type'"}}]}`,
		},
		{
			"a block with an unknown tag",
			`{"items":[{"content":[{"type":"bogus"}]}]}`,
			`{"detail":[` +
				`{"type":"string_type","loc":["body","items",0,"content","str"],` +
				`"msg":"Input should be a valid string","input":[{"type":"bogus"}]},` +
				`{"type":"union_tag_invalid","loc":["body","items",0,"content",` +
				`"list[tagged-union[TextContentBlock,ImageContentBlock,FileContentBlock]]",0],` +
				`"msg":"Input tag 'bogus' found using 'type' does not match any of the expected tags: ` +
				`'text', 'image', 'file'","input":{"type":"bogus"},` +
				`"ctx":{"discriminator":"'type'","expected_tags":"'text', 'image', 'file'","tag":"bogus"}}]}`,
		},
		{
			"a text block with no text",
			`{"items":[{"content":[{"type":"text","text":"x"},{"type":"text"}]}]}`,
			`{"detail":[` +
				`{"type":"string_type","loc":["body","items",0,"content","str"],` +
				`"msg":"Input should be a valid string","input":[{"text":"x","type":"text"},{"type":"text"}]},` +
				`{"type":"missing","loc":["body","items",0,"content",` +
				`"list[tagged-union[TextContentBlock,ImageContentBlock,FileContentBlock]]",1,"text","text"],` +
				`"msg":"Field required","input":{"type":"text"}}]}`,
		},
		{
			"an image block with no source",
			`{"items":[{"content":[{"type":"image"}]}]}`,
			`{"detail":[` +
				`{"type":"string_type","loc":["body","items",0,"content","str"],` +
				`"msg":"Input should be a valid string","input":[{"type":"image"}]},` +
				`{"type":"missing","loc":["body","items",0,"content",` +
				`"list[tagged-union[TextContentBlock,ImageContentBlock,FileContentBlock]]",0,"image","source"],` +
				`"msg":"Field required","input":{"type":"image"}}]}`,
		},
		{
			"an attachment media type that is not MIME-shaped",
			`{"items":[{"content":[{"type":"image","source":{"type":"base64","media_type":"bad","data":"aGk="}}]}]}`,
			`{"detail":[` +
				`{"type":"string_type","loc":["body","items",0,"content","str"],` +
				`"msg":"Input should be a valid string","input":[{"source":{"data":"aGk=",` +
				`"media_type":"bad","type":"base64"},"type":"image"}]},` +
				`{"type":"value_error","loc":["body","items",0,"content",` +
				`"list[tagged-union[TextContentBlock,ImageContentBlock,FileContentBlock]]",0,"image",` +
				`"source","media_type"],` +
				`"msg":"Value error, media_type must look like 'type/subtype', got 'bad'","input":"bad",` +
				`"ctx":{"error":{}}}]}`,
		},
	}
	for _, tc := range cases {
		rec := post(t, mux, "/v1/default/banks/b1/memories", tc.body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status = %d, body = %s", tc.name, rec.Code, rec.Body.String())
		}
		if got := strings.TrimSpace(rec.Body.String()); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
}

// An attachment payload that is not decodable base64, or is empty, is a request
// error the reference answers with a 400 sentence naming the item and block —
// it is not silently dropped and the write is not accepted.
func TestRetainRejectsUnusableAttachmentPayload(t *testing.T) {
	mux := enableVision(t)
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			"undecodable base64",
			`{"items":[{"content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"!!!!"}}]}]}`,
			`{"detail":"items[0].content[0]: attachment source data is not valid base64: Only base64 data is allowed"}`,
		},
		{
			"empty payload",
			`{"items":[{"content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":""}}]}]}`,
			`{"detail":"items[0].content[0]: attachment source data is empty"}`,
		},
	}
	for _, tc := range cases {
		rec := post(t, mux, "/v1/default/banks/b1/memories", tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, body = %s", tc.name, rec.Code, rec.Body.String())
		}
		if got := strings.TrimSpace(rec.Body.String()); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
}

// Only the canonicalizer may mint a placeholder: a caller-authored token that
// matches no attachment of its own item is scrubbed before the text is stored,
// so one document cannot cite another's image. This is the text the live
// reference stores for the same request.
func TestRetainScrubsForgedAttachmentPlaceholder(t *testing.T) {
	mux, store := newServer(t)
	body := `{"items":[{"content":"the ⟦hs-att:deadbeefcafe⟧ arrived","document_id":"forged"}]}`
	rec := post(t, mux, "/v1/default/banks/b1/memories", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	doc, err := store.GetDocument(t.Context(), "b1", "forged")
	if err != nil {
		t.Fatal(err)
	}
	if doc.OriginalText != "the  arrived" {
		t.Fatalf("stored text = %q, want %q", doc.OriginalText, "the  arrived")
	}
}

// enableVision rebuilds the mux's handler set with a vision-capable write model,
// so a request that carries inline images reaches canonicalization instead of
// being refused before it.
func enableVision(t *testing.T) *http.ServeMux {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(t.Context(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(store, 5, 30000)
	h.VisionEnabled = true
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	return mux
}

// The reference's decoder keeps a missing content member distinct from an empty
// one: the rejection is "Field required", carrying the item the caller sent as
// its input — not an empty object, and not the empty-value rejection.
func TestRetainMissingMemberReportsTheItem(t *testing.T) {
	mux, _ := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories", `{"items":[{"text":"Alice"}]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	want := `{"detail":[{"type":"missing","loc":["body","items",0,"content"],` +
		`"msg":"Field required","input":{"text":"Alice"}}]}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}

// A tags element that is not a string is the reference's `list[str]` member
// validation, reported at its own position rather than the list being coerced.
func TestRetainRejectsNonStringTagElement(t *testing.T) {
	mux, _ := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories", `{"items":[{"content":"x","tags":"[\"a\",1]"}]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	want := `{"detail":[{"type":"string_type","loc":["body","items",0,"tags",1],` +
		`"msg":"Input should be a valid string","input":1}]}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}
