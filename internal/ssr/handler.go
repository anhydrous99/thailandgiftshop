package ssr

import (
	"context"
	"net/http"

	"github.com/aws/aws-lambda-go/events"
)

const (
	htmlContentType = "text/html; charset=utf-8"
	homeHTML        = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Thailand Gift Shop</title>
  <link rel="icon" href="/static/favicon.ico" sizes="any">
  <link rel="icon" href="/static/favicon.svg" type="image/svg+xml">
  <link rel="apple-touch-icon" href="/static/apple-touch-icon.png" sizes="180x180">
  <link rel="manifest" href="/static/site.webmanifest">
</head>
<body>
  <h1>Hello, world!</h1>
</body>
</html>`
)

// Handle renders HTML responses for API Gateway HTTP API requests.
func Handle(ctx context.Context, request events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	_ = ctx

	if requestPath(request) != "/" {
		return htmlResponse(http.StatusNotFound, "Not found", nil), nil
	}

	if method := requestMethod(request); method != http.MethodGet {
		return htmlResponse(http.StatusMethodNotAllowed, "Method not allowed", map[string]string{
			"Allow": http.MethodGet,
		}), nil
	}

	return htmlResponse(http.StatusOK, homeHTML, nil), nil
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
