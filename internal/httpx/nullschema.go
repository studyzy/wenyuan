package httpx

// The reference validates every request body against a declared model whose
// field types decide whether the JSON literal `null` is a value or a failure.
// Go's decoder never refuses `null`: it writes the zero value into the field
// (or leaves it alone) and reports nothing, so a body the reference answers
// with a 422 is answered with a 200 here.
//
// The served contract document (openAPIDocument, captured from the reference)
// declares exactly what the reference's model accepts: a field that admits
// `null` is written as a union with a `"type": "null"` branch, and every other
// field is a single type. This file walks a body that carries a `null` token
// beside that declaration and builds the framework's item for each `null` that
// lands on a field which does not admit one, so the rejection the caller sees
// is the one the reference serves — same type name, same loc, same msg, same
// ctx, and the same field order.
//
// The walk runs only when the body actually carries the token, so the common
// path pays a byte scan and nothing more.

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// modelShapeMessage is the framework's wording for a value that is not the
// object a model field extracts its own fields from.
const modelShapeMessage = "Input should be a valid dictionary or object to extract fields from"

// nullToken is the JSON literal the cheap pre-check searches for.
var nullToken = []byte("null")

// openAPIContract is the served contract document's schema table: the request
// body schemas a route's body is declared with, and the path table that maps a
// matched route to the schema it takes.
type openAPIContract struct {
	schemas map[string]json.RawMessage
	paths   map[string]map[string]json.RawMessage
}

var (
	contractOnce sync.Once
	contract     openAPIContract
)

// requestContract parses the served contract document once. A document that
// will not parse leaves the table empty, which disables the walk rather than
// failing a request.
func requestContract() *openAPIContract {
	contractOnce.Do(func() {
		var doc struct {
			Paths      map[string]map[string]json.RawMessage `json:"paths"`
			Components struct {
				Schemas map[string]json.RawMessage `json:"schemas"`
			} `json:"components"`
		}
		if err := json.Unmarshal([]byte(openAPIDocument), &doc); err != nil {
			return
		}
		contract = openAPIContract{schemas: doc.Components.Schemas, paths: doc.Paths}
	})
	return &contract
}

// schemaKind is the declared type of one field's value, reduced to the
// vocabulary the framework names a rejected `null` with.
type schemaKind uint8

const (
	schemaUntyped schemaKind = iota // no declared type: every value is accepted
	schemaModel                     // an object with declared members
	schemaDict                      // a free-form object
	schemaArray
	schemaString
	schemaInteger
	schemaNumber
	schemaBoolean
	schemaEnum
	schemaConst
	schemaDatetime
	schemaUnion // an anyOf/oneOf of more than one alternative
)

// schemaMember is one declared member of an object, in declaration order.
type schemaMember struct {
	name string
	node *schemaNode
}

// schemaBranch is one alternative of a union, named the way the framework
// names the alternative in the location of the item it reports.
type schemaBranch struct {
	name string
	node *schemaNode
}

// schemaNode is one node of the served contract's schema tree: what the
// reference's model declares at one position of a body.
type schemaNode struct {
	kind     schemaKind
	nullable bool // the declaration admits `null`
	refName  string
	members  []schemaMember
	items    *schemaNode
	entries  *schemaNode // a free-form object's value schema
	values   []string    // an enum's members or a const's value
	required []string    // the member names the declaration requires
	// branches holds a union's alternatives in declaration order. They are
	// reported all at once when none of them validates the value, which is how
	// the framework reports a value that fits no alternative.
	branches []schemaBranch
	// tag names the discriminator property of a tagged union, and tags maps
	// each accepted tag value to the alternative it selects. tagOrder keeps the
	// tags in declaration order for the message that lists them.
	tag      string
	tags     map[string]*schemaNode
	tagOrder []string
}

var (
	schemaNodesMu sync.Mutex
	schemaNodes   = map[string]*schemaNode{}
)

// schemaNodeByName resolves a named schema, caching the result. A name that is
// absent (or a schema that will not parse) yields no node, so a field it could
// not describe is left alone.
func schemaNodeByName(name string) *schemaNode {
	if name == "" {
		return nil
	}
	index := requestContract()
	if index.schemas == nil {
		return nil
	}
	raw, ok := index.schemas[name]
	if !ok {
		return nil
	}
	schemaNodesMu.Lock()
	if node, seen := schemaNodes[name]; seen {
		schemaNodesMu.Unlock()
		return node
	}
	// A self-referential model resolves its own name while it is being parsed;
	// recording the in-progress state as absent stops the recursion.
	schemaNodes[name] = nil
	schemaNodesMu.Unlock()
	node := parseSchemaNode(raw, name)
	schemaNodesMu.Lock()
	schemaNodes[name] = node
	schemaNodesMu.Unlock()
	return node
}

