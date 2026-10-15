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
	if failure := ValidateCreate(BankRequest{Disposition: &DispositionInput{Skepticism: intPtr(99), Literalism: intPtr(3), Empathy: intPtr(3)}}); failure == nil {
		t.Fatal("an out-of-range disposition object trait must be rejected")
	}
}

// in-0003: the individual trait wins over the disposition object for validation.
func TestExplicitTraitWinsForValidation(t *testing.T) {
	if failure := ValidateCreate(BankRequest{
		Disposition:           &DispositionInput{Skepticism: intPtr(99), Literalism: intPtr(3), Empathy: intPtr(3)},
		DispositionSkepticism: intPtr(3),
	}); failure != nil {
		t.Fatalf("a valid explicit trait must shadow an invalid object trait: %+v", failure)
	}
}

// The deprecated disposition object declares all three members required, as the
// reference does, so a partial object is rejected with the members that are
// missing — and the error names them, rather than reporting an out-of-range
// zero the caller never sent.
func TestPartialDispositionObjectReportsMissingMembers(t *testing.T) {
	failure := ValidateCreate(BankRequest{
		Disposition: &DispositionInput{Skepticism: intPtr(4)},
	})
	if failure == nil {
		t.Fatal("a partial disposition object must be rejected")
	}
	if len(failure.Errors) != 2 {
		t.Fatalf("errors = %+v, want one per missing member", failure.Errors)
	}
	seen := map[string]bool{}
	for _, item := range failure.Errors {
		if item.Type != "missing" {
			t.Errorf("error type = %q, want missing", item.Type)
		}
		if len(item.Loc) != 3 || item.Loc[0] != "body" || item.Loc[1] != "disposition" {
			t.Errorf("error location = %v, want the disposition member path", item.Loc)
		}
		seen[item.Loc[2]] = true
	}
	if !seen["literalism"] || !seen["empathy"] {
		t.Fatalf("missing members reported = %v, want literalism and empathy", seen)
	}
}

// An empty disposition object is rejected for every member, so no trait is
// silently read as a zero.
func TestEmptyDispositionObjectReportsEveryMember(t *testing.T) {
	failure := ValidateCreate(BankRequest{Disposition: &DispositionInput{}})
	if failure == nil {
		t.Fatal("an empty disposition object must be rejected")
	}
	if len(failure.Errors) != 3 {
		t.Fatalf("errors = %+v, want one per member", failure.Errors)
	}
}

// A member the object did not carry is not a value: a patch built from a
// partial object must not reset the traits it did not mention.
func TestPartialDispositionObjectDoesNotPatchAbsentMembers(t *testing.T) {
	patch := BankRequest{
		Disposition: &DispositionInput{Skepticism: intPtr(4)},
	}.ToPatch()
	if patch.Disposition == nil {
		t.Fatal("the carried member must reach the patch")
	}
	traits := *patch.Disposition
	if traits[storage.DispositionKeySkepticism] != 4 {
		t.Fatalf("skepticism = %d, want 4", traits[storage.DispositionKeySkepticism])
	}
	if _, present := traits[storage.DispositionKeyLiteralism]; present {
		t.Fatalf("literalism must not be patched when the object omitted it: %v", traits)
	}
	if _, present := traits[storage.DispositionKeyEmpathy]; present {
		t.Fatalf("empathy must not be patched when the object omitted it: %v", traits)
	}
}
