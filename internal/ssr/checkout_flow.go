package ssr

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/checkout"
	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/httpapi"
	"github.com/anhydrous99/thailandgiftshop/internal/payments"
	"github.com/aws/aws-lambda-go/events"
)

// ordersPageSize is the order-history page length (gsi1 newest-first).
const ordersPageSize = 20

// ordersScanLimit bounds how far back the cursor can page. The store
// interface exposes a single limit-bounded query, so cursor paging scans this
// window and slices; a store-level exclusive-start key is the future upgrade.
const ordersScanLimit = 200

const checkoutPaymentStatusPaid = "paid"
const checkoutSessionModeSetup = "setup"

const chooseAddressError = "Choose a shipping address to continue."
const checkoutStockChangedError = "Stock changed while you were checking out. Quantities were updated — review your order and try again."
const checkoutPaymentInFlightError = "Your previous payment is still processing. Try again shortly — if it completes, the order appears in your order history."

// handleCheckoutFlowRoute dispatches the session-gated checkout and order
// routes. The shared method gate and trailing-slash redirects already ran in
// handle(); session resolution and, for POSTs, CSRF validation happen here
// before any per-route work (the account-route ordering).
func (h *Handler) handleCheckoutFlowRoute(ctx context.Context, request events.APIGatewayV2HTTPRequest, route pageRoute) events.APIGatewayV2HTTPResponse {
	switch route.kind {
	case pageCheckout:
		return h.handleCheckoutPage(ctx, request)
	case pageCheckoutPlaceOrder:
		return h.handlePlaceOrder(ctx, request)
	case pageCheckoutConfirm:
		return h.handleCheckoutConfirm(ctx, request)
	case pageCheckoutFakePay:
		return h.handleFakePay(ctx, request)
	case pageOrders:
		return h.handleOrdersPage(ctx, request)
	case pageOrderDetail:
		return h.handleOrderDetailPage(ctx, request, route.slug)
	}

	return h.accountNotFoundResponse(ctx, request, route.kind)
}

// handleCheckoutPage renders the real checkout: address selection, the priced
// order summary, and the single place-order action. The empty/unavailable-cart
// guard keeps its long-standing 303 /cart behavior and runs before the
// sign-in gate so an empty-cart visit never bounces through sign-in.
func (h *Handler) handleCheckoutPage(ctx context.Context, request events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	method := httpapi.Method(request)
	state, err := h.cartStateFromRequest(ctx, request)
	if err != nil {
		logHandlerError(pageCheckout, method, httpapi.Path(request), err)
		return httpapi.HTMLResponse(http.StatusInternalServerError, "Internal server error", nil)
	}
	if response, redirected := checkoutCartGuard(state, method); redirected {
		return response
	}
	if !state.signedIn {
		return accountSeeOther(signInLocationForReturnTo("/checkout"), pageCheckout, state.cookies)
	}

	session, customer, signedIn, clearingCookies := h.customerSession(ctx, request)
	if !signedIn {
		return accountSeeOther(signInLocationForReturnTo("/checkout"), pageCheckout, clearingCookies)
	}

	return h.renderCheckoutPage(ctx, request, session, customer, state, checkoutRenderState{
		Canceled: request.QueryStringParameters["canceled"] == "1",
	}, http.StatusOK)
}

// checkoutCartGuard preserves the existing empty/unavailable-cart redirect.
func checkoutCartGuard(state requestCart, method string) (events.APIGatewayV2HTTPResponse, bool) {
	if state.cart.LineCount() != 0 && !hasUnavailableCartLines(state.lines) {
		return events.APIGatewayV2HTTPResponse{}, false
	}
	response := httpapi.SeeOther("/cart", state.cookies, seoHeadersForRoute(pageCheckout))
	if method == http.MethodHead {
		response.Body = ""
	}
	return response, true
}

