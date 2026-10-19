package banktemplate

import (
	"encoding/json"
	"testing"
)

// The reference's bank section renders in BankTemplateConfig's declaration
// order. A Go map renders sorted, which is the same values in different bytes —
// and the served document is the contract, so the order is pinned here.
func TestManifestRendersTheBankSectionInDeclarationOrder(t *testing.T) {
	manifest := Manifest{
		Version: CurrentTemplateVersion,
		Bank: map[string]any{
			"audit_log_enabled":      true,
			"memory_defense":         nil,
			"reflect_mission":        "m",
			"retain_extraction_mode": "concise",
		},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"version":"1","bank":{"reflect_mission":"m","retain_extraction_mode":"concise","audit_log_enabled":true,"memory_defense":null},"mental_models":null,"directives":null}`
	if string(encoded) != want {
		t.Fatalf("manifest = %s\nwant      %s", encoded, want)
	}
}

// A bank with no stored config exports a null bank section, not an empty
// object, and a key the order list has not learned about still serialises.
func TestManifestBankSectionNullAndUnknownKeys(t *testing.T) {
	encoded, err := json.Marshal(Manifest{Version: CurrentTemplateVersion})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"version":"1","bank":null,"mental_models":null,"directives":null}`
	if string(encoded) != want {
		t.Fatalf("manifest = %s, want %s", encoded, want)
	}

	encoded, err = json.Marshal(Manifest{Version: CurrentTemplateVersion, Bank: map[string]any{"zz_new": 1, "reflect_mission": "m"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(encoded); got != `{"version":"1","bank":{"reflect_mission":"m","zz_new":1},"mental_models":null,"directives":null}` {
		t.Fatalf("manifest = %s", got)
	}
}
