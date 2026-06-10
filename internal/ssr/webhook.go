package ssr

import (
	"context"
	"net/http"

	"github.com/anhydrous99/thailandgiftshop/internal/httpapi"
	"github.com/anhydrous99/thailandgiftshop/internal/observability"
	"github.com/aws/aws-lambda-go/events"
)

const stripeSignatureHeaderName = "stripe-signature"

// handleStripeWebhook receives provider webhook deliveries per §7.3. The
// Stripe signature is the sole authenticator: verification runs over the
// exact raw (base64-aware) body bytes before any parsing, and the customer
// CSRF machinery is deliberately bypassed. Responses map ApplyWebhookEvent's
// contract: nil means acknowledged (200), an error means a transient failure
// the provider should retry (500). An invalid signature is a 400 with its own
// alarmed metric.
func (h *Handler) handleStripeWebhook(ctx context.Context, request events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	headers := seoHeadersForRoute(pageStripeWebhook)
	if h.payments == nil || h.checkout == nil {
		return httpapi.TextResponse(http.StatusNotFound, "Not found", headers)
	}

	rawBody, err := httpapi.RawBody(request)
	if err != nil {
		return httpapi.TextResponse(http.StatusBadRequest, "Bad request", headers)
	}
	signatureHeader := httpapi.HeaderValue(request.Headers, stripeSignatureHeaderName)
	event, err := h.payments.ParseWebhook(rawBody, signatureHeader, h.currentTime())
	if err != nil {
		// Never log the payload or signature: a forged delivery controls both.
		logAccountError("stripe webhook: signature verification", err)
		h.recordStripeWebhookSignatureFailure()
		return httpapi.TextResponse(http.StatusBadRequest, "Invalid signature", headers)
	}

	if err := h.checkout.ApplyWebhookEvent(ctx, event); err != nil {
		logAccountError("stripe webhook: apply event", err)
		return httpapi.TextResponse(http.StatusInternalServerError, "Internal server error", headers)
	}

	return httpapi.TextResponse(http.StatusOK, "ok", headers)
}

func (h *Handler) recordStripeWebhookSignatureFailure() {
	if h.metrics == nil {
		return
	}
	h.metrics.Record(observability.Count(
		observability.MetricStripeWebhook,
		observability.Dim("Service", "ssr"),
		observability.Dim("Outcome", "invalid_signature"),
	))
}