// parseSchemaNode reads one schema value. refName names the schema the value
// came from when it came from a named one, which is how the framework's enum
// vocabulary is told from its literal one.
func parseSchemaNode(raw json.RawMessage, refName string) *schemaNode {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil
	}
	if ref, ok := object["$ref"]; ok {
		target := schemaNodeByName(lastPathSegment(jsonString(ref)))
		if target == nil {
			return nil
		}
		node := *target
		if node.refName == "" {
			node.refName = lastPathSegment(jsonString(ref))
		}
		return &node
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		if _, ok := object[key]; ok {
			return parseSchemaUnion(object, key, refName)
		}
	}
	if value, ok := object["const"]; ok {
		return &schemaNode{kind: schemaConst, refName: refName, values: []string{jsonString(value)}}
	}
	if values, ok := object["enum"]; ok {
		return &schemaNode{kind: schemaEnum, refName: refName, values: jsonStrings(values)}
	}
	declared, nullable := schemaDeclaredType(object)
	node := &schemaNode{nullable: nullable, refName: refName}
	switch declared {
	case "object":
		if properties, ok := object["properties"]; ok {
			node.kind = schemaModel
			node.members = orderedSchemaMembers(properties)
			node.required = jsonStrings(object["required"])
			return node
		}
		node.kind = schemaDict
		if entries, ok := object["additionalProperties"]; ok {
			node.entries = parseSchemaNode(entries, "")
		}
		return node
	case "array":
		node.kind = schemaArray
		if items, ok := object["items"]; ok {
			node.items = parseSchemaNode(items, "")
		}
		return node
	case "string":
		if format := jsonString(object["format"]); format == "date-time" {
			node.kind = schemaDatetime
			return node
		}
		node.kind = schemaString
		return node
	case "integer":
		node.kind = schemaInteger
		return node
	case "number":
		node.kind = schemaNumber
		return node
	case "boolean":
		node.kind = schemaBoolean
		return node
	case "null":
		node.nullable = true
		return node
	}
	return node
}

// parseSchemaUnion reads an anyOf/oneOf list. A branch that declares only
// `null` makes the whole field nullable; a single remaining branch is the
// field's type. More than one branch is a union the framework reports one item
// per alternative for, and a `discriminator` names the property whose value
// selects the alternative.
func parseSchemaUnion(object map[string]json.RawMessage, key, refName string) *schemaNode {
	var branches []json.RawMessage
	if err := json.Unmarshal(object[key], &branches); err != nil {
		return nil
	}
	nullable := false
	var concrete []json.RawMessage
	for _, branch := range branches {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(branch, &probe); err != nil {
			continue
		}
		if declared, _ := schemaDeclaredType(probe); declared == "null" && len(probe) == 1 {
			nullable = true
			continue
		}
		concrete = append(concrete, branch)
	}
	if len(concrete) == 0 {
		return &schemaNode{nullable: true, refName: refName}
	}
	if len(concrete) == 1 {
		node := parseSchemaNode(concrete[0], refName)
		if node == nil {
			return nil
		}
		if nullable && !node.nullable {
			copied := *node
			copied.nullable = true
			node = &copied
		}
		return node
	}
	union := &schemaNode{kind: schemaUnion, nullable: nullable, refName: refName}
	for _, branch := range concrete {
		node := parseSchemaNode(branch, "")
		if node == nil {
			return nil
		}
		name := schemaBranchName(branch)
		if name == "" {
			// An alternative the framework's naming cannot be reproduced for:
			// leaving the whole union alone beats reporting items whose
			// location does not match the reference's.
			return nil
		}
		union.branches = append(union.branches, schemaBranch{name: name, node: node})
	}
	raw, ok := object["discriminator"]
	if !ok {
		return union
	}
	var discriminator struct {
		PropertyName string            `json:"propertyName"`
		Mapping      map[string]string `json:"mapping"`
	}
	if err := json.Unmarshal(raw, &discriminator); err != nil || discriminator.PropertyName == "" {
		return nil
	}
	union.tag = discriminator.PropertyName
	union.tags = make(map[string]*schemaNode, len(union.branches))
	for index, branch := range concrete {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(branch, &probe); err != nil {
			return nil
		}
		ref := lastPathSegment(jsonString(probe["$ref"]))
		tag := ""
		for value, target := range discriminator.Mapping {
			if lastPathSegment(target) == ref {
				tag = value
				break
			}
		}
		if tag == "" {
			return nil
		}
		union.tagOrder = append(union.tagOrder, tag)
		union.tags[tag] = union.branches[index].node
	}
	return union
}

