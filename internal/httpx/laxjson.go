package httpx

import (
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"strings"
)

// The reference service validates request bodies with its model library in the
// library's default "lax" mode, which coerces between compatible JSON kinds
// before the value reaches the model: an integer field takes a numeric string
// or a boolean, a float field takes a numeric string or a boolean, and a
// boolean field takes a 0/1 number or one of a fixed set of words. Go's
// encoding/json refuses those bodies outright, so a client that sends exactly
// what the reference accepts would be answered with a 422 here.
//
// DecodeLax closes that gap. The strict decoder stays the gate: a body it
// accepts is handed back untouched. Only a body it refuses is walked a second
// time, guided by the Go types the body decodes into, and only the values the
// reference's lax mode accepts are rewritten in place. Everything else keeps its
// original bytes, so the item the existing machinery builds for a rejected value
// (type, loc, msg, input) is exactly what it was.

// DecodeLax unmarshals body into target, rewriting the values the reference's
// lax mode accepts but encoding/json rejects. It returns the body a failure
// should be reported against: the rewritten body when a value was rewritten (so
// the decoder's byte offsets point at the same text), and the original body
// otherwise.
//
// A caller that reads the request body itself uses this in place of
// json.Unmarshal and reports a failure through BodyDecodeFailure with the
// returned body.
func DecodeLax(body []byte, target any) ([]byte, error) {
	return decodeLaxSchema(body, target, schemaForTarget(target))
}

// decodeLaxSchema is DecodeLax with the request schema a route's body was
// declared with already resolved. Passing the schema in keeps the resolution on
// the route that matched rather than on the model's name.
//
// The null pass runs only for a body that carries a `null` token, so a body
// with none pays a byte scan and nothing else. A body that does carry one is
// answered from the resolved declaration: the walk names every value the
// reference's model refuses, so its items are the answer, and the decoder is
// not consulted further.
func decodeLaxSchema(body []byte, target any, root *schemaNode) ([]byte, error) {
	body, err := checkBodyText(body)
	if err != nil {
		return nil, err
	}
	if items := nullRejectionItems(body, root); len(items) > 0 {
		return body, &ValidationFailure{Errors: items}
	}
	err = json.Unmarshal(body, target)
	if err == nil {
		return body, nil
	}
	coerced, changed := coerceLax(body, target)
	if changed {
		err = json.Unmarshal(coerced, target)
	}
	return coerced, err
}

// coerceLax rewrites the values in body that the reference's lax mode accepts
// for the Go type the body decodes into. It reports whether anything changed;
// a body with no such value is returned as-is, so a rejection is reported
// against the bytes the client sent.
func coerceLax(body []byte, target any) ([]byte, bool) {
	value := reflect.ValueOf(target)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return body, false
	}
	walk := &laxWalker{body: body}
	walk.value(value.Type().Elem())
	walk.ws()
	if walk.broken || walk.i != len(body) || len(walk.edits) == 0 {
		return body, false
	}
	coerced := make([]byte, 0, len(body))
	at := 0
	for _, edit := range walk.edits {
		coerced = append(coerced, body[at:edit.start]...)
		coerced = append(coerced, edit.text...)
		at = edit.end
	}
	coerced = append(coerced, body[at:]...)
	return coerced, true
}

// laxEdit is one in-place replacement of a scalar's bytes.
type laxEdit struct {
	start int
	end   int
	text  string
}

// maxLaxDepth bounds the walk the way the standard decoder bounds its own, so
// a body crafted to nest without end cannot exhaust the stack.
const maxLaxDepth = 10000

// laxWalker walks a JSON document beside the Go type it decodes into,
// collecting the scalar values the reference's lax mode would rewrite.
type laxWalker struct {
	body   []byte
	i      int
	depth  int
	broken bool
	edits  []laxEdit
}

