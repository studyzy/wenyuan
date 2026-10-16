package config

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The shipped configuration artifacts — the YAML template, the dotenv
// template and the generated reference — name environment keys. A key no
// resolver ever asks for would be a silently dead line, so each artifact is
// checked against the set of keys the resolver actually reads: the two
// probe passes over FromEnv with a recording lookup.
func knownResolverKeys(t *testing.T) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for _, k := range referenceRuntimeKeys {
		names[k.Name] = true
	}
	for _, keys := range providerFamilyKeys {
		for _, key := range keys {
			names[key] = true
		}
	}
	record := func(lookup func(string) (string, bool)) {
		seen := map[string]bool{}
		func() {
			defer func() { _ = recover() }()
			FromEnv(func(key string) (string, bool) {
				if key != "" && !seen[key] {
					seen[key] = true
					names[key] = true
				}
				return lookup(key)
			})
		}()
	}
	record(func(string) (string, bool) { return "", false })
	// The same bounded answer the inventory probe uses: family keys the
	// resolver declares, plus exactly the first indexed member of each chain.
	// An open-ended "_PROVIDER" answer here would feed the resolver an
	// infinitely long chain, and this test would never return.
	populated := map[string]string{}
	for _, k := range referenceRuntimeKeys {
		populated[k.Name] = k.Default
	}
	for _, keys := range providerFamilyKeys {
		for _, key := range keys {
			populated[key] = ""
		}
	}
	record(func(key string) (string, bool) {
		if value, ok := populated[key]; ok && value != "" {
			return value, true
		}
		if !strings.HasSuffix(key, "_PROVIDER") {
			return "", false
		}
		if _, ok := populated[key]; ok {
			return "openai", true
		}
		if strings.HasSuffix(key, "LLM_1_PROVIDER") || strings.HasSuffix(key, "RERANKER_1_PROVIDER") {
			return "openai", true
		}
		return "", false
	})
	return names
}

// The shipped YAML template is nested: each underscore in an environment name
// is one nesting level (llm: {provider: ...} answers llm_provider). The
// extractor keeps an indentation path stack and reconstructs the flat key
// every commented line answers. A line qualifies as a template line only when
// its value is a plain scalar or empty, which excludes the header prose
// ("lowercased: HINDSIGHT_API_PORT is `port` here" carries a sentence, and
// "Quote a value..." starts with an uppercase word).
func newNestedYAMLKeyReader() func(string) []string {
	type frame struct {
		indent int
		part   string
	}
	var stack []frame
	return func(line string) []string {
		m := nestedKeyLine.FindStringSubmatch(line)
		if m == nil {
			return nil
		}
		// The nesting level is the indentation before the `#`, not the
		// single space the template puts after it.
		indent := len(m[1])
		part := m[2]
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, frame{indent: indent, part: part})
		// A group root (no value after the colon) is only path context: the
		// flat keys the resolver asks for are the leaves beneath it.
		if m[3] == "" {
			return nil
		}
		words := make([]string, 0, len(stack))
		for _, f := range stack {
			words = append(words, f.part)
		}
		flat := strings.Join(words, "_")
		return []string{strings.ToUpper(envPrefix + "_" + flat)}
	}
}

var nestedKeyLine = regexp.MustCompile(`^(\s*)#(?:\s*)([a-z][a-z0-9_]*)\s*:\s*("[^"]*"|[A-Za-z0-9_.\-/:]*)\s*(#.*)?$`)

var envKeyLine = regexp.MustCompile(`^#?\s*(HINDSIGHT_API_[A-Z0-9_]+)=`)

var docKeyCell = regexp.MustCompile(`\|( ` + "`HINDSIGHT_API_[A-Z0-9_]+`" + `) `)

func checkArtifactKeys(t *testing.T, path string, extract func(string) []string, known map[string]bool) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		for _, key := range extract(scanner.Text()) {
			if !known[key] {
				t.Errorf("%s:%d names %s, which no resolver branch reads", path, lineNo, key)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
}

func envKeys(line string) []string {
	m := envKeyLine.FindStringSubmatch(line)
	if m == nil {
		return nil
	}
	return m[1:]
}

func docKeys(line string) []string {
	var keys []string
	for _, m := range docKeyCell.FindAllStringSubmatch(line, -1) {
		keys = append(keys, strings.Trim(m[1], "` "))
	}
	return keys
}

func TestShippedArtifactsNameOnlyKnownKeys(t *testing.T) {
	known := knownResolverKeys(t)
	checkArtifactKeys(t, filepath.Join("..", "..", "etc", "example.wenyuan.yaml"), newNestedYAMLKeyReader(), known)
	checkArtifactKeys(t, filepath.Join("..", "..", ".env.example"), envKeys, known)
	checkArtifactKeys(t, filepath.Join("..", "..", "docs", "CONFIGURATION.md"), docKeys, known)
}

// Both templates are inert as shipped: every line is a comment, so copying
// either one changes nothing until the operator edits it.
func TestShippedTemplatesAreInert(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", "..", "etc", "example.wenyuan.yaml"),
		filepath.Join("..", "..", ".env.example"),
	} {
		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("open %s: %v", path, err)
		}
		scanner := bufio.NewScanner(f)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			if line := strings.TrimSpace(scanner.Text()); line != "" && !strings.HasPrefix(line, "#") {
				t.Errorf("%s:%d is active; a shipped template must be fully commented", path, lineNo)
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatalf("scan %s: %v", path, err)
		}
		_ = f.Close()
	}
}
