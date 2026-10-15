package memoryops

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/studyzy/wenyuan/internal/storage"
)

// DefenseAction is the disposition a Memory Defense rule applies to a match.
type DefenseAction string

// The three dispositions a rule may carry.
const (
	DefenseActionAllow  DefenseAction = "allow"
	DefenseActionRedact DefenseAction = "redact"
	DefenseActionBlock  DefenseAction = "block"
)

// sensitiveDataDetector is the only detector the built-in regex defense runs.
// A rule naming another detector is a silent no-op here, because entitlement
// and dispatch belong to whichever extension is loaded.
const sensitiveDataDetector = "sensitive_data"

var validDefenseActions = map[DefenseAction]struct{}{
	DefenseActionAllow:  {},
	DefenseActionRedact: {},
	DefenseActionBlock:  {},
}

// PolicyRule pairs a detector name with the action to take when it matches.
type PolicyRule struct {
	On     string
	Action DefenseAction
}

// DefensePolicy is a bank's Memory Defense configuration. The zero value is
// disabled, so a bank that configures nothing screens nothing.
type DefensePolicy struct {
	Enabled bool
	Rules   []PolicyRule
}

// sensitiveDataRule returns the rule for the sensitive_data detector, if any.
func (p DefensePolicy) sensitiveDataRule() (PolicyRule, bool) {
	for _, rule := range p.Rules {
		if rule.On == sensitiveDataDetector {
			return rule, true
		}
	}
	return PolicyRule{}, false
}

// ParseDefensePolicy parses the raw memory_defense bank-config value. A nil
// value is the disabled default. A rule whose on is missing/empty or whose
// action is unknown is a validation error, which the config API surfaces as
// 422; the parser does not pin on against a fixed roster, matching upstream.
func ParseDefensePolicy(raw any) (DefensePolicy, error) {
	if raw == nil {
		return DefensePolicy{}, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return DefensePolicy{}, errors.New("memory_defense must be an object")
	}

	policy := DefensePolicy{}
	if value, present := obj["enabled"]; present {
		enabled, ok := value.(bool)
		if !ok {
			return DefensePolicy{}, errors.New("memory_defense.enabled must be a boolean")
		}
		policy.Enabled = enabled
	}

	rawRules, present := obj["rules"]
	if !present || rawRules == nil {
		return policy, nil
	}
	rules, ok := rawRules.([]any)
	if !ok {
		return DefensePolicy{}, errors.New("memory_defense.rules must be a list")
	}
	for _, entry := range rules {
		item, ok := entry.(map[string]any)
		if !ok {
			return DefensePolicy{}, errors.New("memory_defense rule must be an object")
		}
		on, ok := item["on"].(string)
		if !ok || on == "" {
			return DefensePolicy{}, errors.New("memory_defense rule on must be a non-empty string")
		}
		action, ok := item["action"].(string)
		if !ok {
			return DefensePolicy{}, errors.New("memory_defense rule action is required")
		}
		if _, ok := validDefenseActions[DefenseAction(action)]; !ok {
			return DefensePolicy{}, errors.New("memory_defense rule action must be one of allow, redact, block")
		}
		policy.Rules = append(policy.Rules, PolicyRule{On: on, Action: DefenseAction(action)})
	}
	return policy, nil
}

// DefenseHit is one fingerprinted match. Preview is a length-aware redaction
// of the matched value; the raw secret never appears here.
type DefenseHit struct {
	Detector string `json:"detector"`
	Preview  string `json:"preview"`
}

// DefenseDecision is the outcome of screening one item under a policy.
type DefenseDecision struct {
	Action DefenseAction
	// Detector names the inner detector that matched; empty on allow.
	Detector string
	// Message is a human-readable summary of the matched types.
	Message string
	// RedactedContent is set only for a redact decision.
	RedactedContent string
	// MatchedTypes are the pattern labels that fired, de-duplicated.
	MatchedTypes []string
	// Hits are per-match fingerprinted previews.
	Hits []DefenseHit
}

// RedactionResult is the outcome of scrubbing one string.
type RedactionResult struct {
	Content      string
	MatchedTypes []string
	Hits         []DefenseHit
}

