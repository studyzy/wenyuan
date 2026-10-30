package mcp

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

// This file reproduces the byte order the reference's `tools/list` answer
// carries.
//
// The reference's tool list is a pydantic model serialized in declaration
// order: each entry is `name, title, description, inputSchema, outputSchema,
// annotations, _meta` (title is always emitted, a title-cased copy of the name),
// and every JSON schema inside it comes from pydantic, which lists the members
// of a schema node in the order it builds them —
// `additionalProperties, description, properties, required, type` for an
// object, and `anyOf, default, description, title` for a property whose
// members are a union. A Go `map[string]any` marshals its keys sorted, which
// reorders all of that, so the entries are rendered through an ordered value
// instead of the encoder's map path.

// orderedObject is a JSON object whose members render in the order it holds
// them. Its values are marshalled normally (so a nested ordered value renders
// in its own order too).
type orderedObject []orderedMember

// orderedMember is one member of an orderedObject.
type orderedMember struct {
	key   string
	value any
}

// MarshalJSON writes the object's members in order.
func (o orderedObject) MarshalJSON() ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for index, member := range o {
		if index > 0 {
			buffer.WriteByte(',')
		}
		key, err := json.Marshal(member.key)
		if err != nil {
			return nil, err
		}
		buffer.Write(key)
		buffer.WriteByte(':')
		value, err := json.Marshal(member.value)
		if err != nil {
			return nil, err
		}
		buffer.Write(value)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

// schemaMemberOrder is the member order pydantic emits for a JSON-schema node,
// most specific first: an object's node, a property's union node, and the
// scalar keyword sets a single-type node carries.
var schemaMemberOrder = []string{
	"additionalProperties", "anyOf", "const", "default", "description", "enum", "format",
	"items", "maximum", "minimum", "oneOf", "properties", "required", "title", "type",
	"x-fastmcp-wrap-result",
}

// orderedSchema renders a JSON-schema value with pydantic's member order.
//
// A node's members are emitted in `schemaMemberOrder`; a member that is a map
// is itself a schema node and is ordered the same way, and a member that is a
// list is walked so the nodes inside it (a union's branches, a node's
// `items`) are ordered too. A non-object value is returned unchanged.
func orderedSchema(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return orderedSchemaNode(typed)
	case json.RawMessage:
		return typed
	default:
		return value
	}
}

// orderedSchemaNode orders one schema node's members.
func orderedSchemaNode(node map[string]any) orderedObject {
	out := make(orderedObject, 0, len(node))
	seen := make(map[string]bool, len(node))
	for _, member := range schemaMemberOrder {
		value, present := node[member]
		if !present {
			continue
		}
		seen[member] = true
		out = append(out, orderedMember{key: member, value: orderedSchemaChild(member, value)})
	}
	rest := make([]string, 0, len(node))
	for member := range node {
		if !seen[member] {
			rest = append(rest, member)
		}
	}
	sort.Strings(rest)
	for _, member := range rest {
		out = append(out, orderedMember{key: member, value: orderedSchemaChild(member, node[member])})
	}
	return out
}

// orderedSchemaChild orders the schema nodes inside one member's value.
func orderedSchemaChild(_ string, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return orderedSchemaNode(typed)
	case []any:
		ordered := make([]any, len(typed))
		for index, entry := range typed {
			ordered[index] = orderedSchemaChild("", entry)
		}
		return ordered
	default:
		return value
	}
}

// fastmcpToolEntries renders the advertised tools as the entries the
// reference's `tools/list` answer carries, in the reference's member order.
func fastmcpToolEntries(defs []tool, withAnnotations bool) []orderedObject {
	entries := make([]orderedObject, 0, len(defs))
	for _, def := range defs {
		members := orderedObject{
			{key: "name", value: def.Name},
			// FastMCP always emits the title it derived from the name.
			{key: "title", value: toolTitle(def.Name)},
			{key: "description", value: def.Description},
			{key: "inputSchema", value: orderedSchema(def.InputSchema)},
			{key: "outputSchema", value: orderedSchema(toolOutputSchema(def.Name))},
		}
		if withAnnotations {
			// A ToolAnnotations is a pydantic model of its own, so its members
			// follow that model's declaration order rather than the schema one.
			members = append(members, orderedMember{key: "annotations", value: orderedAnnotations(toolAnnotations(def.Name))})
		}
		members = append(members, orderedMember{
			key: "_meta", value: orderedObject{{key: "fastmcp", value: orderedObject{{key: "tags", value: []any{}}}}},
		})
		entries = append(entries, members)
	}
	return entries
}

// annotationMemberOrder is ToolAnnotations' declaration order
// (`title, readOnlyHint, destructiveHint, idempotentHint, openWorldHint`), which
// is the order pydantic serializes one in.
var annotationMemberOrder = []string{"title", "readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint"}

// orderedAnnotations renders an annotation set in the model's declaration order.
func orderedAnnotations(annotations map[string]any) orderedObject {
	out := make(orderedObject, 0, len(annotations))
	seen := make(map[string]bool, len(annotations))
	for _, member := range annotationMemberOrder {
		if value, present := annotations[member]; present {
			seen[member] = true
			out = append(out, orderedMember{key: member, value: value})
		}
	}
	rest := make([]string, 0, len(annotations))
	for member := range annotations {
		if !seen[member] {
			rest = append(rest, member)
		}
	}
	sort.Strings(rest)
	for _, member := range rest {
		out = append(out, orderedMember{key: member, value: annotations[member]})
	}
	return out
}

// toolTitle is the title FastMCP derives from a tool name when the tool does not
// declare one: underscores and hyphens become spaces and each word is
// title-cased (`fastmcp.tools.base._default_title`, "always emit a title" for
// clients that would otherwise drop the tool).
func toolTitle(name string) string {
	words := strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(name))
	for index, word := range words {
		runes := []rune(strings.ToLower(word))
		runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
		words[index] = string(runes)
	}
	return strings.Join(words, " ")
}
