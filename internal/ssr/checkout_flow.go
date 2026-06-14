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
	"github.com/anhydrous99/thailandgiftshop/internal/signedtoken"
	"github.com/aws/aws-lambda-go/events"
)

// ordersPageSize is the order-history page length. Pages walk the
// customer-orders index (gsi1) newest-first via store-level cursors; the
// ?after URL parameter is an HMAC-signed, customer-bound cursor token
// (decodeOrdersCursor).
const ordersPageSize = 20

const checkoutPaymentStatusPaid = "paid"
const checkoutSessionModeSetup = "setup"
const checkoutCancelURLPath = "/checkout?canceled=1"
const checkoutCancelTokenParam = "cancel_token"
const checkoutCancelReturnTTL = time.Hour

const chooseAddressError = "Choose a shipping address to continue."
const checkoutStockChangedError = "Stock changed while you were checking out. Quantities were updated — review your order and try again."
const checkoutPaymentInFlightError = "Your previous payment is still processing. Try again shortly — if it completes, the order appears in your order history."

var checkoutCancelReturnPurpose = []byte("tgs-checkout-cancel-return")

type checkoutCancelReturnPayload struct {
	Version   int    `json:"version"`
	OrderID   string `json:"order_id"`
	ExpiresAt int64  `json:"expires_at"`
}

func (h *Handler) checkoutCancelReturnURL(baseURL string) func(commerce.Order, time.Time) string {
	return func(order commerce.Order, now time.Time) string {
		if strings.TrimSpace(h.customerSessionSecret) == "" {
			return ""
		}
		token, err := signedtoken.Encode(checkoutCancelReturnPayload{
			Version:   customerSignedValueVersion,
			OrderID:   order.ID,
			ExpiresAt: now.UTC().Add(checkoutCancelReturnTTL).Unix(),
		}, h.customerSessionSecret, checkoutCancelReturnPurpose)
		if err != nil {
			logAccountError("checkout cancel URL: mint token", err)
			return ""
		}
		location := strings.TrimRight(baseURL, "/") + checkoutCancelURLPath
		separator := "?"
		if strings.Contains(location, "?") {
			separator = "&"
		}
		return location + separator + checkoutCancelTokenParam + "=" + url.QueryEscape(token)
	}
}

func (h *Handler) validCheckoutCancelReturn(request events.APIGatewayV2HTTPRequest, orderID string) bool {
	token := strings.TrimSpace(request.QueryStringParameters[checkoutCancelTokenParam])
	if token == "" {
		return false
	}
	var payload checkoutCancelReturnPayload
	if !signedtoken.Decode(token, h.customerSessionSecret, checkoutCancelReturnPurpose, &payload) {
		return false
	}
	if payload.Version != customerSignedValueVersion || payload.OrderID != orderID {
		return false
	}
	return h.currentTime().UTC().Before(time.Unix(payload.ExpiresAt, 0))
}

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

// handleCheckoutPage renders the real checkout: the signed-in layout with
// address selection, or the guest layout with email + address entry. The
// empty/unavailable-cart guard keeps its long-standing 303 /cart behavior and
// runs before any auth/render branch so an empty-cart visit never bounces
// through sign-in.
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
	if state.signedIn {
		session, customer, signedIn, clearingCookies := h.customerSession(ctx, request)
		if !signedIn {
			// Race: the session died between the cart read and this resolution.
			return accountSeeOther(signInLocationForReturnTo("/checkout"), pageCheckout, clearingCookies)
		}
		return h.renderCheckoutPage(ctx, request, session, customer, state, checkoutRenderState{
			Canceled: request.QueryStringParameters["canceled"] == "1",
		}, http.StatusOK)
	}
	if state.sessionErr != nil {
		// A presented session cookie failed on a transient store error: do NOT
		// show this (possibly signed-in) shopper the guest form against the
		// cookie mirror — fail like cart mutations do.
		logHandlerError(pageCheckout, method, httpapi.Path(request), state.sessionErr)
		return httpapi.HTMLResponse(http.StatusInternalServerError, "Internal server error", nil)
	}

	return h.renderGuestCheckoutPage(ctx, request, state, checkoutRenderState{
		Canceled: request.QueryStringParameters["canceled"] == "1",
	}, "", http.StatusOK)
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
	GuestEmail        string            // guest re-render preservation
	GuestAddressForm  addressFormData   // guest re-render preservation
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

