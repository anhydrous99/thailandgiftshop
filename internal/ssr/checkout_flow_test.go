package ssr

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/cart"
	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/checkout"
	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/observability"
	"github.com/anhydrous99/thailandgiftshop/internal/payments"
	"github.com/anhydrous99/thailandgiftshop/internal/signedtoken"
	"github.com/aws/aws-lambda-go/events"
)

// --- Routing and method gates ---

func TestCheckoutFlowRouteForPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want pageRoute
	}{
		{
			name: "checkout place order",
			path: "/checkout/place-order",
			want: pageRoute{kind: pageCheckoutPlaceOrder, knownPageShape: true},
		},
		{
			name: "checkout confirm",
			path: "/checkout/confirm",
			want: pageRoute{kind: pageCheckoutConfirm, knownPageShape: true},
		},
		{
			name: "checkout confirm trailing slash redirect",
			path: "/checkout/confirm/",
			want: pageRoute{kind: pageCheckoutConfirm, redirectTo: "/checkout/confirm", knownPageShape: true},
		},
		{
			name: "checkout fake pay",
			path: "/checkout/fake-pay",
			want: pageRoute{kind: pageCheckoutFakePay, knownPageShape: true},
		},
		{
			name: "checkout fake pay trailing slash redirect",
			path: "/checkout/fake-pay/",
			want: pageRoute{kind: pageCheckoutFakePay, redirectTo: "/checkout/fake-pay", knownPageShape: true},
		},
		{
			name: "orders",
			path: "/orders",
			want: pageRoute{kind: pageOrders, knownPageShape: true},
		},
		{
			name: "orders trailing slash redirect",
			path: "/orders/",
			want: pageRoute{kind: pageOrders, redirectTo: "/orders", knownPageShape: true},
		},
		{
			name: "order detail",
			path: "/orders/" + ssrTestAccountID,
			want: pageRoute{kind: pageOrderDetail, slug: ssrTestAccountID, knownPageShape: true},
		},
		{
			name: "order detail trailing slash redirect",
			path: "/orders/" + ssrTestAccountID + "/",
			want: pageRoute{kind: pageOrderDetail, slug: ssrTestAccountID, redirectTo: "/orders/" + ssrTestAccountID, knownPageShape: true},
		},
		{
			name: "stripe webhook",
			path: "/webhooks/stripe",
			want: pageRoute{kind: pageStripeWebhook, knownPageShape: true},
		},
		{
			name: "order detail rejects malformed id",
			path: "/orders/UPPERCASE-IS-NOT-AN-ID-1234",
			want: pageRoute{kind: pageUnknown},
		},
		{
			name: "order detail rejects short id",
			path: "/orders/tooshort",
			want: pageRoute{kind: pageUnknown},
		},
		{
			name: "order detail rejects extra segments",
			path: "/orders/" + ssrTestAccountID + "/extra",
			want: pageRoute{kind: pageUnknown},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := routeForPath(test.path); got != test.want {
				t.Fatalf("routeForPath(%q) = %#v, want %#v", test.path, got, test.want)
			}
		})
	}
}

func TestCheckoutFlowMethodGates(t *testing.T) {
	for _, path := range []string{"/checkout/place-order", "/webhooks/stripe"} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut} {
			t.Run(method+" "+path, func(t *testing.T) {
				response, err := Handle(context.Background(), pageRequest(method, path))
				if err != nil {
					t.Fatalf("Handle returned error: %v", err)
				}
				if response.StatusCode != http.StatusMethodNotAllowed {
					t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusMethodNotAllowed)
				}
				if got := response.Headers["Allow"]; got != http.MethodPost {
					t.Fatalf("Allow = %q, want POST", got)
				}
			})
		}
	}

	for _, path := range []string{"/checkout/confirm", "/orders", "/orders/" + ssrTestAccountID} {
		t.Run("POST "+path, func(t *testing.T) {
			response, err := Handle(context.Background(), pageRequest(http.MethodPost, path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusMethodNotAllowed)
			}
			if got := response.Headers["Allow"]; got != allowedMethods {
				t.Fatalf("Allow = %q, want %q", got, allowedMethods)
			}
		})
	}

	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		t.Run(method+" /checkout/fake-pay", func(t *testing.T) {
			response, err := Handle(context.Background(), pageRequest(method, "/checkout/fake-pay"))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusMethodNotAllowed)
			}
			if got := response.Headers["Allow"]; got != accountFormAllowedMethods {
				t.Fatalf("Allow = %q, want %q", got, accountFormAllowedMethods)
			}
		})
	}
}

// TestCheckoutFlowAnonymousRedirectsToSignIn pins the anonymous behavior of
// the checkout-flow routes after guest checkout: /orders and /orders/{id}
// stay sign-in gated; /checkout renders the guest layout; place-order without
// a guest CSRF pair re-renders the guest page 403; confirm and fake-pay 404
// for absent/unknown sessions (the session gate no longer runs first).
func TestCheckoutFlowAnonymousRedirectsToSignIn(t *testing.T) {
	env := newAccountTestEnv(t)
	redirects := []struct {
		name     string
		method   string
		path     string
		location string
	}{
		{
			name:     "orders",
			method:   http.MethodGet,
			path:     "/orders",
			location: "/account/sign-in?return_to=%2Forders",
		},
		{
			name:     "order detail",
			method:   http.MethodGet,
			path:     "/orders/" + ssrTestAccountID,
			location: "/account/sign-in?return_to=" + url.QueryEscape("/orders/"+ssrTestAccountID),
		},
	}
	for _, test := range redirects {
		t.Run(test.name, func(t *testing.T) {
			response, err := env.handler.Handle(context.Background(), pageRequest(test.method, test.path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusSeeOther {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusSeeOther)
			}
			if got := response.Headers["Location"]; got != test.location {
				t.Fatalf("Location = %q, want %q", got, test.location)
			}
			if got := response.Headers["Cache-Control"]; got != privatePageCacheControl {
				t.Fatalf("Cache-Control = %q, want %q", got, privatePageCacheControl)
			}
		})
	}

	notFounds := []struct {
		name   string
		method string
		path   string
		query  map[string]string
	}{
		{name: "confirm without session id", method: http.MethodGet, path: "/checkout/confirm"},
		{name: "confirm with unknown session id", method: http.MethodGet, path: "/checkout/confirm", query: map[string]string{"session_id": "cs_fake_x"}},
		{name: "fake pay without session id", method: http.MethodGet, path: "/checkout/fake-pay"},
		{name: "fake pay with unknown session id", method: http.MethodGet, path: "/checkout/fake-pay", query: map[string]string{"session_id": "cs_fake_x"}},
	}
	for _, test := range notFounds {
		t.Run(test.name, func(t *testing.T) {
			request := pageRequest(test.method, test.path)
			request.QueryStringParameters = test.query
			response, err := env.handler.Handle(context.Background(), request)
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusNotFound)
			}
		})
	}

	t.Run("place order without a guest csrf pair re-renders 403", func(t *testing.T) {
		request := formPostRequest("/checkout/place-order", url.Values{"email": {"guest@example.com"}}.Encode())
		request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{{Slug: "mango-sticky-rice-kit", Quantity: 1}})}
		response, err := env.handler.Handle(context.Background(), request)
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusForbidden)
		}
		assertBodyContains(t, response.Body, []string{`data-testid="guest-checkout-form"`, expiredFormError})
	})

	t.Run("checkout with items renders the guest layout", func(t *testing.T) {
		request := pageRequest(http.MethodGet, "/checkout")
		request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{{Slug: "mango-sticky-rice-kit", Quantity: 1}})}
		response, err := env.handler.Handle(context.Background(), request)
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("response = %d %q, want 200 guest checkout", response.StatusCode, response.Headers["Location"])
		}
		assertBodyContains(t, response.Body, []string{
			`data-testid="guest-checkout-form"`,
			`data-testid="guest-email-input"`,
			`data-testid="checkout-sign-in-link"`,
		})
	})
}

func TestCheckoutSignedInEmptyAndUnavailableCartGuardPreserved(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")

	response, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/checkout", jar))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther || response.Headers["Location"] != "/cart" {
		t.Fatalf("empty-cart checkout = %d %q, want 303 /cart", response.StatusCode, response.Headers["Location"])
	}
	if got := response.Headers["Cache-Control"]; got != privatePageCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, privatePageCacheControl)
	}
}

// --- Place order ---

// checkoutTestSetup signs up a customer, fills the server cart through the
// real mutation route, and saves an address; it returns the jar and the
// saved address id.
func checkoutTestSetup(t *testing.T, env accountTestEnv, quantity int) (testCookieJar, string) {
	t.Helper()
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	addToTestCart(t, env.handler, jar, "mango-sticky-rice-kit", quantity)
	createTestAddress(t, env.handler, jar, "Anong Shopper")
	listResponse, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/account/addresses", jar))
	if err != nil {
		t.Fatalf("Handle addresses returned error: %v", err)
	}
	jar.update(t, listResponse)
	return jar, firstAddressIDFromBody(t, listResponse.Body)
}

// placeTestOrder POSTs the place-order form and returns the order id and the
// fake checkout session id parsed from the redirect.
func placeTestOrder(t *testing.T, env accountTestEnv, jar testCookieJar, addressID string) (string, string) {
	t.Helper()
	token := accountCSRFToken(t, env.handler, jar)
	response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/place-order", url.Values{
		customerCSRFFieldName: {token},
		"address_id":          {addressID},
	}, jar))
	if err != nil {
		t.Fatalf("Handle place-order returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("place-order status = %d body %q, want 303", response.StatusCode, response.Body)
	}
	jar.update(t, response)
	location := response.Headers["Location"]
	marker := "/checkout/fake-pay?session_id="
	index := strings.Index(location, marker)
	if index < 0 {
		t.Fatalf("place-order Location = %q, want fake-pay redirect", location)
	}
	sessionID := location[index+len(marker):]
	orderID := strings.TrimPrefix(sessionID, "cs_fake_")
	if orderID == sessionID || !accountIDPattern.MatchString(orderID) {
		t.Fatalf("could not parse order id from session %q", sessionID)
	}
	return orderID, sessionID
}

func mangoStock(t *testing.T, env accountTestEnv) int {
	t.Helper()
	product, found, err := env.catalog.GetProductBySlug(context.Background(), "mango-sticky-rice-kit")
	if err != nil || !found {
		t.Fatalf("GetProductBySlug = found %t, err %v", found, err)
	}
	return product.StockQuantity
}

func TestPlaceOrderCreatesPendingOrderReservesStockAndRedirects(t *testing.T) {
	env := newAccountTestEnv(t)
	jar, addressID := checkoutTestSetup(t, env, 2)

	orderID, sessionID := placeTestOrder(t, env, jar, addressID)

	order, found, err := env.commerce.GetOrder(context.Background(), orderID)
	if err != nil || !found {
		t.Fatalf("GetOrder = found %t, err %v", found, err)
	}
	if order.Status != commerce.OrderStatusPendingPayment {
		t.Fatalf("order status = %q, want pending_payment", order.Status)
	}
	if order.StripeCheckoutSessionID != sessionID {
		t.Fatalf("order session id = %q, want %q", order.StripeCheckoutSessionID, sessionID)
	}
	if order.Email != "shopper@example.com" {
		t.Fatalf("order email = %q", order.Email)
	}
	if len(order.Lines) != 1 || order.Lines[0].Slug != "mango-sticky-rice-kit" || order.Lines[0].Quantity != 2 || order.Lines[0].UnitPriceCents != 2899 || order.Lines[0].LineTotalCents != 5798 {
		t.Fatalf("order lines = %#v, want frozen mango snapshot", order.Lines)
	}
	if order.TotalCents != 5798 || order.SubtotalCents != 5798 || order.ShippingCents != 0 || order.TaxCents != 0 {
		t.Fatalf("order totals = %d/%d/%d/%d, want 5798 subtotal and zero shipping/tax", order.SubtotalCents, order.ShippingCents, order.TaxCents, order.TotalCents)
	}
	if order.ShippingAddress.FullName != "Anong Shopper" || order.ShippingAddress.Line1 != "123 Sukhumvit Rd" {
		t.Fatalf("order shipping address = %#v, want snapshot", order.ShippingAddress)
	}
	if order.CartFingerprint == "" {
		t.Fatal("order cart fingerprint is empty")
	}

	// Stock is reserved at place-order time.
	if got := mangoStock(t, env); got != 3 {
		t.Fatalf("mango stock = %d, want 3 after reserving 2 of 5", got)
	}

	// The cart points at the pending order but is NOT cleared: a canceled
	// payment returns the shopper to an intact cart.
	customerID := accountCustomerID(t, env, "shopper@example.com")
	record, _, err := env.commerce.GetCart(context.Background(), customerID)
	if err != nil {
		t.Fatalf("GetCart returned error: %v", err)
	}
	if record.PendingOrderID != orderID {
		t.Fatalf("cart pending order = %q, want %q", record.PendingOrderID, orderID)
	}
	if len(record.Lines) != 1 || record.Lines[0].Quantity != 2 {
		t.Fatalf("cart lines after place-order = %#v, want intact mango 2", record.Lines)
	}
}

func TestPlaceOrderRequiresCSRF(t *testing.T) {
	env := newAccountTestEnv(t)
	jar, addressID := checkoutTestSetup(t, env, 1)

	response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/place-order", url.Values{
		"address_id": {addressID},
	}, jar))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusForbidden)
	}
	if got := mangoStock(t, env); got != 5 {
		t.Fatalf("mango stock = %d, want untouched 5", got)
	}
}

