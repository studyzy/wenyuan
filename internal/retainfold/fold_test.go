package retainfold

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

func appendItem(content string) storage.RetainItem {
	return storage.RetainItem{Content: content, DocumentID: "doc-1", UpdateMode: "append"}
}

func member(id string, items ...storage.RetainItem) Member {
	return Member{OperationID: id, Items: items}
}

func itemsTokensOf(items ...storage.RetainItem) int {
	total := 0
	for _, item := range items {
		total += storage.CountTokens(item.Content)
	}
	return total
}

func TestMemberAppendOnly(t *testing.T) {
	tests := []struct {
		name   string
		member Member
		want   bool
	}{
		{"all appends", Member{Items: []storage.RetainItem{
			{Content: "one", UpdateMode: "append"},
			{Content: "two", UpdateMode: "append"},
		}}, true},
		{"one replace among appends", Member{Items: []storage.RetainItem{
			{Content: "one", UpdateMode: "append"},
			{Content: "two", UpdateMode: "replace"},
		}}, false},
		{"replace only", Member{Items: []storage.RetainItem{{Content: "one", UpdateMode: "replace"}}}, false},
		{"unset mode", Member{Items: []storage.RetainItem{{Content: "one"}}}, false},
		{"no items", Member{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.member.AppendOnly(); got != tt.want {
				t.Fatalf("AppendOnly() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestJoinRejection(t *testing.T) {
	base := Member{
		OperationID:  "primary",
		Items:        []storage.RetainItem{appendItem("primary turn")},
		TenantID:     "tenant-1",
		APIKeyID:     "key-1",
		DocumentTags: []string{"a", "b"},
		Strategy:     "intake",
	}
	joinable := func(mutate func(*Member)) Member {
		peer := Member{
			OperationID:  "peer",
			Items:        []storage.RetainItem{appendItem("peer turn")},
			TenantID:     base.TenantID,
			APIKeyID:     base.APIKeyID,
			DocumentTags: []string{"b", "a"},
			Strategy:     base.Strategy,
		}
		mutate(&peer)
		return peer
	}
	tests := []struct {
		name   string
		want   string
		mutate func(*Member)
	}{
		{"matching peer joins", "", func(*Member) {}},
		{"tag order does not matter", "", func(*Member) {}},
		{"non-append peer", "peer is not append-only", func(m *Member) {
			m.Items = []storage.RetainItem{{Content: "body", UpdateMode: "replace"}}
		}},
		{"empty peer", "peer is not append-only", func(m *Member) { m.Items = nil }},
		{"file metadata peer", "peer carries file metadata", func(m *Member) { m.HasFileMetadata = true }},
		{"different tenant", "peer has a different tenant or api key", func(m *Member) { m.TenantID = "tenant-2" }},
		{"different api key", "peer has a different tenant or api key", func(m *Member) { m.APIKeyID = "key-2" }},
		{"different document tags", "peer has different document tags", func(m *Member) {
			m.DocumentTags = []string{"a", "c"}
		}},
		{"different tag count", "peer has different document tags", func(m *Member) {
			m.DocumentTags = []string{"a"}
		}},
		{"different strategy", "peer has a different strategy", func(m *Member) { m.Strategy = "digest" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := JoinRejection(base, joinable(tt.mutate)); got != tt.want {
				t.Fatalf("JoinRejection() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPlanFoldFoldsConsecutiveAppends(t *testing.T) {
	primary := Member{OperationID: "op-0", Items: []storage.RetainItem{appendItem("first turn")}}
	peers := []Member{
		member("op-1", appendItem("second turn")),
		member("op-2", appendItem("third turn"), appendItem("fourth turn")),
		member("op-3", appendItem("fifth turn")),
	}
	plan := PlanFold(primary, peers, DefaultMaxFoldPeers, 1_000_000)
	if got, want := len(plan.Members), 4; got != want {
		t.Fatalf("folded %d members, want %d", got, want)
	}
	if got, want := plan.PeerIDs(), []string{"op-1", "op-2", "op-3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("peer ids = %v, want %v", got, want)
	}
	if len(plan.Deferred) != 0 {
		t.Fatalf("deferred = %v, want none", plan.Deferred)
	}
	merged := MergeItems(plan.Members)
	if got, want := len(merged), 5; got != want {
		t.Fatalf("merged %d items, want %d", got, want)
	}
	for i, want := range []string{"first turn", "second turn", "third turn", "fourth turn", "fifth turn"} {
		if merged[i].Content != want {
			t.Fatalf("merged item %d = %q, want %q", i, merged[i].Content, want)
		}
	}
	if merged[2].DocumentID != "doc-1" {
		t.Fatalf("merged item lost its document id: %+v", merged[2])
	}
}

func TestPlanFoldDefersEverythingBehindUnfoldablePrimary(t *testing.T) {
	peers := []Member{member("op-1", appendItem("one")), member("op-2", appendItem("two"))}
	tests := []struct {
		name    string
		primary Member
	}{
		{"replace primary", Member{OperationID: "op-0", Items: []storage.RetainItem{
			{Content: "body", UpdateMode: "replace"},
		}}},
		{"file metadata primary", func() Member {
			m := member("op-0", appendItem("body"))
			m.HasFileMetadata = true
			return m
		}()},
		{"primary without items", Member{OperationID: "op-0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := PlanFold(tt.primary, peers, DefaultMaxFoldPeers, 1_000_000)
			if got, want := plan.PeerIDs(), []string(nil); !reflect.DeepEqual(got, want) {
				t.Fatalf("peer ids = %v, want none", got)
			}
			if got, want := plan.Deferred, []string{"op-1", "op-2"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("deferred = %v, want %v", got, want)
			}
		})
	}
}

func TestPlanFoldDefersAllWhenNoPeersAllowed(t *testing.T) {
	primary := Member{OperationID: "op-0", Items: []storage.RetainItem{appendItem("body")}}
	peers := []Member{member("op-1", appendItem("one"))}
	plan := PlanFold(primary, peers, 0, 1_000_000)
	if got, want := plan.Deferred, []string{"op-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("deferred = %v, want %v", got, want)
	}
	if got := plan.PeerIDs(); got != nil {
		t.Fatalf("peer ids = %v, want none", got)
	}
}

func TestPlanFoldStopsAtFirstPeerThatCannotJoin(t *testing.T) {
	primary := Member{OperationID: "op-0", Items: []storage.RetainItem{appendItem("first turn")}}
	peers := []Member{
		member("op-1", appendItem("second turn")),
		member("op-2", appendItem("third turn")),
	}
	peers[1].Strategy = "digest"
	peers = append(peers, member("op-3", appendItem("fourth turn")))
	plan := PlanFold(primary, peers, DefaultMaxFoldPeers, 1_000_000)
	if got, want := plan.PeerIDs(), []string{"op-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("peer ids = %v, want %v", got, want)
	}
	if got, want := plan.Deferred, []string{"op-2", "op-3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("deferred = %v, want %v", got, want)
	}
}

func TestMaxPeersForRetryHalvesPerRetry(t *testing.T) {
	tests := []struct {
		retryCount int
		want       int
	}{
		{0, 16},
		{1, 8},
		{2, 4},
		{3, 2},
		{4, 1},
		{5, 0},
		{9, 0},
		{-2, 16},
	}
	for _, tt := range tests {
		if got := MaxPeersForRetry(tt.retryCount); got != tt.want {
			t.Fatalf("MaxPeersForRetry(%d) = %d, want %d", tt.retryCount, got, tt.want)
		}
	}
}

func TestPlanFoldRespectsMaxPeers(t *testing.T) {
	primary := Member{OperationID: "op-0", Items: []storage.RetainItem{appendItem("first turn")}}
	peers := make([]Member, 0, 10)
	for i := 1; i <= 10; i++ {
		peers = append(peers, member("op-"+string(rune('0'+i)), appendItem("turn")))
	}
	plan := PlanFold(primary, peers, MaxPeersForRetry(1), 1_000_000)
	if got, want := len(plan.Members), 9; got != want {
		t.Fatalf("folded %d members, want %d", got, want)
	}
	if got, want := len(plan.Deferred), 2; got != want {
		t.Fatalf("deferred %d peers, want %d", got, want)
	}
	if plan.Deferred[0] != "op-9" || plan.Deferred[1] != "op-:" {
		t.Fatalf("deferred = %v, want the last two in order", plan.Deferred)
	}
}

func TestPlanFoldStopsAtTokenBudget(t *testing.T) {
	primaryItem := appendItem("primary turn")
	first := appendItem("first peer turn")
	second := appendItem("second peer turn")
	third := appendItem("third peer turn")
	primary := Member{OperationID: "op-0", Items: []storage.RetainItem{primaryItem}}
	peers := []Member{
		member("op-1", first),
		member("op-2", second),
		member("op-3", third),
	}
	// The budget fits the primary and the first two peers exactly, so the
	// boundary peer joins at equality and the one after it defers.
	budget := itemsTokensOf(primaryItem) + itemsTokensOf(first) + itemsTokensOf(second)
	plan := PlanFold(primary, peers, DefaultMaxFoldPeers, budget)
	if got, want := plan.PeerIDs(), []string{"op-1", "op-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("peer ids = %v, want %v", got, want)
	}
	if got, want := plan.Deferred, []string{"op-3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("deferred = %v, want %v", got, want)
	}
}

func TestPlanFoldRunsOversizedPrimaryAlone(t *testing.T) {
	primary := Member{OperationID: "op-0", Items: []storage.RetainItem{
		appendItem(strings.Repeat("a very large document body ", 2000)),
	}}
	peers := []Member{member("op-1", appendItem("one")), member("op-2", appendItem("two"))}
	plan := PlanFold(primary, peers, DefaultMaxFoldPeers, 1)
	if got, want := len(plan.Members), 1; got != want {
		t.Fatalf("folded %d members, want %d", got, want)
	}
	if plan.Members[0].OperationID != "op-0" {
		t.Fatalf("primary missing from the plan: %+v", plan.Members[0])
	}
	if got, want := plan.Deferred, []string{"op-1", "op-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("deferred = %v, want %v", got, want)
	}
}

func TestMemberRefsRoundTripThroughPayload(t *testing.T) {
	members := []Member{
		{OperationID: "op-0", Items: []storage.RetainItem{appendItem("one"), appendItem("two")}},
		member("op-1", appendItem("three")),
	}
	want := []MemberRef{{OperationID: "op-0", ItemsCount: 2}, {OperationID: "op-1", ItemsCount: 1}}
	if got := RefsFor(members); !reflect.DeepEqual(got, want) {
		t.Fatalf("RefsFor() = %+v, want %+v", got, want)
	}

	payload := map[string]any{PayloadKey: want}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	refs, ok := RefsFromPayload(decoded[PayloadKey])
	if !ok {
		t.Fatal("fold members missing from the decoded payload")
	}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %+v, want %+v", refs, want)
	}
}

func TestRefsFromPayloadAbsence(t *testing.T) {
	if refs, ok := RefsFromPayload(nil); ok || refs != nil {
		t.Fatalf("absent value read as %+v, ok=%v", refs, ok)
	}
	if refs, ok := RefsFromPayload("not a list"); ok || refs != nil {
		t.Fatalf("non-list value read as %+v, ok=%v", refs, ok)
	}
	if got := RefsFor(nil); got != nil {
		t.Fatalf("RefsFor(nil) = %+v, want nil", got)
	}
}