// renderGuestCheckoutPage renders the account-less checkout layout: contact
// email + shipping address entry on one form, protected by the guest
// double-submit CSRF pair. Re-renders after a failed POST pass the submitted
// token back via guestToken instead of minting (the signInPageResponse shape);
// guestToken == "" mints a fresh token + cookie.
func (h *Handler) renderGuestCheckoutPage(ctx context.Context, request events.APIGatewayV2HTTPRequest, state requestCart, render checkoutRenderState, guestToken string, statusCode int) events.APIGatewayV2HTTPResponse {
	cookies := append([]string(nil), state.cookies...)
	if guestToken == "" {
		minted, cookie, err := h.mintGuestCSRF()
		if err != nil {
			logAccountError("guest checkout: mint guest csrf", err)
			return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckout, nil)
		}
		guestToken = minted
		cookies = append(cookies, cookie)
	}

	subtotal := subtotalCents(state.lines)
	vm := checkoutPageViewModel{
		Metadata:        checkoutMetadata(),
		Breadcrumbs:     checkoutBreadcrumbs(),
		HeaderCartLabel: cartNavigationLabel(state.cart.TotalItemCount()),
		CSRFToken:       guestToken,
		Lines:           h.checkoutLineViews(state.lines, render.LineNotices),
		Subtotal:        formatPrice(subtotal),
		Total:           formatPrice(subtotal),
		Canceled:        render.Canceled,
		ErrorMessage:    render.ErrorMessage,
		Guest:           true,
		GuestEmail:      render.GuestEmail,
		AddressForm:     render.GuestAddressForm,
	}

	var body bytes.Buffer
	if err := checkoutPage(vm).Render(ctx, &body); err != nil {
		logAccountError("guest checkout: render", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckout, nil)
	}

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
// on-site fake-pay page in demo mode). Anonymous shoppers take the guest
// branch instead of a sign-in redirect.
func (h *Handler) handlePlaceOrder(ctx context.Context, request events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	session, customer, signedIn, clearingCookies := h.customerSession(ctx, request)
	if !signedIn {
		return h.handleGuestPlaceOrder(ctx, request, clearingCookies)
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

// handleGuestPlaceOrder is the account-less place-order branch (§7.2): cookie
// cart authority, guest CSRF, email + address entry, the signed guest-order
// pointer cookie for idempotent re-entry, and the paid-pointer resolution that
// keeps a webhook-finalized off-site payment from being re-charged.
func (h *Handler) handleGuestPlaceOrder(ctx context.Context, request events.APIGatewayV2HTTPRequest, clearingCookies []string) events.APIGatewayV2HTTPResponse {
	method := httpapi.Method(request)
	if h.checkout == nil || h.commerce == nil {
		logAccountError("guest place order", errors.New("checkout service not configured"))
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckoutPlaceOrder, nil)
	}

	state, err := h.cartStateFromRequest(ctx, request)
	if err != nil {
		logAccountError("guest place order: load cart", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckoutPlaceOrder, nil)
	}
	if state.sessionErr != nil {
		// Never place a guest order for a shopper whose real cart authority is
		// a server CART row we couldn't read.
		logHandlerError(pageCheckoutPlaceOrder, method, httpapi.Path(request), state.sessionErr)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckoutPlaceOrder, nil)
	}
	if state.signedIn {
		// Impossible (we only got here when the session didn't resolve), but
		// guard defensively: the signed-in flow owns server carts.
		logAccountError("guest place order", errors.New("signed-in cart state on the guest branch"))
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckoutPlaceOrder, nil)
	}
	if response, redirected := checkoutCartGuard(state, method); redirected {
		return response
	}

	form, err := httpapi.FormValues(request)
	if err != nil {
		return accountHTMLResponse(http.StatusBadRequest, "Bad request", pageCheckoutPlaceOrder, nil)
	}
	render := checkoutRenderState{
		GuestEmail: strings.TrimSpace(form.Get("email")),
	}
	formData, address, validAddress := parseAddressForm(form)
	render.GuestAddressForm = formData

	if !h.validGuestCSRF(request) {
		render.ErrorMessage = expiredFormError
		return h.renderGuestCheckoutPage(ctx, request, state, render, "", http.StatusForbidden)
	}
	guestToken := form.Get(guestCSRFFieldName)
	email := render.GuestEmail
	if !validCustomerEmail(email) {
		render.ErrorMessage = invalidEmailError
		return h.renderGuestCheckoutPage(ctx, request, state, render, guestToken, http.StatusBadRequest)
	}
	if !validAddress {
		render.ErrorMessage = invalidAddressError
		return h.renderGuestCheckoutPage(ctx, request, state, render, guestToken, http.StatusBadRequest)
	}

	switch decision := h.resolveAddressValidation(ctx, form, address); decision.kind {
	case addressReject:
		render.ErrorMessage = unverifiableAddressError
		return h.renderGuestCheckoutPage(ctx, request, state, render, guestToken, http.StatusBadRequest)
	case addressConfirm:
		render.GuestAddressForm.Confirming = true
		render.GuestAddressForm.Suggestion = decision.suggestion
		return h.renderGuestCheckoutPage(ctx, request, state, render, guestToken, http.StatusOK)
	default:
		address = decision.address
	}

	pendingID, pendingFingerprint, _ := h.guestOrderPointer(request)

	orderAddress := orderAddressFromAddress(address)
	addressID := checkout.GuestAddressID(orderAddress, email)
	lines := h.orderLinesFromCartViews(state.lines)

	if pendingID != "" {
		pending, found, err := h.commerce.GetOrder(ctx, pendingID)
		if err != nil {
			logAccountError("guest place order: load pointer order", err)
			return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckoutPlaceOrder, nil)
		}
		if found && pending.CustomerID == "" && pending.StripeCheckoutSessionID != "" &&
			orderStatusPaidOrLater(pending.Status) &&
			pending.CartFingerprint == h.checkout.Fingerprint(lines, addressID) {
			// This browser already paid for exactly this cart+address+email but
			// never reached confirm (the webhook finalized the order off-site).
			// PlaceOrder cannot resume it: resumePendingOrder only resumes
			// orders still pending_payment, and a paid order falls through both
			// of its branches, so re-placing would create a DUPLICATE order and
			// a second charge for the same still-populated cookie cart. Route
			// through confirm instead: the replay finalize is a no-op for paid
			// orders, and the confirm success branch clears the cart cookie +
			// pointer and mints the access link (§7.3).
			cookies := append(append([]string(nil), state.cookies...), clearingCookies...)
			return accountSeeOther("/checkout/confirm?session_id="+url.QueryEscape(pending.StripeCheckoutSessionID),
				pageCheckoutPlaceOrder, cookies)
		}
		// A fingerprint mismatch (new intent) and non-paid pointers fall
		// through deliberately: PlaceOrder's own re-entry handles pending
		// resume and stale-pointer cancel.
	}

	redirectURL, order, err := h.checkout.PlaceOrder(ctx, checkout.PlaceOrderInput{
		Guest:     true,
		Email:     email,
		AddressID: addressID,
		Address:   orderAddress,
		Lines:     lines,
		Cart:      commerce.CartRecord{PendingOrderID: pendingID, PendingFingerprint: pendingFingerprint},
	})
	if err != nil {
		var insufficient catalog.InsufficientStockError
		if errors.As(err, &insufficient) {
			return h.renderGuestInsufficientStockCheckout(ctx, request, render, guestToken, insufficient)
		}
		if errors.Is(err, checkout.ErrPaymentSessionInFlight) {
			logAccountError("guest place order: previous payment in flight", err)
			render.ErrorMessage = checkoutPaymentInFlightError
			return h.renderGuestCheckoutPage(ctx, request, state, render, guestToken, http.StatusConflict)
		}
		// Version-conflict exhaustion and provider failures are transient;
		// PlaceOrder already compensated (released stock, canceled the order).
		logAccountError("guest place order", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckoutPlaceOrder, nil)
	}

	// order.CartFingerprint (not a recompute) so resumed orders rewrite the
	// same pointer. A mint failure proceeds without the cookie: the pointer is
	// an idempotency optimization, never a correctness requirement.
	cookies := append(append([]string(nil), state.cookies...), clearingCookies...)
	pointerCookie, err := h.mintGuestOrderPointerCookie(order.ID, order.CartFingerprint)
	if err != nil {
		logAccountError("guest place order: mint pointer cookie", err)
	} else {
		cookies = append(cookies, pointerCookie)
	}
	return accountSeeOther(redirectURL, pageCheckoutPlaceOrder, cookies)
}