// value consumes one JSON value, coercing it when target is a scalar field the
// reference's lax mode accepts a different kind for.
func (w *laxWalker) value(target reflect.Type) {
	w.ws()
	if w.i >= len(w.body) || w.depth > maxLaxDepth {
		w.broken = true
		return
	}
	switch w.body[w.i] {
	case '{':
		w.object(target)
	case '[':
		w.array(target)
	case '"':
		start := w.i
		text, ok := w.str()
		if !ok {
			w.broken = true
			return
		}
		if replacement, ok := laxString(target, text); ok {
			w.edits = append(w.edits, laxEdit{start: start, end: w.i, text: replacement})
		}
	default:
		start := w.i
		literal := w.literal()
		if literal == nil {
			w.broken = true
			return
		}
		if replacement, ok := laxLiteral(target, literal); ok {
			w.edits = append(w.edits, laxEdit{start: start, end: w.i, text: replacement})
		}
	}
}

// object consumes an object, resolving each member against the struct or map
// type it decodes into.
func (w *laxWalker) object(target reflect.Type) {
	w.i++ // '{'
	w.depth++
	for {
		w.ws()
		if w.i >= len(w.body) {
			w.broken = true
			return
		}
		if w.body[w.i] == '}' {
			w.i++
			w.depth--
			return
		}
		if w.body[w.i] != '"' {
			w.broken = true
			return
		}
		key, ok := w.str()
		if !ok {
			w.broken = true
			return
		}
		w.ws()
		if w.i >= len(w.body) || w.body[w.i] != ':' {
			w.broken = true
			return
		}
		w.i++
		if member, ok := laxMemberType(target, key); ok {
			w.value(member)
		} else {
			w.skip()
		}
		w.ws()
		if w.i >= len(w.body) {
			w.broken = true
			return
		}
		switch w.body[w.i] {
		case ',':
			w.i++
		case '}':
			w.i++
			w.depth--
			return
		default:
			w.broken = true
			return
		}
	}
}

// array consumes an array, resolving each element against the element type it
// decodes into.
func (w *laxWalker) array(target reflect.Type) {
	element, ok := laxElementType(target)
	w.i++ // '['
	w.depth++
	for {
		w.ws()
		if w.i >= len(w.body) {
			w.broken = true
			return
		}
		if w.body[w.i] == ']' {
			w.i++
			w.depth--
			return
		}
		if ok {
			w.value(element)
		} else {
			w.skip()
		}
		w.ws()
		if w.i >= len(w.body) {
			w.broken = true
			return
		}
		switch w.body[w.i] {
		case ',':
			w.i++
		case ']':
			w.i++
			w.depth--
			return
		default:
			w.broken = true
			return
		}
	}
}

// skip consumes one JSON value without coercing anything inside it, so a field
// the target does not declare costs nothing beyond its text.
func (w *laxWalker) skip() {
	w.ws()
	if w.i >= len(w.body) || w.depth > maxLaxDepth {
		w.broken = true
		return
	}
	switch w.body[w.i] {
	case '{':
		w.i++
		w.depth++
		for {
			w.ws()
			if w.i >= len(w.body) {
				w.broken = true
				return
			}
			if w.body[w.i] == '}' {
				w.i++
				w.depth--
				return
			}
			if w.body[w.i] != '"' {
				w.broken = true
				return
			}
			if _, ok := w.str(); !ok {
				w.broken = true
				return
			}
			w.ws()
			if w.i >= len(w.body) || w.body[w.i] != ':' {
				w.broken = true
				return
			}
			w.i++
			w.skip()
			w.ws()
			if w.i >= len(w.body) {
				w.broken = true
				return
			}
			switch w.body[w.i] {
			case ',':
				w.i++
			case '}':
				w.i++
				w.depth--
				return
			default:
				w.broken = true
				return
			}
		}
	case '[':
		w.i++
		w.depth++
		for {
			w.ws()
			if w.i >= len(w.body) {
				w.broken = true
				return
			}
			if w.body[w.i] == ']' {
				w.i++
				w.depth--
				return
			}
			w.skip()
			w.ws()
			if w.i >= len(w.body) {
				w.broken = true
				return
			}
			switch w.body[w.i] {
			case ',':
				w.i++
			case ']':
				w.i++
				w.depth--
				return
			default:
				w.broken = true
				return
			}
		}
	case '"':
		if _, ok := w.str(); !ok {
			w.broken = true
		}
	default:
		if w.literal() == nil {
			w.broken = true
		}
	}
}

