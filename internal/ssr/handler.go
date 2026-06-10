package ssr

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/cart"
	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/httpapi"
	"github.com/anhydrous99/thailandgiftshop/internal/observability"
	"github.com/aws/aws-lambda-go/events"
)

const htmlContentType = httpapi.HTMLContentType
const textContentType = httpapi.TextContentType
const xmlContentType = httpapi.XMLContentType
const helloFragmentBody = "HTMX refreshed this greeting from the server"
const latestProductLimit = 8

type Handler struct {
	catalogStore               catalog.Store
	metrics                    observability.Recorder
	productImagePlaceholderURL string
	cartCookieSecret           string
	originSecretDigest         [32]byte
	originSecretSet            bool
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

	originSecretDigest, originSecretSet := httpapi.OriginSecretDigestFromEnvironment()
	return &Handler{
		catalogStore:               catalogStore,
		productImagePlaceholderURL: placeholderURL,
		cartCookieSecret:           os.Getenv(cart.EnvCookieSecret),
		originSecretDigest:         originSecretDigest,
		originSecretSet:            originSecretSet,
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
	method := httpapi.Method(request)
	route := ssrMetricRoute(httpapi.Path(request))
	response, err := h.handle(ctx, request)
	h.recordRouteMetrics(started, route, method, response.StatusCode)
	return response, err
}

func (h *Handler) handle(ctx context.Context, request events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	if !h.validOrigin(request) {
		return httpapi.HTMLResponse(http.StatusForbidden, "Forbidden", nil), nil
	}
	path := httpapi.Path(request)
	if path == "/hello-fragment" {
		return handleHelloFragment(request), nil
	}

	method := httpapi.Method(request)
	route := routeForPath(path)
	if route.knownPageShape && !isAllowedRouteMethod(route, method) {
		return httpapi.HTMLResponse(http.StatusMethodNotAllowed, "Method not allowed", map[string]string{
			"Allow": allowedMethodsForRoute(route),
		}), nil
	}
	if route.redirectTo != "" {
		if extraHeaders := seoHeadersForRoute(route.kind); extraHeaders != nil {
			return httpapi.PermanentRedirect(route.redirectTo, extraHeaders), nil
		}
		return httpapi.PermanentRedirect(route.redirectTo, nil), nil
	}
	if route.kind == pageRobotsTxt {
		return httpapi.TextResponse(http.StatusOK, maybeEmptyBody(method, robotsTxt()), seoHeadersForRoute(route.kind)), nil
	}
	if route.kind == pageSitemapXML {
		return h.handleSitemap(ctx, method), nil
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
			logHandlerError(route.kind, method, path, cartErr)
			return httpapi.HTMLResponse(http.StatusInternalServerError, "Internal server error", nil), nil
		}
		pageCartState = &currentCart
		headerCartLabel = cartNavigationLabel(currentCart.cart.TotalItemCount())
		cookies = currentCart.cookies
	} else {
		headerCartLabel = h.cartNavigation(request)
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
			body = notFoundBody(ctx, headerCartLabel)
		}
	case pageCategories:
		body, err = h.renderCategoryIndex(ctx, headerCartLabel)
	case pageCategoryDetail:
		found := false
		body, found, err = h.renderCategoryDetail(ctx, route.slug, headerCartLabel)
		if !found {
			statusCode = http.StatusNotFound
			body = notFoundBody(ctx, headerCartLabel)
		}
	case pageStory:
		body, err = h.renderStory(ctx, headerCartLabel)
	case pageCart:
		body, err = renderCartPage(ctx, cartPageViewModel{
			Metadata:                   cartMetadata(),
			Lines:                      pageCartState.lines,
			ProductImagePlaceholderURL: h.productImagePlaceholderURL,
			HeaderCartLabel:            headerCartLabel,
		})
	case pageCheckout:
		if pageCartState.cart.LineCount() == 0 || hasUnavailableCartLines(pageCartState.lines) {
			response := httpapi.SeeOther("/cart", cookies, seoHeadersForRoute(route.kind))
			if method == http.MethodHead {
				response.Body = ""
			}
			return response, nil
		}
		body, err = renderCheckoutPage(ctx, checkoutPageViewModel{
			Metadata:                   checkoutMetadata(),
			Lines:                      pageCartState.lines,
			ProductImagePlaceholderURL: h.productImagePlaceholderURL,
			HeaderCartLabel:            headerCartLabel,
		})
	default:
		statusCode = http.StatusNotFound
		body = notFoundBody(ctx, headerCartLabel)
	}
	if err != nil {
		logHandlerError(route.kind, method, path, err)
		return httpapi.HTMLResponse(http.StatusInternalServerError, "Internal server error", nil), nil
	}

	if method == http.MethodHead {
		body = ""
	}

	extraHeaders := seoHeadersForRoute(route.kind)
	if statusCode != http.StatusOK {
		// Never mark error bodies cacheable: a 404 for a just-published slug
		// must not linger at the edge for the catalog s-maxage window.
		extraHeaders = nil
	}

	return httpapi.HTMLResponseWithCookies(statusCode, body, extraHeaders, cookies), nil
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