func TestPlaceOrderWithoutAddressRerendersChooseAddress(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	addToTestCart(t, env.handler, jar, "mango-sticky-rice-kit", 1)
	token := accountCSRFToken(t, env.handler, jar)

	response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/place-order", url.Values{
		customerCSRFFieldName: {token},
	}, jar))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}
	assertBodyContains(t, response.Body, []string{chooseAddressError, `data-testid="checkout-error"`})
	if got := mangoStock(t, env); got != 5 {
		t.Fatalf("mango stock = %d, want untouched 5", got)
	}
}

// failingStockStore loses every reservation with a typed insufficient-stock
// error, standing in for the buyer-vs-buyer race the version-conditioned
// catalog write resolves.
type failingStockStore struct {
	err error
}

func (s failingStockStore) AdjustStock(ctx context.Context, adjustments []catalog.StockAdjustment) error {
	_ = ctx
	_ = adjustments
	return s.err
}

func TestPlaceOrderInsufficientStockRerendersWithReclampedCart(t *testing.T) {
	env := newAccountTestEnv(t)
	jar, addressID := checkoutTestSetup(t, env, 3)
	customerID := accountCustomerID(t, env, "shopper@example.com")

	// A concurrent buyer takes 4 of the 5 units, and the reservation loses.
	if err := env.catalog.AdjustStock(context.Background(), []catalog.StockAdjustment{{ProductID: "prod_account_mango", Delta: -4}}); err != nil {
		t.Fatalf("AdjustStock returned error: %v", err)
	}
	env.handler.checkout.Stock = failingStockStore{err: catalog.InsufficientStockError{
		ProductID: "prod_account_mango",
		Slug:      "mango-sticky-rice-kit",
		Available: 1,
	}}

	token := accountCSRFToken(t, env.handler, jar)
	response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/place-order", url.Values{
		customerCSRFFieldName: {token},
		"address_id":          {addressID},
	}, jar))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("status code = %d body %q, want %d", response.StatusCode, response.Body, http.StatusConflict)
	}
	assertBodyContains(t, response.Body, []string{
		checkoutStockChangedError,
		`data-testid="checkout-line-notice"`,
		`Only 1 left of Mango Sticky Rice Treats — quantities updated.`,
		`Quantity 1`,
	})

	// The re-clamped cart was persisted to the CART row.
	if lines := serverCartLines(t, env, customerID); len(lines) != 1 || lines[0].Quantity != 1 {
		t.Fatalf("re-clamped server cart = %#v, want mango 1", lines)
	}
}

// inFlightSessionProvider reports one session as completed with its
// asynchronous payment still settling — the state the provider refuses to
// expire, so a pending order pointing at it cannot be canceled.
type inFlightSessionProvider struct {
	payments.Provider
	sessionID string
}

func (p inFlightSessionProvider) GetSession(ctx context.Context, sessionID string) (payments.Session, error) {
	session, err := p.Provider.GetSession(ctx, sessionID)
	if err == nil && sessionID == p.sessionID {
		session.Status = payments.SessionStatusComplete
		session.PaymentStatus = "unpaid"
		session.URL = ""
	}
	return session, err
}

func TestPlaceOrderPaymentInFlightRerendersCheckoutWithNotice(t *testing.T) {
	env := newAccountTestEnv(t)
	jar, addressID := checkoutTestSetup(t, env, 1)
	orderID, sessionID := placeTestOrder(t, env, jar, addressID)

	// The shopper edits the cart (fingerprint change) while the first order's
	// completed checkout session is still settling its asynchronous payment,
	// so the stale-pointer cleanup refuses to cancel the pending order.
	addToTestCart(t, env.handler, jar, "thai-tea-sampler", 1)
	env.handler.checkout.Payments = inFlightSessionProvider{Provider: env.payments, sessionID: sessionID}

	token := accountCSRFToken(t, env.handler, jar)
	response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/place-order", url.Values{
		customerCSRFFieldName: {token},
		"address_id":          {addressID},
	}, jar))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d body %q, want %d", response.StatusCode, response.Body, http.StatusConflict)
	}
	assertBodyContains(t, response.Body, []string{
		checkoutPaymentInFlightError,
		`data-testid="checkout-error"`,
	})

	// The pending order and its reservation are untouched; no fresh order was
	// placed.
	order, found, err := env.commerce.GetOrder(context.Background(), orderID)
	if err != nil || !found {
		t.Fatalf("GetOrder = found %t, err %v", found, err)
	}
	if order.Status != commerce.OrderStatusPendingPayment {
		t.Fatalf("order status = %q, want still pending_payment", order.Status)
	}
	if got := mangoStock(t, env); got != 4 {
		t.Fatalf("mango stock = %d, want 4 (single original reservation)", got)
	}
}

// --- Fake pay and confirm ---

func TestFakePayThroughConfirmPlacesPaidOrder(t *testing.T) {
	env := newAccountTestEnv(t)
	jar, addressID := checkoutTestSetup(t, env, 2)
	customerID := accountCustomerID(t, env, "shopper@example.com")
	orderID, sessionID := placeTestOrder(t, env, jar, addressID)

	// The fake-pay page renders the total and both actions.
	payPageRequest := jarPageRequest(http.MethodGet, "/checkout/fake-pay", jar)
	payPageRequest.QueryStringParameters = map[string]string{"session_id": sessionID}
	payPage, err := env.handler.Handle(context.Background(), payPageRequest)
	if err != nil {
		t.Fatalf("Handle fake-pay returned error: %v", err)
	}
	if payPage.StatusCode != http.StatusOK {
		t.Fatalf("fake-pay status = %d body %q, want 200", payPage.StatusCode, payPage.Body)
	}
	jar.update(t, payPage)
	assertBodyContains(t, payPage.Body, []string{
		`Demo payment — no real charge`,
		`data-testid="fake-pay-button"`,
		`Pay $57.98`,
		`name="save_card"`,
		`data-testid="fake-pay-cancel"`,
	})

	// Pay: the fake provider marks the session paid and 303s to the real
	// confirm reconcile URL.
	csrfToken := hiddenInputValue(t, payPage.Body, customerCSRFFieldName)
	payResponse, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/fake-pay", url.Values{
		customerCSRFFieldName: {csrfToken},
		"session_id":          {sessionID},
		"action":              {"pay"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle fake-pay POST returned error: %v", err)
	}
	if payResponse.StatusCode != http.StatusSeeOther {
		t.Fatalf("fake-pay POST status = %d, want 303", payResponse.StatusCode)
	}
	wantConfirmURL := "http://127.0.0.1:8080/checkout/confirm?session_id=" + sessionID
	if got := payResponse.Headers["Location"]; got != wantConfirmURL {
		t.Fatalf("fake-pay Location = %q, want %q", got, wantConfirmURL)
	}

	// Confirm reconciles server-side and redirects to the order with a
	// cleared tgs_cart mirror.
	confirmRequest := jarPageRequest(http.MethodGet, "/checkout/confirm", jar)
	confirmRequest.QueryStringParameters = map[string]string{"session_id": sessionID}
	confirmResponse, err := env.handler.Handle(context.Background(), confirmRequest)
	if err != nil {
		t.Fatalf("Handle confirm returned error: %v", err)
	}
	if confirmResponse.StatusCode != http.StatusSeeOther {
		t.Fatalf("confirm status = %d body %q, want 303", confirmResponse.StatusCode, confirmResponse.Body)
	}
	if got := confirmResponse.Headers["Location"]; got != "/orders/"+orderID+"?placed=1" {
		t.Fatalf("confirm Location = %q, want order detail", got)
	}
	if raw := rawSetCookie(t, confirmResponse, cart.CookieName); !strings.Contains(raw, "Max-Age=0") {
		t.Fatalf("confirm cart mirror = %q, want clearing cookie", raw)
	}
	jar.update(t, confirmResponse)

	order, _, err := env.commerce.GetOrder(context.Background(), orderID)
	if err != nil {
		t.Fatalf("GetOrder returned error: %v", err)
	}
	if order.Status != commerce.OrderStatusPaid {
		t.Fatalf("order status = %q, want paid", order.Status)
	}
	if order.StripePaymentIntentID == "" || order.PaymentCardBrand != "visa" || order.PaymentCardLast4 != "4242" {
		t.Fatalf("order payment patch = %q/%q/%q, want fake card details", order.StripePaymentIntentID, order.PaymentCardBrand, order.PaymentCardLast4)
	}
	record, _, err := env.commerce.GetCart(context.Background(), customerID)
	if err != nil {
		t.Fatalf("GetCart returned error: %v", err)
	}
	if len(record.Lines) != 0 || record.PendingOrderID != "" {
		t.Fatalf("cart after payment = %#v, want cleared with no pending pointer", record)
	}

	// The order page shows the placed banner, status, timeline, and totals.
	detailRequest := jarPageRequest(http.MethodGet, "/orders/"+orderID, jar)
	detailRequest.QueryStringParameters = map[string]string{"placed": "1"}
	detailResponse, err := env.handler.Handle(context.Background(), detailRequest)
	if err != nil {
		t.Fatalf("Handle order detail returned error: %v", err)
	}
	if detailResponse.StatusCode != http.StatusOK {
		t.Fatalf("order detail status = %d, want 200", detailResponse.StatusCode)
	}
	if got := detailResponse.Headers["Cache-Control"]; got != privatePageCacheControl {
		t.Fatalf("order detail Cache-Control = %q, want %q", got, privatePageCacheControl)
	}
	if got := detailResponse.Headers["X-Robots-Tag"]; got != "noindex, follow" {
		t.Fatalf("order detail X-Robots-Tag = %q, want noindex, follow", got)
	}
	assertBodyContains(t, detailResponse.Body, []string{
		`data-testid="order-placed-banner"`,
		`data-testid="order-status"`,
		`>Paid</span>`,
		`data-testid="order-timeline-step"`,
		`Pending payment`,
		`Mango Sticky Rice Treats`,
		`$57.98`,
		`Visa •••• 4242`,
		`Anong Shopper`,
	})

	// The order history lists it.
	ordersResponse, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/orders", jar))
	if err != nil {
		t.Fatalf("Handle orders returned error: %v", err)
	}
	if ordersResponse.StatusCode != http.StatusOK {
		t.Fatalf("orders status = %d, want 200", ordersResponse.StatusCode)
	}
	assertBodyContains(t, ordersResponse.Body, []string{`data-testid="order-row"`, orderID, `$57.98`})
}

func TestFakePayCancelKeepsCartIntact(t *testing.T) {
	env := newAccountTestEnv(t)
	jar, addressID := checkoutTestSetup(t, env, 2)
	customerID := accountCustomerID(t, env, "shopper@example.com")
	_, sessionID := placeTestOrder(t, env, jar, addressID)
	token := accountCSRFToken(t, env.handler, jar)

	cancelResponse, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/fake-pay", url.Values{
		customerCSRFFieldName: {token},
		"session_id":          {sessionID},
		"action":              {"cancel"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle cancel returned error: %v", err)
	}
	if cancelResponse.StatusCode != http.StatusSeeOther {
		t.Fatalf("cancel status = %d, want 303", cancelResponse.StatusCode)
	}
	if got := cancelResponse.Headers["Location"]; got != "http://127.0.0.1:8080/checkout?canceled=1" {
		t.Fatalf("cancel Location = %q, want checkout canceled URL", got)
	}

	checkoutRequest := jarPageRequest(http.MethodGet, "/checkout", jar)
	checkoutRequest.QueryStringParameters = map[string]string{"canceled": "1"}
	checkoutResponse, err := env.handler.Handle(context.Background(), checkoutRequest)
	if err != nil {
		t.Fatalf("Handle checkout returned error: %v", err)
	}
	if checkoutResponse.StatusCode != http.StatusOK {
		t.Fatalf("checkout status = %d, want 200", checkoutResponse.StatusCode)
	}
	assertBodyContains(t, checkoutResponse.Body, []string{
		`data-testid="checkout-canceled-notice"`,
		`Payment canceled. Your cart is unchanged`,
		`Mango Sticky Rice Treats`,
	})
	if lines := serverCartLines(t, env, customerID); len(lines) != 1 || lines[0].Quantity != 2 {
		t.Fatalf("cart after cancel = %#v, want intact mango 2", lines)
	}
}

func TestConfirmRendersProcessingPageWhileUnpaid(t *testing.T) {
	env := newAccountTestEnv(t)
	jar, addressID := checkoutTestSetup(t, env, 1)
	orderID, sessionID := placeTestOrder(t, env, jar, addressID)

	confirmRequest := jarPageRequest(http.MethodGet, "/checkout/confirm", jar)
	confirmRequest.QueryStringParameters = map[string]string{"session_id": sessionID}
	response, err := env.handler.Handle(context.Background(), confirmRequest)
	if err != nil {
		t.Fatalf("Handle confirm returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("confirm status = %d, want 200 processing page", response.StatusCode)
	}
	assertBodyContains(t, response.Body, []string{
		`<meta http-equiv="refresh" content="3">`,
		`data-testid="checkout-processing"`,
		`Payment processing`,
		`/orders/` + orderID,
	})
	if got := response.Headers["Cache-Control"]; got != privatePageCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, privatePageCacheControl)
	}

	order, _, err := env.commerce.GetOrder(context.Background(), orderID)
	if err != nil {
		t.Fatalf("GetOrder returned error: %v", err)
	}
	if order.Status != commerce.OrderStatusPendingPayment {
		t.Fatalf("order status = %q, want still pending_payment", order.Status)
	}
}

