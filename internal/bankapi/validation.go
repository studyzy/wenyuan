package bankapi

import (
	"errors"
	"net/http"
	"unicode/utf8"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// ValidateBankID reports the identifier admission failures: empty, over the
// byte budget, invalid UTF-8 or containing control characters. It returns the
// client-error status and message for the first violated rule.
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
	default:
		msg = "Bank id must not contain control characters"
	}
	return http.StatusBadRequest, msg, false
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
// the member that field decodes from sits ahead of that field's rules. A rule
// item whose field carries a type item is dropped, because the model never
// reached that field: its value was never decoded, so no bound can be checked
// against it.
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
		if typed := types[field]; len(typed) > 0 {
			items = append(items, typed...)
			continue
		}
		items = append(items, rules[field]...)
	}
	return items
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
		for _, member := range []struct {
			name  string
			value *int
		}{
			{"skepticism", req.Disposition.Skepticism},
			{"literalism", req.Disposition.Literalism},
			{"empathy", req.Disposition.Empathy},
		} {
			loc := []string{"body", "disposition", member.name}
			if member.value == nil {
				items = append(items, httpx.Missing(loc, dispositionInput(req.Disposition)))
				continue
			}
			items = append(items, traitError(loc, member.value)...)
		}
	}
	items = append(items, traitError([]string{"body", "disposition_skepticism"}, req.DispositionSkepticism)...)
	items = append(items, traitError([]string{"body", "disposition_literalism"}, req.DispositionLiteralism)...)
	items = append(items, traitError([]string{"body", "disposition_empathy"}, req.DispositionEmpathy)...)
	return items
}

// dispositionInput renders the disposition object as the caller supplied it, so
// a missing-member item names the object it came from the way the framework
// does.
func dispositionInput(d *DispositionInput) map[string]any {
	out := map[string]any{}
	if d == nil {
		return out
	}
	if d.Skepticism != nil {
		out["skepticism"] = *d.Skepticism
	}
	if d.Literalism != nil {
		out["literalism"] = *d.Literalism
	}
	if d.Empathy != nil {
		out["empathy"] = *d.Empathy
	}
	return out
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
