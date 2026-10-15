package fsengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// pageModeReadOnly is the mode mirrored files carry unless the mount is
// writable; pageModeWritable is the writable alternative.
const (
	pageModeReadOnly  = 0o444
	pageModeWritable  = 0o644
	pageModeUnlinkTmp = 0o644
)

// Options are the fixed inputs of one sync pass.
type Options struct {
	// Dir is the mount root (absolute).
	Dir string
	// BankID is the mirrored bank.
	BankID string
	// APIURL is recorded into the state file.
	APIURL string
	// Writable selects 0644 instead of 0444 for mirrored files.
	Writable bool
}

// Result counts what one successful pass did.
type Result struct {
	Pages     int
	Folders   int
	Written   int
	Unchanged int
	Removed   int
	Reverted  int
}

// Sync runs one mirror pass: fetch the tree and export snapshots, render
// the plan, write drifted files, regenerate index.md, prune removed pages
// and folders, then stamp the state file. A fetch failure records itself
// into the state and leaves the on-disk mirror untouched.
func Sync(ctx context.Context, f *Fetcher, o Options) (Result, error) {
	var res Result
	state := LoadState(o.Dir, o.BankID, o.APIURL)
	tree, err := f.FetchTree(ctx, o.BankID)
	if err != nil {
		return res, recordSyncFailure(o.Dir, state, err)
	}
	docs, err := f.FetchExport(ctx, o.BankID)
	if err != nil {
		return res, recordSyncFailure(o.Dir, state, err)
	}

	plan := BuildPlan(tree)
	mode := fsFileMode(pageModeReadOnly)
	if o.Writable {
		mode = fsFileMode(pageModeWritable)
	}
	for _, dir := range plan.Dirs {
		if err := os.MkdirAll(filepath.Join(o.Dir, dir), 0o755); err != nil {
			return res, err
		}
	}

	hashes := map[string]string{}
	for _, page := range plan.Pages {
		content := renderedPage(page, docs)
		hashes[page.Rel] = contentHash(content)
		target := filepath.Join(o.Dir, page.Rel)
		if existing, err := os.ReadFile(target); err == nil && string(existing) == content {
			res.Unchanged++
			_ = applyMode(target, mode)
			continue
		}
		if err := writeFileAtomic(target, []byte(content), mode); err != nil {
			return res, err
		}
		if prev, ok := state.Files[page.Rel]; ok && prev.Hash == hashes[page.Rel] {
			res.Reverted++
		} else {
			res.Written++
		}
	}

	index := RenderIndex(o.BankID, o.APIURL, plan)
	if err := os.MkdirAll(ControlDir(o.Dir), 0o755); err != nil {
		return res, err
	}
	if err := writeFileAtomic(IndexPath(o.Dir), []byte(index), mode); err != nil {
		return res, err
	}

	res.Removed = pruneFiles(o.Dir, state, plan)
	pruneDirs(o.Dir, state, plan)

	newState := State{
		Version:    1,
		BankID:     o.BankID,
		APIURL:     o.APIURL,
		LastSyncAt: strPtr(NowStamp()),
		LastSyncOk: true,
		Files:      map[string]FileRecord{},
		Dirs:       append([]string(nil), plan.Dirs...),
	}
	for _, page := range plan.Pages {
		newState.Files[page.Rel] = FileRecord{File: page.Rel, Hash: hashes[page.Rel]}
	}
	if err := SaveState(o.Dir, newState); err != nil {
		return res, err
	}
	res.Pages = plan.PageCount
	res.Folders = plan.FolderCount
	return res, nil
}

// renderedPage picks a page's document: the export snapshot's markdown
// verbatim with a trailing newline ensured, or the placeholder document.
func renderedPage(page PagePlan, docs map[string]string) string {
	doc, ok := docs[page.ID]
	if ok && strings.TrimSpace(doc) != "" {
		if !strings.HasSuffix(doc, "\n") {
			return doc + "\n"
		}
		return doc
	}
	return PlaceholderDoc(page)
}

// contentHash is the lowercase sha256 hex of the rendered content.
func contentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// recordSyncFailure stamps a failed pass into the state file and returns
// the original error. Nothing on disk is pruned.
func recordSyncFailure(dir string, state State, syncErr error) error {
	msg := syncErr.Error()
	state.LastSyncAt = strPtr(NowStamp())
	state.LastSyncOk = false
	state.LastError = &msg
	_ = SaveState(dir, state)
	return syncErr
}

// pruneFiles removes state-tracked files the fresh plan no longer contains
// and returns how many were removed.
func pruneFiles(dir string, state State, plan *Plan) int {
	planned := map[string]bool{}
	for _, page := range plan.Pages {
		planned[page.Rel] = true
	}
	removed := 0
	for rel := range state.Files {
		if planned[rel] {
			continue
		}
		removeFile(filepath.Join(dir, rel))
		removed++
	}
	return removed
}

// pruneDirs attempts to remove previously created directories that left the
// plan, deepest first and via plain rmdir only, so a non-empty directory
// holding user files survives.
func pruneDirs(dir string, state State, plan *Plan) {
	planned := map[string]bool{}
	for _, d := range plan.Dirs {
		planned[d] = true
	}
	var stale []string
	for _, d := range state.Dirs {
		if !planned[d] {
			stale = append(stale, d)
		}
	}
	sort.Slice(stale, func(i, j int) bool {
		depthI := strings.Count(stale[i], "/")
		depthJ := strings.Count(stale[j], "/")
		if depthI != depthJ {
			return depthI > depthJ
		}
		return stale[i] > stale[j]
	})
	for _, d := range stale {
		_ = os.Remove(filepath.Join(dir, d))
	}
}

// writeFileAtomic writes content to a pid-suffixed temp file in the target
// directory, renames it onto the target — which replaces a read-only target
// on Unix — and then asserts the configured mode.
func writeFileAtomic(target string, content []byte, mode osMode) error {
	tmp := fmt.Sprintf("%s.tmp.%d", target, os.Getpid())
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return applyMode(target, mode)
}

// removeFile unlinks one mirrored file: chmod to a writable mode first,
// then remove. Removal failures are ignored.
func removeFile(path string) {
	_ = os.Chmod(path, pageModeUnlinkTmp)
	_ = os.Remove(path)
}

func strPtr(s string) *string { return &s }
