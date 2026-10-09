package ssr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/httpapi"
	"github.com/anhydrous99/thailandgiftshop/internal/signedtoken"
	"github.com/aws/aws-lambda-go/events"
)

// Customer sessions are server-side rows referenced by an HMAC-signed cookie
// envelope: the cookie carries the customer id plus a random token whose
// SHA-256 hash names the session row. Fixed 30-day expiry, no rolling renewal.
const customerSessionTTL = 30 * 24 * time.Hour
const guestCSRFTTL = 2 * time.Hour
const passwordResetTTL = 30 * time.Minute
const customerSignedValueVersion = 1

var customerSessionPurpose = []byte("tgs-customer-session")
var customerCSRFPurpose = []byte("tgs-customer-csrf")
var customerGuestCSRFPurpose = []byte("tgs-customer-guest-csrf")
var customerOrdersCursorPurpose = []byte("tgs-orders-cursor")
var customerPasswordResetPurpose = []byte("customer_password_reset")

const customerCSRFFieldName = "csrf_token"
const guestCSRFFieldName = "guest_csrf_token"
const returnToFieldName = "return_to"
const maxReturnToLength = 256

type customerSessionPayload struct {
	Version    int    `json:"version"`
	CustomerID string `json:"customer_id"`
	Token      string `json:"token"`
	ExpiresAt  int64  `json:"expires_at"`
}

type customerCSRFPayload struct {
	Version          int    `json:"version"`
	SessionExpiresAt int64  `json:"session_expires_at"`
	SessionNonce     string `json:"session_nonce"`
	TokenNonce       string `json:"token_nonce"`
}

type guestCSRFPayload struct {
	Version   int    `json:"version"`
	Nonce     string `json:"nonce"`
	ExpiresAt int64  `json:"expires_at"`
}

type passwordResetTokenPayload struct {
	Version    int    `json:"version"`
	CustomerID string `json:"customer_id"`
	Token      string `json:"token"`
	ExpiresAt  int64  `json:"expires_at"`
}

// customerSession resolves the signed-in customer for a request. It never
// fails the request on a bad cookie (the cart-cookie precedent): invalid,
// expired, revoked, or orphaned sessions all come back anonymous, with
// clearing Set-Cookie strings the response should carry. Transient store
// errors are logged and treated as anonymous without clearing cookies so a
// blip never destroys a valid session.
func (h *Handler) customerSession(ctx context.Context, request events.APIGatewayV2HTTPRequest) (commerce.Session, commerce.Customer, bool, []string) {
	session, customer, signedIn, clearing, _ := h.customerSessionWithStoreError(ctx, request)
	return session, customer, signedIn, clearing
}

// customerSessionWithStoreError additionally reports a transient store error
// hit while a session cookie was presented. Page renders keep the graceful
// anonymous fallback, but writes that would otherwise silently land in the
// wrong cart (cart mutations) must fail instead of degrading.
func (h *Handler) customerSessionWithStoreError(ctx context.Context, request events.APIGatewayV2HTTPRequest) (commerce.Session, commerce.Customer, bool, []string, error) {
	value, found := httpapi.CookieValue(request, commerce.SessionCookieName)
	if !found {
		return commerce.Session{}, commerce.Customer{}, false, nil, nil
	}

	clearing := []string{clearCustomerSessionCookie().String(), clearCustomerCSRFCookie().String()}
	var payload customerSessionPayload
	if !signedtoken.Decode(value, h.customerSessionSecret, customerSessionPurpose, &payload) {
		return commerce.Session{}, commerce.Customer{}, false, clearing, nil
	}
	now := h.currentTime().UTC()
	if payload.Version != customerSignedValueVersion || payload.CustomerID == "" || payload.Token == "" || !now.Before(time.Unix(payload.ExpiresAt, 0)) {
		return commerce.Session{}, commerce.Customer{}, false, clearing, nil
	}
	if h.commerce == nil {
		return commerce.Session{}, commerce.Customer{}, false, clearing, nil
	}

	session, sessionFound, err := h.commerce.GetSession(ctx, payload.CustomerID, hashCustomerSessionToken(payload.Token))
	if err != nil {
		logAccountError("resolve session", err)
		return commerce.Session{}, commerce.Customer{}, false, nil, err
	}
	if !sessionFound || !now.Before(session.ExpiresAt) {
		return commerce.Session{}, commerce.Customer{}, false, clearing, nil
	}

	customer, customerFound, err := h.commerce.GetCustomerByID(ctx, payload.CustomerID)
	if err != nil {
		logAccountError("resolve customer", err)
		return commerce.Session{}, commerce.Customer{}, false, nil, err
	}
	if !customerFound || session.CredentialRevision != customer.CredentialRevision {
		return commerce.Session{}, commerce.Customer{}, false, clearing, nil
	}

	return session, customer, true, nil, nil
}

// mintedCustomerSession bundles everything a successful sign-in/sign-up
// issues: the persisted session row plus the session and CSRF cookies.
type mintedCustomerSession struct {
	session       commerce.Session
	sessionCookie string
	csrfCookie    string
	csrfToken     string
}

