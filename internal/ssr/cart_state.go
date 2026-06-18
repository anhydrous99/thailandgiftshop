package ssr

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/anhydrous99/thailandgiftshop/internal/appenv"
	"github.com/anhydrous99/thailandgiftshop/internal/cart"
	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/checkout"
	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/httpapi"
	"github.com/aws/aws-lambda-go/events"
)

type cartLineView struct {
	Product            catalog.Product
	VariantID          string
	VariantLabel       string
	Quantity           int
	StockLimit         int
	Available          bool
	UnavailableMessage string
}

// allowedCartMutationHosts is the set of hosts a cart-mutation POST may
// originate from. Add-to-cart submits from edge-cached pages that cannot carry
// a per-session CSRF token, so cart mutations are protected by an Origin /
// Referer host check instead.
var allowedCartMutationHosts = map[string]struct{}{
	"thailandgiftshop.com":     {},
	"www.thailandgiftshop.com": {},
	"localhost":                {},
	"127.0.0.1":                {},
}

// validCartMutationOrigin rejects cross-site cart mutations. It checks the
// Origin header (then Referer) host against the site's own hosts. A present
// but foreign host is rejected; absent headers are rejected in production.
func validCartMutationOrigin(request events.APIGatewayV2HTTPRequest) bool {
	if origin := strings.TrimSpace(httpapi.HeaderValue(request.Headers, "Origin")); origin != "" {
		return cartMutationHostAllowed(origin)
	}
	if referer := strings.TrimSpace(httpapi.HeaderValue(request.Headers, "Referer")); referer != "" {
		return cartMutationHostAllowed(referer)
	}
	return !appenv.IsProduction()
}

func cartMutationHostAllowed(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return false
	}
	_, ok := allowedCartMutationHosts[strings.ToLower(parsed.Hostname())]
	return ok
}

