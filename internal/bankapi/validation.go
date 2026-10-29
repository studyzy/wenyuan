package bankapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// ValidateBankID reports the identifier admission failures: empty, over the
// byte budget, invalid UTF-8, containing control characters, or prefixed with
// the reserved '_'. It returns the client-error status and message for the
// first violated rule.
func ValidateBankID(id string) (int, string, bool) {
	if storage.ValidBankID(id) {
		return 0, "", true
	}
	var msg string
	switch {
	case id == "":
		msg = "Bank id must not be empty"
	case len(id) > storage.BankIDMaxBytes:
		msg = "Bank id is too long: at most 192 bytes of UTF-8 are allowed"
	case !utf8.ValidString(id):
		msg = "Bank id must be valid UTF-8"
	case hasControlCharacter(id):
		msg = "Bank id must not contain control characters"
	case strings.HasPrefix(id, "_"):
		msg = "Bank id must not start with '_': that prefix is reserved"
	default:
		msg = "Bank id must not contain control characters"
	}
	return http.StatusBadRequest, msg, false
}

// hasControlCharacter reports whether the identifier carries a Unicode `Cc`
// character, the rule the storage layer refuses (bank_utils.py checks it before
// the reserved prefix, so the messages report in that order).
func hasControlCharacter(id string) bool {
	for _, r := range id {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return true
		}
	}
	return false
}

// writeIdentifierError emits the client-error envelope for a rejected bank
// identifier or alias.
func writeIdentifierError(w http.ResponseWriter, r *http.Request, id string) bool {
	status, msg, ok := ValidateBankID(id)
	if ok {
		return false
	}
	httpx.WriteErrorFor(w, r, httpx.NewHTTPError(status, msg))
	return true
}

// carriesValidationItems reports whether a decode failure already names the
// body's own validation items, which the handler completes into its own list
// rather than answering on its own.
func carriesValidationItems(err error) bool {
	var pending *BodyNeedsValidation
	return errors.As(err, &pending)
}

// ValidateCreate checks the create-or-replace body. fieldItems are the body's
// own type items, reported at the fields they name.
func ValidateCreate(req BankRequest, fieldItems []httpx.ValidationError) *httpx.ValidationFailure {
	return failureOrNil(orderedValidationItems(req, fieldItems))
}

// ValidatePatch checks the partial-update body, the same way.
func ValidatePatch(req BankRequest, fieldItems []httpx.ValidationError) *httpx.ValidationFailure {
	return failureOrNil(orderedValidationItems(req, fieldItems))
}

// orderedValidationItems merges the body's type items with the rules this
// package applies to the decoded values, in the model's field order — which is
// the order the reference reports them in, because it validates every field of
// its model (types and bounds together) before it runs a field validator.
//
// The rules are collected per declared field: the disposition object carries
// its members' rules, each flat trait carries its own, and a type failure for
// the member that field decodes from sits ahead of that field's rules. A type
// failure for the field as a whole — a disposition given as a string, say —
// replaces that field's rules instead of preceding them: the field never
// decoded, so its bounds were never checked.
//
// The disposition object's own type items and its members' rule items are
// merged positionally inside that field, because the reference validates the
// nested model in one pass: an out-of-range literalism is reported beside an
// unparsable skepticism and a missing empathy, in the members' declaration
// order.
func orderedValidationItems(req BankRequest, fieldItems []httpx.ValidationError) []httpx.ValidationError {
	rules := map[string][]httpx.ValidationError{}
	for _, item := range traitErrors(req) {
		field := "disposition"
		if len(item.Loc) == 2 {
			field = item.Loc[1]
		}
		rules[field] = append(rules[field], item)
	}
	types := map[string][]httpx.ValidationError{}
	for _, item := range fieldItems {
		if len(item.Loc) < 2 {
			continue
		}
		types[item.Loc[1]] = append(types[item.Loc[1]], item)
	}
	var items []httpx.ValidationError
	for _, field := range bankRequestFields() {
		typed := types[field]
		if field == dispositionField {
			items = append(items, mergeDispositionItems(typed, rules[field])...)
			continue
		}
		if len(typed) > 0 {
			items = append(items, typed...)
			continue
		}
		items = append(items, rules[field]...)
	}
	// The nested model's second validation round runs after the outer model's
	// own pass, so the members it finds missing are reported last.
	return append(items, dispositionMissingMembers(req, items)...)
}

// dispositionField is the request field that names a nested model with its own
// declaration order.
const dispositionField = "disposition"

