package hindsight

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/studyzy/wenyuan/internal/hindsightarchive"
	"github.com/studyzy/wenyuan/internal/storage"
)

// Mode selects how a migration treats a target bank that already has rows.
type Mode string

const (
	// Replace deletes the target bank's rows first, so a re-run is idempotent.
	Replace Mode = "replace"
	// Append keeps existing rows and adds the archive's rows alongside them.
	Append Mode = "append"
)

// Options controls a migration run.
type Options struct {
	Mode Mode
	// DryRun parses and maps the archive and reports counts without writing.
	DryRun bool
	// Now is injectable for deterministic tests.
	Now func() time.Time
}

// Run migrates the archive into the store and returns a report.
func Run(ctx context.Context, store storage.Store, mapping Mapping, opts Options) (Report, error) {
	if mapping.Archive == nil {
		return Report{}, errors.New("migration requires a parsed archive")
	}
	bankID := mapping.targetBankID()
	if bankID == "" {
		return Report{}, errors.New("migration requires a bank id (archive source or explicit target)")
	}
	mode := opts.Mode
	if mode == "" {
		mode = Replace
	}

	report := Report{BankID: bankID, Unmapped: unmappedFields()}
	for _, doc := range mapping.Archive.Documents {
		report.Documents++
		report.Chunks += len(doc.Chunks)
		report.Facts += len(doc.Facts)
		report.Entities += countEntities(doc)
		report.Links += countLinks(doc)
	}
	if opts.DryRun {
		return report, nil
	}

	if mode == Replace {
		if _, err := store.DeleteBank(ctx, bankID); err != nil {
			return Report{}, fmt.Errorf("clear target bank %s: %w", bankID, err)
		}
	}
	if err := ensureBank(ctx, store, bankID, opts); err != nil {
		return Report{}, err
	}
	for _, doc := range mapping.Archive.Documents {
		if err := writeDocument(ctx, store, bankID, doc); err != nil {
			return Report{}, err
		}
	}
	return report, nil
}

func ensureBank(ctx context.Context, store storage.Store, bankID string, opts Options) error {
	if _, err := store.GetBank(ctx, bankID); err == nil {
		return nil
	}
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now()
	}
	bank := storage.Bank{
		ID:                    bankID,
		Name:                  bankID,
		DispositionSkepticism: 3,
		DispositionLiteralism: 3,
		DispositionEmpathy:    3,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if _, err := store.CreateBank(ctx, bank); err != nil {
		return fmt.Errorf("create target bank %s: %w", bankID, err)
	}
	return nil
}

func writeDocument(ctx context.Context, store storage.Store, bankID string, doc hindsightarchive.Document) error {
	created := time.Now().UTC()
	if doc.CreatedAt != nil {
		created = *doc.CreatedAt
	}
	document := storage.Document{
		ID:           doc.ID,
		BankID:       bankID,
		OriginalText: doc.OriginalText,
		RetainParams: doc.RetainParams,
		CreatedAt:    created,
	}
	if _, err := store.PutDocument(ctx, bankID, doc.ID, document); err != nil {
		return fmt.Errorf("write document %s: %w", doc.ID, err)
	}

	for index, fact := range doc.Facts {
		unit := unitFromFact(bankID, doc.ID, index, fact)
		if _, err := store.UpsertUnit(ctx, unit); err != nil {
			return fmt.Errorf("write fact %d of %s: %w", index, doc.ID, err)
		}
		for _, name := range fact.Entities {
			entity, err := store.ResolveEntity(ctx, bankID, name, storage.EntityKindRegular, true)
			if err != nil {
				return fmt.Errorf("resolve entity %q: %w", name, err)
			}
			if err := store.UpsertUnitEntity(ctx, unit.ID, entity.ID); err != nil {
				return fmt.Errorf("link unit %s to entity %s: %w", unit.ID, entity.ID, err)
			}
		}
		for _, relation := range fact.CausalRelations {
			if relation.TargetFactIndex < 0 || relation.TargetFactIndex >= len(doc.Facts) {
				continue
			}
			link := storage.MemoryLink{
				ID:       linkID(bankID, unit.ID, factUnitID(bankID, doc.ID, relation.TargetFactIndex), relation.RelationType),
				BankID:   bankID,
				FromID:   unit.ID,
				ToID:     factUnitID(bankID, doc.ID, relation.TargetFactIndex),
				LinkType: storage.MemoryLinkType(relation.RelationType),
			}
			if err := store.UpsertLink(ctx, link); err != nil {
				return fmt.Errorf("write causal link from %s: %w", unit.ID, err)
			}
		}
	}
	return nil
}

func countEntities(doc hindsightarchive.Document) int {
	seen := map[string]bool{}
	for _, fact := range doc.Facts {
		for _, name := range fact.Entities {
			seen[name] = true
		}
	}
	return len(seen)
}

func countLinks(doc hindsightarchive.Document) int {
	total := 0
	for _, fact := range doc.Facts {
		total += len(fact.CausalRelations)
	}
	return total
}
