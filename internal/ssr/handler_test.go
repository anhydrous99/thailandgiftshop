package ssr

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"
)

var expectedHomeContent = []string{
	"<!doctype html>",
	`<html lang="en">`,
	`<title>Thailand Gift Shop</title>`,
	`<link rel="icon" href="/static/favicon.ico" sizes="any">`,
	`<link rel="icon" href="/static/favicon.svg" type="image/svg+xml">`,
	`<link rel="apple-touch-icon" href="/static/apple-touch-icon.png" sizes="180x180">`,
	`<link rel="manifest" href="/static/site.webmanifest">`,
	`<h1>Hello, world!</h1>`,
}

func TestHandle(t *testing.T) {
	tests := []struct {
		name         string
		request      events.APIGatewayV2HTTPRequest
		statusCode   int
		body         string
		bodyContains []string
		headers      map[string]string
	}{
		{
			name: "root returns hello world",
			request: events.APIGatewayV2HTTPRequest{
				RawPath: "/",
				RequestContext: events.APIGatewayV2HTTPRequestContext{
					HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{
						Method: http.MethodGet,
					},
				},
			},
			statusCode:   http.StatusOK,
			bodyContains: expectedHomeContent,
			headers: map[string]string{
				"Content-Type": htmlContentType,
			},
		},
		{
			name:         "empty request defaults to root get",
			request:      events.APIGatewayV2HTTPRequest{},
			statusCode:   http.StatusOK,
			bodyContains: expectedHomeContent,
			headers: map[string]string{
				"Content-Type": htmlContentType,
			},
		},
		{
			name: "root rejects unsupported methods",
			request: events.APIGatewayV2HTTPRequest{
				RawPath: "/",
				RequestContext: events.APIGatewayV2HTTPRequestContext{
					HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{
						Method: http.MethodPost,
					},
				},
			},
			statusCode: http.StatusMethodNotAllowed,
			body:       "Method not allowed",
			headers: map[string]string{
				"Allow":        http.MethodGet,
				"Content-Type": htmlContentType,
			},
		},
		{
			name: "unknown path returns not found",
			request: events.APIGatewayV2HTTPRequest{
				RawPath: "/about",
				RequestContext: events.APIGatewayV2HTTPRequestContext{
					HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{
						Method: http.MethodGet,
					},
				},
			},
			statusCode: http.StatusNotFound,
			body:       "Not found",
			headers: map[string]string{
				"Content-Type": htmlContentType,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := Handle(context.Background(), test.request)
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}

			if response.StatusCode != test.statusCode {
				t.Fatalf("status code = %d, want %d", response.StatusCode, test.statusCode)
			}
			if test.body != "" && response.Body != test.body {
				t.Fatalf("body = %q, want %q", response.Body, test.body)
			}
			for _, want := range test.bodyContains {
				if !strings.Contains(response.Body, want) {
					t.Fatalf("body does not contain %q: %q", want, response.Body)
				}
			}
			for key, want := range test.headers {
				if got := response.Headers[key]; got != want {
					t.Fatalf("header %s = %q, want %q", key, got, want)
				}
			}
		})
	}
}
