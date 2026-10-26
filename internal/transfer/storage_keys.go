package transfer

import (
	"fmt"
	"strings"
)

// DocumentExportStorageKey is where a document export stashes its archive.
//
// The two export routes key their archives differently in the reference, and the
// difference is observable: `result_metadata.storage_key` (and the download_url
// derived from it) is part of the operation's public response.
//
//   - the whole-bank export keys under `banks/{bank}/exports/{uuid}/transfer.zip`
//     (engine/memory_engine.py:3735);
//   - the document export keys under `tenants/{schema}/banks/{bank}/exports/{uuid}/transfer.zip`
//     — it builds the key from `bank_storage_prefix(bank_id)`
//     (engine/memory_engine.py:3670, engine/storage/__init__.py:12-30), whose
//     first segment is the caller's tenant schema.
//
// The tenant segment comes first because an object store is shared across tenant
// schemas: two tenants' banks with the same id would otherwise write the same
// key. Both layouts are accepted by the download route, which recovers the
// owning bank from either.
func DocumentExportStorageKey(schema, bankID, exportID string) string {
	return "tenants/" + keySegment(schema) + "/banks/" + keySegment(bankID) +
		"/exports/" + keySegment(exportID) + "/transfer.zip"
}

// keySegment encodes a name as exactly one key segment.
//
// It is the reference's `key_segment` (engine/storage/__init__.py:29-40):
// everything outside the unreserved set is percent-encoded — `/` so a name
// cannot nest under another's prefix, `%` so two names never share an encoding —
// and a `.` is encoded as well, because an object store refuses `.` and `..` as
// path segments. A key built here therefore always passes the download route's
// segment validation.
func keySegment(value string) string {
	var out strings.Builder
	for index := 0; index < len(value); index++ {
		char := value[index]
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9',
			char == '-', char == '_':
			out.WriteByte(char)
		default:
			fmt.Fprintf(&out, "%%%02X", char)
		}
	}
	return out.String()
}
