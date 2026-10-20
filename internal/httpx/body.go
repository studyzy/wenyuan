package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// detailBodyNotText is the fixed detail the reference's request layer returns
// when the body cannot be decoded as text, which happens before any handler or
// validation rule runs.
const detailBodyNotText = "There was an error parsing the body"

// utf8BOM is the byte-order mark the reference's parser accepts at the start of
// a body and the bytes it counts positions from (it strips the mark first).
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// checkBodyText prepares a request body the way the reference's request layer
// does: a leading UTF-8 byte-order mark is dropped, and a body that is not valid
// UTF-8 is refused with the layer's own bad-request detail rather than becoming a
// validation item (the request never reaches the handler in that case).
func checkBodyText(body []byte) ([]byte, error) {
	body = bytes.TrimPrefix(body, utf8BOM)
	if !utf8.Valid(body) {
		return nil, NewHTTPError(KindBadRequest, detailBodyNotText)
	}
	return body, nil
}

// ErrEmptyBody reports a request that carried no body at all. The framework
// answers such a request with the item for a missing required field, so the
// rule lives beside the decoder rather than in every handler.
var ErrEmptyBody = errors.New("empty request body")

// A validation item echoes the body it was raised for — `{"input": {...}}` — and
// the reference's report keeps that body as an insertion-ordered mapping, so its
// members appear in the order the client wrote them. A Go map sorts its keys, so
// the decoder records the order here and the writer replays it.

// RecordBodyOrder parses the top-level object's member names in document order
// and stores them on the request's context. DecodeBody calls it; a handler that
// reads its own body calls it too, so the failure it raises echoes the body the
// way the client wrote it.
func RecordBodyOrder(r *http.Request, body []byte) {
	body = bytes.TrimPrefix(body, utf8BOM)
	body = bytes.TrimLeft(body, " \t\r\n")
	if r == nil || len(body) == 0 || body[0] != '{' {
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return
	}
	members := make([]orderedEntry, 0, 8)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return
		}
		key, ok := keyToken.(string)
		if !ok {
			return
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			// The decoder refuses the body itself from here on (an out-of-range
			// literal, say), and the request ends as that failure rather than as an
			// item list, so no echo is needed.
			return
		}
		members = append(members, orderedEntry{key: key, value: orderNested(value)})
	}
	// The order rides on the request's context, so any caller holding the same
	// request can echo the body the way the client wrote it.
	*r = *r.WithContext(context.WithValue(r.Context(), bodyOrderKey{}, orderMap(members)))
}

// OrderedObject is a JSON object whose members are written in the order they
// were added. The reference's mappings are insertion-ordered, so an object it
// builds member by member reaches the wire in that order rather than in a Go
// map's sorted one; a value that has to be reproduced verbatim is built here.
type OrderedObject []OrderedMember

// OrderedMember is one member of an OrderedObject.
type OrderedMember struct {
	Key   string
	Value any
}