// mintCustomerSession creates a fresh server-side session row and the signed
// cookie pair for it. Tokens are exclusively server-minted here — no
// pre-authentication identifier is ever upgraded (session-fixation defense).
// The caller supplies the customer snapshot whose credential was verified;
// re-reading the latest revision here would authorize a stale password.
func (h *Handler) mintCustomerSession(ctx context.Context, customer commerce.Customer) (mintedCustomerSession, error) {
	token, err := signedtoken.RandomToken(32)
	if err != nil {
		return mintedCustomerSession{}, err
	}
	nonce, err := signedtoken.RandomToken(16)
	if err != nil {
		return mintedCustomerSession{}, err
	}

	now := h.currentTime().UTC()
	session := commerce.Session{
		CustomerID:         customer.ID,
		CredentialRevision: customer.CredentialRevision,
		TokenHash:          hashCustomerSessionToken(token),
		Nonce:              nonce,
		CreatedAt:          now,
		ExpiresAt:          now.Add(customerSessionTTL),
	}
	if err := h.commerce.PutSession(ctx, session); err != nil {
		return mintedCustomerSession{}, err
	}

	sessionValue, err := signedtoken.Encode(customerSessionPayload{
		Version:    customerSignedValueVersion,
		CustomerID: customer.ID,
		Token:      token,
		ExpiresAt:  session.ExpiresAt.Unix(),
	}, h.customerSessionSecret, customerSessionPurpose)
	if err != nil {
		return mintedCustomerSession{}, err
	}
	csrfToken, err := h.newCustomerCSRFToken(session)
	if err != nil {
		return mintedCustomerSession{}, err
	}

	return mintedCustomerSession{
		session:       session,
		sessionCookie: customerSessionCookie(sessionValue, session.ExpiresAt).String(),
		csrfCookie:    customerCSRFCookie(csrfToken, session.ExpiresAt).String(),
		csrfToken:     csrfToken,
	}, nil
}

func (h *Handler) newCustomerCSRFToken(session commerce.Session) (string, error) {
	tokenNonce, err := signedtoken.RandomToken(16)
	if err != nil {
		return "", err
	}

	return signedtoken.Encode(customerCSRFPayload{
		Version:          customerSignedValueVersion,
		SessionExpiresAt: session.ExpiresAt.Unix(),
		SessionNonce:     session.Nonce,
		TokenNonce:       tokenNonce,
	}, h.customerSessionSecret, customerCSRFPurpose)
}

// validCustomerCSRF applies the admin double-submit scheme verbatim: the
// HttpOnly cookie and the hidden form field must match exactly, carry a valid
// signature, bind to the current session's nonce and expiry, and be unexpired.
func (h *Handler) validCustomerCSRF(request events.APIGatewayV2HTTPRequest, session commerce.Session) bool {
	cookieValue, found := httpapi.CookieValue(request, commerce.CSRFCookieName)
	if !found {
		return false
	}
	values, err := httpapi.FormValues(request)
	if err != nil {
		return false
	}
	submittedValue := values.Get(customerCSRFFieldName)
	if submittedValue == "" || submittedValue != cookieValue {
		return false
	}

	var payload customerCSRFPayload
	if !signedtoken.Decode(submittedValue, h.customerSessionSecret, customerCSRFPurpose, &payload) {
		return false
	}
	if payload.Version != customerSignedValueVersion || payload.SessionNonce != session.Nonce || payload.SessionExpiresAt != session.ExpiresAt.Unix() || payload.TokenNonce == "" {
		return false
	}

	return h.currentTime().UTC().Before(session.ExpiresAt)
}

// customerCSRFForResponse reuses a still-valid CSRF cookie or mints a fresh
// token plus its Set-Cookie string for a protected page render (the admin
// csrfForProtectedResponse pattern).
func (h *Handler) customerCSRFForResponse(request events.APIGatewayV2HTTPRequest, session commerce.Session) (string, []string, error) {
	if cookieValue, found := httpapi.CookieValue(request, commerce.CSRFCookieName); found {
		var payload customerCSRFPayload
		if signedtoken.Decode(cookieValue, h.customerSessionSecret, customerCSRFPurpose, &payload) &&
			payload.Version == customerSignedValueVersion &&
			payload.SessionNonce == session.Nonce &&
			payload.SessionExpiresAt == session.ExpiresAt.Unix() &&
			payload.TokenNonce != "" {
			return cookieValue, nil, nil
		}
	}

	csrfToken, err := h.newCustomerCSRFToken(session)
	if err != nil {
		return "", nil, err
	}
	return csrfToken, []string{customerCSRFCookie(csrfToken, session.ExpiresAt).String()}, nil
}

