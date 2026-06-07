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
	`<html lang="en" class="scroll-smooth">`,
	`<title>Thailand Gift Shop</title>`,
	`<link rel="icon" href="/static/favicon.ico" sizes="any">`,
	`<link rel="icon" href="/static/favicon.svg" type="image/svg+xml">`,
	`<link rel="apple-touch-icon" href="/static/apple-touch-icon.png" sizes="180x180">`,
	`<link rel="manifest" href="/static/site.webmanifest">`,
	`/static/assets/app.css`,
	`/static/vendor/htmx.min.js`,
	`src="/static/logo.svg"`,
	`src="/static/home-hero.png"`,
	`aria-label="Main navigation"`,
	`href="#latest"`,
	`href="#shop-aisles"`,
	`href="#categories"`,
	`href="#story"`,
	`Bangkok gift shop online`,
	`Thai snacks, souvenirs`,
	`Latest products`,
	`No products are available yet.`,
	`No categories are available yet.`,
}

func TestRouteForPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want pageRoute
	}{
		{
			name: "home",
			path: "/",
			want: pageRoute{kind: pageHome, knownPageShape: true},
		},
		{
			name: "products",
			path: "/products",
			want: pageRoute{kind: pageProducts, knownPageShape: true},
		},
		{
			name: "product detail",
			path: "/products/mango-sticky-rice-kit",
			want: pageRoute{kind: pageProductDetail, slug: "mango-sticky-rice-kit", knownPageShape: true},
		},
		{
			name: "categories",
			path: "/categories",
			want: pageRoute{kind: pageCategories, knownPageShape: true},
		},
		{
			name: "category detail",
			path: "/categories/pantry",
			want: pageRoute{kind: pageCategoryDetail, slug: "pantry", knownPageShape: true},
		},
		{
			name: "story",
			path: "/story",
			want: pageRoute{kind: pageStory, knownPageShape: true},
		},
		{
			name: "shop redirect",
			path: "/shop",
			want: pageRoute{kind: pageProducts, redirectTo: "/products", knownPageShape: true},
		},
		{
			name: "about redirect",
			path: "/about",
			want: pageRoute{kind: pageStory, redirectTo: "/story", knownPageShape: true},
		},
		{
			name: "products trailing slash redirect",
			path: "/products/",
			want: pageRoute{kind: pageProducts, redirectTo: "/products", knownPageShape: true},
		},
		{
			name: "product detail trailing slash redirect",
			path: "/products/mango-sticky-rice-kit/",
			want: pageRoute{kind: pageProductDetail, slug: "mango-sticky-rice-kit", redirectTo: "/products/mango-sticky-rice-kit", knownPageShape: true},
		},
		{
			name: "categories trailing slash redirect",
			path: "/categories/",
			want: pageRoute{kind: pageCategories, redirectTo: "/categories", knownPageShape: true},
		},
		{
			name: "category detail trailing slash redirect",
			path: "/categories/pantry/",
			want: pageRoute{kind: pageCategoryDetail, slug: "pantry", redirectTo: "/categories/pantry", knownPageShape: true},
		},
		{
			name: "story trailing slash redirect",
			path: "/story/",
			want: pageRoute{kind: pageStory, redirectTo: "/story", knownPageShape: true},
		},
		{
			name: "unknown",
			path: "/missing",
			want: pageRoute{kind: pageUnknown},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := routeForPath(test.path); got != test.want {
				t.Fatalf("routeForPath(%q) = %#v, want %#v", test.path, got, test.want)
			}
		})
	}
}

func TestRouteForPathRejectsExtraSegments(t *testing.T) {
	for _, path := range []string{"/products/a/b", "/categories/a/b"} {
		t.Run(path, func(t *testing.T) {
			if got := routeForPath(path); got != (pageRoute{kind: pageUnknown}) {
				t.Fatalf("routeForPath(%q) = %#v, want unknown", path, got)
			}

			response, err := Handle(context.Background(), pageRequest(http.MethodGet, path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusNotFound)
			}
			if response.Body != "Not found" {
				t.Fatalf("body = %q, want %q", response.Body, "Not found")
			}
			if got := response.Headers["Location"]; got != "" {
				t.Fatalf("Location = %q, want empty", got)
			}
		})
	}
}

