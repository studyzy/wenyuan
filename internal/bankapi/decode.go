package bankapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"

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
		if err := decodeRawObject(decoded, object); err != nil {
			return nil, httpx.BodyDecodeFailure(err, decoded)
		}
	}
	return object, nil
}

// decodeBankBody decodes a create-or-replace or partial-update body into req and
// reports the body's own validation items alongside it.
//
// The reference answers one item per field its model rejects, in field order, so
// a body carrying several wrong values is answered with several items; the
// shared decoder stops at the first one. Once it has rejected the body, the
// items are therefore taken from the declaration walk, which describes every
// field at once — the same vocabulary, in the same order. The fields that did
// decode are filled in so the handler's own rules can be reported beside them.
//
// A rejection the walk cannot describe — a syntax failure, an unreadable body,
// a whole-body shape the request layer refused before any model saw it — keeps
// the decoder's answer.
func decodeBankBody(r *http.Request) (BankRequest, []httpx.ValidationError, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBankBodyBytes))
	if err != nil {
		return BankRequest{}, nil, httpx.BodyDecodeFailure(err, body)
	}
	// The body is restored so the shared decoder — which reads the request
	// itself, and records the fields the model does not declare — sees the same
	// bytes this walk does.
	r.Body = io.NopCloser(bytes.NewReader(body))
	var req BankRequest
	decodeErr := httpx.DecodeBody(r, &req)
	if decodeErr == nil {
		return req, bankFieldTypeItems(body), nil
	}
	var failure *httpx.ValidationFailure
	if !errors.As(decodeErr, &failure) || failure.Detail != "" {
		// Not an item list — an unreadable body, or one the request layer
		// refused before any model saw it.
		return BankRequest{}, nil, decodeErr
	}
	// The walk answers a whole-body shape failure (a body that is not an
	// object) with the framework's own item for the body parameter, which is
	// the one the decoder already produced.
	if !bankBodyIsObject(body) {
		return BankRequest{}, nil, decodeErr
	}
	items := httpx.ValidationItemsAgainstRequest(r, &BankRequest{}, body)
	if len(items) == 0 {
		return BankRequest{}, nil, decodeErr
	}
	fillBankDecodedMembers(body, &req)
	return req, items, &BodyNeedsValidation{Items: items}
}

// BodyNeedsValidation reports an item list the handler still has to complete:
// the body's own items are known, and the fields that did decode still have to
// be checked against the handler's rules. It is not a failure of its own; the
// handler renders the completed list.
type BodyNeedsValidation struct {
	Items []httpx.ValidationError
}

// Error renders the first item, like every other validation failure.
func (e *BodyNeedsValidation) Error() string {
	if len(e.Items) == 0 {
		return "validation failed"
	}
	return e.Items[0].Msg
}

// bankMemberBody is one top-level member of a bank request body: its name and
// the bytes it was written as.
type bankMemberBody struct {
	name string
	body []byte
}

// bankMemberBodies reads a bank request body as its top-level members, keeping
// every member's bytes. It reports false for a body that does not read as a
// JSON object, and then the caller's own decode is the only thing that can
// answer.
func bankMemberBodies(body []byte) ([]bankMemberBody, bool) {
	members := map[string]json.RawMessage{}
	if err := json.Unmarshal(body, &members); err != nil || members == nil {
		return nil, false
	}
	out := make([]bankMemberBody, 0, len(members))
	for name, raw := range members {
		out = append(out, bankMemberBody{name: name, body: raw})
	}
	return out, true
}

// bankFieldSource returns the top-level member whose name a field decodes from
// ("disposition_skepticism" for a flat field, "disposition" for the object),
// reading the name off the struct's own tag so the two cannot drift.
func bankFieldSource(field reflect.StructField) string {
	name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
	return name
}

// fillBankDecodedMembers decodes each declared member of a bank request body
// into its field on its own, so a body the shared decoder rejected still hands
// the handler the values it did carry.
func fillBankDecodedMembers(body []byte, req *BankRequest) {
	bodies, ok := bankMemberBodies(body)
	if !ok {
		return
	}
	values := map[string]json.RawMessage{}
	for _, member := range bodies {
		values[member.name] = member.body
	}
	target := reflect.ValueOf(req).Elem()
	typ := target.Type()
	for i := range typ.NumField() {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}
		name := bankFieldSource(field)
		raw, present := values[name]
		if name == "" || name == "-" || !present {
			continue
		}
		holder := reflect.New(field.Type)
		if _, err := httpx.DecodeLax(raw, holder.Interface()); err != nil {
			continue
		}
		target.Field(i).Set(holder.Elem())
	}
}

