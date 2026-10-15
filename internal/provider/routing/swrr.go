package routing

import "sync"

// weightedRoundRobin is a smooth weighted round-robin scheduler (the nginx
// algorithm). It yields a starting member index per call such that, over time,
// member i is chosen in proportion to weights[i] while selections stay
// interleaved rather than bursty. Equal weights degrade to plain round-robin.
//
// The running totals are shared across concurrent callers, so the tiny
// selection critical section is mutex-guarded. Concurrency may interleave the
// selections, which only affects distribution, never correctness.
type weightedRoundRobin struct {
	mu      sync.Mutex
	weights []int
	current []int
	total   int
}

// newWeightedRoundRobin copies the weights so a caller mutating its slice
// cannot corrupt the running schedule.
func newWeightedRoundRobin(weights []int) *weightedRoundRobin {
	w := make([]int, len(weights))
	copy(w, weights)
	total := 0
	for _, weight := range w {
		total += weight
	}
	return &weightedRoundRobin{weights: w, current: make([]int, len(w)), total: total}
}

// next returns the member to start on for the current call.
func (s *weightedRoundRobin) next() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	best := 0
	for i, weight := range s.weights {
		s.current[i] += weight
		if s.current[i] > s.current[best] {
			best = i
		}
	}
	s.current[best] -= s.total
	return best
}