func TestRedirects(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		path     string
		location string
	}{
		{name: "shop", method: http.MethodGet, path: "/shop", location: "/products"},
		{name: "about", method: http.MethodGet, path: "/about", location: "/story"},
		{name: "products trailing slash", method: http.MethodGet, path: "/products/", location: "/products"},
		{name: "product detail trailing slash", method: http.MethodGet, path: "/products/mango-sticky-rice-kit/", location: "/products/mango-sticky-rice-kit"},
		{name: "categories trailing slash", method: http.MethodGet, path: "/categories/", location: "/categories"},
		{name: "category detail trailing slash", method: http.MethodGet, path: "/categories/pantry/", location: "/categories/pantry"},
		{name: "story trailing slash", method: http.MethodGet, path: "/story/", location: "/story"},
		{name: "head redirect has no body", method: http.MethodHead, path: "/shop", location: "/products"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := Handle(context.Background(), pageRequest(test.method, test.path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusPermanentRedirect {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusPermanentRedirect)
			}
			if got := response.Headers["Location"]; got != test.location {
				t.Fatalf("Location = %q, want %q", got, test.location)
			}
			if got := response.Headers["Content-Type"]; got != htmlContentType {
				t.Fatalf("Content-Type = %q, want %q", got, htmlContentType)
			}
			if test.method == http.MethodHead && response.Body != "" {
				t.Fatalf("HEAD body = %q, want empty", response.Body)
			}
		})
	}
}

func TestKnownPageMethods(t *testing.T) {
	for _, path := range []string{
		"/",
		"/products",
		"/products/mango-sticky-rice-kit",
		"/categories",
		"/categories/pantry",
		"/story",
		"/shop",
		"/about",
		"/products/",
		"/products/mango-sticky-rice-kit/",
		"/categories/",
		"/categories/pantry/",
		"/story/",
	} {
		t.Run(path, func(t *testing.T) {
			response, err := Handle(context.Background(), pageRequest(http.MethodPost, path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusMethodNotAllowed)
			}
			if response.Body != "Method not allowed" {
				t.Fatalf("body = %q, want %q", response.Body, "Method not allowed")
			}
			if got := response.Headers["Allow"]; got != allowedMethods {
				t.Fatalf("Allow = %q, want %q", got, allowedMethods)
			}
		})
	}

	t.Run("unknown extra segments are not method rejected", func(t *testing.T) {
		response, err := Handle(context.Background(), pageRequest(http.MethodPost, "/products/a/b"))
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusNotFound)
		}
	})
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
			name: "root returns home page",
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
				RawPath: "/missing",
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

func pageRequest(method string, path string) events.APIGatewayV2HTTPRequest {
	return events.APIGatewayV2HTTPRequest{
		RawPath: path,
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{
				Method: method,
			},
		},
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
		`/static/logo.svg`,
		`/static/home-hero.png`,
		`Bangkok gift shop online`,
		`Latest products`,
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
				Name:          "Mango Sticky Rice Treats",
				Description:   "Shelf-stable Thai dessert snacks.",
				PriceCents:    2899,
				ImageURL:      "/images/products/mango-sticky-rice-kit.jpg",
				Status:        catalog.StatusActive,
				StockQuantity: 3,
			},
			{
				ID:            "prod_002",
				Slug:          "thai-tea-sampler",
				Name:          "Thai Tea Selection",
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
		`href="/products/mango-sticky-rice-kit"`,
		`href="/products/thai-tea-sampler"`,
		`src="/images/products/mango-sticky-rice-kit.jpg"`,
		`src="/images/placeholder-product.jpg"`,
		`Mango Sticky Rice Treats`,
		`$28.99`,
		`In stock`,
		`Out of stock`,
	} {
		if !strings.Contains(response.Body, want) {
			t.Fatalf("body does not contain %q: %q", want, response.Body)
		}
	}
}

func TestHomeRendersCategoryCardsFromCatalog(t *testing.T) {
	handler := NewHandler(&fakeCatalogStore{
		categories: []catalog.Category{
			{
				Slug:        "temple-bells",
				Name:        "Temple Bells",
				Description: "Small brass bells and shrine-side keepsakes.",
				Status:      catalog.StatusActive,
			},
			{
				Slug:        "jasmine-garlands",
				Name:        "Jasmine Garlands",
				Description: "Fragrant market-inspired gifts and floral mementos.",
				Status:      catalog.StatusActive,
			},
		},
	})

	response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	for _, want := range []string{
		`data-testid="category-card"`,
		`href="/categories/temple-bells"`,
		`Temple Bells`,
		`Small brass bells and shrine-side keepsakes.`,
		`href="/categories/jasmine-garlands"`,
		`Jasmine Garlands`,
	} {
		if !strings.Contains(response.Body, want) {
			t.Fatalf("body does not contain %q: %q", want, response.Body)
		}
	}
	for _, unwanted := range []string{
		`href="/categories/souvenirs"`,
		`data-testid="category-card" href="#latest"`,
		`No categories are available yet.`,
	} {
		if strings.Contains(response.Body, unwanted) {
			t.Fatalf("body unexpectedly contains %q: %q", unwanted, response.Body)
		}
	}
}

