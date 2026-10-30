package personalrollup

import (
	"fmt"
	"strings"

	"github.com/studyzy/wenyuan/internal/provider"
)

// TopicWindow is how many vocabulary entries a round's prompt carries. It bounds
// the prompt's growth: the vocabulary is the tenant's, and one person's topics
// outlive any single bank.
const TopicWindow = 30

// PromptOptions carries what shapes one round's prompt.
type PromptOptions struct {
	// TopicWindow is how many known topics to list. Zero uses TopicWindow.
	TopicWindow int
}

// Prompt builds the round's messages.
//
// Previous and fresh are numbered separately (P and F) so an answer's sources can
// only ever cite this round's inputs. A previous fact's provenance was settled in
// an earlier round and is not the model's to restate: letting it cite P entries
// would make the ledger describe whatever the model thought that round rather
// than what the fact was actually made from.
func Prompt(previous []PreviousFact, fresh []SourceFact, topics []string, opts PromptOptions) []provider.Message {
	window := opts.TopicWindow
	if window <= 0 {
		window = TopicWindow
	}
	var system strings.Builder
	system.WriteString("You maintain one person's cross-bank memory: a single knowledge base drawn from ")
	system.WriteString("every project and repository that person owns.\n")
	system.WriteString("You are given what that memory currently holds and the new facts from one or more of the person's banks.\n")
	system.WriteString("Produce the complete updated fact list: keep what is still true, merge facts that say the same ")
	system.WriteString("thing across different banks, drop what the new facts supersede, and add what is genuinely new.\n\n")
	system.WriteString("Rules:\n")
	system.WriteString("- Reply with JSON only, no prose and no code fences.\n")
	system.WriteString("- The shape is {\"facts\": [{\"text\": \"...\", \"sources\": [0, 1], \"topics\": [\"deploy\"]}]}.\n")
	system.WriteString("- Each fact is one self-contained statement in a single sentence.\n")
	system.WriteString("- \"sources\" must contain only F indexes: the source facts this statement was made from.\n")
	system.WriteString("- Cite every F index that supports the statement; never cite a P index.\n")
	system.WriteString("- A fact carried over unchanged needs the same wording and the F indexes now supporting it.\n")
	system.WriteString("- Keep concrete values exactly as they were: ports, paths, versions, API and field names, ")
	system.WriteString("configuration keys, limits and other constraints. Never abstract them away and never round them.\n")
	system.WriteString("- Prefer an existing topic from the list below; propose a new one only when none fits.\n")
	system.WriteString("- Do not invent facts. Do not mention the person by name and do not name the sources.\n")

	var user strings.Builder
	user.WriteString("Current memory:\n")
	if len(previous) == 0 {
		user.WriteString("(none yet)\n")
	}
	for i, fact := range previous {
		fmt.Fprintf(&user, "[P%d] %s\n", i, fact.Text)
	}
	user.WriteString("\nNew facts from this person's banks:\n")
	if len(fresh) == 0 {
		user.WriteString("(none)\n")
	}
	lastBank := ""
	for i, fact := range fresh {
		if fact.BankID != lastBank {
			fmt.Fprintf(&user, "bank %s:\n", fact.BankID)
			lastBank = fact.BankID
		}
		fmt.Fprintf(&user, "[F%d] %s\n", i, fact.Text)
	}
	user.WriteString("\nKnown topics:\n")
	listed := topics
	if len(listed) > window {
		listed = listed[:window]
	}
	if len(listed) == 0 {
		user.WriteString("(none yet)\n")
	}
	for _, topic := range listed {
		fmt.Fprintf(&user, "- %s\n", topic)
	}
	user.WriteString("\nReply with the complete updated fact list as JSON.")

	return []provider.Message{
		{Role: provider.RoleSystem, Content: system.String()},
		{Role: provider.RoleUser, Content: user.String()},
	}
}
