package storage

import "fmt"

// claimOptions carries the adjustable claim behaviour behind the variadic
// ClaimOption arguments.
type claimOptions struct {
	maxReclaims     int
	hasReclaimLimit bool
}

// ClaimOption adjusts one claim call.
type ClaimOption func(*claimOptions)

// WithReclaimLimit turns on lease-expiry recovery counting, the claim-time form
// of the reference's _reclaim_own_processing_tasks (worker/poller.py:1496-1565):
// a claim that takes a 'processing' row whose lease expired increments its
// retry_count, and a row whose retry_count has already reached limit is failed
// instead — "exceeded max recovery attempts (retry_count >= N)" — rather than
// being run yet again. Without it a claim makes no recovery counting.
func WithReclaimLimit(limit int) ClaimOption {
	return func(o *claimOptions) {
		// The reference never carries a negative recovery budget.
		if limit < 0 {
			limit = 0
		}
		o.hasReclaimLimit = true
		o.maxReclaims = limit
	}
}

// applyClaimOptions folds the variadic options into one set.
func applyClaimOptions(opts []ClaimOption) claimOptions {
	var options claimOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	return options
}

// reclaimExhaustedMessage is the failure reason a row carries once its
// recovery budget is spent (worker/poller.py:1531-1536).
func reclaimExhaustedMessage(limit int) string {
	return fmt.Sprintf("exceeded max recovery attempts (retry_count >= %d)", limit)
}