func TestConfirmRendersPaymentReceivedPageForTerminalOrder(t *testing.T) {
	env := newAccountTestEnv(t)
	jar, addressID := checkoutTestSetup(t, env, 1)
	orderID, sessionID := placeTestOrder(t, env, jar, addressID)

	// An admin cancel raced the shopper: the order reaches a terminal status
	// directly while the still-open session collects the payment.
	if _, err := env.commerce.TransitionOrder(context.Background(), orderID, commerce.OrderStatusPendingPayment, commerce.OrderStatusCanceled, commerce.OrderPatch{Actor: commerce.OrderActorAdmin}); err != nil {
		t.Fatalf("TransitionOrder returned error: %v", err)
	}
	if _, err := env.payments.MarkSessionPaid(sessionID, false); err != nil {
		t.Fatalf("MarkSessionPaid returned error: %v", err)
	}

	confirmRequest := jarPageRequest(http.MethodGet, "/checkout/confirm", jar)
	confirmRequest.QueryStringParameters = map[string]string{"session_id": sessionID}
	response, err := env.handler.Handle(context.Background(), confirmRequest)
	if err != nil {
		t.Fatalf("Handle confirm returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("confirm status = %d body %q, want 200 payment-received page, never an error page for a charged customer", response.StatusCode, response.Body)
	}
	assertBodyContains(t, response.Body, []string{
		`data-testid="checkout-payment-received"`,
		`Payment received`,
		`A refund is being issued automatically`,
		orderID,
	})

	// The canceled order is never resurrected by the confirm page.
	order, _, err := env.commerce.GetOrder(context.Background(), orderID)
	if err != nil {
		t.Fatalf("GetOrder returned error: %v", err)
	}
	if order.Status != commerce.OrderStatusCanceled {
		t.Fatalf("order status = %q, want still canceled", order.Status)
	}
}

func TestConfirmAndOrderDetailAreOwnershipGuarded(t *testing.T) {
	env := newAccountTestEnv(t)
	jar, addressID := checkoutTestSetup(t, env, 1)
	orderID, sessionID := placeTestOrder(t, env, jar, addressID)

	otherJar := testCookieJar{}
	signUpTestCustomer(t, env.handler, otherJar, "intruder@example.com", "orchid-market-99")

	confirmRequest := jarPageRequest(http.MethodGet, "/checkout/confirm", otherJar)
	confirmRequest.QueryStringParameters = map[string]string{"session_id": sessionID}
	confirmResponse, err := env.handler.Handle(context.Background(), confirmRequest)
	if err != nil {
		t.Fatalf("Handle confirm returned error: %v", err)
	}
	if confirmResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-customer confirm status = %d, want 404", confirmResponse.StatusCode)
	}

	detailResponse, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/orders/"+orderID, otherJar))
	if err != nil {
		t.Fatalf("Handle order detail returned error: %v", err)
	}
	if detailResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-customer order detail status = %d, want 404 (never 403)", detailResponse.StatusCode)
	}

	fakePayRequest := jarPageRequest(http.MethodGet, "/checkout/fake-pay", otherJar)
	fakePayRequest.QueryStringParameters = map[string]string{"session_id": sessionID}
	fakePayResponse, err := env.handler.Handle(context.Background(), fakePayRequest)
	if err != nil {
		t.Fatalf("Handle fake-pay returned error: %v", err)
	}
	if fakePayResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-customer fake-pay status = %d, want 404", fakePayResponse.StatusCode)
	}
}

func TestOrderDetailReconcilesPendingOrderWhenSessionPaid(t *testing.T) {
	env := newAccountTestEnv(t)
	jar, addressID := checkoutTestSetup(t, env, 1)
	orderID, sessionID := placeTestOrder(t, env, jar, addressID)

	// The payment landed but neither the webhook nor confirm ran yet.
	if _, err := env.payments.MarkSessionPaid(sessionID, false); err != nil {
		t.Fatalf("MarkSessionPaid returned error: %v", err)
	}

	response, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/orders/"+orderID, jar))
	if err != nil {
		t.Fatalf("Handle order detail returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("order detail status = %d, want 200", response.StatusCode)
	}
	assertBodyContains(t, response.Body, []string{`data-testid="order-status"`, `>Paid</span>`})

	order, _, err := env.commerce.GetOrder(context.Background(), orderID)
	if err != nil {
		t.Fatalf("GetOrder returned error: %v", err)
	}
	if order.Status != commerce.OrderStatusPaid {
		t.Fatalf("order status = %q, want paid after reconcile", order.Status)
	}
}

func TestOrderSnapshotFrozenAgainstLaterCatalogEdits(t *testing.T) {
	env := newAccountTestEnv(t)
	jar, addressID := checkoutTestSetup(t, env, 2)
	orderID, _ := placeTestOrder(t, env, jar, addressID)

	product, found, err := env.catalog.GetProductBySlug(context.Background(), "mango-sticky-rice-kit")
	if err != nil || !found {
		t.Fatalf("GetProductBySlug = found %t, err %v", found, err)
	}
	repriced := product
	repriced.PriceCents = 9999
	repriced.Name = "Renamed Mango Treats"
	if _, err := env.catalog.UpdateProduct(context.Background(), product, repriced); err != nil {
		t.Fatalf("UpdateProduct returned error: %v", err)
	}

	response, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/orders/"+orderID, jar))
	if err != nil {
		t.Fatalf("Handle order detail returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("order detail status = %d, want 200", response.StatusCode)
	}
	assertBodyContains(t, response.Body, []string{`Mango Sticky Rice Treats`, `$28.99`, `$57.98`})
	assertBodyOmits(t, response.Body, []string{`Renamed Mango Treats`, `$99.99`})

	order, _, err := env.commerce.GetOrder(context.Background(), orderID)
	if err != nil {
		t.Fatalf("GetOrder returned error: %v", err)
	}
	if order.Lines[0].UnitPriceCents != 2899 || order.Lines[0].Name != "Mango Sticky Rice Treats" {
		t.Fatalf("order snapshot = %#v, want frozen price and name", order.Lines[0])
	}
}

// stubStripeKindProvider stands in for the production Stripe provider so the
// fake-pay 404 gate can be exercised.
type stubStripeKindProvider struct{}

func (stubStripeKindProvider) Kind() string { return payments.KindStripe }
func (stubStripeKindProvider) EnsureCustomer(ctx context.Context, customerID string, email string) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (stubStripeKindProvider) CreatePaymentSession(ctx context.Context, input payments.PaymentSessionInput) (payments.Session, error) {
	return payments.Session{}, fmt.Errorf("not implemented")
}
func (stubStripeKindProvider) CreateSetupSession(ctx context.Context, input payments.SetupSessionInput) (payments.Session, error) {
	return payments.Session{}, fmt.Errorf("not implemented")
}
func (stubStripeKindProvider) GetSession(ctx context.Context, sessionID string) (payments.Session, error) {
	return payments.Session{}, payments.ErrSessionNotFound
}
func (stubStripeKindProvider) ExpireSession(ctx context.Context, sessionID string) error {
	return payments.ErrSessionNotFound
}
func (stubStripeKindProvider) CreateRefund(ctx context.Context, input payments.RefundInput) (payments.Refund, error) {
	return payments.Refund{}, fmt.Errorf("not implemented")
}
func (stubStripeKindProvider) GetRefund(ctx context.Context, refundID string) (payments.Refund, error) {
	return payments.Refund{}, payments.ErrRefundNotFound
}
func (stubStripeKindProvider) ListPaymentMethods(ctx context.Context, stripeCustomerID string) ([]payments.PaymentMethod, error) {
	return nil, nil
}
func (stubStripeKindProvider) DetachPaymentMethod(ctx context.Context, stripeCustomerID string, paymentMethodID string) error {
	return payments.ErrPaymentMethodNotFound
}
func (stubStripeKindProvider) ParseWebhook(payload []byte, signatureHeader string, now time.Time) (payments.Event, error) {
	return payments.Event{}, fmt.Errorf("not implemented")
}

func TestFakePay404sUnlessProviderIsFake(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	env.handler.payments = stubStripeKindProvider{}

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		request := jarPageRequest(method, "/checkout/fake-pay", jar)
		request.QueryStringParameters = map[string]string{"session_id": "cs_fake_anything"}
		response, err := env.handler.Handle(context.Background(), request)
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("%s fake-pay status = %d, want 404 with non-fake provider", method, response.StatusCode)
		}
	}
}

// --- Orders paging ---

// ordersPagingBase is the creation time of the first (oldest) order the
// paging tests seed; each later order is one minute newer.
var ordersPagingBase = time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)

// createPagingTestOrders seeds count paid orders for the customer, one minute
// apart starting at ordersPagingBase, and returns their IDs oldest-first.
func createPagingTestOrders(t *testing.T, env accountTestEnv, customerID string, email string, count int) []string {
	t.Helper()
	orderIDs := make([]string, 0, count)
	for index := range count {
		created, err := env.commerce.CreateOrder(context.Background(), commerce.Order{
			CustomerID:    customerID,
			Email:         email,
			Status:        commerce.OrderStatusPaid,
			Lines:         []commerce.OrderLine{{Slug: "mango-sticky-rice-kit", ProductID: "prod_account_mango", Name: "Mango Sticky Rice Treats", UnitPriceCents: 2899, Quantity: 1, LineTotalCents: 2899}},
			SubtotalCents: 2899,
			TotalCents:    2899,
			Currency:      "usd",
			CreatedAt:     ordersPagingBase.Add(time.Duration(index) * time.Minute),
		})
		if err != nil {
			t.Fatalf("CreateOrder %d returned error: %v", index, err)
		}
		orderIDs = append(orderIDs, created.ID)
	}
	return orderIDs
}

var ordersCursorLinkPattern = regexp.MustCompile(`href="/orders\?after=([^"]+)"`)

// firstPageCursorToken renders /orders page 1 and returns the decoded signed
// cursor token from its older-orders link.
func firstPageCursorToken(t *testing.T, env accountTestEnv, jar testCookieJar) string {
	t.Helper()
	firstPage, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/orders", jar))
	if err != nil {
		t.Fatalf("Handle orders returned error: %v", err)
	}
	match := ordersCursorLinkPattern.FindStringSubmatch(firstPage.Body)
	if match == nil {
		t.Fatal("first page is missing the next-page cursor link")
	}
	token, err := url.QueryUnescape(match[1])
	if err != nil {
		t.Fatalf("cursor token does not query-unescape: %v", err)
	}
	return token
}

