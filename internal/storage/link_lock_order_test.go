package storage

import (
	"math/rand"
	"reflect"
	"testing"
)

func TestOrderKeyForNormalizesDirection(t *testing.T) {
	forward := MemoryLink{FromUnitID: "a", ToUnitID: "b", LinkType: LinkCausedBy}
	reverse := MemoryLink{FromUnitID: "b", ToUnitID: "a", LinkType: LinkCausedBy}
	if orderKeyFor(forward) != orderKeyFor(reverse) {
		t.Fatalf("direction must not change the key: %+v vs %+v", orderKeyFor(forward), orderKeyFor(reverse))
	}
	if orderKeyFor(forward).entity != nilEntityID {
		t.Fatalf("a null entity must fold to the nil id, got %q", orderKeyFor(forward).entity)
	}
}

func TestSortLinksByLockOrder(t *testing.T) {
	links := []MemoryLink{
		{FromUnitID: "b", ToUnitID: "a", LinkType: LinkCauses},
		{FromUnitID: "a", ToUnitID: "b", LinkType: LinkCausedBy, EntityID: "e2"},
		{FromUnitID: "a", ToUnitID: "b", LinkType: LinkCausedBy, EntityID: "e1"},
		{FromUnitID: "d", ToUnitID: "c", LinkType: LinkCausedBy},
	}
	want := []string{
		"a/b/caused_by/e1",
		"a/b/caused_by/e2",
		"a/b/causes/" + nilEntityID,
		"c/d/caused_by/" + nilEntityID,
	}
	if got := linkOrderKeysOf(sortLinksByLockOrder(links)); !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestSortLinksByLockOrderIsInputOrderIndependent(t *testing.T) {
	links := []MemoryLink{
		{FromUnitID: "9", ToUnitID: "1", LinkType: LinkTemporal},
		{FromUnitID: "2", ToUnitID: "8", LinkType: LinkCausedBy, EntityID: "e1"},
		{FromUnitID: "8", ToUnitID: "2", LinkType: LinkCausedBy, EntityID: "e2"},
		{FromUnitID: "3", ToUnitID: "7", LinkType: LinkSemantic},
		{FromUnitID: "4", ToUnitID: "6", LinkType: LinkEnables},
	}
	want := linkOrderKeysOf(sortLinksByLockOrder(links))
	for seed := int64(0); seed < 20; seed++ {
		shuffled := append([]MemoryLink(nil), links...)
		r := rand.New(rand.NewSource(seed))
		r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		if got := linkOrderKeysOf(sortLinksByLockOrder(shuffled)); !reflect.DeepEqual(got, want) {
			t.Fatalf("seed %d: order = %v, want %v", seed, got, want)
		}
	}
}

func linkOrderKeysOf(links []MemoryLink) []string {
	keys := make([]string, 0, len(links))
	for _, link := range links {
		key := orderKeyFor(link)
		keys = append(keys, key.low+"/"+key.high+"/"+key.linkType+"/"+key.entity)
	}
	return keys
}
