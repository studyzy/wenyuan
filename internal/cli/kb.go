package cli

// The `knowledge-base` command family: registration and the read-side
// actions (tree, search, export). The write-side actions live in
// kb_write.go. The bank id is percent-encoded in every path, unlike the
// memory family which passes it through verbatim.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/studyzy/wenyuan/internal/cli/fsengine"
)

// RegisterKBCommands wires the knowledge-base command family.
func RegisterKBCommands(r *Registry) {
	kb := r.Sub("knowledge-base", "inspect and manage a bank's knowledge base")
	registerKBTree(kb.Sub("tree", "show the knowledge-base tree"))
	registerKBSearch(kb.Sub("search", "search a bank's knowledge pages"))
	registerKBExport(kb.Sub("export", "show the knowledge-base export bundle"))
	registerKBCreateFolder(kb.Sub("create-folder", "create a folder"))
	registerKBCreatePage(kb.Sub("create-page", "create a generated page"))
	registerKBGetPage(kb.Sub("get-page", "show one knowledge page"))
	registerKBUpdate(kb.Sub("update", "update a tree node"))
	registerKBDelete(kb.Sub("delete", "delete a node and its subtree"))
}

// kbBase joins the bank id into the knowledge-base base path.
func kbBase(bankID string) string {
	return "/v1/default/banks/" + fsengine.EncodePathSegment(bankID) + "/knowledge-base"
}

// kbNode is one knowledge-base tree node.
type kbNode struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Name      string   `json:"name"`
	Mission   *string  `json:"mission"`
	Managed   bool     `json:"managed"`
	Tags      []string `json:"tags"`
	Timestamp *string  `json:"timestamp"`
	IsStale   *bool    `json:"is_stale"`
	Children  []kbNode `json:"children"`
}

// kbTagsMatch validates one --tags-match value, lowercased.
func kbTagsMatch(value string) (string, error) {
	m := strings.ToLower(value)
	switch m {
	case "any", "all", "any_strict", "all_strict", "exact":
		return m, nil
	}
	return "", NewCommandError("--tags-match must be one of any, all, any_strict, all_strict, exact")
}

// kbTagParams adds the shared tag filter query parameters: one tags param
// per comma-separated item, then the match mode and the verbatim groups.
func kbTagParams(params *url.Values, tags, tagsMatch, tagGroups string) error {
	for _, t := range commaList(tags) {
		params.Add("tags", t)
	}
	if tagsMatch != "" {
		m, err := kbTagsMatch(tagsMatch)
		if err != nil {
			return err
		}
		params.Set("tags_match", m)
	}
	if tagGroups != "" {
		params.Set("tag_groups", tagGroups)
	}
	return nil
}

// kbRenderNodes prints the tree the pretty tree and update views share: one
// line per node at two spaces of indent per depth level.
func kbRenderNodes(ui *UI, nodes []kbNode, depth int) {
	for _, n := range nodes {
		line := strings.Repeat("  ", depth) + n.ID + " " + n.Name
		if n.Kind == "folder" {
			line += "/"
		}
		if n.IsStale != nil && *n.IsStale {
			line += " (stale)"
		}
		ui.Plain("%s", line)
		kbRenderNodes(ui, n.Children, depth+1)
	}
}

func registerKBTree(kb *SubCmd) {
	tags := kb.Flags().String("tags", "", "comma-separated tags to filter by")
	tagsMatch := kb.Flags().String("tags-match", "", "tag match mode: any, all, any_strict, all_strict, or exact")
	tagGroups := kb.Flags().String("tag-groups", "", "tag groups as JSON, passed through verbatim")
	kb.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("knowledge-base tree", d.Args, 1); err != nil {
			return err
		}
		params := url.Values{}
		if err := kbTagParams(&params, *tags, *tagsMatch, *tagGroups); err != nil {
			return err
		}
		path := kbBase(d.Args[0]) + "/tree"
		if enc := params.Encode(); enc != "" {
			path += "?" + enc
		}
		var raw json.RawMessage
		if err := d.Client.Do(ctx, "GET", path, nil, &raw); err != nil {
			return err
		}
		if d.Format.IsMachine() {
			return renderRaw(d, raw)
		}
		var tree struct {
			Roots []kbNode `json:"roots"`
		}
		if err := json.Unmarshal(raw, &tree); err != nil {
			return &DecodeError{Reason: err.Error(), Body: string(raw)}
		}
		ui := d.UI
		ui.Plain("Knowledge Base: %s", d.Args[0])
		if len(tree.Roots) == 0 {
			ui.Plain("  %s", ui.Dim("No folders or pages found."))
			return nil
		}
		kbRenderNodes(ui, tree.Roots, 1)
		return nil
	})
}

