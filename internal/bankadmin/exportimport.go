package bankadmin

// Writing a bank's export back into a tenant.
//
// The applier is the write side of the reader in exportarchive.go. It writes
// through the storage interface's public operations, in the order the schema's
// foreign keys demand: the bank row, then documents and their chunks, then the
// facts anchored to those documents, then the observations consolidated from
// those facts, then the mental models and the knowledge pages that name them.
//
// Two consequences of writing through the public interface rather than the
// tables are stated rather than hidden: an observation and a knowledge page
// body each are a memory unit, so they are embedded and therefore recalled like
// any other fact; and a document's chunk text is stored as the export carries
// it.

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// ImportExportMode is what an existing target bank means to a replay.
type ImportExportMode = string

// The three modes a replay accepts.
const (
	// ImportExportRename writes the archive under an id derived from the
	// occupied target, leaving what is there untouched: the target keeps its
	// facts, the archive lands beside it as <name>-import, <name>-import-2, ...
	ImportExportRename ImportExportMode = "rename"
	// ImportExportReplace clears the target bank and writes the archive into it.
	// Any local write the target received after its export is lost, which is
	// what the mode is for: a restore is a whole bank.
	ImportExportReplace ImportExportMode = "replace"
	// ImportExportMerge folds the archive into the target, skipping a document
	// the target already holds under the same id, so re-running an import after
	// further source writes adds what is new without duplicating or destroying
	// what already moved.
	ImportExportMerge ImportExportMode = "merge"
)

// ExportImportOptions describes one replay.
type ExportImportOptions struct {
	// BankID is the target bank the archive is re-homed under.
	BankID string
	// Mode decides what an existing target bank means. See ImportExportMode.
	Mode ImportExportMode
	// Embeddings fills the units' vectors. A nil generator writes the bank
	// without vectors, the way a write does when no model is configured.
	Embeddings storage.EmbeddingGenerator
}

// ExportImportReport counts what one replay wrote.
type ExportImportReport struct {
	BankID                 string
	DocumentsImported      int
	ChunksImported         int
	FactsImported          int
	ObservationsImported   int
	MentalModelsImported   int
	KnowledgePagesImported int
	// ObservationsLost counts observations whose backing facts the archive did
	// not carry; an observation no fact supports cannot be written.
	ObservationsLost int
	// UnwrittenMentalModels names the models the store refused, with its reason:
	// a page naming one of them is stored without its body.
	UnwrittenMentalModels []string
}

// RunExportImport replays an export archive into opts.BankID. The target must
// be absent: a replay writes a whole bank, so merging it into a live one would
// mix two banks' evidence under one id.
func RunExportImport(ctx context.Context, store storage.Store, archive ExportArchive, opts ExportImportOptions) (ExportImportReport, error) {
	report := ExportImportReport{BankID: opts.BankID}
	switch opts.Mode {
	case ImportExportRename, ImportExportReplace, ImportExportMerge:
	default:
		return report, fmt.Errorf("no import mode given: want rename, replace or merge")
	}
	occupied := false
	if _, err := store.GetBank(ctx, opts.BankID); err == nil {
		occupied = true
	}
	if occupied && opts.Mode == ImportExportRename {
		target, err := freeArchiveBankID(ctx, store, opts.BankID)
		if err != nil {
			return report, err
		}
		opts.BankID = target
	}
	// A rename settles the id it actually writes under, which is what the
	// caller has to be told.
	report.BankID = opts.BankID
	applier := exportApplier{store: store, opts: opts, report: &report, existing: occupied}
	if _, err := applier.prepareBank(ctx, archive.Bank); err != nil {
		return report, err
	}
	factIDs, err := applier.writeDocuments(ctx, archive.Documents)
	if err != nil {
		return report, err
	}
	observationIDs, err := applier.writeObservations(ctx, archive.Observations, factIDs)
	if err != nil {
		return report, err
	}
	applier.writeMentalModels(ctx, archive.MentalModels, append(factIDs.all(), observationIDs...))
	report.UnwrittenMentalModels = applier.lostModels
	if err := applier.writeKnowledgePages(ctx, archive.KnowledgePages); err != nil {
		return report, err
	}
	return report, nil
}

