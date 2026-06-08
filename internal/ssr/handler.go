package ssr

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/cart"
	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/observability"
	"github.com/aws/aws-lambda-go/events"
)

const htmlContentType = "text/html; charset=utf-8"
const allowedMethods = http.MethodGet + ", " + http.MethodHead
const cartMutationAllowedMethods = http.MethodPost
const helloFragmentBody = "HTMX refreshed this greeting from the server"
const latestProductLimit = 8

type pageKind string

const (
	pageHome           pageKind = "home"
	pageProducts       pageKind = "products"
	pageProductDetail  pageKind = "product-detail"
	pageCategories     pageKind = "categories"
	pageCategoryDetail pageKind = "category-detail"
	pageStory          pageKind = "story"
	pageCart           pageKind = "cart"
	pageCheckout       pageKind = "checkout"
	pageCartItems      pageKind = "cart-items"
	pageCartQuantity   pageKind = "cart-quantity"
	pageCartRemove     pageKind = "cart-remove"
	pageCartClear      pageKind = "cart-clear"
	pageUnknown        pageKind = "unknown"
)

type pageRoute struct {
	kind           pageKind
	slug           string
	redirectTo     string
	knownPageShape bool
}

type Handler struct {
	catalogStore               catalog.Store
	metrics                    observability.Recorder
	productImagePlaceholderURL string
}

var ssrColdStartRecorded atomic.Bool

func NewHandler(catalogStore catalog.Store) *Handler {
	return NewHandlerWithProductImagePlaceholderURL(catalogStore, os.Getenv(catalog.EnvProductImagePlaceholderURL))
}

func NewHandlerWithProductImagePlaceholderURL(catalogStore catalog.Store, placeholderURL string) *Handler {
	if catalogStore == nil {
		catalogStore = catalog.EmptyStore{}
	}
	if strings.TrimSpace(placeholderURL) == "" {
		placeholderURL = catalog.DefaultProductImagePlaceholderURL
	}

	return &Handler{
		catalogStore:               catalogStore,
		productImagePlaceholderURL: placeholderURL,
	}
}

func NewHandlerFromEnvironment(ctx context.Context) (*Handler, error) {
	metrics := observability.NewEMFRecorder(os.Stdout)
	catalogStore, _, err := catalog.NewStoreFromEnvWithRecorder(ctx, metrics)
	if err != nil {
		return nil, err
	}

	handler := NewHandler(catalogStore)
	handler.metrics = metrics
	return handler, nil
}

func (h *Handler) Catalog() catalog.Store {
	return h.catalogStore
}

var defaultHandler = NewHandler(catalog.EmptyStore{})

// Handle renders HTML responses for API Gateway HTTP API requests.
func Handle(ctx context.Context, request events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	return defaultHandler.Handle(ctx, request)
}

// Handle renders HTML responses for API Gateway HTTP API requests.
func (h *Handler) Handle(ctx context.Context, request events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	started := time.Now()
	method := requestMethod(request)
	route := ssrMetricRoute(requestPath(request))
	response, err := h.handle(ctx, request)
	h.recordRouteMetrics(started, route, method, response.StatusCode)
	return response, err
}

