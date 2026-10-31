package personalrollup

import (
	"reflect"
	"testing"
)

func TestProjectDirectoryCountsLinks(t *testing.T) {
	keyA, keyB := "ka", "kb"
	banks := map[string][]string{
		keyA: {"alice::orders", "alice::billing"},
		keyB: {"alice::orders"},
	}
	directory := ProjectDirectory(banks, nil)
	want := []Link{
		{BankID: "alice::orders", Weight: 2},
		{BankID: "alice::billing", Weight: 1},
	}
	if !reflect.DeepEqual(directory.Links, want) {
		t.Fatalf("links = %+v, want %+v", directory.Links, want)
	}
}

// TestProjectDirectoryGroupsSharedTopics pins what "the index helps you find the
// right bank" means: a topic two banks share is an edge between them, and a topic
// only one bank holds is not.
func TestProjectDirectoryGroupsSharedTopics(t *testing.T) {
	topics := []TopicEntry{
		{Name: "deploy", BankID: "alice::orders"},
		{Name: "deploy", BankID: "alice::billing"},
		{Name: "postgres", BankID: "alice::orders"},
		{Name: "postgres", BankID: "alice::billing"},
		{Name: "auth", BankID: "alice::gateway"},
	}
	directory := ProjectDirectory(map[string][]string{}, topics)
	if len(directory.Groups) != 2 {
		t.Fatalf("groups = %+v, want the two shared topics", directory.Groups)
	}
	// Equal sizes break on the topic name, so the order is total.
	if directory.Groups[0].Topic != "deploy" || directory.Groups[1].Topic != "postgres" {
		t.Fatalf("group order = %s,%s, want deploy,postgres",
			directory.Groups[0].Topic, directory.Groups[1].Topic)
	}
	for _, group := range directory.Groups {
		if len(group.Banks) != 2 || group.Banks[0] != "alice::billing" {
			t.Fatalf("group %s banks = %v, want sorted pair", group.Topic, group.Banks)
		}
	}
}

func TestProjectDirectoryDeduplicatesEntries(t *testing.T) {
	topics := []TopicEntry{
		{Name: "deploy", BankID: "alice::orders"},
		{Name: "deploy", BankID: "alice::orders"},
		{Name: "", BankID: "alice::orders"},
	}
	directory := ProjectDirectory(nil, topics)
	if len(directory.Topics) != 1 {
		t.Fatalf("topics = %+v, want one entry", directory.Topics)
	}
}

func TestProjectDirectoryRespectsTheLimits(t *testing.T) {
	topics := make([]TopicEntry, 0, 50)
	for i := 0; i < 50; i++ {
		topics = append(topics, TopicEntry{Name: string(rune('a' + i%26)), BankID: "alice::x"})
	}
	directory := ProjectDirectory(nil, topics, 3, 5)
	if len(directory.Topics) != 5 {
		t.Fatalf("topics = %d, want 5", len(directory.Topics))
	}
	if len(directory.Groups) != 0 {
		t.Fatalf("groups = %+v, want none: every topic has one bank", directory.Groups)
	}
	groups := ProjectDirectory(nil, []TopicEntry{
		{Name: "a", BankID: "b1"}, {Name: "a", BankID: "b2"},
		{Name: "b", BankID: "b1"}, {Name: "b", BankID: "b2"},
		{Name: "c", BankID: "b1"}, {Name: "c", BankID: "b2"},
	}, 2, 20)
	if len(groups.Groups) != 2 {
		t.Fatalf("groups = %+v, want 2", groups.Groups)
	}
}

func TestProjectDirectoryIsDeterministic(t *testing.T) {
	banks := map[string][]string{
		"k1": {"alice::b", "alice::a"},
		"k2": {"alice::c", "alice::a"},
	}
	topics := []TopicEntry{
		{Name: "deploy", BankID: "alice::b"},
		{Name: "deploy", BankID: "alice::a"},
	}
	first := ProjectDirectory(banks, topics)
	for i := 0; i < 20; i++ {
		if got := ProjectDirectory(banks, topics); !reflect.DeepEqual(got, first) {
			t.Fatalf("projection %d differs:\n%+v\n%+v", i, got, first)
		}
	}
}