func (h *Handler) handleCartMutation(ctx context.Context, request events.APIGatewayV2HTTPRequest, route pageRoute) events.APIGatewayV2HTTPResponse {
	method := httpapi.Method(request)
	path := httpapi.Path(request)
	if !validCartMutationOrigin(request) {
		// Cross-site request forgery guard for the tokenless cart endpoints.
		return httpapi.HTMLResponse(http.StatusForbidden, "Forbidden", nil)
	}
	form, err := httpapi.FormValues(request)
	if err != nil {
		return httpapi.HTMLResponse(http.StatusBadRequest, "Bad request", nil)
	}

	state, err := h.cartStateFromRequest(ctx, request)
	if err != nil {
		logHandlerError(route.kind, method, path, err)
		return httpapi.HTMLResponse(http.StatusInternalServerError, "Internal server error", nil)
	}
	if state.sessionErr != nil {
		// A presented session cookie failed to resolve because of a transient
		// store error. Renders may degrade to the anonymous cookie cart, but
		// mutating it here would silently diverge from the authoritative
		// server cart and be discarded on the next signed-in render — fail
		// the write instead.
		logHandlerError(route.kind, method, path, state.sessionErr)
		return httpapi.HTMLResponse(http.StatusInternalServerError, "Internal server error", nil)
	}
	currentCart := state.cart

	// apply re-runs the mutation against a fresh base cart when a signed-in
	// write loses a version race and has to be re-applied.
	var apply func(cart.Cart) (cart.Cart, error)

	switch route.kind {
	case pageCartItems:
		quantity, ok := positiveFormQuantity(form)
		if !ok {
			return httpapi.HTMLResponse(http.StatusBadRequest, "Invalid quantity", nil)
		}
		slug := form.Get("slug")
		variantID := strings.TrimSpace(form.Get("variant_id"))
		product, found, err := h.cartProduct(ctx, slug)
		if err != nil {
			logHandlerError(route.kind, method, path, err)
			return httpapi.HTMLResponse(http.StatusInternalServerError, "Internal server error", nil)
		}
		if !found {
			return httpapi.HTMLResponse(http.StatusNotFound, "Not found", nil)
		}
		stockLimit, ok := availableStockForProduct(product, variantID, true)
		if !ok {
			if !product.UsesVariants() {
				return httpapi.HTMLResponse(http.StatusNotFound, "Not found", nil)
			}
			return httpapi.HTMLResponse(http.StatusBadRequest, "Select an available size", nil)
		}
		boundedQuantity := min(quantity, stockLimit, cart.MaxQuantity)
		apply = func(base cart.Cart) (cart.Cart, error) {
			return base.AddLine(product.Slug, variantID, boundedQuantity)
		}
	case pageCartQuantity:
		quantity, ok := positiveFormQuantity(form)
		if !ok {
			return httpapi.HTMLResponse(http.StatusBadRequest, "Invalid quantity", nil)
		}
		variantID := strings.TrimSpace(form.Get("variant_id"))
		product, found, err := h.cartProduct(ctx, route.slug)
		if err != nil {
			logHandlerError(route.kind, method, path, err)
			return httpapi.HTMLResponse(http.StatusInternalServerError, "Internal server error", nil)
		}
		if !found {
			return httpapi.HTMLResponse(http.StatusNotFound, "Not found", nil)
		}
		stockLimit, ok := availableStockForProduct(product, variantID, false)
		if !ok {
			if !product.UsesVariants() {
				return httpapi.HTMLResponse(http.StatusNotFound, "Not found", nil)
			}
			return httpapi.HTMLResponse(http.StatusBadRequest, "Bad request", nil)
		}
		boundedQuantity := min(quantity, stockLimit, cart.MaxQuantity)
		apply = func(base cart.Cart) (cart.Cart, error) {
			return base.SetLineQuantity(product.Slug, variantID, boundedQuantity)
		}
	case pageCartRemove:
		variantID := strings.TrimSpace(form.Get("variant_id"))
		product, found, err := h.cartProduct(ctx, route.slug)
		if err != nil {
			logHandlerError(route.kind, method, path, err)
			return httpapi.HTMLResponse(http.StatusInternalServerError, "Internal server error", nil)
		}
		if !found {
			return httpapi.HTMLResponse(http.StatusNotFound, "Not found", nil)
		}
		apply = func(base cart.Cart) (cart.Cart, error) {
			return base.RemoveLine(product.Slug, variantID)
		}
	case pageCartClear:
		apply = func(base cart.Cart) (cart.Cart, error) {
			return base.Clear(), nil
		}
	default:
		return httpapi.HTMLResponse(http.StatusNotFound, "Not found", nil)
	}

	mutatedCart, err := apply(currentCart)
	if err != nil {
		return cartMutationErrorResponse(route.kind, method, path, err)
	}
	mutatedCart, _, _, err = h.normalizeCart(ctx, mutatedCart)
	if err != nil {
		logHandlerError(route.kind, method, path, err)
		return httpapi.HTMLResponse(http.StatusInternalServerError, "Internal server error", nil)
	}

	if state.signedIn {
		mutatedCart, err = h.writeServerCart(ctx, state.record, mutatedCart, apply)
		if errors.Is(err, errCartWriteContention) {
			// Bounded retries lost every race; another writer owns the cart
			// right now. PRG to /cart, which re-reads the winning state and
			// re-syncs the mirror — never a 500 for a self-healing race.
			logHandlerWarn(route.kind, method, path, err)
			return httpapi.SeeOther("/cart", nil, nil)
		}
		if err != nil {
			return cartMutationErrorResponse(route.kind, method, path, err)
		}
		// The server cart is authoritative; the __Host-tgs_cart mirror is
		// best-effort and must never fail the mutation.
		return httpapi.SeeOther("/cart", []string{h.mirrorCartCookie(mutatedCart)}, nil)
	}

	cookieValue, err := h.cartResponseCookie(mutatedCart)
	if err != nil {
		logHandlerWarn(route.kind, method, path, err)
		return httpapi.HTMLResponse(http.StatusBadRequest, "Bad request", nil)
	}

	return httpapi.SeeOther("/cart", []string{cookieValue}, nil)
}

// cartWriteRetryAttempts bounds how many re-read + re-apply rounds a signed-in
// cart mutation runs after the initial version-conditional write conflicts.
const cartWriteRetryAttempts = 3