func TestOrdersPageListsNewestFirstWithCursorPaging(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	customerID := accountCustomerID(t, env, "shopper@example.com")

	orderIDs := createPagingTestOrders(t, env, customerID, "shopper@example.com", 25)
	newestID := orderIDs[len(orderIDs)-1]
	oldestID := orderIDs[0]

	firstPage, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/orders", jar))
	if err != nil {
		t.Fatalf("Handle orders returned error: %v", err)
	}
	if firstPage.StatusCode != http.StatusOK {
		t.Fatalf("orders status = %d, want 200", firstPage.StatusCode)
	}
	if got := strings.Count(firstPage.Body, `data-testid="order-row"`); got != ordersPageSize {
		t.Fatalf("first page rows = %d, want %d", got, ordersPageSize)
	}
	if !strings.Contains(firstPage.Body, newestID) {
		t.Fatal("first page does not contain the newest order")
	}
	if strings.Contains(firstPage.Body, `href="/orders/`+oldestID+`"`) {
		t.Fatal("first page should not contain the oldest order")
	}
	// Newest first: the newest order's row appears before an older one.
	if strings.Index(firstPage.Body, newestID) > strings.Index(firstPage.Body, orderIDs[len(orderIDs)-2]) {
		t.Fatal("orders are not sorted newest first")
	}

	// The next link carries a signed cursor token, not the pre-deploy bare
	// order-ID format.
	match := ordersCursorLinkPattern.FindStringSubmatch(firstPage.Body)
	if match == nil {
		t.Fatal("first page is missing the next-page cursor link")
	}
	token, err := url.QueryUnescape(match[1])
	if err != nil {
		t.Fatalf("cursor token does not query-unescape: %v", err)
	}
	if accountIDPattern.MatchString(token) {
		t.Fatalf("cursor %q is a bare order ID, want the signed token format", token)
	}

	// Payload-shape check: the token's first segment is world-readable JSON
	// (the envelope signs, it does not encrypt). Pin exactly what it carries
	// — position fields only, never the customer ID — so a future field
	// addition that leaks anything new fails here and forces a decision.
	payloadSegment, _, ok := strings.Cut(token, ".")
	if !ok {
		t.Fatalf("cursor token %q is not a payload.signature envelope", token)
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(payloadSegment)
	if err != nil {
		t.Fatalf("cursor payload segment does not base64-decode: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		t.Fatalf("cursor payload is not JSON: %v", err)
	}
	if len(payload) != 3 {
		t.Fatalf("cursor payload keys = %v, want exactly version, order_id, created_at", payload)
	}
	for _, key := range []string{"version", "order_id", "created_at"} {
		if _, found := payload[key]; !found {
			t.Fatalf("cursor payload %v is missing %q", payload, key)
		}
	}
	if _, found := payload["customer_id"]; found {
		t.Fatalf("cursor payload %v carries customer_id; the customer binding belongs in the MAC purpose only", payload)
	}
	if strings.Contains(string(payloadBytes), customerID) {
		t.Fatalf("cursor payload %q leaks the customer ID", payloadBytes)
	}

	secondPageRequest := jarPageRequest(http.MethodGet, "/orders", jar)
	secondPageRequest.QueryStringParameters = map[string]string{"after": token}
	secondPage, err := env.handler.Handle(context.Background(), secondPageRequest)
	if err != nil {
		t.Fatalf("Handle orders page 2 returned error: %v", err)
	}
	if got := strings.Count(secondPage.Body, `data-testid="order-row"`); got != 5 {
		t.Fatalf("second page rows = %d, want 5", got)
	}
	if !strings.Contains(secondPage.Body, oldestID) {
		t.Fatal("second page does not contain the oldest order")
	}
	if strings.Contains(secondPage.Body, `data-testid="orders-next-page"`) {
		t.Fatal("second page should not offer another page")
	}

	// An invalid cursor falls back to the first page.
	badCursorRequest := jarPageRequest(http.MethodGet, "/orders", jar)
	badCursorRequest.QueryStringParameters = map[string]string{"after": "not-a-valid-cursor"}
	badCursorPage, err := env.handler.Handle(context.Background(), badCursorRequest)
	if err != nil {
		t.Fatalf("Handle orders bad cursor returned error: %v", err)
	}
	if got := strings.Count(badCursorPage.Body, `data-testid="order-row"`); got != ordersPageSize {
		t.Fatalf("bad cursor rows = %d, want first page of %d", got, ordersPageSize)
	}
}

func TestOrdersPageCursorIsCustomerBound(t *testing.T) {
	env := newAccountTestEnv(t)

	jarA := testCookieJar{}
	signUpTestCustomer(t, env.handler, jarA, "alpha@example.com", "orchid-market-99")
	customerA := accountCustomerID(t, env, "alpha@example.com")
	ordersA := createPagingTestOrders(t, env, customerA, "alpha@example.com", 25)

	jarB := testCookieJar{}
	signUpTestCustomer(t, env.handler, jarB, "beta@example.com", "orchid-market-99")
	customerB := accountCustomerID(t, env, "beta@example.com")
	ordersB := createPagingTestOrders(t, env, customerB, "beta@example.com", 1)

	tokenA := firstPageCursorToken(t, env, jarA)

	// B replays A's cursor: the MAC purpose embeds B's customer ID, so A's
	// token fails verification and B gets their own first page — never an
	// error, never A's data.
	request := jarPageRequest(http.MethodGet, "/orders", jarB)
	request.QueryStringParameters = map[string]string{"after": tokenA}
	response, err := env.handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle orders with foreign cursor returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("foreign cursor status = %d, want 200", response.StatusCode)
	}
	if got := strings.Count(response.Body, `data-testid="order-row"`); got != 1 {
		t.Fatalf("foreign cursor rows = %d, want B's single order", got)
	}
	if !strings.Contains(response.Body, ordersB[0]) {
		t.Fatal("foreign cursor page is missing B's own order")
	}
	for _, orderID := range ordersA {
		if strings.Contains(response.Body, orderID) {
			t.Fatalf("foreign cursor page leaked A's order %s", orderID)
		}
	}
}

func TestOrdersPageRejectsCrossPurposeCursor(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	customerID := accountCustomerID(t, env, "shopper@example.com")
	orderIDs := createPagingTestOrders(t, env, customerID, "shopper@example.com", 25)

	// The payload is a real position (the 20th-newest order), so only the
	// MAC purpose decides whether it pages.
	payload := ordersCursorPayload{
		Version:   customerSignedValueVersion,
		OrderID:   orderIDs[5],
		CreatedAt: ordersPagingBase.Add(5 * time.Minute).UTC().Format(time.RFC3339),
	}

	// Sanity: under the correct customer-bound purpose the same payload
	// reaches page 2 — the rejections below are purely purpose-driven.
	boundToken, err := signedtoken.Encode(payload, ssrTestSessionSecret, ordersCursorTokenPurpose(customerID))
	if err != nil {
		t.Fatalf("Encode bound cursor returned error: %v", err)
	}
	crossPurposeToken, err := signedtoken.Encode(payload, ssrTestSessionSecret, customerCSRFPurpose)
	if err != nil {
		t.Fatalf("Encode cross-purpose cursor returned error: %v", err)
	}
	barePurposeToken, err := signedtoken.Encode(payload, ssrTestSessionSecret, customerOrdersCursorPurpose)
	if err != nil {
		t.Fatalf("Encode bare-purpose cursor returned error: %v", err)
	}

	tests := []struct {
		name     string
		token    string
		wantRows int
	}{
		{name: "customer-bound purpose pages", token: boundToken, wantRows: 5},
		{name: "csrf purpose restarts at page 1", token: crossPurposeToken, wantRows: ordersPageSize},
		{name: "bare cursor purpose without the customer ID restarts at page 1", token: barePurposeToken, wantRows: ordersPageSize},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := jarPageRequest(http.MethodGet, "/orders", jar)
			request.QueryStringParameters = map[string]string{"after": test.token}
			response, err := env.handler.Handle(context.Background(), request)
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.StatusCode)
			}
			if got := strings.Count(response.Body, `data-testid="order-row"`); got != test.wantRows {
				t.Fatalf("rows = %d, want %d", got, test.wantRows)
			}
		})
	}
}

func TestOrdersPageTamperedCursorFallsBackToFirstPage(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	customerID := accountCustomerID(t, env, "shopper@example.com")
	createPagingTestOrders(t, env, customerID, "shopper@example.com", 25)

	token := firstPageCursorToken(t, env, jar)

	// Flip one character of an otherwise valid envelope-shaped token.
	flipped := byte('A')
	if token[0] == flipped {
		flipped = 'B'
	}
	tampered := string(flipped) + token[1:]
	if tampered == token {
		t.Fatal("tampering produced an identical token")
	}

	request := jarPageRequest(http.MethodGet, "/orders", jar)
	request.QueryStringParameters = map[string]string{"after": tampered}
	response, err := env.handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle orders tampered cursor returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("tampered cursor status = %d, want 200", response.StatusCode)
	}
	if got := strings.Count(response.Body, `data-testid="order-row"`); got != ordersPageSize {
		t.Fatalf("tampered cursor rows = %d, want first page of %d", got, ordersPageSize)
	}
}

func TestOrdersPageExactPageSizeHasNoNextLink(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	customerID := accountCustomerID(t, env, "shopper@example.com")
	createPagingTestOrders(t, env, customerID, "shopper@example.com", ordersPageSize)

	response, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/orders", jar))
	if err != nil {
		t.Fatalf("Handle orders returned error: %v", err)
	}
	if got := strings.Count(response.Body, `data-testid="order-row"`); got != ordersPageSize {
		t.Fatalf("rows = %d, want %d", got, ordersPageSize)
	}
	if strings.Contains(response.Body, `data-testid="orders-next-page"`) {
		t.Fatal("exactly one full page must not offer an older-orders link")
	}
}

// --- Webhook ---

// fakeSignedWebhookRequest builds a POST /webhooks/stripe event signed the
// way Stripe signs deliveries, against the fake provider's fixed demo secret.
func fakeSignedWebhookRequest(payload []byte, timestamp time.Time) events.APIGatewayV2HTTPRequest {
	mac := hmac.New(sha256.New, []byte(payments.FakeWebhookSigningSecret))
	fmt.Fprintf(mac, "%d.%s", timestamp.Unix(), payload)
	signature := fmt.Sprintf("t=%d,v1=%s", timestamp.Unix(), hex.EncodeToString(mac.Sum(nil)))

	request := pageRequest(http.MethodPost, "/webhooks/stripe")
	request.Body = string(payload)
	request.Headers = map[string]string{
		"content-type":     "application/json",
		"Stripe-Signature": signature,
	}
	return request
}

func checkoutSessionEventPayload(eventID string, eventType string, sessionID string, orderID string, paymentStatus string, amountTotal int) []byte {
	return fmt.Appendf(nil,
		`{"id":%q,"object":"event","type":%q,"data":{"object":{"id":%q,"object":"checkout.session","client_reference_id":%q,"metadata":{"order_id":%q},"mode":"payment","payment_status":%q,"amount_total":%s,"payment_intent":"pi_test_webhook"}}}`,
		eventID, eventType, sessionID, orderID, orderID, paymentStatus, strconv.Itoa(amountTotal))
}

func TestWebhookRejectsInvalidSignature(t *testing.T) {
	env := newAccountTestEnv(t)
	recorder := &testMetricRecorder{}
	env.handler.metrics = recorder

	payload := checkoutSessionEventPayload("evt_bad_sig", "checkout.session.completed", "cs_fake_x", ssrTestAccountID, "paid", 5798)
	request := fakeSignedWebhookRequest(payload, time.Now())
	request.Headers["Stripe-Signature"] = "t=1,v1=deadbeef"

	response, err := env.handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}
	if response.Body != "Invalid signature" {
		t.Fatalf("body = %q, want Invalid signature", response.Body)
	}
	if got := response.Headers["Cache-Control"]; got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	assertRecordedMetric(t, recorder, observability.MetricStripeWebhook, observability.UnitCount, map[string]string{
		"Service": "ssr",
		"Outcome": "invalid_signature",
	})
}

func TestWebhookExpiredEventReleasesStockOnceAndDeduplicates(t *testing.T) {
	env := newAccountTestEnv(t)
	jar, addressID := checkoutTestSetup(t, env, 2)
	orderID, sessionID := placeTestOrder(t, env, jar, addressID)
	if got := mangoStock(t, env); got != 3 {
		t.Fatalf("mango stock = %d, want 3 after reservation", got)
	}

	payload := checkoutSessionEventPayload("evt_expired_1", "checkout.session.expired", sessionID, orderID, "unpaid", 5798)
	response, err := env.handler.Handle(context.Background(), fakeSignedWebhookRequest(payload, time.Now()))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d body %q, want 200", response.StatusCode, response.Body)
	}
	order, _, err := env.commerce.GetOrder(context.Background(), orderID)
	if err != nil {
		t.Fatalf("GetOrder returned error: %v", err)
	}
	if order.Status != commerce.OrderStatusExpired {
		t.Fatalf("order status = %q, want expired", order.Status)
	}
	if got := mangoStock(t, env); got != 5 {
		t.Fatalf("mango stock = %d, want released back to 5", got)
	}

	// A duplicate delivery acknowledges without releasing stock again.
	duplicate, err := env.handler.Handle(context.Background(), fakeSignedWebhookRequest(payload, time.Now()))
	if err != nil {
		t.Fatalf("Handle duplicate returned error: %v", err)
	}
	if duplicate.StatusCode != http.StatusOK {
		t.Fatalf("duplicate status = %d, want 200", duplicate.StatusCode)
	}
	if got := mangoStock(t, env); got != 5 {
		t.Fatalf("mango stock after duplicate = %d, want still 5", got)
	}
}

func TestWebhookCompletedPaidEventFinalizesOrder(t *testing.T) {
	env := newAccountTestEnv(t)
	jar, addressID := checkoutTestSetup(t, env, 2)
	customerID := accountCustomerID(t, env, "shopper@example.com")
	orderID, sessionID := placeTestOrder(t, env, jar, addressID)

	payload := checkoutSessionEventPayload("evt_completed_1", "checkout.session.completed", sessionID, orderID, "paid", 5798)
	response, err := env.handler.Handle(context.Background(), fakeSignedWebhookRequest(payload, time.Now()))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d body %q, want 200", response.StatusCode, response.Body)
	}

	order, _, err := env.commerce.GetOrder(context.Background(), orderID)
	if err != nil {
		t.Fatalf("GetOrder returned error: %v", err)
	}
	if order.Status != commerce.OrderStatusPaid {
		t.Fatalf("order status = %q, want paid", order.Status)
	}
	record, _, err := env.commerce.GetCart(context.Background(), customerID)
	if err != nil {
		t.Fatalf("GetCart returned error: %v", err)
	}
	if len(record.Lines) != 0 || record.PendingOrderID != "" {
		t.Fatalf("cart after webhook finalize = %#v, want cleared", record)
	}
	if got := mangoStock(t, env); got != 3 {
		t.Fatalf("mango stock = %d, want reservation kept for the paid order", got)
	}
}

func TestWebhookNeverDowngradesPaidOrder(t *testing.T) {
	env := newAccountTestEnv(t)
	jar, addressID := checkoutTestSetup(t, env, 2)
	orderID, sessionID := placeTestOrder(t, env, jar, addressID)

	paidPayload := checkoutSessionEventPayload("evt_paid_first", "checkout.session.completed", sessionID, orderID, "paid", 5798)
	if response, err := env.handler.Handle(context.Background(), fakeSignedWebhookRequest(paidPayload, time.Now())); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("paid webhook = %d, err %v, want 200", response.StatusCode, err)
	}

	// A late expired delivery for the same session must not downgrade or
	// release the paid order's stock.
	expiredPayload := checkoutSessionEventPayload("evt_expired_late", "checkout.session.expired", sessionID, orderID, "unpaid", 5798)
	response, err := env.handler.Handle(context.Background(), fakeSignedWebhookRequest(expiredPayload, time.Now()))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("late expired status = %d, want 200", response.StatusCode)
	}
	order, _, err := env.commerce.GetOrder(context.Background(), orderID)
	if err != nil {
		t.Fatalf("GetOrder returned error: %v", err)
	}
	if order.Status != commerce.OrderStatusPaid {
		t.Fatalf("order status = %q, want still paid", order.Status)
	}
	if got := mangoStock(t, env); got != 3 {
		t.Fatalf("mango stock = %d, want reservation kept", got)
	}
}

func TestWebhookUnknownOrderAcknowledged(t *testing.T) {
	env := newAccountTestEnv(t)
	payload := checkoutSessionEventPayload("evt_unknown_order", "checkout.session.completed", "cs_fake_missing", ssrTestAccountID, "paid", 5798)

	response, err := env.handler.Handle(context.Background(), fakeSignedWebhookRequest(payload, time.Now()))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want 200 (a retry cannot fix an unknown order)", response.StatusCode)
	}
}

