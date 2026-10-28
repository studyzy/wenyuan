package transferarchive

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// This file writes a carried row the way the reference's producer does.
//
// The reference dumps a whole-bank section with
//
//	json.dumps(rows, indent=2, default=_row_json_default)   (engine/transfer/export.py:918)
//
// so a row file is a JSON list drawn at two-space indent, and each row's keys
// follow the table's own column order — the row is a `SELECT *`
// (engine/transfer/export.py:431-440), and CPython's json.dumps preserves the
// order a dict was built in.
//
// Go's encoding/json sorts a map's keys and writes compact, so a row written
// straight from a map matches neither the reference's order nor its layout.

// rowColumns records the column order of the tables whose rows an archive
// carries verbatim, in the order migrations/0002_reference_tables.sql declares
// them. A key a row carries that the order does not name is written after the
// known ones, sorted, so a column added to the schema later is still carried
// rather than dropped.
var rowColumns = map[string][]string{
	BanksName: {
		"bank_id", "name", "disposition", "created_at", "updated_at", "mission",
		"last_consolidated_at", "mission_changed_at", "config", "internal_id",
	},
	DirectivesName: {
		"id", "bank_id", "name", "content", "priority", "is_active", "tags",
		"created_at", "updated_at",
	},
	WebhooksName: {
		"id", "bank_id", "url", "secret", "event_types", "enabled", "http_config",
		"created_at", "updated_at",
	},
	MentalModelsName: {
		"id", "bank_id", "name", "source_query", "content", "tags",
		"last_refreshed_at", "created_at", "reflect_response", "max_tokens",
		"trigger", "last_refreshed_source_query", "structured_content", "subtype",
		"description", "entity_id", "observations", "links", "last_updated",
		"last_memory_seen_at", "last_refresh_failed_at",
	},
	MentalModelHistoryName: {"mental_model_id", "bank_id", "content", "changed_at"},
	AttachmentsName: {
		"bank_id", "document_id", "attachment_hash", "short_id", "media_type",
		"byte_size", "kind", "filename", "created_at", "entry",
	},
	DataEntry(AsyncOperationsTable): {
		"operation_id", "bank_id", "operation_type", "status", "created_at",
		"updated_at", "completed_at", "error_message", "result_metadata",
		"worker_id", "claimed_at", "retry_count", "task_payload", "next_retry_at",
		"serialization_key",
	},
	DataEntry(GraphMaintenanceQueueTable):  {"unit_id", "bank_id", "enqueued_at"},
	DataEntry(EntityMaintenanceQueueTable): {"bank_id", "canonical_name", "enqueued_at"},
	DataEntry(InvalidatedMemoryTable): {
		"id", "bank_id", "text", "fact_type", "context", "event_date",
		"occurred_start", "occurred_end", "mentioned_at", "document_id",
		"chunk_id", "metadata", "tags", "observation_scopes", "created_at",
		"invalidated_at",
	},
	HistoryEntry(AuditLogTable): {
		"id", "bank_id", "action", "transport", "started_at", "ended_at",
		"request", "response", "metadata",
	},
	HistoryEntry(LLMRequestsTable): {
		"id", "bank_id", "operation", "scope", "trace_id", "span_id",
		"parent_span_id", "provider", "model", "status", "started_at", "ended_at",
		"duration_ms", "input_tokens", "output_tokens", "cached_tokens",
		"thoughts_tokens", "total_tokens", "input", "output", "error", "llm_info",
		"metadata",
	},
}

// ColumnOrder returns the column order of a carried table, or nil when the
// table has none recorded. A caller may name the table either by its entry
// ("banks.json", "data/async_operations.json") or by its bare name
// ("banks", "async_operations").
func ColumnOrder(table string) []string {
	if order, ok := rowColumns[table]; ok {
		return order
	}
	if order, ok := rowColumns[table+".json"]; ok {
		return order
	}
	return rowColumns[DataDir+"/"+table+".json"]
}

// EncodeRowFile renders carried rows as the reference's producer writes a
// row-file section: a JSON list at CPython's two-space indent, every row
// carrying its table's columns in column order.
//
// An empty (or nil) section is `[]`, which is what `json.dumps([])` produces and
// what the reference writes for a selected-but-empty section.
func EncodeRowFile(rows []map[string]any, order []string) ([]byte, error) {
	if len(rows) == 0 {
		return []byte("[]"), nil
	}
	ordered := make([]orderedObject, 0, len(rows))
	for _, row := range rows {
		ordered = append(ordered, orderedObject{values: row, keys: orderedKeys(row, order)})
	}
	encoded, err := json.Marshal(ordered)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, encoded, "", "  "); err != nil {
		return nil, err
	}
	return unescapeNonCPython(out.Bytes()), nil
}

