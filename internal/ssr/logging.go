package ssr

import (
	"log/slog"
	"os"
)

// errorLogger writes structured JSON lines to stdout, where the Lambda runtime
// forwards them to CloudWatch Logs alongside the EMF metric records.
var errorLogger = slog.New(slog.NewJSONHandler(os.Stdout, nil))

func logHandlerError(kind pageKind, method string, path string, err error) {
	errorLogger.Error("request failed",
		slog.String("service", "ssr"),
		slog.String("route", string(kind)),
		slog.String("method", method),
		slog.String("path", path),
		slog.String("error", err.Error()),
	)
}

func logHandlerWarn(kind pageKind, method string, path string, err error) {
	errorLogger.Warn("request degraded",
		slog.String("service", "ssr"),
		slog.String("route", string(kind)),
		slog.String("method", method),
		slog.String("path", path),
		slog.String("error", err.Error()),
	)
}