// errCartWriteContention reports that every bounded retry of a signed-in cart
// write lost a version race; the caller responds with a PRG redirect back to
// /cart so the shopper sees the winning state instead of a 500.
var errCartWriteContention = errors.New("server cart write contention: retries exhausted")

// writeServerCart persists a signed-in cart mutation through the
// version-conditional CART row: first write with the request's version, then
// on conflict re-read and re-apply the mutation a bounded number of times.
// The returned cart is what the __Host-tgs_cart mirror must reflect.
func (h *Handler) writeServerCart(ctx context.Context, record commerce.CartRecord, normalizedCart cart.Cart, apply func(cart.Cart) (cart.Cart, error)) (cart.Cart, error) {
	record.Lines = normalizedCart.Lines()
	if _, err := h.commerce.PutCart(ctx, record); err == nil {
		return normalizedCart, nil
	} else if !errors.Is(err, commerce.ErrVersionConflict) {
		return cart.Empty(), err
	}

	// Re-read and re-apply the mutation onto whatever won each race.
	for range cartWriteRetryAttempts {
		fresh, _, err := h.commerce.GetCart(ctx, record.CustomerID)
		if err != nil {
			return cart.Empty(), err
		}
		fresh.CustomerID = record.CustomerID
		base, err := cart.New(fresh.Lines)
		if err != nil {
			base = cart.Empty()
		}
		reapplied, err := apply(base)
		if err != nil {
			return cart.Empty(), err
		}
		normalizedCart, _, _, err = h.normalizeCart(ctx, reapplied)
		if err != nil {
			return cart.Empty(), err
		}
		fresh.Lines = normalizedCart.Lines()
		if _, err := h.commerce.PutCart(ctx, fresh); err == nil {
			return normalizedCart, nil
		} else if !errors.Is(err, commerce.ErrVersionConflict) {
			return cart.Empty(), err
		}
	}

	return cart.Empty(), errCartWriteContention
}

func cartMutationErrorResponse(kind pageKind, method string, path string, err error) events.APIGatewayV2HTTPResponse {
	if errors.Is(err, cart.ErrInvalidSlug) {
		return httpapi.HTMLResponse(http.StatusNotFound, "Not found", nil)
	}
	if errors.Is(err, cart.ErrInvalidQuantity) || errors.Is(err, cart.ErrLineItemLimit) || errors.Is(err, cart.ErrCookieTooLarge) {
		return httpapi.HTMLResponse(http.StatusBadRequest, "Bad request", nil)
	}

	logHandlerError(kind, method, path, err)
	return httpapi.HTMLResponse(http.StatusInternalServerError, "Internal server error", nil)
}

func (h *Handler) cartProduct(ctx context.Context, slug string) (catalog.Product, bool, error) {
	product, found, err := h.catalogStore.GetProductBySlug(ctx, slug)
	if err != nil || !found || product.Status != catalog.StatusActive {
		return catalog.Product{}, false, err
	}

	return product, true, nil
}

func availableStockForProduct(product catalog.Product, variantID string, requireVariantForVariantProducts bool) (int, bool) {
	if product.UsesVariants() {
		if requireVariantForVariantProducts && variantID == "" {
			return 0, false
		}
		stock, ok := product.AvailableStockForVariant(variantID)
		if !ok || stock <= 0 {
			return 0, false
		}
		return stock, true
	}
	if variantID != "" || product.StockQuantity <= 0 {
		return 0, false
	}

	return product.StockQuantity, true
}

type requestCart struct {
	cart     cart.Cart
	lines    []cartLineView
	cookies  []string
	signedIn bool
	record   commerce.CartRecord
	// adjusted reports that normalization changed the cart on this read
	// (out-of-stock lines dropped or quantities reduced), so the cart page
	// can tell the shopper their cart was auto-updated.
	adjusted bool
	// session and customer carry the resolved signed-in session and customer
	// (zero values when anonymous), so callers that already obtained the cart
	// state do not re-resolve the session with two more GetItem reads.
	session  commerce.Session
	customer commerce.Customer
	// sessionErr reports that a presented session cookie could not be
	// resolved because of a transient store error; the cart fell back to the
	// anonymous cookie. Renders tolerate the fallback, mutations must not.
	sessionErr error
}