// screenContent applies a policy to one item's text. A disabled policy, a
// policy with no sensitive_data rule, or an allow rule all approve the item
// untouched.
func screenContent(policy DefensePolicy, content string) DefenseDecision {
	if !policy.Enabled {
		return DefenseDecision{Action: DefenseActionAllow}
	}
	rule, ok := policy.sensitiveDataRule()
	if !ok || rule.Action == DefenseActionAllow {
		return DefenseDecision{Action: DefenseActionAllow}
	}
	result := applyRedaction(content)
	if len(result.MatchedTypes) == 0 {
		return DefenseDecision{Action: DefenseActionAllow}
	}
	decision := DefenseDecision{
		Action:       rule.Action,
		Detector:     sensitiveDataDetector,
		Message:      "Sensitive data pattern matched: " + strings.Join(result.MatchedTypes, ", "),
		MatchedTypes: result.MatchedTypes,
		Hits:         result.Hits,
	}
	if rule.Action == DefenseActionRedact {
		decision.RedactedContent = result.Content
	}
	return decision
}

// redactionPattern is one compiled detector in the catalog.
type redactionPattern struct {
	label string
	re    *regexp.Regexp
}

// asciiToken bounds a token body on the ASCII word alphabet. Go's \b is an
// ASCII word boundary, which is what upstream's lookarounds emulate: a secret
// butted against CJK prose still matches, and a partial ASCII token does not.
func asciiToken(body string) string {
	return `\b` + body + `\b`
}