func registerKBSearch(kb *SubCmd) {
	var limit triStateInt
	kb.Flags().Var(&limit, "limit", "maximum results (1-50)")
	kb.Flags().Var(&limit, "l", "maximum results (short)")
	tags := kb.Flags().String("tags", "", "comma-separated tags to filter by")
	tagsMatch := kb.Flags().String("tags-match", "", "tag match mode: any, all, any_strict, all_strict, or exact")
	tagGroups := kb.Flags().String("tag-groups", "", "tag groups as JSON, passed through verbatim")
	kb.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("knowledge-base search", d.Args, 2); err != nil {
			return err
		}
		if strings.TrimSpace(d.Args[1]) == "" {
			return NewCommandError("search query must not be empty")
		}
		if limit.set && limit.value < 1 {
			return NewCommandError("invalid --limit %d: expected a value between 1 and 50", limit.value)
		}
		params := url.Values{}
		// An omitted --limit leaves the server default in place, as the
		// reference does.
		if limit.set {
			params.Set("limit", fmt.Sprintf("%d", limit.value))
		}
		params.Set("q", d.Args[1])
		if err := kbTagParams(&params, *tags, *tagsMatch, *tagGroups); err != nil {
			return err
		}
		var raw json.RawMessage
		if err := d.Client.Do(ctx, "GET", kbBase(d.Args[0])+"/search?"+params.Encode(), nil, &raw); err != nil {
			return err
		}
		if d.Format.IsMachine() {
			return renderRaw(d, raw)
		}
		var page struct {
			Results []struct {
				ID      string  `json:"id"`
				Name    string  `json:"name"`
				Score   float64 `json:"score"`
				Snippet string  `json:"snippet"`
			} `json:"results"`
			Total int `json:"total"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return &DecodeError{Reason: err.Error(), Body: string(raw)}
		}
		ui := d.UI
		ui.Plain("Search Results: %s", d.Args[0])
		if len(page.Results) == 0 {
			ui.Plain("  No pages matched.")
			return nil
		}
		for _, hit := range page.Results {
			ui.Plain("  %s %s (%.3f)", hit.ID, hit.Name, hit.Score)
			ui.Plain("    %s", Truncate(hit.Snippet, 100))
			ui.Plain("")
		}
		return nil
	})
}

func registerKBExport(kb *SubCmd) {
	kb.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("knowledge-base export", d.Args, 1); err != nil {
			return err
		}
		var raw json.RawMessage
		if err := d.Client.Do(ctx, "GET", kbBase(d.Args[0])+"/export", nil, &raw); err != nil {
			return err
		}
		if d.Format.IsMachine() {
			return renderRaw(d, raw)
		}
		var bundle struct {
			Files []struct {
				Path string `json:"path"`
			} `json:"files"`
		}
		if err := json.Unmarshal(raw, &bundle); err != nil {
			return &DecodeError{Reason: err.Error(), Body: string(raw)}
		}
		ui := d.UI
		ui.Plain("Knowledge Base Bundle: %s", d.Args[0])
		for _, file := range bundle.Files {
			ui.Plain("  %s", file.Path)
		}
		ui.Plain("")
		// The hint names this CLI, so the upstream wording is adapted to
		// the wenyuan spelling.
		ui.Plain("Use --output json to get the file contents, or 'wenyuan-admin fs mount' to mirror them to disk.")
		return nil
	})
}