// schemaBranchName renders the name the framework gives a union alternative in
// an item's location: a primitive's own name, a list of the element's, a
// declared class's name, a tagged union's member list, or nothing when the
// alternative has no name this can reproduce.
func schemaBranchName(raw json.RawMessage) string {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return ""
	}
	if ref, ok := object["$ref"]; ok {
		return schemaClassName(lastPathSegment(jsonString(ref)))
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		unioned, ok := object[key]
		if !ok {
			continue
		}
		var branches []json.RawMessage
		if err := json.Unmarshal(unioned, &branches); err != nil {
			return ""
		}
		var names []string
		for _, branch := range branches {
			var probe map[string]json.RawMessage
			if err := json.Unmarshal(branch, &probe); err != nil {
				return ""
			}
			if declared, _ := schemaDeclaredType(probe); declared == "null" {
				continue
			}
			name := schemaBranchName(branch)
			if name == "" {
				return ""
			}
			names = append(names, name)
		}
		switch {
		case len(names) == 0:
			return ""
		case len(names) == 1:
			return names[0]
		case key == "oneOf" && object["discriminator"] != nil:
			return "tagged-union[" + strings.Join(names, ",") + "]"
		default:
			return "union[" + strings.Join(names, ",") + "]"
		}
	}
	declared, _ := schemaDeclaredType(object)
	switch declared {
	case "string":
		if jsonString(object["format"]) == "date-time" {
			return "datetime"
		}
		return "str"
	case "integer":
		return "int"
	case "number":
		return "float"
	case "boolean":
		return "bool"
	case "array":
		inner := "any"
		if items, ok := object["items"]; ok {
			inner = schemaBranchName(items)
		}
		if inner == "" {
			return ""
		}
		return "list[" + inner + "]"
	}
	return ""
}

// schemaClassName renders a schema name as the class name the framework uses:
// the name a dual-direction model carries is suffixed with the direction, which
// the framework's own naming does not include.
func schemaClassName(name string) string {
	for _, suffix := range []string{"-Input", "-Output"} {
		name = strings.TrimSuffix(name, suffix)
	}
	return name
}

// schemaDeclaredType reads a schema's declared type, and reports whether it
// admits `null`. The type is either one name or a list of them.
func schemaDeclaredType(object map[string]json.RawMessage) (string, bool) {
	raw, ok := object["type"]
	if !ok {
		return "", false
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return single, single == "null"
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return "", false
	}
	declared := ""
	nullable := false
	for _, name := range list {
		if name == "null" {
			nullable = true
			continue
		}
		if declared == "" {
			declared = name
		}
	}
	return declared, nullable
}

// orderedSchemaMembers reads an object's declared members, keeping the
// declaration order the framework reports its items in.
func orderedSchemaMembers(raw json.RawMessage) []schemaMember {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil
	}
	var members []schemaMember
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return members
		}
		key, ok := token.(string)
		if !ok {
			return members
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return members
		}
		members = append(members, schemaMember{name: key, node: parseSchemaNode(value, "")})
	}
	return members
}

// lastPathSegment returns the final segment of a JSON pointer.
func lastPathSegment(ref string) string {
	if index := strings.LastIndex(ref, "/"); index >= 0 {
		return ref[index+1:]
	}
	return ref
}

// jsonString reads a JSON string, or the empty string when the value is not one.
func jsonString(raw json.RawMessage) string {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

// jsonStrings reads a JSON array of strings.
func jsonStrings(raw json.RawMessage) []string {
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil
	}
	return values
}

// routeSchemaCache keeps the resolved request schema of each route pattern.
var routeSchemaCache sync.Map // string -> *schemaNode

// schemaForRequest resolves the request schema a route's body is declared
// with: the matched route's declared body when the router matched one, and
// otherwise the schema whose declaration the target model mirrors.
func schemaForRequest(r *http.Request, target any) *schemaNode {
	if r != nil {
		if node := schemaForPattern(r.Pattern); node != nil {
			return node
		}
	}
	return schemaForTarget(target)
}

// schemaForPattern resolves the request body schema of a matched route pattern
// ("POST /v1/default/banks/{bank_id}/memories/recall"), or no node when the
// pattern names no route in the contract.
func schemaForPattern(pattern string) *schemaNode {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil
	}
	if cached, ok := routeSchemaCache.Load(pattern); ok {
		node, _ := cached.(*schemaNode)
		return node
	}
	method, path, ok := strings.Cut(pattern, " ")
	if !ok {
		routeSchemaCache.Store(pattern, (*schemaNode)(nil))
		return nil
	}
	operations := requestContract().paths[path]
	if operations == nil {
		routeSchemaCache.Store(pattern, (*schemaNode)(nil))
		return nil
	}
	raw, ok := operations[strings.ToLower(method)]
	if !ok {
		routeSchemaCache.Store(pattern, (*schemaNode)(nil))
		return nil
	}
	var operation struct {
		RequestBody struct {
			Content map[string]struct {
				Schema json.RawMessage `json:"schema"`
			} `json:"content"`
		} `json:"requestBody"`
	}
	if err := json.Unmarshal(raw, &operation); err != nil {
		routeSchemaCache.Store(pattern, (*schemaNode)(nil))
		return nil
	}
	body, ok := operation.RequestBody.Content["application/json"]
	if !ok {
		routeSchemaCache.Store(pattern, (*schemaNode)(nil))
		return nil
	}
	node := parseSchemaNode(body.Schema, "")
	routeSchemaCache.Store(pattern, node)
	return node
}

