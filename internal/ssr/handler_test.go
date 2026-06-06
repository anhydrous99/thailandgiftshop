package ssr

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
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
	`/static/assets/app.css`,
	`/static/vendor/htmx.min.js`,
	`Thai gifts, pantry favorites`,
	`No products are available yet.`,
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
			name: "root returns catalog shell",
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
			name: "root supports head without body",
			request: events.APIGatewayV2HTTPRequest{
				RawPath: "/",
				RequestContext: events.APIGatewayV2HTTPRequestContext{
					HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{
						Method: http.MethodHead,
					},
				},
			},
			statusCode: http.StatusOK,
			body:       "",
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
				"Allow":        allowedMethods,
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

func TestNewHandlerUsesInjectedCatalogStore(t *testing.T) {
	store := &fakeCatalogStore{}
	handler := NewHandler(store)

	if handler.Catalog() != store {
		t.Fatal("handler did not retain injected catalog store")
	}

	response, err := handler.Handle(context.Background(), events.APIGatewayV2HTTPRequest{
		RawPath: "/",
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{
				Method: http.MethodGet,
			},
		},
	})
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
}

func TestHomeIncludesFrontendAssets(t *testing.T) {
	response, err := Handle(context.Background(), events.APIGatewayV2HTTPRequest{
		RawPath: "/",
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{
				Method: http.MethodGet,
			},
		},
	})
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}

	for _, want := range []string{
		`/static/assets/app.css`,
		`/static/vendor/htmx.min.js`,
		`Thai gifts, pantry favorites`,
		`No products are available yet.`,
	} {
		if !strings.Contains(response.Body, want) {
			t.Fatalf("body does not contain %q: %q", want, response.Body)
		}
	}
}

func TestHomeRendersProductImageURLsFromCatalog(t *testing.T) {
	handler := NewHandlerWithProductImagePlaceholderURL(&fakeCatalogStore{
		products: []catalog.Product{
			{
				ID:            "prod_001",
				Slug:          "mango-sticky-rice-kit",
				Name:          "Mango Sticky Rice Kit",
				Description:   "A shelf-stable dessert gift kit.",
				PriceCents:    2899,
				ImageURL:      "/images/products/mango-sticky-rice-kit.jpg",
				Status:        catalog.StatusActive,
				StockQuantity: 3,
			},
			{
				ID:            "prod_002",
				Slug:          "thai-tea-sampler",
				Name:          "Thai Tea Sampler",
				Description:   "Loose leaf Thai tea and sweet snacks.",
				PriceCents:    2199,
				Status:        catalog.StatusActive,
				StockQuantity: 0,
			},
		},
	}, "/images/placeholder-product.jpg")

	response, err := handler.Handle(context.Background(), events.APIGatewayV2HTTPRequest{
		RawPath: "/",
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{
				Method: http.MethodGet,
			},
		},
	})
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	for _, want := range []string{
		`src="/images/products/mango-sticky-rice-kit.jpg"`,
		`src="/images/placeholder-product.jpg"`,
		`Mango Sticky Rice Kit`,
		`$28.99`,
		`Out of stock`,
	} {
		if !strings.Contains(response.Body, want) {
			t.Fatalf("body does not contain %q: %q", want, response.Body)
		}
	}
}

func TestHomeReturnsInternalServerErrorWhenCatalogFails(t *testing.T) {
	handler := NewHandler(&fakeCatalogStore{
		err: errors.New("catalog unavailable"),
	})

	response, err := handler.Handle(context.Background(), events.APIGatewayV2HTTPRequest{
		RawPath: "/",
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{
				Method: http.MethodGet,
			},
		},
	})
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusInternalServerError)
	}
	if response.Body != "Internal server error" {
		t.Fatalf("body = %q, want internal error", response.Body)
	}
}

type fakeCatalogStore struct {
	products []catalog.Product
	err      error
}

func (f fakeCatalogStore) ListActiveProducts(ctx context.Context, limit int) ([]catalog.Product, error) {
	if f.err != nil {
		return nil, f.err
	}
	if limit > 0 && len(f.products) > limit {
		return f.products[:limit], nil
	}
	return f.products, nil
}

func (fakeCatalogStore) GetProductBySlug(ctx context.Context, slug string) (catalog.Product, bool, error) {
	return catalog.Product{}, false, nil
}

func (fakeCatalogStore) ListActiveCategories(ctx context.Context) ([]catalog.Category, error) {
	return []catalog.Category{}, nil
}

func (fakeCatalogStore) ListActiveProductsByCategory(ctx context.Context, categorySlug string, limit int) ([]catalog.Product, error) {
	return []catalog.Product{}, nil
}

func TestHelloFragmentHTMX(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
	}{
		{
			name: "canonical HX-Request header",
			headers: map[string]string{
				"HX-Request": "true",
			},
		},
		{
			name: "lowercase hx-request header",
			headers: map[string]string{
				"hx-request": "true",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := Handle(context.Background(), events.APIGatewayV2HTTPRequest{
				RawPath: "/hello-fragment",
				Headers: test.headers,
				RequestContext: events.APIGatewayV2HTTPRequestContext{
					HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{
						Method: http.MethodGet,
					},
				},
			})
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
			}
			if got := response.Headers["Content-Type"]; got != htmlContentType {
				t.Fatalf("Content-Type = %q, want %q", got, htmlContentType)
			}
			if got := response.Headers["Cache-Control"]; got != "no-store" {
				t.Fatalf("Cache-Control = %q, want %q", got, "no-store")
			}
			if got := response.Headers["Vary"]; got != "HX-Request" {
				t.Fatalf("Vary = %q, want %q", got, "HX-Request")
			}
			if !strings.Contains(response.Body, "HTMX refreshed this greeting") {
				t.Fatalf("body does not contain HTMX greeting: %q", response.Body)
			}
			if strings.Contains(response.Body, "<!doctype html>") || strings.Contains(response.Body, "<html") {
				t.Fatalf("fragment body contains full-page shell: %q", response.Body)
			}
		})
	}
}

func TestHelloFragmentRequiresHXRequest(t *testing.T) {
	response, err := Handle(context.Background(), events.APIGatewayV2HTTPRequest{
		RawPath: "/hello-fragment",
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{
				Method: http.MethodGet,
			},
		},
	})
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusNotFound)
	}
}

func TestHomeIgnoresLegacyFallbackGreetingQuery(t *testing.T) {
	response, err := Handle(context.Background(), events.APIGatewayV2HTTPRequest{
		RawPath: "/",
		QueryStringParameters: map[string]string{
			"hello": "fallback",
		},
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{
				Method: http.MethodGet,
			},
		},
	})
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if strings.Contains(response.Body, "Fallback greeting refreshed") {
		t.Fatalf("body contains removed fallback greeting: %q", response.Body)
	}
	if !strings.Contains(response.Body, "<!doctype html>") || !strings.Contains(response.Body, `<html lang="en">`) || !strings.Contains(response.Body, "No products are available yet.") {
		t.Fatalf("fallback response does not contain full-page shell: %q", response.Body)
	}
}