func (h *Handler) cartStateFromRequest(ctx context.Context, request events.APIGatewayV2HTTPRequest) (requestCart, error) {
	// Signed-in carts live in the CART row; the __Host-tgs_cart cookie is only a
	// write-through mirror for the header label and the edge cache key, so
	// it is ignored as a source here and rewritten on the way out.
	session, customer, signedIn, sessionClearing, sessionErr := h.customerSessionWithStoreError(ctx, request)
	if signedIn {
		state, err := h.serverCartState(ctx, request, customer.ID)
		if err != nil {
			return requestCart{}, err
		}
		// Carry the already-resolved session/customer so checkout renders do
		// not resolve the session a second time.
		state.session = session
		state.customer = customer
		return state, nil
	}

	decodedCart := cart.Empty()
	needsClear := false
	fromMirror := false
	if cookieValue, found := cartCookieValue(request); found {
		decoded := cart.DecodeCookie(cookieValue, h.cartCookieSecret)
		decodedCart = decoded.Cart
		needsClear = decoded.NeedsClear
		fromMirror = decoded.Mirror
	}
	if checkoutCanceledReturn(request) {
		if orderID, _, ok := h.guestOrderPointer(request); ok && !h.validCheckoutCancelReturn(request, orderID) {
			lines, err := h.pendingCheckoutLines(ctx, decodedCart)
			if err != nil {
				return requestCart{}, err
			}
			return requestCart{cart: decodedCart, lines: lines, cookies: sessionClearing, sessionErr: sessionErr}, nil
		}
		if err := h.cancelGuestReturnedCheckout(ctx, request); err != nil {
			return requestCart{}, err
		}
	}

	normalizedCart, lines, changed, err := h.normalizeCart(ctx, decodedCart)
	if err != nil {
		return requestCart{}, err
	}
	// Dead session cookies ride along with the anonymous fallback so
	// cart-bearing routes clear them instead of re-validating them forever.
	state := requestCart{cart: normalizedCart, lines: lines, cookies: sessionClearing, sessionErr: sessionErr, adjusted: changed}
	if needsClear || (changed && normalizedCart.LineCount() == 0) {
		state.cookies = append(state.cookies, clearCartCookie())
		return state, nil
	}
	if changed {
		if fromMirror {
			// Rewriting a leftover mirror keeps its marking: it is still an
			// echo of old server-cart state, not anonymous shopping intent.
			state.cookies = append(state.cookies, h.mirrorCartCookie(normalizedCart))
			return state, nil
		}
		cookieValue, err := h.cartResponseCookie(normalizedCart)
		if err != nil {
			return requestCart{}, err
		}
		state.cookies = append(state.cookies, cookieValue)
		return state, nil
	}

	return state, nil
}