// mergeDispositionItems puts the disposition object's own items back in the
// position the nested model's single validation pass gives them.
//
// A type item for the whole object (["body","disposition"]) means the field
// never decoded, so it stands alone. An item below it (…,"skepticism"]) is a
// member the nested model rejected; the members are walked in their
// declaration order, each contributing its type item and then its rule item.
// Members that the reference's model does not declare are left where the
// decoder put them, after the declared walk, because their position inside the
// nested model is not something this package can know.
func mergeDispositionItems(typed, rules []httpx.ValidationError) []httpx.ValidationError {
	// The nested model's pass runs member by member, so an item is reported
	// where its member stands whether it came from the type check or from a
	// bound. An item that names the object rather than a member (the framework
	// adding a missing member while a sibling failed to decode) is placed by
	// the member it names.
	ruleFor := map[string][]httpx.ValidationError{}
	var unmatched []httpx.ValidationError
	for _, item := range rules {
		if len(item.Loc) == 3 && dispositionMemberDeclared(item.Loc[2]) {
			ruleFor[item.Loc[2]] = append(ruleFor[item.Loc[2]], item)
			continue
		}
		unmatched = append(unmatched, item)
	}
	members := map[string][]httpx.ValidationError{}
	var trailing []httpx.ValidationError
	for _, item := range typed {
		if len(item.Loc) < 3 {
			// A whole-object item: nothing decoded, so no member rule applies
			// beyond the members the framework already reported as missing.
			return append(typed, unmatched...)
		}
		name := item.Loc[2]
		if !dispositionMemberDeclared(name) {
			trailing = append(trailing, item)
			continue
		}
		members[name] = append(members[name], item)
	}
	var items []httpx.ValidationError
	for _, member := range dispositionMembers {
		items = append(items, ruleFor[member]...)
		items = append(items, members[member]...)
	}
	items = append(items, trailing...)
	return append(items, unmatched...)
}

// dispositionMembers is DispositionInput's declaration order.
var dispositionMembers = []string{"skepticism", "literalism", "empathy"}

func dispositionMemberDeclared(name string) bool {
	for _, member := range dispositionMembers {
		if member == name {
			return true
		}
	}
	return false
}

// traitErrors reports out-of-range bounded disposition traits and incomplete
// disposition objects.
//
// Both forms are validated independently, as the reference's nested model and
// flat fields are: an out-of-range member of the `disposition` object is
// rejected even when a flat `disposition_<trait>` field for the same trait
// would win the storage precedence, and its item names the member's own path
// (["body","disposition","skepticism"]). The object is declared before the flat
// fields, and its members are reported in their own declaration order, each at
// the position it holds — the order the reference's single validation pass
// produces.
func traitErrors(req BankRequest) []httpx.ValidationError {
	var items []httpx.ValidationError
	if req.Disposition != nil {
		for _, member := range dispositionTraitMembers(req.Disposition) {
			loc := []string{"body", "disposition", member.name}
			items = append(items, traitError(loc, member.value)...)
		}
	}
	items = append(items, traitError([]string{"body", "disposition_skepticism"}, req.DispositionSkepticism)...)
	items = append(items, traitError([]string{"body", "disposition_literalism"}, req.DispositionLiteralism)...)
	items = append(items, traitError([]string{"body", "disposition_empathy"}, req.DispositionEmpathy)...)
	return items
}

// dispositionTraitMembers lists the object's three traits in declaration order
// with the values that decoded.
func dispositionTraitMembers(d *DispositionInput) []struct {
	name  string
	value *int
} {
	return []struct {
		name  string
		value *int
	}{
		{"skepticism", d.Skepticism},
		{"literalism", d.Literalism},
		{"empathy", d.Empathy},
	}
}

// dispositionMissingMembers reports the members of the disposition object that
// the caller did not supply. The framework validates the nested model in two
// rounds and reports the second round last, so these items come after every
// member item rather than inside the member order; the object's own value is
// echoed, as the caller wrote it.
func dispositionMissingMembers(req BankRequest, reported []httpx.ValidationError) []httpx.ValidationError {
	if req.Disposition == nil {
		return nil
	}
	raw := req.DispositionRaw
	var items []httpx.ValidationError
	for _, member := range dispositionTraitMembers(req.Disposition) {
		if member.value != nil || req.Disposition.Supplied[member.name] {
			continue
		}
		loc := []string{"body", "disposition", member.name}
		echo := req.Disposition.renderedMembers(raw)
		item := httpx.Missing(loc, echo)
		// A member that did decode is already reported by the declaration walk,
		// with the object the walk echoes. This pass reports what the walk
		// cannot see: a member the body named whose value never decoded.
		if !reportedAlready(reported, member.name, echo) {
			items = append(items, item)
		}
	}
	return items
}

// reportedAlready reports whether an item at the member's path already names
// the same object, which is the framework's own missing-member item for it.
func reportedAlready(items []httpx.ValidationError, member string, echo map[string]any) bool {
	for _, existing := range items {
		if len(existing.Loc) == 3 && existing.Loc[2] == member && sameInput(existing.Input, echo) {
			return true
		}
	}
	return false
}