// renderGuestInsufficientStockCheckout mirrors renderInsufficientStockCheckout
// for the cookie cart: re-resolving via cartStateFromRequest re-clamps the
// anonymous cart against live stock and rewrites the repaired tgs_cart cookie.
func (h *Handler) renderGuestInsufficientStockCheckout(ctx context.Context, request events.APIGatewayV2HTTPRequest, render checkoutRenderState, guestToken string, insufficient catalog.InsufficientStockError) events.APIGatewayV2HTTPResponse {
	state, err := h.cartStateFromRequest(ctx, request)
	if err != nil {
		logAccountError("guest place order: reload cart after stock shortage", err)
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

	render.ErrorMessage = checkoutStockChangedError
	render.LineNotices = notices
	return h.renderGuestCheckoutPage(ctx, request, state, render, guestToken, http.StatusConflict)
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
// the provider server-side, the order authorization is checked, and only a
// provider-confirmed paid status finalizes the order. Authorization: guest
// orders (CustomerID == "") are authorized by possession of the provider
// session id — server-verified, unguessable for Stripe cs_… ids (the fake
// provider's derived ids are an accepted demo/CI-only weakening); customer
// orders keep the sign-in + ownership 404 gate.
func (h *Handler) handleCheckoutConfirm(ctx context.Context, request events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	method := httpapi.Method(request)
	_, customer, signedIn, clearingCookies := h.customerSession(ctx, request)
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
	if !found {
		return h.accountNotFoundResponse(ctx, request, pageCheckoutConfirm)
	}
	guest := order.CustomerID == ""
	switch {
	case guest:
		// Guest order: possession of the (server-verified) session id is the
		// authorization. The page reveals nothing the payer does not already
		// have.
	case signedIn && order.CustomerID == customer.ID:
		// Existing owner path.
	case !signedIn:
		return accountSeeOther(signInLocationForReturnTo(confirmReturnTo(request)), pageCheckoutConfirm, clearingCookies)
	default:
		// Not-found for foreign orders too: ownership must not leak existence.
		return h.accountNotFoundResponse(ctx, request, pageCheckoutConfirm)
	}
	// Dead/expired session cookies presented on the Stripe→confirm hop ride
	// along on the guest renders so they are cleared instead of re-presented
	// forever; clearingCookies is definitionally empty when signed in.
	var rideAlongCookies []string
	if guest {
		rideAlongCookies = clearingCookies
	}

	if paymentSession.PaymentStatus != checkoutPaymentStatusPaid {
		return h.renderCheckoutProcessingPage(ctx, request, order.ID, rideAlongCookies)
	}

	finalized, err := h.checkout.FinalizePayment(ctx, order.ID, paymentSession)
	if err != nil {
		if errors.Is(err, checkout.ErrOrderNotFinalizable) {
			// The payment landed after the order reached a terminal status
			// (the paid_after_terminal webhook condition): the customer was
			// charged, so never show them an error page — acknowledge the
			// payment; the refund is issued automatically by the webhook
			// handler and the page says so.
			logAccountError("checkout confirm: payment received for terminal order", err)
			return h.renderCheckoutPaymentReceivedPage(ctx, request, order.ID, rideAlongCookies)
		}
		if !orderStatusPaidOrLater(finalized.Status) {
			logAccountError("checkout confirm: finalize payment", err)
			return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckoutConfirm, nil)
		}
		// The order is paid; only the idempotent cart cleanup failed. The
		// webhook replay re-attempts it, so the shopper still gets success.
		logAccountError("checkout confirm: cart cleanup degraded", err)
	}

	if guest {
		// Access expiry is anchored to PaidAt, NOT to this visit: the cs_…
		// URL lives in the shopper's browser history forever, so an unbounded
		// re-mint here would make every confirm replay extend access by 30
		// days.
		expiresAt := finalized.PaidAt.Add(guestOrderAccessTTL)
		if finalized.PaidAt.IsZero() {
			// Defensive; finalize always stamps PaidAt.
			expiresAt = h.currentTime().UTC().Add(guestOrderAccessTTL)
		}
		location := "/orders/" + order.ID + "?placed=1"
		if h.currentTime().UTC().Before(expiresAt) {
			token, err := h.mintGuestOrderAccessToken(order.ID, expiresAt)
			if err != nil {
				// Keep the tokenless location — the page itself will 303 to
				// sign-in, but the payment is safe; never fail a paid order
				// over a token mint.
				logAccountError("checkout confirm: mint guest order access token", err)
			} else {
				location = guestOrderAccessPath(order.ID, token) + "&placed=1"
			}
		}
		// Past the window the redirect stays tokenless by design (the access
		// link is dead; the shopper's record is the Stripe receipt / support).
		cookies := append([]string(nil), clearingCookies...)
		if ptrOrder, _, ok := h.guestOrderPointer(request); ok && ptrOrder == order.ID {
			// Only clear a pointer that names THIS order: a second tab may
			// have started a newer checkout whose pointer must survive.
			cookies = append(cookies, clearCartCookie(request))
			cookies = append(cookies, clearGuestOrderCookie().String())
		}
		response := httpapi.SeeOther(location, cookies, seoHeadersForRoute(pageCheckoutConfirm))
		if method == http.MethodHead {
			response.Body = ""
		}
		return response
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

func (h *Handler) renderCheckoutProcessingPage(ctx context.Context, request events.APIGatewayV2HTTPRequest, orderID string, cookies []string) events.APIGatewayV2HTTPResponse {
	var body bytes.Buffer
	if err := checkoutProcessingPage(checkoutProcessingPageData{
		Metadata:        checkoutConfirmMetadata(),
		HeaderCartLabel: h.cartNavigation(request),
		OrderID:         orderID,
	}).Render(ctx, &body); err != nil {
		logAccountError("checkout confirm: render processing page", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckoutConfirm, nil)
	}

	return accountHTMLResponse(http.StatusOK, maybeEmptyBody(httpapi.Method(request), body.String()), pageCheckoutConfirm, cookies)
}

// renderCheckoutPaymentReceivedPage tells a charged customer whose order hit a
// terminal status (canceled/expired before the payment settled) that their
// payment was received and a refund is being issued automatically — never a
// 500.
func (h *Handler) renderCheckoutPaymentReceivedPage(ctx context.Context, request events.APIGatewayV2HTTPRequest, orderID string, cookies []string) events.APIGatewayV2HTTPResponse {
	var body bytes.Buffer
	if err := checkoutPaymentReceivedPage(checkoutProcessingPageData{
		Metadata:        checkoutConfirmMetadata(),
		HeaderCartLabel: h.cartNavigation(request),
		OrderID:         orderID,
	}).Render(ctx, &body); err != nil {
		logAccountError("checkout confirm: render payment received page", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckoutConfirm, nil)
	}

	return accountHTMLResponse(http.StatusOK, maybeEmptyBody(httpapi.Method(request), body.String()), pageCheckoutConfirm, cookies)
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
		return h.handleGuestFakePay(ctx, request, fakeProvider, clearingCookies)
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

// handleGuestFakePay drives the demo payment page for a guest checkout. The
// ownership rule is stricter than confirm's: the guest-order pointer cookie
// must name the session's order — free in a real browser flow because the
// cookie was just set by place-order, and the fake provider never runs in
// production.
func (h *Handler) handleGuestFakePay(ctx context.Context, request events.APIGatewayV2HTTPRequest, fakeProvider *payments.FakeProvider, clearingCookies []string) events.APIGatewayV2HTTPResponse {
	if httpapi.Method(request) == http.MethodPost {
		if !h.validGuestCSRF(request) {
			return accountHTMLResponse(http.StatusForbidden, "Forbidden", pageCheckoutFakePay, nil)
		}
		form, err := httpapi.FormValues(request)
		if err != nil {
			return accountHTMLResponse(http.StatusBadRequest, "Bad request", pageCheckoutFakePay, nil)
		}
		paymentSession, owned := h.ownedGuestFakePaySession(ctx, request, fakeProvider, strings.TrimSpace(form.Get("session_id")))
		if !owned {
			return h.accountNotFoundResponse(ctx, request, pageCheckoutFakePay)
		}
		switch form.Get("action") {
		case "pay":
			// Guests never save a demo card (no provider customer).
			redirectURL, markErr := fakeProvider.MarkSessionPaid(paymentSession.ID, false)
			if markErr != nil {
				return h.accountNotFoundResponse(ctx, request, pageCheckoutFakePay)
			}
			return accountSeeOther(redirectURL, pageCheckoutFakePay, clearingCookies)
		case "cancel":
			_, cancelURL, found := fakeProvider.SessionRedirectURLs(paymentSession.ID)
			if !found {
				return h.accountNotFoundResponse(ctx, request, pageCheckoutFakePay)
			}
			return accountSeeOther(cancelURL, pageCheckoutFakePay, clearingCookies)
		}
		return accountHTMLResponse(http.StatusBadRequest, "Bad request", pageCheckoutFakePay, nil)
	}

	sessionID := strings.TrimSpace(request.QueryStringParameters["session_id"])
	paymentSession, owned := h.ownedGuestFakePaySession(ctx, request, fakeProvider, sessionID)
	if !owned {
		return h.accountNotFoundResponse(ctx, request, pageCheckoutFakePay)
	}
	csrfToken, csrfCookie, err := h.mintGuestCSRF()
	if err != nil {
		logAccountError("guest fake pay: mint guest csrf", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckoutFakePay, nil)
	}

	var body bytes.Buffer
	if err := fakePayPage(fakePayPageData{
		Metadata:        fakePayMetadata(),
		HeaderCartLabel: h.cartNavigation(request),
		CSRFToken:       csrfToken,
		SessionID:       paymentSession.ID,
		Total:           formatPrice(paymentSession.AmountTotalCents),
		Guest:           true,
	}).Render(ctx, &body); err != nil {
		logAccountError("guest fake pay: render", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageCheckoutFakePay, nil)
	}

	cookies := append(append([]string(nil), clearingCookies...), csrfCookie)
	return accountHTMLResponse(http.StatusOK, maybeEmptyBody(httpapi.Method(request), body.String()), pageCheckoutFakePay, cookies)
}

// ownedGuestFakePaySession loads a fake session and checks guest ownership:
// payment-mode sessions only (setup sessions belong to signed-in flows), the
// order must be a guest order, and the pointer cookie must name it. Not owned
// always answers 404, never 403.
func (h *Handler) ownedGuestFakePaySession(ctx context.Context, request events.APIGatewayV2HTTPRequest, fakeProvider *payments.FakeProvider, sessionID string) (payments.Session, bool) {
	if sessionID == "" {
		return payments.Session{}, false
	}
	paymentSession, err := fakeProvider.GetSession(ctx, sessionID)
	if err != nil {
		return payments.Session{}, false
	}
	if paymentSession.Mode == checkoutSessionModeSetup {
		return payments.Session{}, false
	}
	if h.commerce == nil || paymentSession.OrderID == "" {
		return payments.Session{}, false
	}
	order, found, err := h.commerce.GetOrder(ctx, paymentSession.OrderID)
	if err != nil || !found || order.CustomerID != "" {
		return payments.Session{}, false
	}
	ptrOrderID, _, ok := h.guestOrderPointer(request)
	if !ok || ptrOrderID != order.ID {
		return payments.Session{}, false
	}

	return paymentSession, true
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

// ordersCursorPayload is the signed ?after token for /orders paging. It
// carries only the order GSI sort-key components — both already user-visible
// (order IDs sit in /orders/<id> URLs; created_at is on the order page) — so
// the store can rebuild the ExclusiveStartKey without exposing raw DynamoDB
// keys. The payload is readable by anyone holding the token (the envelope
// signs, it does not encrypt): never add confidential fields here. The
// customer binding is in the MAC purpose (ordersCursorTokenPurpose), not in
// the payload.
type ordersCursorPayload struct {
	Version   int    `json:"version"`
	OrderID   string `json:"order_id"`
	CreatedAt string `json:"created_at"` // UTC RFC3339, second precision (formatCommerceTime parity)
}

// ordersCursorTokenPurpose scopes the cursor MAC to one customer by mixing
// the customer ID into the HMAC purpose; signedtoken.signPayload computes
// HMAC over purpose || 0x00 || payload, so the MAC input becomes
//
//	"tgs-orders-cursor" || 0x00 || customerID || 0x00 || payload
//
// A token minted for one account fails verification under every other
// account without the customer ID ever appearing in the URL. Customer IDs
// are fixed-length 26-char [a-z0-9] values (signedtoken.NewID via
// commerce ID minting), so the concatenation is unambiguous; no existing
// purpose string contains 0x00 and the bare customerOrdersCursorPurpose is
// never used directly, so this cannot collide with the session/CSRF purposes.
func ordersCursorTokenPurpose(customerID string) []byte {
	purpose := make([]byte, 0, len(customerOrdersCursorPurpose)+1+len(customerID))
	purpose = append(purpose, customerOrdersCursorPurpose...)
	purpose = append(purpose, 0)
	return append(purpose, customerID...)
}

// decodeOrdersCursor resolves ?after into a store cursor. Every invalid input
// — bad signature, wrong purpose, token minted for another customer,
// malformed fields — yields the zero cursor, i.e. page 1. Failing open to the
// customer's own first page is safe: the listing is always scoped to the
// session customer.
func (h *Handler) decodeOrdersCursor(after string, customerID string) commerce.OrderCursor {
	if after == "" {
		return commerce.OrderCursor{}
	}
	var payload ordersCursorPayload
	if !signedtoken.Decode(after, h.customerSessionSecret, ordersCursorTokenPurpose(customerID), &payload) {
		return commerce.OrderCursor{}
	}
	if payload.Version != customerSignedValueVersion || !accountIDPattern.MatchString(payload.OrderID) {
		return commerce.OrderCursor{}
	}
	createdAt, err := time.Parse(time.RFC3339, payload.CreatedAt)
	if err != nil {
		return commerce.OrderCursor{}
	}
	return commerce.OrderCursor{OrderID: payload.OrderID, CreatedAt: createdAt}
}

func (h *Handler) encodeOrdersCursor(cursor commerce.OrderCursor, customerID string) (string, error) {
	return signedtoken.Encode(ordersCursorPayload{
		Version:   customerSignedValueVersion,
		OrderID:   cursor.OrderID,
		CreatedAt: cursor.CreatedAt.UTC().Format(time.RFC3339),
	}, h.customerSessionSecret, ordersCursorTokenPurpose(customerID))
}

// handleOrdersPage renders the order history newest-first with true
// store-level cursor paging: ?after carries a signed, customer-bound cursor
// token; anything invalid silently restarts at the first page.
func (h *Handler) handleOrdersPage(ctx context.Context, request events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	_, customer, signedIn, clearingCookies := h.customerSession(ctx, request)
	if !signedIn {
		return accountSeeOther(signInLocationForReturnTo("/orders"), pageOrders, clearingCookies)
	}

	cursor := h.decodeOrdersCursor(request.QueryStringParameters["after"], customer.ID)
	page, err := h.commerce.ListOrdersByCustomer(ctx, customer.ID, ordersPageSize, cursor)
	if err != nil {
		logAccountError("orders: list", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageOrders, nil)
	}

	nextCursor := ""
	if !page.NextCursor.IsZero() {
		token, err := h.encodeOrdersCursor(page.NextCursor, customer.ID)
		if err != nil {
			// Degrade: render the page without the older-orders link.
			logAccountError("orders: encode cursor", err)
		} else {
			nextCursor = url.QueryEscape(token)
		}
	}

	rows := make([]orderRowView, 0, len(page.Orders))
	for _, order := range page.Orders {
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
// Load-then-gate keeps responses indistinguishable: anonymous + bad/absent
// access token on ANY id (existing, foreign, or missing) → the same 303 to
// sign-in; signed-in + bad token → the same 404. A valid ?access token grants
// a read-only render of exactly one guest order — never a customer's.
func (h *Handler) handleOrderDetailPage(ctx context.Context, request events.APIGatewayV2HTTPRequest, orderID string) events.APIGatewayV2HTTPResponse {
	_, customer, signedIn, clearingCookies := h.customerSession(ctx, request)

	order, found, err := h.commerce.GetOrder(ctx, orderID)
	if err != nil {
		logAccountError("order detail: load", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageOrderDetail, nil)
	}
	ownerView := found && signedIn && order.CustomerID == customer.ID
	guestAccess := found && order.CustomerID == "" && h.validGuestOrderAccess(request, orderID)
	switch {
	case ownerView:
	case guestAccess:
	case !signedIn:
		return accountSeeOther(signInLocationForReturnTo("/orders/"+orderID), pageOrderDetail, clearingCookies)
	default:
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
	if !ownerView {
		// Tokenized guest view: degraded breadcrumbs (no /account, /orders),
		// the presented token echoed as the save-this-link self-URL (never
		// re-minted here — minting is the confirm branch's job alone, keeping
		// every token's expiry anchored to PaidAt+30d).
		vm.Guest = true
		vm.GuestAccessURL = guestOrderAccessPath(order.ID, request.QueryStringParameters[guestOrderAccessParam])
		vm.Breadcrumbs = guestOrderDetailBreadcrumbs(order.ID)
	}

	var body bytes.Buffer
	if err := orderDetailPage(vm).Render(ctx, &body); err != nil {
		logAccountError("order detail: render", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageOrderDetail, nil)
	}

	// clearingCookies is empty for the owner view (signed in) and carries
	// dead-session clears for the tokenized guest view.
	return accountHTMLResponse(http.StatusOK, maybeEmptyBody(httpapi.Method(request), body.String()), pageOrderDetail, clearingCookies)
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

// orderStatusPaidOrLater reports whether the order's payment was captured:
// the fulfillment chain and the refund family (a refunded order was paid
// first, so replays and pointer checks must treat it as paid).
func orderStatusPaidOrLater(status commerce.OrderStatus) bool {
	switch status {
	case commerce.OrderStatusPaid, commerce.OrderStatusShipped, commerce.OrderStatusDelivered,
		commerce.OrderStatusRefundPending, commerce.OrderStatusRefunded, commerce.OrderStatusRefundFailed:
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
