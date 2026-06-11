package ssr

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/signedtoken"
	"github.com/aws/aws-lambda-go/events"
)

const guestStateTestOrderID = "0123456789abcdefghjkmnpqrs"

func guestStateTestHandler(t *testing.T) *Handler {
	t.Helper()
	env := newAccountTestEnv(t)
	return env.handler
}

func pointerCookieRequest(t *testing.T, cookie string) events.APIGatewayV2HTTPRequest {
	t.Helper()
	header := http.Header{}
	header.Add("Set-Cookie", cookie)
	parsed := (&http.Response{Header: header}).Cookies()
	if len(parsed) != 1 {
		t.Fatalf("unparseable cookie %q", cookie)
	}
	request := pageRequest(http.MethodGet, "/checkout/place-order")
	request.Cookies = []string{parsed[0].Name + "=" + parsed[0].Value}
	return request
}

func TestGuestOrderPointerCookieRoundTrip(t *testing.T) {
	handler := guestStateTestHandler(t)

	cookie, err := handler.mintGuestOrderPointerCookie(guestStateTestOrderID, "fp123")
	if err != nil {
		t.Fatalf("mintGuestOrderPointerCookie returned error: %v", err)
	}
	for _, want := range []string{commerce.GuestOrderCookieName + "=", "Path=/", "Max-Age=3600", "HttpOnly", "Secure", "SameSite=Lax"} {
		if !strings.Contains(cookie, want) {
			t.Fatalf("pointer cookie %q missing %q", cookie, want)
		}
	}

	orderID, fingerprint, ok := handler.guestOrderPointer(pointerCookieRequest(t, cookie))
	if !ok || orderID != guestStateTestOrderID || fingerprint != "fp123" {
		t.Fatalf("guestOrderPointer = %q %q %t, want round trip", orderID, fingerprint, ok)
	}
}

func TestGuestOrderPointerRejectsBadCookies(t *testing.T) {
	handler := guestStateTestHandler(t)
	valid, err := handler.mintGuestOrderPointerCookie(guestStateTestOrderID, "fp123")
	if err != nil {
		t.Fatalf("mintGuestOrderPointerCookie returned error: %v", err)
	}
	validValue := strings.TrimPrefix(strings.SplitN(valid, ";", 2)[0], commerce.GuestOrderCookieName+"=")

	// Cross-purpose: an access token in the pointer cookie never validates.
	accessToken, err := handler.mintGuestOrderAccessToken(guestStateTestOrderID, handler.currentTime().Add(time.Hour))
	if err != nil {
		t.Fatalf("mintGuestOrderAccessToken returned error: %v", err)
	}

	// Malformed order id inside a correctly signed payload.
	badID, err := signedtoken.Encode(guestOrderPointerPayload{
		Version:     customerSignedValueVersion,
		OrderID:     "NOT-AN-ID",
		Fingerprint: "fp123",
		ExpiresAt:   handler.currentTime().Add(time.Hour).Unix(),
	}, handler.customerSessionSecret, guestOrderPointerPurpose)
	if err != nil {
		t.Fatalf("Encode returned error: %v", err)
	}

	tests := []struct {
		name  string
		value string
	}{
		{name: "tampered", value: validValue + "x"},
		{name: "malformed", value: "not-a-token"},
		{name: "cross purpose access token", value: accessToken},
		{name: "malformed order id", value: badID},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := pageRequest(http.MethodGet, "/checkout/place-order")
			request.Cookies = []string{commerce.GuestOrderCookieName + "=" + test.value}
			if _, _, ok := handler.guestOrderPointer(request); ok {
				t.Fatalf("guestOrderPointer accepted %s cookie", test.name)
			}
		})
	}

	t.Run("absent", func(t *testing.T) {
		if _, _, ok := handler.guestOrderPointer(pageRequest(http.MethodGet, "/checkout/place-order")); ok {
			t.Fatal("guestOrderPointer accepted a request without the cookie")
		}
	})

	t.Run("expired", func(t *testing.T) {
		request := pointerCookieRequest(t, valid)
		baseNow := handler.currentTime()
		handler.now = func() time.Time { return baseNow.Add(guestOrderPointerTTL + time.Minute) }
		defer func() { handler.now = time.Now }()
		if _, _, ok := handler.guestOrderPointer(request); ok {
			t.Fatal("guestOrderPointer accepted an expired cookie")
		}
	})
}