// MarshalJSON writes the members in the order they were added.
func (o OrderedObject) MarshalJSON() ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for i, member := range o {
		if i > 0 {
			buffer.WriteByte(',')
		}
		key, err := json.Marshal(member.Key)
		if err != nil {
			return nil, err
		}
		buffer.Write(key)
		buffer.WriteByte(':')
		value, err := json.Marshal(member.Value)
		if err != nil {
			return nil, err
		}
		buffer.Write(value)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

// orderedEntry is one member of the request body, with its value kept in the
// member order it was written in.
type orderedEntry struct {
	key   string
	value orderedValue
}

// orderedValue marshals a JSON value with its own objects' members in document
// order.
type orderedValue struct {
	raw any
}

// MarshalJSON writes the value as recorded.
func (o orderedValue) MarshalJSON() ([]byte, error) {
	return json.Marshal(o.raw)
}

// orderNested re-reads a raw JSON value so every object in it keeps its member
// order: a decoded map would sort its keys.
func orderNested(raw []byte) orderedValue {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, ok := orderValue(decoder)
	if !ok {
		return orderedValue{raw: json.RawMessage(raw)}
	}
	return orderedValue{raw: value}
}

// orderMap is a JSON object whose members marshal in the recorded order.
type orderMap []orderedEntry

// MarshalJSON writes the object's members in the order they were read.
func (m orderMap) MarshalJSON() ([]byte, error) {
	buffer := bytes.NewBuffer(make([]byte, 0, 64))
	buffer.WriteByte('{')
	for i, entry := range m {
		if i > 0 {
			buffer.WriteByte(',')
		}
		key, err := json.Marshal(entry.key)
		if err != nil {
			return nil, err
		}
		buffer.Write(key)
		buffer.WriteByte(':')
		value, err := json.Marshal(entry.value)
		if err != nil {
			return nil, err
		}
		buffer.Write(value)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

// orderValue reads one JSON value from a decoder, keeping every object's member
// order.
func orderValue(decoder *json.Decoder) (any, bool) {
	token, err := decoder.Token()
	if err != nil {
		return nil, false
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return token, true
	}
	switch delim {
	case '{':
		var members orderMap
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, false
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, false
			}
			value, ok := orderValue(decoder)
			if !ok {
				return nil, false
			}
			members = append(members, orderedEntry{key: key, value: orderedValue{raw: value}})
		}
		if _, err := decoder.Token(); err != nil {
			return nil, false
		}
		return members, true
	case '[':
		var list []any
		for decoder.More() {
			value, ok := orderValue(decoder)
			if !ok {
				return nil, false
			}
			list = append(list, orderedValue{raw: value})
		}
		if _, err := decoder.Token(); err != nil {
			return nil, false
		}
		return list, true
	}
	return nil, false
}

// bodyOrderKey types the context key, so a value stored under it cannot collide
// with another package's.
type bodyOrderKey struct{}

// OrderWholeBodyFailure gives a failure whose items echo the whole request body
// — a `missing` required member carries the body as its `input` — the recorded
// member order, so the response renders that body the way the client wrote it.
// The recorder is filled in as well, for an item that echoes a member whose own
// value is an object.
func OrderWholeBodyFailure(r *http.Request, failure *ValidationFailure) *ValidationFailure {
	if failure == nil {
		return nil
	}
	if failure.BodyOrder == nil {
		order, ok := BodyOrder(r)
		if !ok {
			return failure
		}
		copied := *failure
		copied.BodyOrder = order
		failure = &copied
	}
	return failure
}

// BodyOrder returns the recorded member order of the request's body, if the
// decoder captured one.
func BodyOrder(r *http.Request) (orderMap, bool) {
	members, ok := r.Context().Value(bodyOrderKey{}).(orderMap)
	return members, ok
}

// DecodeBody reads and decodes a JSON request body, recording the body fields
// the target does not declare, and reports a body the reference's framework
// would reject as a 422 validation failure rather than a 400.
//
// The framework distinguishes the two: a body that is not valid JSON is a
// `json_invalid` item, and a value of the wrong type is the item the framework
// names for that type (`int_parsing`, `string_type`, ...). A client that reads
// the error body therefore sees one shape for every rejected request.
func DecodeBody(r *http.Request, target any) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return &ValidationFailure{Errors: []ValidationError{
			ValidationItem("json_invalid", []string{"body"}, "JSON decode error", nil,
				map[string]any{"error": err.Error()}),
		}}
	}
	var textErr error
	if body, textErr = checkBodyText(body); textErr != nil {
		return textErr
	}
	RecordUnknownBodyFields(r, body, target)
	if len(body) == 0 {
		return BodyDecodeFailure(ErrEmptyBody, body)
	}
	// The reference's framework coerces values between compatible JSON kinds
	// before validating them, so a body Go's decoder refuses may still be one
	// the reference accepts (and one it rejects keeps the item the decoder
	// machinery builds for its original text).
	decoded, err := decodeLaxSchema(body, target, schemaForRequest(r, target))
	if err != nil {
		if failure := BodyDecodeFailure(err, decoded); failure != nil {
			// The items echo the body as the client wrote it, so the recorded member
			// order rides along with the failure.
			if order, ok := BodyOrder(r); ok {
				failure.BodyOrder = order
			}
			return failure
		}
		return err
	}
	return nil
}