// exportApplier holds one replay's state.
type exportApplier struct {
	store storage.Store
	opts  ExportImportOptions
	// existing reports that the target bank was already there, which is what
	// distinguishes a merge or a replace from an import into a fresh id.
	existing bool
	report   *ExportImportReport
	// lostModels names the mental models the store refused, with its reason: a
	// page whose content lives in one of them has lost its body, which the
	// caller has to hear about rather than discover by reading the page.
	lostModels []string
}

// prepareBank settles the bank row: a fresh id is created with the export's
// configuration, a merge leaves an existing bank's configuration alone, and a
// replace rewrites it after the bank's memories are cleared.
func (a exportApplier) prepareBank(ctx context.Context, bank ExportBank) (storage.Bank, error) {
	// The flag answers which state the bank should end in; the store answers
	// whether there is a row to replace right now.
	if _, err := a.store.GetBank(ctx, a.opts.BankID); err != nil {
		return a.createBank(ctx, bank)
	}
	if a.opts.Mode == ImportExportMerge {
		return a.store.GetBank(ctx, a.opts.BankID)
	}
	// A replace clears the bank's memories and then takes the archive's
	// configuration, so the bank ends up exactly as exported.
	if _, err := a.store.ClearBankMemories(ctx, a.opts.BankID, ""); err != nil {
		return storage.Bank{}, fmt.Errorf("clear bank %s: %w", a.opts.BankID, err)
	}
	replaced, err := a.store.ReplaceBank(ctx, storage.Bank{
		BankID:      a.opts.BankID,
		Name:        a.opts.BankID,
		Disposition: bank.Disposition,
		Mission:     bank.Mission,
		Config:      bank.Config,
	})
	if err != nil {
		return storage.Bank{}, fmt.Errorf("replace bank %s: %w", a.opts.BankID, err)
	}
	return replaced, nil
}

// createBank writes a fresh bank row with the export's configuration.
func (a exportApplier) createBank(ctx context.Context, bank ExportBank) (storage.Bank, error) {
	created, err := a.store.CreateBank(ctx, storage.Bank{
		BankID:      a.opts.BankID,
		Name:        a.opts.BankID,
		Disposition: bank.Disposition,
		Mission:     bank.Mission,
		Config:      bank.Config,
	})
	if err != nil {
		return storage.Bank{}, fmt.Errorf("create bank %s: %w", a.opts.BankID, err)
	}
	return created, nil
}