// requestSchemaOverrides maps a Go request model to the contract schema that
// declares its fields, for the models whose Go name differs from the schema
// name. A model whose Go name is the schema's own name needs no entry.
var requestSchemaOverrides = map[string]string{
	"github.com/studyzy/wenyuan/internal/bankapi.BankRequest":            "CreateBankRequest",
	"github.com/studyzy/wenyuan/internal/banktemplate.Manifest":          "BankTemplateManifest",
	"github.com/studyzy/wenyuan/internal/bankapi.AliasRequest":           "CreateBankAliasRequest",
	"github.com/studyzy/wenyuan/internal/bankapi.AliasUpdateRequest":     "SetBankAliasPrimaryRequest",
	"github.com/studyzy/wenyuan/internal/bankapi.ConfigWriteRequest":     "BankConfigUpdate",
	"github.com/studyzy/wenyuan/internal/directives.CreateRequest":       "CreateDirectiveRequest",
	"github.com/studyzy/wenyuan/internal/directives.UpdateRequest":       "UpdateDirectiveRequest",
	"github.com/studyzy/wenyuan/internal/mentalmodels.CreateRequest":     "CreateMentalModelRequest",
	"github.com/studyzy/wenyuan/internal/mentalmodels.UpdateRequest":     "UpdateMentalModelRequest",
	"github.com/studyzy/wenyuan/internal/reporting.MemoryUpdateRequest":  "UpdateMemoryRequest",
	"github.com/studyzy/wenyuan/internal/reporting.dryRunExtractRequest": "DryRunExtractRequest",
}

var targetSchemaCache sync.Map // reflect.Type -> *schemaNode

// schemaForTarget resolves the contract schema a decoded model mirrors, by the
// model's own name and then by the override table. A model that resolves to
// nothing is left alone, so a route whose body this walk cannot describe keeps
// its existing behavior.
func schemaForTarget(target any) *schemaNode {
	typ := reflect.TypeOf(target)
	if typ == nil {
		return nil
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return nil
	}
	if cached, ok := targetSchemaCache.Load(typ); ok {
		node, _ := cached.(*schemaNode)
		return node
	}
	node := schemaNodeByName(typ.Name())
	if node == nil {
		if name, ok := requestSchemaOverrides[typ.PkgPath()+"."+typ.Name()]; ok {
			node = schemaNodeByName(name)
		}
	}
	targetSchemaCache.Store(typ, node)
	return node
}

// nullRejectionItems walks body against the request schema and returns the
// framework's items for every value the reference's model refuses: a `null`
// that lands on a field the declaration does not admit, a missing required
// member, a member or value that is not the object an object field extracts
// its members from, and a value outside a declared enum or literal. The items
// come in declaration order, which is the order the framework reports them.
//
// A body that does not carry a `null` token is never walked: this pass exists
// to close the `null` gap, and every other rejection already has machinery.
func nullRejectionItems(body []byte, root *schemaNode) []ValidationError {
	if root == nil || !bytes.Contains(body, nullToken) {
		return nil
	}
	value, ok := decodeWalkValue(body)
	if !ok {
		return nil
	}
	if value == nil {
		// The body parameter is required, so a `null` body is the framework's
		// missing-field item, not a value the model could extract members from.
		return []ValidationError{Missing([]string{"body"}, nil)}
	}
	var items []ValidationError
	collectRejections(value, root, []string{"body"}, &items)
	return items
}

// ValidationItemsAgainstRequest walks body against the schema the route (or,
// when no route matched, the target model) declares and returns the framework's
// items for every value the reference's model refuses. It is the shared
// decoder's walk without the `null`-only gate: a route that renders the walk's
// items in its own shape, or that reports them beside items it derives itself,
// calls this rather than DecodeBody and gets the same vocabulary.
//
// A body that is not valid JSON yields no items, so the caller's own decode
// reports the parse failure.
func ValidationItemsAgainstRequest(r *http.Request, target any, body []byte) []ValidationError {
	return validationItems(schemaForRequest(r, target), body)
}

// validationItems is ValidationItemsAgainstRequest with the request schema
// already resolved.
func validationItems(root *schemaNode, body []byte) []ValidationError {
	if root == nil {
		return nil
	}
	value, ok := decodeWalkValue(body)
	if !ok {
		return nil
	}
	if value == nil {
		// The body parameter is required, so a `null` body is the framework's
		// missing-field item rather than a value a model could extract members
		// from. A route whose declared body admits null handles that itself
		// before calling this.
		return []ValidationError{Missing([]string{"body"}, nil)}
	}
	var items []ValidationError
	collectRejections(value, root, []string{"body"}, &items)
	return items
}

