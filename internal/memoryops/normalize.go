package memoryops

// DropNullValues removes keys whose value is JSON null so the stored metadata
// always matches its declared mapping type rather than poisoning later reads.
func DropNullValues(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		if value == nil {
			continue
		}
		out[key] = value
	}
	return out
}

// IsValidUUIDShape reports whether a value has the canonical 8-4-4-4-12 hex
// identifier shape. An empty value is not valid.
func IsValidUUIDShape(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !isHexDigit(byte(r)) {
				return false
			}
		}
	}
	return true
}

func isHexDigit(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}
