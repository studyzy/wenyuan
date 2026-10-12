package bankapi

import (
	"encoding/json"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// configPatch converts a configuration override map into a storage patch. Keys
// may be given in internal field form or environment-variable form; only the
// hierarchical per-bank fields may be overridden.
func configPatch(updates map[string]any) (storage.BankPatch, *httpx.ValidationFailure) {
	var failure *httpx.ValidationFailure
	patch := storage.BankPatch{}
	var items []httpx.ValidationError

	for key, raw := range updates {
		switch normalizeConfigKey(key) {
		case "disposition_skepticism":
			if value, ok := asInt(raw); ok {
				patch.DispositionSkepticism = &value
			} else {
				items = append(items, invalidConfigItem(key))
			}
		case "disposition_literalism":
			if value, ok := asInt(raw); ok {
				patch.DispositionLiteralism = &value
			} else {
				items = append(items, invalidConfigItem(key))
			}
		case "disposition_empathy":
			if value, ok := asInt(raw); ok {
				patch.DispositionEmpathy = &value
			} else {
				items = append(items, invalidConfigItem(key))
			}
		case "reflect_mission":
			if value, ok := raw.(string); ok {
				patch.ReflectMission = &value
			} else {
				items = append(items, invalidConfigItem(key))
			}
		default:
			items = append(items, httpx.ValidationError{
				Loc:   []string{"body", "updates", key},
				Msg:   "field is not an overridable bank configuration key",
				Type:  "value_error.unknown",
				Input: raw,
			})
		}
	}
	if len(items) > 0 {
		failure = &httpx.ValidationFailure{Errors: items}
	}
	return patch, failure
}

// normalizeConfigKey maps an environment-variable-form key to its internal
// field form, leaving an already-internal key unchanged.
func normalizeConfigKey(key string) string {
	upper := strings.ToUpper(key)
	const prefix = "HINDSIGHT_"
	if !strings.HasPrefix(upper, prefix) {
		return key
	}
	trimmed := strings.TrimPrefix(upper, prefix)
	switch trimmed {
	case "DISPOSITION_SKEPTICISM", "BANK_DISPOSITION_SKEPTICISM":
		return "disposition_skepticism"
	case "DISPOSITION_LITERALISM", "BANK_DISPOSITION_LITERALISM":
		return "disposition_literalism"
	case "DISPOSITION_EMPATHY", "BANK_DISPOSITION_EMPATHY":
		return "disposition_empathy"
	case "REFLECT_MISSION", "BANK_REFLECT_MISSION":
		return "reflect_mission"
	default:
		return key
	}
}

func asInt(raw any) (int, bool) {
	switch value := raw.(type) {
	case int:
		return value, true
	case float64:
		return int(value), float64(int(value)) == value
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	default:
		return 0, false
	}
}

func invalidConfigItem(key string) httpx.ValidationError {
	return httpx.ValidationError{
		Loc:   []string{"body", "updates", key},
		Msg:   "value has the wrong type for this bank configuration key",
		Type:  "value_error.type",
		Input: key,
	}
}
