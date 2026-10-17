package reporting

import (
	_ "embed"
	"encoding/json"
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// bankTemplateSchemaJSON is the reference's generated JSON Schema for the bank
// template manifest, taken verbatim from `BankTemplateManifest.model_json_schema()`.
// Serving the generated document rather than a hand-built approximation keeps the
// field set, required list, enums, defaults and `$defs` in step with the model.
//
//go:embed bank_template_schema.json
var bankTemplateSchemaJSON []byte

// BankTemplateSchema reports the JSON Schema for the bank template manifest
// format, so a client can validate a manifest before importing it.
func (h *Handlers) BankTemplateSchema(w http.ResponseWriter, r *http.Request) {
	schema, err := bankTemplateManifestSchema()
	if err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindInternal, "bank template schema unavailable"))
		return
	}
	// The reference returns a bare dict here, not a response model, so its nulls
	// (every optional field's `"default": null`) are always on the wire. Keep them.
	httpx.WriteJSON(w, http.StatusOK, schema)
}

// bankTemplateManifestSchema decodes the embedded reference schema.
func bankTemplateManifestSchema() (map[string]any, error) {
	var schema map[string]any
	if err := json.Unmarshal(bankTemplateSchemaJSON, &schema); err != nil {
		return nil, err
	}
	return schema, nil
}
