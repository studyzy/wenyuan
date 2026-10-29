package storage

import (
	"unicode/utf8"
)

// FnmatchCase is Python's fnmatch.fnmatchcase for a tag-glob pattern: `*`
// matches any run of characters, `?` exactly one character, `[seq]`/`[!seq]`
// character classes with ranges, case-sensitive throughout. The observation
// scope globs treat any entry containing `*` as a glob under these rules
// (the reference's _expand_scope_globs, consolidator.py:509-541), so every
// scope matcher in the service resolves entries through this one predicate —
// consolidation's scope expansion and the document-scope cascade must agree
// on what a pattern matches.
func FnmatchCase(pattern, name string) bool {
	// Greedy scan with backtracking at the most recent star.
	px, nx := 0, 0
	star, mark := -1, 0
	for nx < len(name) {
		if px < len(pattern) {
			if pattern[px] == '*' {
				star, mark = px, nx
				px++
				continue
			}
			if patternAdvance, nameAdvance, ok := matchGlobToken(pattern, px, name[nx:]); ok {
				px += patternAdvance
				nx += nameAdvance
				continue
			}
		}
		if star >= 0 {
			px = star + 1
			_, size := utf8.DecodeRuneInString(name[mark:])
			mark += size
			nx = mark
			continue
		}
		return false
	}
	for px < len(pattern) && pattern[px] == '*' {
		px++
	}
	return px == len(pattern)
}

// matchGlobToken matches one pattern token against the rune name begins with:
// a literal byte, `?` for any one character, or a character class. It reports
// how far each side advances and whether the token matched; a `[` that never
// closes falls back to the literal compare, which is what fnmatch does with it.
func matchGlobToken(pattern string, px int, name string) (patternAdvance, nameAdvance int, ok bool) {
	switch c := pattern[px]; c {
	case '?':
		_, size := utf8.DecodeRuneInString(name)
		return 1, size, true
	case '[':
		if matched, next := matchScopeClass(pattern, px, name); next > px {
			if !matched {
				return 0, 0, false
			}
			_, size := utf8.DecodeRuneInString(name)
			return next - px, size, true
		}
	}
	if name[0] == pattern[px] {
		return 1, 1, true
	}
	return 0, 0, false
}

// matchScopeClass matches a fnmatch character class — pattern[at] is the '[' —
// against the rune name begins with. It reports whether that rune is in (or,
// negated, out of) the class and where the pattern continues after the closing
// `]`; next == at marks a `[` with no closing `]`, which is a literal and not
// a class.
func matchScopeClass(pattern string, at int, name string) (matched bool, next int) {
	// fnmatch.translate scans for the close the same way: a leading `!` marks
	// negation and a `]` right after `[` (or after the `!`) is a member, not
	// the close.
	j := at + 1
	if j < len(pattern) && pattern[j] == '!' {
		j++
	}
	if j < len(pattern) && pattern[j] == ']' {
		j++
	}
	for j < len(pattern) && pattern[j] != ']' {
		j++
	}
	if j >= len(pattern) {
		return false, at
	}
	body := pattern[at+1 : j]
	negate := len(body) > 0 && body[0] == '!'
	r, _ := utf8.DecodeRuneInString(name)
	if negate {
		body = body[1:]
	}
	return classMember(body, r) != negate, j + 1
}

// classMember reports whether r is one of the body's members: single runes and
// lo-hi ranges, the way the character class fnmatch.translate emits reads them.
// A `-` between two members is a range; anywhere else it is a member of its own.
func classMember(body string, r rune) bool {
	for k := 0; k < len(body); {
		lo, size := utf8.DecodeRuneInString(body[k:])
		k += size
		if k+1 < len(body) && body[k] == '-' && body[k+1] != ']' {
			hi, hiSize := utf8.DecodeRuneInString(body[k+1:])
			k += 1 + hiSize
			if r >= lo && r <= hi {
				return true
			}
			continue
		}
		if r == lo {
			return true
		}
	}
	return false
}