func (h *Handler) handle(ctx context.Context, request events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	path := requestPath(request)
	if path == "/hello-fragment" {
		return handleHelloFragment(request), nil
	}

	method := requestMethod(request)
	route := routeForPath(path)
	if route.knownPageShape && !isAllowedRouteMethod(route, method) {
		return htmlResponse(http.StatusMethodNotAllowed, "Method not allowed", map[string]string{
			"Allow": allowedMethodsForRoute(route),
		}), nil
	}
	if route.redirectTo != "" {
		return redirectResponse(route.redirectTo), nil
	}
	if isCartMutationRoute(route.kind) {
		return h.handleCartMutation(ctx, request, route), nil
	}
	var body string
	statusCode := http.StatusOK
	cookies := []string(nil)
	var err error
	headerCartLabel := cartNavigationLabel(0)
	var pageCartState *requestCart
	if route.kind == pageCart || route.kind == pageCheckout {
		currentCart, cartErr := h.cartStateFromRequest(ctx, request)
		if cartErr != nil {
			return htmlResponse(http.StatusInternalServerError, "Internal server error", nil), nil
		}
		pageCartState = &currentCart
		headerCartLabel = cartNavigationLabel(currentCart.cart.TotalItemCount())
		cookies = currentCart.cookies
	} else {
		headerCookies := []string(nil)
		headerCartLabel, headerCookies = h.cartNavigation(ctx, request)
		cookies = append(cookies, headerCookies...)
	}

	switch route.kind {
	case pageHome:
		body, err = h.renderHome(ctx, headerCartLabel)
	case pageProducts:
		body, err = h.renderProductListing(ctx, headerCartLabel)
	case pageProductDetail:
		found := false
		body, found, err = h.renderProductDetail(ctx, route.slug, headerCartLabel)
		if !found {
			statusCode = http.StatusNotFound
			body = "Not found"
		}
	case pageCategories:
		body, err = h.renderCategoryIndex(ctx, headerCartLabel)
	case pageCategoryDetail:
		found := false
		body, found, err = h.renderCategoryDetail(ctx, route.slug, headerCartLabel)
		if !found {
			statusCode = http.StatusNotFound
			body = "Not found"
		}
	case pageStory:
		body, err = h.renderStory(ctx, headerCartLabel)
	case pageCart:
		body, err = renderCartPage(ctx, cartPageViewModel{
			Lines:                      pageCartState.lines,
			ProductImagePlaceholderURL: h.productImagePlaceholderURL,
			HeaderCartLabel:            headerCartLabel,
		})
	case pageCheckout:
		if pageCartState.cart.LineCount() == 0 || hasUnavailableCartLines(pageCartState.lines) {
			response := seeOtherResponse("/cart", cookies)
			if method == http.MethodHead {
				response.Body = ""
			}
			return response, nil
		}
		body, err = renderCheckoutPage(ctx, checkoutPageViewModel{
			Lines:                      pageCartState.lines,
			ProductImagePlaceholderURL: h.productImagePlaceholderURL,
			HeaderCartLabel:            headerCartLabel,
		})
	default:
		statusCode = http.StatusNotFound
		body = "Not found"
	}
	if err != nil {
		return htmlResponse(http.StatusInternalServerError, "Internal server error", nil), nil
	}

	if method == http.MethodHead {
		body = ""
	}

	return htmlResponseWithCookies(statusCode, body, nil, cookies), nil
}

func ssrMetricRoute(path string) string {
	if path == "/hello-fragment" {
		return "hello_fragment"
	}
	return string(routeForPath(path).kind)
}

func (h *Handler) recordRouteMetrics(started time.Time, route string, method string, statusCode int) {
	if h.metrics == nil {
		return
	}
	if ssrColdStartRecorded.CompareAndSwap(false, true) {
		h.metrics.Record(observability.Count(
			observability.MetricRouteColdStart,
			observability.Dim("Service", "ssr"),
		))
	}
	h.metrics.Record(observability.Duration(
		observability.MetricRouteDurationMs,
		time.Since(started),
		observability.Dim("Service", "ssr"),
		observability.Dim("Route", route),
		observability.Dim("Method", method),
		observability.Dim("Status", strconv.Itoa(statusCode)),
	))
}