// failingGetOrderStore injects a transient store outage into the webhook path.
type failingGetOrderStore struct {
	commerce.Store
}

func (failingGetOrderStore) GetOrder(ctx context.Context, orderID string) (commerce.Order, bool, error) {
	return commerce.Order{}, false, fmt.Errorf("dynamodb unavailable")
}

func TestWebhookTransientStoreErrorReturns500(t *testing.T) {
	env := newAccountTestEnv(t)
	jar, addressID := checkoutTestSetup(t, env, 1)
	orderID, sessionID := placeTestOrder(t, env, jar, addressID)
	env.handler.checkout.Commerce = failingGetOrderStore{Store: env.commerce}

	payload := checkoutSessionEventPayload("evt_store_down", "checkout.session.expired", sessionID, orderID, "unpaid", 2899)
	response, err := env.handler.Handle(context.Background(), fakeSignedWebhookRequest(payload, time.Now()))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status code = %d, want 500 so the provider retries", response.StatusCode)
	}
}

func TestWebhook404sWhenPaymentsUnconfigured(t *testing.T) {
	// The default handler has no provider or checkout service wired; the
	// endpoint behaves as absent rather than erroring.
	response, err := NewHandler(cartRouteStore()).Handle(context.Background(), pageRequest(http.MethodPost, "/webhooks/stripe"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status code = %d, want 404", response.StatusCode)
	}
}

// refundEventPayload builds a refund.* event payload the way Stripe delivers
// it: the refund carries metadata.order_id/attempt and a bare payment_intent.
func refundEventPayload(eventID string, eventType string, refundID string, orderID string, attempt int, paymentIntentID string, status string, failureReason string, amountCents int) []byte {
	return fmt.Appendf(nil,
		`{"id":%q,"object":"event","type":%q,"data":{"object":{"id":%q,"object":"refund","status":%q,"failure_reason":%q,"amount":%s,"payment_intent":%q,"metadata":{"order_id":%q,"attempt":%q}}}}`,
		eventID, eventType, refundID, status, failureReason, strconv.Itoa(amountCents), paymentIntentID, orderID, strconv.Itoa(attempt))
}

// refundPendingWebhookOrder finalizes a placed order via the signed webhook
// (persisting payment intent pi_test_webhook) and moves it to refund_pending
// with a recorded refund ID, mirroring an admin refund awaiting settlement.
func refundPendingWebhookOrder(t *testing.T, env accountTestEnv, orderID string, sessionID string) {
	t.Helper()
	payload := checkoutSessionEventPayload("evt_completed_"+orderID, "checkout.session.completed", sessionID, orderID, "paid", 5798)
	response, err := env.handler.Handle(context.Background(), fakeSignedWebhookRequest(payload, time.Now()))
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("finalize webhook = %d %v, want 200", response.StatusCode, err)
	}
	paid, _, err := env.commerce.GetOrder(context.Background(), orderID)
	if err != nil || paid.Status != commerce.OrderStatusPaid {
		t.Fatalf("order = %s %v, want paid", paid.Status, err)
	}
	refundID := "re_test_" + orderID
	attempt := 1
	if _, err := env.commerce.TransitionOrder(context.Background(), orderID, commerce.OrderStatusPaid, commerce.OrderStatusRefundPending, commerce.OrderPatch{
		Actor:          commerce.OrderActorAdmin,
		StripeRefundID: &refundID,
		RefundAttempt:  &attempt,
	}); err != nil {
		t.Fatalf("TransitionOrder to refund_pending returned error: %v", err)
	}
}

func TestWebhookRefundUpdatedSettlesRefundPendingOrder(t *testing.T) {
	env := newAccountTestEnv(t)
	jar, addressID := checkoutTestSetup(t, env, 2)
	orderID, sessionID := placeTestOrder(t, env, jar, addressID)
	refundPendingWebhookOrder(t, env, orderID, sessionID)
	if got := mangoStock(t, env); got != 3 {
		t.Fatalf("mango stock = %d, want the reservation still held before settlement", got)
	}

	payload := refundEventPayload("evt_refund_settle_1", "refund.updated", "re_test_"+orderID, orderID, 1, "pi_test_webhook", "succeeded", "", 5798)
	response, err := env.handler.Handle(context.Background(), fakeSignedWebhookRequest(payload, time.Now()))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d body %q, want 200", response.StatusCode, response.Body)
	}
	order, _, err := env.commerce.GetOrder(context.Background(), orderID)
	if err != nil {
		t.Fatalf("GetOrder returned error: %v", err)
	}
	if order.Status != commerce.OrderStatusRefunded || order.RefundedAt.IsZero() {
		t.Fatalf("order = %s refundedAt %v, want refunded", order.Status, order.RefundedAt)
	}
	if got := mangoStock(t, env); got != 5 {
		t.Fatalf("mango stock = %d, want released back to 5 for the unshipped refund", got)
	}

	// The shopper's order page renders the refund status straight from the
	// label map: list row and detail chip.
	detail, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/orders/"+orderID, jar))
	if err != nil {
		t.Fatalf("Handle order detail returned error: %v", err)
	}
	assertBodyContains(t, detail.Body, []string{`data-testid="order-status"`, "Refunded"})
	list, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/orders", jar))
	if err != nil {
		t.Fatalf("Handle orders list returned error: %v", err)
	}
	assertBodyContains(t, list.Body, []string{`data-testid="order-row"`, "Refunded"})
}

func TestWebhookRefundUpdatedForeignPaymentIntentIsRejected(t *testing.T) {
	env := newAccountTestEnv(t)
	jar, addressID := checkoutTestSetup(t, env, 2)
	orderID, sessionID := placeTestOrder(t, env, jar, addressID)
	refundPendingWebhookOrder(t, env, orderID, sessionID)
	recorder := &testMetricRecorder{}
	env.handler.metrics = recorder
	// The checkout service captured the recorder at handler construction;
	// point it at the test recorder so webhook outcomes are observable.
	env.handler.checkout.Metrics = recorder

	// metadata.order_id routes the event here, but the payment intent does
	// not match the order: ACK without touching it (alarmed refund_mismatch).
	payload := refundEventPayload("evt_refund_foreign_1", "refund.updated", "re_test_"+orderID, orderID, 1, "pi_someone_else", "succeeded", "", 5798)
	response, err := env.handler.Handle(context.Background(), fakeSignedWebhookRequest(payload, time.Now()))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d body %q, want 200 ACK", response.StatusCode, response.Body)
	}
	order, _, err := env.commerce.GetOrder(context.Background(), orderID)
	if err != nil {
		t.Fatalf("GetOrder returned error: %v", err)
	}
	if order.Status != commerce.OrderStatusRefundPending {
		t.Fatalf("order status = %q, want still refund_pending", order.Status)
	}
	if got := mangoStock(t, env); got != 3 {
		t.Fatalf("mango stock = %d, want the reservation untouched", got)
	}
	assertRecordedMetric(t, recorder, observability.MetricStripeWebhook, observability.UnitCount, map[string]string{
		"Service": "ssr",
		"Outcome": "refund_mismatch",
	})
}

func TestOrderStatusLabelsCoverRefundFamily(t *testing.T) {
	tests := []struct {
		status commerce.OrderStatus
		want   string
	}{
		{commerce.OrderStatusRefundPending, "Refund pending"},
		{commerce.OrderStatusRefunded, "Refunded"},
		{commerce.OrderStatusRefundFailed, "Refund delayed"},
	}
	for _, test := range tests {
		if got := orderStatusLabel(test.status); got != test.want {
			t.Errorf("orderStatusLabel(%s) = %q, want %q", test.status, got, test.want)
		}
	}
}

// --- Guest checkout flow ---

const guestCheckoutEmail = "guest@example.com"

// guestCheckoutForm builds the guest place-order POST body: guest CSRF token,
// contact email, and the inline US shipping address.
func guestCheckoutForm(token string, email string) url.Values {
	return url.Values{
		guestCSRFFieldName: {token},
		"email":            {email},
		"full_name":        {"Guest Shopper"},
		"line1":            {"99 Charoen Krung Rd"},
		"city":             {"Austin"},
		"region":           {"TX"},
		"postal_code":      {"78701"},
	}
}

func guestCheckoutOrderAddress() commerce.OrderAddress {
	return commerce.OrderAddress{
		FullName:   "Guest Shopper",
		Line1:      "99 Charoen Krung Rd",
		City:       "Austin",
		Region:     "TX",
		PostalCode: "78701",
		Country:    "US",
	}
}

// guestCartJar returns an anonymous jar holding a cookie cart with quantity
// mango units (no account, no server cart).
func guestCartJar(t *testing.T, env accountTestEnv, quantity int) testCookieJar {
	t.Helper()
	jar := testCookieJar{}
	addToTestCart(t, env.handler, jar, "mango-sticky-rice-kit", quantity)
	return jar
}

// placeGuestTestOrder POSTs the guest place-order form and returns the order
// id and fake session id from the redirect; the jar gains the pointer cookie.
func placeGuestTestOrder(t *testing.T, env accountTestEnv, jar testCookieJar, email string) (string, string) {
	t.Helper()
	token := guestCSRFTokenFor(t, env.handler, jar, "/checkout")
	response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/place-order", guestCheckoutForm(token, email), jar))
	if err != nil {
		t.Fatalf("Handle guest place-order returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("guest place-order status = %d body %q, want 303", response.StatusCode, response.Body)
	}
	jar.update(t, response)
	location := response.Headers["Location"]
	marker := "/checkout/fake-pay?session_id="
	index := strings.Index(location, marker)
	if index < 0 {
		t.Fatalf("guest place-order Location = %q, want fake-pay redirect", location)
	}
	sessionID := location[index+len(marker):]
	orderID := strings.TrimPrefix(sessionID, "cs_fake_")
	if orderID == sessionID || !accountIDPattern.MatchString(orderID) {
		t.Fatalf("could not parse order id from session %q", sessionID)
	}
	return orderID, sessionID
}

func allOrdersCount(t *testing.T, env accountTestEnv) int {
	t.Helper()
	page, err := env.commerce.ListOrders(context.Background(), 0, commerce.OrderCursor{})
	if err != nil {
		t.Fatalf("ListOrders returned error: %v", err)
	}
	return len(page.Orders)
}

// finalizeGuestOrderOffSite marks the session paid and finalizes through the
// service, the way the webhook does when the shopper never reaches confirm.
func finalizeGuestOrderOffSite(t *testing.T, env accountTestEnv, orderID string, sessionID string) {
	t.Helper()
	if _, err := env.payments.MarkSessionPaid(sessionID, false); err != nil {
		t.Fatalf("MarkSessionPaid returned error: %v", err)
	}
	session, err := env.payments.GetSession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if _, err := env.handler.checkout.FinalizePayment(context.Background(), orderID, session); err != nil {
		t.Fatalf("FinalizePayment returned error: %v", err)
	}
}

func TestGuestCheckoutPageRendersEmailAndAddressForm(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := guestCartJar(t, env, 2)

	response, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/checkout", jar))
	if err != nil {
		t.Fatalf("Handle checkout returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 guest checkout", response.StatusCode)
	}
	if raw := rawSetCookie(t, response, commerce.GuestCSRFCookieName); !strings.Contains(raw, "HttpOnly") {
		t.Fatalf("guest csrf cookie = %q, want HttpOnly set-cookie", raw)
	}
	assertBodyContains(t, response.Body, []string{
		`data-testid="guest-checkout-form"`,
		`action="/checkout/place-order"`,
		`name="` + guestCSRFFieldName + `"`,
		`data-testid="guest-email-input"`,
		`data-testid="checkout-sign-in-link"`,
		`href="/account/sign-in?return_to=%2Fcheckout"`,
		`name="line1"`,
		`data-testid="place-order-button"`,
	})

	// Empty cart still bounces to /cart before any guest render.
	emptyResponse, err := env.handler.Handle(context.Background(), pageRequest(http.MethodGet, "/checkout"))
	if err != nil {
		t.Fatalf("Handle empty checkout returned error: %v", err)
	}
	if emptyResponse.StatusCode != http.StatusSeeOther || emptyResponse.Headers["Location"] != "/cart" {
		t.Fatalf("empty-cart checkout = %d %q, want 303 /cart", emptyResponse.StatusCode, emptyResponse.Headers["Location"])
	}

	// HEAD renders headers only.
	headResponse, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodHead, "/checkout", jar))
	if err != nil {
		t.Fatalf("Handle HEAD checkout returned error: %v", err)
	}
	if headResponse.StatusCode != http.StatusOK || headResponse.Body != "" {
		t.Fatalf("HEAD checkout = %d body %q, want 200 with empty body", headResponse.StatusCode, headResponse.Body)
	}
}

func TestGuestCheckoutPageFailsClosedOnSessionStoreError(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	addToTestCart(t, env.handler, jar, "mango-sticky-rice-kit", 1)
	env.handler.commerce = failingGetSessionStore{Store: env.commerce}
	env.handler.checkout.Commerce = failingGetSessionStore{Store: env.commerce}

	// The shopper presents a session cookie that fails on a transient store
	// error: never degrade them to the guest form against the cookie mirror.
	response, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/checkout", jar))
	if err != nil {
		t.Fatalf("Handle checkout returned error: %v", err)
	}
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("checkout during session outage = %d, want 500", response.StatusCode)
	}
	assertBodyOmits(t, response.Body, []string{`data-testid="guest-checkout-form"`})

	// The place-order POST fails closed identically.
	postResponse, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/place-order", guestCheckoutForm("x", guestCheckoutEmail), jar))
	if err != nil {
		t.Fatalf("Handle place-order returned error: %v", err)
	}
	if postResponse.StatusCode != http.StatusInternalServerError {
		t.Fatalf("place-order during session outage = %d, want 500", postResponse.StatusCode)
	}
}

