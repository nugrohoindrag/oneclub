// Package obs sets up structured JSON logging via slog with personal data and
// secrets masked by attribute key (UU PDP; Technical Doc §5.3).
//
// Monitoring infrastructure (Prometheus, Grafana, tracing) is out of scope by
// product decision (2026-10-04); logs go to stdout and health endpoints
// serve deploy health checks.
package obs

import (
	"log/slog"
	"os"
	"strings"

	"oneclub/internal/kernel/mask"
)

// SetupLogger installs the default JSON logger.
func SetupLogger(level, instance, process string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: lv,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if mask.IsSecretKey(a.Key) {
				return slog.String(a.Key, mask.Redacted)
			}
			if kind := mask.PersonalKind(a.Key); kind != "" && a.Value.Kind() == slog.KindString {
				return slog.String(a.Key, mask.Value(kind, a.Value.String()).(string))
			}
			return a
		},
	})
	l := slog.New(h).With("instance", instance, "process", process)
	slog.SetDefault(l)
	return l
}
