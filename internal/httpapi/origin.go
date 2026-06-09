package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"os"
	"strings"

	"github.com/anhydrous99/thailandgiftshop/internal/appenv"
	"github.com/aws/aws-lambda-go/events"
)

// CloudFront injects OriginSecretHeaderName with the EnvOriginHeaderSecret
// value on every request it forwards to the API Gateway origin; both Lambda
// handlers verify it so the execute-api endpoint cannot be used to bypass
// CloudFront and its WAF.
const EnvOriginHeaderSecret = "ADMIN_ORIGIN_HEADER_SECRET"
const OriginSecretHeaderName = "X-TGS-Origin-Secret"

// OriginSecretDigestFromEnvironment loads the origin secret once at handler
// construction; only its digest is retained.
func OriginSecretDigestFromEnvironment() ([32]byte, bool) {
	secret := strings.TrimSpace(os.Getenv(EnvOriginHeaderSecret))
	if secret == "" {
		return [32]byte{}, false
	}
	return sha256.Sum256([]byte(secret)), true
}

// ValidOriginSecret reports whether the request carries the CloudFront-
// injected origin secret. Environments without a configured secret
// (devserver, tests) only enforce in production.
func ValidOriginSecret(request events.APIGatewayV2HTTPRequest, secretDigest [32]byte, secretConfigured bool) bool {
	if !secretConfigured {
		return !appenv.IsProduction()
	}
	provided := HeaderValue(request.Headers, OriginSecretHeaderName)
	providedDigest := sha256.Sum256([]byte(provided))
	return subtle.ConstantTimeCompare(providedDigest[:], secretDigest[:]) == 1
}
