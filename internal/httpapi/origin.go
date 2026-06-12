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

// EnvPreviousOriginHeaderSecret allows Lambda to accept the pre-rotation
// header while a CloudFront distribution update reaches every edge location.
const EnvPreviousOriginHeaderSecret = "ADMIN_ORIGIN_HEADER_PREVIOUS_SECRET"

const OriginSecretHeaderName = "X-TGS-Origin-Secret"

type OriginSecretDigests [][32]byte

// OriginSecretDigestFromEnvironment loads the origin secret once at handler
// construction; only its digest is retained.
func OriginSecretDigestFromEnvironment() ([32]byte, bool) {
	digests, configured := OriginSecretDigestsFromEnvironment()
	if !configured {
		return [32]byte{}, false
	}
	return digests[0], true
}

// OriginSecretDigestsFromEnvironment loads the current origin secret and, when
// provided during rotations, the previous one accepted for CloudFront rollout.
func OriginSecretDigestsFromEnvironment() (OriginSecretDigests, bool) {
	secrets := []string{
		os.Getenv(EnvOriginHeaderSecret),
		os.Getenv(EnvPreviousOriginHeaderSecret),
	}
	digests := make(OriginSecretDigests, 0, len(secrets))
	seen := make(map[[32]byte]struct{}, len(secrets))
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if secret == "" {
			continue
		}
		digest := sha256.Sum256([]byte(secret))
		if _, ok := seen[digest]; ok {
			continue
		}
		seen[digest] = struct{}{}
		digests = append(digests, digest)
	}

	return digests, len(digests) > 0
}

// ValidOriginSecret reports whether the request carries the CloudFront-
// injected origin secret. Environments without a configured secret
// (devserver, tests) only enforce in production.
func ValidOriginSecret(request events.APIGatewayV2HTTPRequest, secretDigest [32]byte, secretConfigured bool) bool {
	return ValidOriginSecrets(request, OriginSecretDigests{secretDigest}, secretConfigured)
}

// ValidOriginSecrets reports whether the request carries one of the accepted
// CloudFront-injected origin secrets.
func ValidOriginSecrets(request events.APIGatewayV2HTTPRequest, secretDigests OriginSecretDigests, secretConfigured bool) bool {
	if !secretConfigured {
		return !appenv.IsProduction()
	}
	provided := HeaderValue(request.Headers, OriginSecretHeaderName)
	providedDigest := sha256.Sum256([]byte(provided))
	matched := 0
	for _, secretDigest := range secretDigests {
		matched |= subtle.ConstantTimeCompare(providedDigest[:], secretDigest[:])
	}
	return matched == 1
}
