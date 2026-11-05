package extraction

import "testing"

// The response schema declares from_attachments as integers: the model is told
// to answer with 1-based attachment positions, not string identifiers.
func TestFromAttachmentsSchemaDeclaresIntegers(t *testing.T) {
	stub, _ := extractWithMode(t, Options{}, map[string]any{"facts": []any{}})
	prop := stub.lastReq.ResponseSchema.Properties["facts"].Items.Properties["from_attachments"]
	if prop == nil || prop.Items == nil || prop.Items.Type != "integer" {
		t.Fatalf("from_attachments items = %+v, want integer", prop)
	}
}

// Only integral JSON numbers are attachment positions; a stringified index is
// dropped, matching the reference's isinstance(n, int) filter.
func TestFromAttachmentsAcceptsOnlyIntegers(t *testing.T) {
	got := attachmentNumbers([]any{float64(1), "2", float64(3), true, float64(2.5)})
	want := []string{"1", "3"}
	if len(got) != len(want) {
		t.Fatalf("positions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("positions = %v, want %v", got, want)
		}
	}
}
