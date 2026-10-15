// Package fsengine implements the knowledge-base disk mirror: the pure
// logic behind the `fs` command family. It owns the local layout, the
// control-directory files, the two-request fetch of the tree and export
// snapshots, and the one-way API-to-disk sync pass. Command wiring and user
// interaction stay in the parent cli package.
package fsengine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ControlDirName is the control directory held inside every mount.
const ControlDirName = ".wenyuan-fs"

// Control-directory file names.
const (
	stateFileName  = "state.json"
	configFileName = "config.json"
	pidFileName    = "daemon.pid"
	logFileName    = "daemon.log"
	indexFileName  = "index.md"
)

// ControlDir returns <mount>/.wenyuan-fs.
func ControlDir(mount string) string { return filepath.Join(mount, ControlDirName) }

// StatePath returns the control directory's state file.
func StatePath(mount string) string { return filepath.Join(ControlDir(mount), stateFileName) }

// ConfigPath returns the control directory's saved configuration file.
func ConfigPath(mount string) string { return filepath.Join(ControlDir(mount), configFileName) }

// PIDPath returns the control directory's daemon pid record.
func PIDPath(mount string) string { return filepath.Join(ControlDir(mount), pidFileName) }

// LogPath returns the control directory's daemon log file.
func LogPath(mount string) string { return filepath.Join(ControlDir(mount), logFileName) }

// IndexPath returns the control directory's generated index document.
func IndexPath(mount string) string { return filepath.Join(ControlDir(mount), indexFileName) }

// writeControlFile writes one pretty-printed JSON control file with the
// trailing newline every control file carries.
func writeControlFile(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Config is the saved mirror configuration in <dir>/.wenyuan-fs/config.json.
// Unlike every other control file it uses snake_case keys.
type Config struct {
	APIURL          string `json:"api_url"`
	APIToken        string `json:"api_token,omitempty"`
	BankID          string `json:"bank_id"`
	IntervalSeconds int    `json:"interval_seconds"`
	Writable        bool   `json:"writable"`
}

// SaveConfig persists the mirror configuration, creating the control
// directory when missing.
func SaveConfig(mount string, cfg Config) error {
	return writeControlFile(ConfigPath(mount), cfg)
}

// LoadConfig reads the saved configuration. A missing or unreadable file
// yields the zero Config; callers treat empty fields as unset.
func LoadConfig(mount string) Config {
	var cfg Config
	data, err := os.ReadFile(ConfigPath(mount))
	if err != nil || json.Unmarshal(data, &cfg) != nil {
		return Config{}
	}
	return cfg
}

// FileRecord is one state-tracked mirrored page: its relative path and the
// sha256 hex of the rendered content.
type FileRecord struct {
	File string `json:"file"`
	Hash string `json:"hash"`
}

// State is <dir>/.wenyuan-fs/state.json. Keys are camelCase.
type State struct {
	Version    int                   `json:"version"`
	BankID     string                `json:"bankId"`
	APIURL     string                `json:"apiUrl"`
	LastSyncAt *string               `json:"lastSyncAt"`
	LastSyncOk bool                  `json:"lastSyncOk"`
	LastError  *string               `json:"lastError"`
	Files      map[string]FileRecord `json:"files"`
	Dirs       []string              `json:"dirs"`
}

// LoadState reads the state file. A missing, unreadable or wrong-version
// file falls back to an empty state seeded with the current bank and API
// values, matching the reference behavior.
func LoadState(mount, bankID, apiURL string) State {
	st := State{
		Version: 1,
		BankID:  bankID,
		APIURL:  apiURL,
		Files:   map[string]FileRecord{},
		Dirs:    []string{},
	}
	data, err := os.ReadFile(StatePath(mount))
	if err != nil {
		return st
	}
	var parsed State
	if json.Unmarshal(data, &parsed) != nil || parsed.Version != 1 {
		return st
	}
	if parsed.Files == nil {
		parsed.Files = map[string]FileRecord{}
	}
	if parsed.Dirs == nil {
		parsed.Dirs = []string{}
	}
	return parsed
}

// SaveState persists the state file with its directory list sorted.
func SaveState(mount string, st State) error {
	sort.Strings(st.Dirs)
	if st.Files == nil {
		st.Files = map[string]FileRecord{}
	}
	return writeControlFile(StatePath(mount), st)
}

// PIDRecord is <dir>/.wenyuan-fs/daemon.pid, written by the starting
// process right after spawning the daemon.
type PIDRecord struct {
	PID             int    `json:"pid"`
	BankID          string `json:"bankId"`
	IntervalSeconds int    `json:"intervalSeconds"`
	StartedAt       string `json:"startedAt"`
}

// SavePID persists the daemon pid record.
func SavePID(mount string, rec PIDRecord) error {
	return writeControlFile(PIDPath(mount), rec)
}

// LoadPID reads the daemon pid record; ok is false when the file is missing
// or unreadable, which callers treat as no daemon registered.
func LoadPID(mount string) (PIDRecord, bool) {
	var rec PIDRecord
	data, err := os.ReadFile(PIDPath(mount))
	if err != nil || json.Unmarshal(data, &rec) != nil {
		return PIDRecord{}, false
	}
	return rec, true
}

// EncodePathSegment percent-encodes one path segment, keeping the
// unreserved set A-Za-z0-9-_.~ and escaping everything else.
func EncodePathSegment(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			const hex = "0123456789ABCDEF"
			b.WriteString("%")
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0F])
		}
	}
	return b.String()
}

// TimestampFormat is the control-file timestamp layout: UTC RFC 3339 with
// millisecond precision and a Z suffix.
const TimestampFormat = "2006-01-02T15:04:05.000Z"

// NowStamp returns the current control-file timestamp.
func NowStamp() string { return time.Now().UTC().Format(TimestampFormat) }
