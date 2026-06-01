package ssr

import (
	"context"
	"net/http"
	"testing"

	"github.com/aws/aws-lambda-go/events"
)

func TestHandle(t *testing.T) {
	tests := []struct {
		name       string
		request    events.APIGatewayV2HTTPRequest
		statusCode int
		body       string
		headers    map[string]string
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
			statusCode: http.StatusOK,
			body:       "Hello, world!",
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
			if response.Body != test.body {
				t.Fatalf("body = %q, want %q", response.Body, test.body)
			}
			for key, want := range test.headers {
				if got := response.Headers[key]; got != want {
					t.Fatalf("header %s = %q, want %q", key, got, want)
				}
			}
		})
	}
}
