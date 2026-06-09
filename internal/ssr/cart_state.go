package ssr

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/anhydrous99/thailandgiftshop/internal/cart"
	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
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

func (h *Handler) handleCartMutation(ctx context.Context, request events.APIGatewayV2HTTPRequest, route pageRoute) events.APIGatewayV2HTTPResponse {
	method := httpapi.Method(request)
	path := httpapi.Path(request)
	form, err := httpapi.FormValues(request)
	if err != nil {
		return httpapi.HTMLResponse(http.StatusBadRequest, "Bad request", nil)
	}

	currentCart, _, _, err := h.cartFromRequest(ctx, request)
	if err != nil {
		logHandlerError(route.kind, method, path, err)
		return httpapi.HTMLResponse(http.StatusInternalServerError, "Internal server error", nil)
	}
	mutatedCart := currentCart

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
		mutatedCart, err = currentCart.AddLine(product.Slug, variantID, min(quantity, stockLimit, cart.MaxQuantity))
		if err != nil {
			return cartMutationErrorResponse(route.kind, method, path, err)
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
		mutatedCart, err = currentCart.SetLineQuantity(product.Slug, variantID, min(quantity, stockLimit, cart.MaxQuantity))
		if err != nil {
			return cartMutationErrorResponse(route.kind, method, path, err)
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
		mutatedCart, err = currentCart.RemoveLine(product.Slug, variantID)
		if err != nil {
			return cartMutationErrorResponse(route.kind, method, path, err)
		}
	case pageCartClear:
		mutatedCart = currentCart.Clear()
	}

	mutatedCart, _, _, err = h.normalizeCart(ctx, mutatedCart)
	if err != nil {
		logHandlerError(route.kind, method, path, err)
		return httpapi.HTMLResponse(http.StatusInternalServerError, "Internal server error", nil)
	}
	cookieValue, err := h.cartResponseCookie(mutatedCart, request)
	if err != nil {
		logHandlerWarn(route.kind, method, path, err)
		return httpapi.HTMLResponse(http.StatusBadRequest, "Bad request", nil)
	}

	return httpapi.SeeOther("/cart", []string{cookieValue}, nil)
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
	cart    cart.Cart
	lines   []cartLineView
	cookies []string
}

func (h *Handler) cartFromRequest(ctx context.Context, request events.APIGatewayV2HTTPRequest) (cart.Cart, []cartLineView, []string, error) {
	currentCart, err := h.cartStateFromRequest(ctx, request)
	if err != nil {
		return cart.Empty(), nil, nil, err
	}
	return currentCart.cart, currentCart.lines, currentCart.cookies, nil
}

func (h *Handler) cartStateFromRequest(ctx context.Context, request events.APIGatewayV2HTTPRequest) (requestCart, error) {
	decodedCart := cart.Empty()
	needsClear := false
	if cookieValue, found := cartCookieValue(request); found {
		decoded := cart.DecodeCookie(cookieValue, h.cartCookieSecret)
		decodedCart = decoded.Cart
		needsClear = decoded.NeedsClear
	}

	normalizedCart, lines, changed, err := h.normalizeCart(ctx, decodedCart)
	if err != nil {
		return requestCart{}, err
	}
	if needsClear || (changed && normalizedCart.LineCount() == 0) {
		return requestCart{cart: normalizedCart, lines: lines, cookies: []string{clearCartCookie(request)}}, nil
	}
	if changed {
		cookieValue, err := h.cartResponseCookie(normalizedCart, request)
		if err != nil {
			return requestCart{}, err
		}
		return requestCart{cart: normalizedCart, lines: lines, cookies: []string{cookieValue}}, nil
	}

	return requestCart{cart: normalizedCart, lines: lines}, nil
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

func (h *Handler) cartResponseCookie(currentCart cart.Cart, request events.APIGatewayV2HTTPRequest) (string, error) {
	if currentCart.LineCount() == 0 {
		return clearCartCookie(request), nil
	}
	encoded, err := cart.EncodeCookie(currentCart, h.cartCookieSecret)
	if err != nil {
		return "", err
	}

	return (&http.Cookie{
		Name:     cart.CookieName,
		Value:    encoded,
		Path:     "/",
		MaxAge:   cart.CookieMaxAge,
		HttpOnly: true,
		Secure:   isHTTPSRequest(request),
		SameSite: http.SameSiteLaxMode,
	}).String(), nil
}

func clearCartCookie(request events.APIGatewayV2HTTPRequest) string {
	return (&http.Cookie{
		Name:     cart.CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isHTTPSRequest(request),
		SameSite: http.SameSiteLaxMode,
	}).String()
}

func isHTTPSRequest(request events.APIGatewayV2HTTPRequest) bool {
	for _, headerName := range []string{"cloudfront-forwarded-proto", "x-forwarded-proto"} {
		for proto := range strings.SplitSeq(httpapi.HeaderValue(request.Headers, headerName), ",") {
			if strings.EqualFold(strings.TrimSpace(proto), "https") {
				return true
			}
		}
	}

	return false
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