func routeForPath(path string) pageRoute {
	switch path {
	case "/":
		return pageRoute{kind: pageHome, knownPageShape: true}
	case "/shop":
		return pageRoute{kind: pageProducts, redirectTo: "/products", knownPageShape: true}
	case "/about":
		return pageRoute{kind: pageStory, redirectTo: "/story", knownPageShape: true}
	case "/products":
		return pageRoute{kind: pageProducts, knownPageShape: true}
	case "/products/":
		return pageRoute{kind: pageProducts, redirectTo: "/products", knownPageShape: true}
	case "/categories":
		return pageRoute{kind: pageCategories, knownPageShape: true}
	case "/categories/":
		return pageRoute{kind: pageCategories, redirectTo: "/categories", knownPageShape: true}
	case "/story":
		return pageRoute{kind: pageStory, knownPageShape: true}
	case "/story/":
		return pageRoute{kind: pageStory, redirectTo: "/story", knownPageShape: true}
	case "/cart":
		return pageRoute{kind: pageCart, knownPageShape: true}
	case "/cart/":
		return pageRoute{kind: pageCart, redirectTo: "/cart", knownPageShape: true}
	case "/checkout":
		return pageRoute{kind: pageCheckout, knownPageShape: true}
	case "/checkout/":
		return pageRoute{kind: pageCheckout, redirectTo: "/checkout", knownPageShape: true}
	case "/cart/items":
		return pageRoute{kind: pageCartItems, knownPageShape: true}
	case "/cart/clear":
		return pageRoute{kind: pageCartClear, knownPageShape: true}
	}
	if route := cartMutationRouteForPath(path, "/cart/items/", "/quantity", pageCartQuantity); route.knownPageShape {
		return route
	}
	if route := cartMutationRouteForPath(path, "/cart/items/", "/remove", pageCartRemove); route.knownPageShape {
		return route
	}

	if route := detailRouteForPath(path, "/products/", pageProductDetail); route.knownPageShape {
		return route
	}
	if route := detailRouteForPath(path, "/categories/", pageCategoryDetail); route.knownPageShape {
		return route
	}

	return pageRoute{kind: pageUnknown}
}

func detailRouteForPath(path string, prefix string, kind pageKind) pageRoute {
	slug, ok := strings.CutPrefix(path, prefix)
	if !ok {
		return pageRoute{kind: pageUnknown}
	}

	if slug == "" {
		return pageRoute{kind: pageUnknown}
	}
	if trimmedSlug, ok := strings.CutSuffix(slug, "/"); ok {
		slug = trimmedSlug
		if slug == "" || strings.Contains(slug, "/") {
			return pageRoute{kind: pageUnknown}
		}
		return pageRoute{kind: kind, slug: slug, redirectTo: prefix + slug, knownPageShape: true}
	}
	if strings.Contains(slug, "/") {
		return pageRoute{kind: pageUnknown}
	}

	return pageRoute{kind: kind, slug: slug, knownPageShape: true}
}

func cartMutationRouteForPath(path string, prefix string, suffix string, kind pageKind) pageRoute {
	slug, ok := strings.CutPrefix(path, prefix)
	if !ok {
		return pageRoute{kind: pageUnknown}
	}
	slug, ok = strings.CutSuffix(slug, suffix)
	if !ok {
		return pageRoute{kind: pageUnknown}
	}
	if slug == "" || strings.Contains(slug, "/") {
		return pageRoute{kind: pageUnknown}
	}

	return pageRoute{kind: kind, slug: slug, knownPageShape: true}
}

func redirectResponse(location string) events.APIGatewayV2HTTPResponse {
	return events.APIGatewayV2HTTPResponse{
		StatusCode: http.StatusPermanentRedirect,
		Headers: map[string]string{
			"Content-Type": htmlContentType,
			"Location":     location,
		},
	}
}

func seeOtherResponse(location string, cookies []string) events.APIGatewayV2HTTPResponse {
	return events.APIGatewayV2HTTPResponse{
		StatusCode: http.StatusSeeOther,
		Headers: map[string]string{
			"Content-Type": htmlContentType,
			"Location":     location,
		},
		Cookies: cookies,
	}
}

func isAllowedPageMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func isAllowedRouteMethod(route pageRoute, method string) bool {
	if isCartMutationRoute(route.kind) {
		return method == http.MethodPost
	}

	return isAllowedPageMethod(method)
}

func allowedMethodsForRoute(route pageRoute) string {
	if isCartMutationRoute(route.kind) {
		return cartMutationAllowedMethods
	}

	return allowedMethods
}

func isCartMutationRoute(kind pageKind) bool {
	return kind == pageCartItems || kind == pageCartQuantity || kind == pageCartRemove || kind == pageCartClear
}

func handleHelloFragment(request events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	if requestMethod(request) != http.MethodGet || !hasHeaderValue(request.Headers, "HX-Request", "true") {
		return htmlResponse(http.StatusNotFound, "Not found", nil)
	}

	return htmlResponse(http.StatusOK, helloFragmentBody, map[string]string{
		"Cache-Control": "no-store",
		"Vary":          "HX-Request",
	})
}