// checkoutRenderState carries the per-request banners and per-line notices
// the checkout page renders on top of the cart and address data.
type checkoutRenderState struct {
	Canceled          bool
	ErrorMessage      string
	SelectedAddressID string
	LineNotices       map[string]string // keyed by slug+"\x00"+variantID
}

func (h *Handler) renderCheckoutPage(ctx context.Context, request events.APIGatewayV2HTTPRequest, session commerce.Session, customer commerce.Customer, state requestCart, render checkoutRenderState, statusCode int) events.APIGatewayV2HTTPResponse {
	csrfToken, csrfCookies, err := h.customerCSRFForResponse(request, session)
	if err != nil {
		logAccountError("checkout: issue csrf token", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckout, nil)
	}
	addresses, err := h.commerce.ListAddresses(ctx, customer.ID)
	if err != nil {
		logAccountError("checkout: list addresses", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckout, nil)
	}

	addressRows := make([]addressView, 0, len(addresses))
	for _, address := range addresses {
		addressRows = append(addressRows, addressRowView(address, customer.DefaultAddressID))
	}
	selected := render.SelectedAddressID
	if _, found := findAddressByID(addresses, selected); !found {
		selected = ""
	}
	if selected == "" {
		if _, found := findAddressByID(addresses, customer.DefaultAddressID); found {
			selected = customer.DefaultAddressID
		} else if len(addresses) > 0 {
			selected = addresses[0].ID
		}
	}

	subtotal := subtotalCents(state.lines)
	vm := checkoutPageViewModel{
		Metadata:        checkoutMetadata(),
		Breadcrumbs:     checkoutBreadcrumbs(),
		HeaderCartLabel: cartNavigationLabel(state.cart.TotalItemCount()),
		CSRFToken:       csrfToken,
		Lines:           h.checkoutLineViews(state.lines, render.LineNotices),
		Addresses:       addressRows,
		SelectedAddress: selected,
		Subtotal:        formatPrice(subtotal),
		Total:           formatPrice(subtotal),
		Canceled:        render.Canceled,
		ErrorMessage:    render.ErrorMessage,
	}

	var body bytes.Buffer
	if err := checkoutPage(vm).Render(ctx, &body); err != nil {
		logAccountError("checkout: render", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckout, nil)
	}

	cookies := append(append([]string(nil), state.cookies...), csrfCookies...)
	return accountHTMLResponse(statusCode, maybeEmptyBody(httpapi.Method(request), body.String()), pageCheckout, cookies)
}

func (h *Handler) checkoutLineViews(lines []cartLineView, notices map[string]string) []checkoutLineView {
	views := make([]checkoutLineView, 0, len(lines))
	for _, line := range lines {
		views = append(views, checkoutLineView{
			Name:         line.Product.Name,
			Slug:         line.Product.Slug,
			VariantLabel: line.VariantLabel,
			ImageURL:     line.Product.DisplayImageURL(h.productImagePlaceholderURL),
			Quantity:     line.Quantity,
			UnitPrice:    formatPrice(line.Product.PriceCents),
			LineTotal:    formatPrice(lineTotalCents(line)),
			Notice:       notices[checkoutLineKey(line.Product.Slug, line.VariantID)],
		})
	}
	return views
}

func checkoutLineKey(slug string, variantID string) string {
	return slug + "\x00" + variantID
}

