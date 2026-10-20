package bankapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// The two retired write routes still declare the request bodies they had before
// retirement, and the framework validates a declared body BEFORE the handler
// runs. So a caller sending an unparseable body learns the route was retired
// (410) only when the body is acceptable; a body the model rejects is answered
// 422 first, exactly as it was while the route was live.
//
// The handlers never look at the body — they answer the terminal status — but
// the shapes below match the reference's models, because the 422 items are the
// reference's items:
//
//	UpdateDispositionRequest  (api/http.py:1856-1859): disposition: DispositionTraits
//	DispositionTraits         (api/http.py:1769-1774): skepticism/literalism/empathy,
//	                          int, ge=1, le=5, all required
//	AddBackgroundRequest      (api/http.py:1886-1895): content: str (required),
//	                          update_disposition: bool = True
//
// A 410 stays a 410: this gate only turns a body the reference would reject into
// the same rejection.

// UpdateDispositionRequest is the retired profile write's body. Its trait
// members reuse the bank request's own type, so the two cannot drift.
type UpdateDispositionRequest struct {
	Disposition *DispositionInput `json:"disposition"`
}

// AddBackgroundRequest is the retired background write's body.
type AddBackgroundRequest struct {
	Content           *string `json:"content"`
	UpdateDisposition *bool   `json:"update_disposition"`
}

// RetiredBodyGate validates the body a retired write route declares, returning
// the failure the framework would answer with, or nil when the body is
// acceptable and the route should answer its retirement status.
func RetiredBodyGate(r *http.Request, model any) *httpx.ValidationFailure {
	body, readErr := io.ReadAll(io.LimitReader(r.Body, maxBankBodyBytes))
	if readErr != nil {
		return httpx.BodyDecodeFailure(readErr, body)
	}
	if len(body) == 0 {
		// A request that carried no body is the framework's missing-field item
		// for the body itself, not a syntax failure.
		return httpx.BodyDecodeFailure(httpx.ErrEmptyBody, body)
	}
	// The decoder's items and the model's own required-member items are
	// reported together, in the model's declaration order: a trait that is the
	// wrong type is still accompanied by its absent siblings.
	failure := decodeRetiredBody(model, body)
	var items []httpx.ValidationError
	if failure != nil {
		if failure.Detail != "" {
			return failure
		}
		items = failure.Errors
	}
	items = mergeRetiredItems(retiredMemberItems(model, body, items), items)
	if len(items) == 0 {
		return nil
	}
	return &httpx.ValidationFailure{Errors: items}
}

// decodeRetiredBody applies the decoder's rules to the body, reporting the item
// list the framework would answer with. The route's contract schema is not
// consulted: it is built for the null-rejection pass, which reports a declared
// model as a dict mismatch where the reference's item calls it a model shape.
func decodeRetiredBody(model any, body []byte) *httpx.ValidationFailure {
	decoded, err := httpx.DecodeLax(body, model)
	if err == nil {
		return nil
	}
	failure := httpx.BodyDecodeFailure(err, decoded)
	if failure.Detail != "" {
		return failure
	}
	return &httpx.ValidationFailure{Errors: modelShapeItemsForNested(failure.Errors)}
}

// modelShapeItemsForNested renames the decoder's `dict_type` item for a member
// declared as a model to the model library's own item, which is what the
// reference reports for a nested value that is not an object.
func modelShapeItemsForNested(items []httpx.ValidationError) []httpx.ValidationError {
	for i, item := range items {
		if item.Type != "dict_type" || len(item.Loc) != 2 || item.Loc[0] != "body" || item.Loc[1] != "disposition" {
			continue
		}
		items[i] = modelShapeItem(item.Loc, item.Input)
	}
	return items
}

