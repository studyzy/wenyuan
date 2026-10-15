package cli

import (
	"encoding/json"
	"fmt"
	"strings"
)

// decodeSentinel prefixes a response-payload decode failure. When an error
// text starts with it, classification is forced to the decode branch no
// matter what the quoted response body mentions.
const decodeSentinel = "Invalid response payload"

// APIErrorKind enumerates the error classes in classification order; the
// first match wins.
type APIErrorKind int

const (
	KindConnection APIErrorKind = iota
	KindBadRequest
	KindTimeout
	KindDNS
	KindNotFound
	KindAuth
	KindPermission
	KindServer
	KindInvalidURL
	KindDecode
	KindOther
)

// ClassifiedError is a typed API error whose message carries the full
// diagnostic rendering. main prints it verbatim and exits 1.
type ClassifiedError struct {
	Kind    APIErrorKind
	apiURL  string
	text    string // the full error text
	heading string // the heading candidate after splitting
	body    string // the body part after a valid split, "" when none
	detail  string // the extracted server detail, "" when none
}

// Classify inspects an error's text against the configured API URL and
// returns the typed rendering. The error text may come from any layer; the
// API client produces the shapes the split logic is tuned for.
func Classify(err error, apiURL string) *ClassifiedError {
	text := err.Error()
	c := &ClassifiedError{apiURL: apiURL, text: text}
	if strings.HasPrefix(text, decodeSentinel) {
		c.Kind = KindDecode
		c.heading = text
		return c
	}
	c.heading = text
	if idx := strings.Index(text, "): "); idx >= 0 {
		// A "): " sequence makes the text before it the heading candidate.
		// The part after it only becomes the body when the candidate's last
		// parenthetical is digit-led; a parenthetical like "(not a status)"
		// means the text is not an HTTP failure at all, so the body is
		// dropped and the candidate alone is classified.
		candidate := text[:idx]
		c.heading = candidate
		if statusStartsDigit(candidate) {
			c.body = text[idx+len("): "):]
			c.detail = extractDetail(c.body)
		}
	}
	c.Kind = classifyHeading(c.heading)
	return c
}

// statusStartsDigit reports whether the substring after the last '(' of the
// heading candidate starts with an ASCII digit, which makes the split a
// valid status shape. A parenthetical whose status is not digit-led is not a
// split.
func statusStartsDigit(heading string) bool {
	last := strings.LastIndex(heading, "(")
	if last < 0 || last+1 >= len(heading) {
		return false
	}
	return heading[last+1] >= '0' && heading[last+1] <= '9'
}

// classifyHeading matches the heading against the class markers in order.
// The patterns cover both the reference error shapes and the ones Go's
// net/http produces for the same underlying failures.
func classifyHeading(heading string) APIErrorKind {
	switch {
	case containsAny(heading, "Connection refused", "connection refused", "tcp connect error", "error sending request"):
		return KindConnection
	case strings.Contains(heading, "400 Bad Request") || strings.Contains(heading, "(400)"):
		return KindBadRequest
	case strings.Contains(heading, "timeout") || strings.Contains(heading, "Timeout"):
		return KindTimeout
	case containsAny(heading, "dns", "DNS", "failed to lookup", "no such host", "name resolution"):
		return KindDNS
	case strings.Contains(heading, "404"):
		return KindNotFound
	case strings.Contains(heading, "401"):
		return KindAuth
	case strings.Contains(heading, "403"):
		return KindPermission
	case containsAny(heading, "500", "502", "503"):
		return KindServer
	case containsAny(heading, "invalid URL", "InvalidUri"):
		return KindInvalidURL
	case strings.Contains(heading, "Failed to parse") || strings.Contains(heading, "error decoding") || heading == decodeSentinel:
		return KindDecode
	}
	return KindOther
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// extractDetail pulls the server detail out of a response body: a body that
// is not a JSON object surfaces verbatim; a JSON object's top-level detail
// field wins, serialized when it is not a string; a body without one falls
// back to the raw text. No nested traversal.
func extractDetail(body string) string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		return trimmed
	}
	v, ok := obj["detail"]
	if !ok {
		return trimmed
	}
	if s, ok := v.(string); ok {
		return s
	}
	data, err := json.Marshal(v)
	if err != nil {
		return trimmed
	}
	return string(data)
}

// Error renders the full diagnostic message: the title, the API URL block,
// then the class-specific sections.
func (e *ClassifiedError) Error() string {
	var b strings.Builder
	b.WriteString(e.title())
	b.WriteString("\n\n")
	_, _ = fmt.Fprintf(&b, "API URL: %s\n", e.apiURL)
	e.appendSections(&b)
	return strings.TrimRight(b.String(), "\n")
}