// serverCartState sources the cart from the customer's CART row, normalizes
// it against the live catalog, best-effort persists any normalization drift,
// and always re-emits the __Host-tgs_cart mirror so the cookie self-heals across
// devices.
func (h *Handler) serverCartState(ctx context.Context, request events.APIGatewayV2HTTPRequest, customerID string) (requestCart, error) {
	record, _, err := h.commerce.GetCart(ctx, customerID)
	if err != nil {
		return requestCart{}, err
	}
	record.CustomerID = customerID
	if checkoutCanceledReturn(request) && record.PendingOrderID != "" {
		if !h.validCheckoutCancelReturn(request, record.PendingOrderID) {
			serverCart, cartErr := cart.New(record.Lines)
			if cartErr != nil {
				logAccountError("server cart: invalid stored lines", cartErr)
				serverCart = cart.Empty()
			}
			lines, linesErr := h.pendingCheckoutLines(ctx, serverCart)
			if linesErr != nil {
				return requestCart{}, linesErr
			}
			return requestCart{
				cart:     serverCart,
				lines:    lines,
				cookies:  []string{h.mirrorCartCookie(serverCart)},
				signedIn: true,
				record:   record,
			}, nil
		}
		if err := h.cancelCustomerReturnedCheckout(ctx, request, customerID, record.PendingOrderID); err != nil {
			return requestCart{}, err
		}
		record, _, err = h.commerce.GetCart(ctx, customerID)
		if err != nil {
			return requestCart{}, err
		}
		record.CustomerID = customerID
	}

	serverCart, err := cart.New(record.Lines)
	if err != nil {
		logAccountError("server cart: invalid stored lines", err)
		serverCart = cart.Empty()
	}
	normalizedCart, lines, changed, err := h.normalizeCart(ctx, serverCart)
	if err != nil {
		return requestCart{}, err
	}
	if changed {
		repaired := record
		repaired.Lines = normalizedCart.Lines()
		if updated, err := h.commerce.PutCart(ctx, repaired); err == nil {
			record = updated
		} else if !errors.Is(err, commerce.ErrVersionConflict) {
			return requestCart{}, err
		}
		// A conflicting write means another request just updated the cart;
		// it owns the repair, and this render keeps its stale version.
	}

	return requestCart{
		cart:     normalizedCart,
		lines:    lines,
		cookies:  []string{h.mirrorCartCookie(normalizedCart)},
		signedIn: true,
		record:   record,
		adjusted: changed,
	}, nil
}

func checkoutCanceledReturn(request events.APIGatewayV2HTTPRequest) bool {
	return request.QueryStringParameters["canceled"] == "1"
}

func (h *Handler) cancelCustomerReturnedCheckout(ctx context.Context, request events.APIGatewayV2HTTPRequest, customerID string, orderID string) error {
	if orderID == "" {
		return nil
	}
	if !h.validCheckoutCancelReturn(request, orderID) {
		return nil
	}
	order, found, err := h.commerce.GetOrder(ctx, orderID)
	if err != nil || !found || order.CustomerID != customerID {
		return err
	}
	return h.cancelReturnedCheckout(ctx, order)
}

func (h *Handler) cancelGuestReturnedCheckout(ctx context.Context, request events.APIGatewayV2HTTPRequest) error {
	orderID, _, ok := h.guestOrderPointer(request)
	if !ok {
		return nil
	}
	if !h.validCheckoutCancelReturn(request, orderID) {
		return nil
	}
	order, found, err := h.commerce.GetOrder(ctx, orderID)
	if err != nil || !found || order.CustomerID != "" {
		return err
	}
	return h.cancelReturnedCheckout(ctx, order)
}

func (h *Handler) cancelReturnedCheckout(ctx context.Context, order commerce.Order) error {
	if h.checkout == nil || order.Status != commerce.OrderStatusPendingPayment {
		return nil
	}
	err := h.checkout.CancelPendingOrder(ctx, order)
	if errors.Is(err, checkout.ErrOrderPaidNotCanceled) || errors.Is(err, checkout.ErrPaymentSessionInFlight) {
		return nil
	}
	return err
}

const maxCartProductLookupConcurrency = 8

type cartProductLookupResult struct {
	product catalog.Product
	found   bool
}

func (h *Handler) cartProductsBySlug(ctx context.Context, lines []cart.Line) (map[string]cartProductLookupResult, error) {
	if len(lines) == 0 {
		return map[string]cartProductLookupResult{}, nil
	}

	seen := make(map[string]bool, len(lines))
	uniqueSlugs := make([]string, 0, len(lines))
	for _, line := range lines {
		if seen[line.Slug] {
			continue
		}
		seen[line.Slug] = true
		uniqueSlugs = append(uniqueSlugs, line.Slug)
	}
	if len(uniqueSlugs) == 0 {
		return map[string]cartProductLookupResult{}, nil
	}

	lookupCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type workerResult struct {
		slug    string
		product catalog.Product
		found   bool
		err     error
	}

	workerCount := min(maxCartProductLookupConcurrency, len(uniqueSlugs))
	jobs := make(chan string)
	results := make(chan workerResult, workerCount)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for slug := range jobs {
				product, found, err := h.catalogStore.GetProductBySlug(lookupCtx, slug)
				select {
				case results <- workerResult{slug: slug, product: product, found: found, err: err}:
				case <-lookupCtx.Done():
					return
				}
				if err != nil {
					return
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, slug := range uniqueSlugs {
			select {
			case jobs <- slug:
			case <-lookupCtx.Done():
				return
			}
		}
	}()

	go func() {
		workers.Wait()
		close(results)
	}()

	products := make(map[string]cartProductLookupResult, len(uniqueSlugs))
	var firstErr error
	for result := range results {
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
				cancel()
			}
			continue
		}
		products[result.slug] = cartProductLookupResult{product: result.product, found: result.found}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	if err := lookupCtx.Err(); err != nil {
		return nil, err
	}

	return products, nil
}

