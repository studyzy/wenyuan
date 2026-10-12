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
	msg := "bank identifier is invalid"
	switch {
	case id == "":
		msg = "bank identifier must not be empty"
	case len(id) > storage.BankIDMaxBytes:
		msg = "bank identifier must be at most 255 bytes"
	case !utf8.ValidString(id):
		msg = "bank identifier must be valid UTF-8"
	default:
		msg = "bank identifier must not contain control characters"
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

// traitErrors reports out-of-range bounded disposition traits, taking the
// explicit individual field over the deprecated disposition object.
func traitErrors(req BankRequest) []httpx.ValidationError {
	var items []httpx.ValidationError
	items = append(items, traitError("disposition_skepticism", req.traitPtr(req.DispositionSkepticism, func(d Disposition) int { return d.Skepticism }))...)
	items = append(items, traitError("disposition_literalism", req.traitPtr(req.DispositionLiteralism, func(d Disposition) int { return d.Literalism }))...)
	items = append(items, traitError("disposition_empathy", req.traitPtr(req.DispositionEmpathy, func(d Disposition) int { return d.Empathy }))...)
	return items
}

func traitError(field string, value *int) []httpx.ValidationError {
	if value == nil {
		return nil
	}
	if *value < storage.DispositionTraitMin || *value > storage.DispositionTraitMax {
		return []httpx.ValidationError{{
			Loc:   []string{"body", field},
			Msg:   "must be an integer between 1 and 5",
			Type:  "value_error.range",
			Input: *value,
		}}
	}
	return nil
}

func failureOrNil(items []httpx.ValidationError) *httpx.ValidationFailure {
	if len(items) == 0 {
		return nil
	}
	return &httpx.ValidationFailure{Errors: items}
}