// handlePlaceOrder runs the §7.1 sequence through checkout.Service.PlaceOrder
// and redirects the shopper to the provider's hosted payment page (or the
// on-site fake-pay page in demo mode).
func (h *Handler) handlePlaceOrder(ctx context.Context, request events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	session, customer, signedIn, clearingCookies := h.customerSession(ctx, request)
	if !signedIn {
		return accountSeeOther(signInLocationForReturnTo("/checkout"), pageCheckoutPlaceOrder, clearingCookies)
	}
	if !h.validCustomerCSRF(request, session) {
		return accountHTMLResponse(http.StatusForbidden, "Forbidden", pageCheckoutPlaceOrder, nil)
	}
	if h.checkout == nil || h.commerce == nil {
		logAccountError("place order", errors.New("checkout service not configured"))
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckoutPlaceOrder, nil)
	}

	state, err := h.serverCartState(ctx, request, customer.ID)
	if err != nil {
		logAccountError("place order: load cart", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckoutPlaceOrder, nil)
	}
	if response, redirected := checkoutCartGuard(state, httpapi.Method(request)); redirected {
		return response
	}

	form, err := httpapi.FormValues(request)
	if err != nil {
		return accountHTMLResponse(http.StatusBadRequest, "Bad request", pageCheckoutPlaceOrder, nil)
	}
	addressID := strings.TrimSpace(form.Get("address_id"))
	if !accountIDPattern.MatchString(addressID) {
		return h.renderCheckoutPage(ctx, request, session, customer, state, checkoutRenderState{ErrorMessage: chooseAddressError}, http.StatusBadRequest)
	}
	address, found, err := h.commerce.GetAddress(ctx, customer.ID, addressID)
	if err != nil {
		logAccountError("place order: load address", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckoutPlaceOrder, nil)
	}
	if !found {
		return h.renderCheckoutPage(ctx, request, session, customer, state, checkoutRenderState{ErrorMessage: chooseAddressError}, http.StatusBadRequest)
	}

	redirectURL, _, err := h.checkout.PlaceOrder(ctx, checkout.PlaceOrderInput{
		Customer:  customer,
		Email:     customer.Email,
		AddressID: address.ID,
		Address:   orderAddressFromAddress(address),
		Lines:     h.orderLinesFromCartViews(state.lines),
		Cart:      state.record,
	})
	if err != nil {
		var insufficient catalog.InsufficientStockError
		if errors.As(err, &insufficient) {
			return h.renderInsufficientStockCheckout(ctx, request, session, customer, address.ID, insufficient)
		}
		if errors.Is(err, checkout.ErrPaymentSessionInFlight) {
			// The previous order's completed session is still settling its
			// asynchronous payment: the order must stay pending, so re-render
			// checkout with a retry-shortly notice instead of a 500.
			logAccountError("place order: previous payment in flight", err)
			return h.renderCheckoutPage(ctx, request, session, customer, state, checkoutRenderState{
				ErrorMessage:      checkoutPaymentInFlightError,
				SelectedAddressID: address.ID,
			}, http.StatusConflict)
		}
		// Version-conflict exhaustion and provider failures are transient;
		// PlaceOrder already compensated (released stock, canceled the order).
		logAccountError("place order", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckoutPlaceOrder, nil)
	}

	return accountSeeOther(redirectURL, pageCheckoutPlaceOrder, state.cookies)
}

// renderInsufficientStockCheckout re-resolves the server cart (normalization
// re-clamps quantities to the live stock and persists the repaired cart plus
// its mirror) and re-renders checkout with the per-line notice.
func (h *Handler) renderInsufficientStockCheckout(ctx context.Context, request events.APIGatewayV2HTTPRequest, session commerce.Session, customer commerce.Customer, addressID string, insufficient catalog.InsufficientStockError) events.APIGatewayV2HTTPResponse {
	state, err := h.serverCartState(ctx, request, customer.ID)
	if err != nil {
		logAccountError("place order: reload cart after stock shortage", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckoutPlaceOrder, nil)
	}
	if response, redirected := checkoutCartGuard(state, httpapi.Method(request)); redirected {
		return response
	}

	notices := map[string]string{}
	for _, line := range state.lines {
		if line.Product.Slug != insufficient.Slug || line.VariantID != insufficient.VariantID {
			continue
		}
		notices[checkoutLineKey(line.Product.Slug, line.VariantID)] = insufficientStockNotice(line.Product.Name, insufficient.Available)
	}

	return h.renderCheckoutPage(ctx, request, session, customer, state, checkoutRenderState{
		ErrorMessage:      checkoutStockChangedError,
		SelectedAddressID: addressID,
		LineNotices:       notices,
	}, http.StatusConflict)
}