func TestHomeRendersEmptyCategoryState(t *testing.T) {
	handler := NewHandler(&fakeCatalogStore{})

	response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if !strings.Contains(response.Body, `No categories are available yet.`) {
		t.Fatalf("body does not contain empty category state: %q", response.Body)
	}
	if strings.Contains(response.Body, `data-testid="category-card"`) {
		t.Fatalf("body contains category card despite empty categories: %q", response.Body)
	}
}

func TestProductListingRendersActiveProductsAndCategoryLinks(t *testing.T) {
	store := &fakeCatalogStore{
		products: []catalog.Product{
			{
				ID:            "prod_active_001",
				Slug:          "mango-sticky-rice-kit",
				Name:          "Mango Sticky Rice Treats",
				Description:   "Shelf-stable Thai dessert snacks.",
				PriceCents:    2899,
				ImageURL:      "/images/products/mango-sticky-rice-kit.jpg",
				Status:        catalog.StatusActive,
				StockQuantity: 4,
			},
			{
				ID:            "prod_draft_001",
				Slug:          "draft-product",
				Name:          "Draft Product",
				Description:   "Should not be visible.",
				PriceCents:    1099,
				Status:        catalog.StatusDraft,
				StockQuantity: 2,
			},
		},
		categories: []catalog.Category{
			{
				Slug:        "thai-snacks",
				Name:        "Thai Snacks",
				Description: "Crunchy, sweet, and pantry-friendly finds.",
				Status:      catalog.StatusActive,
			},
			{
				Slug:        "hidden-category",
				Name:        "Hidden Category",
				Description: "Should not be visible.",
				Status:      catalog.StatusDraft,
			},
		},
	}
	handler := NewHandlerWithProductImagePlaceholderURL(store, "/images/placeholder-product.jpg")

	response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/products"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if len(store.activeProductLimits) != 1 || store.activeProductLimits[0] != 0 {
		t.Fatalf("ListActiveProducts limits = %v, want [0]", store.activeProductLimits)
	}
	assertBodyContains(t, response.Body, []string{
		`<h1 class="mt-3 text-5xl font-black leading-none text-[#2D2A4A] sm:text-6xl">Products</h1>`,
		`Browse Thai snacks, souvenirs, textiles, pantry items, decor, wellness, and small keepsakes.`,
		`href="/products/mango-sticky-rice-kit"`,
		`src="/images/products/mango-sticky-rice-kit.jpg"`,
		`Mango Sticky Rice Treats`,
		`$28.99`,
		`In stock`,
		`Browse categories`,
		`href="/categories/thai-snacks"`,
		`Thai Snacks`,
	})
	assertBodyOmits(t, response.Body, []string{
		`href="/products/draft-product"`,
		`Draft Product`,
		`prod_active_001`,
		`/products/prod_`,
		`href="/categories/hidden-category"`,
		`Hidden Category`,
	})
}

func TestProductListingRendersEmptyState(t *testing.T) {
	handler := NewHandler(&fakeCatalogStore{})

	response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/products"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, response.Body, []string{
		`No products are available yet.`,
		`Check back soon for Thai gift-shop finds.`,
	})
	assertBodyOmits(t, response.Body, []string{`data-testid="product-card"`, `Browse categories`})
}

