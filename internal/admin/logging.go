package admin

import (
	"log/slog"
	"os"
)

// errorLogger writes structured JSON lines to stdout, where the Lambda runtime
// forwards them to CloudWatch Logs alongside the EMF metric records.
var errorLogger = slog.New(slog.NewJSONHandler(os.Stdout, nil))

func logAdminError(operation string, err error) {
	errorLogger.Error("request failed",
		slog.String("service", "admin"),
		slog.String("operation", operation),
		slog.String("error", err.Error()),
	)
}