// freeArchiveBankID derives an id neither the bank nor its existing siblings
// occupy, so a rename never overwrites anything: <name>-import, then
// <name>-import-2, -3, ...
func freeArchiveBankID(ctx context.Context, store storage.Store, target string) (string, error) {
	for suffix := 1; suffix <= 100; suffix++ {
		candidate := target + "-import"
		if suffix > 1 {
			candidate = fmt.Sprintf("%s-import-%d", target, suffix)
		}
		if _, err := store.GetBank(ctx, candidate); err != nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no free bank id for %q", target)
}

// skipExistingDocument reports whether a merge should leave a document alone
// because the target already holds it.
func (a exportApplier) skipExistingDocument(ctx context.Context, documentID string) bool {
	if !a.existing || a.opts.Mode != ImportExportMerge {
		return false
	}
	_, err := a.store.GetDocument(ctx, a.opts.BankID, documentID)
	return err == nil
}

// factIDsByDocument remembers which unit each fact landed under, keyed by its
// document and its position there, which is how an observation names its
// evidence.
type factIDsByDocument map[string][]string

// all lists every identity in a stable order.
func (f factIDsByDocument) all() []string {
	documents := make([]string, 0, len(f))
	for document := range f {
		documents = append(documents, document)
	}
	sort.Strings(documents)
	out := make([]string, 0)
	for _, document := range documents {
		out = append(out, f[document]...)
	}
	return out
}

// writeDocuments writes every document with its chunks and facts.
func (a exportApplier) writeDocuments(ctx context.Context, documents []ExportDocument) (factIDsByDocument, error) {
	factIDs := make(factIDsByDocument, len(documents))
	for _, document := range documents {
		if document.ID == "" {
			return nil, fmt.Errorf("archive holds a document without an id")
		}
		if a.skipExistingDocument(ctx, document.ID) {
			continue
		}
		if _, err := a.store.PutDocument(ctx, a.opts.BankID, document.ID, storage.Document{
			ID:           document.ID,
			BankID:       a.opts.BankID,
			OriginalText: document.OriginalText,
			ContentHash:  storage.ContentHash(document.OriginalText),
			RetainParams: document.RetainParams,
			Tags:         document.Tags,
			CreatedAt:    exportTime(document.CreatedAt, a.now()),
			UpdatedAt:    exportTime(document.CreatedAt, a.now()),
		}); err != nil {
			return nil, fmt.Errorf("write document %s: %w", document.ID, err)
		}
		a.report.DocumentsImported++
		chunks := make([]storage.DocumentChunk, 0, len(document.Chunks))
		for _, chunk := range document.Chunks {
			chunks = append(chunks, storage.DocumentChunk{ChunkIndex: chunk.ChunkIndex, ChunkText: chunk.ChunkText})
		}
		if _, err := a.store.PutDocumentChunks(ctx, a.opts.BankID, document.ID, chunks, true); err != nil {
			return nil, fmt.Errorf("write chunks of %s: %w", document.ID, err)
		}
		a.report.ChunksImported += len(chunks)
		units, err := a.factUnits(document)
		if err != nil {
			return nil, err
		}
		if err := a.writeUnits(ctx, document.Facts, units); err != nil {
			return nil, fmt.Errorf("write facts of %s: %w", document.ID, err)
		}
		ids := make([]string, 0, len(units))
		for _, unit := range units {
			ids = append(ids, unit.ID)
		}
		factIDs[document.ID] = ids
		a.report.FactsImported += len(units)
	}
	return factIDs, nil
}

// factUnits turns a document's facts into stored units, each anchored to the
// chunk of the document the fact came from.
func (a exportApplier) factUnits(document ExportDocument) ([]storage.MemoryUnit, error) {
	units := make([]storage.MemoryUnit, 0, len(document.Facts))
	for ordinal, fact := range document.Facts {
		factType, err := memoryUnitType(fact.FactType)
		if err != nil {
			return nil, fmt.Errorf("document %s: %w", document.ID, err)
		}
		if fact.Text == "" {
			return nil, fmt.Errorf("document %s holds a fact without text", document.ID)
		}
		units = append(units, storage.MemoryUnit{
			ID:         exportFactID(a.opts.BankID, document.ID, ordinal),
			BankID:     a.opts.BankID,
			Text:       fact.Text,
			Context:    fact.Context,
			FactType:   factType,
			Metadata:   fact.Metadata,
			DocumentID: document.ID,
			ChunkID:    exportChunkID(a.opts.BankID, document.ID, fact.ChunkIndex),
			Tags:       fact.Tags,
			// The archive carries the fact's own timestamps, so the unit keeps
			// when it was written rather than taking the replay's clock.
			EventDate:         exportTimePtr(fact.EventDate),
			OccurredStart:     exportTimePtr(fact.OccurredStart),
			OccurredEnd:       exportTimePtr(fact.OccurredEnd),
			MentionedAt:       exportTimePtr(fact.MentionedAt),
			CreatedAt:         exportTimePtr(fact.CreatedAt),
			ObservationScopes: fact.ObservationScopes,
		})
	}
	return units, nil
}

// writeObservations writes the consolidated observations, pointing each at the
// units its evidence names.
func (a exportApplier) writeObservations(ctx context.Context, observations []ExportObservation, factIDs factIDsByDocument) ([]string, error) {
	units := make([]storage.MemoryUnit, 0, len(observations))
	for _, observation := range observations {
		if observation.SourceID == "" {
			return nil, fmt.Errorf("archive holds an observation without an id")
		}
		proof := proofUnits(factIDs, observation.Sources)
		if len(proof) == 0 {
			a.report.ObservationsLost++
			continue
		}
		factType := storage.TypeObservation
		units = append(units, storage.MemoryUnit{
			ID:                exportObservationUnitID(a.opts.BankID, observation.SourceID),
			BankID:            a.opts.BankID,
			Text:              observation.Text,
			ContextAbsent:     true,
			FactType:          factType,
			Metadata:          observation.Metadata,
			Tags:              observation.Tags,
			ProofCount:        proofCount(observation.ProofCount, len(proof)),
			SourceMemoryIDs:   proof,
			EventDate:         exportTimePtr(observation.EventDate),
			OccurredStart:     exportTimePtr(observation.OccurredStart),
			OccurredEnd:       exportTimePtr(observation.OccurredEnd),
			MentionedAt:       exportTimePtr(observation.MentionedAt),
			CreatedAt:         exportTimePtr(observation.CreatedAt),
			ConsolidatedAt:    exportTimePtr(observation.ConsolidatedAt),
			ObservationScopes: observation.ObservationScopes,
		})
	}
	if err := a.writeUnits(ctx, nil, units); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(units))
	for _, unit := range units {
		ids = append(ids, unit.ID)
	}
	a.report.ObservationsImported += len(units)
	return ids, nil
}

