package httpx

import "strings"

// BigintSign reports whether text is an integer literal the reference's
// framework accepts as a Python int but that does not fit int64, and the
// literal's sign (+1 or -1). It mirrors the framework's grammar — surrounding
// whitespace, an optional sign, digits with single underscores between them, and
// a fractional part only when every one of its digits is zero — so a caller can
// decide which bound an unrepresentable value is outside of, rather than
// mistaking it for a value the parser refuses.
func BigintSign(text string) (int, bool) {
	trimmed := strings.TrimSpace(text)
	sign := 1
	switch {
	case strings.HasPrefix(trimmed, "+"):
		trimmed = trimmed[1:]
	case strings.HasPrefix(trimmed, "-"):
		sign = -1
		trimmed = trimmed[1:]
	}
	whole := trimmed
	if dot := strings.IndexByte(trimmed, '.'); dot >= 0 {
		whole = trimmed[:dot]
		fraction := trimmed[dot+1:]
		if fraction == "" {
			return 0, false
		}
		for i := range len(fraction) {
			if fraction[i] != '0' {
				return 0, false
			}
		}
	}
	if whole == "" {
		return 0, false
	}
	digits := 0
	for i := range len(whole) {
		switch {
		case whole[i] >= '0' && whole[i] <= '9':
			digits++
		case whole[i] == '_':
			if digits == 0 || i+1 == len(whole) || whole[i+1] == '_' {
				return 0, false
			}
		default:
			return 0, false
		}
	}
	if digits == 0 {
		return 0, false
	}
	return sign, true
}
