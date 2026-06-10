package ssr

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/cart"
	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/observability"
	"github.com/anhydrous99/thailandgiftshop/internal/payments"
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

func TestCheckoutFlowAnonymousRedirectsToSignIn(t *testing.T) {
	env := newAccountTestEnv(t)
	tests := []struct {
		name     string
		method   string
		path     string
		query    map[string]string
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
		{
			name:     "confirm keeps the session id in return_to",
			method:   http.MethodGet,
			path:     "/checkout/confirm",
			query:    map[string]string{"session_id": "cs_fake_x"},
			location: "/account/sign-in?return_to=" + url.QueryEscape("/checkout/confirm?session_id=cs_fake_x"),
		},
		{
			name:     "fake pay keeps the session id in return_to",
			method:   http.MethodGet,
			path:     "/checkout/fake-pay",
			query:    map[string]string{"session_id": "cs_fake_x"},
			location: "/account/sign-in?return_to=" + url.QueryEscape("/checkout/fake-pay?session_id=cs_fake_x"),
		},
		{
			name:     "place order",
			method:   http.MethodPost,
			path:     "/checkout/place-order",
			location: "/account/sign-in?return_to=%2Fcheckout",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := pageRequest(test.method, test.path)
			request.QueryStringParameters = test.query
			response, err := env.handler.Handle(context.Background(), request)
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

	t.Run("checkout with items redirects with return_to", func(t *testing.T) {
		request := pageRequest(http.MethodGet, "/checkout")
		request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{{Slug: "mango-sticky-rice-kit", Quantity: 1}})}
		response, err := env.handler.Handle(context.Background(), request)
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusSeeOther || response.Headers["Location"] != "/account/sign-in?return_to=%2Fcheckout" {
			t.Fatalf("response = %d %q, want 303 to sign-in with return_to", response.StatusCode, response.Headers["Location"])
		}
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
		`A refund is pending`,
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

func TestOrdersPageListsNewestFirstWithCursorPaging(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	customerID := accountCustomerID(t, env, "shopper@example.com")

	base := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	orderIDs := make([]string, 0, 25)
	for index := range 25 {
		created, err := env.commerce.CreateOrder(context.Background(), commerce.Order{
			CustomerID:    customerID,
			Email:         "shopper@example.com",
			Status:        commerce.OrderStatusPaid,
			Lines:         []commerce.OrderLine{{Slug: "mango-sticky-rice-kit", ProductID: "prod_account_mango", Name: "Mango Sticky Rice Treats", UnitPriceCents: 2899, Quantity: 1, LineTotalCents: 2899}},
			SubtotalCents: 2899,
			TotalCents:    2899,
			Currency:      "usd",
			CreatedAt:     base.Add(time.Duration(index) * time.Minute),
		})
		if err != nil {
			t.Fatalf("CreateOrder %d returned error: %v", index, err)
		}
		orderIDs = append(orderIDs, created.ID)
	}
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

	// The cursor is the last order on the page (the 20th newest = index 5).
	cursorID := orderIDs[5]
	if !strings.Contains(firstPage.Body, `href="/orders?after=`+cursorID+`"`) {
		t.Fatalf("first page is missing the next-page cursor link for %s", cursorID)
	}

	secondPageRequest := jarPageRequest(http.MethodGet, "/orders", jar)
	secondPageRequest.QueryStringParameters = map[string]string{"after": cursorID}
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
