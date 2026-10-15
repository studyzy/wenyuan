package routing

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/provider"
)

// MemberForMetadata returns the member selected by a retain item's metadata.
//
// The bool reports whether a route matched: it is false when the chain is not
// in metadata mode, when metadata is empty, or when no route matches, in which
// case the primary is returned so the caller's existing binding is already
// right. The first route in declared order wins, so overlapping routes are
// resolved by configuration order rather than rejected.
func (p *Provider) MemberForMetadata(metadata map[string]any) (provider.Provider, bool) {
	if p.strategy.Mode != ModeMetadata || len(metadata) == 0 {
		return p.members[0], false
	}
	for _, route := range p.strategy.Routes {
		if metadataMatches(metadata[route.Key], route.Value) {
			return p.members[route.Member], true
		}
	}
	return p.members[0], false
}

// metadataMatches reports whether a retain item's metadata value matches a
// route's value. Retain metadata is free-form JSON while a route value is
// always a string, so comparison is on the string form. A list or array value
// matches when any entry does, which is what makes {"labels": ["pii", "eu"]}
// routable. A nil or object value never matches.
func metadataMatches(actual any, expected string) bool {
	if actual == nil {
		return false
	}
	rv := reflect.ValueOf(actual)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		for i := range rv.Len() {
			if metadataMatches(rv.Index(i).Interface(), expected) {
				return true
			}
		}
		return false
	case reflect.Map:
		return false
	case reflect.Bool:
		// JSON booleans should match "true"/"false" whatever their case.
		return strings.EqualFold(strconv.FormatBool(rv.Bool()), expected)
	}
	return fmt.Sprintf("%v", actual) == expected
}