func insufficientStockNotice(name string, available int) string {
	if available <= 0 {
		return name + " just sold out — it was removed from your order."
	}
	return "Only " + strconv.Itoa(available) + " left of " + name + " — quantities updated."
}

func (h *Handler) orderLinesFromCartViews(lines []cartLineView) []commerce.OrderLine {
	orderLines := make([]commerce.OrderLine, 0, len(lines))
	for _, line := range lines {
		orderLines = append(orderLines, commerce.OrderLine{
			Slug:           line.Product.Slug,
			ProductID:      line.Product.ID,
			Name:           line.Product.Name,
			VariantID:      line.VariantID,
			VariantLabel:   line.VariantLabel,
			UnitPriceCents: line.Product.PriceCents,
			Quantity:       line.Quantity,
			LineTotalCents: lineTotalCents(line),
			ImageURL:       line.Product.DisplayImageURL(h.productImagePlaceholderURL),
		})
	}
	return orderLines
}

func orderAddressFromAddress(address commerce.Address) commerce.OrderAddress {
	return commerce.OrderAddress{
		FullName:   address.FullName,
		Line1:      address.Line1,
		Line2:      address.Line2,
		City:       address.City,
		Region:     address.Region,
		PostalCode: address.PostalCode,
		Country:    address.Country,
		Phone:      address.Phone,
	}
}

// handleCheckoutConfirm is the §7.4 reconcile page. The session_id query
// parameter is never trusted as payment proof: the session is fetched from
// the provider server-side, the order ownership is checked, and only a
// provider-confirmed paid status finalizes the order.
func (h *Handler) handleCheckoutConfirm(ctx context.Context, request events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	method := httpapi.Method(request)
	_, customer, signedIn, clearingCookies := h.customerSession(ctx, request)
	if !signedIn {
		return accountSeeOther(signInLocationForReturnTo(confirmReturnTo(request)), pageCheckoutConfirm, clearingCookies)
	}
	if h.payments == nil || h.checkout == nil || h.commerce == nil {
		return h.accountNotFoundResponse(ctx, request, pageCheckoutConfirm)
	}

	sessionID := strings.TrimSpace(request.QueryStringParameters["session_id"])
	if sessionID == "" {
		return h.accountNotFoundResponse(ctx, request, pageCheckoutConfirm)
	}
	paymentSession, err := h.payments.GetSession(ctx, sessionID)
	if errors.Is(err, payments.ErrSessionNotFound) {
		return h.accountNotFoundResponse(ctx, request, pageCheckoutConfirm)
	}
	if err != nil {
		logAccountError("checkout confirm: load payment session", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckoutConfirm, nil)
	}
	if paymentSession.OrderID == "" {
		return h.accountNotFoundResponse(ctx, request, pageCheckoutConfirm)
	}
	order, found, err := h.commerce.GetOrder(ctx, paymentSession.OrderID)
	if err != nil {
		logAccountError("checkout confirm: load order", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckoutConfirm, nil)
	}
	if !found || order.CustomerID != customer.ID {
		// Not-found for foreign orders too: ownership must not leak existence.
		return h.accountNotFoundResponse(ctx, request, pageCheckoutConfirm)
	}

	if paymentSession.PaymentStatus != checkoutPaymentStatusPaid {
		return h.renderCheckoutProcessingPage(ctx, request, order.ID)
	}

	finalized, err := h.checkout.FinalizePayment(ctx, order.ID, paymentSession)
	if err != nil {
		if errors.Is(err, checkout.ErrOrderNotFinalizable) {
			// The payment landed after the order reached a terminal status
			// (the paid_after_terminal webhook condition): the customer was
			// charged, so never show them an error page — acknowledge the
			// payment and point them at support while the refund is handled.
			logAccountError("checkout confirm: payment received for terminal order", err)
			return h.renderCheckoutPaymentReceivedPage(ctx, request, order.ID)
		}
		if !orderStatusPaidOrLater(finalized.Status) {
			logAccountError("checkout confirm: finalize payment", err)
			return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckoutConfirm, nil)
		}
		// The order is paid; only the idempotent cart cleanup failed. The
		// webhook replay re-attempts it, so the shopper still gets success.
		logAccountError("checkout confirm: cart cleanup degraded", err)
	}

	// Success clears the tgs_cart mirror alongside the server cart that
	// FinalizePayment just emptied.
	response := httpapi.SeeOther("/orders/"+order.ID+"?placed=1", []string{clearCartCookie(request)}, seoHeadersForRoute(pageCheckoutConfirm))
	if method == http.MethodHead {
		response.Body = ""
	}
	return response
}