func TestProductDetailRendersExactActiveProduct(t *testing.T) {
	store := &fakeCatalogStore{
		productsBySlug: map[string]catalog.Product{
			"mango-sticky-rice-kit": {
				ID:            "prod_active_002",
				Slug:          "mango-sticky-rice-kit",
				Name:          "Mango Sticky Rice Treats",
				Description:   "Shelf-stable Thai dessert snacks.",
				PriceCents:    2899,
				ImageURL:      "/images/products/mango-sticky-rice-kit.jpg",
				Status:        catalog.StatusActive,
				StockQuantity: 0,
			},
			"mango": {
				ID:            "prod_active_003",
				Slug:          "mango",
				Name:          "Wrong Mango",
				Description:   "Should not be returned for longer slug.",
				PriceCents:    999,
				Status:        catalog.StatusActive,
				StockQuantity: 5,
			},
		},
	}
	handler := NewHandlerWithProductImagePlaceholderURL(store, "/images/placeholder-product.jpg")

	response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/products/mango-sticky-rice-kit"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if len(store.productSlugLookups) != 1 || store.productSlugLookups[0] != "mango-sticky-rice-kit" {
		t.Fatalf("GetProductBySlug lookups = %v, want exact slug", store.productSlugLookups)
	}
	assertBodyContains(t, response.Body, []string{
		`href="/products"`,
		`Back to products`,
		`<h1 class="mt-5 text-5xl font-black leading-none text-[#2D2A4A] sm:text-6xl">Mango Sticky Rice Treats</h1>`,
		`src="/images/products/mango-sticky-rice-kit.jpg"`,
		`alt="Mango Sticky Rice Treats"`,
		`$28.99`,
		`Shelf-stable Thai dessert snacks.`,
		`Out of stock`,
	})
	assertBodyOmits(t, response.Body, []string{
		`Wrong Mango`,
		`prod_active_002`,
		`Add to cart`,
		`Checkout`,
		`Quantity`,
	})
}

func TestProductDetailMissingDraftAndInactiveSlugsReturn404(t *testing.T) {
	handler := NewHandler(&fakeCatalogStore{
		productsBySlug: map[string]catalog.Product{
			"draft-product": {
				Slug:   "draft-product",
				Name:   "Draft Product",
				Status: catalog.StatusDraft,
			},
			"archived-product": {
				Slug:   "archived-product",
				Name:   "Archived Product",
				Status: catalog.StatusArchived,
			},
		},
	})

	for _, path := range []string{"/products/missing-product", "/products/draft-product", "/products/archived-product"} {
		t.Run(path, func(t *testing.T) {
			response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusNotFound)
			}
			if response.Body != "Not found" {
				t.Fatalf("body = %q, want Not found", response.Body)
			}
		})
	}
}

func TestHandleProductsSupportsHeadAndRejectsPost(t *testing.T) {
	handler := NewHandler(&fakeCatalogStore{
		products: []catalog.Product{
			{
				Slug:          "thai-tea-sampler",
				Name:          "Thai Tea Selection",
				Description:   "Loose leaf Thai tea and sweet snacks.",
				PriceCents:    2199,
				Status:        catalog.StatusActive,
				StockQuantity: 3,
			},
		},
		productsBySlug: map[string]catalog.Product{
			"thai-tea-sampler": {
				Slug:          "thai-tea-sampler",
				Name:          "Thai Tea Selection",
				Description:   "Loose leaf Thai tea and sweet snacks.",
				PriceCents:    2199,
				Status:        catalog.StatusActive,
				StockQuantity: 3,
			},
		},
	})

	for _, path := range []string{"/products", "/products/thai-tea-sampler"} {
		t.Run("head "+path, func(t *testing.T) {
			response, err := handler.Handle(context.Background(), pageRequest(http.MethodHead, path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
			}
			if response.Body != "" {
				t.Fatalf("HEAD body = %q, want empty", response.Body)
			}
		})

		t.Run("post "+path, func(t *testing.T) {
			response, err := handler.Handle(context.Background(), pageRequest(http.MethodPost, path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusMethodNotAllowed)
			}
			if got := response.Headers["Allow"]; got != allowedMethods {
				t.Fatalf("Allow = %q, want %q", got, allowedMethods)
			}
			if response.Body != "Method not allowed" {
				t.Fatalf("body = %q, want Method not allowed", response.Body)
			}
		})
	}
}

func TestProductPagesEscapeCatalogText(t *testing.T) {
	product := catalog.Product{
		Slug:          "scripted-product",
		Name:          `<script>alert(1)</script> Tea`,
		Description:   `Description with <script>alert(2)</script> markup.`,
		PriceCents:    1299,
		Status:        catalog.StatusActive,
		StockQuantity: 7,
	}
	handler := NewHandler(&fakeCatalogStore{
		products: []catalog.Product{product},
		productsBySlug: map[string]catalog.Product{
			"scripted-product": product,
		},
	})

	for _, path := range []string{"/products", "/products/scripted-product"} {
		t.Run(path, func(t *testing.T) {
			response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
			}
			assertBodyContains(t, response.Body, []string{
				`&lt;script&gt;alert(1)&lt;/script&gt; Tea`,
				`Description with &lt;script&gt;alert(2)&lt;/script&gt; markup.`,
			})
			assertBodyOmits(t, response.Body, []string{
				`<script>alert(1)</script>`,
				`<script>alert(2)</script>`,
			})
		})
	}
}

