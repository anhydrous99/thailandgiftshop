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
const homeProductLimit = 12

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

	if path != "/" {
		return htmlResponse(http.StatusNotFound, "Not found", nil), nil
	}

	method := requestMethod(request)
	if method != http.MethodGet && method != http.MethodHead {
		return htmlResponse(http.StatusMethodNotAllowed, "Method not allowed", map[string]string{
			"Allow": allowedMethods,
		}), nil
	}

	body, err := h.renderHome(ctx)
	if err != nil {
		return htmlResponse(http.StatusInternalServerError, "Internal server error", nil), nil
	}

	if method == http.MethodHead {
		body = ""
	}

	return htmlResponse(http.StatusOK, body, nil), nil
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
	products, err := h.catalogStore.ListActiveProducts(ctx, homeProductLimit)
	if err != nil {
		return "", err
	}

	var body bytes.Buffer
	if err := home(products, h.productImagePlaceholderURL).Render(ctx, &body); err != nil {
		return "", err
	}

	return body.String(), nil
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
