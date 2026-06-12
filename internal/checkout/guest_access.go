package checkout

import (
	"net/url"
	"strings"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/signedtoken"
)

const GuestOrderAccessTTL = 30 * 24 * time.Hour
const GuestOrderAccessParam = "access"

const guestOrderAccessTokenVersion = 1

var guestOrderAccessPurpose = []byte("tgs-guest-order-access")

type guestOrderAccessPayload struct {
	Version   int    `json:"version"`
	OrderID   string `json:"order_id"`
	ExpiresAt int64  `json:"expires_at"`
}

func MintGuestOrderAccessToken(orderID string, secret string, expiresAt time.Time) (string, error) {
	return signedtoken.Encode(guestOrderAccessPayload{
		Version:   guestOrderAccessTokenVersion,
		OrderID:   orderID,
		ExpiresAt: expiresAt.Unix(),
	}, secret, guestOrderAccessPurpose)
}

func ValidGuestOrderAccessToken(token string, orderID string, secret string, now time.Time) bool {
	var payload guestOrderAccessPayload
	if !signedtoken.Decode(token, secret, guestOrderAccessPurpose, &payload) {
		return false
	}
	if payload.Version != guestOrderAccessTokenVersion || payload.OrderID != orderID {
		return false
	}
	return now.UTC().Before(time.Unix(payload.ExpiresAt, 0))
}

func GuestOrderAccessPath(orderID string, token string) string {
	return "/orders/" + orderID + "?" + GuestOrderAccessParam + "=" + url.QueryEscape(token)
}

func GuestOrderAccessURL(order commerce.Order, now time.Time, baseURL string, secret string) (string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	secret = strings.TrimSpace(secret)
	if order.ID == "" || baseURL == "" || secret == "" {
		return "", nil
	}
	expiresAt := order.PaidAt.UTC().Add(GuestOrderAccessTTL)
	if order.PaidAt.IsZero() {
		expiresAt = now.UTC().Add(GuestOrderAccessTTL)
	}
	if !now.UTC().Before(expiresAt) {
		return "", nil
	}
	token, err := MintGuestOrderAccessToken(order.ID, secret, expiresAt)
	if err != nil {
		return "", err
	}
	return baseURL + GuestOrderAccessPath(order.ID, token), nil
}