func TestCategoriesIndexRendersActiveCategoriesAndDataDrivenLinks(t *testing.T) {
	handler := NewHandler(&fakeCatalogStore{
		categories: []catalog.Category{
			{
				Slug:        "temple-bells",
				Name:        "Temple Bells",
				Description: "Small brass bells and shrine-side keepsakes.",
				Status:      catalog.StatusActive,
			},
			{
				Slug:        "jasmine-garlands",
				Name:        "Jasmine Garlands",
				Description: "Fragrant market-inspired gifts and floral mementos.",
				Status:      catalog.StatusActive,
			},
			{
				Slug:        "hidden-category",
				Name:        "Hidden Category",
				Description: "Should not be visible.",
				Status:      catalog.StatusDraft,
			},
		},
	})

	response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/categories"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, response.Body, []string{
		`<h1 class="mt-3 text-5xl font-black leading-none text-[#2D2A4A] sm:text-6xl">Categories</h1>`,
		`Shop Thai gift-shop finds by aisle.`,
		`data-testid="category-card"`,
		`href="/categories/temple-bells"`,
		`Temple Bells`,
		`Small brass bells and shrine-side keepsakes.`,
		`href="/categories/jasmine-garlands"`,
		`Jasmine Garlands`,
	})
	assertBodyOmits(t, response.Body, []string{
		`href="/categories/hidden-category"`,
		`Hidden Category`,
		`No categories are available yet.`,
	})
}

func TestCategoriesIndexRendersEmptyState(t *testing.T) {
	handler := NewHandler(&fakeCatalogStore{})

	response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/categories"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, response.Body, []string{
		`No categories are available yet.`,
		`Check back soon for more Thai gift-shop aisles.`,
	})
	assertBodyOmits(t, response.Body, []string{`data-testid="category-card"`})
}

func TestCategoryDetailRendersCategoryAndActiveProducts(t *testing.T) {
	store := &fakeCatalogStore{
		categories: []catalog.Category{
			{
				Slug:        "thai-snacks",
				Name:        "Thai Snacks",
				Description: "Crunchy, sweet, and pantry-friendly finds.",
				Status:      catalog.StatusActive,
			},
		},
		categoryProducts: map[string][]catalog.Product{
			"thai-snacks": {
				{
					ID:            "prod_active_101",
					Slug:          "thai-tea-sampler",
					Name:          "Thai Tea Selection",
					Description:   "Loose leaf Thai tea and sweet snacks.",
					PriceCents:    2199,
					Status:        catalog.StatusActive,
					StockQuantity: 3,
				},
				{
					ID:            "prod_draft_101",
					Slug:          "draft-snack",
					Name:          "Draft Snack",
					Description:   "Should not be visible.",
					PriceCents:    999,
					Status:        catalog.StatusDraft,
					StockQuantity: 2,
				},
			},
		},
	}
	handler := NewHandlerWithProductImagePlaceholderURL(store, "/images/placeholder-product.jpg")

	response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/categories/thai-snacks"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if len(store.categoryProductLookups) != 1 || store.categoryProductLookups[0] != "thai-snacks" {
		t.Fatalf("ListActiveProductsByCategory lookups = %v, want [thai-snacks]", store.categoryProductLookups)
	}
	if len(store.categoryProductLimits) != 1 || store.categoryProductLimits[0] != 0 {
		t.Fatalf("ListActiveProductsByCategory limits = %v, want [0]", store.categoryProductLimits)
	}
	assertBodyContains(t, response.Body, []string{
		`href="/categories"`,
		`Back to categories`,
		`<h1 class="mt-5 text-5xl font-black leading-none text-[#2D2A4A] sm:text-6xl">Thai Snacks</h1>`,
		`Crunchy, sweet, and pantry-friendly finds.`,
		`href="/products/thai-tea-sampler"`,
		`Thai Tea Selection`,
		`Loose leaf Thai tea and sweet snacks.`,
		`$21.99`,
		`In stock`,
	})
	assertBodyOmits(t, response.Body, []string{
		`href="/products/draft-snack"`,
		`Draft Snack`,
		`prod_active_101`,
		`/products/prod_`,
	})
}