func (h *Handler) normalizeCart(ctx context.Context, currentCart cart.Cart) (cart.Cart, []cartLineView, bool, error) {
	cartLines := currentCart.Lines()
	productsBySlug, err := h.cartProductsBySlug(ctx, cartLines)
	if err != nil {
		return cart.Empty(), nil, false, err
	}

	normalizedCart := cart.Empty()
	lines := make([]cartLineView, 0, currentCart.LineCount())
	changed := false

	for _, line := range cartLines {
		lookup := productsBySlug[line.Slug]
		product := lookup.product
		found := lookup.found
		if !found || product.Status != catalog.StatusActive {
			changed = true
			continue
		}

		lineView := cartLineView{
			Product:    product,
			VariantID:  line.VariantID,
			Quantity:   line.Quantity,
			Available:  true,
			StockLimit: product.StockQuantity,
		}
		stockLimit := product.StockQuantity
		if product.UsesVariants() {
			variant, foundVariant := findProductVariant(product, line.VariantID)
			lineView.Available = false
			lineView.StockLimit = 0
			lineView.UnavailableMessage = "Selected size is unavailable. Remove it to continue."
			if foundVariant {
				lineView.VariantLabel = variant.Label
			}
			if foundVariant && variant.Status == catalog.StatusActive && variant.StockQuantity > 0 {
				lineView.Available = true
				lineView.StockLimit = min(variant.StockQuantity, cart.MaxQuantity)
				stockLimit = variant.StockQuantity
			}
		} else if line.VariantID != "" || product.StockQuantity <= 0 {
			changed = true
			continue
		}

		quantity := min(line.Quantity, stockLimit, cart.MaxQuantity)
		if !lineView.Available {
			quantity = min(line.Quantity, cart.MaxQuantity)
		}
		if quantity != line.Quantity {
			changed = true
		}
		var setErr error
		normalizedCart, setErr = normalizedCart.SetLineQuantity(product.Slug, line.VariantID, quantity)
		if setErr != nil {
			return cart.Empty(), nil, false, setErr
		}
		lineView.Quantity = quantity
		lines = append(lines, lineView)
	}

	return normalizedCart, lines, changed, nil
}

func (h *Handler) pendingCheckoutLines(ctx context.Context, currentCart cart.Cart) ([]cartLineView, error) {
	cartLines := currentCart.Lines()
	productsBySlug, err := h.cartProductsBySlug(ctx, cartLines)
	if err != nil {
		return nil, err
	}

	lines := make([]cartLineView, 0, currentCart.LineCount())
	for _, line := range cartLines {
		lookup := productsBySlug[line.Slug]
		product := lookup.product
		if !lookup.found || product.Status != catalog.StatusActive {
			continue
		}
		lineView := cartLineView{
			Product:    product,
			VariantID:  line.VariantID,
			Quantity:   line.Quantity,
			Available:  true,
			StockLimit: max(product.StockQuantity, line.Quantity),
		}
		if product.UsesVariants() {
			variant, foundVariant := findProductVariant(product, line.VariantID)
			if !foundVariant || variant.Status != catalog.StatusActive {
				lineView.Available = false
				lineView.UnavailableMessage = "Selected size is unavailable. Remove it to continue."
			} else {
				lineView.VariantLabel = variant.Label
				lineView.StockLimit = max(variant.StockQuantity, line.Quantity)
			}
		}
		lines = append(lines, lineView)
	}
	return lines, nil
}

