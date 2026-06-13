package ssr

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestHandlerErrorLogExcludesRawPath(t *testing.T) {
	originalLogger := errorLogger
	defer func() { errorLogger = originalLogger }()

	var buffer bytes.Buffer
	errorLogger = slog.New(slog.NewJSONHandler(&buffer, nil))

	logHandlerError(pageCheckoutConfirm, "GET", "/checkout/confirm?access=guest-order-token", errors.New("boom"))
	logLine := buffer.String()

	for _, disallowed := range []string{"/checkout/confirm", "guest-order-token", "\"path\""} {
		if strings.Contains(logLine, disallowed) {
			t.Fatalf("log line %q contains sensitive path fragment %q", logLine, disallowed)
		}
	}
	for _, want := range []string{"\"route\":\"checkout-confirm\"", "\"method\":\"GET\"", "\"error\":\"boom\""} {
		if !strings.Contains(logLine, want) {
			t.Fatalf("log line %q missing %q", logLine, want)
		}
	}
}
