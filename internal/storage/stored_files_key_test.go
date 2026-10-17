package storage

import "testing"

// test-bank-id-from-known-layouts: both the tenant-prefixed and the plain
// bank-scoped key layouts resolve to the owning bank, so a key written under
// either layout is downloadable.
func TestBankIDFromStoredFileKeyLayouts(t *testing.T) {
	cases := []struct {
		name string
		key  string
		want string
		ok   bool
	}{
		{"banks layout", "banks/b1/exports/op/transfer.zip", "b1", true},
		{"banks layout, encoded id", "banks/a%2Fb/x.zip", "a/b", true},
		{"tenant layout", "tenants/public/banks/b1/exports/op/transfer.zip", "b1", true},
		{"tenant layout, encoded id", "tenants/acme/banks/a%2Fb/x.zip", "a/b", true},
		{"tenant layout, too short", "tenants/public/banks", "", false},
		{"tenant layout, no bank", "tenants/public/banks//x.zip", "", false},
		{"loose key", "loose/key.zip", "", false},
		{"attachment key", "banks/b1/documents/d1/attachments/sha256-h", "b1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := BankIDFromStoredFileKey(tc.key)
			if ok != tc.ok || (ok && got != tc.want) {
				t.Fatalf("BankIDFromStoredFileKey(%q) = %q, %v; want %q, %v", tc.key, got, ok, tc.want, tc.ok)
			}
		})
	}
}