func handleHelloFragment(request events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	if httpapi.Method(request) != http.MethodGet || !hasHeaderValue(request.Headers, "HX-Request", "true") {
		return httpapi.HTMLResponse(http.StatusNotFound, "Not found", nil)
	}

	return httpapi.HTMLResponse(http.StatusOK, helloFragmentBody, map[string]string{
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
		Metadata:                   homeMetadata(),
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
		Metadata:                   productListingMetadata(),
		Breadcrumbs:                productListingBreadcrumbs(),
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
		Metadata:                   productDetailMetadata(productResult.product, h.productImagePlaceholderURL),
		Breadcrumbs:                productDetailBreadcrumbs(productResult.product),
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
		Metadata:        categoryIndexMetadata(),
		Breadcrumbs:     categoryIndexBreadcrumbs(),
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

func relatedCategories(categories []catalog.Category, currentSlug string) []catalog.Category {
	related := make([]catalog.Category, 0, len(categories))
	for _, category := range categories {
		if category.Status != catalog.StatusActive || category.Slug == currentSlug {
			continue
		}
		related = append(related, category)
	}
	return related
}

func featuredHomeCategories(categories []catalog.Category) []catalog.Category {
	if len(categories) <= 3 {
		return categories
	}

	return categories[:3]
}

func (h *Handler) renderCategoryDetail(ctx context.Context, slug string, headerCartLabel string) (string, bool, error) {
	categories, products, categoriesErr, productsErr := parallelCatalogReads(ctx,
		func(ctx context.Context) ([]catalog.Category, error) {
			return h.loadActiveCategories(ctx)
		},
		func(ctx context.Context) ([]catalog.Product, error) {
			return h.catalogStore.ListActiveProductsByCategory(ctx, slug, 0)
		},
	)
	if categoriesErr != nil {
		return "", false, categoriesErr
	}
	category, found := findCategoryBySlug(categories, slug)
	if !found {
		return "", false, nil
	}
	if productsErr != nil {
		return "", false, productsErr
	}

	var body bytes.Buffer
	if err := categoryDetailPage(categoryDetailPageViewModel{
		Metadata:                   categoryDetailMetadata(category),
		Breadcrumbs:                categoryDetailBreadcrumbs(category),
		Category:                   category,
		Categories:                 categories,
		Products:                   products,
		RelatedCategories:          relatedCategories(categories, category.Slug),
		ProductImagePlaceholderURL: h.productImagePlaceholderURL,
		HeaderCartLabel:            headerCartLabel,
	}).Render(ctx, &body); err != nil {
		return "", false, err
	}

	return body.String(), true, nil
}

// notFoundBody renders the styled 404 page; on render failure it falls back
// to the plain string so error paths never depend on template rendering.
func notFoundBody(ctx context.Context, headerCartLabel string) string {
	var body bytes.Buffer
	if err := notFoundPage(headerCartLabel).Render(ctx, &body); err != nil {
		return "Not found"
	}

	return body.String()
}

func (h *Handler) renderStory(ctx context.Context, headerCartLabel string) (string, error) {
	var body bytes.Buffer
	if err := storyPage(storyPageViewModel{Metadata: storyMetadata(), Breadcrumbs: storyBreadcrumbs(), HeaderCartLabel: headerCartLabel}).Render(ctx, &body); err != nil {
		return "", err
	}

	return body.String(), nil
}

func (h *Handler) handleSitemap(ctx context.Context, method string) events.APIGatewayV2HTTPResponse {
	products, categories, productsErr, categoriesErr := parallelCatalogReads(ctx,
		func(ctx context.Context) ([]catalog.Product, error) {
			return h.catalogStore.ListActiveProducts(ctx, 0)
		},
		func(ctx context.Context) ([]catalog.Category, error) {
			return h.loadActiveCategories(ctx)
		},
	)
	if err := errors.Join(productsErr, categoriesErr); err != nil {
		logHandlerError(pageSitemapXML, method, "/sitemap.xml", err)
		return httpapi.HTMLResponse(http.StatusInternalServerError, "Internal server error", nil)
	}
	body, err := sitemapXML(products, categories)
	if err != nil {
		logHandlerError(pageSitemapXML, method, "/sitemap.xml", err)
		return httpapi.HTMLResponse(http.StatusInternalServerError, "Internal server error", nil)
	}

	return httpapi.XMLResponse(http.StatusOK, maybeEmptyBody(method, body), seoHeadersForRoute(pageSitemapXML))
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

func maybeEmptyBody(method string, body string) string {
	if method == http.MethodHead {
		return ""
	}
	return body
}