func confirmReturnTo(request events.APIGatewayV2HTTPRequest) string {
	returnTo := "/checkout/confirm"
	if sessionID := strings.TrimSpace(request.QueryStringParameters["session_id"]); sessionID != "" {
		returnTo += "?session_id=" + url.QueryEscape(sessionID)
	}
	return returnTo
}

func (h *Handler) renderCheckoutProcessingPage(ctx context.Context, request events.APIGatewayV2HTTPRequest, orderID string) events.APIGatewayV2HTTPResponse {
	var body bytes.Buffer
	if err := checkoutProcessingPage(checkoutProcessingPageData{
		Metadata:        checkoutConfirmMetadata(),
		HeaderCartLabel: h.cartNavigation(request),
		OrderID:         orderID,
	}).Render(ctx, &body); err != nil {
		logAccountError("checkout confirm: render processing page", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckoutConfirm, nil)
	}

	return accountHTMLResponse(http.StatusOK, maybeEmptyBody(httpapi.Method(request), body.String()), pageCheckoutConfirm, nil)
}

// renderCheckoutPaymentReceivedPage tells a charged customer whose order hit a
// terminal status (canceled/expired before the payment settled) that their
// payment was received and support will refund or reinstate it — never a 500.
func (h *Handler) renderCheckoutPaymentReceivedPage(ctx context.Context, request events.APIGatewayV2HTTPRequest, orderID string) events.APIGatewayV2HTTPResponse {
	var body bytes.Buffer
	if err := checkoutPaymentReceivedPage(checkoutProcessingPageData{
		Metadata:        checkoutConfirmMetadata(),
		HeaderCartLabel: h.cartNavigation(request),
		OrderID:         orderID,
	}).Render(ctx, &body); err != nil {
		logAccountError("checkout confirm: render payment received page", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckoutConfirm, nil)
	}

	return accountHTMLResponse(http.StatusOK, maybeEmptyBody(httpapi.Method(request), body.String()), pageCheckoutConfirm, nil)
}