func TestCategoryDetailValidSlugWithNoProductsRendersEmptyState(t *testing.T) {
	store := &fakeCatalogStore{
		categories: []catalog.Category{
			{
				Slug:        "temple-bells",
				Name:        "Temple Bells",
				Description: "Small brass bells and shrine-side keepsakes.",
				Status:      catalog.StatusActive,
			},
		},
		categoryProducts: map[string][]catalog.Product{
			"temple-bells": {},
		},
	}
	handler := NewHandler(store)

	response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/categories/temple-bells"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if len(store.categoryProductLookups) != 1 || store.categoryProductLookups[0] != "temple-bells" {
		t.Fatalf("ListActiveProductsByCategory lookups = %v, want [temple-bells]", store.categoryProductLookups)
	}
	assertBodyContains(t, response.Body, []string{
		`Temple Bells`,
		`No products are available in this category yet.`,
		`Check back soon for more Thai gift-shop finds in this aisle.`,
	})
	assertBodyOmits(t, response.Body, []string{`data-testid="product-card"`})
}

func TestCategoryDetailMissingInactiveAndUnlistedSlugsReturn404BeforeProductLookup(t *testing.T) {
	store := &fakeCatalogStore{
		categories: []catalog.Category{
			{
				Slug:        "thai-snacks",
				Name:        "Thai Snacks",
				Description: "Crunchy, sweet, and pantry-friendly finds.",
				Status:      catalog.StatusActive,
			},
			{
				Slug:        "hidden-category",
				Name:        "Hidden Category",
				Description: "Should not be visible.",
				Status:      catalog.StatusDraft,
			},
		},
		categoryProducts: map[string][]catalog.Product{
			"temple-bells": {
				{
					Slug:   "temple-bell-product",
					Name:   "Temple Bell Product",
					Status: catalog.StatusActive,
				},
			},
		},
	}
	handler := NewHandler(store)

	for _, path := range []string{"/categories/missing-category", "/categories/hidden-category", "/categories/temple-bells"} {
		t.Run(path, func(t *testing.T) {
			response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusNotFound)
			}
			if response.Body != "Not found" {
				t.Fatalf("body = %q, want Not found", response.Body)
			}
			if len(store.categoryProductLookups) != 0 {
				t.Fatalf("ListActiveProductsByCategory lookups = %v, want none before slug validation", store.categoryProductLookups)
			}
		})
	}
}

func TestCategoryPagesSupportHeadAndRejectPost(t *testing.T) {
	store := &fakeCatalogStore{
		categories: []catalog.Category{
			{
				Slug:        "temple-bells",
				Name:        "Temple Bells",
				Description: "Small brass bells and shrine-side keepsakes.",
				Status:      catalog.StatusActive,
			},
		},
		categoryProducts: map[string][]catalog.Product{"temple-bells": {}},
	}
	handler := NewHandler(store)

	for _, path := range []string{"/categories", "/categories/temple-bells"} {
		t.Run("head "+path, func(t *testing.T) {
			response, err := handler.Handle(context.Background(), pageRequest(http.MethodHead, path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
			}
			if response.Body != "" {
				t.Fatalf("HEAD body = %q, want empty", response.Body)
			}
		})

		t.Run("post "+path, func(t *testing.T) {
			response, err := handler.Handle(context.Background(), pageRequest(http.MethodPost, path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusMethodNotAllowed)
			}
			if got := response.Headers["Allow"]; got != allowedMethods {
				t.Fatalf("Allow = %q, want %q", got, allowedMethods)
			}
			if response.Body != "Method not allowed" {
				t.Fatalf("body = %q, want Method not allowed", response.Body)
			}
		})
	}
}

func TestCategoryPagesEscapeCatalogText(t *testing.T) {
	store := &fakeCatalogStore{
		categories: []catalog.Category{
			{
				Slug:        "scripted-category",
				Name:        `<script>alert(1)</script> Aisle`,
				Description: `Description with <script>alert(2)</script> markup.`,
				Status:      catalog.StatusActive,
			},
		},
		categoryProducts: map[string][]catalog.Product{"scripted-category": {}},
	}
	handler := NewHandler(store)

	for _, path := range []string{"/categories", "/categories/scripted-category"} {
		t.Run(path, func(t *testing.T) {
			response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
			}
			assertBodyContains(t, response.Body, []string{
				`&lt;script&gt;alert(1)&lt;/script&gt; Aisle`,
				`Description with &lt;script&gt;alert(2)&lt;/script&gt; markup.`,
			})
			assertBodyOmits(t, response.Body, []string{
				`<script>alert(1)</script>`,
				`<script>alert(2)</script>`,
			})
		})
	}
}

