package ssr

import (
	"bytes"
	"context"
	"maps"
	"net/http"
	"strings"
	"sync"

	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/aws/aws-lambda-go/events"
)

const htmlContentType = "text/html; charset=utf-8"
const allowedMethods = http.MethodGet + ", " + http.MethodHead
const helloStatusText = "Hello from thailandgiftshop.com"
const fallbackHelloStatusText = "Fallback greeting refreshed from the full page"
const helloFragmentBody = "HTMX refreshed this greeting from the server"

var homeCache struct {
	sync.Mutex
	html string
	ok   bool
}

type Handler struct {
	catalogStore catalog.Store
}

func NewHandler(catalogStore catalog.Store) *Handler {
	if catalogStore == nil {
		catalogStore = catalog.EmptyStore{}
	}

	return &Handler{
		catalogStore: catalogStore,
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

	body, err := renderHome(ctx)
	if err != nil {
		return htmlResponse(http.StatusInternalServerError, "Internal server error", nil), nil
	}
	if request.QueryStringParameters["hello"] == "fallback" {
		body = strings.Replace(body, helloStatusText, fallbackHelloStatusText, 1)
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

func renderHome(ctx context.Context) (string, error) {
	homeCache.Lock()
	defer homeCache.Unlock()

	if homeCache.ok {
		return homeCache.html, nil
	}

	var body bytes.Buffer
	if err := home().Render(ctx, &body); err != nil {
		return "", err
	}

	homeCache.html = body.String()
	homeCache.ok = true
	return homeCache.html, nil
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
