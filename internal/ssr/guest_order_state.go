package ssr

import (
	"net/http"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/checkout"
	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/httpapi"
	"github.com/anhydrous99/thailandgiftshop/internal/signedtoken"
	"github.com/aws/aws-lambda-go/events"
)

// Guest checkout pointer: the cookie analog of CartRecord.PendingOrderID for
// shoppers with no server cart. Short-lived: it only needs to outlive one
// 30-minute hosted-checkout session.
const guestOrderPointerTTL = time.Hour

const guestOrderAccessTTL = checkout.GuestOrderAccessTTL
const guestOrderAccessParam = checkout.GuestOrderAccessParam

// Both values are signed with the existing customer session secret; the
// purpose strings provide domain separation (signedtoken HMACs over
// purpose || 0x00 || payload), so neither value ever verifies as a session,
// CSRF token, orders cursor, or each other.
var guestOrderPointerPurpose = []byte("tgs-guest-order-pointer")
var guestOrderAccessPurpose = []byte("tgs-guest-order-access")

type guestOrderPointerPayload struct {
	Version     int    `json:"version"`
	OrderID     string `json:"order_id"`
	Fingerprint string `json:"fingerprint"`
	ExpiresAt   int64  `json:"expires_at"`
}

type guestOrderAccessPayload struct {
	Version   int    `json:"version"`
	OrderID   string `json:"order_id"`
	ExpiresAt int64  `json:"expires_at"`
}

// mintGuestOrderPointerCookie issues the signed __Host-tgs_guest_order cookie
// pointing this browser's next place-order at an in-flight pending order.
// SameSite=Lax is required and sufficient: the return redirect from
// checkout.stripe.com is a cross-site top-level GET navigation, which Lax
// sends cookies on.
func (h *Handler) mintGuestOrderPointerCookie(orderID string, fingerprint string) (string, error) {
	value, err := signedtoken.Encode(guestOrderPointerPayload{
		Version:     customerSignedValueVersion,
		OrderID:     orderID,
		Fingerprint: fingerprint,
		ExpiresAt:   h.currentTime().UTC().Add(guestOrderPointerTTL).Unix(),
	}, h.customerSessionSecret, guestOrderPointerPurpose)
	if err != nil {
		return "", err
	}

	return (&http.Cookie{
		Name:     commerce.GuestOrderCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   int(guestOrderPointerTTL.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}).String(), nil
}

// guestOrderPointer resolves the pointer cookie. Any failure — absent,
// tampered, cross-purpose, expired, malformed order ID — yields ok=false.
func (h *Handler) guestOrderPointer(request events.APIGatewayV2HTTPRequest) (string, string, bool) {
	value, found := httpapi.CookieValue(request, commerce.GuestOrderCookieName)
	if !found {
		return "", "", false
	}
	var payload guestOrderPointerPayload
	if !signedtoken.Decode(value, h.customerSessionSecret, guestOrderPointerPurpose, &payload) {
		return "", "", false
	}
	if payload.Version != customerSignedValueVersion || !accountIDPattern.MatchString(payload.OrderID) {
		return "", "", false
	}
	if !h.currentTime().UTC().Before(time.Unix(payload.ExpiresAt, 0)) {
		return "", "", false
	}

	return payload.OrderID, payload.Fingerprint, true
}

func clearGuestOrderCookie() *http.Cookie {
	return clearHostCookie(commerce.GuestOrderCookieName)
}

// mintGuestOrderAccessToken issues the read-only order-access token. The
// caller supplies expiresAt (confirm passes PaidAt+30d, §7.3) so replays
// re-mint identically-expiring tokens, never fresher ones.
func (h *Handler) mintGuestOrderAccessToken(orderID string, expiresAt time.Time) (string, error) {
	return checkout.MintGuestOrderAccessToken(orderID, h.customerSessionSecret, expiresAt)
}

// validGuestOrderAccess reports whether the request's ?access token grants a
// read of exactly this order. Never errors; invalid == absent.
func (h *Handler) validGuestOrderAccess(request events.APIGatewayV2HTTPRequest, orderID string) bool {
	token := request.QueryStringParameters[guestOrderAccessParam]
	if token == "" {
		return false
	}
	var payload guestOrderAccessPayload
	if !signedtoken.Decode(token, h.customerSessionSecret, guestOrderAccessPurpose, &payload) {
		return false
	}
	if payload.Version != customerSignedValueVersion || payload.OrderID != orderID {
		return false
	}

	return h.currentTime().UTC().Before(time.Unix(payload.ExpiresAt, 0))
}

func guestOrderAccessPath(orderID string, token string) string {
	return checkout.GuestOrderAccessPath(orderID, token)
}
