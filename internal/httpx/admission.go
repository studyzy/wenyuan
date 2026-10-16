package httpx

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// AdmissionRule bounds how many requests for one path suffix may run at once.
type AdmissionRule struct {
	// Method is the HTTP method the rule applies to. Empty matches any method,
	// which is only right for a suffix that identifies one route.
	Method string
	// Suffix is the trailing route fragment the rule applies to, such as
	// "/memories/recall".
	Suffix string
	// Lane is the name the shed message reports, so a caller can tell which
	// limit it hit.
	Lane string
	// Limit is how many requests under that suffix may be in flight together.
	Limit int
	// MaxWait is how long a request for this lane may wait for a slot before it
	// is shed. Zero takes AdmissionConfig.MaxWait, which is what a lane with no
	// configured wait uses; the reference sets a different wait per lane
	// because a recall and a retain are not equally worth queuing for.
	MaxWait time.Duration
}

// AdmissionConfig bounds the concurrency of the expensive operations, so a
// burst cannot exhaust a process's memory or connections. A request that waits
// past MaxWait is shed with 503 and a Retry-After; a request that goes away
// while waiting is reported as 499.
type AdmissionConfig struct {
	Rules             []AdmissionRule
	MaxWait           time.Duration
	RetryAfterSeconds int
}

// AdmissionMiddleware bounds per-operation concurrency. Each rule owns a
// semaphore; a request whose path does not match any rule passes straight
// through.
func AdmissionMiddleware(cfg AdmissionConfig) Middleware {
	lanes := make([]struct {
		rule AdmissionRule
		slot chan struct{}
	}, 0, len(cfg.Rules))
	for _, rule := range cfg.Rules {
		if rule.Limit <= 0 {
			continue
		}
		lanes = append(lanes, struct {
			rule AdmissionRule
			slot chan struct{}
		}{rule: rule, slot: make(chan struct{}, rule.Limit)})
	}
	retryAfter := cfg.RetryAfterSeconds
	if retryAfter <= 0 {
		retryAfter = 1
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, lane := range lanes {
				if lane.rule.Method != "" && lane.rule.Method != r.Method {
					continue
				}
				if !hasSuffix(r.URL.Path, lane.rule.Suffix) {
					continue
				}
				wait := lane.rule.MaxWait
				if wait <= 0 {
					wait = cfg.MaxWait
				}
				if !admit(w, r, lane, wait, retryAfter) {
					return
				}
				defer func() { <-lane.slot }()
				break
			}
			next.ServeHTTP(w, r)
		})
	}
}

// admit acquires a slot, waiting up to maxWait. It writes the failure response
// and reports false when the request cannot proceed.
func admit(w http.ResponseWriter, r *http.Request, lane struct {
	rule AdmissionRule
	slot chan struct{}
}, maxWait time.Duration, retryAfter int) bool {
	select {
	case lane.slot <- struct{}{}:
		return true
	default:
	}
	timer := time.NewTimer(maxWait)
	defer timer.Stop()
	select {
	case lane.slot <- struct{}{}:
		return true
	case <-r.Context().Done():
		// The client went away before a slot freed; 499 records that rather
		// than attributing it to the server. The wording matches the
		// reference's, which reports the same condition.
		writeStatusOnly(w, 499, "client disconnected while queued")
		return false
	case <-timer.C:
		WriteError(w, NewHTTPError(KindUnavailable, shedDetail(lane.rule, lane.rule.Limit)).
			WithHeader(HeaderRetryAfter, itoa(retryAfter)), EncodeOptions{})
		return false
	}
}

// shedDetail is the message a request shed by the lane's limit receives, naming
// the lane and its limit the way the reference does.
func shedDetail(rule AdmissionRule, limit int) string {
	lane := rule.Lane
	if lane == "" {
		lane = strings.Trim(rule.Suffix, "/")
	}
	return fmt.Sprintf("Server is at capacity for '%s' (%d concurrent); try again shortly.", lane, limit)
}

// writeStatusOnly writes a minimal body for a status the shared envelope does
// not model (499).
func writeStatusOnly(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"detail":"` + detail + `"}`))
}

// hasSuffix reports whether path ends with suffix at a path boundary.
func hasSuffix(path, suffix string) bool {
	if len(suffix) == 0 || len(path) < len(suffix) {
		return false
	}
	return path[len(path)-len(suffix):] == suffix
}

// itoa renders a small non-negative integer without importing strconv into the
// hot path of every middleware.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	neg := value < 0
	if neg {
		value = -value
	}
	var buf [20]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
