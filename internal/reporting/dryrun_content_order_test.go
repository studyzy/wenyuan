package reporting

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The reference's validation report echoes `ctx` in the order the model declares
// it, and `input` as the caller's own mapping — insertion order included. Both
// are a byte-level part of the contract (a Go map renders its members
// alphabetically), so these cases assert the exact rendering rather than a
// decoded comparison.
func TestDryRunExtractThisRejectsWithTheReferenceReport(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "an unknown discriminator keeps ctx in declaration order",
			body: `{"content":[{"type":"bogus"}]}`,
			want: `{"detail":[` +
				`{"type":"string_type","loc":["body","content","str"],"msg":"Input should be a valid string","input":[{"type":"bogus"}]},` +
				`{"type":"union_tag_invalid","loc":["body","content","list[tagged-union[TextContentBlock,ImageContentBlock,FileContentBlock]]",0],` +
				`"msg":"Input tag 'bogus' found using 'type' does not match any of the expected tags: 'text', 'image', 'file'",` +
				`"input":{"type":"bogus"},` +
				`"ctx":{"discriminator":"'type'","tag":"bogus","expected_tags":"'text', 'image', 'file'"}}]}`,
		},
		{
			name: "a non-string discriminator is a tag mismatch, not an absent tag",
			body: `{"content":[{"type":5}]}`,
			want: `{"detail":[` +
				`{"type":"string_type","loc":["body","content","str"],"msg":"Input should be a valid string","input":[{"type":5}]},` +
				`{"type":"union_tag_invalid","loc":["body","content","list[tagged-union[TextContentBlock,ImageContentBlock,FileContentBlock]]",0],` +
				`"msg":"Input tag '5' found using 'type' does not match any of the expected tags: 'text', 'image', 'file'",` +
				`"input":{"type":5},` +
				`"ctx":{"discriminator":"'type'","tag":"5","expected_tags":"'text', 'image', 'file'"}}]}`,
		},
		{
			name: "a discarded block echoes the caller's member order",
			body: `{"content":[{"type":"text","text":"x"},{"type":"text"}]}`,
			want: `{"detail":[` +
				`{"type":"string_type","loc":["body","content","str"],"msg":"Input should be a valid string","input":[{"type":"text","text":"x"},{"type":"text"}]},` +
				`{"type":"missing","loc":["body","content","list[tagged-union[TextContentBlock,ImageContentBlock,FileContentBlock]]",1,"text","text"],` +
				`"msg":"Field required","input":{"type":"text"}}]}`,
		},
		{
			name: "a missing attachment member echoes its source object as written",
			body: `{"content":[{"type":"image","source":{"type":"base64","data":"aGk="}}]}`,
			want: `{"detail":[` +
				`{"type":"string_type","loc":["body","content","str"],"msg":"Input should be a valid string",` +
				`"input":[{"type":"image","source":{"type":"base64","data":"aGk="}}]},` +
				`{"type":"missing","loc":["body","content","list[tagged-union[TextContentBlock,ImageContentBlock,FileContentBlock]]",0,"image","source","media_type"],` +
				`"msg":"Field required","input":{"type":"base64","data":"aGk="}}]}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			handlers := NewHandlers(nil)
			mux := http.NewServeMux()
			handlers.Extraction = nil
			handlers.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
				mux.HandleFunc(method+" "+pattern, handler)
			})
			rec := send(mux, http.MethodPost, "/v1/default/banks/order-bank/memories/dry-run-extract", testCase.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
			}
			var got, want any
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if err := json.Unmarshal([]byte(testCase.want), &want); err != nil {
				t.Fatalf("decode want: %v", err)
			}
			if got, want := rec.Body.String(), testCase.want; got != want {
				t.Fatalf("body\n got: %s\nwant: %s", got, want)
			}
		})
	}
}
