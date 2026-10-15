package storage

import (
	"errors"
	"time"
)

// MentalModelSubtype selects which kind of mental model a record is. A pinned
// note and a structural note share one table and differ only by this value.
type MentalModelSubtype string

// The accepted mental-model subtypes.
const (
	SubtypeStructural MentalModelSubtype = "structural"
	SubtypeEmergent   MentalModelSubtype = "emergent"
	SubtypePinned     MentalModelSubtype = "pinned"
	SubtypeLearned    MentalModelSubtype = "learned"
)

// ErrMentalModelNotFound is returned when a mental model identifier is unknown
// to a bank.
var ErrMentalModelNotFound = errors.New("mental model not found")

// ErrDirectiveNotFound is returned when a directive identifier is unknown to a
// bank.
var ErrDirectiveNotFound = errors.New("directive not found")

// ErrInvalidMentalModelSubtype is returned when a subtype is outside the set the
// column accepts, so the rejection happens before the database refuses it.
var ErrInvalidMentalModelSubtype = errors.New("invalid mental model subtype")

// MentalModelSubtypes returns the accepted subtype set in stable order, so a
// caller can validate against exactly the values the column accepts.
func MentalModelSubtypes() []MentalModelSubtype {
	return []MentalModelSubtype{SubtypeStructural, SubtypeEmergent, SubtypePinned, SubtypeLearned}
}

// ValidMentalModelSubtype reports whether a subtype is one the column accepts.
func ValidMentalModelSubtype(subtype MentalModelSubtype) bool {
	for _, candidate := range MentalModelSubtypes() {
		if candidate == subtype {
			return true
		}
	}
	return false
}

// MentalModel is one named, reusable note a reflection can be built from.
//
// The identifier is unique per bank rather than globally, so two banks may hold
// a mental model with the same identifier without colliding.
type MentalModel struct {
	ID                       string
	BankID                   string
	Subtype                  MentalModelSubtype
	Name                     string
	Description              string
	SourceQuery              string
	Content                  string
	Embedding                []float32
	EntityID                 string
	Observations             map[string]any
	Links                    []string
	Tags                     []string
	MaxTokens                int
	Trigger                  map[string]any
	Structured               map[string]any
	ReflectResult            map[string]any
	LastRefreshedSourceQuery string
	LastRefreshedAt          time.Time
	// LastMemorySeenAt is the bank write watermark this model's last
	// successful refresh observed. Staleness compares against it rather than
	// against the refresh time, because a refresh that found nothing new still
	// moved its clock.
	LastMemorySeenAt *time.Time
	// LastRefreshFailedAt parks an automatic refresh after a failure, so a
	// schedule that cannot succeed stops spending a model call per tick.
	LastRefreshFailedAt *time.Time
	LastUpdated         time.Time
	CreatedAt           time.Time
}

// MentalModelHistoryEntry is one recorded refresh of a mental model. Two kinds
// share the list: a version snapshot, carrying the content and reflect response
// that were replaced, and a failure record for a refresh that refused to write.
type MentalModelHistoryEntry struct {
	// ChangedAt is when the entry was recorded.
	ChangedAt time.Time
	// PreviousContent is the content the refresh replaced. It is nil for a
	// failure record, which produced no version.
	PreviousContent *string
	// PreviousReflectResponse is the reflect response the refresh replaced.
	PreviousReflectResponse map[string]any
	// Kind marks a failure record; empty on a version snapshot.
	Kind string
	// Outcome, FailureReason and ErrorMessage describe a refused write.
	Outcome       string
	FailureReason string
	ErrorMessage  string
}

// Payload renders the entry as the JSON object the history row stores, using
// the reference's keys: previous_content and previous_reflect_response. The
// failure markers are carried alongside them for a record that produced no
// version, which the reference's reader ignores.
//
// It is exported so an archive carries exactly what a stored row holds rather
// than a second mapping that could drift from this one.
func (e MentalModelHistoryEntry) Payload() map[string]any { return historyEntryToMap(e) }

// MentalModelHistoryFailureKind is the Kind recorded for a refresh that refused
// to write, matching the reference's refresh_failed marker.
const MentalModelHistoryFailureKind = "refresh_failed"

// Directive is a standing instruction the reflection loop follows. A higher
// priority is followed before a lower one, and an inactive directive is kept
// but not applied.
type Directive struct {
	ID        string
	BankID    string
	Name      string
	Content   string
	Priority  int
	IsActive  bool
	Tags      []string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// MentalModelPatch carries a partial mental-model update. A nil field is left
// unchanged, so a caller can update one attribute without restating the rest.
type MentalModelPatch struct {
	Name        *string
	Description *string
	SourceQuery *string
	Content     *string
	// LastRefreshedSourceQuery is the query a refresh actually ran. It is what
	// tells a later delta refresh whether the topic has moved: a source query
	// that differs from the last refreshed one makes the existing content answer
	// a different question, so the refresh reads the whole scope instead of
	// merging into content that no longer matches.
	LastRefreshedSourceQuery *string
	Tags                     *[]string
	MaxTokens                *int
	Trigger                  *map[string]any
	Observations             *map[string]any
	Structured               *map[string]any
	ReflectResult            *map[string]any
}

// DirectivePatch carries a partial directive update.
type DirectivePatch struct {
	Name     *string
	Content  *string
	Priority *int
	IsActive *bool
	Tags     *[]string
}
