package memoryops

import (
	"strings"
	"testing"
)

// A secret-shaped token is replaced by its typed placeholder, and the matched
// type is reported once even when the token appears twice.
func TestApplyRedactionRedactsSecrets(t *testing.T) {
	secret := "ghp_" + strings.Repeat("a", 36)
	content := "token " + secret + " and again " + secret
	result := applyRedaction(content)

	if strings.Contains(result.Content, secret) {
		t.Fatalf("raw secret survived redaction: %q", result.Content)
	}
	if want := "[REDACTED:github_token]"; !strings.Contains(result.Content, want) {
		t.Fatalf("redacted content %q missing %q", result.Content, want)
	}
	if len(result.MatchedTypes) != 1 || result.MatchedTypes[0] != "github_token" {
		t.Fatalf("matched types = %v, want [github_token]", result.MatchedTypes)
	}
	if len(result.Hits) != 2 {
		t.Fatalf("hits = %v, want two entries", result.Hits)
	}
	for _, hit := range result.Hits {
		if hit.Detector != "github_token" {
			t.Fatalf("hit detector = %q", hit.Detector)
		}
		if strings.Contains(hit.Preview, secret) || hit.Preview == secret {
			t.Fatalf("hit preview leaks the raw secret: %q", hit.Preview)
		}
	}
}

// A candidate that fails the Luhn check is left alone; a valid card is redacted.
func TestApplyRedactionCardLuhn(t *testing.T) {
	invalid := "4111 1111 1111 1112"
	valid := "5500 0000 0000 0004"
	result := applyRedaction("bad " + invalid + " good " + valid)

	if !strings.Contains(result.Content, invalid) {
		t.Fatalf("a Luhn-invalid candidate must not be redacted: %q", result.Content)
	}
	if strings.Contains(result.Content, valid) {
		t.Fatalf("a Luhn-valid card must be redacted: %q", result.Content)
	}
	if !strings.Contains(result.Content, "[REDACTED:credit_card]") {
		t.Fatalf("redacted content missing card placeholder: %q", result.Content)
	}
	for _, hit := range result.Hits {
		if strings.Contains(hit.Preview, valid) {
			t.Fatalf("card preview leaks the raw candidate: %q", hit.Preview)
		}
	}
}

// A card-shaped run inside a UUID is not redacted as a card.
func TestOverlapsUUIDRejectsCandidateInsideUUID(t *testing.T) {
	content := "id 550e8400-e29b-41d4-a716-446655440000 end"
	start := strings.Index(content, "550e8400")
	end := start + len("550e8400-e29b-41d4-a716-446655440000")
	if !overlapsUUID(content, start, end) {
		t.Fatal("a candidate spanning the UUID must be reported as overlapping")
	}
	if overlapsUUID(content, 0, len("id")) {
		t.Fatal("a candidate far from the UUID must not overlap it")
	}
}

// Fingerprints shorten a value and never reproduce it.
func TestFingerprintValue(t *testing.T) {
	cases := []struct {
		value string
		want  string
	}{
		{"abc", "[redacted]"},
		{"abcdef", "ab...ef"},
		{"abcdefghijklmno", "ab...no"},
		{"ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "ghp_...aaaa"},
	}
	for _, tc := range cases {
		if got := fingerprintValue(tc.value); got != tc.want {
			t.Fatalf("fingerprintValue(%q) = %q, want %q", tc.value, got, tc.want)
		}
	}
}

// A disabled policy, a missing rule, and an allow rule all approve untouched.
func TestScreenContentPolicyGates(t *testing.T) {
	secret := "ghp_" + strings.Repeat("a", 36)
	redact := DefensePolicy{Enabled: true, Rules: []PolicyRule{{On: "sensitive_data", Action: DefenseActionRedact}}}

	if got := screenContent(DefensePolicy{}, secret); got.Action != DefenseActionAllow {
		t.Fatalf("disabled policy = %v", got.Action)
	}
	if got := screenContent(DefensePolicy{Enabled: true}, secret); got.Action != DefenseActionAllow {
		t.Fatalf("policy with no rule = %v", got.Action)
	}
	allow := DefensePolicy{Enabled: true, Rules: []PolicyRule{{On: "sensitive_data", Action: DefenseActionAllow}}}
	if got := screenContent(allow, secret); got.Action != DefenseActionAllow {
		t.Fatalf("allow rule = %v", got.Action)
	}
	got := screenContent(redact, secret)
	if got.Action != DefenseActionRedact {
		t.Fatalf("redact rule = %v", got.Action)
	}
	if got.Detector != sensitiveDataDetector || !strings.Contains(got.RedactedContent, "[REDACTED:github_token]") {
		t.Fatalf("unexpected redact decision: %+v", got)
	}
	if got := screenContent(redact, "ordinary prose"); got.Action != DefenseActionAllow {
		t.Fatalf("no match = %v", got.Action)
	}
}

// Policy parsing accepts the valid shapes and rejects an invalid rule.
func TestParseDefensePolicy(t *testing.T) {
	if policy, err := ParseDefensePolicy(nil); err != nil || policy.Enabled {
		t.Fatalf("nil policy = %+v, %v", policy, err)
	}
	policy, err := ParseDefensePolicy(map[string]any{
		"enabled": true,
		"rules":   []any{map[string]any{"on": "sensitive_data", "action": "block"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Enabled || len(policy.Rules) != 1 || policy.Rules[0].Action != DefenseActionBlock {
		t.Fatalf("parsed policy = %+v", policy)
	}

	bad := []map[string]any{
		{"enabled": true, "rules": []any{map[string]any{"action": "redact"}}},
		{"enabled": true, "rules": []any{map[string]any{"on": "", "action": "redact"}}},
		{"enabled": true, "rules": []any{map[string]any{"on": "sensitive_data", "action": "nope"}}},
		{"enabled": "yes"},
	}
	for _, raw := range bad {
		if _, err := ParseDefensePolicy(raw); err == nil {
			t.Fatalf("policy %+v must be rejected", raw)
		}
	}
}
