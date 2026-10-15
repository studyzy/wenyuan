package monitoring

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
)

// JSON records carry the reference's keys and its spelling of a level, so a
// pipeline configured for the reference parses ours unchanged.
func TestJSONLogShape(t *testing.T) {
	var out bytes.Buffer
	previous := LogSink
	LogSink = &out
	defer func() { LogSink = previous }()

	cfg := config.FromEnv(func(string) (string, bool) { return "", false })
	cfg.LogFormat = "json"
	logger := slog.New(NewLogHandler(cfg))
	logger.Info("served a request", "path", "/v1/version")
	logger.Warn("degraded")

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %q", out.String())
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("record is not JSON: %v (%q)", err, lines[0])
	}
	for _, key := range []string{"severity", "message", "timestamp"} {
		if _, ok := record[key]; !ok {
			t.Fatalf("record must carry %q: %v", key, record)
		}
	}
	if record["severity"] != "INFO" || record["message"] != "served a request" || record["path"] != "/v1/version" {
		t.Fatalf("record = %v", record)
	}
	var warned map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &warned); err != nil {
		t.Fatal(err)
	}
	if warned["severity"] != "WARNING" {
		t.Fatalf("the reference spells the level WARNING, got %v", warned["severity"])
	}
}

// The allowlist narrows which keys a record carries; without one every key
// stays, which is the reference's default.
func TestJSONLogFieldAllowlist(t *testing.T) {
	var out bytes.Buffer
	previous := LogSink
	LogSink = &out
	defer func() { LogSink = previous }()

	cfg := config.FromEnv(func(string) (string, bool) { return "", false })
	cfg.LogFormat = "json"
	cfg.LogJSONFields = []string{"severity", "message"}
	slog.New(NewLogHandler(cfg)).Info("narrow", "path", "/x")

	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &record); err != nil {
		t.Fatal(err)
	}
	if _, ok := record["timestamp"]; ok {
		t.Fatalf("timestamp is not in the allowlist: %v", record)
	}
	if _, ok := record["path"]; ok {
		t.Fatalf("path is not in the allowlist: %v", record)
	}
	if record["message"] != "narrow" {
		t.Fatalf("record = %v", record)
	}
}

// The default is the reference's text shape, and the configured level filters.
func TestTextLogIsTheDefault(t *testing.T) {
	var out bytes.Buffer
	previous := LogSink
	LogSink = &out
	defer func() { LogSink = previous }()

	cfg := config.FromEnv(func(string) (string, bool) { return "", false })
	cfg.LogLevel = "warning"
	logger := slog.New(NewLogHandler(cfg))
	logger.Info("filtered out")
	logger.Warn("kept")

	if strings.Contains(out.String(), "filtered out") {
		t.Fatalf("the level must filter: %q", out.String())
	}
	if !strings.Contains(out.String(), "kept") {
		t.Fatalf("a warning must survive: %q", out.String())
	}
	if strings.HasPrefix(strings.TrimSpace(out.String()), "{") {
		t.Fatalf("text is the default shape: %q", out.String())
	}
}
