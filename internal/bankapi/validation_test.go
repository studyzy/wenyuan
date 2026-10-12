package bankapi

import (
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

func intPtr(v int) *int { return &v }

func TestValidateCreateRejectsMalformedIdentifier(t *testing.T) {
	if _, _, ok := ValidateBankID(""); ok {
		t.Fatal("an empty identifier must be rejected")
	}
	if _, _, ok := ValidateBankID("has\ncontrol"); ok {
		t.Fatal("a control-character identifier must be rejected")
	}
	if _, _, ok := ValidateBankID("ok-id"); !ok {
		t.Fatal("a valid identifier must pass")
	}
}

func TestValidateTraitsAcceptsBounds(t *testing.T) {
	min := storage.DispositionTraitMin
	max := storage.DispositionTraitMax
	if failure := ValidateCreate(BankRequest{DispositionSkepticism: &min}); failure != nil {
		t.Fatalf("lower bound must be accepted: %+v", failure)
	}
	if failure := ValidatePatch(BankRequest{DispositionEmpathy: &max}); failure != nil {
		t.Fatalf("upper bound must be accepted: %+v", failure)
	}
}

func TestValidateTraitsRejectsOutOfBounds(t *testing.T) {
	low := storage.DispositionTraitMin - 1
	high := storage.DispositionTraitMax + 1
	if failure := ValidatePatch(BankRequest{DispositionSkepticism: &low}); failure == nil {
		t.Fatal("below the lower bound must be rejected")
	}
	if failure := ValidatePatch(BankRequest{DispositionSkepticism: &high}); failure == nil {
		t.Fatal("above the upper bound must be rejected")
	}
}

func TestValidateAllowsUnsetTraits(t *testing.T) {
	if failure := ValidateCreate(BankRequest{}); failure != nil {
		t.Fatalf("unset traits must be allowed: %+v", failure)
	}
}

// in-0003: an out-of-range trait inside the deprecated disposition object is
// still rejected when no explicit individual trait is supplied.
func TestDispositionObjectIsValidated(t *testing.T) {
	if failure := ValidateCreate(BankRequest{Disposition: &Disposition{Skepticism: 99}}); failure == nil {
		t.Fatal("an out-of-range disposition object trait must be rejected")
	}
}

// in-0003: the individual trait wins over the disposition object for validation.
func TestExplicitTraitWinsForValidation(t *testing.T) {
	if failure := ValidateCreate(BankRequest{
		Disposition:           &Disposition{Skepticism: 99, Literalism: 3, Empathy: 3},
		DispositionSkepticism: intPtr(3),
	}); failure != nil {
		t.Fatalf("a valid explicit trait must shadow an invalid object trait: %+v", failure)
	}
}
