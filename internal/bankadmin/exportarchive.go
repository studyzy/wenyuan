package bankadmin

// Re-homing a bank exported by this service onto another tenant.
//
// The served import route applies the reference's archive format, whose
// importer carries documents and configuration but not the bank's synthesized
// state. An archive this service writes additionally carries mental models,
// knowledge pages and observation history, so a tenant move has to write those
// back too: the knowledge pages are the bank's curated reading, and their
// content lives in the mental model each page names.
//
// The reader below is the writer's own inverse (internal/transfer/export_build.go):
// each section is one JSON file, documents are one file each, and every row is
// the stored row. The applier writes them through the storage interface's
// public writes, so every bank-scoped table an archive carries lands in the
// target with its entity links, co-occurrences and memory links intact.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/storage"
)

// ExportManifest is the archive's index.
type ExportManifest struct {
	SchemaVersion      int    `json:"schema_version"`
	SourceBankID       string `json:"source_bank_id"`
	ExportedAt         string `json:"exported_at"`
	DocumentCount      int    `json:"document_count"`
	FactCount          int    `json:"fact_count"`
	ObservationCount   int    `json:"observation_count"`
	MentalModelCount   int    `json:"mental_model_count"`
	KnowledgePageCount int    `json:"knowledge_page_count"`
	OperationCount     int    `json:"operation_count"`
}

// ExportBank is the bank row an export carries.
type ExportBank struct {
	BankID      string         `json:"bank_id"`
	Name        string         `json:"name"`
	Disposition map[string]int `json:"disposition"`
	Mission     string         `json:"mission"`
	Config      map[string]any `json:"config"`
}

// ExportChunk is one stored segment of a document.
type ExportChunk struct {
	ChunkIndex int    `json:"chunk_index"`
	ChunkText  string `json:"chunk_text"`
}

// ExportFact is one memory unit: an extracted fact, or a consolidated
// observation when FactType says so.
type ExportFact struct {
	SourceID          string          `json:"source_id"`
	Text              string          `json:"text"`
	Context           string          `json:"context"`
	FactType          string          `json:"fact_type"`
	ChunkIndex        int             `json:"chunk_index"`
	Entities          []string        `json:"entities"`
	Tags              []string        `json:"tags"`
	Metadata          map[string]any  `json:"metadata"`
	ObservationScopes json.RawMessage `json:"observation_scopes"`
	EventDate         *string         `json:"event_date"`
	OccurredStart     *string         `json:"occurred_start"`
	OccurredEnd       *string         `json:"occurred_end"`
	MentionedAt       *string         `json:"mentioned_at"`
	CreatedAt         *string         `json:"created_at"`
	ConsolidatedAt    *string         `json:"consolidated_at"`
}

// ExportDocument is one document section: the document, its chunks, and the
// facts extracted from it, in the export's order.
type ExportDocument struct {
	ID           string         `json:"id"`
	OriginalText string         `json:"original_text"`
	RetainParams map[string]any `json:"retain_params"`
	Tags         []string       `json:"tags"`
	CreatedAt    *string        `json:"created_at"`
	Chunks       []ExportChunk  `json:"chunks"`
	Facts        []ExportFact   `json:"facts"`
}

// ExportObservation is one consolidated observation. Its evidence is document
// ids and fact positions rather than the unit ids its facts landed under.
type ExportObservation struct {
	SourceID          string            `json:"source_id"`
	Text              string            `json:"text"`
	Tags              []string          `json:"tags"`
	Metadata          map[string]any    `json:"metadata"`
	ObservationScopes json.RawMessage   `json:"observation_scopes"`
	EventDate         *string           `json:"event_date"`
	OccurredStart     *string           `json:"occurred_start"`
	OccurredEnd       *string           `json:"occurred_end"`
	MentionedAt       *string           `json:"mentioned_at"`
	CreatedAt         *string           `json:"created_at"`
	ConsolidatedAt    *string           `json:"consolidated_at"`
	ProofCount        int               `json:"proof_count"`
	Sources           []ExportProofLink `json:"sources"`
}

// ExportProofLink points one observation at the fact behind it.
type ExportProofLink struct {
	DocumentID string `json:"document_id"`
	FactIndex  int    `json:"fact_index"`
}

// ExportMentalModel is one stored model row, including the content a knowledge
// page reads through its mental_model_id.
type ExportMentalModel struct {
	ID                       string         `json:"id"`
	Name                     string         `json:"name"`
	Description              string         `json:"description"`
	Subtype                  string         `json:"subtype"`
	SourceQuery              string         `json:"source_query"`
	Content                  string         `json:"content"`
	EntityID                 string         `json:"entity_id"`
	Observations             map[string]any `json:"observations"`
	Links                    []string       `json:"links"`
	Tags                     []string       `json:"tags"`
	MaxTokens                int            `json:"max_tokens"`
	Trigger                  map[string]any `json:"trigger"`
	Structured               map[string]any `json:"structured_content"`
	ReflectResult            map[string]any `json:"reflect_response"`
	LastRefreshedSourceQuery string         `json:"last_refreshed_source_query"`
	LastRefreshedAt          *string        `json:"last_refreshed_at"`
	LastMemorySeenAt         *string        `json:"last_memory_seen_at"`
	LastRefreshFailedAt      *string        `json:"last_refresh_failed_at"`
	LastFullRefreshedAt      *string        `json:"last_full_refreshed_at"`
	LastUpdated              *string        `json:"last_updated"`
	CreatedAt                *string        `json:"created_at"`
}

