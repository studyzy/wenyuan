package memoryops

// This file reproduces how the reference's decoder renders the values it echoes
// back in a validation report. The reference is Python: its json decoder keeps
// an object's member order, and a value stringified into a message uses Python's
// `repr` (single-quoted strings, `True`/`False`/`None`). A Go map sorts its keys
// and `%v` renders a different syntax, so the values that reach the wire are
// built here instead of decoded into maps.

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// orderedObjectValue is a JSON object whose members keep the order the caller
// wrote them in. The reference's decoder preserves that order, so a validation
// item echoing an object renders it as written rather than with a Go map's
// sorted keys.
type orderedObjectValue []orderedObjectMember

// orderedObjectMember is one member of an orderedObjectValue.
type orderedObjectMember struct {
	Key   string
	Value json.RawMessage
}

// MarshalJSON writes the object's members in the recorded order, each value as
// the bytes it arrived as.
func (o orderedObjectValue) MarshalJSON() ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for index, member := range o {
		if index > 0 {
			buffer.WriteByte(',')
		}
		key, err := json.Marshal(member.Key)
		if err != nil {
			return nil, err
		}
		buffer.Write(key)
		buffer.WriteByte(':')
		value := member.Value
		if len(bytes.TrimSpace(value)) == 0 {
			value = json.RawMessage("null")
		}
		buffer.Write(value)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

// orderedJSONObject reads a JSON object keeping its member order, reporting
// false when the bytes are not an object.
func orderedJSONObject(raw []byte) (orderedObjectValue, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, false
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, false
	}
	out := orderedObjectValue{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, false
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, false
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, false
		}
		out = append(out, orderedObjectMember{Key: key, Value: value})
	}
	if _, err := decoder.Token(); err != nil {
		return nil, false
	}
	return out, true
}

// stageCtx is the context the reference attaches to a content-block tag failure.
// Its members are reported in this order — the discriminator, the tag that did
// not match, then the accepted set — regardless of which of them apply.
type stageCtx struct {
	Discriminator string  `json:"discriminator"`
	Tag           *string `json:"tag,omitempty"`
	ExpectedTags  string  `json:"expected_tags,omitempty"`
}

// discriminatorText renders a non-string discriminator the way the framework
// stringifies it for the mismatch message: JSON null as Python's `None`,
// booleans as `True`/`False`, and containers with Python's repr (single-quoted
// keys, ", " between members).
func discriminatorText(raw []byte) string {
	switch string(raw) {
	case "null":
		return "None"
	case "true":
		return "True"
	case "false":
		return "False"
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	var builder strings.Builder
	writePythonValue(&builder, value)
	return builder.String()
}

// writePythonValue writes Python's repr of a decoded JSON value.
func writePythonValue(b *strings.Builder, value any) {
	switch typed := value.(type) {
	case nil:
		b.WriteString("None")
	case bool:
		if typed {
			b.WriteString("True")
		} else {
			b.WriteString("False")
		}
	case string:
		b.WriteString(pythonReprString(typed))
	case float64:
		b.WriteString(strconv.FormatFloat(typed, 'g', -1, 64))
	case []any:
		b.WriteByte('[')
		for index, element := range typed {
			if index > 0 {
				b.WriteString(", ")
			}
			writePythonValue(b, element)
		}
		b.WriteByte(']')
	case map[string]any:
		b.WriteByte('{')
		first := true
		for _, key := range sortedStringKeys(typed) {
			if !first {
				b.WriteString(", ")
			}
			first = false
			b.WriteString(pythonReprString(key))
			b.WriteString(": ")
			writePythonValue(b, typed[key])
		}
		b.WriteByte('}')
	}
}

// pythonReprString renders a string the way Python's repr does: single quotes
// unless the text carries one and not the other.
func pythonReprString(value string) string {
	quote := byte('\'')
	if strings.ContainsRune(value, '\'') && !strings.ContainsRune(value, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range value {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if byte(r) == quote {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

// sortedStringKeys returns a map's keys in sorted order.
func sortedStringKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