// decodeWalkValue reads one JSON document keeping every object's members in
// document order, which the walk needs. It reports false for a document that
// will not parse.
func decodeWalkValue(body []byte) (any, bool) {
	// A document with trailing content (or any other syntax failure) must reach
	// the body decoder's syntax branch rather than being characterised here.
	if !json.Valid(body) {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	value, err := decodeOrderedValue(decoder)
	if err != nil {
		return nil, false
	}
	return value, true
}

// OrderItemsByDeclaration reorders items into the field-declaration order of
// the schema the route (or the target model) declares, which is the order the
// reference reports its items in. It keeps the items' relative order where the
// schema cannot place a location, so an item this walk cannot describe is left
// where the caller put it rather than being moved to the front.
func OrderItemsByDeclaration(r *http.Request, target any, items []ValidationError) {
	if len(items) < 2 {
		return
	}
	root := schemaForRequest(r, target)
	if root == nil {
		return
	}
	// The key of every item is carried through the sort with the item, so the
	// comparator always compares the two entries it is given.
	ordered := make([]orderedItem, len(items))
	for i, item := range items {
		ordered[i] = orderedItem{key: declarationKey(root, item.Loc), item: item}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return slices.Compare(ordered[i].key, ordered[j].key) < 0
	})
	for i := range ordered {
		items[i] = ordered[i].item
	}
}

// orderedItem pairs an item with the declaration key its location resolves to.
type orderedItem struct {
	key  []int
	item ValidationError
}

// declarationKey maps a JSON location (including the leading "body" segment) to
// the sequence of declaration positions the schema assigns it. A location the
// schema cannot place keeps the positions resolved so far, which orders it by
// its parent member rather than dropping it.
func declarationKey(root *schemaNode, loc []string) []int {
	node := root
	key := make([]int, 0, len(loc))
	for index, segment := range loc {
		// The first segment names the request body itself, which the schema
		// root already describes.
		if index == 0 && segment == "body" {
			continue
		}
		node = branchFor(node, segment)
		if node == nil {
			return key
		}
		switch node.kind {
		case schemaModel:
			index := node.memberIndex(segment)
			if index < 0 {
				return key
			}
			key = append(key, index)
			node = node.members[index].node
		case schemaArray:
			position, err := strconv.Atoi(segment)
			if err != nil {
				return key
			}
			key = append(key, position)
			node = node.items
		case schemaDict:
			// A free-form object's entries have no declaration order of their
			// own, so their key stays at the object's own position.
			key = append(key, 0)
			node = node.entries
		default:
			return key
		}
	}
	return key
}

// StripRejectedValues returns body with the value at each location removed, so
// a caller that renders the walk's items itself can still decode the members
// the reference's model accepted. A location that names no value — a missing
// required member, or the body itself — leaves the document unchanged, and a
// document that will not parse is returned as it was.
//
// Dropping the rejected values matters because a member that will not decode
// leaves its Go field at its zero value: a later check on that field would
// report a failure the reference never saw.
func StripRejectedValues(body []byte, locs [][]string) []byte {
	current := body
	for _, loc := range locs {
		if len(loc) < 2 {
			continue
		}
		var document any
		if err := json.Unmarshal(current, &document); err != nil {
			continue
		}
		stripped, changed := stripPath(document, loc[1:])
		if !changed {
			continue
		}
		encoded, err := json.Marshal(stripped)
		if err != nil {
			continue
		}
		current = encoded
	}
	return current
}

// stripPath removes the value at path from a decoded document, reporting
// whether anything was removed.
func stripPath(value any, path []string) (any, bool) {
	if len(path) == 0 {
		return value, false
	}
	switch container := value.(type) {
	case map[string]any:
		child, present := container[path[0]]
		if !present {
			return value, false
		}
		if len(path) == 1 {
			delete(container, path[0])
			return container, true
		}
		next, changed := stripPath(child, path[1:])
		if changed {
			container[path[0]] = next
		}
		return container, changed
	case []any:
		index, err := strconv.Atoi(path[0])
		if err != nil || index < 0 || index >= len(container) {
			return value, false
		}
		if len(path) == 1 {
			return append(container[:index:index], container[index+1:]...), true
		}
		next, changed := stripPath(container[index], path[1:])
		if changed {
			container[index] = next
		}
		return container, changed
	}
	return value, false
}

// ModelShapeMessage returns the message the framework gives a value that is not
// the object a model field extracts its members from, naming the model the
// declaration puts at that location: "Input should be a valid dictionary or
// instance of X". A location the request schema does not declare as a named
// model reports the empty string, so the caller keeps the walk's own wording.
//
// The reference serves both wordings, and which one a route sees depends on how
// its handler reads the body: a body parameter the framework validates itself
// reports the generic shape failure, while a payload the handler validates
// against a model reports the class-named one.
func ModelShapeMessage(r *http.Request, target any, loc []string) string {
	node := nodeAt(schemaForRequest(r, target), loc)
	if node == nil || node.kind != schemaModel || node.refName == "" {
		return ""
	}
	return "Input should be a valid dictionary or instance of " + schemaClassName(node.refName)
}