func (e *ClassifiedError) title() string {
	switch e.Kind {
	case KindConnection:
		return "Cannot connect to Wenyuan API"
	case KindBadRequest:
		return "Request rejected (400)"
	case KindTimeout:
		return "Request timed out"
	case KindDNS:
		return "Cannot resolve API hostname"
	case KindNotFound:
		switch {
		case strings.Contains(e.text, "Bank configuration API is disabled"):
			return "Bank configuration API is disabled"
		case e.detail != "":
			return fmt.Sprintf("Not found (404): %s", e.detail)
		default:
			return "API endpoint not found (404)"
		}
	case KindAuth:
		return "Authentication failed"
	case KindPermission:
		return "Permission denied (403)"
	case KindServer:
		return "API server error"
	case KindInvalidURL:
		return "Invalid API URL"
	case KindDecode:
		return "Invalid API response format"
	default:
		return "API request failed"
	}
}

func (e *ClassifiedError) appendSections(b *strings.Builder) {
	switch e.Kind {
	case KindConnection:
		causes(b, "The Wenyuan API server is not running",
			"The server is running on a different address than "+e.apiURL,
			"A firewall is blocking the connection")
		tries(b, "Start the Wenyuan API server and ensure it's accessible")
	case KindBadRequest:
		detail := e.detail
		if detail == "" {
			detail = e.text
		}
		block(b, "Server response:", detail)
	case KindTimeout:
		causes(b, "The API server is slow to respond", "Network latency is too high")
		tries(b, "Check if the API server is healthy", "Try again with a better network connection")
	case KindDNS:
		causes(b, "The hostname in the API URL is incorrect", "DNS server is not responding")
		tries(b, "Check the HINDSIGHT_API_URL environment variable")
	case KindNotFound:
		e.appendNotFound(b)
	case KindAuth:
		causes(b, "API requires authentication", "Invalid or missing credentials")
		tries(b, "Check if the API requires an API key or token")
		e.serverResponse(b)
	case KindPermission:
		causes(b, "This operation is not allowed", "The feature may be disabled on the server")
		tries(b, "Check server configuration or contact your administrator")
		e.serverResponse(b)
	case KindServer:
		label := "Service temporarily unavailable"
		if strings.Contains(e.heading, "500") {
			label = "Internal server error (500)"
		}
		block(b, "The server encountered an error:", "  - "+label)
		tries(b, "Check the API server logs for details", "Try again in a few moments")
		e.serverResponse(b)
	case KindInvalidURL:
		b.WriteString("\nThe API URL format is invalid.\n")
		b.WriteString("Ensure it starts with http:// or https://\n")
		block(b, "Example:", "export HINDSIGHT_API_URL=http://localhost:8888")
	case KindDecode:
		e.appendDecode(b)
	default:
		block(b, "Error:", e.text)
		tries(b,
			"Check that HINDSIGHT_API_URL is set correctly",
			"Ensure the API server is running",
			"Verify network connectivity to the API server")
	}
}

func (e *ClassifiedError) appendNotFound(b *strings.Builder) {
	if strings.Contains(e.text, "Bank configuration API is disabled") {
		b.WriteString("\nThis feature has been disabled on the server.\n")
		b.WriteString("To enable, set HINDSIGHT_API_ENABLE_BANK_CONFIG_API=true on the API server\n")
		block(b, "Note:", "This allows per-bank LLM configuration overrides via API")
		return
	}
	// A detail-bearing 404 is self-explanatory: the title carries the detail
	// and only the API URL block follows.
	if e.detail != "" {
		return
	}
	causes(b, "The API endpoint path has changed", "You're using an incompatible API version")
	tries(b, "Check that you're using the correct API version")
}

func (e *ClassifiedError) appendDecode(b *strings.Builder) {
	reason, raw, ok := splitDecodeText(e.text)
	if ok {
		block(b, "Decoder error:", reason)
	}
	causes(b,
		"The API returned an unexpected response format",
		"Version mismatch between CLI and API",
		"The API endpoint doesn't exist or returned HTML instead of JSON")
	tries(b,
		"Run with --verbose flag to see the full request/response",
		"Ensure you're using a compatible API version")
	if ok {
		block(b, "Actual response:", raw)
	}
}

// serverResponse appends the optional server detail block; absent detail
// means absent block.
func (e *ClassifiedError) serverResponse(b *strings.Builder) {
	if e.detail == "" {
		return
	}
	block(b, "Server response:", e.detail)
}

// splitDecodeText pulls the decoder reason and the raw response out of the
// sentinel-shaped text.
func splitDecodeText(text string) (reason, raw string, ok bool) {
	rest, found := strings.CutPrefix(text, decodeSentinel+": ")
	if !found {
		return "", "", false
	}
	raw = rest
	reason = rest
	if idx := strings.Index(rest, "\nResponse was: "); idx >= 0 {
		reason = rest[:idx]
		raw = rest[idx+len("\nResponse was: "):]
	}
	return reason, raw, true
}

// block writes one labeled section: a blank line, the label, then the body.
func block(b *strings.Builder, label, body string) {
	_, _ = fmt.Fprintf(b, "\n%s\n%s\n", label, body)
}

func causes(b *strings.Builder, items ...string) {
	writeBullets(b, "Possible causes:", items)
}

func tries(b *strings.Builder, items ...string) {
	writeBullets(b, "Try:", items)
}

func writeBullets(b *strings.Builder, label string, items []string) {
	b.WriteString("\n" + label + "\n")
	for _, item := range items {
		_, _ = fmt.Fprintf(b, "  - %s\n", item)
	}
}