func TestGuestPlaceOrderHappyPath(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := guestCartJar(t, env, 2)
	token := guestCSRFTokenFor(t, env.handler, jar, "/checkout")

	response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/place-order", guestCheckoutForm(token, guestCheckoutEmail), jar))
	if err != nil {
		t.Fatalf("Handle guest place-order returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d body %q, want 303", response.StatusCode, response.Body)
	}
	location := response.Headers["Location"]
	if !strings.Contains(location, "/checkout/fake-pay?session_id=cs_fake_") {
		t.Fatalf("Location = %q, want fake-pay redirect", location)
	}
	sessionID := location[strings.Index(location, "cs_fake_"):]
	orderID := strings.TrimPrefix(sessionID, "cs_fake_")

	order, found, err := env.commerce.GetOrder(context.Background(), orderID)
	if err != nil || !found {
		t.Fatalf("GetOrder = found %t, err %v", found, err)
	}
	if order.CustomerID != "" {
		t.Fatalf("order customer id = %q, want empty guest marker", order.CustomerID)
	}
	if order.Email != guestCheckoutEmail {
		t.Fatalf("order email = %q, want %q", order.Email, guestCheckoutEmail)
	}
	if order.Status != commerce.OrderStatusPendingPayment || order.CheckoutAttempt != 1 {
		t.Fatalf("order = status %q attempt %d, want pending_payment attempt 1", order.Status, order.CheckoutAttempt)
	}
	if len(order.Lines) != 1 || order.Lines[0].Slug != "mango-sticky-rice-kit" || order.Lines[0].Quantity != 2 || order.Lines[0].UnitPriceCents != 2899 {
		t.Fatalf("order lines = %#v, want frozen mango snapshot", order.Lines)
	}
	if order.TotalCents != 5798 || order.SubtotalCents != 5798 {
		t.Fatalf("order totals = %d/%d, want 5798", order.SubtotalCents, order.TotalCents)
	}
	if order.ShippingAddress != guestCheckoutOrderAddress() {
		t.Fatalf("order address = %#v, want the entered address with country US", order.ShippingAddress)
	}
	wantFingerprint := env.handler.checkout.Fingerprint(order.Lines, checkout.GuestAddressID(guestCheckoutOrderAddress(), guestCheckoutEmail))
	if order.CartFingerprint != wantFingerprint {
		t.Fatalf("order fingerprint = %q, want %q", order.CartFingerprint, wantFingerprint)
	}

	// Stock reserved once.
	if got := mangoStock(t, env); got != 3 {
		t.Fatalf("mango stock = %d, want 3", got)
	}

	// The response sets the signed pointer cookie and leaves tgs_cart alone
	// (the cart survives until payment).
	pointer := rawSetCookie(t, response, commerce.GuestOrderCookieName)
	for _, want := range []string{"Max-Age=3600", "HttpOnly", "Secure", "SameSite=Lax"} {
		if !strings.Contains(pointer, want) {
			t.Fatalf("pointer cookie = %q, missing %q", pointer, want)
		}
	}
	for _, setCookie := range response.Cookies {
		if strings.HasPrefix(setCookie, cart.CookieName+"=") {
			t.Fatalf("place-order touched the cart cookie: %q", setCookie)
		}
	}
}

func TestGuestPlaceOrderCSRFAndValidation(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := guestCartJar(t, env, 1)
	token := guestCSRFTokenFor(t, env.handler, jar, "/checkout")

	t.Run("missing guest csrf", func(t *testing.T) {
		form := guestCheckoutForm("", guestCheckoutEmail)
		form.Del(guestCSRFFieldName)
		response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/place-order", form, jar))
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", response.StatusCode)
		}
		assertBodyContains(t, response.Body, []string{expiredFormError, `data-testid="guest-checkout-form"`, `value="` + guestCheckoutEmail + `"`})
	})

	t.Run("mismatched guest csrf", func(t *testing.T) {
		response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/place-order", guestCheckoutForm(token+"x", guestCheckoutEmail), jar))
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", response.StatusCode)
		}
	})

	t.Run("invalid email", func(t *testing.T) {
		response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/place-order", guestCheckoutForm(token, "not-an-email"), jar))
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", response.StatusCode)
		}
		assertBodyContains(t, response.Body, []string{invalidEmailError, `value="not-an-email"`, `value="Guest Shopper"`})
	})

	t.Run("missing address fields", func(t *testing.T) {
		form := guestCheckoutForm(token, guestCheckoutEmail)
		form.Del("line1")
		response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/place-order", form, jar))
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", response.StatusCode)
		}
		assertBodyContains(t, response.Body, []string{invalidAddressError, `value="` + guestCheckoutEmail + `"`, `value="Austin"`})
	})

	t.Run("empty cart bounces to /cart", func(t *testing.T) {
		emptyJar := testCookieJar{}
		response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/place-order", guestCheckoutForm(token, guestCheckoutEmail), emptyJar))
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusSeeOther || response.Headers["Location"] != "/cart" {
			t.Fatalf("response = %d %q, want 303 /cart", response.StatusCode, response.Headers["Location"])
		}
	})

	if got := mangoStock(t, env); got != 5 {
		t.Fatalf("mango stock = %d, want untouched 5", got)
	}
	if count := allOrdersCount(t, env); count != 0 {
		t.Fatalf("order count = %d, want 0", count)
	}
}

func TestGuestPlaceOrderInsufficientStockRerendersWithReclampedCart(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := guestCartJar(t, env, 3)
	token := guestCSRFTokenFor(t, env.handler, jar, "/checkout")

	// A concurrent buyer takes 4 of the 5 units, and the reservation loses.
	if err := env.catalog.AdjustStock(context.Background(), []catalog.StockAdjustment{{ProductID: "prod_account_mango", Delta: -4}}); err != nil {
		t.Fatalf("AdjustStock returned error: %v", err)
	}
	env.handler.checkout.Stock = failingStockStore{err: catalog.InsufficientStockError{
		ProductID: "prod_account_mango",
		Slug:      "mango-sticky-rice-kit",
		Available: 1,
	}}

	response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/place-order", guestCheckoutForm(token, guestCheckoutEmail), jar))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d body %q, want 409", response.StatusCode, response.Body)
	}
	assertBodyContains(t, response.Body, []string{
		checkoutStockChangedError,
		`data-testid="checkout-line-notice"`,
		`Only 1 left of Mango Sticky Rice Treats — quantities updated.`,
		`Quantity 1`,
		`data-testid="guest-checkout-form"`,
		`value="` + guestCheckoutEmail + `"`,
	})

	// The re-clamped anonymous cart was rewritten into the cookie.
	repaired := rawSetCookie(t, response, cart.CookieName)
	if strings.Contains(repaired, "Max-Age=0") {
		t.Fatalf("cart cookie = %q, want a repaired (not cleared) cookie", repaired)
	}
}

func TestGuestPlaceOrderResumePointer(t *testing.T) {
	t.Run("same cart address and email resumes the session", func(t *testing.T) {
		env := newAccountTestEnv(t)
		jar := guestCartJar(t, env, 2)
		_, firstSession := placeGuestTestOrder(t, env, jar, guestCheckoutEmail)

		token := guestCSRFTokenFor(t, env.handler, jar, "/checkout")
		response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/place-order", guestCheckoutForm(token, guestCheckoutEmail), jar))
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusSeeOther || !strings.Contains(response.Headers["Location"], firstSession) {
			t.Fatalf("re-entry = %d %q, want 303 to the original session %q", response.StatusCode, response.Headers["Location"], firstSession)
		}
		if count := allOrdersCount(t, env); count != 1 {
			t.Fatalf("order count = %d, want 1", count)
		}
		if got := mangoStock(t, env); got != 3 {
			t.Fatalf("mango stock = %d, want 3 (single reservation)", got)
		}
	})

	t.Run("email-only change cancels the stale order", func(t *testing.T) {
		env := newAccountTestEnv(t)
		jar := guestCartJar(t, env, 2)
		firstOrder, firstSession := placeGuestTestOrder(t, env, jar, guestCheckoutEmail)

		token := guestCSRFTokenFor(t, env.handler, jar, "/checkout")
		response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/place-order", guestCheckoutForm(token, "corrected@example.com"), jar))
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusSeeOther {
			t.Fatalf("status = %d, want 303", response.StatusCode)
		}
		if strings.Contains(response.Headers["Location"], firstSession) {
			t.Fatalf("Location = %q, want a fresh session after the email change", response.Headers["Location"])
		}
		stale, _, err := env.commerce.GetOrder(context.Background(), firstOrder)
		if err != nil {
			t.Fatalf("GetOrder returned error: %v", err)
		}
		if stale.Status != commerce.OrderStatusCanceled {
			t.Fatalf("stale order status = %q, want canceled", stale.Status)
		}
		if count := allOrdersCount(t, env); count != 2 {
			t.Fatalf("order count = %d, want 2", count)
		}
		// Old reservation released, fresh one held.
		if got := mangoStock(t, env); got != 3 {
			t.Fatalf("mango stock = %d, want 3", got)
		}
		page, err := env.commerce.ListOrders(context.Background(), 0, commerce.OrderCursor{})
		if err != nil {
			t.Fatalf("ListOrders returned error: %v", err)
		}
		fresh := page.Orders[0]
		if fresh.ID == firstOrder {
			fresh = page.Orders[1]
		}
		if fresh.Email != "corrected@example.com" {
			t.Fatalf("fresh order email = %q, want the corrected email", fresh.Email)
		}
	})

	t.Run("cart change cancels the stale order", func(t *testing.T) {
		env := newAccountTestEnv(t)
		jar := guestCartJar(t, env, 2)
		firstOrder, _ := placeGuestTestOrder(t, env, jar, guestCheckoutEmail)

		addToTestCart(t, env.handler, jar, "mango-sticky-rice-kit", 1)
		token := guestCSRFTokenFor(t, env.handler, jar, "/checkout")
		response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/place-order", guestCheckoutForm(token, guestCheckoutEmail), jar))
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusSeeOther {
			t.Fatalf("status = %d body %q, want 303", response.StatusCode, response.Body)
		}
		stale, _, err := env.commerce.GetOrder(context.Background(), firstOrder)
		if err != nil {
			t.Fatalf("GetOrder returned error: %v", err)
		}
		if stale.Status != commerce.OrderStatusCanceled {
			t.Fatalf("stale order status = %q, want canceled", stale.Status)
		}
		// 2 released, 3 reserved.
		if got := mangoStock(t, env); got != 2 {
			t.Fatalf("mango stock = %d, want 2", got)
		}
	})
}

