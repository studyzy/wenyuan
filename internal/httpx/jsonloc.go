package httpx

import (
	"strconv"
	"strings"
)

// locForOffset reconstructs the path to the JSON value that spans byte
// `offset`, inserting an index for every array level it passes through.
//
// The Go decoder reports a wrong type as a `json.UnmarshalTypeError` whose
// Field is the struct path with dots but no indices ("strategies.scopes.tags"),
// while the framework words the same failure with an index for each list level
// (["body","strategies",0,"scopes",0,"tags",1]). The indices are not in the
// error, so they are recovered by walking the raw body and keeping the path of
// the value whose span covers the offset.
//
// field is the error's dotted path. The scan's key names must match it, or the
// caller's loc is left alone rather than guessed.
func locForOffset(body []byte, field string, offset int64) ([]string, bool) {
	if field == "" || offset <= 0 || offset > int64(len(body)) {
		return nil, false
	}
	scan := &jsonLocScanner{body: body, offset: offset}
	scan.value(nil)
	if scan.path == nil {
		return nil, false
	}
	if !keysMatchField(scan.path, field) {
		return nil, false
	}
	return scan.path, true
}

// keysMatchField reports whether the named entries of a scanned path are the
// dotted segments of the decoder's field path, in order. A mismatch means the
// scan landed on a different value than the error names, so its indices must
// not be trusted.
func keysMatchField(path []string, field string) bool {
	segments := strings.Split(field, ".")
	seen := 0
	for _, entry := range path {
		if isIndex(entry) {
			continue
		}
		if seen >= len(segments) || segments[seen] != entry {
			return false
		}
		seen++
	}
	return seen == len(segments)
}

// isIndex reports whether a path entry is an array index rather than a key.
func isIndex(entry string) bool {
	if entry == "" {
		return false
	}
	for _, r := range entry {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// jsonLocScanner walks a JSON document, remembering the path to the value whose
// byte span covers the target offset.
type jsonLocScanner struct {
	body   []byte
	i      int
	offset int64
	path   []string
}

// value consumes one JSON value, recording the path when the value's span
// covers the target offset.
func (s *jsonLocScanner) value(path []string) {
	s.ws()
	if s.i >= len(s.body) {
		return
	}
	start := s.i
	switch s.body[s.i] {
	case '{':
		s.i++
		for {
			s.ws()
			if s.i >= len(s.body) || s.body[s.i] == '}' {
				s.i++
				return
			}
			if s.body[s.i] != '"' {
				// A malformed member: stop rather than guess.
				return
			}
			key := s.str()
			s.ws()
			if s.i < len(s.body) && s.body[s.i] == ':' {
				s.i++
			}
			s.value(append(append([]string{}, path...), key))
			s.ws()
			if s.i < len(s.body) && s.body[s.i] == ',' {
				s.i++
				continue
			}
			if s.i < len(s.body) && s.body[s.i] == '}' {
				s.i++
			}
			return
		}
	case '[':
		s.i++
		index := 0
		for {
			s.ws()
			if s.i >= len(s.body) || s.body[s.i] == ']' {
				s.i++
				return
			}
			s.value(append(append([]string{}, path...), strconv.Itoa(index)))
			index++
			s.ws()
			if s.i < len(s.body) && s.body[s.i] == ',' {
				s.i++
				continue
			}
			if s.i < len(s.body) && s.body[s.i] == ']' {
				s.i++
			}
			return
		}
	case '"':
		s.str()
	default:
		for s.i < len(s.body) {
			switch s.body[s.i] {
			case ',', '}', ']', ' ', '\t', '\n', '\r':
				goto done
			}
			s.i++
		}
	}
done:
	if int64(s.i) >= s.offset && int64(start) < s.offset {
		s.path = path
	}
}

// ws skips insignificant whitespace.
func (s *jsonLocScanner) ws() {
	for s.i < len(s.body) {
		switch s.body[s.i] {
		case ' ', '\t', '\n', '\r':
			s.i++
		default:
			return
		}
	}
}

// str consumes a JSON string, returning its decoded content.
func (s *jsonLocScanner) str() string {
	// The opening quote.
	s.i++
	var out strings.Builder
	for s.i < len(s.body) {
		c := s.body[s.i]
		if c == '\\' {
			s.i++
			if s.i < len(s.body) {
				out.WriteByte(s.body[s.i])
				s.i++
			}
			continue
		}
		if c == '"' {
			s.i++
			return out.String()
		}
		out.WriteByte(c)
		s.i++
	}
	return out.String()
}
