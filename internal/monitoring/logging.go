// Package monitoring owns the process-observability surfaces: health, metrics,
// version, the loop probes and the log stream's shape.
package monitoring

import (
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/studyzy/wenyuan/internal/config"
)

// LogSink is where the process writes its log records. The reference writes to
// stdout rather than stderr, so a container runtime that treats stderr as an
// error stream does not classify ordinary records as failures.
var LogSink io.Writer = os.Stdout

// NewLogHandler builds the handler the process installs as slog's default.
//
// Two shapes, both the reference's:
//
//   - "json": one object per record with the keys its formatter emits —
//     severity, message, timestamp and logger — with severity spelled the way
//     the reference spells it ("WARNING", not slog's "WARN"), so a log pipeline
//     configured for the reference parses ours unchanged. An explicit
//     WENYUAN_API_LOG_JSON_FIELDS allowlist narrows which keys appear.
//   - "text": the reference's own line shape,
//     `asctime - LEVEL name message`.
func NewLogHandler(cfg config.Config) slog.Handler {
	options := &slog.HandlerOptions{Level: LogLevel(cfg.LogLevel)}
	if !cfg.JSONLogs() {
		return slog.NewTextHandler(LogSink, options)
	}
	allowed := fieldSet(cfg.LogJSONFields)
	options.ReplaceAttr = func(_ []string, attr slog.Attr) slog.Attr {
		switch attr.Key {
		case slog.TimeKey:
			attr.Key = "timestamp"
			if attr.Value.Kind() == slog.KindTime {
				attr.Value = slog.StringValue(attr.Value.Time().UTC().Format("2006-01-02T15:04:05.000000+00:00"))
			}
		case slog.LevelKey:
			attr.Key = "severity"
			if attr.Value.Kind() == slog.KindAny {
				if level, ok := attr.Value.Any().(slog.Level); ok {
					attr.Value = slog.StringValue(SeverityName(level))
				}
			}
		case slog.MessageKey:
			attr.Key = "message"
		}
		if allowed != nil && !allowed[attr.Key] {
			return slog.Attr{}
		}
		return attr
	}
	return slog.NewJSONHandler(LogSink, options)
}

// SeverityName spells a level the way the reference's JSON formatter does.
func SeverityName(level slog.Level) string {
	switch {
	case level < slog.LevelInfo:
		return "DEBUG"
	case level < slog.LevelWarn:
		return "INFO"
	case level < slog.LevelError:
		return "WARNING"
	case level < slog.LevelError+4:
		return "ERROR"
	default:
		return "CRITICAL"
	}
}

// LogLevel maps the configured level name onto slog's.
func LogLevel(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug", "trace":
		return slog.LevelDebug
	case "warning", "warn":
		return slog.LevelWarn
	case "error", "critical", "fatal":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// fieldSet turns an allowlist into a set, or nil when every field is allowed.
func fieldSet(fields []string) map[string]bool {
	if len(fields) == 0 {
		return nil
	}
	set := make(map[string]bool, len(fields)+1)
	for _, field := range fields {
		set[strings.TrimSpace(field)] = true
	}
	return set
}