// BodyDecodeFailure renders a decode error for a body the caller already read as
// the framework's structured item. A handler that reads the body itself — to
// report the fields it does not know, for instance — reports a rejected body the
// same way the shared decoder does.
func BodyDecodeFailure(err error, body []byte) *ValidationFailure {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		// A failure the request layer itself raised (a body that is not text)
		// keeps its own status and detail instead of becoming an item list.
		return &ValidationFailure{Status: httpErr.Status, Detail: httpErr.Detail}
	}
	return bodyFailure(err, body)
}

// NestedDecodeError is a decode failure a type that decodes itself localized.
// Such a type hands a member to a second decoder over a sub-slice, so the
// *json.UnmarshalTypeError it gets back has an Offset relative to that sub-slice
// and a Field missing the enclosing path. This error carries the JSON path from
// the body root and the sub-slice the offset indexes, so the shared builder
// reports the same item the framework does for a nested member.
type NestedDecodeError struct {
	Err  *json.UnmarshalTypeError
	Path []string // below the body root, e.g. include, entities, max_tokens
	Body []byte   // the sub-slice Err.Offset indexes
}

// Error renders the underlying decoder failure.
func (e *NestedDecodeError) Error() string { return e.Err.Error() }

// Unwrap exposes the decoder failure, so a caller that only knows the standard
// error types still recognizes it.
func (e *NestedDecodeError) Unwrap() error { return e.Err }

// LocalizeNestedDecodeError rebases the error a self-decoding type's member
// decode returned onto the request body. prefix is the JSON path from the body
// root to the object the type decodes (e.g. ["include"]), member is the failed
// member's name, and data is the sub-slice that member's decode read. An error
// that is not a decoder type failure is returned unchanged, so an unrelated
// failure keeps its own rendering.
func LocalizeNestedDecodeError(prefix []string, member string, data []byte, err error) error {
	var already *NestedDecodeError
	if errors.As(err, &already) {
		// The member type localized its own failure, so its path continues from
		// this member rather than restarting at the body root.
		path := append(append(append([]string(nil), prefix...), member), already.Path...)
		return &NestedDecodeError{Err: already.Err, Path: path, Body: already.Body}
	}
	var typeError *json.UnmarshalTypeError
	if !errors.As(err, &typeError) {
		return err
	}
	path := append(append([]string(nil), prefix...), member)
	if typeError.Field != "" {
		path = append(path, strings.Split(typeError.Field, ".")...)
	}
	return &NestedDecodeError{Err: typeError, Path: path, Body: data}
}