func hasHeaderValue(headers map[string]string, name string, value string) bool {
	for key, got := range headers {
		if strings.EqualFold(key, name) {
			return got == value
		}
	}

	return false
}

func (h *Handler) renderHome(ctx context.Context, headerCartLabel string) (string, error) {
	products, categories, productsErr, categoriesErr := parallelCatalogReads(ctx,
		func(ctx context.Context) ([]catalog.Product, error) {
			return h.catalogStore.ListRecentlyAddedProducts(ctx, latestProductLimit)
		},
		func(ctx context.Context) ([]catalog.Category, error) {
			return h.loadActiveCategories(ctx)
		},
	)
	if productsErr != nil {
		return "", productsErr
	}
	if categoriesErr != nil {
		return "", categoriesErr
	}

	var body bytes.Buffer
	if err := home(homePageViewModel{
		Products:                   products,
		Categories:                 categories,
		FeaturedCategories:         featuredHomeCategories(categories),
		ProductImagePlaceholderURL: h.productImagePlaceholderURL,
		HeaderCartLabel:            headerCartLabel,
	}).Render(ctx, &body); err != nil {
		return "", err
	}

	return body.String(), nil
}

func (h *Handler) renderProductListing(ctx context.Context, headerCartLabel string) (string, error) {
	products, categories, productsErr, categoriesErr := parallelCatalogReads(ctx,
		func(ctx context.Context) ([]catalog.Product, error) {
			return h.catalogStore.ListActiveProducts(ctx, 0)
		},
		func(ctx context.Context) ([]catalog.Category, error) {
			return h.loadActiveCategories(ctx)
		},
	)
	if productsErr != nil {
		return "", productsErr
	}
	if categoriesErr != nil {
		return "", categoriesErr
	}

	var body bytes.Buffer
	if err := productListingPage(productListingPageViewModel{
		Products:                   products,
		Categories:                 categories,
		ProductImagePlaceholderURL: h.productImagePlaceholderURL,
		HeaderCartLabel:            headerCartLabel,
	}).Render(ctx, &body); err != nil {
		return "", err
	}

	return body.String(), nil
}

func (h *Handler) renderProductDetail(ctx context.Context, slug string, headerCartLabel string) (string, bool, error) {
	productResult, categories, productErr, categoriesErr := parallelCatalogReads(ctx,
		func(ctx context.Context) (productLookupResult, error) {
			product, found, err := h.catalogStore.GetProductBySlug(ctx, slug)
			return productLookupResult{product: product, found: found}, err
		},
		func(ctx context.Context) ([]catalog.Category, error) {
			return h.loadActiveCategories(ctx)
		},
	)
	if productErr != nil {
		return "", false, productErr
	}
	if !productResult.found || productResult.product.Status != catalog.StatusActive {
		return "", false, nil
	}
	if categoriesErr != nil {
		return "", false, categoriesErr
	}

	var body bytes.Buffer
	if err := productDetailPage(productDetailPageViewModel{
		Product:                    productResult.product,
		Categories:                 activeProductCategories(productResult.product, categories),
		ProductImagePlaceholderURL: h.productImagePlaceholderURL,
		HeaderCartLabel:            headerCartLabel,
	}).Render(ctx, &body); err != nil {
		return "", false, err
	}

	return body.String(), true, nil
}

func (h *Handler) renderCategoryIndex(ctx context.Context, headerCartLabel string) (string, error) {
	categories, err := h.loadActiveCategories(ctx)
	if err != nil {
		return "", err
	}

	var body bytes.Buffer
	if err := categoryIndexPage(categoryIndexPageViewModel{
		Categories:      categories,
		HeaderCartLabel: headerCartLabel,
	}).Render(ctx, &body); err != nil {
		return "", err
	}

	return body.String(), nil
}

func findCategoryBySlug(categories []catalog.Category, slug string) (catalog.Category, bool) {
	for _, category := range categories {
		if category.Slug == slug && category.Status == catalog.StatusActive {
			return category, true
		}
	}

	return catalog.Category{}, false
}

