package bankadmin

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// field is one tuple field while building a test stream: a payload with its
// wire length, or a nil payload for a NULL.
type field []byte

// buildBinaryCopyStream assembles a binary COPY stream the way PostgreSQL
// writes one: signature, flags, header extension, tuples, trailer.
func buildBinaryCopyStream(tuples ...[]field) []byte {
	out := []byte(pgcopySignature)
	out = binary.BigEndian.AppendUint32(out, 0) // flags
	out = binary.BigEndian.AppendUint32(out, 0) // header extension length
	for _, tuple := range tuples {
		out = binary.BigEndian.AppendUint16(out, uint16(len(tuple)))
		for _, value := range tuple {
			if value == nil {
				out = binary.BigEndian.AppendUint32(out, ^uint32(0)) // -1: NULL
				continue
			}
			out = binary.BigEndian.AppendUint32(out, uint32(len(value)))
			out = append(out, value...)
		}
	}
	out = binary.BigEndian.AppendUint16(out, ^uint16(0)) // end-of-data trailer
	return out
}

// stripFieldsTestTuple is the tuple every stripping test uses: "a", NULL, "c".
var stripFieldsTestTuple = []field{[]byte("a"), nil, []byte("c")}

func TestStripBinaryCopyFieldsWithoutDroppedFields(t *testing.T) {
	stream := buildBinaryCopyStream(stripFieldsTestTuple, stripFieldsTestTuple)
	stripped, err := stripBinaryCopyFields(stream, nil, 3)
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	if !bytes.Equal(stripped, stream) {
		t.Fatal("an empty drop set must return the stream unchanged")
	}
}

func TestStripBinaryCopyFieldsDropsNamedFields(t *testing.T) {
	tests := []struct {
		name    string
		dropped []int
		want    []field
	}{
		{"middle null field", []int{1}, []field{[]byte("a"), []byte("c")}},
		{"leading field", []int{0}, []field{nil, []byte("c")}},
		{"null and value", []int{0, 1}, []field{[]byte("c")}},
		{"every field", []int{0, 1, 2}, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := buildBinaryCopyStream(stripFieldsTestTuple, stripFieldsTestTuple)
			stripped, err := stripBinaryCopyFields(stream, test.dropped, 3)
			if err != nil {
				t.Fatalf("strip: %v", err)
			}
			want := buildBinaryCopyStream(test.want, test.want)
			if !bytes.Equal(stripped, want) {
				t.Fatalf("stripped stream does not match the expected shape")
			}
		})
	}
}

func TestStripBinaryCopyFieldsKeepsHeaderExtension(t *testing.T) {
	stream := buildBinaryCopyStream(stripFieldsTestTuple)
	// Rebuild the header with a non-empty extension: signature + flags, an
	// extension length of 4, and four extension bytes.
	stream = append(stream[:len(pgcopySignature)+4], binary.BigEndian.AppendUint32(nil, 4)...)
	stream = append(stream, 0xde, 0xad, 0xbe, 0xef)
	stream = binary.BigEndian.AppendUint16(stream, 3)
	for _, value := range stripFieldsTestTuple {
		if value == nil {
			stream = binary.BigEndian.AppendUint32(stream, ^uint32(0))
			continue
		}
		stream = binary.BigEndian.AppendUint32(stream, uint32(len(value)))
		stream = append(stream, value...)
	}
	stream = binary.BigEndian.AppendUint16(stream, ^uint16(0))

	stripped, err := stripBinaryCopyFields(stream, []int{0}, 3)
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	if !bytes.Contains(stripped, []byte{0xde, 0xad, 0xbe, 0xef}) {
		t.Fatal("header extension bytes must survive the rewrite")
	}
}

func TestStripBinaryCopyFieldsRejectsMalformedStreams(t *testing.T) {
	tests := []struct {
		name  string
		build func() []byte
	}{
		{"wrong signature", func() []byte {
			stream := buildBinaryCopyStream(stripFieldsTestTuple)
			stream[0] = 'X'
			return stream
		}},
		{"truncated header", func() []byte {
			return []byte(pgcopySignature[:8])
		}},
		{"header extension overruns", func() []byte {
			stream := buildBinaryCopyStream()
			binary.BigEndian.PutUint32(stream[len(pgcopySignature)+4:], 1000)
			return stream
		}},
		{"tuple cut short", func() []byte {
			stream := buildBinaryCopyStream(stripFieldsTestTuple)
			return stream[:len(stream)-2] // lose the end-of-data trailer
		}},
		{"field payload cut short", func() []byte {
			stream := buildBinaryCopyStream([]field{[]byte("abcdef")})
			return stream[:len(stream)-3]
		}},
		{"trailing bytes", func() []byte {
			return append(buildBinaryCopyStream(stripFieldsTestTuple), 0x00)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := stripBinaryCopyFields(test.build(), []int{0}, 1); err == nil {
				t.Fatal("expected the malformed stream to be rejected")
			}
		})
	}
}

func TestStripBinaryCopyFieldsRejectsWrongTupleWidth(t *testing.T) {
	stream := buildBinaryCopyStream(stripFieldsTestTuple)
	if _, err := stripBinaryCopyFields(stream, []int{0}, 4); err == nil {
		t.Fatal("a tuple narrower than the manifest declares must be rejected")
	}
}