// str consumes a JSON string and returns its decoded content.
func (w *laxWalker) str() (string, bool) {
	w.i++ // opening quote
	var out strings.Builder
	for w.i < len(w.body) {
		switch c := w.body[w.i]; c {
		case '\\':
			if w.i+1 >= len(w.body) {
				return "", false
			}
			switch w.body[w.i+1] {
			case 'u':
				if w.i+6 > len(w.body) {
					return "", false
				}
				code, err := strconv.ParseUint(string(w.body[w.i+2:w.i+6]), 16, 32)
				if err != nil {
					return "", false
				}
				out.WriteRune(rune(code))
				w.i += 6
			default:
				out.WriteByte(w.body[w.i+1])
				w.i += 2
			}
		case '"':
			w.i++
			return out.String(), true
		default:
			out.WriteByte(c)
			w.i++
		}
	}
	return "", false
}

// literal consumes a number, true, false or null and returns its bytes, or nil
// when the document is not one of them.
func (w *laxWalker) literal() []byte {
	start := w.i
	for w.i < len(w.body) {
		switch w.body[w.i] {
		case ',', '}', ']', ' ', '\t', '\n', '\r':
			if w.i == start {
				return nil
			}
			return w.body[start:w.i]
		}
		w.i++
	}
	if w.i == start {
		return nil
	}
	return w.body[start:w.i]
}

// ws skips insignificant whitespace.
func (w *laxWalker) ws() {
	for w.i < len(w.body) {
		switch w.body[w.i] {
		case ' ', '\t', '\n', '\r':
			w.i++
		default:
			return
		}
	}
}

// laxMemberType resolves an object member's name to the Go type the decoder
// would decode it into: the struct field the name selects (exact match first,
// then the decoder's case-insensitive fallback) or a string-keyed map's value.
func laxMemberType(target reflect.Type, key string) (reflect.Type, bool) {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	switch target.Kind() {
	case reflect.Struct:
		if field, ok := structField(target, key, true); ok {
			return coercible(field)
		}
		if field, ok := structField(target, key, false); ok {
			return coercible(field)
		}
	case reflect.Map:
		if target.Key().Kind() == reflect.String {
			return coercible(target.Elem())
		}
	}
	return nil, false
}

// structField finds the field a JSON member name selects. When exact is true
// only a name that matches the field's own name does; otherwise the comparison
// is the decoder's case-insensitive one.
func structField(target reflect.Type, key string, exact bool) (reflect.Type, bool) {
	for index := range target.NumField() {
		field := target.Field(index)
		if field.PkgPath != "" && !field.Anonymous {
			continue
		}
		name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if field.Anonymous && name == "" {
			inner := field.Type
			for inner.Kind() == reflect.Pointer {
				inner = inner.Elem()
			}
			if inner.Kind() == reflect.Struct {
				if promoted, ok := structField(inner, key, exact); ok {
					return promoted, true
				}
				continue
			}
		}
		// The `,string` option changes the kind the decoder expects, so the
		// value is left alone.
		if strings.Contains(options, "string") {
			continue
		}
		if name == "" {
			name = field.Name
		}
		if exact {
			if name == key {
				return field.Type, true
			}
			continue
		}
		if strings.EqualFold(name, key) {
			return field.Type, true
		}
	}
	return nil, false
}

// laxElementType resolves the type a JSON array's elements decode into.
func laxElementType(target reflect.Type) (reflect.Type, bool) {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	switch target.Kind() {
	case reflect.Slice, reflect.Array:
		return coercible(target.Elem())
	}
	return nil, false
}

// coercible reports the type a value is decoded into, or that the value's own
// text must be left alone: a value that lands in an empty interface (or a
// container of them) accepts every JSON kind, so there is nothing to rewrite
// and nothing to walk.
func coercible(target reflect.Type) (reflect.Type, bool) {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if target.Kind() == reflect.Interface {
		return nil, false
	}
	return target, true
}

