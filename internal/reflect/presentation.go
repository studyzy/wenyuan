package reflect

import (
	"fmt"
	"regexp"
	"strings"
)

// How a retrieval result is written into the model's prompt.
//
// A tool returns a full record; the model only needs the text and something it
// can cite. So each item is presented under a short alias — f1 for a fact, o1
// for an observation, p1 for a perspective, c1 for a chunk — instead of the
// 36-character identifier it really carries. The aliases exist for one
// reflection only: none may reach the caller, so an alias the model echoes back
// is resolved to the real identifier before anything reads it, and an
// identifier the model was never shown is not a citation at all.
const (
	aliasFact        = "f"
	aliasObservation = "o"
	aliasPerspective = "p"
	aliasChunk       = "c"
)

// unverifiedID is what an identifier the model was never shown is replaced
// with, so a made-up or garbled citation cannot pose as a real one.
const unverifiedID = "[unverified id]"

// uuidPattern matches a UUID in the model's answer.
var uuidPattern = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)

// aliasPattern matches an alias token in the model's answer.
var aliasPattern = regexp.MustCompile(`\b[fopc]\d+\b`)

// aliaser assigns and resolves the short identifiers one reflection uses.
type aliaser struct {
	byID    map[string]string
	seen    map[string]bool
	counter map[string]int
}

// newAliaser builds an aliaser.
func newAliaser() *aliaser {
	return &aliaser{
		byID:    map[string]string{},
		seen:    map[string]bool{},
		counter: map[string]int{},
	}
}

// prefixFor maps an evidence kind onto its alias prefix.
func prefixFor(kind string) string {
	switch kind {
	case "observation":
		return aliasObservation
	case "mental_model", "perspective":
		return aliasPerspective
	case "chunk":
		return aliasChunk
	default:
		return aliasFact
	}
}

// aliasFor names an identifier under the prefix its kind uses.
func (a *aliaser) aliasFor(kind, id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	prefix := prefixFor(kind)
	if existing, ok := a.byID[id]; ok {
		return existing
	}
	name := prefix + fmt.Sprint(a.next(prefix))
	a.byID[id] = name
	a.seen[name] = true
	return name
}

// next returns the next ordinal for a prefix, counted from one.
func (a *aliaser) next(prefix string) int {
	a.counter[prefix]++
	return a.counter[prefix]
}

// resolve rewrites every alias in the text back to the identifier it stands
// for, so no alias reaches the caller.
func (a *aliaser) resolve(text string) string {
	if len(a.byID) == 0 || !aliasPattern.MatchString(text) {
		return text
	}
	reverse := map[string]string{}
	for id, alias := range a.byID {
		reverse[alias] = id
	}
	return aliasPattern.ReplaceAllStringFunc(text, func(token string) string {
		if id, ok := reverse[token]; ok {
			return id
		}
		return token
	})
}

// scrubUnverifiedIDs replaces a UUID in the model's answer that the model was
// never shown — it made it up or garbled it — so it cannot pose as a citation.
// An identifier that was retrieved passes untouched.
func (a *aliaser) scrubUnverifiedIDs(text string) string {
	if !uuidPattern.MatchString(text) {
		return text
	}
	known := map[string]bool{}
	for id := range a.byID {
		known[strings.ToLower(id)] = true
	}
	return uuidPattern.ReplaceAllStringFunc(text, func(token string) string {
		if known[strings.ToLower(token)] {
			return token
		}
		return unverifiedID
	})
}

// renderEvidenceLine writes one evidence item for the prompt: its alias, the
// source chunk under its own alias when known, then the text. A perspective that
// arrived as a search snippet is shown as the snippet, not the whole page, so
// the model reads the page it wants with read_mental_models.
func (l *Loop) renderEvidenceLine(item Evidence) string {
	alias := l.aliases.aliasFor(item.Kind, item.ID)
	var b strings.Builder
	b.WriteByte('[')
	b.WriteString(alias)
	b.WriteByte(']')
	if item.ChunkID != "" {
		b.WriteString(" (chunk ")
		b.WriteString(l.aliases.aliasFor("chunk", item.ChunkID))
		b.WriteByte(')')
	}
	text := item.Text
	if item.SnippetOnly {
		text = knowledgeSnippet(text)
	}
	if text != "" {
		b.WriteByte(' ')
		b.WriteString(text)
	}
	return b.String()
}