// DecodeStructFields decodes the JSON object in data into the struct target one
// declared field at a time, so every field the reference's model rejects is
// reported rather than only the first the shared decoder stops on. Each field
// is decoded with the same lax coercion the body decoder applies, so a value the
// reference accepts for it (a numeric string for an integer field, a boolean
// word for a boolean field) is still accepted and stored in target.
//
// prefix is the JSON path from the body root to the object (e.g.
// ["include","source_facts"]); each failure is localized to prefix plus the
// field's JSON name, with the value read from the bytes that field was decoded
// from. The failures are returned in the struct's field-declaration order,
// which is the order the reference reports them, so a caller accumulates them
// in that order no matter how the request ordered the keys.
func DecodeStructFields(data []byte, target any, prefix []string) []error {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		// A caller hands this only an object it has already accepted as one, so
		// a failure here is a malformed value rather than a member's own field;
		// report it against the object itself.
		last := len(prefix) - 1
		return []error{LocalizeNestedDecodeError(prefix[:last], prefix[last], data, err)}
	}
	typ := reflect.TypeOf(target).Elem()
	dest := reflect.ValueOf(target).Elem()
	var errs []error
	for i := range typ.NumField() {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue // unexported: not a wire field
		}
		name := jsonFieldName(field)
		if name == "" {
			continue
		}
		raw, present := members[name]
		if !present {
			continue
		}
		holder := reflect.New(field.Type)
		// The member's own decode is read as a sub-slice, and the body-wide
		// null pass already reports every null rejection from the request root;
		// running it here too would report the same value twice.
		body, err := decodeLaxSchema(raw, holder.Interface(), nil)
		if err != nil {
			errs = append(errs, LocalizeNestedDecodeError(prefix, name, body, err))
			continue
		}
		dest.Field(i).Set(holder.Elem())
	}
	return errs
}

// jsonFieldName returns the name a struct field decodes from: the name part of
// its json tag, or the Go field name when the tag carries none. A field the tag
// marks "-" is not a wire field and yields the empty name.
func jsonFieldName(field reflect.StructField) string {
	name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
	if name == "-" {
		return ""
	}
	if name == "" {
		return field.Name
	}
	return name
}

// NestedFailureItems renders a sequence of localized nested decode errors as the
// framework's structured items, in the order given. The reference reports every
// invalid member of one request in a single failure, so a type that decodes its
// members one at a time records each member's failure and needs them all in one
// failure; this helper applies the same rendering bodyFailure gives a single
// localized error. Each item's loc carries the full path from the body root and
// its input is read from the sub-slice the inner offset indexes.
func NestedFailureItems(errs []error) []ValidationError {
	items := make([]ValidationError, 0, len(errs))
	for _, err := range errs {
		var nested *NestedDecodeError
		if errors.As(err, &nested) {
			loc := append([]string{"body"}, nested.Path...)
			input := scalarInput(nested.Body, nested.Err)
			kind, msg := typeVocabulary(nested.Err, input)
			items = append(items, ValidationItem(kind, loc, msg, input, nil))
			continue
		}
		// A failure that is not a localized decoder type failure still names one
		// item, so an unexpected member failure is reported rather than dropped.
		items = append(items, ValidationItem("json_invalid", []string{"body"}, "JSON decode error", nil,
			map[string]any{"error": err.Error()}))
	}
	return items
}

