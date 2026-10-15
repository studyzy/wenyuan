package audit

import (
	"context"
	"testing"
)

// The action allowlist narrows recording; an empty allowlist leaves every
// action eligible.
func TestLoggerActionAllowlist(t *testing.T) {
	all := NewLogger(nil, true, nil)
	if !all.ActionAllowed(ActionRecall) || !all.ActionAllowed(ActionRetain) {
		t.Fatalf("empty allowlist must allow every action")
	}

	narrowed := NewLogger(nil, true, []string{ActionRetain})
	if !narrowed.ActionAllowed(ActionRetain) {
		t.Errorf("listed action refused")
	}
	if narrowed.ActionAllowed(ActionRecall) {
		t.Errorf("unlisted action allowed")
	}
	if narrowed.ShouldAudit(context.Background(), ActionRecall, "b1") {
		t.Errorf("unlisted action must not record")
	}
}

// The deployment switch decides when a bank sets no override.
func TestLoggerGlobalSwitch(t *testing.T) {
	logger := NewLogger(nil, false, nil)
	logger.BankConfig = func(context.Context, string) (map[string]any, error) {
		return map[string]any{}, nil
	}
	if logger.ShouldAudit(context.Background(), ActionRetain, "b1") {
		t.Errorf("switch off must not record")
	}
	// With no bank in scope the switch alone decides.
	if logger.ShouldAudit(context.Background(), ActionRetain, "") {
		t.Errorf("switch off must not record without a bank")
	}
}

// A bank's override wins over the deployment default in both directions.
func TestLoggerBankOverrideWins(t *testing.T) {
	on := map[string]any{"audit_log_enabled": true}
	off := map[string]any{"audit_log_enabled": false}

	// Default off, bank on.
	enabled := NewLogger(nil, false, nil)
	enabled.BankConfig = func(_ context.Context, bankID string) (map[string]any, error) {
		if bankID == "on" {
			return on, nil
		}
		return map[string]any{}, nil
	}
	if !enabled.ShouldAudit(context.Background(), ActionRetain, "on") {
		t.Errorf("override on must record")
	}
	if enabled.ShouldAudit(context.Background(), ActionRetain, "other") {
		t.Errorf("default off bank must not record")
	}

	// Default on, bank off.
	disabled := NewLogger(nil, true, nil)
	disabled.BankConfig = func(_ context.Context, bankID string) (map[string]any, error) {
		if bankID == "off" {
			return off, nil
		}
		return map[string]any{}, nil
	}
	if disabled.ShouldAudit(context.Background(), ActionRetain, "off") {
		t.Errorf("override off must not record")
	}
	if !disabled.ShouldAudit(context.Background(), ActionRetain, "other") {
		t.Errorf("default on bank must record")
	}
}

// A config-resolution failure falls back to the deployment default rather than
// creating an audit gap.
func TestLoggerConfigFailureFallsBack(t *testing.T) {
	logger := NewLogger(nil, true, nil)
	logger.BankConfig = func(context.Context, string) (map[string]any, error) {
		return nil, context.DeadlineExceeded
	}
	if !logger.ShouldAudit(context.Background(), ActionRetain, "b1") {
		t.Errorf("resolution failure must fall back to the default (on)")
	}
}