// laxString coerces a JSON string sent for a scalar field the way the
// reference's lax mode does. A string field takes nothing else, so no string
// value is rewritten for one.
func laxString(target reflect.Type, text string) (string, bool) {
	// A type that decodes itself owns its parsing, so nothing is rewritten for
	// it: andUnmarshalerFree reports that by returning no type at all.
	target = andUnmarshalerFree(target)
	if target == nil {
		return "", false
	}
	switch target.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value, ok := pythonInt(text)
		if !ok {
			return "", false
		}
		return strconv.FormatInt(value, 10), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value, ok := pythonInt(text)
		if !ok || value < 0 {
			return "", false
		}
		return strconv.FormatUint(uint64(value), 10), true
	case reflect.Float32, reflect.Float64:
		value, ok := pythonFloat(text)
		if !ok {
			return "", false
		}
		return strconv.FormatFloat(value, 'g', -1, 64), true
	case reflect.Bool:
		value, ok := boolWord(text)
		if !ok {
			return "", false
		}
		return strconv.FormatBool(value), true
	}
	return "", false
}

// laxLiteral coerces a JSON number, true or false sent for a scalar field. A
// number already decodes into a float field, so only an integer or boolean
// field can be waiting for it: an integer field takes a number with no
// fractional part, and a boolean field takes exactly 0 or 1.
func laxLiteral(target reflect.Type, literal []byte) (string, bool) {
	// Same as laxString: a type that owns its parsing is never rewritten.
	target = andUnmarshalerFree(target)
	if target == nil {
		return "", false
	}
	switch string(literal) {
	case "true", "false":
		return laxBool(target, string(literal) == "true")
	case "null":
		return "", false
	}
	number, err := strconv.ParseFloat(string(literal), 64)
	if err != nil {
		return "", false
	}
	switch target.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if number != math.Trunc(number) || number < -maxInt64 || number >= maxInt64 {
			return "", false
		}
		return strconv.FormatInt(int64(number), 10), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if number != math.Trunc(number) || number < 0 || number >= maxUint64 {
			return "", false
		}
		return strconv.FormatUint(uint64(number), 10), true
	case reflect.Bool:
		switch number {
		case 0:
			return "false", true
		case 1:
			return "true", true
		}
	}
	return "", false
}

// laxBool renders a JSON boolean for a scalar field: the reference's lax mode
// treats true as 1 and false as 0.
func laxBool(target reflect.Type, value bool) (string, bool) {
	switch target.Kind() {
	case reflect.Bool:
		return strconv.FormatBool(value), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		if value {
			return "1", true
		}
		return "0", true
	}
	return "", false
}

// jsonUnmarshaler is the interface a type uses to decode itself. Such a type
// owns its own parsing, so a scalar it declares is never rewritten here.
var jsonUnmarshaler = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()

// andUnmarshalerFree returns the type itself, or no type at all when it decodes
// itself.
func andUnmarshalerFree(target reflect.Type) reflect.Type {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if target.Implements(jsonUnmarshaler) || reflect.PointerTo(target).Implements(jsonUnmarshaler) {
		return nil
	}
	return target
}

// maxInt64 and maxUint64 are the exclusive float bounds of the integer types: a
// float64 equal to them is one step past the last representable value.
const (
	maxInt64  = float64(1 << 63)
	maxUint64 = 18446744073709551616.0
)

// boolWord reads the words the reference's lax mode accepts for a boolean
// field. The comparison is case-insensitive and the value is not trimmed, which
// is what the reference does: "Yes" is a boolean and " Yes" is not.
func boolWord(text string) (bool, bool) {
	switch strings.ToLower(text) {
	case "true", "t", "yes", "y", "on", "1":
		return true, true
	case "false", "f", "no", "n", "off", "0":
		return false, true
	}
	return false, false
}

// LaxInteger reads a JSON string the reference's integer coercion accepts,
// returning the value and whether the string is one. A route that validates a
// body with the shared walk and then reads the value itself uses this so the
// two agree: a numeric string the walk accepted is not rejected afterwards.
// A value outside the platform's int range reports false.
func LaxInteger(text string) (int, bool) {
	value, ok := pythonInt(text)
	if !ok || int64(int(value)) != value {
		return 0, false
	}
	return int(value), true
}

// LaxBoolean reads a value the reference's boolean coercion accepts and returns
// it as a bool: a JSON boolean, one of the accepted words ("true", "yes", "on",
// and their negatives), or the integers 0 and 1. It reports false for anything
// else, so a caller can decide whether the value is a framework rejection
// (`bool_type`/`bool_parsing`) or outside it.
func LaxBoolean(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		return boolWord(typed)
	case float64:
		if typed == 0 || typed == 1 {
			return typed == 1, true
		}
	}
	return false, false
}