func activeProductCategories(product catalog.Product, categories []catalog.Category) []catalog.Category {
	if len(product.CategorySlugs) == 0 || len(categories) == 0 {
		return nil
	}

	categoriesBySlug := make(map[string]catalog.Category, len(categories))
	for _, category := range categories {
		if category.Status == catalog.StatusActive {
			categoriesBySlug[category.Slug] = category
		}
	}

	productCategories := make([]catalog.Category, 0, len(product.CategorySlugs))
	seen := make(map[string]bool, len(product.CategorySlugs))
	for _, slug := range product.CategorySlugs {
		category, found := categoriesBySlug[slug]
		if !found || seen[slug] {
			continue
		}
		productCategories = append(productCategories, category)
		seen[slug] = true
	}

	return productCategories
}

func featuredHomeCategories(categories []catalog.Category) []catalog.Category {
	if len(categories) <= 3 {
		return categories
	}

	return categories[:3]
}

func (h *Handler) renderCategoryDetail(ctx context.Context, slug string, headerCartLabel string) (string, bool, error) {
	categories, err := h.loadActiveCategories(ctx)
	if err != nil {
		return "", false, err
	}
	category, found := findCategoryBySlug(categories, slug)
	if !found {
		return "", false, nil
	}

	products, err := h.catalogStore.ListActiveProductsByCategory(ctx, slug, 0)
	if err != nil {
		return "", false, err
	}

	var body bytes.Buffer
	if err := categoryDetailPage(categoryDetailPageViewModel{
		Category:                   category,
		Categories:                 categories,
		Products:                   products,
		ProductImagePlaceholderURL: h.productImagePlaceholderURL,
		HeaderCartLabel:            headerCartLabel,
	}).Render(ctx, &body); err != nil {
		return "", false, err
	}

	return body.String(), true, nil
}

func (h *Handler) renderStory(ctx context.Context, headerCartLabel string) (string, error) {
	var body bytes.Buffer
	if err := storyPage(storyPageViewModel{HeaderCartLabel: headerCartLabel}).Render(ctx, &body); err != nil {
		return "", err
	}

	return body.String(), nil
}

type catalogReadResult[T any] struct {
	value T
	err   error
}

type productLookupResult struct {
	product catalog.Product
	found   bool
}