// writeUnits stores the units with their vectors and the entities they name,
// then the memory links a write would have created between them.
func (a exportApplier) writeUnits(ctx context.Context, facts []ExportFact, units []storage.MemoryUnit) error {
	if len(units) == 0 {
		return nil
	}
	if err := embedExportUnits(ctx, a.opts.Embeddings, units); err != nil {
		return err
	}
	for index, unit := range units {
		if _, err := a.store.UpsertUnit(ctx, unit); err != nil {
			return fmt.Errorf("write unit %s: %w", unit.ID, err)
		}
		if facts == nil || index >= len(facts) {
			continue
		}
		if len(facts[index].Entities) == 0 {
			continue
		}
		if err := a.store.SetUnitEntities(ctx, a.opts.BankID, unit.ID, facts[index].Entities, false); err != nil {
			return fmt.Errorf("link entities of %s: %w", unit.ID, err)
		}
	}
	if facts == nil {
		return nil
	}
	ids := make([]string, 0, len(units))
	for _, unit := range units {
		ids = append(ids, unit.ID)
	}
	for index := 1; index < len(ids); index++ {
		if err := a.store.UpsertLink(ctx, storage.MemoryLink{
			BankID:     a.opts.BankID,
			FromUnitID: ids[index-1],
			ToUnitID:   ids[index],
			LinkType:   storage.LinkTemporal,
			Weight:     1,
			CreatedAt:  a.now(),
		}); err != nil {
			return fmt.Errorf("link %s: %w", ids[index], err)
		}
	}
	return nil
}

// writeMentalModels restores the models a knowledge page reads through, with
// the model's evidence re-pointed at this replay's units: the archive names
// facts the source stored, and this bank's facts carry the same text under
// identities derived from the target.
func (a exportApplier) writeMentalModels(ctx context.Context, models []ExportMentalModel, unitIDs []string) {
	for _, model := range models {
		if model.ID == "" {
			continue
		}
		restored := storage.MentalModel{
			ID:                       model.ID,
			BankID:                   a.opts.BankID,
			Subtype:                  exportMentalModelSubtype(model.Subtype),
			Name:                     model.Name,
			Description:              model.Description,
			SourceQuery:              model.SourceQuery,
			Content:                  model.Content,
			EntityID:                 model.EntityID,
			Observations:             remapModelEvidence(model.Observations, unitIDs),
			Links:                    model.Links,
			Tags:                     model.Tags,
			MaxTokens:                model.MaxTokens,
			Trigger:                  model.Trigger,
			Structured:               model.Structured,
			ReflectResult:            model.ReflectResult,
			LastRefreshedSourceQuery: model.LastRefreshedSourceQuery,
			LastRefreshedAt:          exportTime(model.LastRefreshedAt, a.now()),
			LastMemorySeenAt:         exportTimePtr(model.LastMemorySeenAt),
			LastRefreshFailedAt:      exportTimePtr(model.LastRefreshFailedAt),
			LastFullRefreshedAt:      exportTimePtr(model.LastFullRefreshedAt),
			LastUpdated:              exportTime(model.LastUpdated, a.now()),
			CreatedAt:                exportTime(model.CreatedAt, a.now()),
		}
		if _, err := a.store.PutMentalModel(ctx, restored); err != nil {
			a.lostModels = append(a.lostModels, fmt.Sprintf("%s: %v", model.ID, err))
			continue
		}
		a.report.MentalModelsImported++
	}
}