// cartNavigation derives the header cart label from the signed cookie alone.
// It never queries the catalog and never sets cookies, so catalog GETs stay
// cacheable at the edge. The count can briefly overstate items that became
// unavailable; /cart, /checkout, and cart mutations still fully normalize the
// cart and repair or clear the cookie.
func (h *Handler) cartNavigation(request events.APIGatewayV2HTTPRequest) string {
	cookieValue, found := cartCookieValue(request)
	if !found {
		return cartNavigationLabel(0)
	}

	decoded := cart.DecodeCookie(cookieValue, h.cartCookieSecret)
	return cartNavigationLabel(decoded.Cart.TotalItemCount())
}

func cartNavigationLabel(itemCount int) string {
	if itemCount <= 0 {
		return "Cart"
	}

	return "Cart (" + strconv.Itoa(itemCount) + ")"
}

func positiveFormQuantity(form url.Values) (int, bool) {
	quantity, err := strconv.Atoi(strings.TrimSpace(form.Get("quantity")))
	if err != nil || quantity <= 0 {
		return 0, false
	}

	return quantity, true
}

func cartCookieValue(request events.APIGatewayV2HTTPRequest) (string, bool) {
	return httpapi.CookieValue(request, cart.CookieName)
}

func (h *Handler) cartResponseCookie(currentCart cart.Cart) (string, error) {
	if currentCart.LineCount() == 0 {
		return clearCartCookie(), nil
	}
	encoded, err := cart.EncodeCookie(currentCart, h.cartCookieSecret)
	if err != nil {
		return "", err
	}

	return cartCookieString(encoded), nil
}

// mirrorCartCookie encodes the __Host-tgs_cart write-through mirror of a signed-in
// customer's server cart. The server cart is authoritative, so the mirror is
// best-effort display state and this never fails: an oversized cart degrades
// to a truncated mirror (the header label may briefly undercount; /cart
// renders re-sync it) and any other encode failure degrades to clearing the
// cookie, so an oversized server cart can never brick renders, mutations,
// checkout, or sign-in.
func (h *Handler) mirrorCartCookie(currentCart cart.Cart) string {
	if currentCart.LineCount() == 0 {
		return clearCartCookie()
	}
	encoded, err := cart.EncodeMirrorCookieTruncated(currentCart, h.cartCookieSecret)
	if err != nil {
		logAccountError("cart mirror: encode", err)
		return clearCartCookie()
	}

	return cartCookieString(encoded)
}

// The __Host- prefix requires Secure, so the cart cookie always sets it. As
// with the customer and admin __Host- cookies, browsers still accept it over
// http://127.0.0.1, which local dev and the Playwright suite rely on.
func cartCookieString(encoded string) string {
	return (&http.Cookie{
		Name:     cart.CookieName,
		Value:    encoded,
		Path:     "/",
		MaxAge:   cart.CookieMaxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}).String()
}

func clearCartCookie() string {
	return (&http.Cookie{
		Name:     cart.CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}).String()
}

func lineTotalCents(line cartLineView) int {
	return line.Product.PriceCents * line.Quantity
}

func subtotalCents(lines []cartLineView) int {
	total := 0
	for _, line := range lines {
		total += lineTotalCents(line)
	}

	return total
}

func cartQuantityLimit(product catalog.Product) int {
	return min(product.TotalAvailableStock(), cart.MaxQuantity)
}

func cartLineQuantityLimit(line cartLineView) int {
	if !line.Available || line.StockLimit <= 0 {
		return 0
	}
	return min(line.StockLimit, cart.MaxQuantity)
}

func hasUnavailableCartLines(lines []cartLineView) bool {
	for _, line := range lines {
		if !line.Available {
			return true
		}
	}
	return false
}

func findProductVariant(product catalog.Product, variantID string) (catalog.ProductVariant, bool) {
	for _, variant := range product.Variants {
		if variant.ID == variantID {
			return variant, true
		}
	}
	return catalog.ProductVariant{}, false
}
