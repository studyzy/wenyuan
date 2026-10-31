package personalrollup

import "sort"

// The navigation index: what an agent reads to find the right bank before it
// reads a fact. It is a pure projection of a completed round's output — the banks
// its facts came from and the topics those facts carry — so it costs no model call
// and can be recomputed from the ledger alone.

// Directory defaults.
const (
	// TopGroups bounds how many banks the directory's root groups. Beyond it the
	// directory is a list nobody reads to the end.
	TopGroups = 8
	// TopTopics bounds how many topic entries the directory carries.
	TopTopics = 20
)

// TopicEntry is one (topic, bank) vocabulary entry an answer produced.
type TopicEntry struct {
	// Name is the normalized topic.
	Name   string
	BankID string
	// Key is the merge key of the aggregate fact the entry came from.
	Key string
}

// Link is one directory edge: a bank the aggregate's facts draw on, and how many
// of them do.
type Link struct {
	BankID string
	Weight int
}

// Group is one directory section: a topic several banks share.
type Group struct {
	Topic string
	Banks []string
}

// Directory is the projection of one round's output.
type Directory struct {
	// Links orders every contributing bank by how many aggregate facts cite it.
	Links []Link
	// Topics orders every vocabulary entry.
	Topics []TopicEntry
	// Groups lists the topics more than one bank belongs to, most shared first.
	// A topic only one bank holds is not an edge between anything, so it is not
	// a group.
	Groups []Group
}

// ProjectDirectory computes the directory from a round's source banks and topics.
//
// The input is what the round already wrote into the ledger; the output is what
// the console shows and what the knowledge tree is built from. Nothing here calls
// a model, and the ordering is total: two runs over the same input produce the
// same directory.
func ProjectDirectory(banksByKey map[string][]string, topics []TopicEntry, opts ...int) Directory {
	groupLimit, topicLimit := TopGroups, TopTopics
	if len(opts) > 0 && opts[0] > 0 {
		groupLimit = opts[0]
	}
	if len(opts) > 1 && opts[1] > 0 {
		topicLimit = opts[1]
	}

	weights := make(map[string]int, len(banksByKey))
	for _, banks := range banksByKey {
		for _, bank := range banks {
			weights[bank]++
		}
	}
	links := make([]Link, 0, len(weights))
	for bank, weight := range weights {
		links = append(links, Link{BankID: bank, Weight: weight})
	}
	sort.Slice(links, func(i, j int) bool {
		if links[i].Weight != links[j].Weight {
			return links[i].Weight > links[j].Weight
		}
		return links[i].BankID < links[j].BankID
	})

	seen := make(map[string]bool, len(topics))
	ordered := make([]TopicEntry, 0, len(topics))
	for _, topic := range topics {
		key := topic.Name + "\x00" + topic.BankID
		if topic.Name == "" || seen[key] {
			continue
		}
		seen[key] = true
		ordered = append(ordered, topic)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Name != ordered[j].Name {
			return ordered[i].Name < ordered[j].Name
		}
		return ordered[i].BankID < ordered[j].BankID
	})
	if len(ordered) > topicLimit {
		ordered = ordered[:topicLimit]
	}

	byTopic := make(map[string][]string, len(ordered))
	for _, topic := range ordered {
		byTopic[topic.Name] = append(byTopic[topic.Name], topic.BankID)
	}
	groups := make([]Group, 0, len(byTopic))
	for name, banks := range byTopic {
		if len(banks) < 2 {
			continue
		}
		sorted := append([]string(nil), banks...)
		sort.Strings(sorted)
		groups = append(groups, Group{Topic: name, Banks: sorted})
	}
	sort.Slice(groups, func(i, j int) bool {
		if len(groups[i].Banks) != len(groups[j].Banks) {
			return len(groups[i].Banks) > len(groups[j].Banks)
		}
		return groups[i].Topic < groups[j].Topic
	})
	if len(groups) > groupLimit {
		groups = groups[:groupLimit]
	}
	return Directory{Links: links, Topics: ordered, Groups: groups}
}
