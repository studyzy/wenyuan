package storage

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// maxIntrabatchNames bounds how many distinct names one item's in-batch merge
// compares. Past it the quadratic comparison is skipped entirely rather than
// truncated, because a half-merged batch is a worse answer than a document
// whose same-batch variants simply stay separate — the reference makes the same
// call at the same cap.
const maxIntrabatchNames = 250

// minTokenSimilarity is the near-miss ratio at or above which two words of a
// multi-word name are considered the same word. It is the reference's bar for
// its token gate.
const minTokenSimilarity = 0.6

// itemEntityMentions returns every entity the item's units mention, in unit
// order. That set is the "batch" the in-batch merge clusters: one extraction
// pass over one document, which is the granularity the reference merges at, so
// two spellings of one name that appear in the same document collapse before
// either is scored against the stored registry.
func itemEntityMentions(contents []itemContent) []RetainEntity {
	mentions := make([]RetainEntity, 0)
	for _, content := range contents {
		mentions = append(mentions, content.entities...)
	}
	return mentions
}

// intrabatchCanonicalNames maps each of the batch's entity names onto the
// spelling its in-batch cluster shares.
//
// Two names whose pg_trgm similarity meets the threshold — and which agree word
// by word — are merged before either is scored against the stored registry, so
// a document that names "McDonald's" and "McDonalds" creates one entity rather
// than two. The key is the lowercased match key (whitespace collapsed), the
// value a representative original-case spelling, so the caller can look a
// mention up uniformly.
//
// Names the caller wrote literally (resolution off) are excluded, matching the
// reference: a caller who listed two names as two gets two entities. The
// reference also excludes label entities, which this build does not distinguish
// — a label is stored through the same path as a named entity, so it can take
// part in the merge here.
//
// A threshold at or below zero disables the pass, and so does a batch of fewer
// than two names or more than maxIntrabatchNames of them. The canonical pick
// mirrors the reference: most mentions, then shortest spelling, then
// lexicographically smallest, which prefers the plainest form deterministically.
func intrabatchCanonicalNames(entities []RetainEntity, resolve bool, threshold float64) map[string]string {
	if !resolve || threshold <= 0 {
		return nil
	}
	// repByKey is the first spelling seen for a lowercased key; countByKey is
	// how many mentions carry it, which the canonical pick weighs.
	repByKey := map[string]string{}
	countByKey := map[string]int{}
	order := make([]string, 0, len(entities))
	for _, entity := range entities {
		key := entityMatchKey(entity.Text)
		if key == "" {
			continue
		}
		lower := strings.ToLower(key)
		if _, seen := repByKey[lower]; !seen {
			repByKey[lower] = key
			order = append(order, lower)
		}
		countByKey[lower]++
	}
	if len(order) < 2 || len(order) > maxIntrabatchNames {
		return nil
	}

	parent := make(map[string]string, len(order))
	for _, key := range order {
		parent[key] = key
	}
	find := func(key string) string {
		for parent[key] != key {
			parent[key] = parent[parent[key]]
			key = parent[key]
		}
		return key
	}
	union := func(a, b string) {
		if ra, rb := find(a), find(b); ra != rb {
			parent[ra] = rb
		}
	}
	for i := range order {
		for j := i + 1; j < len(order); j++ {
			a, b := repByKey[order[i]], repByKey[order[j]]
			if entityNameTrigramSimilarity(a, b) < threshold {
				continue
			}
			// Trigram similarity alone lets one long shared word drown out a
			// completely different short one, so a similar pair must also agree
			// word by word.
			if !entityTokensAreCompatible(a, b) {
				continue
			}
			union(order[i], order[j])
		}
	}

	clusters := map[string][]string{}
	for _, key := range order {
		root := find(key)
		clusters[root] = append(clusters[root], key)
	}
	canonical := make(map[string]string, len(order))
	for _, members := range clusters {
		chosen := members[0]
		for _, member := range members[1:] {
			if betterIntrabatchCanonical(member, chosen, repByKey, countByKey) {
				chosen = member
			}
		}
		for _, member := range members {
			canonical[member] = repByKey[chosen]
		}
	}
	return canonical
}

