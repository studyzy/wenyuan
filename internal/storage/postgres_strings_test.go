package storage

import (
	"errors"
	"testing"
)

// settingRow answers one SHOW query with a fixed value.
type settingRow struct {
	value string
	err   error
}

func (r settingRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 1 {
		return errors.New("unexpected destination count")
	}
	target, ok := dest[0].(*string)
	if !ok {
		return errors.New("unexpected destination type")
	}
	*target = r.value
	return nil
}

// The literal-escaping assumption is only valid on a server that treats
// backslashes literally, so an off setting is refused rather than mis-escaped.
func TestRequireStandardConformingStrings(t *testing.T) {
	if err := requireStandardConformingStrings(settingRow{value: "on"}); err != nil {
		t.Fatalf("an on setting must be accepted: %v", err)
	}
	if err := requireStandardConformingStrings(settingRow{value: "ON"}); err != nil {
		t.Fatalf("the setting is case-insensitive: %v", err)
	}
	if err := requireStandardConformingStrings(settingRow{value: "off"}); !errors.Is(err, ErrNonStandardStringEscaping) {
		t.Fatalf("err = %v, want the refusal", err)
	}
	// A failed probe is reported rather than assumed safe.
	if err := requireStandardConformingStrings(settingRow{err: errors.New("connection closed")}); err == nil {
		t.Fatal("a failed probe must be reported")
	}
}