func parallelCatalogReads[A any, B any](ctx context.Context, first func(context.Context) (A, error), second func(context.Context) (B, error)) (A, B, error, error) {
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	firstResult := make(chan catalogReadResult[A], 1)
	secondResult := make(chan catalogReadResult[B], 1)
	go func() {
		value, err := first(readCtx)
		firstResult <- catalogReadResult[A]{value: value, err: err}
	}()
	go func() {
		value, err := second(readCtx)
		secondResult <- catalogReadResult[B]{value: value, err: err}
	}()

	gotFirst := <-firstResult
	if gotFirst.err != nil {
		cancel()
	}
	gotSecond := <-secondResult
	if gotSecond.err != nil {
		cancel()
	}

	return gotFirst.value, gotSecond.value, gotFirst.err, gotSecond.err
}

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
	form, err := parseFormRequest(request)
	if err != nil {
		return htmlResponse(http.StatusBadRequest, "Bad request", nil)
	}

	currentCart, _, _, err := h.cartFromRequest(ctx, request)
	if err != nil {
		return htmlResponse(http.StatusInternalServerError, "Internal server error", nil)
	}
	mutatedCart := currentCart

	switch route.kind {
	case pageCartItems:
		quantity, ok := positiveFormQuantity(form)
		if !ok {
			return htmlResponse(http.StatusBadRequest, "Invalid quantity", nil)
		}
		slug := form.Get("slug")
		variantID := strings.TrimSpace(form.Get("variant_id"))
		product, found, err := h.cartProduct(ctx, slug)
		if err != nil {
			return htmlResponse(http.StatusInternalServerError, "Internal server error", nil)
		}
		if !found {
			return htmlResponse(http.StatusNotFound, "Not found", nil)
		}
		stockLimit, ok := availableStockForProduct(product, variantID, true)
		if !ok {
			if !product.UsesVariants() {
				return htmlResponse(http.StatusNotFound, "Not found", nil)
			}
			return htmlResponse(http.StatusBadRequest, "Select an available size", nil)
		}
		mutatedCart, err = currentCart.AddLine(product.Slug, variantID, min(quantity, stockLimit, cart.MaxQuantity))
		if err != nil {
			return cartMutationErrorResponse(err)
		}
	case pageCartQuantity:
		quantity, ok := positiveFormQuantity(form)
		if !ok {
			return htmlResponse(http.StatusBadRequest, "Invalid quantity", nil)
		}
		variantID := strings.TrimSpace(form.Get("variant_id"))
		product, found, err := h.cartProduct(ctx, route.slug)
		if err != nil {
			return htmlResponse(http.StatusInternalServerError, "Internal server error", nil)
		}
		if !found {
			return htmlResponse(http.StatusNotFound, "Not found", nil)
		}
		stockLimit, ok := availableStockForProduct(product, variantID, false)
		if !ok {
			if !product.UsesVariants() {
				return htmlResponse(http.StatusNotFound, "Not found", nil)
			}
			return htmlResponse(http.StatusBadRequest, "Bad request", nil)
		}
		mutatedCart, err = currentCart.SetLineQuantity(product.Slug, variantID, min(quantity, stockLimit, cart.MaxQuantity))
		if err != nil {
			return cartMutationErrorResponse(err)
		}
	case pageCartRemove:
		variantID := strings.TrimSpace(form.Get("variant_id"))
		product, found, err := h.cartProduct(ctx, route.slug)
		if err != nil {
			return htmlResponse(http.StatusInternalServerError, "Internal server error", nil)
		}
		if !found {
			return htmlResponse(http.StatusNotFound, "Not found", nil)
		}
		mutatedCart, err = currentCart.RemoveLine(product.Slug, variantID)
		if err != nil {
			return cartMutationErrorResponse(err)
		}
	case pageCartClear:
		mutatedCart = currentCart.Clear()
	}

	mutatedCart, _, _, err = h.normalizeCart(ctx, mutatedCart)
	if err != nil {
		return htmlResponse(http.StatusInternalServerError, "Internal server error", nil)
	}
	cookieValue, err := cartResponseCookie(mutatedCart, request)
	if err != nil {
		return htmlResponse(http.StatusBadRequest, "Bad request", nil)
	}

	return seeOtherResponse("/cart", []string{cookieValue})
}

func cartMutationErrorResponse(err error) events.APIGatewayV2HTTPResponse {
	if errors.Is(err, cart.ErrInvalidSlug) {
		return htmlResponse(http.StatusNotFound, "Not found", nil)
	}
	if errors.Is(err, cart.ErrInvalidQuantity) || errors.Is(err, cart.ErrLineItemLimit) || errors.Is(err, cart.ErrCookieTooLarge) {
		return htmlResponse(http.StatusBadRequest, "Bad request", nil)
	}

	return htmlResponse(http.StatusInternalServerError, "Internal server error", nil)
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
		decoded := cart.DecodeCookie(cookieValue, os.Getenv(cart.EnvCookieSecret))
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
		cookieValue, err := cartResponseCookie(normalizedCart, request)
		if err != nil {
			return requestCart{}, err
		}
		return requestCart{cart: normalizedCart, lines: lines, cookies: []string{cookieValue}}, nil
	}

	return requestCart{cart: normalizedCart, lines: lines}, nil
}

