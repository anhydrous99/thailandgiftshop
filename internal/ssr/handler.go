package ssr

import (
	"bytes"
	"context"
	"maps"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/aws/aws-lambda-go/events"
)

const htmlContentType = "text/html; charset=utf-8"
const allowedMethods = http.MethodGet + ", " + http.MethodHead
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
	productImagePlaceholderURL string
}

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
	catalogStore, _, err := catalog.NewStoreFromEnv(ctx)
	if err != nil {
		return nil, err
	}

	return NewHandler(catalogStore), nil
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
	path := requestPath(request)
	if path == "/hello-fragment" {
		return handleHelloFragment(request), nil
	}

	method := requestMethod(request)
	route := routeForPath(path)
	if route.knownPageShape && !isAllowedPageMethod(method) {
		return htmlResponse(http.StatusMethodNotAllowed, "Method not allowed", map[string]string{
			"Allow": allowedMethods,
		}), nil
	}
	if route.redirectTo != "" {
		return redirectResponse(route.redirectTo), nil
	}
	var body string
	statusCode := http.StatusOK
	var err error

	switch route.kind {
	case pageHome:
		body, err = h.renderHome(ctx)
	case pageProducts:
		body, err = h.renderProductListing(ctx)
	case pageProductDetail:
		found := false
		body, found, err = h.renderProductDetail(ctx, route.slug)
		if !found {
			statusCode = http.StatusNotFound
			body = "Not found"
		}
	case pageCategories:
		body, err = h.renderCategoryIndex(ctx)
	case pageCategoryDetail:
		found := false
		body, found, err = h.renderCategoryDetail(ctx, route.slug)
		if !found {
			statusCode = http.StatusNotFound
			body = "Not found"
		}
	case pageStory:
		body, err = h.renderStory(ctx)
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

	return htmlResponse(statusCode, body, nil), nil
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
	if !strings.HasPrefix(path, prefix) {
		return pageRoute{kind: pageUnknown}
	}

	slug := strings.TrimPrefix(path, prefix)
	if slug == "" {
		return pageRoute{kind: pageUnknown}
	}
	if strings.HasSuffix(slug, "/") {
		slug = strings.TrimSuffix(slug, "/")
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

func redirectResponse(location string) events.APIGatewayV2HTTPResponse {
	return events.APIGatewayV2HTTPResponse{
		StatusCode: http.StatusPermanentRedirect,
		Headers: map[string]string{
			"Content-Type": htmlContentType,
			"Location":     location,
		},
	}
}

func isAllowedPageMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
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

func (h *Handler) renderHome(ctx context.Context) (string, error) {
	products, err := h.catalogStore.ListRecentlyAddedProducts(ctx, latestProductLimit)
	if err != nil {
		return "", err
	}
	categories, err := h.loadActiveCategories(ctx)
	if err != nil {
		return "", err
	}

	var body bytes.Buffer
	if err := home(homePageViewModel{
		Products:                   products,
		Categories:                 categories,
		ProductImagePlaceholderURL: h.productImagePlaceholderURL,
	}).Render(ctx, &body); err != nil {
		return "", err
	}

	return body.String(), nil
}

func (h *Handler) renderProductListing(ctx context.Context) (string, error) {
	products, err := h.catalogStore.ListActiveProducts(ctx, 0)
	if err != nil {
		return "", err
	}
	categories, err := h.loadActiveCategories(ctx)
	if err != nil {
		return "", err
	}

	var body bytes.Buffer
	if err := productListingPage(productListingPageViewModel{
		Products:                   products,
		Categories:                 categories,
		ProductImagePlaceholderURL: h.productImagePlaceholderURL,
	}).Render(ctx, &body); err != nil {
		return "", err
	}

	return body.String(), nil
}

func (h *Handler) renderProductDetail(ctx context.Context, slug string) (string, bool, error) {
	product, found, err := h.catalogStore.GetProductBySlug(ctx, slug)
	if err != nil {
		return "", false, err
	}
	if !found || product.Status != catalog.StatusActive {
		return "", false, nil
	}

	var body bytes.Buffer
	if err := productDetailPage(productDetailPageViewModel{
		Product:                    product,
		ProductImagePlaceholderURL: h.productImagePlaceholderURL,
	}).Render(ctx, &body); err != nil {
		return "", false, err
	}

	return body.String(), true, nil
}

func (h *Handler) renderCategoryIndex(ctx context.Context) (string, error) {
	categories, err := h.loadActiveCategories(ctx)
	if err != nil {
		return "", err
	}

	var body bytes.Buffer
	if err := categoryIndexPage(categoryIndexPageViewModel{
		Categories: categories,
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

func (h *Handler) renderCategoryDetail(ctx context.Context, slug string) (string, bool, error) {
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
	}).Render(ctx, &body); err != nil {
		return "", false, err
	}

	return body.String(), true, nil
}

func (h *Handler) renderStory(ctx context.Context) (string, error) {
	var body bytes.Buffer
	if err := storyPage(storyPageViewModel{}).Render(ctx, &body); err != nil {
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

func heroImageURL(products []catalog.Product, fallback string) string {
	for _, product := range products {
		imageURL := product.DisplayImageURL(fallback)
		if imageURL != "" {
			return imageURL
		}
	}

	return catalog.Product{}.DisplayImageURL(fallback)
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
	headers := map[string]string{
		"Content-Type": htmlContentType,
	}
	maps.Copy(headers, extraHeaders)

	return events.APIGatewayV2HTTPResponse{
		StatusCode: statusCode,
		Headers:    headers,
		Body:       body,
	}
}
