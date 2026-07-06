// Package logging provides a shared slog bootstrap so every Zester daemon
// (master, peel, watchdog, CLI tools) logs with a consistent format, level,
// and base attribute set (component, version).
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/nirnx/zester/internal/version"
)

// Valid log level values accepted by Setup.
const (
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// Valid log format values accepted by Setup.
const (
	FormatJSON = "json"
	FormatText = "text"
)

// Defaults applied by Setup when level/format are empty. JSON is the default
// for daemons so a watchdog and its wrapped child emit uniformly parseable
// lines on the same stdout stream.
const (
	DefaultLevel  = LevelInfo
	DefaultFormat = FormatJSON
)

// Levels returns the valid log level values, in increasing severity order.
// Intended for flag usage strings, e.g.
// fmt.Sprintf("Log level (%s)", strings.Join(logging.Levels(), "|")).
func Levels() []string {
	return []string{LevelDebug, LevelInfo, LevelWarn, LevelError}
}

// Formats returns the valid log format values. Intended for flag usage strings.
func Formats() []string {
	return []string{FormatJSON, FormatText}
}

// Setup builds a *slog.Logger writing to w.
//
// level must be one of Levels() (default: info when empty); format must be
// one of Formats() (default: json when empty). Both are matched
// case-insensitively with surrounding whitespace ignored. Invalid values
// return an error listing the valid options.
//
// The returned logger carries the base attributes component=<component> and
// version=<internal/version.Version> on every record.
func Setup(w io.Writer, component, level, format string) (*slog.Logger, error) {
	lvl, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}

	opts := &slog.HandlerOptions{Level: lvl}
	var handler slog.Handler
	switch normalize(format) {
	case "", FormatJSON:
		handler = slog.NewJSONHandler(w, opts)
	case FormatText:
		handler = slog.NewTextHandler(w, opts)
	default:
		return nil, fmt.Errorf("logging: invalid log format %q (valid: %s)", format, strings.Join(Formats(), ", "))
	}

	return slog.New(handler).With(
		slog.String("component", component),
		slog.String("version", version.Version),
	), nil
}

// ParseLevel maps a level string (one of Levels(), case-insensitive, empty =
// info) to its slog.Level. Invalid values return an error listing the valid
// options.
func ParseLevel(level string) (slog.Level, error) {
	switch normalize(level) {
	case "", LevelInfo:
		return slog.LevelInfo, nil
	case LevelDebug:
		return slog.LevelDebug, nil
	case LevelWarn:
		return slog.LevelWarn, nil
	case LevelError:
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("logging: invalid log level %q (valid: %s)", level, strings.Join(Levels(), ", "))
	}
}

func normalize(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
