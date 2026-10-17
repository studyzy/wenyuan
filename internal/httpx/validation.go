package httpx

import (
	"fmt"
	"math"
	"strings"
)

// ValidationItem builds one structured item. Every builder below routes
// through it so the item shape stays in one place.
//
// The reference emits exactly five keys — type, loc, msg, input and, when the
// type carries one, ctx. It deliberately drops the framework's documentation
// link (its validation handler builds the items with `include_url=False`), and
// the served contract document declares the same five, so no link is attached
// here.
func ValidationItem(kind string, loc []string, msg string, input any, ctx any) ValidationError {
	return ValidationError{Type: kind, Loc: loc, Msg: msg, Input: input, Ctx: ctx}
}

// Missing builds the item the framework emits for an absent required field.
// input is the object the field was missing from.
func Missing(loc []string, input any) ValidationError {
	return ValidationItem("missing", loc, "Field required", input, nil)
}

// GreaterThanEqual builds the item for a value below an inclusive lower bound.
func GreaterThanEqual(loc []string, input any, ge int) ValidationError {
	return ValidationItem("greater_than_equal", loc,
		fmt.Sprintf("Input should be greater than or equal to %d", ge),
		input, map[string]any{"ge": ge})
}

// StringTooShort builds the item for a string below its declared minimum length.
func StringTooShort(loc []string, input any, minLength int) ValidationError {
	return ValidationItem("string_too_short", loc,
		fmt.Sprintf("String should have at least %d character%s", minLength, plural(minLength)),
		input, map[string]any{"min_length": minLength})
}

// plural renders an English plural suffix so a length reads the way the
// framework words it.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// LessThanEqual builds the item for a value above an inclusive upper bound.
func LessThanEqual(loc []string, input any, le int) ValidationError {
	return ValidationItem("less_than_equal", loc,
		fmt.Sprintf("Input should be less than or equal to %d", le),
		input, map[string]any{"le": le})
}

// Enum builds the item for a value outside a named enum's members.
func Enum(loc []string, input any, expected []string) ValidationError {
	return enumLike("enum", loc, input, expected)
}

// Literal builds the item for a value outside a `Literal`'s members.
func Literal(loc []string, input any, expected []string) ValidationError {
	return enumLike("literal_error", loc, input, expected)
}

func enumLike(kind string, loc []string, input any, expected []string) ValidationError {
	rendered := ExpectedValues(expected)
	return ValidationItem(kind, loc, "Input should be "+rendered, input, map[string]any{"expected": rendered})
}

// ValueError builds the item a custom field validator reports when it rejects a
// value. The framework prefixes the raised message with "Value error, " and
// files the exception under ctx.
func ValueError(loc []string, input any, msg string) ValidationError {
	return ValidationItem("value_error", loc, "Value error, "+msg, input, map[string]any{"error": map[string]any{}})
}

// TypeMismatch builds the item the framework emits for a value whose JSON type
// does not match the field's declared scalar type.
func TypeMismatch(kind string, loc []string, input any) ValidationError {
	return ValidationItem(kind, loc, TypeMessage(kind), input, nil)
}

// ScalarTypeMismatch builds the item for a decoded value that does not fit the
// scalar type it was sent for, naming it the way the framework does: a string
// that will not convert is the type's `_parsing` entry, any other kind its
// `_type` entry, and a number carrying a fraction sent for an integer is
// `int_from_float`. base is the type's name ("int", "float", "bool").
func ScalarTypeMismatch(base string, loc []string, input any) ValidationError {
	if number, ok := input.(float64); ok && base == "int" && number != math.Trunc(number) {
		return TypeMismatch("int_from_float", loc, input)
	}
	if _, isString := input.(string); isString {
		return TypeMismatch(base+"_parsing", loc, input)
	}
	return TypeMismatch(base+"_type", loc, input)
}

// TypeMessage returns the framework's wording for a wrong-typed value. The
// wording depends on the declared type and on the JSON kind that was sent: a
// string that will not convert fails to *parse* as the type and is worded so,
// any other kind is simply the wrong type.
func TypeMessage(kind string) string {
	switch kind {
	case "int_parsing":
		return "Input should be a valid integer, unable to parse string as an integer"
	case "int_type":
		return "Input should be a valid integer"
	case "int_from_float":
		return "Input should be a valid integer, got a number with a fractional part"
	case "float_parsing":
		return "Input should be a valid number, unable to parse string as a number"
	case "float_type":
		return "Input should be a valid number"
	case "bool_parsing":
		return "Input should be a valid boolean, unable to interpret input"
	case "bool_type":
		return "Input should be a valid boolean"
	case "string_type":
		return "Input should be a valid string"
	case "model_attributes_type":
		return modelShapeMessage
	case "datetime_type":
		return "Input should be a valid datetime"
	case "list_type":
		return "Input should be a valid list"
	case "dict_type":
		return "Input should be a valid dictionary"
	}
	return "Input should be a valid value"
}

// ExpectedValues renders a member list the way the framework words it in an enum
// or literal message: single-quoted, comma-separated, and joined with "or"
// before the last member.
func ExpectedValues(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = "'" + value + "'"
	}
	switch len(quoted) {
	case 0:
		return ""
	case 1:
		return quoted[0]
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " or " + quoted[len(quoted)-1]
}