// bodyFailure renders a decode error as the framework's structured item.
func bodyFailure(err error, body []byte) *ValidationFailure {
	var failure *ValidationFailure
	if errors.As(err, &failure) {
		// A failure the decode path already built (the null pass reports its
		// items as one structured failure) keeps its own item list.
		return failure
	}
	if errors.Is(err, ErrEmptyBody) && len(body) == 0 {
		// An absent body is a missing required field whose input is null: there
		// is no object for the field to be missing from.
		return &ValidationFailure{Errors: []ValidationError{Missing([]string{"body"}, nil)}}
	}
	if len(body) > 0 && !json.Valid(body) {
		// The framework's parser runs before its model, so a body its parser
		// rejects is the json_invalid item — never a type failure the Go decoder
		// happened to report first (a scalar with trailing text decodes far
		// enough for the decoder to name the model before it notices). A body
		// that is only whitespace reaches here too and is rejected like any
		// other malformed text.
		if failure, ok := pythonJSONFailure(body); ok {
			return failure
		}
	}
	var syntaxError *json.SyntaxError
	if errors.As(err, &syntaxError) {
		if failure, ok := pythonJSONFailure(body); ok {
			return failure
		}
		// A body the Go decoder rejects but CPython accepts (its NaN and
		// Infinity literals): keep the decoder's own rendering rather than
		// inventing a position the reference never reports.
		loc := []string{"body"}
		if syntaxError.Offset > 0 {
			loc = append(loc, strconv.Itoa(int(syntaxError.Offset)))
		}
		return &ValidationFailure{Errors: []ValidationError{
			ValidationItem("json_invalid", loc, "JSON decode error", map[string]any{},
				map[string]any{"error": err.Error()}),
		}}
	}
	var nested *NestedDecodeError
	if errors.As(err, &nested) {
		return &ValidationFailure{Errors: NestedFailureItems([]error{err})}
	}
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &typeError) {
		if typeError.Field == "" && isModelType(typeError.Type) {
			// The whole body was sent for a model: the framework reports its
			// model_attributes_type item, whose input is the value itself.
			return &ValidationFailure{Errors: []ValidationError{modelAttributesType(body)}}
		}
		loc := []string{"body"}
		if typeError.Field != "" {
			// The framework's loc carries an index for every list level; the Go
			// decoder's field path has none, so the raw body is walked to
			// recover them (falling back to the plain path when the scan
			// cannot confirm it).
			if scanned, ok := locForOffset(body, typeError.Field, typeError.Offset); ok {
				loc = append(loc, scanned...)
			} else {
				loc = append(loc, strings.Split(typeError.Field, ".")...)
			}
		}
		input := scalarInput(body, typeError)
		kind, msg := typeVocabulary(typeError, input)
		return &ValidationFailure{Errors: []ValidationError{
			ValidationItem(kind, loc, msg, input, nil),
		}}
	}
	return &ValidationFailure{Errors: []ValidationError{
		ValidationItem("json_invalid", []string{"body"}, "JSON decode error", nil,
			map[string]any{"error": err.Error()}),
	}}
}

// JSONDecodeFailure reports whether the JSON text carries a value no decoder of
// the standard library accepts at all — a numeric literal that overflows to
// infinity, such as `1e999`. CPython's parser reads that literal as an `inf`
// float, which then fails to serialize, so the reference's own error handler
// breaks and the client gets a bare 500 ("Internal Server Error") rather than a
// validation item. Callers that decode a body themselves use this before
// rendering a decode failure, because the Go decoder's own message for such a
// body ("cannot unmarshal number into Go struct field …") names the wrong
// problem: the failure is in the text, not in the model. The report is true only
// when the text is otherwise valid, so a genuinely malformed body keeps the
// parser's item.
func JSONDecodeFailure(body []byte) bool {
	text := bytes.TrimPrefix(body, utf8BOM)
	if !json.Valid(text) {
		return false
	}
	var probe any
	return json.Unmarshal(text, &probe) != nil
}

// pythonJSONFailure renders body as the item the reference's framework emits for
// a body its parser rejects: the item's loc carries CPython's 0-based character
// position of the failure and its ctx the bare message CPython reports. The last
// result is false when the scan finds no failure, so a caller that reached here
// through the Go decoder keeps its own rendering.
func pythonJSONFailure(body []byte) (*ValidationFailure, bool) {
	pos, msg, ok := pythonJSONError(body)
	if !ok {
		return nil, false
	}
	return &ValidationFailure{Errors: []ValidationError{
		ValidationItem("json_invalid", []string{"body", strconv.Itoa(pos)}, "JSON decode error", map[string]any{},
			map[string]any{"error": msg}),
	}}, true
}

// modelAttributesType builds the item the framework emits when a whole body
// carries a value a model cannot extract fields from. input is the value the
// caller sent, read back from the body so it names what arrived.
func modelAttributesType(body []byte) ValidationError {
	input := any(nil)
	if len(body) > 0 {
		var value any
		if err := json.Unmarshal(body, &value); err == nil {
			input = value
		}
	}
	return ValidationItem("model_attributes_type", []string{"body"},
		"Input should be a valid dictionary or object to extract fields from", input, nil)
}

