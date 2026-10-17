package app

import (
	"context"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/monitoring"
)

// findValidatorPhase returns the validator-phase family for one operation and
// hook pair, and whether it was recorded.
func findValidatorPhase(operation, hook string) (monitoring.Histogram, bool) {
	for _, family := range monitoring.Workload.Histograms() {
		if family.Name != monitoring.ValidatorPhaseDurationName {
			continue
		}
		if family.Labels["operation"] == operation && family.Labels["hook"] == hook {
			return family, true
		}
	}
	return monitoring.Histogram{}, false
}

// ts-d8-validator-phase: the timing wrapper records a validator hook's duration
// under the reference's operation and hook labels — an operation gate is the
// operation's "pre" hook and a scope resolver reports under "other" — and does
// not change the validator's own decision.
func TestInstrumentedValidatorRecordsPhases(t *testing.T) {
	wrapped := instrumentOperationValidator(appValidator{deny: httpx.OperationRetain})

	decision := wrapped.ValidateOperation(context.Background(), httpx.OperationRequest{Operation: "phase_test_op", BankID: "b"})
	if !decision.Allowed {
		t.Fatalf("wrapping changed the decision: %+v", decision)
	}
	wrapped.ResolveTagScope(context.Background(), "b")

	if _, ok := findValidatorPhase("phase_test_op", "pre"); !ok {
		t.Fatal("no pre-hook validator phase recorded for the operation gate")
	}
	if _, ok := findValidatorPhase("resolve_tag_scope", "other"); !ok {
		t.Fatal("no validator phase recorded for the read-scope resolver")
	}

	// A deny is preserved through the wrapper and still times the hook.
	denied := instrumentOperationValidator(appValidator{deny: httpx.OperationRetain})
	got := denied.ValidateOperation(context.Background(), httpx.OperationRequest{Operation: httpx.OperationRetain})
	if got.Allowed || got.Status == 0 {
		t.Fatalf("wrapping lost the denial: %+v", got)
	}
}

// A validator that carries no bank rule is never timed for a bank write, so the
// wrapper does not invent a hook the deployment does not have.
func TestInstrumentedValidatorSkipsAbsentBankGate(t *testing.T) {
	wrapped := instrumentOperationValidator(appValidator{})
	gate, ok := wrapped.(httpx.BankWriteGate)
	if !ok {
		t.Fatal("the wrapper must satisfy BankWriteGate so the middleware's call is unchanged")
	}
	if decision := gate.ValidateBankWrite(context.Background(), "b", "key"); !decision.Allowed {
		t.Fatalf("a validator with no bank rule denied a write: %+v", decision)
	}
	if _, recorded := findValidatorPhase("bank_write", "pre"); recorded {
		t.Fatal("a bank-write phase was recorded for a validator that carries no bank rule")
	}
}

// A nil validator is returned untouched, so an unextended deployment records no
// validator phase at all.
func TestInstrumentOperationValidatorNil(t *testing.T) {
	if got := instrumentOperationValidator(nil); got != nil {
		t.Fatalf("instrumentOperationValidator(nil) = %v, want nil", got)
	}
}