// redactionCatalog mirrors the upstream _REDACTION_PATTERNS catalog. Order
// matters: more-specific prefixes run before broader ones so a broad pattern
// cannot consume part of a token a narrow one owns.
var redactionCatalog = []redactionPattern{
	// --- AI / LLM providers ---
	{"anthropic_key", regexp.MustCompile(asciiToken(`sk-ant-[A-Za-z0-9_-]{20,}`))},
	{"openai_project_key", regexp.MustCompile(asciiToken(`sk-proj-[A-Za-z0-9_-]{48,}`))},
	{"openai_admin_key", regexp.MustCompile(asciiToken(`sk-admin-[A-Za-z0-9_-]{40,}`))},
	{"openai_key", regexp.MustCompile(asciiToken(`sk-[A-Za-z0-9_-]{20,}`))},
	{"google_api_key", regexp.MustCompile(asciiToken(`AIza[0-9A-Za-z_-]{35}`))},
	{"google_oauth_token", regexp.MustCompile(asciiToken(`ya29\.[0-9A-Za-z_-]{20,}`))},
	{"xai_key", regexp.MustCompile(asciiToken(`xai-[A-Za-z0-9]{40,}`))},
	{"groq_key", regexp.MustCompile(asciiToken(`gsk_[A-Za-z0-9]{20,}`))},
	// Hindsight's own API keys: hsk_<32 hex>, optionally _<hex signature>;
	// hsk_sys_ marks a system key, anchored on the 32 hex characters.
	{"hindsight_key", regexp.MustCompile(asciiToken(`hsk_(?:sys_)?[0-9a-f]{32}(?:_[0-9a-f]{8,})?`))},
	{"huggingface_token", regexp.MustCompile(asciiToken(`hf_[A-Za-z0-9]{30,}`))},
	{"replicate_token", regexp.MustCompile(asciiToken(`r8_[A-Za-z0-9]{30,}`))},
	{"perplexity_key", regexp.MustCompile(asciiToken(`pplx-[A-Za-z0-9]{40,}`))},
	{"databricks_token", regexp.MustCompile(asciiToken(`dapi[A-Za-z0-9]{32}`))},
	// --- Cloud providers ---
	{"aws_access_key", regexp.MustCompile(asciiToken(`AKIA[0-9A-Z]{16}`))},
	{"aws_session_token", regexp.MustCompile(asciiToken(`ASIA[0-9A-Z]{16}`))},
	{"aws_secret_key", regexp.MustCompile(`(?i)aws(.{0,20})?(secret|private)?[\s_-]?access[\s_-]?key[\s_-]?[:=][\s"']*([A-Za-z0-9/+=]{40})`)},
	{"digitalocean_token", regexp.MustCompile(asciiToken(`dop_v1_[a-f0-9]{64}`))},
	// --- Source control & CI ---
	{"github_fg_pat", regexp.MustCompile(asciiToken(`github_pat_[A-Za-z0-9_]{60,}`))},
	{"github_token", regexp.MustCompile(asciiToken(`ghp_[A-Za-z0-9]{36}`))},
	{"github_app_token", regexp.MustCompile(asciiToken(`ghs_[A-Za-z0-9]{36}`))},
	{"github_user_token", regexp.MustCompile(asciiToken(`ghu_[A-Za-z0-9]{36}`))},
	{"github_refresh", regexp.MustCompile(asciiToken(`ghr_[A-Za-z0-9]{36}`))},
	{"github_oauth", regexp.MustCompile(asciiToken(`gho_[A-Za-z0-9]{36}`))},
	{"gitlab_pat", regexp.MustCompile(asciiToken(`glpat-[A-Za-z0-9_-]{20,}`))},
	{"npm_token", regexp.MustCompile(asciiToken(`npm_[A-Za-z0-9]{30,}`))},
	{"pypi_token", regexp.MustCompile(asciiToken(`pypi-AgEIcHlwaS5vcmc[A-Za-z0-9_-]{20,}`))},
	// --- Payment processors ---
	{"stripe_secret", regexp.MustCompile(asciiToken(`sk_(?:live|test)_[A-Za-z0-9]{20,}`))},
	{"stripe_restricted", regexp.MustCompile(asciiToken(`rk_(?:live|test)_[A-Za-z0-9]{20,}`))},
	{"square_token", regexp.MustCompile(asciiToken(`sq0[a-z]{3}-[A-Za-z0-9_-]{22,}`))},
	{"braintree_token", regexp.MustCompile(asciiToken(`access_token\$production\$[a-z0-9]{16}\$[a-f0-9]{32}`))},
	// --- Communication / email ---
	{"slack_token", regexp.MustCompile(asciiToken(`xox[abpr]-[0-9A-Za-z-]{10,}`))},
	{"slack_webhook", regexp.MustCompile(`https://hooks\.slack\.com/services/T[A-Za-z0-9_]{8,}/B[A-Za-z0-9_]{8,}/[A-Za-z0-9_]{20,}`)},
	{"twilio_api_key", regexp.MustCompile(asciiToken(`SK[0-9a-fA-F]{32}`))},
	{"twilio_account_sid", regexp.MustCompile(asciiToken(`AC[0-9a-fA-F]{32}`))},
	{"sendgrid_key", regexp.MustCompile(asciiToken(`SG\.[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43}`))},
	{"mailgun_key", regexp.MustCompile(asciiToken(`key-[A-Za-z0-9]{32}`))},
	{"discord_bot", regexp.MustCompile(asciiToken(`[MNO][A-Za-z0-9]{23}\.[A-Za-z0-9_-]{6}\.[A-Za-z0-9_-]{27}`))},
	{"telegram_bot", regexp.MustCompile(asciiToken(`[0-9]{8,10}:[A-Za-z0-9_-]{35}`))},
	// --- Commerce ---
	{"shopify_token", regexp.MustCompile(asciiToken(`shpat_[a-fA-F0-9]{32}`))},
	// --- Database connection strings (creds embedded in URL) ---
	{"db_url_postgres", regexp.MustCompile(`postgres(?:ql)?://[^\s:/@]+:[^\s/@]+@[^\s]+`)},
	{"db_url_mysql", regexp.MustCompile(`mysql://[^\s:/@]+:[^\s/@]+@[^\s]+`)},
	{"db_url_mongodb", regexp.MustCompile(`mongodb(?:\+srv)?://[^\s:/@]+:[^\s/@]+@[^\s]+`)},
	// --- Private keys & generic credentials ---
	{"private_key_pem", regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH |PGP )?PRIVATE KEY( BLOCK)?-----`)},
	{"jwt", regexp.MustCompile(asciiToken(`eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`))},
	// --- PII ---
	// Card candidates need a Luhn check and a UUID-overlap check below; the
	// digit shape alone redacted timestamps and port lists as if they were
	// cards. Go has no lookaround, so \b stands in for the ASCII boundaries.
	{"credit_card", creditCardPattern},
	{"ssn_us", regexp.MustCompile(asciiToken(`\d{3}-\d{2}-\d{4}`))},
}

// creditCardPattern is the digit shape of a card candidate, before the Luhn and
// UUID checks that decide whether it is one.
var creditCardPattern = regexp.MustCompile(`\b(?:\d{4}[ -]?){3}\d{1,4}\b`)

var uuidPattern = regexp.MustCompile(asciiToken(`[0-9a-fA-F]{8}-(?:[0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12}`))

// applyRedaction scrubs known secret/PII patterns from content, replacing each
// match with [REDACTED:type]. It returns the rewritten content alongside the
// matched labels (de-duplicated, in catalog first-match order) and one
// fingerprinted hit per match. The raw matched value never leaves in a hit.
func applyRedaction(content string) RedactionResult {
	result := RedactionResult{Content: content}
	for _, pattern := range redactionCatalog {
		if pattern.label == "credit_card" {
			var hits []DefenseHit
			content, hits = redactCreditCards(content)
			if len(hits) == 0 {
				continue
			}
			result.MatchedTypes = append(result.MatchedTypes, "credit_card")
			result.Hits = append(result.Hits, hits...)
			continue
		}
		matches := pattern.re.FindAllStringSubmatch(content, -1)
		if len(matches) == 0 {
			continue
		}
		result.MatchedTypes = append(result.MatchedTypes, pattern.label)
		for _, match := range matches {
			raw := canonicalMatch(match)
			if raw == "" {
				continue
			}
			result.Hits = append(result.Hits, DefenseHit{Detector: pattern.label, Preview: fingerprintValue(raw)})
		}
		content = pattern.re.ReplaceAllString(content, "[REDACTED:"+pattern.label+"]")
	}
	result.Content = content
	return result
}

// canonicalMatch picks the matched value the catalog intends to fingerprint.
// A pattern with no capture groups returns the whole match; one with groups
// returns the longest non-empty group, mirroring upstream's findall coercion.
func canonicalMatch(match []string) string {
	if len(match) <= 1 {
		return match[0]
	}
	longest := ""
	for _, group := range match[1:] {
		if len(group) > len(longest) {
			longest = group
		}
	}
	return longest
}

// redactCreditCards replaces only card candidates that pass Luhn and do not
// overlap a UUID. A rejected candidate must not consume a later valid start
// (e.g. "2026 4111 1111 1111 1111"), so the search resumes one character past
// a rejected span rather than past its end.
func redactCreditCards(content string) (string, []DefenseHit) {
	card := creditCardPattern
	var hits []DefenseHit
	var builder strings.Builder
	copiedUntil := 0
	searchStart := 0
	for {
		loc := card.FindStringIndex(content[searchStart:])
		if loc == nil {
			break
		}
		start := searchStart + loc[0]
		end := searchStart + loc[1]
		searchStart = start + 1
		value := content[start:end]
		if !isLuhnCard(value) {
			continue
		}
		if overlapsUUID(content, start, end) {
			continue
		}
		hits = append(hits, DefenseHit{Detector: "credit_card", Preview: fingerprintValue(value)})
		builder.WriteString(content[copiedUntil:start])
		builder.WriteString("[REDACTED:credit_card]")
		copiedUntil = end
		searchStart = end
	}
	if len(hits) == 0 {
		return content, nil
	}
	builder.WriteString(content[copiedUntil:])
	return builder.String(), hits
}

// isLuhnCard validates a digit candidate already bounded by the card pattern.
// Repeated-digit placeholders (including all zeroes) can pass Luhn, so they are
// rejected outright; digits are read ignoring the space/hyphen separators.
func isLuhnCard(value string) bool {
	digits := make([]int, 0, len(value))
	seen := map[int]struct{}{}
	for _, char := range value {
		if char == ' ' || char == '-' {
			continue
		}
		digits = append(digits, int(char-'0'))
		seen[int(char-'0')] = struct{}{}
	}
	if len(seen) <= 1 {
		return false
	}
	checksum := 0
	for index := range len(digits) {
		digit := digits[len(digits)-1-index]
		if index%2 == 1 {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		checksum += digit
	}
	return checksum%10 == 0
}

// overlapsUUID reports whether a card candidate overlaps a UUID anywhere in the
// content. A UUID's numeric segments can pass Luhn, so it must not be redacted
// as a card.
func overlapsUUID(content string, start, end int) bool {
	for _, loc := range uuidPattern.FindAllStringIndex(content, -1) {
		if loc[0] < end && loc[1] > start {
			return true
		}
	}
	return false
}

// fingerprintValue returns a length-aware, redaction-identifiable preview of a
// matched value. It is always a strict shortening, so the raw secret never
// appears in it.
func fingerprintValue(value string) string {
	n := len(value)
	if n < 6 {
		return "[redacted]"
	}
	if n <= 15 {
		return value[:2] + "..." + value[n-2:]
	}
	return value[:4] + "..." + value[n-4:]
}

// applyMemoryDefense screens each item before extraction under the bank's
// Memory Defense policy and rewrites write.Items in place: a redact decision
// replaces the item text, a block decision drops the item, and each non-allow
// decision enqueues a webhook delivery. A bank with no policy (or a disabled
// one, or no sensitive_data rule) is untouched, so an unconfigured bank
// retains exactly as it did before.
//
// A blocked item is dropped from the write. There is no existing response
// field that reports the drop, so the caller is not told through the response;
// the decision is recorded in the queued memory_defense.triggered delivery.
func (h *Handlers) applyMemoryDefense(ctx context.Context, bankID string, bank storage.Bank, items []RetainItem) ([]RetainItem, error) {
	policy, err := ParseDefensePolicy(bank.Config["memory_defense"])
	if err != nil || !policy.Enabled {
		return items, nil
	}
	rule, ok := policy.sensitiveDataRule()
	if !ok || rule.Action == DefenseActionAllow {
		return items, nil
	}
	// The items screened are the ones the caller submitted, before any folding:
	// the reference decides "every item was blocked" against the submitted count
	// and reports each violation by its position in that list.
	submitted := len(items)
	kept := make([]RetainItem, 0, submitted)
	violations := make([]MemoryDefenseViolation, 0)
	for index, item := range items {
		decision := screenContent(policy, item.Content.Flatten())
		switch decision.Action {
		case DefenseActionRedact:
			item.Content = Content{Text: decision.RedactedContent}
			h.notifyMemoryDefense(ctx, bankID, item.DocumentID, decision)
		case DefenseActionBlock:
			h.notifyMemoryDefense(ctx, bankID, item.DocumentID, decision)
			violations = append(violations, MemoryDefenseViolation{
				Index: index, Detector: decision.Detector, Message: decision.Message,
			})
			continue
		}
		kept = append(kept, item)
	}
	// A batch the policy blocked in full is refused rather than accepted as an
	// empty write: the caller asked for something to be remembered and nothing
	// was, which is a client error about their content, not a successful no-op.
	// A partial block proceeds with the survivors, which the caller sees as a
	// smaller result.
	if len(violations) == submitted {
		return nil, &MemoryDefenseAllBlockedError{Violations: violations}
	}
	return kept, nil
}

// MemoryDefenseViolation is one item a Memory Defense rule blocked, in the
// shape the reference reports it in: the item's position in the submitted
// batch, the detector that matched, and its message.
type MemoryDefenseViolation struct {
	Index    int    `json:"index"`
	Detector string `json:"detector"`
	Message  string `json:"message"`
}

// MemoryDefenseAllBlockedError reports that every item in a batch was blocked.
// It is a client error (the reference answers 422 with these violations).
type MemoryDefenseAllBlockedError struct {
	Violations []MemoryDefenseViolation
}

func (e *MemoryDefenseAllBlockedError) Error() string {
	return fmt.Sprintf("all %d items blocked by Memory Defense policy", len(e.Violations))
}

// notifyMemoryDefense enqueues one memory_defense.triggered delivery. The
// payload mirrors the upstream MemoryDefenseEventData field names; a nil
// notifier (no webhooks configured) makes it a no-op.
func (h *Handlers) notifyMemoryDefense(ctx context.Context, bankID, documentID string, decision DefenseDecision) {
	if h.Webhooks == nil {
		return
	}
	hits := make([]map[string]any, 0, len(decision.Hits))
	for _, hit := range decision.Hits {
		hits = append(hits, map[string]any{"detector": hit.Detector, "preview": hit.Preview})
	}
	h.Webhooks.EnqueueWebhookEvent(ctx, bankID, "memory_defense.triggered", map[string]any{
		"status":        string(decision.Action),
		"action":        string(decision.Action),
		"detector":      decision.Detector,
		"document_id":   documentID,
		"matched_types": decision.MatchedTypes,
		"message":       decision.Message,
		"hits":          hits,
	})
}