// isModelType reports whether a decode target names a model: a struct, possibly
// behind pointers. The framework keys its whole-body item on the declared model,
// so only a struct target gets it; a map target keeps the generic dict item.
func isModelType(t reflect.Type) bool {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t != nil && t.Kind() == reflect.Struct
}

// typeVocabulary names a wrong-typed value the way the framework names it for
// the same failure. The framework keys the item on the declared type and on the
// JSON kind that was sent: a string that will not convert to the type is a
// `<type>_parsing` failure, any other kind a `<type>_type` one, and a number
// carrying a fraction sent for an integer has a name of its own.
func typeVocabulary(err *json.UnmarshalTypeError, input any) (string, string) {
	parsing := err.Value == "string"
	switch err.Type.Kind() {
	case reflect.String:
		return typed("string_type")
	case reflect.Bool:
		// A boolean the framework cannot interpret is a parsing failure: that
		// covers every string (including its accepted spellings' near misses) and
		// a whole number other than 0 or 1. A fractional number, a container or
		// null is the wrong type outright.
		if parsing {
			return typed("bool_parsing")
		}
		if number, ok := input.(float64); ok && number == math.Trunc(number) {
			// A whole number other than 0 or 1: the framework cannot interpret it.
			return typed("bool_parsing")
		}
		return typed("bool_type")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		switch {
		case parsing:
			return typed("int_parsing")
		case strings.HasPrefix(err.Value, "number "):
			// The decoder reports a fractional number with the literal it read,
			// which is the only kind that fails as a fraction rather than as a
			// plain wrong type.
			return typed("int_from_float")
		}
		return typed("int_type")
	case reflect.Float32, reflect.Float64:
		if parsing {
			return typed("float_parsing")
		}
		return typed("float_type")
	case reflect.Slice, reflect.Array:
		return typed("list_type")
	case reflect.Map, reflect.Struct:
		return typed("dict_type")
	}
	return typed("value_error")
}

// typed pairs a vocabulary entry with the wording the framework gives it.
func typed(kind string) (string, string) {
	return kind, TypeMessage(kind)
}

// scalarInput reads the input the decoded value failed on, so the item names
// what the caller actually sent. An offset the decoder did not report yields an
// empty input rather than a guess.
//
// A container sent for a scalar lands the decoder's offset just past the
// opening bracket, so the value is read from there: the delimiter scan below
// would stop at the container's first element and report a fragment.
func scalarInput(body []byte, typeError *json.UnmarshalTypeError) any {
	offset := typeError.Offset
	if offset <= 0 || offset > int64(len(body)) {
		return nil
	}
	if typeError.Value == "array" || typeError.Value == "object" {
		if start := offset - 1; body[start] == '[' || body[start] == '{' {
			var value any
			if err := json.NewDecoder(bytes.NewReader(body[start:])).Decode(&value); err == nil {
				return value
			}
		}
	}
	raw := strings.TrimSpace(string(body[:offset]))
	if raw == "" {
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err == nil {
		return value
	}
	// The decoder stops inside the value it rejected, so the value is what
	// follows the last structural delimiter before the offset. A delimiter may
	// itself sit inside a string, so the candidates are tried from the right
	// and the first one that reads a complete value wins. The read runs to the
	// end of the body rather than the offset, because the value's own closing
	// brace lies beyond the offset.
	if value, ok := valueAfterLastDelimiter(string(body), raw); ok {
		return value
	}
	if n, err := strconv.ParseFloat(raw, 64); err == nil {
		return n
	}
	return nil
}

// valueAfterLastDelimiter reads the first complete JSON value that follows one
// of the structural delimiters before the offset.
func valueAfterLastDelimiter(body, prefix string) (any, bool) {
	for index := len(prefix) - 1; index >= 0; index-- {
		if !strings.ContainsRune(",:[{", rune(prefix[index])) {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(body[index+1:]))
		var value any
		if err := decoder.Decode(&value); err == nil {
			return value, true
		}
	}
	return nil, false
}
