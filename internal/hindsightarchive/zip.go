package hindsightarchive

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"time"
)

// OpenZip parses an export archive held in a .zip file. The returned Archive
// is independent of the zip reader, which is closed before returning.
func OpenZip(path string) (*Archive, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open archive %s: %w", path, err)
	}
	defer reader.Close()
	return Read(&zipFS{files: reader.File})
}

// zipFS adapts a zip file list to a minimal read-only fs.FS.
type zipFS struct {
	files []*zip.File
}

func (z *zipFS) Open(name string) (fs.File, error) {
	for _, file := range z.files {
		if file.Name == name {
			rc, err := file.Open()
			if err != nil {
				return nil, err
			}
			return &zipFile{name: name, size: int64(file.UncompressedSize64), rc: rc}, nil
		}
	}
	return nil, fs.ErrNotExist
}

func (z *zipFS) ReadDir(name string) ([]fs.DirEntry, error) {
	prefix := name
	if prefix != "" && prefix != "." {
		prefix += "/"
	}
	seen := map[string]bool{}
	var entries []fs.DirEntry
	for _, file := range z.files {
		rest, ok := trimPrefix(file.Name, prefix)
		if !ok || rest == "" {
			continue
		}
		// Only direct children: stop at the next path separator.
		if idx := indexByte(rest, '/'); idx >= 0 {
			rest = rest[:idx]
			if seen[rest] {
				continue
			}
			seen[rest] = true
			entries = append(entries, dirEntry{name: rest, dir: true})
			continue
		}
		if seen[rest] {
			continue
		}
		seen[rest] = true
		entries = append(entries, dirEntry{name: rest, size: int64(file.UncompressedSize64)})
	}
	return entries, nil
}

func (z *zipFS) ReadFile(name string) ([]byte, error) {
	file, err := z.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

type zipFile struct {
	name string
	size int64
	rc   io.ReadCloser
}

func (f *zipFile) Stat() (fs.FileInfo, error) { return fileInfo{name: f.name, size: f.size}, nil }
func (f *zipFile) Read(p []byte) (int, error) { return f.rc.Read(p) }
func (f *zipFile) Close() error               { return f.rc.Close() }

type dirEntry struct {
	name string
	dir  bool
	size int64
}

func (d dirEntry) Name() string      { return d.name }
func (d dirEntry) IsDir() bool       { return d.dir }
func (d dirEntry) Type() fs.FileMode { return 0 }
func (d dirEntry) Info() (fs.FileInfo, error) {
	return fileInfo{name: d.name, dir: d.dir, size: d.size}, nil
}

type fileInfo struct {
	name string
	dir  bool
	size int64
}

func (i fileInfo) Name() string           { return i.name }
func (i fileInfo) Size() int64            { return i.size }
func (i fileInfo) Mode() fs.FileMode      { return 0 }
func (i fileInfo) ModTime() (t time.Time) { return time.Time{} }
func (i fileInfo) IsDir() bool            { return i.dir }
func (i fileInfo) Sys() any               { return nil }

func trimPrefix(s, prefix string) (string, bool) {
	if prefix == "" {
		return s, true
	}
	if len(s) < len(prefix) || s[:len(prefix)] != prefix {
		return "", false
	}
	return s[len(prefix):], true
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
