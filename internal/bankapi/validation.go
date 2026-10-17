package bankapi

import (
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

// ValidateCreate checks the create-or-replace body.
func ValidateCreate(req BankRequest) *httpx.ValidationFailure {
	return failureOrNil(traitErrors(req))
}

// ValidatePatch checks the partial-update body.
func ValidatePatch(req BankRequest) *httpx.ValidationFailure {
	return failureOrNil(traitErrors(req))
}

// traitErrors reports out-of-range bounded disposition traits and incomplete
// disposition objects.
//
// Both forms are validated independently, as the reference's nested model and
// flat fields are: an out-of-range member of the `disposition` object is
// rejected even when a flat `disposition_<trait>` field for the same trait
// would win the storage precedence, and its item names the member's own path
// (["body","disposition","skepticism"]). The object's missing members are
// reported first, since the model declares it before the flat fields.
func traitErrors(req BankRequest) []httpx.ValidationError {
	var items []httpx.ValidationError
	items = append(items, missingTraitErrors(req)...)
	if req.Disposition != nil {
		for _, member := range []struct {
			name  string
			value *int
		}{
			{"skepticism", req.Disposition.Skepticism},
			{"literalism", req.Disposition.Literalism},
			{"empathy", req.Disposition.Empathy},
		} {
			items = append(items, traitError([]string{"body", "disposition", member.name}, member.value)...)
		}
	}
	items = append(items, traitError([]string{"body", "disposition_skepticism"}, req.DispositionSkepticism)...)
	items = append(items, traitError([]string{"body", "disposition_literalism"}, req.DispositionLiteralism)...)
	items = append(items, traitError([]string{"body", "disposition_empathy"}, req.DispositionEmpathy)...)
	return items
}

// missingTraitErrors reports the members a supplied disposition object left
// out. Each is reported at the member's own path, as the reference does.
func missingTraitErrors(req BankRequest) []httpx.ValidationError {
	if req.Disposition == nil {
		return nil
	}
	members := []struct {
		name  string
		value *int
	}{
		{"skepticism", req.Disposition.Skepticism},
		{"literalism", req.Disposition.Literalism},
		{"empathy", req.Disposition.Empathy},
	}
	var items []httpx.ValidationError
	for _, member := range members {
		if member.value != nil {
			continue
		}
		items = append(items, httpx.Missing([]string{"body", "disposition", member.name}, dispositionInput(req.Disposition)))
	}
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