// nodeAt resolves the schema node a JSON location names, or nil when the
// declaration does not place a node there.
func nodeAt(root *schemaNode, loc []string) *schemaNode {
	node := root
	for index, segment := range loc {
		if index == 0 && segment == "body" {
			continue
		}
		node = branchFor(node, segment)
		if node == nil {
			return nil
		}
		switch node.kind {
		case schemaModel:
			member := node.memberIndex(segment)
			if member < 0 {
				return nil
			}
			node = node.members[member].node
		case schemaArray:
			if _, err := strconv.Atoi(segment); err != nil {
				return nil
			}
			node = node.items
		case schemaDict:
			node = node.entries
		default:
			return nil
		}
	}
	return node
}

// branchFor resolves a union to the alternative a location's next segment
// belongs to: the branch whose model declares that member, or the first
// alternative when none names it. A non-union node is returned unchanged.
func branchFor(node *schemaNode, segment string) *schemaNode {
	if node == nil || node.kind != schemaUnion || len(node.branches) == 0 {
		return node
	}
	for _, branch := range node.branches {
		if branch.node != nil && branch.node.kind == schemaModel && branch.node.memberIndex(segment) >= 0 {
			return branch.node
		}
	}
	return node.branches[0].node
}

// memberIndex returns the declaration position of a named member, or -1 when
// the model does not declare it.
func (n *schemaNode) memberIndex(name string) int {
	for i, member := range n.members {
		if member.name == name {
			return i
		}
	}
	return -1
}

// collectRejections reports the values node refuses at the JSON path loc.
func collectRejections(value any, node *schemaNode, loc []string, items *[]ValidationError) {
	if node == nil {
		return
	}
	if value == nil {
		if node.kind == schemaUnion {
			// A null a union admits is accepted; otherwise the framework
			// reports it once per alternative, because it fits none of them.
			if !node.nullable {
				collectUnion(value, node, loc, items)
			}
			return
		}
		if item, rejected := node.nullItem(loc); rejected {
			*items = append(*items, item)
		}
		return
	}
	if item, rejected := schemaKindItem(value, node, loc); rejected {
		*items = append(*items, item)
		return
	}
	switch node.kind {
	case schemaModel:
		object, ok := value.(*jsonObject)
		if !ok {
			// The declaration extracts members from an object; anything else is
			// the shape failure the framework names for a model field.
			*items = append(*items, ValidationItem("model_attributes_type", loc, modelShapeMessage, value, nil))
			return
		}
		required := make(map[string]bool, len(node.required))
		for _, name := range node.required {
			required[name] = true
		}
		for _, member := range node.members {
			memberValue, present := object.member(member.name)
			if !present {
				if required[member.name] {
					*items = append(*items, Missing(appendMember(loc, member.name), object))
				}
				continue
			}
			collectRejections(memberValue, member.node, appendMember(loc, member.name), items)
		}
	case schemaDict:
		object, ok := value.(*jsonObject)
		if !ok || node.entries == nil {
			return
		}
		for index, name := range object.keys {
			// A value schema constrains every entry, so an entry of a kind it
			// cannot take is reported at its own key, in the order the caller
			// sent the entries.
			collectRejections(object.values[index], node.entries, appendMember(loc, name), items)
		}
	case schemaArray:
		entries, ok := value.([]any)
		if !ok {
			return
		}
		for index, entry := range entries {
			collectRejections(entry, node.items, appendMember(loc, strconv.Itoa(index)), items)
		}
	case schemaUnion:
		collectUnion(value, node, loc, items)
	case schemaEnum, schemaConst:
		if text, ok := value.(string); ok {
			if !schemaValuesContain(node.values, text) {
				if item, rejected := node.valueItem(loc, value); rejected {
					*items = append(*items, item)
				}
			}
			return
		}
		if item, rejected := node.typeItem(loc); rejected {
			*items = append(*items, item)
		}
	}
}

// collectUnion reports a value that fits no alternative of a union. The
// framework validates the value against every alternative and reports each
// alternative's own item when none of them takes the value; a value one
// alternative takes is accepted.
func collectUnion(value any, node *schemaNode, loc []string, items *[]ValidationError) {
	if node.tag != "" {
		collectTagged(value, node, loc, items)
		return
	}
	var rejected [][]ValidationError
	for _, branch := range node.branches {
		var branchItems []ValidationError
		collectRejections(value, branch.node, appendMember(loc, branch.name), &branchItems)
		if len(branchItems) == 0 {
			return // an alternative takes the value
		}
		rejected = append(rejected, branchItems)
	}
	for _, branchItems := range rejected {
		*items = append(*items, branchItems...)
	}
}

