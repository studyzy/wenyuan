package routing

import (
	"context"
	"errors"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
)

// fakeProvider is a scripted member: it records every call and returns its
// queued errors in order, then its content. The last error repeats so an
// always-failing member is expressible.
type fakeProvider struct {
	name    string
	content string
	dims    int

	// errs is consumed one per Complete call; a nil entry means success.
	errs []error

	completeCalls int
	embedCalls    int
	verifyCalls   int
	verifyErr     error

	// order, when shared across members, records the call sequence.
	order *[]string
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Complete(_ context.Context, _ provider.CompletionRequest) (provider.CompletionResult, error) {
	f.completeCalls++
	if f.order != nil {
		*f.order = append(*f.order, f.name)
	}
	if len(f.errs) > 0 {
		err := f.errs[0]
		if len(f.errs) > 1 {
			f.errs = f.errs[1:]
		}
		if err != nil {
			return provider.CompletionResult{}, err
		}
	}
	return provider.CompletionResult{Content: f.content, Model: f.name}, nil
}

func (f *fakeProvider) Embed(_ context.Context, req provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	f.embedCalls++
	return provider.EmbeddingResult{Vectors: make([][]float32, len(req.Texts)), Dimensions: f.dims}, nil
}

func (f *fakeProvider) VerifyConnection(context.Context) error {
	f.verifyCalls++
	return f.verifyErr
}

func (f *fakeProvider) Dimensions() int { return f.dims }

func completeReq() provider.CompletionRequest {
	return provider.CompletionRequest{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}}
}

func TestNewRejectsEmptyChain(t *testing.T) {
	if _, err := New(nil, Strategy{Mode: ModeFailover}); err == nil {
		t.Fatal("expected an empty chain to be rejected")
	}
}

func TestNewRejectsUnknownMode(t *testing.T) {
	if _, err := New([]provider.Provider{&fakeProvider{name: "a"}}, Strategy{Mode: "nonsense"}); err == nil {
		t.Fatal("expected an unknown mode to be rejected")
	}
}

func TestNewRejectsMismatchedWeights(t *testing.T) {
	members := []provider.Provider{&fakeProvider{name: "a"}, &fakeProvider{name: "b"}}
	if _, err := New(members, Strategy{Mode: ModeRoundRobin, Weights: []int{1, 2, 3}}); err == nil {
		t.Fatal("expected mismatched weights to be rejected")
	}
}

func TestNewRejectsOutOfRangeMetadataRoute(t *testing.T) {
	members := []provider.Provider{&fakeProvider{name: "a"}, &fakeProvider{name: "b"}}
	_, err := New(members, Strategy{Mode: ModeMetadata, Routes: []Route{{Key: "k", Value: "v", Member: 2}}})
	if err == nil {
		t.Fatal("expected an out-of-range route member to be rejected")
	}
}