// betterIntrabatchCanonical reports whether candidate should be the cluster's
// canonical spelling rather than current: more mentions wins, then the shorter
// spelling, then the lexicographically smaller one.
func betterIntrabatchCanonical(candidate, current string, repByKey map[string]string, countByKey map[string]int) bool {
	if countByKey[candidate] != countByKey[current] {
		return countByKey[candidate] > countByKey[current]
	}
	if len(repByKey[candidate]) != len(repByKey[current]) {
		return len(repByKey[candidate]) < len(repByKey[current])
	}
	return repByKey[candidate] < repByKey[current]
}

// applyIntrabatchCanonical rewrites an entity's name to its cluster's canonical
// spelling when the batch merged it with a near-identical name, and leaves it
// alone otherwise.
func applyIntrabatchCanonical(entity RetainEntity, canonical map[string]string) RetainEntity {
	if len(canonical) == 0 {
		return entity
	}
	key := entityMatchKey(entity.Text)
	if key == "" {
		return entity
	}
	if name, ok := canonical[strings.ToLower(key)]; ok {
		entity.Text = name
	}
	return entity
}

// entityNameTrigramSimilarity is the Jaccard index of two names' pg_trgm
// trigram sets.
//
// Distinct from the package's whole-string trigramJaccard: pg_trgm builds a
// name's trigrams per word (each word padded with two leading and one trailing
// blank), so "McDonald's" and "McDonalds" share nearly every gram, while the
// whole-string measure spends its grams on the spaces between words. The merge
// cutoff the reference calibrates is expressed against pg_trgm, so this measure
// is the one that transfers it.
func entityNameTrigramSimilarity(a, b string) float64 {
	setA, setB := entityNameTrigrams(a), entityNameTrigrams(b)
	if len(setA) == 0 || len(setB) == 0 {
		return 0
	}
	intersection := 0
	for gram := range setA {
		if setB[gram] {
			intersection++
		}
	}
	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// entityNameTrigrams returns the trigram set of a name the way PostgreSQL
// pg_trgm generates it: every alphanumeric word lowercased and padded with two
// leading and one trailing blank, then every three-character window.
func entityNameTrigrams(name string) map[string]bool {
	out := map[string]bool{}
	for _, word := range entityNameWords(name) {
		padded := []rune("  " + word + " ")
		for i := range len(padded) - 2 {
			out[string(padded[i:i+3])] = true
		}
	}
	return out
}

// entityNameWords returns the maximal runs of letters and digits in a name,
// lowercased. That is pg_trgm's word definition, where underscore and every
// other non-alphanumeric is a separator.
func entityNameWords(name string) []string {
	words := make([]string, 0)
	var current []rune
	flush := func() {
		if len(current) > 0 {
			words = append(words, string(current))
			current = current[:0]
		}
	}
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			current = append(current, r)
			continue
		}
		flush()
	}
	flush()
	return words
}

// entityNumbers returns the numeric runs a name carries, normalized the way the
// reference does: a leading zero run is stripped from the integer part and an
// all-zero fractional part is dropped, so "01" and "1" are one number while
// "3.10" stays distinct from "3.1".
func entityNumbers(name string) map[string]bool {
	out := map[string]bool{}
	for _, run := range digitRuns(name) {
		out[normalizeNumber(run)] = true
	}
	return out
}

// digitRuns returns the maximal runs of digits, optionally with dotted groups,
// in a name ("UA0123" yields "0123", "4.0" yields "4.0").
func digitRuns(name string) []string {
	runs := make([]string, 0)
	runes := []rune(name)
	for i := 0; i < len(runes); {
		if !unicode.IsDigit(runes[i]) {
			i++
			continue
		}
		start := i
		for i < len(runes) && unicode.IsDigit(runes[i]) {
			i++
		}
		for i+1 < len(runes) && runes[i] == '.' && unicode.IsDigit(runes[i+1]) {
			i++
			for i < len(runes) && unicode.IsDigit(runes[i]) {
				i++
			}
		}
		runs = append(runs, string(runes[start:i]))
	}
	return runs
}