// collectTagged reports a value a tagged union cannot place: a value that is
// not an object, one whose discriminator names no member, and one whose
// discriminator is missing.
func collectTagged(value any, node *schemaNode, loc []string, items *[]ValidationError) {
	object, ok := value.(*jsonObject)
	if !ok {
		*items = append(*items, ValidationItem("model_attributes_type", loc, modelShapeMessage, value, nil))
		return
	}
	raw, present := object.member(node.tag)
	if !present {
		*items = append(*items, ValidationItem("union_tag_not_found", loc,
			"Unable to extract tag using discriminator '"+node.tag+"'", value,
			unionTagNotFoundCtx{Discriminator: "'" + node.tag + "'"}))
		return
	}
	expected := schemaTagList(node.tagOrder)
	tag, isString := raw.(string)
	if !isString {
		*items = append(*items, ValidationItem("union_tag_invalid", loc,
			"Input tag 'None' found using '"+node.tag+"' does not match any of the expected tags: "+expected, value,
			unionTagInvalidCtx{Discriminator: "'" + node.tag + "'", Tag: "None", ExpectedTags: expected}))
		return
	}
	member, known := node.tags[tag]
	if !known {
		*items = append(*items, ValidationItem("union_tag_invalid", loc,
			"Input tag '"+tag+"' found using '"+node.tag+"' does not match any of the expected tags: "+expected, value,
			unionTagInvalidCtx{Discriminator: "'" + node.tag + "'", Tag: tag, ExpectedTags: expected}))
		return
	}
	collectRejections(object, member, appendMember(loc, tag), items)
}

// unionTagInvalidCtx is the framework's context for a value whose discriminator
// names no member. The keys keep the framework's own order, which a Go map
// would not.
type unionTagInvalidCtx struct {
	Discriminator string `json:"discriminator"`
	Tag           string `json:"tag"`
	ExpectedTags  string `json:"expected_tags"`
}

// unionTagNotFoundCtx is the framework's context for an object whose
// discriminator member is missing.
type unionTagNotFoundCtx struct {
	Discriminator string `json:"discriminator"`
}

// schemaTagList renders a tagged union's accepted tags the way the framework
// lists them: single-quoted, comma-separated, in declaration order.
func schemaTagList(tags []string) string {
	quoted := make([]string, len(tags))
	for index, tag := range tags {
		quoted[index] = "'" + tag + "'"
	}
	return strings.Join(quoted, ", ")
}

// schemaKindItem reports the item a declared type gives a value of a kind it
// cannot take, reading the scalars the way the framework's lax mode does: a
// string is parsed for an integer, a number or a boolean field, and a whole
// number is read as a boolean. It reports nothing when the value is of an
// acceptable kind, so the caller walks into it.
func schemaKindItem(value any, node *schemaNode, loc []string) (ValidationError, bool) {
	if node == nil {
		return ValidationError{}, false
	}
	switch node.kind {
	case schemaModel:
		if _, ok := value.(*jsonObject); !ok {
			return ValidationItem("model_attributes_type", loc, modelShapeMessage, value, nil), true
		}
	case schemaDict:
		if _, ok := value.(*jsonObject); !ok {
			return TypeMismatch("dict_type", loc, value), true
		}
	case schemaArray:
		if _, ok := value.([]any); !ok {
			return TypeMismatch("list_type", loc, value), true
		}
	case schemaString:
		if _, ok := value.(string); !ok {
			return TypeMismatch("string_type", loc, value), true
		}
	case schemaDatetime:
		if _, ok := value.(string); !ok {
			return TypeMismatch("datetime_type", loc, value), true
		}
	case schemaEnum, schemaConst:
		// A declared member list is checked against the value whatever its
		// kind: the framework names the value as an enum or literal failure
		// rather than as a string one.
		if text, ok := value.(string); !ok || !schemaValuesContain(node.values, text) {
			return node.valueItem(loc, value)
		}
	case schemaInteger:
		return schemaIntegerItem(value, loc)
	case schemaNumber:
		return schemaNumberItem(value, loc)
	case schemaBoolean:
		return schemaBooleanItem(value, loc)
	}
	return ValidationError{}, false
}

// schemaIntegerItem reads a value for an integer field: a whole number, a
// boolean, or a string the framework's integer parser accepts.
func schemaIntegerItem(value any, loc []string) (ValidationError, bool) {
	switch typed := value.(type) {
	case bool:
		return ValidationError{}, false
	case string:
		if _, ok := pythonInt(typed); ok {
			return ValidationError{}, false
		}
		return TypeMismatch("int_parsing", loc, value), true
	case json.Number:
		if _, err := typed.Int64(); err == nil {
			return ValidationError{}, false
		}
		if parsed, err := strconv.ParseFloat(typed.String(), 64); err == nil && parsed == math.Trunc(parsed) {
			return ValidationError{}, false
		}
		return TypeMismatch("int_from_float", loc, value), true
	}
	return TypeMismatch("int_type", loc, value), true
}

// schemaNumberItem reads a value for a number field: a number, a boolean, or a
// string the framework's float parser accepts.
func schemaNumberItem(value any, loc []string) (ValidationError, bool) {
	switch typed := value.(type) {
	case bool, json.Number:
		return ValidationError{}, false
	case string:
		if _, ok := pythonFloat(typed); ok {
			return ValidationError{}, false
		}
		return TypeMismatch("float_parsing", loc, value), true
	}
	return TypeMismatch("float_type", loc, value), true
}