// writeKnowledgePages restores the tree, parents before children so the
// foreign key holds. Page ids are the export's own: a node id is unique per
// bank, and the copy lives under a different bank id.
func (a exportApplier) writeKnowledgePages(ctx context.Context, pages []ExportKnowledgePage) error {
	byID := make(map[string]ExportKnowledgePage, len(pages))
	for _, page := range pages {
		byID[page.ID] = page
	}
	written := make(map[string]bool, len(pages))
	var write func(page ExportKnowledgePage) error
	write = func(page ExportKnowledgePage) error {
		if written[page.ID] {
			return nil
		}
		parent := ""
		if page.ParentID != nil {
			parent = *page.ParentID
			if ancestor, ok := byID[parent]; ok {
				if err := write(ancestor); err != nil {
					return err
				}
			}
		}
		written[page.ID] = true
		modelID := ""
		if page.MentalModelID != nil {
			modelID = *page.MentalModelID
		}
		if _, err := a.store.PutKnowledgeNode(ctx, storage.KnowledgeNode{
			ID:            page.ID,
			BankID:        a.opts.BankID,
			ParentID:      parent,
			Kind:          storage.KnowledgeKind(page.Kind),
			Title:         page.Name,
			MentalModelID: modelID,
			Managed:       page.Managed,
			SortOrder:     page.SortOrder,
			CreatedAt:     exportTime(page.CreatedAt, a.now()),
			UpdatedAt:     exportTime(page.UpdatedAt, a.now()),
		}); err != nil {
			return fmt.Errorf("write knowledge page %s: %w", page.ID, err)
		}
		a.report.KnowledgePagesImported++
		return nil
	}
	for _, page := range pages {
		if err := write(page); err != nil {
			return err
		}
	}
	return nil
}

// sortStringSlice sorts in place, so the replay's unit order is stable.
func sortStringSlice(values []string) {
	sort.Strings(values)
}

// now reports the replay's clock.
func (a exportApplier) now() time.Time { return time.Now().UTC() }

// proofUnits resolves an observation's evidence onto this replay's units.
func proofUnits(factIDs factIDsByDocument, sources []ExportProofLink) []string {
	out := make([]string, 0, len(sources))
	seen := make(map[string]bool, len(sources))
	for _, source := range sources {
		ids, ok := factIDs[source.DocumentID]
		if !ok || source.FactIndex < 0 || source.FactIndex >= len(ids) {
			continue
		}
		id := ids[source.FactIndex]
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// remapModelEvidence replaces the source's unit ids in a model's observation
// record with this bank's, by position: a model's evidence list was written in
// the source bank's order, and the replay kept that order.
func remapModelEvidence(observations map[string]any, unitIDs []string) map[string]any {
	if len(observations) == 0 || len(unitIDs) == 0 {
		return observations
	}
	out := make(map[string]any, len(observations))
	for key, value := range observations {
		out[key] = value
	}
	out["memory_unit_ids"] = unitIDs
	return out
}

// memoryUnitType maps a fact's stored type onto the unit type.
func memoryUnitType(factType string) (storage.MemoryUnitType, error) {
	switch storage.MemoryUnitType(factType) {
	case storage.TypeWorld, storage.TypeExperience, storage.TypeObservation:
		return storage.MemoryUnitType(factType), nil
	case "":
		return storage.TypeWorld, nil
	default:
		return "", fmt.Errorf("unknown fact type %q", factType)
	}
}

// embedExportUnits fills the units' vectors. The embedding generator splits its
// own input into provider-sized requests, so the whole set is handed over at
// once, the way the served import does.
func embedExportUnits(ctx context.Context, embeddings storage.EmbeddingGenerator, units []storage.MemoryUnit) error {
	if embeddings == nil || len(units) == 0 {
		return nil
	}
	texts := make([]string, len(units))
	for index, unit := range units {
		texts[index] = unit.Text
	}
	vectors, err := embeddings.EmbedPassages(ctx, texts)
	if err != nil {
		return err
	}
	if len(vectors) != len(texts) {
		return fmt.Errorf("embedding generator returned %d vectors for %d texts", len(vectors), len(texts))
	}
	for index := range units {
		if len(vectors[index]) > 0 {
			units[index].Embedding = vectors[index]
		}
	}
	return nil
}

// exportTime reads an archived timestamp, falling back to the given clock for an
// absent or unreadable value.
func exportTime(value *string, fallback time.Time) time.Time {
	if parsed := exportTimePtr(value); parsed != nil {
		return *parsed
	}
	return fallback
}

// exportTimePtr reads an archived timestamp, returning nil for an absent or
// unreadable value rather than failing the replay over one row's stamp.
func exportTimePtr(value *string) *time.Time {
	if value == nil || *value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		return nil
	}
	return &parsed
}

// proofCount keeps an observation's recorded evidence strength, falling back to
// the number of facts behind it.
func proofCount(recorded, sources int) int {
	if recorded > 0 {
		return recorded
	}
	if sources > 0 {
		return sources
	}
	return 1
}

// sortStrings sorts in place, so the replay's unit order is stable.
func sortStrings(values []string) {
	sortStringSlice(values)
}
