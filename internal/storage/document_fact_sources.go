package storage

import "context"

// DocumentFactSourceReader is the optional capability a backend implements when
// it can name the document each of a set of facts was extracted from.
//
// The document change history files a consolidation's observation writes under
// the documents their source facts came from, which costs one read of the
// facts' `document_id` column. It is a capability rather than a method on Store
// so a backend that does not keep documents simply records no consolidation
// events, instead of every backend growing a query only this feature reads.
type DocumentFactSourceReader interface {
	// DocumentFactSources returns fact id -> document id for the facts that
	// belong to a document. A fact with no document is absent from the map.
	DocumentFactSources(ctx context.Context, bankID string, factIDs []string) (map[string]string, error)
}

// Both backends carry the capability.
var (
	_ DocumentFactSourceReader = (*MemoryStore)(nil)
	_ DocumentFactSourceReader = (*PostgresStore)(nil)
)