// schemaBooleanItem reads a value for a boolean field: a boolean, one of its
// accepted words, or 0 or 1.
func schemaBooleanItem(value any, loc []string) (ValidationError, bool) {
	switch typed := value.(type) {
	case bool:
		return ValidationError{}, false
	case string:
		if _, ok := boolWord(typed); ok {
			return ValidationError{}, false
		}
		return TypeMismatch("bool_parsing", loc, value), true
	case json.Number:
		number, err := typed.Int64()
		if err != nil {
			return TypeMismatch("bool_type", loc, value), true
		}
		if number == 0 || number == 1 {
			return ValidationError{}, false
		}
		return TypeMismatch("bool_parsing", loc, value), true
	}
	return TypeMismatch("bool_type", loc, value), true
}

// jsonObject is a decoded JSON object whose members keep the order the caller
// sent them. The framework reports a free-form object's entry failures in that
// order, and encoding/json's own object type (a map) does not keep it.
type jsonObject struct {
	keys   []string
	values []any
}

// member reads one member of the object.
func (o *jsonObject) member(name string) (any, bool) {
	for index, key := range o.keys {
		if key == name {
			return o.values[index], true
		}
	}
	return nil, false
}

// MarshalJSON renders the object in document order, so an item that names the
// object as its input carries the object the caller sent.
func (o *jsonObject) MarshalJSON() ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for index, key := range o.keys {
		if index > 0 {
			buffer.WriteByte(',')
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		buffer.Write(encodedKey)
		buffer.WriteByte(':')
		encodedValue, err := json.Marshal(o.values[index])
		if err != nil {
			return nil, err
		}
		buffer.Write(encodedValue)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

// decodeOrderedValue reads one JSON value, keeping every object's members in
// document order.
func decodeOrderedValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	return decodeOrderedToken(decoder, token)
}

// decodeOrderedToken reads the value one token begins: a delimiter opens an
// object or an array, and any other token is already the value.
func decodeOrderedToken(decoder *json.Decoder, token json.Token) (any, error) {
	delimiter, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := &jsonObject{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("object member name is not a string")
			}
			value, err := decodeOrderedValue(decoder)
			if err != nil {
				return nil, err
			}
			object.keys = append(object.keys, key)
			object.values = append(object.values, value)
		}
		if _, err := decoder.Token(); err != nil { // the closing brace
			return nil, err
		}
		return object, nil
	case '[':
		entries := []any{}
		for decoder.More() {
			entry, err := decodeOrderedValue(decoder)
			if err != nil {
				return nil, err
			}
			entries = append(entries, entry)
		}
		if _, err := decoder.Token(); err != nil { // the closing bracket
			return nil, err
		}
		return entries, nil
	}
	return nil, errors.New("unexpected JSON delimiter")
}

// nullItem builds the item the framework emits for a `null` sent to a field
// the declaration does not admit, reporting whether the field rejects one.
func (n *schemaNode) nullItem(loc []string) (ValidationError, bool) {
	if n.nullable {
		return ValidationError{}, false
	}
	if n.kind == schemaModel {
		return ValidationItem("model_attributes_type", loc, modelShapeMessage, nil, nil), true
	}
	return n.typeItem(loc)
}

// valueItem builds the item for a value outside a declared enum or literal.
func (n *schemaNode) valueItem(loc []string, value any) (ValidationError, bool) {
	kind := "literal_error"
	if n.kind == schemaEnum && n.refName != "" {
		// A named enum is a declared enum class, which the framework names
		// "enum"; an inline member list is a Literal.
		kind = "enum"
	}
	return enumLike(kind, loc, value, n.values), true
}

// typeItem builds the item for a value of the wrong type, named the way the
// framework names it for the declared type. A declaration this walk cannot
// name (an untyped or free-form position) reports nothing, so an unknown
// position is left to the machinery that already handles it.
func (n *schemaNode) typeItem(loc []string) (ValidationError, bool) {
	switch n.kind {
	case schemaModel:
		return ValidationItem("model_attributes_type", loc, modelShapeMessage, nil, nil), true
	case schemaDict:
		return TypeMismatch("dict_type", loc, nil), true
	case schemaArray:
		return TypeMismatch("list_type", loc, nil), true
	case schemaString:
		return TypeMismatch("string_type", loc, nil), true
	case schemaInteger:
		return TypeMismatch("int_type", loc, nil), true
	case schemaNumber:
		return TypeMismatch("float_type", loc, nil), true
	case schemaBoolean:
		return TypeMismatch("bool_type", loc, nil), true
	case schemaDatetime:
		return TypeMismatch("datetime_type", loc, nil), true
	case schemaEnum, schemaConst:
		return n.valueItem(loc, nil)
	}
	return ValidationError{}, false
}

// schemaValuesContain reports whether a declared enum or literal admits value.
func schemaValuesContain(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// appendMember extends a JSON path by one named member or list position.
func appendMember(loc []string, member string) []string {
	extended := make([]string, len(loc), len(loc)+1)
	copy(extended, loc)
	return append(extended, member)
}
