// Package signedtoken provides the HMAC-signed value envelope used by
// customer-facing cookies and CSRF tokens, plus random token and identifier
// generators. The wire format and verification semantics deliberately mirror
// internal/admin/session.go: a base64 RawURL-encoded JSON payload, a "."
// separator, and a base64 RawURL-encoded HMAC-SHA256 signature computed over
// purpose || 0x00 || payload with the secret as the key. Callers embed a
// version field in their payload structs, the admin precedent.
package signedtoken

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// MaxSignedValueLength bounds how long an encoded value may be before Decode
// rejects it outright, mirroring the admin signed-value limit.
const MaxSignedValueLength = 2048

const idByteLength = 16

// crockfordEncoding is the lowercase Crockford base32 alphabet. It excludes
// i, l, o, and u, so every generated ID matches ^[a-z0-9]{26}$ while avoiding
// lookalike characters.
var crockfordEncoding = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").WithPadding(base32.NoPadding)

// Encode marshals payload as JSON and returns the "payload.signature"
// envelope signed for the given purpose. Values signed for one purpose never
// verify under another.
func Encode(payload any, secret string, purpose []byte) (string, error) {
	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(jsonPayload)
	encodedSignature := base64.RawURLEncoding.EncodeToString(signPayload(jsonPayload, secret, purpose))
	return encodedPayload + "." + encodedSignature, nil
}

// Decode verifies value against the secret and purpose and, when valid,
// unmarshals the JSON payload into target. It never returns an error: empty,
// oversized, malformed, tampered, or cross-purpose values all yield false.
// The signature comparison is constant-time.
func Decode(value, secret string, purpose []byte, target any) bool {
	if len(value) == 0 || len(value) > MaxSignedValueLength {
		return false
	}
	encodedPayload, encodedSignature, ok := strings.Cut(value, ".")
	if !ok || strings.Contains(encodedSignature, ".") {
		return false
	}
	jsonPayload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(encodedSignature)
	if err != nil {
		return false
	}
	if !hmac.Equal(signature, signPayload(jsonPayload, secret, purpose)) {
		return false
	}

	return json.Unmarshal(jsonPayload, target) == nil
}

// RandomToken returns n cryptographically random bytes encoded with
// base64.RawURLEncoding. n must be positive.
func RandomToken(n int) (string, error) {
	if n <= 0 {
		return "", errors.New("signedtoken: random token byte length must be positive")
	}
	buffer := make([]byte, n)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

// NewID returns a 26-character identifier built from 16 random bytes encoded
// as lowercase Crockford base32. Every ID matches ^[a-z0-9]{26}$.
func NewID() string {
	buffer := make([]byte, idByteLength)
	if _, err := rand.Read(buffer); err != nil {
		// crypto/rand.Read does not fail on supported platforms; an ID
		// source without entropy would be unrecoverable anyway.
		panic(err)
	}
	return crockfordEncoding.EncodeToString(buffer)
}

func signPayload(payload []byte, secret string, purpose []byte) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(purpose)
	mac.Write([]byte{0})
	mac.Write(payload)
	return mac.Sum(nil)
}