func TestNewUnsetStrategyReturnsPrimary(t *testing.T) {
	primary := &fakeProvider{name: "a"}
	got, err := New([]provider.Provider{primary, &fakeProvider{name: "b"}}, Strategy{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got != primary {
		t.Fatalf("unset strategy should return the primary unchanged, got %T", got)
	}
}

func TestFailoverAdvancesOnTransientError(t *testing.T) {
	var order []string
	primary := &fakeProvider{name: "primary", errs: []error{&provider.TransientError{Cause: errors.New("boom")}}, order: &order}
	secondary := &fakeProvider{name: "secondary", content: "from secondary", order: &order}

	chain, err := New([]provider.Provider{primary, secondary}, Strategy{Mode: ModeFailover})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	result, err := chain.Complete(context.Background(), completeReq())
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if result.Content != "from secondary" {
		t.Fatalf("content = %q, want secondary's", result.Content)
	}
	if primary.completeCalls != 1 || secondary.completeCalls != 1 {
		t.Fatalf("calls = primary %d, secondary %d; want 1 and 1", primary.completeCalls, secondary.completeCalls)
	}
	if len(order) != 2 || order[0] != "primary" || order[1] != "secondary" {
		t.Fatalf("call order = %v, want [primary secondary]", order)
	}
}

func TestFailoverStopsOnOutputTooLong(t *testing.T) {
	want := &provider.OutputTooLongError{FinishReason: "length"}
	primary := &fakeProvider{name: "primary", errs: []error{want}}
	secondary := &fakeProvider{name: "secondary", content: "never"}

	chain, err := New([]provider.Provider{primary, secondary}, Strategy{Mode: ModeFailover})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = chain.Complete(context.Background(), completeReq())
	if !provider.IsOutputTooLong(err) {
		t.Fatalf("err = %v, want an output-too-long error", err)
	}
	if secondary.completeCalls != 0 {
		t.Fatalf("secondary calls = %d, want 0", secondary.completeCalls)
	}
}

func TestFailoverStopsOnCanceledContext(t *testing.T) {
	primary := &fakeProvider{name: "primary", errs: []error{context.Canceled}}
	secondary := &fakeProvider{name: "secondary", content: "never"}

	chain, err := New([]provider.Provider{primary, secondary}, Strategy{Mode: ModeFailover})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = chain.Complete(context.Background(), completeReq())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if secondary.completeCalls != 0 {
		t.Fatalf("secondary calls = %d, want 0", secondary.completeCalls)
	}
}

func TestFailoverSurfacesLastError(t *testing.T) {
	last := &provider.TransientError{Cause: errors.New("last")}
	primary := &fakeProvider{name: "primary", errs: []error{errors.New("first")}}
	secondary := &fakeProvider{name: "secondary", errs: []error{last}}

	chain, err := New([]provider.Provider{primary, secondary}, Strategy{Mode: ModeFailover})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = chain.Complete(context.Background(), completeReq())
	if !errors.Is(err, last.Cause) {
		t.Fatalf("err = %v, want the last member's error", err)
	}
}

// TestRoundRobinSWRRSequence pins the exact starting-member sequence a smooth
// weighted round-robin produces for weights [3,1]: member 0 six times and
// member 1 twice out of every eight calls.
func TestRoundRobinSWRRSequence(t *testing.T) {
	members := []provider.Provider{
		&fakeProvider{name: "a", content: "a"},
		&fakeProvider{name: "b", content: "b"},
	}
	chain, err := New(members, Strategy{Mode: ModeRoundRobin, Weights: []int{3, 1}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	counts := map[string]int{}
	sequence := make([]string, 0, 8)
	for range 8 {
		result, err := chain.Complete(context.Background(), completeReq())
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		counts[result.Content]++
		sequence = append(sequence, result.Content)
	}
	if counts["a"] != 6 || counts["b"] != 2 {
		t.Fatalf("distribution = %v (sequence %v), want a=6 b=2", counts, sequence)
	}
}

func TestRoundRobinFallsThroughOnError(t *testing.T) {
	primary := &fakeProvider{name: "a", errs: []error{&provider.TransientError{Cause: errors.New("boom")}}}
	secondary := &fakeProvider{name: "b", content: "b"}

	chain, err := New([]provider.Provider{primary, secondary}, Strategy{Mode: ModeRoundRobin})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	result, err := chain.Complete(context.Background(), completeReq())
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if result.Content != "b" {
		t.Fatalf("content = %q, want b", result.Content)
	}
	if primary.completeCalls != 1 || secondary.completeCalls != 1 {
		t.Fatalf("calls = primary %d, secondary %d; want 1 and 1", primary.completeCalls, secondary.completeCalls)
	}
}

func TestMetadataRoutesByListItem(t *testing.T) {
	primary := &fakeProvider{name: "a", content: "a"}
	secondary := &fakeProvider{name: "b", content: "b"}

	chain, err := New([]provider.Provider{primary, secondary}, Strategy{
		Mode:   ModeMetadata,
		Routes: []Route{{Key: "labels", Value: "financial", Member: 1}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	routed, ok := chain.(*Provider)
	if !ok {
		t.Fatalf("chain is %T, want *Provider", chain)
	}
	member, matched := routed.MemberForMetadata(map[string]any{"labels": []any{"pii", "financial"}})
	if !matched || member != provider.Provider(secondary) {
		t.Fatalf("matched=%v member=%v, want the secondary", matched, member.Name())
	}

	member, matched = routed.MemberForMetadata(map[string]any{"labels": []any{"pii"}})
	if matched || member != provider.Provider(primary) {
		t.Fatalf("unmatched item matched=%v member=%v, want the primary with false", matched, member.Name())
	}
}

func TestMetadataBoolAndDictNeverMatch(t *testing.T) {
	primary := &fakeProvider{name: "a"}
	secondary := &fakeProvider{name: "b"}
	chain, err := New([]provider.Provider{primary, secondary}, Strategy{
		Mode:   ModeMetadata,
		Routes: []Route{{Key: "flag", Value: "true", Member: 1}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	routed := chain.(*Provider)
	if member, matched := routed.MemberForMetadata(map[string]any{"flag": true}); !matched || member != provider.Provider(secondary) {
		t.Fatalf("bool true should route to the secondary")
	}
	if _, matched := routed.MemberForMetadata(map[string]any{"flag": map[string]any{"a": 1}}); matched {
		t.Fatal("a dict value must never match")
	}
	if _, matched := routed.MemberForMetadata(map[string]any{"flag": nil}); matched {
		t.Fatal("a nil value must never match")
	}
}

func TestMetadataChainCallsStayOnPrimary(t *testing.T) {
	primary := &fakeProvider{name: "a", content: "a"}
	secondary := &fakeProvider{name: "b", content: "b"}
	chain, err := New([]provider.Provider{primary, secondary}, Strategy{
		Mode:   ModeMetadata,
		Routes: []Route{{Key: "k", Value: "v", Member: 1}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	result, err := chain.Complete(context.Background(), completeReq())
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if result.Content != "a" || secondary.completeCalls != 0 {
		t.Fatalf("metadata chain must stay on the primary; content=%q secondary calls=%d", result.Content, secondary.completeCalls)
	}
}

// TestNoChainLevelRetry proves the chain tries each member exactly once per
// request: a member holding its own retry budget must be the only retrier.
func TestNoChainLevelRetry(t *testing.T) {
	primary := &fakeProvider{name: "a", errs: []error{&provider.TransientError{Cause: errors.New("boom")}}}
	secondary := &fakeProvider{name: "b", content: "b"}
	chain, err := New([]provider.Provider{primary, secondary}, Strategy{Mode: ModeFailover})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for range 3 {
		if _, err := chain.Complete(context.Background(), completeReq()); err != nil {
			t.Fatalf("Complete: %v", err)
		}
	}
	// The always-failing primary keeps its single scripted error, so each of
	// the three requests must have called it exactly once.
	if primary.completeCalls != 3 || secondary.completeCalls != 3 {
		t.Fatalf("calls = primary %d, secondary %d; want exactly 3 each", primary.completeCalls, secondary.completeCalls)
	}
}

func TestEmbedDimensionsAndRerankDelegateToPrimary(t *testing.T) {
	primary := &fakeProvider{name: "a", dims: 7}
	secondary := &fakeProvider{name: "b", dims: 99}
	chain, err := New([]provider.Provider{primary, secondary}, Strategy{Mode: ModeFailover})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if chain.Dimensions() != 7 {
		t.Fatalf("Dimensions = %d, want the primary's 7", chain.Dimensions())
	}
	embed, err := chain.Embed(context.Background(), provider.EmbeddingRequest{Texts: []string{"x"}})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if embed.Dimensions != 7 || primary.embedCalls != 1 || secondary.embedCalls != 0 {
		t.Fatalf("embed delegated wrong: dims=%d primary=%d secondary=%d", embed.Dimensions, primary.embedCalls, secondary.embedCalls)
	}
	if reranker := provider.ProviderReranker(chain); reranker != nil {
		t.Fatalf("ProviderReranker = %v, want nil when the primary is not a reranker", reranker)
	}
}

func TestVerifyConnectionPrimaryStrictSecondarySoft(t *testing.T) {
	primary := &fakeProvider{name: "a"}
	secondary := &fakeProvider{name: "b", verifyErr: errors.New("unreachable")}
	chain, err := New([]provider.Provider{primary, secondary}, Strategy{Mode: ModeFailover})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := chain.VerifyConnection(context.Background()); err != nil {
		t.Fatalf("an unreachable secondary must not fail verification: %v", err)
	}
	if primary.verifyCalls != 1 || secondary.verifyCalls != 1 {
		t.Fatalf("verify calls = primary %d, secondary %d; want 1 and 1", primary.verifyCalls, secondary.verifyCalls)
	}

	primary.verifyErr = errors.New("primary down")
	if err := chain.VerifyConnection(context.Background()); err == nil {
		t.Fatal("a failing primary must surface its error")
	}
}

func TestNameIsPrimaryName(t *testing.T) {
	chain, err := New([]provider.Provider{
		&fakeProvider{name: "primary"},
		&fakeProvider{name: "secondary"},
	}, Strategy{Mode: ModeFailover})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if chain.Name() != "primary" {
		t.Fatalf("Name = %q, want primary", chain.Name())
	}
}