func (h *Handler) normalizeCart(ctx context.Context, currentCart cart.Cart) (cart.Cart, []cartLineView, bool, error) {
	normalizedCart := cart.Empty()
	lines := make([]cartLineView, 0, currentCart.LineCount())
	changed := false

	for _, line := range currentCart.Lines() {
		product, found, err := h.catalogStore.GetProductBySlug(ctx, line.Slug)
		if err != nil {
			return cart.Empty(), nil, false, err
		}
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

func (h *Handler) cartNavigation(ctx context.Context, request events.APIGatewayV2HTTPRequest) (string, []string) {
	if _, found := cartCookieValue(request); !found {
		return cartNavigationLabel(0), nil
	}

	currentCart, _, cookies, err := h.cartFromRequest(ctx, request)
	if err != nil {
		return cartNavigationLabel(0), nil
	}

	return cartNavigationLabel(currentCart.TotalItemCount()), cookies
}

func cartNavigationLabel(itemCount int) string {
	if itemCount <= 0 {
		return "Cart"
	}

	return "Cart (" + strconv.Itoa(itemCount) + ")"
}

func parseFormRequest(request events.APIGatewayV2HTTPRequest) (url.Values, error) {
	body := request.Body
	if request.IsBase64Encoded {
		decoded, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return nil, err
		}
		body = string(decoded)
	}

	return url.ParseQuery(body)
}

func positiveFormQuantity(form url.Values) (int, bool) {
	quantity, err := strconv.Atoi(strings.TrimSpace(form.Get("quantity")))
	if err != nil || quantity <= 0 {
		return 0, false
	}

	return quantity, true
}

func cartCookieValue(request events.APIGatewayV2HTTPRequest) (string, bool) {
	for _, cookieHeader := range request.Cookies {
		if value, found := namedCookieValue(cookieHeader, cart.CookieName); found {
			return value, true
		}
	}
	if cookieHeader := headerValue(request.Headers, "Cookie"); cookieHeader != "" {
		return namedCookieValue(cookieHeader, cart.CookieName)
	}

	return "", false
}

func namedCookieValue(cookieHeader string, name string) (string, bool) {
	for part := range strings.SplitSeq(cookieHeader, ";") {
		cookieName, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && cookieName == name {
			return value, true
		}
	}

	return "", false
}

func cartResponseCookie(currentCart cart.Cart, request events.APIGatewayV2HTTPRequest) (string, error) {
	if currentCart.LineCount() == 0 {
		return clearCartCookie(request), nil
	}
	encoded, err := cart.EncodeCookie(currentCart, os.Getenv(cart.EnvCookieSecret))
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
		for proto := range strings.SplitSeq(headerValue(request.Headers, headerName), ",") {
			if strings.EqualFold(strings.TrimSpace(proto), "https") {
				return true
			}
		}
	}

	return false
}

func headerValue(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value
		}
	}

	return ""
}

func renderCartPage(ctx context.Context, vm cartPageViewModel) (string, error) {
	var body bytes.Buffer
	if err := cartPage(vm).Render(ctx, &body); err != nil {
		return "", err
	}

	return body.String(), nil
}

func renderCheckoutPage(ctx context.Context, vm checkoutPageViewModel) (string, error) {
	var body bytes.Buffer
	if err := checkoutPage(vm).Render(ctx, &body); err != nil {
		return "", err
	}

	return body.String(), nil
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

var _ = cartLineQuantityLimit

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

func (h *Handler) loadActiveCategories(ctx context.Context) ([]catalog.Category, error) {
	return h.catalogStore.ListActiveCategories(ctx)
}

func formatPrice(priceCents int) string {
	if priceCents < 0 {
		priceCents = 0
	}

	return "$" + strconv.Itoa(priceCents/100) + "." + twoDigitCents(priceCents%100)
}

func twoDigitCents(cents int) string {
	if cents < 10 {
		return "0" + strconv.Itoa(cents)
	}

	return strconv.Itoa(cents)
}

func requestMethod(request events.APIGatewayV2HTTPRequest) string {
	if request.RequestContext.HTTP.Method != "" {
		return request.RequestContext.HTTP.Method
	}

	return http.MethodGet
}

func requestPath(request events.APIGatewayV2HTTPRequest) string {
	if request.RawPath != "" {
		return request.RawPath
	}
	if request.RequestContext.HTTP.Path != "" {
		return request.RequestContext.HTTP.Path
	}

	return "/"
}

func htmlResponse(statusCode int, body string, extraHeaders map[string]string) events.APIGatewayV2HTTPResponse {
	return htmlResponseWithCookies(statusCode, body, extraHeaders, nil)
}

func htmlResponseWithCookies(statusCode int, body string, extraHeaders map[string]string, cookies []string) events.APIGatewayV2HTTPResponse {
	headers := map[string]string{
		"Content-Type": htmlContentType,
	}
	maps.Copy(headers, extraHeaders)

	return events.APIGatewayV2HTTPResponse{
		StatusCode: statusCode,
		Headers:    headers,
		Body:       body,
		Cookies:    cookies,
	}
}