// ExportKnowledgePage is one node of the bank's knowledge tree. It is a bank
// row rather than a document section, so the key is the export's own.
type ExportKnowledgePage struct {
	ID            string  `json:"id"`
	ParentID      *string `json:"parent_id"`
	Kind          string  `json:"kind"`
	Name          string  `json:"name"`
	MentalModelID *string `json:"mental_model_id"`
	SortOrder     int     `json:"sort_order"`
	Managed       bool    `json:"managed"`
	CreatedAt     *string `json:"created_at"`
	UpdatedAt     *string `json:"updated_at"`
}

// ExportArchive is a whole-bank export read back into memory.
type ExportArchive struct {
	Manifest       ExportManifest
	Bank           ExportBank
	Documents      []ExportDocument
	Observations   []ExportObservation
	MentalModels   []ExportMentalModel
	KnowledgePages []ExportKnowledgePage
}

// ReadExportArchive reads an archive written by `bankctl bank export-bank`.
// The directory holds the sections as separate JSON files, documents one file
// each; a section the bank does not have is an empty file or absent.
func ReadExportArchive(dir string) (ExportArchive, error) {
	var archive ExportArchive
	if err := readJSONFile(dir, "manifest.json", &archive.Manifest); err != nil {
		return ExportArchive{}, err
	}
	banks := make([]ExportBank, 0, 1)
	if err := readJSONFile(dir, "banks.json", &banks); err != nil {
		return ExportArchive{}, err
	}
	if len(banks) != 1 {
		return ExportArchive{}, fmt.Errorf("archive holds %d banks, want exactly one", len(banks))
	}
	archive.Bank = banks[0]
	names, err := documentSectionNames(dir)
	if err != nil {
		return ExportArchive{}, err
	}
	for _, name := range names {
		var document ExportDocument
		if err := readJSONFile(dir+"/documents", name, &document); err != nil {
			return ExportArchive{}, err
		}
		archive.Documents = append(archive.Documents, document)
	}
	for _, section := range []struct {
		name string
		into any
	}{
		{"observations.json", &archive.Observations},
		{"mental_models.json", &archive.MentalModels},
		{"knowledge_pages.json", &archive.KnowledgePages},
	} {
		if err := readJSONFile(dir, section.name, section.into); err != nil {
			return ExportArchive{}, err
		}
	}
	return archive, nil
}

// documentSectionNames lists the document section files in export order, which
// is their numeric name rather than their lexical one.
func documentSectionNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir + "/documents")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Slice(names, func(i, j int) bool {
		return sectionOrdinal(names[i]) < sectionOrdinal(names[j])
	})
	return names, nil
}

// sectionOrdinal reads the numeric stem of a document section file, so the
// order survives an archive with more than a thousand documents.
func sectionOrdinal(name string) int {
	stem := strings.TrimSuffix(name, ".json")
	value, err := strconv.Atoi(stem)
	if err != nil {
		return 1 << 30
	}
	return value
}

// readJSONFile decodes one section. An absent section is empty rather than an
// error: a bank that never grew a knowledge page writes none.
func readJSONFile(dir, name string, into any) error {
	data, err := os.ReadFile(dir + "/" + name)
	if os.IsNotExist(err) || len(data) == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, into); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// exportMentalModelSubtype maps an archived subtype onto a stored one, defaulting
// to the pinned subtype the column requires: the store refuses a model with no
// subtype, and an archive predating the column carries none.
func exportMentalModelSubtype(subtype string) storage.MentalModelSubtype {
	candidate := storage.MentalModelSubtype(subtype)
	if storage.ValidMentalModelSubtype(candidate) {
		return candidate
	}
	return storage.SubtypePinned
}

// stableArchiveID builds a deterministic uuid identifier from its parts. The
// identity column is declared uuid, so the digest is laid out in the canonical
// 8-4-4-4-12 form with the version and variant bits set.
func stableArchiveID(kind string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(kind))
	for _, part := range parts {
		h.Write([]byte{0})
		h.Write([]byte(part))
	}
	sum := h.Sum(nil)
	buf := make([]byte, 16)
	copy(buf, sum[:16])
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	hexed := hex.EncodeToString(buf)
	return hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:32]
}

// exportFactID derives a unit identity for a fact from its document and its
// position there, so re-importing the same archive rewrites the same rows
// rather than a second copy.
func exportFactID(bankID, documentID string, ordinal int) string {
	return stableArchiveID("unit", bankID, documentID, strconv.Itoa(ordinal))
}

// exportObservationUnitID derives the unit identity one observation lands
// under. An observation is a memory unit like any fact, and its evidence is
// keyed by its own id.
func exportObservationUnitID(bankID, observationID string) string {
	return stableArchiveID("unit", bankID, "observation", observationID)
}

// exportChunkID mirrors the storage layer's own chunk identity, so units
// reference the chunks that were written rather than a name of this reader's.
func exportChunkID(bankID, documentID string, index int) string {
	return storage.BuildChunkID(bankID, documentID, index)
}
