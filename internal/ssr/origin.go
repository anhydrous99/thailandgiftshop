package ssr

import (
	"github.com/anhydrous99/thailandgiftshop/internal/httpapi"
	"github.com/aws/aws-lambda-go/events"
)

const envOriginHeaderSecret = httpapi.EnvOriginHeaderSecret
const originSecretHeaderName = httpapi.OriginSecretHeaderName

// validOrigin rejects requests that bypassed CloudFront — and with it the WAF
// and edge cache — by calling the API Gateway execute-api endpoint directly.
func (h *Handler) validOrigin(request events.APIGatewayV2HTTPRequest) bool {
	return httpapi.ValidOriginSecret(request, h.originSecretDigest, h.originSecretSet)
}