// LaxFloat reads a value the reference's integer coercion accepts and returns it
// as an integer: a JSON number with no fractional part, or a string Python's
// int() reads. It reports false for anything else, including a number carrying a
// fraction, which the framework names separately (`int_from_float`).
func LaxFloat(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		if typed != math.Trunc(typed) {
			return 0, false
		}
		return int(typed), true
	case string:
		return LaxInteger(typed)
	}
	return 0, false
}

// pythonInt reads a string the reference's integer coercion accepts, the way
// Python's int() parser reads one: surrounding whitespace and an optional sign,
// digits with single underscores between them, and a fractional part only when
// every one of its digits is zero (so "1.0" is 1 and "1.5" is not an integer).
// A value outside int64 has no Go spelling here and is left alone.
func pythonInt(text string) (int64, bool) {
	trimmed := strings.TrimSpace(text)
	i := 0
	negative := false
	if i < len(trimmed) && (trimmed[i] == '+' || trimmed[i] == '-') {
		negative = trimmed[i] == '-'
		i++
	}
	digits := scanDigits(trimmed, &i)
	if digits == "" {
		return 0, false
	}
	if i < len(trimmed) && trimmed[i] == '.' {
		i++
		if !scanZeroFraction(trimmed, &i) {
			return 0, false
		}
	}
	if i != len(trimmed) {
		return 0, false
	}
	value, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, false
	}
	if negative {
		value = -value
	}
	return value, true
}

// pythonFloat reads a string the reference's float coercion accepts, the way
// Python's float() parser reads one: surrounding whitespace, an optional sign,
// and a decimal literal whose underscores sit between digits. A non-finite
// value ("inf", "nan" and their spellings) is a float the reference accepts but
// a JSON body has no way to carry, so it keeps its rejection.
func pythonFloat(text string) (float64, bool) {
	trimmed := strings.TrimSpace(text)
	body := trimmed
	if body != "" && (body[0] == '+' || body[0] == '-') {
		body = body[1:]
	}
	switch strings.ToLower(body) {
	case "inf", "infinity", "nan":
		return 0, false
	}
	if !decimalLiteral(trimmed) {
		return 0, false
	}
	value, err := strconv.ParseFloat(strings.ReplaceAll(trimmed, "_", ""), 64)
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, false
	}
	return value, true
}

// decimalLiteral reports whether text is a decimal number literal in the
// grammar Python's float() accepts: optional sign, digits with an optional
// fractional part, and an optional exponent, with underscores only between
// digits. It rejects the literals Go's parser would otherwise allow and
// Python's does not, such as a hexadecimal float.
func decimalLiteral(text string) bool {
	i := 0
	if i < len(text) && (text[i] == '+' || text[i] == '-') {
		i++
	}
	whole := scanDigits(text, &i)
	fraction := ""
	if i < len(text) && text[i] == '.' {
		i++
		fraction = scanDigits(text, &i)
	}
	if whole == "" && fraction == "" {
		return false
	}
	if i < len(text) && (text[i] == 'e' || text[i] == 'E') {
		i++
		if i < len(text) && (text[i] == '+' || text[i] == '-') {
			i++
		}
		if scanDigits(text, &i) == "" {
			return false
		}
	}
	return i == len(text)
}

// scanDigits consumes a run of digits at *i, allowing a single underscore
// between two digits, and returns the digits without their separators. A
// misplaced underscore ends the run rather than being consumed, so the caller's
// end-of-input check rejects it.
func scanDigits(text string, i *int) string {
	var digits strings.Builder
	for *i < len(text) {
		switch c := text[*i]; {
		case c >= '0' && c <= '9':
			digits.WriteByte(c)
			*i++
		case c == '_' && digits.Len() > 0 && *i+1 < len(text) && isASCIIDigit(text[*i+1]):
			*i++
		default:
			return digits.String()
		}
	}
	return digits.String()
}

// scanZeroFraction consumes a fractional part whose digits must all be zero, the
// only fractional part the reference's integer coercion accepts.
func scanZeroFraction(text string, i *int) bool {
	start := *i
	for *i < len(text) && text[*i] == '0' {
		*i++
	}
	return *i != start
}

func isASCIIDigit(c byte) bool { return c >= '0' && c <= '9' }