// orderedObject is a JSON object whose members keep a caller-chosen order.
type orderedObject struct {
	values map[string]any
	keys   []string
}

// MarshalJSON writes the object's members in the recorded order.
func (o orderedObject) MarshalJSON() ([]byte, error) {
	var out bytes.Buffer
	out.WriteByte('{')
	for index, key := range o.keys {
		if index > 0 {
			out.WriteByte(',')
		}
		name, err := encodeJSONValue(key)
		if err != nil {
			return nil, err
		}
		out.Write(name)
		out.WriteByte(':')
		value, err := encodeJSONValue(o.values[key])
		if err != nil {
			return nil, err
		}
		out.Write(value)
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

// orderedKeys lists a row's keys in column order, then any key the order does
// not name, sorted.
func orderedKeys(row map[string]any, order []string) []string {
	keys := make([]string, 0, len(row))
	written := make(map[string]struct{}, len(row))
	for _, key := range order {
		if _, present := row[key]; !present {
			continue
		}
		keys = append(keys, key)
		written[key] = struct{}{}
	}
	rest := make([]string, 0, len(row))
	for key := range row {
		if _, done := written[key]; !done {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	return append(keys, rest...)
}

// encodeJSONValue renders a value as compact JSON without Go's HTML escaping.
func encodeJSONValue(value any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// unescapeNonCPython undoes the escapes Go writes and CPython never does.
// SetEscapeHTML(false) already stops Go turning '<', '>' and '&' into
// \u003c / \u003e / \u0026; the two line-separator code points are still escaped
// by some paths, and CPython writes them literally.
func unescapeNonCPython(encoded []byte) []byte {
	if !bytes.Contains(encoded, []byte(`\u202`)) {
		return encoded
	}
	out := bytes.ReplaceAll(encoded, []byte(`\u2028`), []byte("\u2028"))
	return bytes.ReplaceAll(out, []byte(`\u2029`), []byte("\u2029"))
}

// NormalizeRowValue reads one value out of a carried row and returns it in the
// form the reference's producer writes.
//
// The reference's `default=_row_json_default` hook renders a datetime through
// Python's `isoformat()`, which always writes a fractional part (`.000000` when
// the microsecond count is zero) and always with a `+00:00` offset for a UTC
// value. A timestamp this service writes from an archive row is therefore
// rebuilt from its parsed instant rather than passed through as text.
func NormalizeRowValue(value any) any {
	switch typed := value.(type) {
	case json.Number, nil:
		return typed
	case time.Time:
		return PyISOString(typed)
	case *time.Time:
		if typed == nil {
			return nil
		}
		return PyISOString(*typed)
	case string:
		// A timestamp kept as text (several row timestamps travel through the
		// API formatter) is rewritten once it parses as one.
		if parsed, ok := parseTimestampText(typed); ok {
			return PyISOString(parsed)
		}
		return typed
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, NormalizeRowValue(item))
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = NormalizeRowValue(item)
		}
		return out
	default:
		return value
	}
}

// parseTimestampText reads the timestamp forms this service writes into a row.
func parseTimestampText(text string) (time.Time, bool) {
	if len(text) < len("2006-01-02T15:04:05") {
		return time.Time{}, false
	}
	// Only a value that opens with a date and a time is a candidate; anything
	// else is ordinary text and is left alone.
	if text[4] != '-' || text[7] != '-' || (text[10] != 'T' && text[10] != ' ') {
		return time.Time{}, false
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999-07",
		"2006-01-02 15:04:05.999999999Z07:00",
	} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

// PyISOString renders an instant the way Python's `datetime.isoformat()` does:
// microsecond precision, always six fractional digits, and `+00:00` for UTC.
func PyISOString(value time.Time) string {
	utc := value.UTC()
	var out strings.Builder
	out.WriteString(utc.Format("2006-01-02T15:04:05"))
	out.WriteByte('.')
	micros := utc.Nanosecond() / 1000
	digits := [6]byte{'0', '0', '0', '0', '0', '0'}
	for index := 5; index >= 0; index-- {
		digits[index] = byte('0' + micros%10)
		micros /= 10
	}
	out.Write(digits[:])
	out.WriteString("+00:00")
	return out.String()
}