func TestGuestOrderAccessTokenValidation(t *testing.T) {
	handler := guestStateTestHandler(t)
	expiresAt := handler.currentTime().Add(guestOrderAccessTTL)
	token, err := handler.mintGuestOrderAccessToken(guestStateTestOrderID, expiresAt)
	if err != nil {
		t.Fatalf("mintGuestOrderAccessToken returned error: %v", err)
	}

	accessRequest := func(orderID string, value string) events.APIGatewayV2HTTPRequest {
		request := pageRequest(http.MethodGet, "/orders/"+orderID)
		request.QueryStringParameters = map[string]string{guestOrderAccessParam: value}
		return request
	}

	if !handler.validGuestOrderAccess(accessRequest(guestStateTestOrderID, token), guestStateTestOrderID) {
		t.Fatal("validGuestOrderAccess rejected a freshly minted token")
	}
	if handler.validGuestOrderAccess(accessRequest(guestStateTestOrderID, token+"x"), guestStateTestOrderID) {
		t.Fatal("validGuestOrderAccess accepted a tampered token")
	}
	if handler.validGuestOrderAccess(pageRequest(http.MethodGet, "/orders/"+guestStateTestOrderID), guestStateTestOrderID) {
		t.Fatal("validGuestOrderAccess accepted an absent token")
	}

	// Wrong order: the payload pins exactly one order id.
	otherID := "zzzzzzzzzzzzzzzzzzzzzzzzzz"
	if handler.validGuestOrderAccess(accessRequest(otherID, token), otherID) {
		t.Fatal("validGuestOrderAccess accepted a token for a different order")
	}

	// Cross-purpose: a pointer-cookie value never validates as an access token.
	pointerValue, err := signedtoken.Encode(guestOrderPointerPayload{
		Version:     customerSignedValueVersion,
		OrderID:     guestStateTestOrderID,
		Fingerprint: "fp123",
		ExpiresAt:   expiresAt.Unix(),
	}, handler.customerSessionSecret, guestOrderPointerPurpose)
	if err != nil {
		t.Fatalf("Encode returned error: %v", err)
	}
	if handler.validGuestOrderAccess(accessRequest(guestStateTestOrderID, pointerValue), guestStateTestOrderID) {
		t.Fatal("validGuestOrderAccess accepted a pointer-purpose value")
	}

	// Expiry honored via the injected clock: valid just before, dead after.
	baseNow := handler.currentTime()
	handler.now = func() time.Time { return expiresAt.Add(-time.Minute) }
	if !handler.validGuestOrderAccess(accessRequest(guestStateTestOrderID, token), guestStateTestOrderID) {
		t.Fatal("validGuestOrderAccess rejected a token inside its window")
	}
	handler.now = func() time.Time { return expiresAt.Add(time.Minute) }
	if handler.validGuestOrderAccess(accessRequest(guestStateTestOrderID, token), guestStateTestOrderID) {
		t.Fatal("validGuestOrderAccess accepted a token past its window")
	}
	handler.now = func() time.Time { return baseNow }
}

func TestGuestOrderAccessPath(t *testing.T) {
	got := guestOrderAccessPath(guestStateTestOrderID, "tok/en+value")
	want := "/orders/" + guestStateTestOrderID + "?access=tok%2Fen%2Bvalue"
	if got != want {
		t.Fatalf("guestOrderAccessPath = %q, want %q", got, want)
	}
}