// normalizeNumber strips a leading zero run from a number's integer part and
// drops a trailing all-zero fractional part.
func normalizeNumber(run string) string {
	parts := strings.Split(run, ".")
	parts[0] = strings.TrimLeft(parts[0], "0")
	if parts[0] == "" {
		parts[0] = "0"
	}
	if len(parts) == 2 && strings.Trim(parts[1], "0") == "" {
		parts = parts[:1]
	}
	return strings.Join(parts, ".")
}

// entityTokensAreCompatible reports whether two multi-word names agree word by
// word.
//
// Whole-name similarity lets one long shared word drown out a completely
// different short one, and "Dr John Richardson"/"Dr Jane Richardson" clears the
// in-batch trigram bar. Every word of the shorter name has to find a counterpart
// in the longer one. Single-word names are exempt, because with one token the
// whole-name check is the token check. Two names that each carry a number the
// other lacks are two different things even so, which is what keeps "Room 101"
// and "Room 102" apart; a subset number ("Boeing 737 MAX" / "Boeing 737 MAX 8")
// is not such a mismatch.
func entityTokensAreCompatible(a, b string) bool {
	numbersA, numbersB := entityNumbers(a), entityNumbers(b)
	if hasNumberOutside(numbersA, numbersB) && hasNumberOutside(numbersB, numbersA) {
		return false
	}
	wordsA, wordsB := entityNameWords(a), entityNameWords(b)
	if len(wordsA) < 2 && len(wordsB) < 2 {
		return true
	}
	short, rest := wordsA, wordsB
	if len(wordsA) > len(wordsB) {
		short, rest = wordsB, wordsA
	}
	for _, word := range short {
		matched := false
		for _, other := range rest {
			if entityTokensMatch(word, other) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// hasNumberOutside reports whether any of the first set's numbers is absent from
// the second.
func hasNumberOutside(numbers, other map[string]bool) bool {
	for number := range numbers {
		if !other[number] {
			return true
		}
	}
	return false
}

// entityTokensMatch reports whether two words are plausibly the same word:
// equal, one an abbreviation of the other, or a near-miss.
func entityTokensMatch(a, b string) bool {
	if a == b || strings.HasPrefix(a, b) || strings.HasPrefix(b, a) {
		return true
	}
	return entitySequenceRatio(a, b) >= minTokenSimilarity
}

// entitySequenceRatio is the Ratcliff-Obershelp similarity the reference's
// token gate uses: twice the matched characters over the two lengths, where the
// match is found by repeatedly taking the longest common contiguous block.
func entitySequenceRatio(a, b string) float64 {
	runesA, runesB := []rune(a), []rune(b)
	total := len(runesA) + len(runesB)
	if total == 0 {
		return 1
	}
	return 2 * float64(entityMatchingChars(runesA, runesB)) / float64(total)
}

// entityMatchingChars counts the characters Ratcliff-Obershelp pairs between
// two rune slices: the longest common contiguous block, plus the same recursion
// on the parts before and after it.
func entityMatchingChars(a, b []rune) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	bestLength, bestI, bestJ := 0, 0, 0
	previous := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		current := make([]int, len(b)+1)
		for j := 1; j <= len(b); j++ {
			if a[i-1] != b[j-1] {
				continue
			}
			current[j] = previous[j-1] + 1
			if current[j] > bestLength {
				bestLength, bestI, bestJ = current[j], i-current[j], j-current[j]
			}
		}
		previous = current
	}
	if bestLength == 0 {
		return 0
	}
	return bestLength +
		entityMatchingChars(a[:bestI], b[:bestJ]) +
		entityMatchingChars(a[bestI+bestLength:], b[bestJ+bestLength:])
}

// joinDocumentParts is the one definition of how a document's content parts
// become its stored body: two JSON arrays merge element-wise, so a conversation
// stays one array, and anything else is newline-joined.
func joinDocumentParts(existing, incoming string) string {
	if merged, ok := mergeJSONArrayParts(existing, incoming); ok {
		return merged
	}
	return existing + "\n" + incoming
}

// mergeJSONArrayParts concatenates JSON array bodies into one array, preserving
// each element as it was written. It reports false unless every part is a JSON
// array, because an append that is not array-to-array must not reshape the body.
func mergeJSONArrayParts(parts ...string) (string, bool) {
	combined := make([]json.RawMessage, 0)
	for _, part := range parts {
		var array []json.RawMessage
		if err := json.Unmarshal([]byte(strings.TrimSpace(part)), &array); err != nil {
			return "", false
		}
		combined = append(combined, array...)
	}
	encoded, err := json.Marshal(combined)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

// itemContent is one memory unit a write is about to store, with the entity
// mentions that belong to it carried alongside so the caller does not have to
// re-derive them.
//
// The unit's identifier is left empty: each backend assigns it, because the
// memory backend keys its maps by the identifier while the PostgreSQL backend
// lets the insert own it.
type itemContent struct {
	unit     MemoryUnit
	entities []RetainEntity
	// causedByIndexes names the earlier units within the same item that caused
	// this one, by their position in the item's unit list. The position is
	// resolved to a stored unit once identifiers exist, which is why it is
	// carried rather than turned into a link here.
	causedByIndexes []int
}

// itemContents builds the units one item produces, given the bank, the chunk
// identifier stored for each segment the item's text was split into, and the
// tags that apply.
//
// When a model was asked and produced facts, each fact becomes its own unit
// carrying the fact's entities, dates and confidence, anchored to the chunk it
// came from. When no model was configured — or the bank asked for chunks mode,
// which stores text without asking — every stored segment of the item becomes a
// unit. A model that was asked and found nothing produces no unit at all: the
// text is still stored on the document, but the bank remembers nothing of it.
//
// chunkBySegment maps a segment's position in the item to the chunk identifier
// stored for it; a segment absent from the map was not stored (an append whose
// text the document already held) and contributes no unit.
func itemContents(bankID, docID string, chunkBySegment map[int]string, item RetainItem, facts []ExtractedFact, tags []string, sizes SegmentOptions, extractionAttempted bool) []itemContent {
	if len(facts) > 0 {
		return factContents(bankID, docID, chunkBySegment, item, facts, tags)
	}
	if extractionAttempted {
		// The model ran and named nothing: the item yields evidence only when
		// the model says it does, so an empty answer stores no unit.
		return nil
	}
	return verbatimContents(bankID, docID, chunkBySegment, item, tags, sizes)
}

// verbatimContents stores each segment of the item's text as a unit. The
// boundaries are the request's, so a bank that configured a segment size gets
// its own rather than the package default.
func verbatimContents(bankID, docID string, chunkBySegment map[int]string, item RetainItem, tags []string, sizes SegmentOptions) []itemContent {
	chunks := SegmentWith(item.Content, sizes)
	out := make([]itemContent, 0, len(chunks))
	for index, chunk := range chunks {
		chunkID, ok := chunkBySegment[index]
		if !ok {
			// The segment was not stored — a re-sent append turn the document
			// already held — so it contributes no unit.
			continue
		}
		unit := MemoryUnit{
			BankID:        bankID,
			Text:          chunk,
			Context:       item.Context,
			FactType:      TypeWorld,
			Metadata:      item.Metadata,
			DocumentID:    docID,
			ChunkID:       chunkID,
			Tags:          tags,
			AttachmentIDs: item.AttachmentIDs,
			// A verbatim segment inherits the item's observation scoping so the
			// consolidation pass folds it at the scope the caller asked for.
			// The value is stored verbatim because it is raw JSON: the caller
			// may send a mode name ("per_tag") or a list of tag groups, which
			// are different shapes and must survive to the resolving pass.
			ObservationScopes: item.ObservationScopes,
		}
		applyItemDates(&unit, item)
		unit.TextSignals = buildTextSignals(item.Entities, unit.OccurredStart, unit.OccurredEnd)
		out = append(out, itemContent{unit: unit, entities: item.Entities})
	}
	return out
}

// factContents stores one unit per extracted fact, each anchored to the chunk
// of the document its text came from.
func factContents(bankID, docID string, chunkBySegment map[int]string, item RetainItem, facts []ExtractedFact, tags []string) []itemContent {
	out := make([]itemContent, 0, len(facts))
	for _, fact := range facts {
		chunkID, ok := chunkBySegment[fact.ChunkIndex]
		if !ok {
			// The fact came from a segment the document already held (a re-sent
			// append turn), so it is not re-stored.
			continue
		}
		// The item's scoping rides with each fact: extraction stamps it onto
		// the fact so a fact that carries its own scopes is the authority for
		// its unit, and a fact that does not falls back to the item. This is
		// what keeps consolidation from folding across the scopes the caller
		// asked to keep apart. Stored verbatim because the raw JSON may be a
		// mode name or a list of tag groups, which are different shapes.
		scopes := fact.ObservationScopes
		if len(scopes) == 0 {
			scopes = item.ObservationScopes
		}
		unit := MemoryUnit{
			BankID:        bankID,
			Text:          fact.Text,
			Context:       item.Context,
			FactType:      factTypeOrDefault(fact.Type, fact.Kind),
			Metadata:      item.Metadata,
			DocumentID:    docID,
			ChunkID:       chunkID,
			Tags:          appendTagLabels(tags, fact.LabelTags),
			AttachmentIDs: mergeAttachmentIDs(item.AttachmentIDs, factAttachmentIDs(item.AttachmentIDs, fact.FromAttachments)),
			// See above: the scoping the fact folds under survives to storage.
			ObservationScopes: scopes,
		}
		applyItemDates(&unit, item)
		applyFactDates(&unit, fact)
		// A label is stored as an entity named key:value, through the same
		// resolution path as any other entity. Labels are appended after the
		// named entities so a caller reading the unit back sees them last.
		entities := append([]RetainEntity{}, item.Entities...)
		entities = append(entities, fact.Entities...)
		for _, label := range fact.Labels {
			if strings.TrimSpace(label) == "" {
				continue
			}
			entities = append(entities, RetainEntity{Text: label})
		}
		// The unit's searched vector is enriched with the names of exactly the
		// entities it mentions, so they are collected before the signals are built.
		unit.TextSignals = buildTextSignals(entities, unit.OccurredStart, unit.OccurredEnd)
		out = append(out, itemContent{
			unit:            unit,
			entities:        entities,
			causedByIndexes: fact.CausedByIndexes,
		})
	}
	return out
}

// labelDerivedTags returns the subset of a unit's stored tags that were derived
// from the bank's entity-label groups rather than supplied by the caller: those
// carrying a key (the part before the first colon, case-insensitively) that
// labelKeys names. It is the read-side counterpart of appendTagLabels, used by
// a retag to tell a unit's label projection from its document tags.
func labelDerivedTags(tags []string, labelKeys map[string]bool) []string {
	if len(tags) == 0 || len(labelKeys) == 0 {
		return nil
	}
	var out []string
	for _, tag := range tags {
		key, _, found := strings.Cut(tag, ":")
		if !found || !labelKeys[strings.ToLower(key)] {
			continue
		}
		out = append(out, tag)
	}
	return out
}

// appendTagLabels unions a unit's caller-supplied tags with the label-derived
// tags from one fact, preserving order and dropping repeats.
func appendTagLabels(tags, labelTags []string) []string {
	if len(labelTags) == 0 {
		return tags
	}
	out := make([]string, 0, len(tags)+len(labelTags))
	seen := map[string]bool{}
	for _, tag := range append(append([]string{}, tags...), labelTags...) {
		if seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	return out
}

// factTypeOrDefault maps an extracted fact type onto the stored unit type.
//
// The reference's rule: "assistant" and "experience" both mean the agent did
// something, and "world" means an objective fact. An unexpected value falls
// back to the fact kind before defaulting to world, which is what keeps a model
// that answered with "assistant" under fact_kind from losing the distinction.
func factTypeOrDefault(factType, factKind string) MemoryUnitType {
	switch factType {
	case string(TypeExperience), "assistant":
		return TypeExperience
	case "world":
		return TypeWorld
	default:
		if factKind == "assistant" {
			return TypeExperience
		}
		return TypeWorld
	}
}

// factAttachmentIDs resolves a fact's from_attachments onto the item's
// attachment identifiers.
//
// The model answers with attachment numbers, which index the item's
// attachments from one; an entry that is already an identifier is taken as one,
// so a model that echoed the id instead of the number still links.
func factAttachmentIDs(itemAttachmentIDs []string, fromAttachments []string) []string {
	if len(fromAttachments) == 0 {
		return nil
	}
	resolved := make([]string, 0, len(fromAttachments))
	seen := map[string]bool{}
	for _, entry := range fromAttachments {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		id := entry
		if position, err := strconv.Atoi(entry); err == nil {
			if position < 1 || position > len(itemAttachmentIDs) {
				continue
			}
			id = itemAttachmentIDs[position-1]
		} else if !containsString(itemAttachmentIDs, entry) {
			// An id the item does not carry cannot resolve to a stored
			// attachment, so it is dropped rather than linked blindly.
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		resolved = append(resolved, id)
	}
	return resolved
}

// mergeAttachmentIDs unions two attachment-identifier lists, preserving order
// and dropping repeats.
func mergeAttachmentIDs(base, extra []string) []string {
	if len(extra) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(extra))
	seen := map[string]bool{}
	for _, id := range append(append([]string{}, base...), extra...) {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// buildTextSignals renders the per-unit text_signals the reference folds into
// search_vector: the names of the entities the unit mentions, then the unit's
// spelled-out occurred dates.
//
// Entity names come first so an entity-only query still matches even when the
// prose never repeats the name; the dates follow in the same "Month D YYYY"
// shape the reference writes, so a query for a month or year reaches the unit.
// The end date is added only when it differs from the start, so a point event
// contributes one stamp rather than the same one twice. A unit with neither
// entities nor event dates has no signals, and the caller stores NULL.
func buildTextSignals(entities []RetainEntity, start, end *time.Time) string {
	parts := make([]string, 0, len(entities)+2)
	for _, entity := range entities {
		if entity.Text != "" {
			parts = append(parts, entity.Text)
		}
	}
	if start != nil {
		parts = append(parts, textSignalDate(*start))
	}
	if end != nil && (start == nil || !end.Equal(*start)) {
		parts = append(parts, textSignalDate(*end))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}

// textSignalDate formats an event date the way the reference spells it into
// text_signals: the full month name, the day without a leading zero, and the
// four-digit year ("October 5 2026").
func textSignalDate(stamp time.Time) string {
	return strings.ReplaceAll(stamp.Format("January 02 2006"), " 0", " ")
}

// applyItemDates sets the unit's ingest clock from the item's timestamp, and
// its event date as a fallback for a fact that carries none.
//
// An item that supplied an explicit timestamp uses it for both; an item that
// omitted one defaults to the write time; an item that asked for no timestamp
// ("unset") carries no date at all. `occurred_start`/`occurred_end` are never
// taken from the item: they are the extracted fact's account of when the event
// happened, and an item that stated no event must not be dated to ingest
// (which is why `occurred_start` is None for an undated fact while
// `mentioned_at` is always set).
func applyItemDates(unit *MemoryUnit, item RetainItem) {
	switch {
	case item.Timestamp != nil:
		unit.EventDate = item.Timestamp
		unit.MentionedAt = item.Timestamp
	case item.HasTimestamp:
		// Timestamped as "none": the item carries no date by request.
	default:
		now := time.Now().UTC()
		unit.EventDate = &now
		unit.MentionedAt = &now
	}
}

// applyFactDates overrides the unit's dates with the fact's own, when the fact
// carried them. An extracted date is more specific than the item's, so it wins.
func applyFactDates(unit *MemoryUnit, fact ExtractedFact) {
	if start := parseFactTime(fact.OccurredStart); start != nil {
		unit.EventDate = start
		unit.OccurredStart = start
	}
	if end := parseFactTime(fact.OccurredEnd); end != nil {
		unit.OccurredEnd = end
	}
}

// factTimeLayouts are the shapes a fact's occurred timestamp may arrive in.
// The reference reads these with its own ISO reader, which accepts a date with
// no time, a time with no zone (taken as UTC), a space instead of the "T", and
// any fractional-second precision — and a model writes exactly those. Parsing
// only the fully-zoned RFC 3339 form silently dropped the date of every fact
// whose answer was "2015-07-01", so the column stayed NULL where the reference
// stored midnight.
var factTimeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04Z07:00",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

// parseFactTime reads a fact timestamp, returning nil for an absent or
// unparseable value rather than failing the write over a model's bad format. A
// value with no zone is read as UTC, the way the reference's reader does. A
// year-1 or year-9999 value is an open bound a model writes for "since
// forever" / "still ongoing", not a date: it reads as unknown and the column
// stays NULL, the way the reference's fact extraction does
// (retain/fact_extraction.py:3705-3715).
func parseFactTime(value string) *time.Time {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	for _, layout := range factTimeLayouts {
		parsed, err := time.Parse(layout, trimmed)
		if err != nil {
			continue
		}
		if IsPlaceholderDate(parsed) {
			return nil
		}
		utc := parsed.UTC()
		return &utc
	}
	return nil
}

// causalLinksFor returns the caused_by links joining an item's units to the
// earlier units that caused them.
//
// The link points from the effect to its cause, which is the direction the
// contract fixed: a fact records what caused it. An index outside the item's
// unit list is dropped, because it names a unit this write did not store.
func causalLinksFor(bankID string, unitIDs []string, contents []itemContent) []MemoryLink {
	links := make([]MemoryLink, 0)
	for index, content := range contents {
		if index >= len(unitIDs) {
			break
		}
		for _, target := range content.causedByIndexes {
			if target < 0 || target >= len(unitIDs) || target == index {
				continue
			}
			links = append(links, MemoryLink{
				BankID:     bankID,
				FromUnitID: unitIDs[index],
				ToUnitID:   unitIDs[target],
				LinkType:   LinkCausedBy,
				Weight:     1,
			})
		}
	}
	return links
}

// linkTuplesFor returns the temporal links joining consecutive units one item
// produced, so a backend can persist them in its own way. The units come from
// one document and are usually close in time, so the link type is temporal;
// embedding-similar pairs get their own semantic links.
func linkTuplesFor(bankID string, unitIDs []string) []MemoryLink {
	links := make([]MemoryLink, 0, maxInt(len(unitIDs)-1, 0))
	for i := 1; i < len(unitIDs); i++ {
		links = append(links, MemoryLink{
			BankID:     bankID,
			FromUnitID: unitIDs[i-1],
			ToUnitID:   unitIDs[i],
			LinkType:   LinkTemporal,
			Weight:     1,
		})
	}
	return links
}

// semanticLinksFor returns the semantic links joining each unit to the units
// whose embedding is within the similarity floor, capped at semanticLinkFanOut
// per unit.
func semanticLinksFor(bankID string, units []MemoryUnit, minSimilarity float64) []MemoryLink {
	links := make([]MemoryLink, 0)
	for i := range units {
		links = append(links, semanticLinksFrom(bankID, units[i], units, minSimilarity)...)
	}
	return links
}