// mintGuestCSRF issues the pre-session double-submit token for the sign-in
// and sign-up forms.
func (h *Handler) mintGuestCSRF() (string, string, error) {
	nonce, err := signedtoken.RandomToken(16)
	if err != nil {
		return "", "", err
	}
	expiresAt := h.currentTime().UTC().Add(guestCSRFTTL)
	token, err := signedtoken.Encode(guestCSRFPayload{
		Version:   customerSignedValueVersion,
		Nonce:     nonce,
		ExpiresAt: expiresAt.Unix(),
	}, h.customerSessionSecret, customerGuestCSRFPurpose)
	if err != nil {
		return "", "", err
	}

	return token, guestCSRFCookie(token, expiresAt).String(), nil
}

// validGuestCSRF verifies the pre-session double-submit pair on sign-in and
// sign-up POSTs.
func (h *Handler) validGuestCSRF(request events.APIGatewayV2HTTPRequest) bool {
	cookieValue, found := httpapi.CookieValue(request, commerce.GuestCSRFCookieName)
	if !found {
		return false
	}
	values, err := httpapi.FormValues(request)
	if err != nil {
		return false
	}
	submittedValue := values.Get(guestCSRFFieldName)
	if submittedValue == "" || submittedValue != cookieValue {
		return false
	}

	var payload guestCSRFPayload
	if !signedtoken.Decode(submittedValue, h.customerSessionSecret, customerGuestCSRFPurpose, &payload) {
		return false
	}
	if payload.Version != customerSignedValueVersion || payload.Nonce == "" {
		return false
	}

	return h.currentTime().UTC().Before(time.Unix(payload.ExpiresAt, 0))
}

func hashCustomerSessionToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func hashPasswordResetToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

// validReturnTo accepts only same-site path targets: must start with "/", not
// "//", contain no backslash nor any ASCII control character, and stay within
// 256 chars (open-redirect guard). Control characters are rejected outright
// because the WHATWG URL parser strips tab/CR/LF before parsing, so a value
// like "/\t/evil.com" would otherwise reach the Location header intact and be
// re-parsed by the browser as the protocol-relative "//evil.com".
func validReturnTo(value string) bool {
	return value != "" &&
		len(value) <= maxReturnToLength &&
		strings.HasPrefix(value, "/") &&
		!strings.HasPrefix(value, "//") &&
		!strings.Contains(value, `\`) &&
		strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) == -1
}

func sanitizedReturnTo(value string, fallback string) string {
	if validReturnTo(value) {
		return value
	}
	return fallback
}

func signInLocationForReturnTo(returnTo string) string {
	if !validReturnTo(returnTo) {
		return "/account/sign-in"
	}
	return "/account/sign-in?return_to=" + url.QueryEscape(returnTo)
}

// The __Host- cookies always set Secure (the prefix requires it); browsers
// still accept them on http://127.0.0.1, which the admin cookie and the
// Playwright suite already rely on.

func customerSessionCookie(value string, expiresAt time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     commerce.SessionCookieName,
		Value:    value,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(customerSessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}

// The CSRF token reaches the browser twice: in this cookie and in hidden form
// inputs rendered into every account form. Scripts only ever read the hidden
// input, so the cookie can stay HttpOnly.
func customerCSRFCookie(value string, expiresAt time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     commerce.CSRFCookieName,
		Value:    value,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(customerSessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}

func guestCSRFCookie(value string, expiresAt time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     commerce.GuestCSRFCookieName,
		Value:    value,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(guestCSRFTTL.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}

func clearCustomerSessionCookie() *http.Cookie {
	return clearHostCookie(commerce.SessionCookieName)
}

func clearCustomerCSRFCookie() *http.Cookie {
	return clearHostCookie(commerce.CSRFCookieName)
}

func clearGuestCSRFCookie() *http.Cookie {
	return clearHostCookie(commerce.GuestCSRFCookieName)
}

func clearHostCookie(name string) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}

const cloudFrontViewerAddressHeader = "CloudFront-Viewer-Address"
const unknownCustomerLoginClient = "unknown"

// customerLoginClient normalizes the caller's IP for throttle keying:
// CloudFront-Viewer-Address first, then the API Gateway source IP (the admin
// login-client normalizer, ported).
func customerLoginClient(request events.APIGatewayV2HTTPRequest) string {
	if client := normalizedClientAddress(httpapi.HeaderValue(request.Headers, cloudFrontViewerAddressHeader)); client != "" {
		return client
	}
	sourceIP := strings.TrimSpace(request.RequestContext.HTTP.SourceIP)
	if sourceIP == "" {
		return unknownCustomerLoginClient
	}
	if client := normalizedClientAddress(sourceIP); client != "" {
		return client
	}
	return sourceIP
}

func normalizedClientAddress(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		host = strings.Trim(host, "[]")
		if host != "" {
			return host
		}
	}
	if ip := net.ParseIP(strings.Trim(value, "[]")); ip != nil {
		return ip.String()
	}
	if host, _, found := strings.Cut(value, ":"); found {
		if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
			return ip.String()
		}
	}
	if split := strings.LastIndex(value, ":"); split > 0 {
		if ip := net.ParseIP(strings.Trim(value[:split], "[]")); ip != nil {
			return ip.String()
		}
	}
	return ""
}
