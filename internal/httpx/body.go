package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
)

// ErrEmptyBody reports a request that carried no body at all. The framework
// answers such a request with the item for a missing required field, so the
// rule lives beside the decoder rather than in every handler.
var ErrEmptyBody = errors.New("empty request body")

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
	RecordUnknownBodyFields(r, body, target)
	if len(strings.TrimSpace(string(body))) == 0 {
		return BodyDecodeFailure(ErrEmptyBody, body)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return BodyDecodeFailure(err, body)
	}
	return nil
}

// BodyDecodeFailure renders a decode error for a body the caller already read as
// the framework's structured item. A handler that reads the body itself — to
// report the fields it does not know, for instance — reports a rejected body the
// same way the shared decoder does.
func BodyDecodeFailure(err error, body []byte) *ValidationFailure {
	return bodyFailure(err, body)
}

// bodyFailure renders a decode error as the framework's structured item.
func bodyFailure(err error, body []byte) *ValidationFailure {
	if errors.Is(err, ErrEmptyBody) {
		// An absent body is a missing required field whose input is null: there
		// is no object for the field to be missing from.
		return &ValidationFailure{Errors: []ValidationError{Missing([]string{"body"}, nil)}}
	}
	var syntaxError *json.SyntaxError
	if errors.As(err, &syntaxError) {
		loc := []string{"body"}
		if syntaxError.Offset > 0 {
			loc = append(loc, fmt.Sprintf("%d", syntaxError.Offset))
		}
		return &ValidationFailure{Errors: []ValidationError{
			ValidationItem("json_invalid", loc, "JSON decode error", map[string]any{},
				map[string]any{"error": syntaxError.Error()}),
		}}
	}
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &typeError) {
		loc := []string{"body"}
		if typeError.Field != "" {
			loc = append(loc, strings.Split(typeError.Field, ".")...)
		}
		kind, msg := typeVocabulary(typeError)
		return &ValidationFailure{Errors: []ValidationError{
			ValidationItem(kind, loc, msg, scalarInput(body, typeError.Offset), nil),
		}}
	}
	return &ValidationFailure{Errors: []ValidationError{
		ValidationItem("json_invalid", []string{"body"}, "JSON decode error", nil,
			map[string]any{"error": err.Error()}),
	}}
}

// typeVocabulary names a wrong-typed value the way the framework names it for
// the same failure. The framework keys the item on the declared type and on the
// JSON kind that was sent: a string that will not convert to the type is a
// `<type>_parsing` failure, any other kind a `<type>_type` one, and a number
// carrying a fraction sent for an integer has a name of its own.
func typeVocabulary(err *json.UnmarshalTypeError) (string, string) {
	parsing := err.Value == "string"
	switch err.Type.Kind() {
	case reflect.String:
		return typed("string_type")
	case reflect.Bool:
		if parsing {
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
func scalarInput(body []byte, offset int64) any {
	if offset <= 0 || offset > int64(len(body)) {
		return nil
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