// handleFakePay serves the demo payment page. It exists only when the wired
// provider is the in-memory fake (local demo runs and CI); with Stripe — and
// therefore always in production — the route 404s.
func (h *Handler) handleFakePay(ctx context.Context, request events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	fakeProvider, ok := h.payments.(*payments.FakeProvider)
	if !ok || fakeProvider.Kind() != payments.KindFake {
		return h.accountNotFoundResponse(ctx, request, pageCheckoutFakePay)
	}

	session, customer, signedIn, clearingCookies := h.customerSession(ctx, request)
	if !signedIn {
		return accountSeeOther(signInLocationForReturnTo(fakePayReturnTo(request)), pageCheckoutFakePay, clearingCookies)
	}
	if httpapi.Method(request) == http.MethodPost {
		return h.handleFakePaySubmit(ctx, request, session, customer, fakeProvider)
	}

	sessionID := strings.TrimSpace(request.QueryStringParameters["session_id"])
	paymentSession, owned := h.ownedFakePaySession(ctx, customer, fakeProvider, sessionID)
	if !owned {
		return h.accountNotFoundResponse(ctx, request, pageCheckoutFakePay)
	}
	csrfToken, csrfCookies, err := h.customerCSRFForResponse(request, session)
	if err != nil {
		logAccountError("fake pay: issue csrf token", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckoutFakePay, nil)
	}

	var body bytes.Buffer
	if err := fakePayPage(fakePayPageData{
		Metadata:        fakePayMetadata(),
		HeaderCartLabel: h.cartNavigation(request),
		CSRFToken:       csrfToken,
		SessionID:       paymentSession.ID,
		SetupMode:       paymentSession.Mode == checkoutSessionModeSetup,
		Total:           formatPrice(paymentSession.AmountTotalCents),
	}).Render(ctx, &body); err != nil {
		logAccountError("fake pay: render", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckoutFakePay, nil)
	}

	return accountHTMLResponse(http.StatusOK, maybeEmptyBody(httpapi.Method(request), body.String()), pageCheckoutFakePay, csrfCookies)
}

func (h *Handler) handleFakePaySubmit(ctx context.Context, request events.APIGatewayV2HTTPRequest, session commerce.Session, customer commerce.Customer, fakeProvider *payments.FakeProvider) events.APIGatewayV2HTTPResponse {
	if !h.validCustomerCSRF(request, session) {
		return accountHTMLResponse(http.StatusForbidden, "Forbidden", pageCheckoutFakePay, nil)
	}
	form, err := httpapi.FormValues(request)
	if err != nil {
		return accountHTMLResponse(http.StatusBadRequest, "Bad request", pageCheckoutFakePay, nil)
	}
	sessionID := strings.TrimSpace(form.Get("session_id"))
	paymentSession, owned := h.ownedFakePaySession(ctx, customer, fakeProvider, sessionID)
	if !owned {
		return h.accountNotFoundResponse(ctx, request, pageCheckoutFakePay)
	}

	switch form.Get("action") {
	case "pay":
		var redirectURL string
		var markErr error
		if paymentSession.Mode == checkoutSessionModeSetup {
			redirectURL, markErr = fakeProvider.MarkSetupComplete(paymentSession.ID)
		} else {
			saveCard := form.Get("save_card") != ""
			redirectURL, markErr = fakeProvider.MarkSessionPaid(paymentSession.ID, saveCard)
		}
		if markErr != nil {
			return h.accountNotFoundResponse(ctx, request, pageCheckoutFakePay)
		}
		return accountSeeOther(redirectURL, pageCheckoutFakePay, nil)
	case "cancel":
		_, cancelURL, found := fakeProvider.SessionRedirectURLs(paymentSession.ID)
		if !found {
			return h.accountNotFoundResponse(ctx, request, pageCheckoutFakePay)
		}
		return accountSeeOther(cancelURL, pageCheckoutFakePay, nil)
	}

	return accountHTMLResponse(http.StatusBadRequest, "Bad request", pageCheckoutFakePay, nil)
}

// ownedFakePaySession loads a fake session and checks ownership: payment
// sessions must belong to one of the signed-in customer's orders (404
// otherwise, never 403). Setup sessions are keyed by the unguessable customer
// id; the fake provider never runs in production.
func (h *Handler) ownedFakePaySession(ctx context.Context, customer commerce.Customer, fakeProvider *payments.FakeProvider, sessionID string) (payments.Session, bool) {
	if sessionID == "" {
		return payments.Session{}, false
	}
	paymentSession, err := fakeProvider.GetSession(ctx, sessionID)
	if err != nil {
		return payments.Session{}, false
	}
	if paymentSession.Mode == checkoutSessionModeSetup {
		return paymentSession, true
	}
	if h.commerce == nil || paymentSession.OrderID == "" {
		return payments.Session{}, false
	}
	order, found, err := h.commerce.GetOrder(ctx, paymentSession.OrderID)
	if err != nil || !found || order.CustomerID != customer.ID {
		return payments.Session{}, false
	}

	return paymentSession, true
}