func TestStoryReturnsStaticPageWithoutCatalogQuery(t *testing.T) {
	handler := NewHandler(&fakeCatalogStore{err: errors.New("story should not query catalog")})

	response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/story"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if got := response.Headers["Content-Type"]; got != htmlContentType {
		t.Fatalf("Content-Type = %q, want %q", got, htmlContentType)
	}
	assertBodyContains(t, response.Body, []string{
		`<!doctype html>`,
		`<html lang="en" class="scroll-smooth">`,
		`<title>Our Story | Thailand Gift Shop</title>`,
		`src="/static/logo.svg"`,
		`aria-label="Main navigation"`,
		`href="/products"`,
		`href="/categories"`,
		`href="/story"`,
		`<h1 class="mt-3 text-5xl font-black leading-none text-[#2D2A4A] sm:text-6xl">Our Story</h1>`,
		`Bangkok gift shop online`,
		`Thai snacks, souvenirs, textiles, pantry items, decor, wellness, and small keepsakes`,
		`Thailand Gift Shop is a Bangkok gift shop online for Thai snacks, souvenirs, textiles, pantry items, decor, wellness, and small keepsakes.`,
		`Each aisle is shaped for calm browsing: clear categories, strong product images, concise details, and slug-based links that work without JavaScript.`,
		`The shop point of view is market-bright and practical, rooted in the colors, textures, pantry flavors, and compact keepsakes travelers remember from Bangkok gift shops.`,
	})
}

