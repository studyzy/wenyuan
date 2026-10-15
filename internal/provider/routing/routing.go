// Package routing builds a multi-member chat provider: an ordered chain of
// provider.Provider members plus a strategy that decides the order they are
// tried in. It mirrors the reference's multi-provider so a deployment that
// configured a multi-member LLM strategy routes across members the same way.
package routing

import (
	"context"
	"errors"
	"fmt"

	"github.com/studyzy/wenyuan/internal/provider"
)

// Strategy modes, matching the reference's LLM_STRATEGY_* constants.
const (
	ModeFailover   = "failover"
	ModeRoundRobin = "round-robin"
	ModeMetadata   = "metadata"
)

// Route sends a retain item whose metadata[key] equals Value to Member.
type Route struct {
	// Key is the metadata field to match.
	Key string
	// Value is the expected string form of the field.
	Value string
	// Member is the chain index the matching item routes to.
	Member int
}

// Strategy is the resolved multi-member routing policy.
type Strategy struct {
	// Mode is one of failover, round-robin, metadata.
	Mode string
	// Weights is the round-robin weighting, one entry per member; nil means
	// uniform.
	Weights []int
	// Routes is the metadata routing table (metadata mode only).
	Routes []Route
}

// New wraps an ordered chain of members (index 0 is the primary) in a provider
// that routes per the strategy. A chain of one member, or an unset strategy,
// returns the primary unchanged.
func New(members []provider.Provider, strategy Strategy) (provider.Provider, error) {
	if len(members) == 0 {
		return nil, fmt.Errorf("routing: a chain needs at least one member")
	}
	if strategy.Mode == "" {
		// No strategy configured: nothing to route, so the primary serves
		// every call directly.
		return members[0], nil
	}
	switch strategy.Mode {
	case ModeFailover, ModeRoundRobin, ModeMetadata:
	default:
		return nil, fmt.Errorf("routing: invalid strategy mode %q; must be one of %s, %s, %s",
			strategy.Mode, ModeFailover, ModeRoundRobin, ModeMetadata)
	}
	if strategy.Weights != nil && len(strategy.Weights) != len(members) {
		return nil, fmt.Errorf("routing: strategy weights has %d entries but the chain has %d members; they must match",
			len(strategy.Weights), len(members))
	}
	if strategy.Mode == ModeMetadata {
		for _, route := range strategy.Routes {
			if route.Member < 0 || route.Member >= len(members) {
				return nil, fmt.Errorf("routing: metadata route %s=%q selects member %d, but the chain has members 0..%d",
					route.Key, route.Value, route.Member, len(members)-1)
			}
		}
	}
	if len(members) == 1 {
		return members[0], nil
	}

	weights := strategy.Weights
	if weights == nil {
		weights = make([]int, len(members))
		for i := range len(members) {
			weights[i] = 1
		}
	}
	return &Provider{
		members:  members,
		strategy: strategy,
		sched:    newWeightedRoundRobin(weights),
	}, nil
}

// Provider is the multi-member chain. It implements provider.Provider by
// picking a member per call; Embed and Dimensions always resolve to the primary
// because only completion calls carry a routing decision.
type Provider struct {
	members  []provider.Provider
	strategy Strategy
	sched    *weightedRoundRobin
}

// Ensure the chain satisfies the boundary.
var _ provider.Provider = (*Provider)(nil)

// Name reports the primary member's name.
func (p *Provider) Name() string { return p.members[0].Name() }

// Complete dispatches one completion to the first member whose attempt
// succeeds. Each member owns its own retry budget, so the chain itself never
// retries: it tries a member once and either returns its result or advances.
func (p *Provider) Complete(ctx context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	order := p.memberOrder()
	var lastErr error
	for _, idx := range order {
		result, err := p.members[idx].Complete(ctx, req)
		if err == nil {
			return result, nil
		}
		if !shouldFailover(err) {
			return provider.CompletionResult{}, err
		}
		lastErr = err
	}
	return provider.CompletionResult{}, lastErr
}

// Embed delegates to the primary: embeddings have no failover semantic, so the
// chain must not spread vectors across members that guarantee different widths.
func (p *Provider) Embed(ctx context.Context, req provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return p.members[0].Embed(ctx, req)
}

// Dimensions reports the primary's vector width, matching Embed.
func (p *Provider) Dimensions() int { return p.members[0].Dimensions() }

// VerifyConnection verifies the primary strictly and every other member softly:
// an unreachable failover member must not block startup when it may recover
// before it is needed.
func (p *Provider) VerifyConnection(ctx context.Context) error {
	if err := p.members[0].VerifyConnection(ctx); err != nil {
		return err
	}
	for _, member := range p.members[1:] {
		_ = member.VerifyConnection(ctx)
	}
	return nil
}

// RerankCapability delegates to the primary's reranking capability so wrapping
// a reranker in a chain does not hide it from callers.
func (p *Provider) RerankCapability() provider.Reranker {
	return provider.ProviderReranker(p.members[0])
}

// memberOrder returns the member indices to try, in order, for one call.
func (p *Provider) memberOrder() []int {
	n := len(p.members)
	if p.strategy.Mode == ModeMetadata {
		// A metadata member is chosen per item, not per chain call. Anything
		// reaching the chain has no item to route on, so it stays on the
		// primary and never fails over into another item's lane.
		return []int{0}
	}
	if p.strategy.Mode == ModeFailover {
		order := make([]int, n)
		for i := range n {
			order[i] = i
		}
		return order
	}
	start := p.sched.next()
	order := make([]int, n)
	for i := range n {
		order[i] = (start + i) % n
	}
	return order
}

// shouldFailover reports whether an error from one member justifies trying the
// next. An output-limit error propagates: another member would refuse the same
// over-length output, so retrying it cannot help. A canceled context is the
// caller's own decision and must not be masked by a member switch.
func shouldFailover(err error) bool {
	if provider.IsOutputTooLong(err) {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	return true
}