func fakePayReturnTo(request events.APIGatewayV2HTTPRequest) string {
	returnTo := "/checkout/fake-pay"
	if sessionID := strings.TrimSpace(request.QueryStringParameters["session_id"]); sessionID != "" {
		returnTo += "?session_id=" + url.QueryEscape(sessionID)
	}
	return returnTo
}

// handleOrdersPage renders the order history newest-first with cursor paging:
// ?after=<orderID> resumes below that order.
func (h *Handler) handleOrdersPage(ctx context.Context, request events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	_, customer, signedIn, clearingCookies := h.customerSession(ctx, request)
	if !signedIn {
		return accountSeeOther(signInLocationForReturnTo("/orders"), pageOrders, clearingCookies)
	}

	orders, err := h.commerce.ListOrdersByCustomer(ctx, customer.ID, ordersScanLimit)
	if err != nil {
		logAccountError("orders: list", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageOrders, nil)
	}

	start := 0
	if after := request.QueryStringParameters["after"]; accountIDPattern.MatchString(after) {
		for index, order := range orders {
			if order.ID == after {
				start = index + 1
				break
			}
		}
	}
	end := min(start+ordersPageSize, len(orders))
	page := orders[start:end]
	nextCursor := ""
	if end < len(orders) && len(page) > 0 {
		nextCursor = page[len(page)-1].ID
	}

	rows := make([]orderRowView, 0, len(page))
	for _, order := range page {
		rows = append(rows, orderRowView{
			ID:          order.ID,
			StatusLabel: orderStatusLabel(order.Status),
			PlacedAt:    formatOrderDate(order.CreatedAt),
			Total:       formatPrice(order.TotalCents),
			ItemCount:   orderItemCount(order),
		})
	}

	var body bytes.Buffer
	if err := ordersPage(ordersPageData{
		Metadata:        ordersMetadata(),
		Breadcrumbs:     ordersBreadcrumbs(),
		HeaderCartLabel: h.cartNavigation(request),
		Orders:          rows,
		NextCursor:      nextCursor,
	}).Render(ctx, &body); err != nil {
		logAccountError("orders: render", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageOrders, nil)
	}

	return accountHTMLResponse(http.StatusOK, maybeEmptyBody(httpapi.Method(request), body.String()), pageOrders, nil)
}

// handleOrderDetailPage renders one order from its frozen snapshot. A foreign
// order is a 404, never a 403. Pending orders get a reconcile backstop so a
// shopper landing here before the webhook still sees the paid state.
func (h *Handler) handleOrderDetailPage(ctx context.Context, request events.APIGatewayV2HTTPRequest, orderID string) events.APIGatewayV2HTTPResponse {
	_, customer, signedIn, clearingCookies := h.customerSession(ctx, request)
	if !signedIn {
		return accountSeeOther(signInLocationForReturnTo("/orders/"+orderID), pageOrderDetail, clearingCookies)
	}

	order, found, err := h.commerce.GetOrder(ctx, orderID)
	if err != nil {
		logAccountError("order detail: load", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageOrderDetail, nil)
	}
	if !found || order.CustomerID != customer.ID {
		return h.accountNotFoundResponse(ctx, request, pageOrderDetail)
	}

	order = h.reconcilePendingOrder(ctx, order)

	vm := orderDetailPageData{
		Metadata:        orderDetailMetadata(order.ID),
		Breadcrumbs:     orderDetailBreadcrumbs(order.ID),
		HeaderCartLabel: h.cartNavigation(request),
		Placed:          request.QueryStringParameters["placed"] == "1",
		OrderID:         order.ID,
		StatusLabel:     orderStatusLabel(order.Status),
		PlacedAt:        formatOrderDate(order.CreatedAt),
		Lines:           orderLineViews(order.Lines),
		Subtotal:        formatPrice(order.SubtotalCents),
		Shipping:        formatPrice(order.ShippingCents),
		Tax:             formatPrice(order.TaxCents),
		Total:           formatPrice(order.TotalCents),
		Address: orderAddressView{
			FullName:   order.ShippingAddress.FullName,
			Line1:      order.ShippingAddress.Line1,
			Line2:      order.ShippingAddress.Line2,
			City:       order.ShippingAddress.City,
			Region:     order.ShippingAddress.Region,
			PostalCode: order.ShippingAddress.PostalCode,
			Country:    order.ShippingAddress.Country,
			Phone:      order.ShippingAddress.Phone,
		},
		CardBrand:       brandDisplayName(order.PaymentCardBrand),
		CardLast4:       order.PaymentCardLast4,
		TrackingCarrier: order.TrackingCarrier,
		TrackingNumber:  order.TrackingNumber,
		Timeline:        orderTimelineSteps(order.StatusHistory),
	}
	if order.PaymentCardLast4 == "" {
		vm.CardBrand = ""
	}

	var body bytes.Buffer
	if err := orderDetailPage(vm).Render(ctx, &body); err != nil {
		logAccountError("order detail: render", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageOrderDetail, nil)
	}

	return accountHTMLResponse(http.StatusOK, maybeEmptyBody(httpapi.Method(request), body.String()), pageOrderDetail, nil)
}