// sameInput reports whether two item inputs name the same members with the
// same values, so a member already reported by one pass is not reported again
// by the other. One side is the generic value the framework produced and the
// other is the object this service renders, so the comparison is by member and
// by value rather than by Go type or identity.
func sameInput(framework, rendered any) bool {
	renderedMap, ok := rendered.(map[string]any)
	if !ok {
		return false
	}
	for key, renderedValue := range renderedMap {
		frameworkValue, present := memberOf(framework, key)
		if !present || !sameScalar(frameworkValue, renderedValue) {
			return false
		}
	}
	return countMembers(framework) == len(renderedMap)
}

// memberOf reads one member out of a value an item echoes as its input: the
// framework echoes an object it captured, this service echoes a map.
func memberOf(value any, name string) (any, bool) {
	if asMap, ok := objectMembers(value); ok {
		member, present := asMap[name]
		return member, present
	}
	fields, ok := structFields(value)
	if !ok {
		return nil, false
	}
	field := fields.field
	structType := fields.structType
	for i := range structType.NumField() {
		tag, _, _ := strings.Cut(structType.Field(i).Tag.Get("json"), ",")
		if tag != name {
			continue
		}
		member := field.Field(i)
		if member.Kind() == reflect.Pointer {
			if member.IsNil() {
				return nil, false
			}
			return member.Elem().Interface(), true
		}
		return member.Interface(), true
	}
	return nil, false
}

// countMembers reports how many members a model instance or an object carries.
func countMembers(value any) int {
	if asMap, ok := objectMembers(value); ok {
		return len(asMap)
	}
	fields, ok := structFields(value)
	if !ok {
		return 0
	}
	count := 0
	for i := range fields.structType.NumField() {
		if fields.structType.Field(i).PkgPath != "" || fields.field.Field(i).IsZero() {
			continue
		}
		count++
	}
	return count
}

// objectMembers reads a value an item echoes as an object's members: a map, or
// a value that renders as a JSON object (the request layer keeps an object it
// could not bind into a model as the text it read, which is what the framework
// echoes for a member that failed).
func objectMembers(value any) (map[string]any, bool) {
	if asMap, ok := value.(map[string]any); ok {
		return asMap, true
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 || encoded[0] != '{' {
		return nil, false
	}
	members := map[string]any{}
	if err := json.Unmarshal(encoded, &members); err != nil || members == nil {
		return nil, false
	}
	for name, member := range members {
		if number, isNumber := member.(float64); isNumber {
			members[name] = number
		}
	}
	return members, true
}

// structFields unwraps a pointer to the struct a model instance is stored in.
// It is the shape this service's own decoders produce (the framework echoes a
// decoded model, whose object-typed members are pointers).
func structFields(value any) (structValue, bool) {
	structValue := structValue{}
	field := reflect.ValueOf(value)
	for field.Kind() == reflect.Pointer {
		if field.IsNil() {
			return structValue, false
		}
		field = field.Elem()
	}
	if field.Kind() != reflect.Struct {
		return structValue, false
	}
	structValue = structValueBetween(field)
	return structValue, true
}

// structValue is a struct with the object it was stored in, so a nested member
// is read the same way a top-level one is.
type structValue struct {
	field      reflect.Value
	structType reflect.Type
}

func structValueBetween(field reflect.Value) structValue {
	return structValue{field: field, structType: field.Type()}
}

// floatMember reads a number a model member can hold; a bool is not one.
func floatMember(value any) (float64, bool) {
	if _, isBool := value.(bool); isBool {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	}
	return 0, false
}

// sameScalar compares two values a model member can hold. Numbers are compared
// through float64 so a value echoed as a JSON number and the same value
// decoded into an int are the same input.
func sameScalar(left, right any) bool {
	if leftNumber, ok := floatMember(left); ok {
		rightNumber, rightOK := floatMember(right)
		return rightOK && leftNumber == rightNumber
	}
	return fmt.Sprint(left) == fmt.Sprint(right)
}

func traitError(loc []string, value *int) []httpx.ValidationError {
	if value == nil {
		return nil
	}
	if *value < storage.DispositionTraitMin {
		return []httpx.ValidationError{httpx.GreaterThanEqual(loc, *value, storage.DispositionTraitMin)}
	}
	if *value > storage.DispositionTraitMax {
		return []httpx.ValidationError{httpx.LessThanEqual(loc, *value, storage.DispositionTraitMax)}
	}
	return nil
}

func failureOrNil(items []httpx.ValidationError) *httpx.ValidationFailure {
	if len(items) == 0 {
		return nil
	}
	return &httpx.ValidationFailure{Errors: items}
}
