package tenant

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ErrInvalidID reports a tenant id that cannot become a schema name.
var ErrInvalidID = errors.New("invalid tenant id")

// ErrNotFound reports a tenant or key the registry does not hold.
var ErrNotFound = errors.New("tenant not found")

// ErrExists reports a tenant or key id already present in the registry.
var ErrExists = errors.New("tenant already exists")

// KindValue returns the stored spelling of the record's kind, defaulting an
// empty kind to personal.
func (r Record) KindValue() Kind {
	if r.Kind == "" {
		return KindPersonal
	}
	return r.Kind
}

// IsTeam reports whether the record is a team tenant.
func (r Record) IsTeam() bool { return r.KindValue() == KindTeam }

// ErrInvalidTransition reports a status change the state machine does not allow.
var ErrInvalidTransition = errors.New("invalid tenant status transition")

// ErrKindMismatch reports a team operation on a tenant that is not a team, or
// the reverse. A tenant's kind is fixed at creation: an existing personal
// tenant is never promoted to a team.
var ErrKindMismatch = errors.New("tenant kind does not match the operation")

// ErrInvalidProject reports a project id, project name or bank id that cannot
// be stored.
var ErrInvalidProject = errors.New("invalid project")

// Status is a tenant's lifecycle state.
type Status string

const (
	// StatusProvisioning is a tenant whose schema is still being created.
	StatusProvisioning Status = "provisioning"
	// StatusActive serves data-plane requests and background work.
	StatusActive Status = "active"
	// StatusSuspended keeps the schema but rejects every credential.
	StatusSuspended Status = "suspended"
	// StatusDeleted keeps the schema until it is purged; every key is rejected.
	StatusDeleted Status = "deleted"
)

// Kind distinguishes a person's own tenant from a team's. A team is a tenant
// with its own schema; the kind is what makes it one without a second table.
type Kind string

const (
	// KindPersonal is a person's tenant. It is the default, so an existing row
	// needs no backfill.
	KindPersonal Kind = "personal"
	// KindTeam is a team's tenant.
	KindTeam Kind = "team"
)

// Record is one row of the tenant registry.
type Record struct {
	ID          string
	DisplayName string
	Status      Status
	// Kind is personal or team. An empty value reads as personal, which is what
	// a record written before the column existed means.
	Kind      Kind
	Config    map[string]any
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// Key is one tenant credential. Only the hash is stored; the plaintext exists
// once, at creation time.
type Key struct {
	ID         string
	TenantID   string
	Hash       []byte
	Prefix     string
	Label      string
	Ephemeral  bool
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
	CreatedAt  time.Time
}

// tenantIDPattern is the slug shape a tenant id must have before it can be
// used as a PostgreSQL identifier. The upper bound is the identifier length.
var tenantIDPattern = regexp.MustCompile("^[a-z][a-z0-9_]{1,62}$")

// ValidateID reports whether id can name a tenant and its schema.
//
// public is deliberately not reserved: an existing single-tenant deployment
// lives in public, and adoption needs that name. Reserved names are the ones
// that would collide with the registry schema, PostgreSQL's own namespaces, or
// the system catalog.
func ValidateID(id string) error {
	if !tenantIDPattern.MatchString(id) {
		return fmt.Errorf("%w: %q must match %s", ErrInvalidID, id, tenantIDPattern)
	}
	if id == "admin" || id == "information_schema" || strings.HasPrefix(id, "pg_") {
		return fmt.Errorf("%w: %q is reserved", ErrInvalidID, id)
	}
	return nil
}

// HashKey returns the sha256 of a key's plaintext. Keys are high-entropy
// random values, so a plain digest is the right storage form; a password KDF
// would add cost without adding security.
func HashKey(plaintext string) []byte {
	sum := sha256.Sum256([]byte(plaintext))
	return sum[:]
}

// NewKey mints a credential: a wyk_-prefixed random plaintext, its stored
// record, and the identifier the registry files it under. The caller owns the
// plaintext and must return it exactly once.
func NewKey(now time.Time) (string, Key, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", Key{}, fmt.Errorf("generate key: %w", err)
	}
	plaintext := "wyk_" + base64.RawURLEncoding.EncodeToString(raw)
	id, err := newKeyID()
	if err != nil {
		return "", Key{}, err
	}
	return plaintext, Key{
		ID:        id,
		Hash:      HashKey(plaintext),
		Prefix:    plaintext[:8],
		CreatedAt: now,
	}, nil
}

// newKeyID returns a random UUID-shaped identifier, so the key table's uuid
// column accepts it without a driver extension.
func newKeyID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate key id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// CanTransition reports whether the status machine allows from -> to. A
// transition to the current status is not allowed; an idempotent PATCH is the
// caller's job to recognise before asking for a transition.
func CanTransition(from, to Status) bool {
	switch from {
	case StatusProvisioning:
		return to == StatusActive || to == StatusDeleted
	case StatusActive:
		return to == StatusSuspended || to == StatusDeleted
	case StatusSuspended:
		return to == StatusActive || to == StatusDeleted
	}
	return false
}

// Transition applies a status change, stamping updated_at and, on deletion,
// deleted_at. It returns the record untouched when the change is not allowed.
func Transition(record Record, to Status, now time.Time) (Record, error) {
	if !CanTransition(record.Status, to) {
		return record, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, record.Status, to)
	}
	record.Status = to
	record.UpdatedAt = now
	if to == StatusDeleted {
		deletedAt := now
		record.DeletedAt = &deletedAt
	}
	return record, nil
}
