package tenant

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidateID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{name: "simple", id: "acme"},
		{name: "two chars", id: "a1"},
		{name: "underscore", id: "team_01"},
		{name: "sixty three chars", id: strings.Repeat("a", 63)},
		{name: "public stays available", id: "public"},
		{name: "empty", id: "", wantErr: true},
		{name: "one char", id: "a", wantErr: true},
		{name: "leading digit", id: "1abc", wantErr: true},
		{name: "uppercase", id: "Admin", wantErr: true},
		{name: "dash", id: "acme-corp", wantErr: true},
		{name: "reserved admin", id: "admin", wantErr: true},
		{name: "reserved pg", id: "pg_temp", wantErr: true},
		{name: "reserved information_schema", id: "information_schema", wantErr: true},
		{name: "too long", id: strings.Repeat("a", 64), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateID(tc.id)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidID) {
					t.Fatalf("ValidateID(%q) = %v, want ErrInvalidID", tc.id, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateID(%q) = %v, want nil", tc.id, err)
			}
		})
	}
}

func TestHashKey(t *testing.T) {
	got := HashKey("wyk_secret")
	want := sha256.Sum256([]byte("wyk_secret"))
	if !bytes.Equal(got, want[:]) {
		t.Fatalf("HashKey = %x, want %x", got, want)
	}
	if len(got) != sha256.Size {
		t.Fatalf("HashKey length = %d, want %d", len(got), sha256.Size)
	}
}

func TestNewKey(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	plain, key, err := NewKey(now)
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	if !strings.HasPrefix(plain, "wyk_") {
		t.Fatalf("plaintext %q does not carry the wyk_ prefix", plain)
	}
	if len(plain) != len("wyk_")+43 {
		t.Fatalf("plaintext length = %d, want 47", len(plain))
	}
	if key.ID == "" {
		t.Fatal("key id is empty")
	}
	if key.Prefix != plain[:8] {
		t.Fatalf("prefix = %q, want %q", key.Prefix, plain[:8])
	}
	if !bytes.Equal(key.Hash, HashKey(plain)) {
		t.Fatalf("stored hash does not hash the plaintext")
	}
	if !key.CreatedAt.Equal(now) {
		t.Fatalf("created_at = %v, want %v", key.CreatedAt, now)
	}

	otherPlain, other, err := NewKey(now)
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	if plain == otherPlain {
		t.Fatal("two generated keys share a plaintext")
	}
	if key.ID == other.ID {
		t.Fatal("two generated keys share an id")
	}
}

func TestCanTransition(t *testing.T) {
	tests := []struct {
		from Status
		to   Status
		want bool
	}{
		{StatusProvisioning, StatusActive, true},
		{StatusProvisioning, StatusDeleted, true},
		{StatusActive, StatusSuspended, true},
		{StatusSuspended, StatusActive, true},
		{StatusActive, StatusDeleted, true},
		{StatusSuspended, StatusDeleted, true},
		{StatusActive, StatusProvisioning, false},
		{StatusProvisioning, StatusSuspended, false},
		{StatusDeleted, StatusActive, false},
		{StatusDeleted, StatusDeleted, false},
		{StatusActive, StatusActive, false},
		{Status("bogus"), StatusActive, false},
	}
	for _, tc := range tests {
		if got := CanTransition(tc.from, tc.to); got != tc.want {
			t.Errorf("CanTransition(%q, %q) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestTransition(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	record := Record{ID: "acme", Status: StatusProvisioning, UpdatedAt: now.Add(-time.Hour)}

	active, err := Transition(record, StatusActive, now)
	if err != nil {
		t.Fatalf("Transition to active: %v", err)
	}
	if active.Status != StatusActive {
		t.Fatalf("status = %q, want active", active.Status)
	}
	if !active.UpdatedAt.Equal(now) {
		t.Fatalf("updated_at = %v, want %v", active.UpdatedAt, now)
	}
	if active.DeletedAt != nil {
		t.Fatal("active record carries deleted_at")
	}

	if _, err := Transition(active, StatusActive, now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Transition active -> active = %v, want ErrInvalidTransition", err)
	}

	deleted, err := Transition(active, StatusDeleted, now)
	if err != nil {
		t.Fatalf("Transition to deleted: %v", err)
	}
	if deleted.DeletedAt == nil || !deleted.DeletedAt.Equal(now) {
		t.Fatalf("deleted_at = %v, want %v", deleted.DeletedAt, now)
	}
	if _, err := Transition(deleted, StatusActive, now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Transition deleted -> active = %v, want ErrInvalidTransition", err)
	}
}
