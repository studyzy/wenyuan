package fsengine

import (
	"sort"
	"strings"
)

// Node is one knowledge-base tree node as the tree endpoint answers it.
type Node struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Name      string   `json:"name"`
	Mission   *string  `json:"mission"`
	Managed   bool     `json:"managed"`
	Tags      []string `json:"tags"`
	Timestamp *string  `json:"timestamp"`
	IsStale   *bool    `json:"is_stale"`
	Children  []Node   `json:"children"`
}

// Tree is the tree endpoint's payload.
type Tree struct {
	Roots []Node `json:"roots"`
}

// Outline is one entry of the index.md outline: a folder or a page with its
// tree position and rendered metadata.
type Outline struct {
	Kind     string // "folder" or "page"
	Name     string
	Mission  string
	Managed  bool
	Rel      string // folder: directory path; page: path without the .md suffix
	Children []*Outline
}

// PagePlan is one mirrored page: its id, tree metadata and on-disk path
// relative to the mount root, including the .md suffix.
type PagePlan struct {
	ID        string
	Rel       string
	Name      string
	Tags      []string
	Timestamp *string
}

// Plan is the mirror layout derived from one tree snapshot.
type Plan struct {
	Dirs        []string   // relative directories, parents first in tree order
	Pages       []PagePlan // tree order
	PagePaths   map[string]string
	Outline     []*Outline
	FolderCount int
	PageCount   int
}

// BuildPlan turns a tree snapshot into the mirror layout. Siblings are
// emitted sorted by name byte-wise, and each sibling's path segment is
// deduplicated within its parent: the first taker keeps the plain slug and
// later collisions append an id-derived suffix.
func BuildPlan(tree *Tree) *Plan {
	p := &Plan{PagePaths: map[string]string{}}
	buildPlanLevel(tree.Roots, "", p)
	return p
}

func buildPlanLevel(nodes []Node, dirRel string, p *Plan) {
	sorted := append([]Node(nil), nodes...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	type assigned struct {
		node Node
		seg  string
	}
	segs := make([]assigned, len(sorted))
	used := map[string]bool{}
	for i, n := range sorted {
		base := Slugify(n.Name)
		seg := base
		if used[seg] {
			seg = base + "-" + IDSuffix(n.ID)
			for used[seg] {
				seg += "-x"
			}
		}
		used[seg] = true
		segs[i] = assigned{node: n, seg: seg}
	}

	for _, a := range segs {
		n, seg := a.node, a.seg
		if n.Kind == "folder" {
			rel := seg
			if dirRel != "" {
				rel = dirRel + "/" + seg
			}
			p.Dirs = append(p.Dirs, rel)
			p.FolderCount++
			o := &Outline{Kind: "folder", Name: n.Name, Mission: derefString(n.Mission), Rel: rel}
			p.Outline = append(p.Outline, o)
			buildPlanLevel(n.Children, rel, p)
			continue
		}
		fileRel := seg + ".md"
		if dirRel != "" {
			fileRel = dirRel + "/" + seg + ".md"
		}
		p.Pages = append(p.Pages, PagePlan{
			ID: n.ID, Rel: fileRel, Name: n.Name,
			Tags: n.Tags, Timestamp: n.Timestamp,
		})
		p.PagePaths[n.ID] = fileRel
		p.PageCount++
		p.Outline = append(p.Outline, &Outline{
			Kind: "page", Name: n.Name, Managed: n.Managed,
			Rel: strings.TrimSuffix(fileRel, ".md"),
		})
	}
}

// Slugify renders a node name as a path segment: lowercased, every run of
// characters outside [a-z0-9._-] collapsed to one dash, edge dashes
// trimmed, dot runs collapsed, and an empty result renamed to untitled.
func Slugify(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(name) {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
		if ok {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	for strings.Contains(s, "..") {
		s = strings.ReplaceAll(s, "..", ".")
	}
	if s == "" {
		return "untitled"
	}
	return s
}

// IDSuffix returns the collision suffix for a node id: up to the last six
// alphanumeric characters of the id, or "x" when it has none.
func IDSuffix(id string) string {
	var alnum []rune
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			alnum = append(alnum, r)
		}
	}
	if len(alnum) == 0 {
		return "x"
	}
	if len(alnum) > 6 {
		alnum = alnum[len(alnum)-6:]
	}
	return string(alnum)
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// PlaceholderDoc renders the document a page gets when the export snapshot
// carries no content for it: front-matter, a blank line and the
// not-generated notice.
func PlaceholderDoc(p PagePlan) string {
	var ts any
	if p.Timestamp != nil {
		ts = *p.Timestamp
	}
	tags := p.Tags
	if tags == nil {
		tags = []string{}
	}
	fm := FrontMatter([]FMField{
		{Key: "id", Value: p.ID},
		{Key: "type", Value: "knowledge-page"},
		{Key: "title", Value: p.Name},
		{Key: "tags", Value: tags},
		{Key: "timestamp", Value: ts},
	})
	return fm + "\n\n_This page has not been generated yet._\n"
}

// RenderIndex renders the control directory's index.md: front-matter with
// the bank, API URL and counts, then a markdown outline of the tree.
func RenderIndex(bankID, apiURL string, p *Plan) string {
	fm := FrontMatter([]FMField{
		{Key: "bank", Value: bankID},
		{Key: "api_url", Value: apiURL},
		{Key: "folders", Value: p.FolderCount},
		{Key: "pages", Value: p.PageCount},
	})
	var b strings.Builder
	b.WriteString(fm)
	b.WriteString("\n\n")
	b.WriteString("# Knowledge base — `" + bankID + "`\n\n")
	if len(p.Outline) == 0 {
		b.WriteString("_This bank has no knowledge base yet._\n")
		return b.String()
	}
	renderOutline(&b, p.Outline, 0)
	return b.String()
}

func renderOutline(b *strings.Builder, nodes []*Outline, depth int) {
	indent := strings.Repeat("  ", depth)
	for _, n := range nodes {
		switch n.Kind {
		case "folder":
			b.WriteString(indent + "- **" + n.Name + "/**")
			if n.Mission != "" {
				b.WriteString(" — _" + n.Mission + "_")
			}
		default:
			b.WriteString(indent + "- [`" + n.Name + "`](../" + n.Rel + ".md)")
			if n.Managed {
				b.WriteString(" ·auto")
			}
		}
		b.WriteByte('\n')
		renderOutline(b, n.Children, depth+1)
	}
}
