package tenant

import "strings"

// RelaxJSON removes the two relaxations a chat model writes into JSON it was
// asked to emit strictly: comments and a trailing comma before a closing
// bracket. The answer they decorate is the answer, so a caller that fails the
// strict decode reads the relaxed text instead of spending the round.
func RelaxJSON(text string) string { return stripTrailingCommas(stripJSONComments(text)) }

// stripJSONComments removes // and /* */ comments, which JSON does not allow
// but a chat model writes anyway. A // or /* sequence inside a quoted string is
// left alone.
func stripJSONComments(text string) string {
	var out strings.Builder
	out.Grow(len(text))
	inString := false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if inString {
			out.WriteByte(c)
			if c == '\\' && i+1 < len(text) {
				i++
				out.WriteByte(text[i])
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
			out.WriteByte(c)
		case c == '/' && i+1 < len(text) && text[i+1] == '/':
			for i < len(text) && text[i] != '\n' {
				i++
			}
			if i < len(text) {
				out.WriteByte(text[i])
			}
		case c == '/' && i+1 < len(text) && text[i+1] == '*':
			i += 2
			for i+1 < len(text) && (text[i] != '*' || text[i+1] != '/') {
				i++
			}
			i++
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

// stripTrailingCommas drops a comma that no value follows inside an array or
// object.
func stripTrailingCommas(text string) string {
	// Which bytes sit inside a string, so a comma that is text is not read as
	// punctuation.
	inside := make([]bool, len(text))
	inString := false
	escaped := false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if inString {
			inside[i] = true
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inside[i] = true
			inString = true
		}
	}
	var out strings.Builder
	out.Grow(len(text))
	for i := 0; i < len(text); i++ {
		if text[i] == ',' && !inside[i] {
			j := i + 1
			for j < len(text) && (text[j] == ' ' || text[j] == '\t' || text[j] == '\r' || text[j] == '\n') {
				j++
			}
			if j < len(text) && (text[j] == ']' || text[j] == '}') {
				continue
			}
		}
		out.WriteByte(text[i])
	}
	return out.String()
}