func TestGuestPlaceOrderPaidPointerRedirectsToConfirm(t *testing.T) {
	t.Run("identical resubmit routes to confirm and clears cookies", func(t *testing.T) {
		env := newAccountTestEnv(t)
		jar := guestCartJar(t, env, 2)
		orderID, sessionID := placeGuestTestOrder(t, env, jar, guestCheckoutEmail)
		finalizeGuestOrderOffSite(t, env, orderID, sessionID)

		token := guestCSRFTokenFor(t, env.handler, jar, "/checkout")
		response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/place-order", guestCheckoutForm(token, guestCheckoutEmail), jar))
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusSeeOther {
			t.Fatalf("status = %d body %q, want 303", response.StatusCode, response.Body)
		}
		if got := response.Headers["Location"]; got != "/checkout/confirm?session_id="+sessionID {
			t.Fatalf("Location = %q, want the paid order's confirm URL", got)
		}
		if count := allOrdersCount(t, env); count != 1 {
			t.Fatalf("order count = %d, want 1 (no duplicate order)", count)
		}
		if got := mangoStock(t, env); got != 3 {
			t.Fatalf("mango stock = %d, want 3 (no stock movement)", got)
		}

		// Following the redirect finalize-replays and clears cart + pointer.
		confirmRequest := jarPageRequest(http.MethodGet, "/checkout/confirm", jar)
		confirmRequest.QueryStringParameters = map[string]string{"session_id": sessionID}
		confirmResponse, err := env.handler.Handle(context.Background(), confirmRequest)
		if err != nil {
			t.Fatalf("Handle confirm returned error: %v", err)
		}
		if confirmResponse.StatusCode != http.StatusSeeOther {
			t.Fatalf("confirm status = %d, want 303", confirmResponse.StatusCode)
		}
		if !strings.HasPrefix(confirmResponse.Headers["Location"], "/orders/"+orderID+"?access=") {
			t.Fatalf("confirm Location = %q, want the tokenized order URL", confirmResponse.Headers["Location"])
		}
		if raw := rawSetCookie(t, confirmResponse, cart.CookieName); !strings.Contains(raw, "Max-Age=0") {
			t.Fatalf("cart cookie = %q, want clearing", raw)
		}
		if raw := rawSetCookie(t, confirmResponse, commerce.GuestOrderCookieName); !strings.Contains(raw, "Max-Age=0") {
			t.Fatalf("pointer cookie = %q, want clearing", raw)
		}
	})

	t.Run("changed email after paying places a fresh order", func(t *testing.T) {
		env := newAccountTestEnv(t)
		jar := guestCartJar(t, env, 2)
		orderID, sessionID := placeGuestTestOrder(t, env, jar, guestCheckoutEmail)
		finalizeGuestOrderOffSite(t, env, orderID, sessionID)

		token := guestCSRFTokenFor(t, env.handler, jar, "/checkout")
		response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/place-order", guestCheckoutForm(token, "corrected@example.com"), jar))
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusSeeOther || !strings.Contains(response.Headers["Location"], "/checkout/fake-pay?session_id=") {
			t.Fatalf("response = %d %q, want a fresh fake-pay redirect", response.StatusCode, response.Headers["Location"])
		}
		if strings.Contains(response.Headers["Location"], sessionID) {
			t.Fatalf("Location = %q, want a session other than the paid one", response.Headers["Location"])
		}
		if count := allOrdersCount(t, env); count != 2 {
			t.Fatalf("order count = %d, want 2 (new intent, new order)", count)
		}
		if got, _, _ := env.commerce.GetOrder(context.Background(), orderID); got.Status != commerce.OrderStatusPaid {
			t.Fatalf("paid order status = %q, want untouched paid", got.Status)
		}
	})

	t.Run("absent pointer places a duplicate order (accepted residual)", func(t *testing.T) {
		env := newAccountTestEnv(t)
		jar := guestCartJar(t, env, 2)
		orderID, sessionID := placeGuestTestOrder(t, env, jar, guestCheckoutEmail)
		finalizeGuestOrderOffSite(t, env, orderID, sessionID)
		delete(jar, commerce.GuestOrderCookieName)

		token := guestCSRFTokenFor(t, env.handler, jar, "/checkout")
		response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/place-order", guestCheckoutForm(token, guestCheckoutEmail), jar))
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusSeeOther || !strings.Contains(response.Headers["Location"], "/checkout/fake-pay?session_id=") {
			t.Fatalf("response = %d %q, want a fresh fake-pay redirect", response.StatusCode, response.Headers["Location"])
		}
		if count := allOrdersCount(t, env); count != 2 {
			t.Fatalf("order count = %d, want 2 (the documented duplicate)", count)
		}
	})

	t.Run("refund-family pointer routes to confirm", func(t *testing.T) {
		env := newAccountTestEnv(t)
		jar := guestCartJar(t, env, 2)
		orderID, sessionID := placeGuestTestOrder(t, env, jar, guestCheckoutEmail)
		finalizeGuestOrderOffSite(t, env, orderID, sessionID)
		refundID := "re_fake_guest"
		attempt := 1
		if _, err := env.commerce.TransitionOrder(context.Background(), orderID, commerce.OrderStatusPaid, commerce.OrderStatusRefundPending, commerce.OrderPatch{Actor: commerce.OrderActorAdmin, StripeRefundID: &refundID, RefundAttempt: &attempt}); err != nil {
			t.Fatalf("TransitionOrder returned error: %v", err)
		}

		stockBefore := mangoStock(t, env)
		token := guestCSRFTokenFor(t, env.handler, jar, "/checkout")
		response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/place-order", guestCheckoutForm(token, guestCheckoutEmail), jar))
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusSeeOther || response.Headers["Location"] != "/checkout/confirm?session_id="+sessionID {
			t.Fatalf("response = %d %q, want 303 to confirm (never a re-charge)", response.StatusCode, response.Headers["Location"])
		}
		if count := allOrdersCount(t, env); count != 1 {
			t.Fatalf("order count = %d, want 1", count)
		}
		if got := mangoStock(t, env); got != stockBefore {
			t.Fatalf("mango stock = %d, want unchanged %d", got, stockBefore)
		}

		confirmRequest := jarPageRequest(http.MethodGet, "/checkout/confirm", jar)
		confirmRequest.QueryStringParameters = map[string]string{"session_id": sessionID}
		confirmResponse, err := env.handler.Handle(context.Background(), confirmRequest)
		if err != nil {
			t.Fatalf("Handle confirm returned error: %v", err)
		}
		if confirmResponse.StatusCode != http.StatusSeeOther || !strings.HasPrefix(confirmResponse.Headers["Location"], "/orders/"+orderID+"?access=") {
			t.Fatalf("confirm = %d %q, want the tokenized order URL", confirmResponse.StatusCode, confirmResponse.Headers["Location"])
		}
		if raw := rawSetCookie(t, confirmResponse, cart.CookieName); !strings.Contains(raw, "Max-Age=0") {
			t.Fatalf("cart cookie = %q, want clearing", raw)
		}
		if raw := rawSetCookie(t, confirmResponse, commerce.GuestOrderCookieName); !strings.Contains(raw, "Max-Age=0") {
			t.Fatalf("pointer cookie = %q, want clearing", raw)
		}

		// The order page shows the honest refund status.
		jar.update(t, confirmResponse)
		location, err := url.Parse(confirmResponse.Headers["Location"])
		if err != nil {
			t.Fatalf("confirm Location does not parse: %v", err)
		}
		detailRequest := jarPageRequest(http.MethodGet, location.Path, jar)
		detailRequest.QueryStringParameters = map[string]string{"access": location.Query().Get("access"), "placed": "1"}
		detailResponse, err := env.handler.Handle(context.Background(), detailRequest)
		if err != nil {
			t.Fatalf("Handle order detail returned error: %v", err)
		}
		if detailResponse.StatusCode != http.StatusOK {
			t.Fatalf("order detail status = %d, want 200", detailResponse.StatusCode)
		}
		assertBodyContains(t, detailResponse.Body, []string{`data-testid="order-status"`, `Refund pending`})
	})
}

func TestGuestFakePayThroughConfirmPlacesPaidOrder(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := guestCartJar(t, env, 2)
	orderID, sessionID := placeGuestTestOrder(t, env, jar, guestCheckoutEmail)

	// The guest fake-pay page posts guest_csrf_token and never offers the
	// save-card checkbox.
	payPageRequest := jarPageRequest(http.MethodGet, "/checkout/fake-pay", jar)
	payPageRequest.QueryStringParameters = map[string]string{"session_id": sessionID}
	payPage, err := env.handler.Handle(context.Background(), payPageRequest)
	if err != nil {
		t.Fatalf("Handle fake-pay returned error: %v", err)
	}
	if payPage.StatusCode != http.StatusOK {
		t.Fatalf("fake-pay status = %d body %q, want 200", payPage.StatusCode, payPage.Body)
	}
	jar.update(t, payPage)
	assertBodyContains(t, payPage.Body, []string{
		`Demo payment — no real charge`,
		`data-testid="fake-pay-button"`,
		`Pay $57.98`,
		`name="` + guestCSRFFieldName + `"`,
		`data-testid="fake-pay-cancel"`,
	})
	assertBodyOmits(t, payPage.Body, []string{`name="save_card"`, `name="` + customerCSRFFieldName + `"`})

	csrfToken := hiddenInputValue(t, payPage.Body, guestCSRFFieldName)
	payResponse, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/fake-pay", url.Values{
		guestCSRFFieldName: {csrfToken},
		"session_id":       {sessionID},
		"action":           {"pay"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle fake-pay POST returned error: %v", err)
	}
	if payResponse.StatusCode != http.StatusSeeOther {
		t.Fatalf("fake-pay POST status = %d, want 303", payResponse.StatusCode)
	}
	if got := payResponse.Headers["Location"]; got != "http://127.0.0.1:8080/checkout/confirm?session_id="+sessionID {
		t.Fatalf("fake-pay Location = %q, want the confirm URL", got)
	}

	// Confirm finalizes, mints the access link, and clears cart + pointer.
	confirmRequest := jarPageRequest(http.MethodGet, "/checkout/confirm", jar)
	confirmRequest.QueryStringParameters = map[string]string{"session_id": sessionID}
	confirmResponse, err := env.handler.Handle(context.Background(), confirmRequest)
	if err != nil {
		t.Fatalf("Handle confirm returned error: %v", err)
	}
	if confirmResponse.StatusCode != http.StatusSeeOther {
		t.Fatalf("confirm status = %d body %q, want 303", confirmResponse.StatusCode, confirmResponse.Body)
	}
	location, err := url.Parse(confirmResponse.Headers["Location"])
	if err != nil {
		t.Fatalf("confirm Location does not parse: %v", err)
	}
	if location.Path != "/orders/"+orderID || location.Query().Get("placed") != "1" || location.Query().Get("access") == "" {
		t.Fatalf("confirm Location = %q, want /orders/{id}?access=…&placed=1", confirmResponse.Headers["Location"])
	}
	if raw := rawSetCookie(t, confirmResponse, cart.CookieName); !strings.Contains(raw, "Max-Age=0") {
		t.Fatalf("cart cookie = %q, want clearing", raw)
	}
	if raw := rawSetCookie(t, confirmResponse, commerce.GuestOrderCookieName); !strings.Contains(raw, "Max-Age=0") {
		t.Fatalf("pointer cookie = %q, want clearing", raw)
	}

	order, _, err := env.commerce.GetOrder(context.Background(), orderID)
	if err != nil {
		t.Fatalf("GetOrder returned error: %v", err)
	}
	if order.Status != commerce.OrderStatusPaid || order.CustomerID != "" {
		t.Fatalf("order = status %q customer %q, want paid guest order", order.Status, order.CustomerID)
	}

	// The tokenized URL works from a cookie-less browser.
	detailRequest := pageRequest(http.MethodGet, location.Path)
	detailRequest.QueryStringParameters = map[string]string{"access": location.Query().Get("access"), "placed": "1"}
	detailResponse, err := env.handler.Handle(context.Background(), detailRequest)
	if err != nil {
		t.Fatalf("Handle order detail returned error: %v", err)
	}
	if detailResponse.StatusCode != http.StatusOK {
		t.Fatalf("order detail status = %d, want 200", detailResponse.StatusCode)
	}
	assertBodyContains(t, detailResponse.Body, []string{
		`data-testid="order-status"`,
		`>Paid</span>`,
		`data-testid="order-placed-banner"`,
		`data-testid="guest-order-link-notice"`,
		`data-testid="guest-signup-upsell"`,
	})
	assertBodyOmits(t, detailResponse.Body, []string{`Back to orders`})
}

func TestGuestFakePayOwnership(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := guestCartJar(t, env, 1)
	orderID, sessionID := placeGuestTestOrder(t, env, jar, guestCheckoutEmail)

	t.Run("no pointer cookie", func(t *testing.T) {
		request := pageRequest(http.MethodGet, "/checkout/fake-pay")
		request.QueryStringParameters = map[string]string{"session_id": sessionID}
		response, err := env.handler.Handle(context.Background(), request)
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", response.StatusCode)
		}
	})

	t.Run("pointer naming a different order", func(t *testing.T) {
		foreignCookie, err := env.handler.mintGuestOrderPointerCookie(ssrTestAccountID, "fp")
		if err != nil {
			t.Fatalf("mintGuestOrderPointerCookie returned error: %v", err)
		}
		value := strings.TrimPrefix(strings.SplitN(foreignCookie, ";", 2)[0], commerce.GuestOrderCookieName+"=")
		request := pageRequest(http.MethodGet, "/checkout/fake-pay")
		request.Cookies = []string{commerce.GuestOrderCookieName + "=" + value}
		request.QueryStringParameters = map[string]string{"session_id": sessionID}
		response, err := env.handler.Handle(context.Background(), request)
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", response.StatusCode)
		}
	})

	t.Run("setup-mode session", func(t *testing.T) {
		setupSession, err := env.payments.CreateSetupSession(context.Background(), payments.SetupSessionInput{
			CustomerID:       "cust000000000000000000000x",
			StripeCustomerID: "cus_fake_x",
			SuccessURL:       "http://127.0.0.1:8080/account/payment-methods?saved=1",
			CancelURL:        "http://127.0.0.1:8080/account/payment-methods",
		})
		if err != nil {
			t.Fatalf("CreateSetupSession returned error: %v", err)
		}
		request := jarPageRequest(http.MethodGet, "/checkout/fake-pay", jar)
		request.QueryStringParameters = map[string]string{"session_id": setupSession.ID}
		response, err := env.handler.Handle(context.Background(), request)
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (setup sessions are signed-in only)", response.StatusCode)
		}
	})

	t.Run("POST without guest csrf", func(t *testing.T) {
		response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/checkout/fake-pay", url.Values{
			"session_id": {sessionID},
			"action":     {"pay"},
		}, jar))
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", response.StatusCode)
		}
		if got, _, _ := env.commerce.GetOrder(context.Background(), orderID); got.Status != commerce.OrderStatusPendingPayment {
			t.Fatalf("order status = %q, want still pending_payment", got.Status)
		}
	})
}