// reconcilePendingOrder is the §7.4 webhook-latency backstop: rendering a
// pending order re-checks the provider session and finalizes when it already
// paid. Idempotent; every failure degrades to rendering the stored order.
func (h *Handler) reconcilePendingOrder(ctx context.Context, order commerce.Order) commerce.Order {
	if order.Status != commerce.OrderStatusPendingPayment || order.StripeCheckoutSessionID == "" {
		return order
	}
	if h.payments == nil || h.checkout == nil {
		return order
	}

	paymentSession, err := h.payments.GetSession(ctx, order.StripeCheckoutSessionID)
	if err != nil {
		logAccountError("order detail: reconcile session lookup", err)
		return order
	}
	if paymentSession.PaymentStatus != checkoutPaymentStatusPaid {
		return order
	}
	finalized, err := h.checkout.FinalizePayment(ctx, order.ID, paymentSession)
	if err != nil {
		logAccountError("order detail: reconcile finalize", err)
		if orderStatusPaidOrLater(finalized.Status) {
			return finalized
		}
		return order
	}

	return finalized
}

func orderStatusPaidOrLater(status commerce.OrderStatus) bool {
	switch status {
	case commerce.OrderStatusPaid, commerce.OrderStatusShipped, commerce.OrderStatusDelivered:
		return true
	}
	return false
}

func orderLineViews(lines []commerce.OrderLine) []orderLineView {
	views := make([]orderLineView, 0, len(lines))
	for _, line := range lines {
		views = append(views, orderLineView{
			Name:         line.Name,
			VariantLabel: line.VariantLabel,
			ImageURL:     line.ImageURL,
			Quantity:     line.Quantity,
			UnitPrice:    formatPrice(line.UnitPriceCents),
			LineTotal:    formatPrice(line.LineTotalCents),
		})
	}
	return views
}

func orderTimelineSteps(history []commerce.StatusEvent) []orderTimelineStep {
	steps := make([]orderTimelineStep, 0, len(history))
	for _, event := range history {
		steps = append(steps, orderTimelineStep{
			StatusLabel: orderStatusLabel(event.Status),
			At:          formatOrderTime(event.At),
			Actor:       event.Actor,
		})
	}
	return steps
}

func orderItemCount(order commerce.Order) int {
	count := 0
	for _, line := range order.Lines {
		count += line.Quantity
	}
	return count
}

func formatOrderDate(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format("Jan 2, 2006")
}

func formatOrderTime(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format("Jan 2, 2006 15:04 UTC")
}