// bankBodyIsObject reports whether the body's top-level JSON value is an
// object, which is the only shape the per-field walk has anything to say about.
func bankBodyIsObject(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

// bankRequestFields returns the bank request model's wire field names in
// declaration order, which is the order the reference reports its items in.
func bankRequestFields() []string {
	typ := reflect.TypeOf(BankRequest{})
	names := make([]string, 0, typ.NumField())
	for i := range typ.NumField() {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}
		name := bankFieldSource(field)
		if name == "" || name == "-" {
			continue
		}
		names = append(names, name)
	}
	return names
}

// bankFieldTypeItems reports every member of the bank request body the
// reference's model rejects for its JSON type, in the order it reports them.
//
// The reference validates its model field by field and answers one item per
// rejected field, so a body carrying two wrong-typed values gets two items.
// The shared decoder stops at the first type error it meets, so this walks the
// members that decode cleanly as a map — which is also the reference's order:
// its model is validated by fields first and by validators second, so a field
// type failure always comes before another field's bounds failure.
//
// A member whose value is null is deliberately skipped: the framework treats
// null as a missing input for a field that declares a default, so such a member
// is either dropped by the shared decoder or reported by its null pass — never a
// type item here.
func bankFieldTypeItems(body []byte) []httpx.ValidationError {
	bodies, ok := bankMemberBodies(body)
	if !ok {
		return nil
	}
	typ := reflect.TypeOf(BankRequest{})
	var items []httpx.ValidationError
	for i := range typ.NumField() {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}
		source := bankFieldSource(field)
		if source == "" || source == "-" {
			continue
		}
		var raw []byte
		found := false
		for _, member := range bodies {
			if member.name == source {
				raw, found = member.body, true
				break
			}
		}
		if !found || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		// The member is decoded on its own and its failure rebased onto the
		// request body, so the item names the field's own path
		// (["body","name"]) exactly as the shared decoder's would.
		holder := reflect.New(field.Type)
		coerced, err := httpx.DecodeLax(raw, holder.Interface())
		if err == nil {
			continue
		}
		// A member object whose own member is wrong-typed is reported at that
		// nested path, by the model's own walk: the framework names the inner
		// value, not the object that holds it.
		if nested := httpx.ValidationItemsAgainstRequest(nil, holder.Interface(), coerced); len(nested) > 0 {
			items = append(items, prefixLocations(source, nested)...)
			continue
		}
		localized := httpx.LocalizeNestedDecodeError(nil, source, coerced, err)
		items = append(items, httpx.NestedFailureItems([]error{localized})...)
	}
	return items
}

// prefixLocations re-homes items the model walk produced for a member's own
// sub-document onto the request body, so a nested member's item reads
// (["body","disposition","skepticism"]) rather than starting at the member.
func prefixLocations(member string, items []httpx.ValidationError) []httpx.ValidationError {
	out := make([]httpx.ValidationError, 0, len(items))
	for _, item := range items {
		loc := item.Loc
		if len(loc) > 0 && loc[0] == "body" {
			loc = loc[1:]
		}
		moved := append([]string{"body", member}, loc...)
		item.Loc = moved
		out = append(out, item)
	}
	return out
}

// decodeRawObject reads the request body into a generic map while keeping the
// written form of every top-level number: the framework's own JSON parser
// distinguishes an integer literal (`5`) from a decimal one (`5.0`), and the
// bank configuration write reports that difference in its type errors and
// refuses an integer field a decimal value. encoding/json's default decode
// collapses both to float64, so the `updates` map's members are read with
// UseNumber and left as json.Number. Every number nested inside a member is
// converted back to float64, because the field validators behind the map
// (the mental-model trigger and the entity-label vocabulary) still read the
// generic shape a plain decode produces.
func decodeRawObject(body []byte, object map[string]any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil {
		return err
	}
	for key, value := range object {
		if key == "updates" {
			if members, ok := value.(map[string]any); ok {
				for member, item := range members {
					if _, isNumber := item.(json.Number); isNumber {
						continue
					}
					members[member] = deepFloatNumbers(item)
				}
				continue
			}
		}
		object[key] = deepFloatNumbers(value)
	}
	return nil
}

// deepFloatNumbers replaces every json.Number in a decoded value with a float64,
// recursing through the containers the config validators walk.
func deepFloatNumbers(value any) any {
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Float64()
		if err != nil {
			return typed
		}
		return number
	case map[string]any:
		for key, item := range typed {
			typed[key] = deepFloatNumbers(item)
		}
		return typed
	case []any:
		for index, item := range typed {
			typed[index] = deepFloatNumbers(item)
		}
		return typed
	default:
		return value
	}
}
