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
	if failure := ValidateCreate(BankRequest{DispositionSkepticism: &min}, nil); failure != nil {
		t.Fatalf("lower bound must be accepted: %+v", failure)
	}
	if failure := ValidatePatch(BankRequest{DispositionEmpathy: &max}, nil); failure != nil {
		t.Fatalf("upper bound must be accepted: %+v", failure)
	}
}

func TestValidateTraitsRejectsOutOfBounds(t *testing.T) {
	low := storage.DispositionTraitMin - 1
	high := storage.DispositionTraitMax + 1
	if failure := ValidatePatch(BankRequest{DispositionSkepticism: &low}, nil); failure == nil {
		t.Fatal("below the lower bound must be rejected")
	}
	if failure := ValidatePatch(BankRequest{DispositionSkepticism: &high}, nil); failure == nil {
		t.Fatal("above the upper bound must be rejected")
	}
}

func TestValidateAllowsUnsetTraits(t *testing.T) {
	if failure := ValidateCreate(BankRequest{}, nil); failure != nil {
		t.Fatalf("unset traits must be allowed: %+v", failure)
	}
}

// in-0003: an out-of-range trait inside the deprecated disposition object is
// still rejected when no explicit individual trait is supplied.
func TestDispositionObjectIsValidated(t *testing.T) {
	if failure := ValidateCreate(BankRequest{Disposition: &DispositionInput{Skepticism: intPtr(99), Literalism: intPtr(3), Empathy: intPtr(3)}}, nil); failure == nil {
		t.Fatal("an out-of-range disposition object trait must be rejected")
	}
}

// The disposition object and the flat fields are validated independently, as
// the reference validates the nested model and the flat fields: an
// out-of-range object member is rejected even when a valid flat field for the
// same trait would win the storage precedence, and its item names the member's
// own nested path.
func TestDispositionObjectIsValidatedIndependently(t *testing.T) {
	failure := ValidateCreate(BankRequest{
		Disposition:           &DispositionInput{Skepticism: intPtr(99), Literalism: intPtr(3), Empathy: intPtr(3)},
		DispositionSkepticism: intPtr(3),
	}, nil)
	if failure == nil || len(failure.Errors) != 1 {
		t.Fatalf("an out-of-range object trait must be rejected: %+v", failure)
	}
	loc := failure.Errors[0].Loc
	if len(loc) != 3 || loc[0] != "body" || loc[1] != "disposition" || loc[2] != "skepticism" {
		t.Fatalf("item location = %v, want the nested member path", loc)
	}
}

// The deprecated disposition object declares all three members required, as the
// reference does, so a partial object is rejected with the members that are
// missing — and the error names them, rather than reporting an out-of-range
// zero the caller never sent.
func TestPartialDispositionObjectReportsMissingMembers(t *testing.T) {
	failure := ValidateCreate(BankRequest{
		Disposition: &DispositionInput{Skepticism: intPtr(4)},
	}, nil)
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
	failure := ValidateCreate(BankRequest{Disposition: &DispositionInput{}}, nil)
	if failure == nil {
		t.Fatal("an empty disposition object must be rejected")
	}
	if len(failure.Errors) != 3 {
		t.Fatalf("errors = %+v, want one per member", failure.Errors)
	}
}

// Inside the disposition object the items follow the member declaration order,
// each at the position its member holds: an out-of-range member is reported
// where it stands, not after the missing ones.
func TestDispositionObjectItemsFollowMemberOrder(t *testing.T) {
	failure := ValidateCreate(BankRequest{
		Disposition: &DispositionInput{Skepticism: intPtr(9)},
	}, nil)
	if failure == nil || len(failure.Errors) != 3 {
		t.Fatalf("errors = %+v, want one per member", failure)
	}
	want := []struct{ kind, member string }{
		{"less_than_equal", "skepticism"},
		{"missing", "literalism"},
		{"missing", "empathy"},
	}
	for i, expected := range want {
		item := failure.Errors[i]
		if item.Type != expected.kind || item.Loc[2] != expected.member {
			t.Fatalf("item %d = %s %v, want %s %s", i, item.Type, item.Loc, expected.kind, expected.member)
		}
	}
}

// A member the object did not carry is not a value: the overrides built from a
// partial object must name only the traits the request carried, and the legacy
// disposition and mission columns must never be written.
func TestPartialDispositionObjectDoesNotPatchAbsentMembers(t *testing.T) {
	req := BankRequest{Disposition: &DispositionInput{Skepticism: intPtr(4)}}
	patch := req.ToPatch()
	if patch.Disposition != nil || patch.Mission != nil {
		t.Fatalf("the legacy columns must not be written: %+v", patch)
	}
	updates := req.configUpdates()
	if updates["disposition_skepticism"] != 4 {
		t.Fatalf("skepticism = %v, want 4", updates["disposition_skepticism"])
	}
	if _, present := updates["disposition_literalism"]; present {
		t.Fatalf("literalism must not be overridden when the object omitted it: %v", updates)
	}
	if _, present := updates["disposition_empathy"]; present {
		t.Fatalf("empathy must not be overridden when the object omitted it: %v", updates)
	}
}
