package ssr

import (
	"bytes"
	"context"
	"net/http"
	"sync"

	"github.com/aws/aws-lambda-go/events"
)

const htmlContentType = "text/html; charset=utf-8"
const allowedMethods = http.MethodGet + ", " + http.MethodHead

var homeCache struct {
	sync.Mutex
	html string
	ok   bool
}

// Handle renders HTML responses for API Gateway HTTP API requests.
func Handle(ctx context.Context, request events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	if requestPath(request) != "/" {
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

	if method == http.MethodHead {
		body = ""
	}

	return htmlResponse(http.StatusOK, body, nil), nil
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
	for key, value := range extraHeaders {
		headers[key] = value
	}

	return events.APIGatewayV2HTTPResponse{
		StatusCode: statusCode,
		Headers:    headers,
		Body:       body,
	}
}