func TestGuestConfirmAuthorization(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := guestCartJar(t, env, 1)
	guestOrderID, guestSessionID := placeGuestTestOrder(t, env, jar, guestCheckoutEmail)
	if _, err := env.payments.MarkSessionPaid(guestSessionID, false); err != nil {
		t.Fatalf("MarkSessionPaid returned error: %v", err)
	}

	t.Run("anonymous with the session id finalizes", func(t *testing.T) {
		request := pageRequest(http.MethodGet, "/checkout/confirm")
		request.QueryStringParameters = map[string]string{"session_id": guestSessionID}
		response, err := env.handler.Handle(context.Background(), request)
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusSeeOther || !strings.HasPrefix(response.Headers["Location"], "/orders/"+guestOrderID+"?access=") {
			t.Fatalf("response = %d %q, want the tokenized order redirect", response.StatusCode, response.Headers["Location"])
		}
		if got, _, _ := env.commerce.GetOrder(context.Background(), guestOrderID); got.Status != commerce.OrderStatusPaid {
			t.Fatalf("order status = %q, want paid", got.Status)
		}
	})

	t.Run("signed-in third party with the session id is allowed (bearer semantics)", func(t *testing.T) {
		intruderJar := testCookieJar{}
		signUpTestCustomer(t, env.handler, intruderJar, "intruder@example.com", "orchid-market-99")
		request := jarPageRequest(http.MethodGet, "/checkout/confirm", intruderJar)
		request.QueryStringParameters = map[string]string{"session_id": guestSessionID}
		response, err := env.handler.Handle(context.Background(), request)
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusSeeOther || !strings.HasPrefix(response.Headers["Location"], "/orders/"+guestOrderID+"?access=") {
			t.Fatalf("response = %d %q, want the bearer redirect", response.StatusCode, response.Headers["Location"])
		}
	})

	t.Run("anonymous with a customer order session id redirects to sign-in", func(t *testing.T) {
		customerJar, addressID := checkoutTestSetup(t, env, 1)
		_, customerSessionID := placeTestOrder(t, env, customerJar, addressID)
		request := pageRequest(http.MethodGet, "/checkout/confirm")
		request.QueryStringParameters = map[string]string{"session_id": customerSessionID}
		response, err := env.handler.Handle(context.Background(), request)
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		wantLocation := "/account/sign-in?return_to=" + url.QueryEscape("/checkout/confirm?session_id="+customerSessionID)
		if response.StatusCode != http.StatusSeeOther || response.Headers["Location"] != wantLocation {
			t.Fatalf("response = %d %q, want 303 %q", response.StatusCode, response.Headers["Location"], wantLocation)
		}
	})

	t.Run("anonymous without a session id 404s", func(t *testing.T) {
		response, err := env.handler.Handle(context.Background(), pageRequest(http.MethodGet, "/checkout/confirm"))
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (behavior delta from the old sign-in gate)", response.StatusCode)
		}
	})
}

func TestGuestConfirmAccessWindow(t *testing.T) {
	env := newAccountTestEnv(t)
	paidTime := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	env.handler.now = func() time.Time { return paidTime }
	env.handler.checkout.Now = func() time.Time { return paidTime }

	jar := guestCartJar(t, env, 1)
	orderID, sessionID := placeGuestTestOrder(t, env, jar, guestCheckoutEmail)
	if _, err := env.payments.MarkSessionPaid(sessionID, false); err != nil {
		t.Fatalf("MarkSessionPaid returned error: %v", err)
	}

	accessExpiry := func(t *testing.T, locationHeader string) int64 {
		t.Helper()
		location, err := url.Parse(locationHeader)
		if err != nil {
			t.Fatalf("Location does not parse: %v", err)
		}
		token := location.Query().Get("access")
		if token == "" {
			t.Fatalf("Location = %q, want an access token", locationHeader)
		}
		var payload guestOrderAccessPayload
		if !signedtoken.Decode(token, env.handler.customerSessionSecret, guestOrderAccessPurpose, &payload) {
			t.Fatalf("access token does not decode: %q", token)
		}
		if payload.OrderID != orderID {
			t.Fatalf("access token order = %q, want %q", payload.OrderID, orderID)
		}
		return payload.ExpiresAt
	}

	confirm := func(t *testing.T) events.APIGatewayV2HTTPResponse {
		t.Helper()
		request := jarPageRequest(http.MethodGet, "/checkout/confirm", jar)
		request.QueryStringParameters = map[string]string{"session_id": sessionID}
		response, err := env.handler.Handle(context.Background(), request)
		if err != nil {
			t.Fatalf("Handle confirm returned error: %v", err)
		}
		if response.StatusCode != http.StatusSeeOther {
			t.Fatalf("confirm status = %d, want 303", response.StatusCode)
		}
		return response
	}

	wantExpiry := paidTime.Add(guestOrderAccessTTL).Unix()
	first := confirm(t)
	if got := accessExpiry(t, first.Headers["Location"]); got != wantExpiry {
		t.Fatalf("first token expiry = %d, want PaidAt+30d %d", got, wantExpiry)
	}

	// A replay days later re-mints with the SAME expiry, never a fresher one.
	env.handler.now = func() time.Time { return paidTime.Add(5 * 24 * time.Hour) }
	replay := confirm(t)
	if got := accessExpiry(t, replay.Headers["Location"]); got != wantExpiry {
		t.Fatalf("replay token expiry = %d, want the original %d (replays never extend access)", got, wantExpiry)
	}

	// Past the window: tokenless redirect (the link is dead by design).
	env.handler.now = func() time.Time { return paidTime.Add(guestOrderAccessTTL + time.Hour) }
	late := confirm(t)
	if got := late.Headers["Location"]; got != "/orders/"+orderID+"?placed=1" {
		t.Fatalf("late confirm Location = %q, want the tokenless order URL", got)
	}
}

func TestGuestConfirmClearsDeadSessionCookie(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := guestCartJar(t, env, 1)
	orderID, sessionID := placeGuestTestOrder(t, env, jar, guestCheckoutEmail)
	_ = orderID
	jar[commerce.SessionCookieName] = "garbage"

	// Unpaid: the processing render carries the session-clearing cookie.
	processingRequest := jarPageRequest(http.MethodGet, "/checkout/confirm", jar)
	processingRequest.QueryStringParameters = map[string]string{"session_id": sessionID}
	processing, err := env.handler.Handle(context.Background(), processingRequest)
	if err != nil {
		t.Fatalf("Handle confirm returned error: %v", err)
	}
	if processing.StatusCode != http.StatusOK {
		t.Fatalf("processing status = %d, want 200", processing.StatusCode)
	}
	if raw := rawSetCookie(t, processing, commerce.SessionCookieName); !strings.Contains(raw, "Max-Age=0") {
		t.Fatalf("processing session cookie = %q, want clearing", raw)
	}

	// Paid: the success redirect carries it alongside the cart clear.
	if _, err := env.payments.MarkSessionPaid(sessionID, false); err != nil {
		t.Fatalf("MarkSessionPaid returned error: %v", err)
	}
	successRequest := jarPageRequest(http.MethodGet, "/checkout/confirm", jar)
	successRequest.QueryStringParameters = map[string]string{"session_id": sessionID}
	success, err := env.handler.Handle(context.Background(), successRequest)
	if err != nil {
		t.Fatalf("Handle confirm returned error: %v", err)
	}
	if success.StatusCode != http.StatusSeeOther {
		t.Fatalf("success status = %d, want 303", success.StatusCode)
	}
	if raw := rawSetCookie(t, success, commerce.SessionCookieName); !strings.Contains(raw, "Max-Age=0") {
		t.Fatalf("success session cookie = %q, want clearing", raw)
	}
	if raw := rawSetCookie(t, success, cart.CookieName); !strings.Contains(raw, "Max-Age=0") {
		t.Fatalf("success cart cookie = %q, want clearing", raw)
	}
}

func TestGuestConfirmUnpaidRendersProcessing(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := guestCartJar(t, env, 1)
	orderID, sessionID := placeGuestTestOrder(t, env, jar, guestCheckoutEmail)

	request := jarPageRequest(http.MethodGet, "/checkout/confirm", jar)
	request.QueryStringParameters = map[string]string{"session_id": sessionID}
	response, err := env.handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle confirm returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("confirm status = %d, want 200 processing page", response.StatusCode)
	}
	assertBodyContains(t, response.Body, []string{
		`<meta http-equiv="refresh" content="3">`,
		`data-testid="checkout-processing"`,
		`Payment processing`,
		`/orders/` + orderID,
	})
	if got, _, _ := env.commerce.GetOrder(context.Background(), orderID); got.Status != commerce.OrderStatusPendingPayment {
		t.Fatalf("order status = %q, want still pending_payment", got.Status)
	}
}

func TestGuestConfirmTerminalRendersPaymentReceived(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := guestCartJar(t, env, 1)
	orderID, sessionID := placeGuestTestOrder(t, env, jar, guestCheckoutEmail)

	if _, err := env.commerce.TransitionOrder(context.Background(), orderID, commerce.OrderStatusPendingPayment, commerce.OrderStatusCanceled, commerce.OrderPatch{Actor: commerce.OrderActorAdmin}); err != nil {
		t.Fatalf("TransitionOrder returned error: %v", err)
	}
	if _, err := env.payments.MarkSessionPaid(sessionID, false); err != nil {
		t.Fatalf("MarkSessionPaid returned error: %v", err)
	}

	request := jarPageRequest(http.MethodGet, "/checkout/confirm", jar)
	request.QueryStringParameters = map[string]string{"session_id": sessionID}
	response, err := env.handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle confirm returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("confirm status = %d body %q, want 200 payment-received page", response.StatusCode, response.Body)
	}
	assertBodyContains(t, response.Body, []string{
		`data-testid="checkout-payment-received"`,
		`Payment received`,
		`A refund is being issued automatically`,
		orderID,
	})
	if got, _, _ := env.commerce.GetOrder(context.Background(), orderID); got.Status != commerce.OrderStatusCanceled {
		t.Fatalf("order status = %q, want still canceled", got.Status)
	}
}

func TestGuestOrderDetailAccessToken(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := guestCartJar(t, env, 1)
	orderID, sessionID := placeGuestTestOrder(t, env, jar, guestCheckoutEmail)
	finalizeGuestOrderOffSite(t, env, orderID, sessionID)

	token, err := env.handler.mintGuestOrderAccessToken(orderID, env.handler.currentTime().Add(guestOrderAccessTTL))
	if err != nil {
		t.Fatalf("mintGuestOrderAccessToken returned error: %v", err)
	}
	tokenRequest := func(orderID string, access string) events.APIGatewayV2HTTPRequest {
		request := pageRequest(http.MethodGet, "/orders/"+orderID)
		request.QueryStringParameters = map[string]string{"access": access}
		return request
	}

	t.Run("valid token renders read-only", func(t *testing.T) {
		response, err := env.handler.Handle(context.Background(), tokenRequest(orderID, token))
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", response.StatusCode)
		}
		if got := response.Headers["Cache-Control"]; got != privatePageCacheControl {
			t.Fatalf("Cache-Control = %q, want %q", got, privatePageCacheControl)
		}
		if got := response.Headers["X-Robots-Tag"]; got != "noindex, follow" {
			t.Fatalf("X-Robots-Tag = %q, want noindex, follow", got)
		}
		assertBodyContains(t, response.Body, []string{
			`data-testid="order-status"`,
			`>Paid</span>`,
			`data-testid="guest-order-link-notice"`,
			guestOrderAccessPath(orderID, token),
		})
		assertBodyOmits(t, response.Body, []string{`Back to orders`, `href="/orders"`})
	})

	t.Run("placed renders the upsell", func(t *testing.T) {
		request := tokenRequest(orderID, token)
		request.QueryStringParameters["placed"] = "1"
		response, err := env.handler.Handle(context.Background(), request)
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		assertBodyContains(t, response.Body, []string{`data-testid="guest-signup-upsell"`, `href="/account/sign-up"`})
	})

	t.Run("tampered token redirects to sign-in", func(t *testing.T) {
		response, err := env.handler.Handle(context.Background(), tokenRequest(orderID, token+"x"))
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusSeeOther || response.Headers["Location"] != "/account/sign-in?return_to="+url.QueryEscape("/orders/"+orderID) {
			t.Fatalf("response = %d %q, want the anonymous sign-in redirect", response.StatusCode, response.Headers["Location"])
		}
	})

	t.Run("expired token redirects to sign-in", func(t *testing.T) {
		baseNow := env.handler.currentTime()
		env.handler.now = func() time.Time { return baseNow.Add(guestOrderAccessTTL + time.Hour) }
		defer func() { env.handler.now = time.Now }()
		response, err := env.handler.Handle(context.Background(), tokenRequest(orderID, token))
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusSeeOther {
			t.Fatalf("status = %d, want 303 (expired == absent)", response.StatusCode)
		}
	})

	t.Run("token never unlocks a customer order", func(t *testing.T) {
		customerJar, addressID := checkoutTestSetup(t, env, 1)
		customerOrderID, _ := placeTestOrder(t, env, customerJar, addressID)
		customerToken, err := env.handler.mintGuestOrderAccessToken(customerOrderID, env.handler.currentTime().Add(guestOrderAccessTTL))
		if err != nil {
			t.Fatalf("mintGuestOrderAccessToken returned error: %v", err)
		}

		anonymous, err := env.handler.Handle(context.Background(), tokenRequest(customerOrderID, customerToken))
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if anonymous.StatusCode != http.StatusSeeOther {
			t.Fatalf("anonymous status = %d, want 303 sign-in", anonymous.StatusCode)
		}

		intruderJar := testCookieJar{}
		signUpTestCustomer(t, env.handler, intruderJar, "intruder@example.com", "orchid-market-99")
		signedInRequest := jarPageRequest(http.MethodGet, "/orders/"+customerOrderID, intruderJar)
		signedInRequest.QueryStringParameters = map[string]string{"access": customerToken}
		signedIn, err := env.handler.Handle(context.Background(), signedInRequest)
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if signedIn.StatusCode != http.StatusNotFound {
			t.Fatalf("signed-in status = %d, want 404", signedIn.StatusCode)
		}

		// The owner path is unchanged.
		owner, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/orders/"+customerOrderID, customerJar))
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if owner.StatusCode != http.StatusOK {
			t.Fatalf("owner status = %d, want 200", owner.StatusCode)
		}
		assertBodyContains(t, owner.Body, []string{`Back to orders`})
		assertBodyOmits(t, owner.Body, []string{`data-testid="guest-order-link-notice"`})
	})
}
