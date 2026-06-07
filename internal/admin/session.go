package admin

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const adminSessionCookieName = "__Host-tgs_admin"
const adminCSRFCookieName = "__Host-tgs_admin_csrf"

const adminSessionTTL = 8 * time.Hour
const signedValueVersion = 1
const maxSignedValueLength = 2048

var signedSessionPurpose = []byte("tgs-admin-session")
var signedCSRFPurpose = []byte("tgs-admin-csrf")

type adminSession struct {
	ExpiresAt time.Time
	Nonce     string
}

type sessionPayload struct {
	Version   int    `json:"version"`
	ExpiresAt int64  `json:"expires_at"`
	Nonce     string `json:"nonce"`
}

type csrfPayload struct {
	Version          int    `json:"version"`
	SessionExpiresAt int64  `json:"session_expires_at"`
	SessionNonce     string `json:"session_nonce"`
	TokenNonce       string `json:"token_nonce"`
}

func newAdminSession(secret string, now time.Time) (adminSession, string, error) {
	nonce, err := randomToken()
	if err != nil {
		return adminSession{}, "", err
	}
	session := adminSession{
		ExpiresAt: now.UTC().Add(adminSessionTTL),
		Nonce:     nonce,
	}
	payload := sessionPayload{
		Version:   signedValueVersion,
		ExpiresAt: session.ExpiresAt.Unix(),
		Nonce:     session.Nonce,
	}
	value, err := encodeSignedValue(payload, secret, signedSessionPurpose)
	if err != nil {
		return adminSession{}, "", err
	}

	return session, value, nil
}

func decodeAdminSession(value string, secret string, now time.Time) (adminSession, bool) {
	var payload sessionPayload
	if !decodeSignedValue(value, secret, signedSessionPurpose, &payload) {
		return adminSession{}, false
	}
	if payload.Version != signedValueVersion || payload.Nonce == "" {
		return adminSession{}, false
	}
	expiresAt := time.Unix(payload.ExpiresAt, 0).UTC()
	if !now.UTC().Before(expiresAt) {
		return adminSession{}, false
	}

	return adminSession{ExpiresAt: expiresAt, Nonce: payload.Nonce}, true
}

func newAdminCSRFToken(session adminSession, secret string) (string, error) {
	tokenNonce, err := randomToken()
	if err != nil {
		return "", err
	}

	return encodeSignedValue(csrfPayload{
		Version:          signedValueVersion,
		SessionExpiresAt: session.ExpiresAt.Unix(),
		SessionNonce:     session.Nonce,
		TokenNonce:       tokenNonce,
	}, secret, signedCSRFPurpose)
}

func validateAdminCSRFToken(value string, session adminSession, secret string, now time.Time) bool {
	var payload csrfPayload
	if !decodeSignedValue(value, secret, signedCSRFPurpose, &payload) {
		return false
	}
	if payload.Version != signedValueVersion || payload.SessionNonce != session.Nonce || payload.SessionExpiresAt != session.ExpiresAt.Unix() || payload.TokenNonce == "" {
		return false
	}

	return now.UTC().Before(session.ExpiresAt)
}

func encodeSignedValue(payload any, secret string, purpose []byte) (string, error) {
	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(jsonPayload)
	encodedSignature := base64.RawURLEncoding.EncodeToString(signPayload(jsonPayload, secret, purpose))
	return encodedPayload + "." + encodedSignature, nil
}

func decodeSignedValue(value string, secret string, purpose []byte, target any) bool {
	if len(value) == 0 || len(value) > maxSignedValueLength {
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

func signPayload(payload []byte, secret string, purpose []byte) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(purpose)
	mac.Write([]byte{0})
	mac.Write(payload)
	return mac.Sum(nil)
}

func randomToken() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func adminSessionCookie(value string, expiresAt time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    value,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(adminSessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}

func adminCSRFCookie(value string, expiresAt time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     adminCSRFCookieName,
		Value:    value,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(adminSessionTTL.Seconds()),
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}

func clearAdminSessionCookie() *http.Cookie {
	return &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}

func clearAdminCSRFCookie() *http.Cookie {
	return &http.Cookie{
		Name:     adminCSRFCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}