// retiredMemberItems reports what the declared model requires beyond the
// decoder's type checks: a required member the body left out, a nested model
// that is present but not an object, and — for the traits — a value outside the
// 1-5 scale.
func retiredMemberItems(model any, body []byte, reported []httpx.ValidationError) []httpx.ValidationError {
	object, ok := bodyObject(body)
	if !ok {
		// A body that is not an object is the decoder's own model-shape item.
		return nil
	}
	switch model.(type) {
	case *UpdateDispositionRequest:
		raw, present := object["disposition"]
		if !present {
			return []httpx.ValidationError{httpx.Missing([]string{"body", "disposition"}, decodedObject(object))}
		}
		if string(bytes.TrimSpace(raw)) == "null" {
			// A null is the decoder's model-shape item, which it already
			// reported, so there is nothing further to say.
			return nil
		}
		disposition, isObject := bodyObject(raw)
		if !isObject {
			// The decoder's item for this value was already renamed; nothing more.
			return nil
		}
		// Every trait is checked against the body, not the decoded struct: the
		// decoder stops at the first value it cannot take, so the struct's other
		// members may hold zero values the body never sent.
		var items []httpx.ValidationError
		for _, name := range []string{"skepticism", "literalism", "empathy"} {
			loc := []string{"body", "disposition", name}
			rawMember, supplied := disposition[name]
			if !supplied {
				items = append(items, httpx.Missing(loc, decodedObject(disposition)))
				continue
			}
			number, ok := jsonIntegerOf(rawMember)
			if reportedAt(reported, loc) {
				// The decoder already named this value's type error; this item
				// marks the position so that item keeps the model's order.
				items = append(items, httpx.ValidationError{Type: "claimed", Loc: loc})
				continue
			}
			if !ok && string(bytes.TrimSpace(rawMember)) != "null" {
				// A value the decoder's lax pass converted (a numeric string, a
				// boolean) is one the model accepts; anything else it rejects.
				if _, lax := httpx.LaxInteger(jsonStringValue(rawMember)); lax {
					continue
				}
				if _, isBool := jsonValue(rawMember).(bool); isBool {
					continue
				}
				items = append(items, httpx.ScalarTypeMismatch("int", loc, jsonValue(rawMember)))
				continue
			}
			if !ok {
				// A null or a value the decoder accepted in another form: the
				// decoder owns the null item, and a non-integer never reaches the
				// range check.
				continue
			}
			if number < storage.DispositionTraitMin {
				items = append(items, httpx.GreaterThanEqual(loc, number, storage.DispositionTraitMin))
			} else if number > storage.DispositionTraitMax {
				items = append(items, httpx.LessThanEqual(loc, number, storage.DispositionTraitMax))
			}
		}
		return items
	case *AddBackgroundRequest:
		if _, present := object["content"]; !present {
			return []httpx.ValidationError{httpx.Missing([]string{"body", "content"}, decodedObject(object))}
		}
	}
	return nil
}

// mergeRetiredItems interleaves a model's own items with the decoder's, keeping
// the model's declaration order: a trait the decoder already named keeps the
// decoder's item at that position rather than appearing twice.
func mergeRetiredItems(modelItems, decoderItems []httpx.ValidationError) []httpx.ValidationError {
	claimed := make(map[int]bool, len(decoderItems))
	out := make([]httpx.ValidationError, 0, len(modelItems)+len(decoderItems))
	for _, item := range modelItems {
		replaced := false
		for index, existing := range decoderItems {
			if claimed[index] || !sameLoc(existing.Loc, item.Loc) {
				continue
			}
			out = append(out, existing)
			claimed[index] = true
			replaced = true
			break
		}
		if !replaced {
			out = append(out, item)
		}
	}
	for index, existing := range decoderItems {
		if !claimed[index] {
			out = append(out, existing)
		}
	}
	return out
}

// sameLoc reports whether two item locations are identical.
func sameLoc(a, b []string) bool {
	return strings.Join(a, "\x00") == strings.Join(b, "\x00")
}

// jsonStringValue returns the text of a JSON string member, or "" for any other
// kind.
func jsonStringValue(raw json.RawMessage) string {
	value, ok := jsonValue(raw).(string)
	if !ok {
		return ""
	}
	return value
}

// jsonIntegerOf reports the integer a JSON value carries, if it is one written
// as a number (a numeric string was already read by the decoder, and null is
// the decoder's item).
func jsonIntegerOf(raw json.RawMessage) (int, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] == '"' || string(trimmed) == "null" {
		return 0, false
	}
	var number float64
	if err := json.Unmarshal(trimmed, &number); err != nil {
		return 0, false
	}
	if number != float64(int(number)) {
		return 0, false
	}
	return int(number), true
}

// reportedAt reports whether the decoder already filed an item at loc.
func reportedAt(items []httpx.ValidationError, loc []string) bool {
	for _, item := range items {
		if len(item.Loc) == len(loc) && strings.Join(item.Loc, "\x00") == strings.Join(loc, "\x00") {
			return true
		}
	}
	return false
}

// bodyObject decodes a JSON object value into its members.
func bodyObject(raw []byte) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, false
	}
	return object, true
}

// decodedObject renders raw object members as the decoded map the framework
// names as an item's input.
func decodedObject(object map[string]json.RawMessage) map[string]any {
	out := make(map[string]any, len(object))
	for key, raw := range object {
		out[key] = jsonValue(raw)
	}
	return out
}

// jsonValue decodes one raw JSON value for an item's input.
func jsonValue(raw json.RawMessage) any {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return value
}

// modelShapeItem builds the item the library reports when a value sent for a
// nested model is not an object.
func modelShapeItem(loc []string, input any) httpx.ValidationError {
	return httpx.ValidationItem("model_attributes_type", loc, httpx.TypeMessage("model_attributes_type"), input, nil)
}

// retiredRouteBody is one retired write route's declared body.
type retiredRouteBody struct {
	model func() any
}

// retiredRouteBodies maps the retired write routes to their declared bodies. The
// read route (GET .../profile) declares none, so it answers 410 directly.
var retiredRouteBodies = map[string]retiredRouteBody{
	"PUT /v1/default/banks/{bank_id}/profile":     {model: func() any { return &UpdateDispositionRequest{} }},
	"POST /v1/default/banks/{bank_id}/background": {model: func() any { return &AddBackgroundRequest{} }},
}

// retiredRouteKey renders a method and path pattern as the map key above.
func retiredRouteKey(method, pattern string) string {
	return strings.ToUpper(method) + " " + pattern
}
