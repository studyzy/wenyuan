package bankapi

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// maxBankBodyBytes bounds a bank/alias request body.
const maxBankBodyBytes = 1 << 20

// decodeRequest decodes a JSON request body into target, recording the fields
// the target does not declare, and returns the raw object so a missing-field
// item can carry it as input the way the framework does.
func decodeRequest(r *http.Request, target any) (map[string]any, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBankBodyBytes))
	if err != nil {
		return nil, httpx.BodyDecodeFailure(err, body)
	}
	if len(body) == 0 {
		// A request that carried no body at all is the framework's item for a
		// missing required field, not a syntax failure.
		return nil, httpx.BodyDecodeFailure(httpx.ErrEmptyBody, body)
	}
	decoded, err := httpx.DecodeLax(body, target)
	httpx.RecordUnknownBodyFields(r, decoded, target)
	if err != nil {
		return nil, httpx.BodyDecodeFailure(err, decoded)
	}
	object := map[string]any{}
	if len(decoded) > 0 {
		if err := json.Unmarshal(decoded, &object); err != nil {
			return nil, httpx.BodyDecodeFailure(err, decoded)
		}
	}
	return object, nil
}
