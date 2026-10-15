package storage

import "strconv"

// BuildChunkID flattens a (bank, document, index) triple into the chunk id.
//
// The mapping is injective: the separator is escaped inside each component, so
// the id always splits back into exactly three fields. Without that, ("a","b_c")
// and ("a_b","c") would both produce "a_b_c_0" and one bank's chunk would
// overwrite the other's.
func BuildChunkID(bankID, documentID string, index int) string {
	return escapeChunkIDPart(bankID) + "_" + escapeChunkIDPart(documentID) + "_" + strconv.Itoa(index)
}

// escapeChunkIDPart escapes the escape character and the separator inside one
// id component.
func escapeChunkIDPart(value string) string {
	out := make([]byte, 0, len(value))
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '~':
			out = append(out, "~7E"...)
		case '_':
			out = append(out, "~5F"...)
		default:
			out = append(out, value[i])
		}
	}
	return string(out)
}