func TestStorySupportsHeadAndRejectsPost(t *testing.T) {
	handler := NewHandler(&fakeCatalogStore{err: errors.New("story should not query catalog")})

	headResponse, err := handler.Handle(context.Background(), pageRequest(http.MethodHead, "/story"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if headResponse.StatusCode != http.StatusOK {
		t.Fatalf("HEAD status code = %d, want %d", headResponse.StatusCode, http.StatusOK)
	}
	if headResponse.Body != "" {
		t.Fatalf("HEAD body = %q, want empty", headResponse.Body)
	}

	postResponse, err := handler.Handle(context.Background(), pageRequest(http.MethodPost, "/story"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if postResponse.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status code = %d, want %d", postResponse.StatusCode, http.StatusMethodNotAllowed)
	}
	if got := postResponse.Headers["Allow"]; got != allowedMethods {
		t.Fatalf("Allow = %q, want %q", got, allowedMethods)
	}
	if postResponse.Body != "Method not allowed" {
		t.Fatalf("POST body = %q, want Method not allowed", postResponse.Body)
	}
}

func TestStoryAboutRedirect(t *testing.T) {
	response, err := Handle(context.Background(), pageRequest(http.MethodGet, "/about"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusPermanentRedirect {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusPermanentRedirect)
	}
	if got := response.Headers["Location"]; got != "/story" {
		t.Fatalf("Location = %q, want /story", got)
	}
}

func TestHandleRouteMatrix(t *testing.T) {
	handler := NewHandlerWithProductImagePlaceholderURL(routeMatrixStore(), "/images/placeholder-product.jpg")
	tests := []struct {
		name       string
		path       string
		statusCode int
		location   string
	}{
		{name: "home", path: "/", statusCode: http.StatusOK},
		{name: "products", path: "/products", statusCode: http.StatusOK},
		{name: "product detail", path: "/products/thai-tea-sampler", statusCode: http.StatusOK},
		{name: "categories", path: "/categories", statusCode: http.StatusOK},
		{name: "category detail", path: "/categories/thai-snacks", statusCode: http.StatusOK},
		{name: "story", path: "/story", statusCode: http.StatusOK},
		{name: "shop redirect", path: "/shop", statusCode: http.StatusPermanentRedirect, location: "/products"},
		{name: "about redirect", path: "/about", statusCode: http.StatusPermanentRedirect, location: "/story"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, test.path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != test.statusCode {
				t.Fatalf("status code = %d, want %d", response.StatusCode, test.statusCode)
			}
			if test.location != "" && response.Headers["Location"] != test.location {
				t.Fatalf("Location = %q, want %q", response.Headers["Location"], test.location)
			}
		})
	}
}

func TestNoExcludedUI(t *testing.T) {
	handler := NewHandlerWithProductImagePlaceholderURL(routeMatrixStore(), "/images/placeholder-product.jpg")
	for _, path := range []string{"/story", "/products", "/categories", "/categories/thai-snacks"} {
		t.Run(path, func(t *testing.T) {
			response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
			}
			assertBodyOmits(t, response.Body, excludedUILabels())
		})
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

func routeMatrixStore() *fakeCatalogStore {
	product := catalog.Product{
		ID:            "prod_matrix_001",
		Slug:          "thai-tea-sampler",
		Name:          "Thai Tea Selection",
		Description:   "Loose leaf Thai tea and sweet snacks.",
		PriceCents:    2199,
		Status:        catalog.StatusActive,
		StockQuantity: 3,
	}
	category := catalog.Category{
		Slug:        "thai-snacks",
		Name:        "Thai Snacks",
		Description: "Crunchy, sweet, and pantry-friendly finds.",
		Status:      catalog.StatusActive,
	}

	return &fakeCatalogStore{
		products: []catalog.Product{product},
		productsBySlug: map[string]catalog.Product{
			product.Slug: product,
		},
		categories: []catalog.Category{category},
		categoryProducts: map[string][]catalog.Product{
			category.Slug: {product},
		},
	}
}

func excludedUILabels() []string {
	return []string{
		"Cart",
		"Checkout",
		"Account",
		"Search",
		"Ready to wrap",
		"Gift set",
	}
}

type fakeCatalogStore struct {
	products               []catalog.Product
	productsBySlug         map[string]catalog.Product
	categories             []catalog.Category
	categoryProducts       map[string][]catalog.Product
	err                    error
	activeProductLimits    []int
	productSlugLookups     []string
	categoryProductLookups []string
	categoryProductLimits  []int
}

func (f *fakeCatalogStore) ListActiveProducts(ctx context.Context, limit int) ([]catalog.Product, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.activeProductLimits = append(f.activeProductLimits, limit)
	products := make([]catalog.Product, 0, len(f.products))
	for _, product := range f.products {
		if product.Status != catalog.StatusActive {
			continue
		}
		products = append(products, product)
		if limit > 0 && len(products) >= limit {
			return products, nil
		}
	}
	return products, nil
}

func (f *fakeCatalogStore) ListRecentlyAddedProducts(ctx context.Context, limit int) ([]catalog.Product, error) {
	if f.err != nil {
		return nil, f.err
	}
	products := make([]catalog.Product, 0, len(f.products))
	for _, product := range f.products {
		if product.Status != catalog.StatusActive {
			continue
		}
		products = append(products, product)
		if limit > 0 && len(products) >= limit {
			return products, nil
		}
	}
	return products, nil
}

func (f *fakeCatalogStore) GetProductBySlug(ctx context.Context, slug string) (catalog.Product, bool, error) {
	if f.err != nil {
		return catalog.Product{}, false, f.err
	}
	f.productSlugLookups = append(f.productSlugLookups, slug)
	if f.productsBySlug != nil {
		product, found := f.productsBySlug[slug]
		return product, found, nil
	}
	for _, product := range f.products {
		if product.Slug == slug {
			return product, true, nil
		}
	}
	return catalog.Product{}, false, nil
}

func (f *fakeCatalogStore) ListActiveCategories(ctx context.Context) ([]catalog.Category, error) {
	if f.err != nil {
		return nil, f.err
	}
	categories := make([]catalog.Category, 0, len(f.categories))
	for _, category := range f.categories {
		if category.Status == catalog.StatusActive {
			categories = append(categories, category)
		}
	}
	return categories, nil
}

func (f *fakeCatalogStore) ListActiveProductsByCategory(ctx context.Context, categorySlug string, limit int) ([]catalog.Product, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.categoryProductLookups = append(f.categoryProductLookups, categorySlug)
	f.categoryProductLimits = append(f.categoryProductLimits, limit)

	var source []catalog.Product
	if f.categoryProducts != nil {
		source = f.categoryProducts[categorySlug]
	} else {
		source = f.products
	}
	products := make([]catalog.Product, 0, len(source))
	for _, product := range source {
		if product.Status != catalog.StatusActive {
			continue
		}
		products = append(products, product)
		if limit > 0 && len(products) >= limit {
			return products, nil
		}
	}
	return products, nil
}

func assertBodyContains(t *testing.T, body string, wants []string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Fatalf("body does not contain %q: %q", want, body)
		}
	}
}

func assertBodyOmits(t *testing.T, body string, unwanteds []string) {
	t.Helper()
	for _, unwanted := range unwanteds {
		if strings.Contains(body, unwanted) {
			t.Fatalf("body unexpectedly contains %q: %q", unwanted, body)
		}
	}
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
	if !strings.Contains(response.Body, "<!doctype html>") || !strings.Contains(response.Body, `<html lang="en"`) || !strings.Contains(response.Body, "No products are available yet.") {
		t.Fatalf("fallback response does not contain full-page shell: %q", response.Body)
	}
}
