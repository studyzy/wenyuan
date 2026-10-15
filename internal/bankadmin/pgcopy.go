package bankadmin

import (
	"encoding/binary"
	"fmt"
)

// The PostgreSQL binary COPY wire format, as documented in the PostgreSQL
// manual (COPY, section "Binary Format"): an 11-byte signature, an int32 flags
// field, an int32 header-extension length followed by that many bytes, then a
// sequence of tuples — each an int16 field count followed by that many fields,
// each field an int32 byte length (-1 for NULL) and that many payload bytes —
// closed by an int16 -1 trailer. All integers are big-endian.

const pgcopySignature = "PGCOPY\n\xff\r\n\x00"

// checkBinaryCopyHeader verifies the stream opens with the PGCOPY signature
// and returns the offset of the first tuple, past the fixed header and its
// extension bytes.
func checkBinaryCopyHeader(data []byte) (int, error) {
	if len(data) < len(pgcopySignature)+8 || string(data[:len(pgcopySignature)]) != pgcopySignature {
		return 0, fmt.Errorf("backup stream is not in PostgreSQL binary COPY format")
	}
	pos := len(pgcopySignature) + 4 // past the signature and the flags field
	extension := int(int32(binary.BigEndian.Uint32(data[pos:])))
	pos += 4 + extension
	if extension < 0 || pos > len(data) {
		return 0, fmt.Errorf("binary COPY header extension is truncated")
	}
	return pos, nil
}

// stripBinaryCopyFields rewrites a binary COPY stream so every tuple drops the
// fields at the given indices. Binary COPY carries no column identities — the
// fields match the COPY column list purely by position — so a backed-up column
// the target no longer has must be removed from the stream itself; replaying
// it unedited would desynchronise the stream and land values in the wrong
// columns. sourceFieldCount is the tuple width the manifest declares, against
// which every tuple is checked.
func stripBinaryCopyFields(data []byte, dropped []int, sourceFieldCount int) ([]byte, error) {
	if len(dropped) == 0 {
		return data, nil
	}
	pos, err := checkBinaryCopyHeader(data)
	if err != nil {
		return nil, err
	}
	omit := make(map[int]bool, len(dropped))
	for _, index := range dropped {
		omit[index] = true
	}
	keptCount := sourceFieldCount - len(omit)
	out := make([]byte, 0, len(data))
	out = append(out, data[:pos]...)

	for {
		if pos+2 > len(data) {
			return nil, fmt.Errorf("binary COPY stream is truncated mid-tuple")
		}
		fieldCount := int(int16(binary.BigEndian.Uint16(data[pos:])))
		pos += 2
		if fieldCount == -1 {
			out = append(out, data[pos-2:pos]...)
			break
		}
		if fieldCount != sourceFieldCount {
			return nil, fmt.Errorf("binary COPY tuple has %d fields, the manifest declares %d", fieldCount, sourceFieldCount)
		}
		out = binary.BigEndian.AppendUint16(out, uint16(keptCount))
		for index := 0; index < fieldCount; index++ {
			if pos+4 > len(data) {
				return nil, fmt.Errorf("binary COPY stream is truncated mid-field")
			}
			length := int(int32(binary.BigEndian.Uint32(data[pos:])))
			pos += 4
			if length < 0 {
				// NULL: no payload follows a negative length.
				if !omit[index] {
					out = append(out, data[pos-4:pos]...)
				}
				continue
			}
			if pos+length > len(data) {
				return nil, fmt.Errorf("binary COPY stream is truncated mid-field")
			}
			if !omit[index] {
				out = binary.BigEndian.AppendUint32(out, uint32(length))
				out = append(out, data[pos:pos+length]...)
			}
			pos += length
		}
	}
	if pos != len(data) {
		return nil, fmt.Errorf("binary COPY stream has %d trailing byte(s) after the end-of-data marker", len(data)-pos)
	}
	return out, nil
}
