package webhooks

// DefaultGlobalEventType is the event the server-level target receives when no
// filter is configured, matching the reference's built-in default.
const DefaultGlobalEventType = "consolidation.completed"

// GlobalTarget is the deployment-level webhook target. Every bank's events are
// delivered to it in addition to that bank's own registrations, independent of
// them: it is one static receiver named by the deployment rather than a row a
// bank owns, so it has no identifier and no stored configuration.
//
// An empty URL disables it. A nil *GlobalTarget behaves the same, so a
// deployment that never sets the key cannot deliver anywhere by accident.
type GlobalTarget struct {
	// URL is the receiver. Empty disables the global deliveries.
	URL string
	// Secret signs the deliveries, with the same HMAC-SHA256 scheme a
	// per-bank webhook uses.
	Secret string
	// EventTypes filters which events are delivered. Empty selects the
	// reference's default set rather than "everything": an unset filter must
	// not turn into a firehose.
	EventTypes []string
	// ExposeResponseBody records the receiver's response body on the delivery.
	// It is off by default because a response body can carry data the
	// deployment would not otherwise surface to API callers.
	ExposeResponseBody bool
}

// Enabled reports whether the target has a receiver. A nil target is disabled,
// so callers never need a nil check before asking.
func (t *GlobalTarget) Enabled() bool { return t != nil && t.URL != "" }

// Subscribes reports whether an event type passes the target's filter. An empty
// filter uses the reference's default event, not an all-events wildcard.
func (t *GlobalTarget) Subscribes(eventType string) bool {
	if t == nil {
		return false
	}
	if len(t.EventTypes) == 0 {
		return eventType == DefaultGlobalEventType
	}
	for _, subscribed := range t.EventTypes {
		if subscribed == eventType {
			return true
		}
	}
	return false
}

// ExposesResponseBody reports whether deliveries record the receiver's response
// body. A nil target never does. The setting is read even when the target has
// no URL, so a deployment that exposes response bodies keeps them on the
// per-bank deliveries too.
func (t *GlobalTarget) ExposesResponseBody() bool {
	return t != nil && t.ExposeResponseBody
}
