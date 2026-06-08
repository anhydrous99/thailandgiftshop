package ssr

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/cart"
	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/observability"
	"github.com/aws/aws-lambda-go/events"
)

const ssrTestCartSecret = "ssr-cart-test-secret"

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
	`href="/products"`,
	`href="#latest"`,
	`href="#shop-aisles"`,
	`href="#categories"`,
	`href="#story"`,
	`href="/cart"`,
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
			name: "cart",
			path: "/cart",
			want: pageRoute{kind: pageCart, knownPageShape: true},
		},
		{
			name: "checkout",
			path: "/checkout",
			want: pageRoute{kind: pageCheckout, knownPageShape: true},
		},
		{
			name: "cart items mutation",
			path: "/cart/items",
			want: pageRoute{kind: pageCartItems, knownPageShape: true},
		},
		{
			name: "cart quantity mutation",
			path: "/cart/items/mango-sticky-rice-kit/quantity",
			want: pageRoute{kind: pageCartQuantity, slug: "mango-sticky-rice-kit", knownPageShape: true},
		},
		{
			name: "cart remove mutation",
			path: "/cart/items/mango-sticky-rice-kit/remove",
			want: pageRoute{kind: pageCartRemove, slug: "mango-sticky-rice-kit", knownPageShape: true},
		},
		{
			name: "cart clear mutation",
			path: "/cart/clear",
			want: pageRoute{kind: pageCartClear, knownPageShape: true},
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
			name: "cart trailing slash redirect",
			path: "/cart/",
			want: pageRoute{kind: pageCart, redirectTo: "/cart", knownPageShape: true},
		},
		{
			name: "checkout trailing slash redirect",
			path: "/checkout/",
			want: pageRoute{kind: pageCheckout, redirectTo: "/checkout", knownPageShape: true},
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

func TestSSRMetricsRecordRouteDurationAndColdStartWithoutChangingResponse(t *testing.T) {
	ssrColdStartRecorded.Store(false)
	plainResponse, err := NewHandler(catalog.EmptyStore{}).Handle(context.Background(), pageRequest(http.MethodGet, "/"))
	if err != nil {
		t.Fatalf("plain Handle returned error: %v", err)
	}

	handler := NewHandler(catalog.EmptyStore{})
	recorder := &testMetricRecorder{}
	handler.metrics = recorder
	instrumentedResponse, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/"))
	if err != nil {
		t.Fatalf("instrumented Handle returned error: %v", err)
	}
	if instrumentedResponse.StatusCode != plainResponse.StatusCode || instrumentedResponse.Body != plainResponse.Body {
		t.Fatalf("instrumented response changed: got status %d body length %d, want status %d body length %d", instrumentedResponse.StatusCode, len(instrumentedResponse.Body), plainResponse.StatusCode, len(plainResponse.Body))
	}

	assertRecordedMetric(t, recorder, observability.MetricRouteColdStart, observability.UnitCount, map[string]string{
		"Service": "ssr",
	})
	assertRecordedMetric(t, recorder, observability.MetricRouteDurationMs, observability.UnitMilliseconds, map[string]string{
		"Service": "ssr",
		"Route":   string(pageHome),
		"Method":  http.MethodGet,
		"Status":  strconv.Itoa(http.StatusOK),
	})

	_, err = handler.Handle(context.Background(), pageRequest(http.MethodGet, "/missing"))
	if err != nil {
		t.Fatalf("second Handle returned error: %v", err)
	}
	if got := recordedMetricCount(recorder, observability.MetricRouteColdStart); got != 1 {
		t.Fatalf("cold-start metric count = %d, want 1", got)
	}
	assertRecordedMetric(t, recorder, observability.MetricRouteDurationMs, observability.UnitMilliseconds, map[string]string{
		"Service": "ssr",
		"Route":   string(pageUnknown),
		"Method":  http.MethodGet,
		"Status":  strconv.Itoa(http.StatusNotFound),
	})
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
		{name: "cart trailing slash", method: http.MethodGet, path: "/cart/", location: "/cart"},
		{name: "checkout trailing slash", method: http.MethodGet, path: "/checkout/", location: "/checkout"},
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
		"/cart",
		"/checkout",
		"/shop",
		"/about",
		"/products/",
		"/products/mango-sticky-rice-kit/",
		"/categories/",
		"/categories/pantry/",
		"/story/",
		"/cart/",
		"/checkout/",
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

func TestCartRoutes(t *testing.T) {
	handler := NewHandler(cartRouteStore())

	for _, path := range []string{"/cart", "/checkout"} {
		t.Run("head "+path, func(t *testing.T) {
			response, err := handler.Handle(context.Background(), pageRequest(http.MethodHead, path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusOK && path == "/cart" {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
			}
			if response.Body != "" {
				t.Fatalf("HEAD body = %q, want empty", response.Body)
			}
		})
	}

	for _, path := range []string{"/cart/items", "/cart/items/mango-sticky-rice-kit/quantity", "/cart/items/mango-sticky-rice-kit/remove", "/cart/clear"} {
		t.Run("get "+path, func(t *testing.T) {
			response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusMethodNotAllowed)
			}
			if got := response.Headers["Allow"]; got != http.MethodPost {
				t.Fatalf("Allow = %q, want POST", got)
			}
		})
	}
}

func TestCartMutationsPostCartItemsSetsCookieAndRedirectsToCart(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	handler := NewHandler(cartRouteStore())
	body := base64.StdEncoding.EncodeToString([]byte("slug=mango-sticky-rice-kit&quantity=2"))
	request := formPostRequest("/cart/items", body)
	request.IsBase64Encoded = true
	request.Headers["CloudFront-Forwarded-Proto"] = "https"

	response, err := handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusSeeOther)
	}
	if got := response.Headers["Location"]; got != "/cart" {
		t.Fatalf("Location = %q, want /cart", got)
	}
	decoded := decodedCartFromResponse(t, response)
	if got := decoded.Lines(); len(got) != 1 || got[0].Slug != "mango-sticky-rice-kit" || got[0].Quantity != 2 {
		t.Fatalf("cart lines = %#v, want mango quantity 2", got)
	}
	if cookie := response.Cookies[0]; !strings.Contains(cookie, "Secure") {
		t.Fatalf("cookie = %q, want Secure", cookie)
	}
}

func TestCartMutationVariantProductRequiresSelectedActiveVariant(t *testing.T) {
	handler := NewHandler(cartRouteStore())

	for _, body := range []string{
		"slug=variant-shirt&quantity=1",
		"slug=variant-shirt&variant_id=var-archived&quantity=1",
		"slug=variant-shirt&variant_id=var-sold-out&quantity=1",
		"slug=variant-shirt&variant_id=missing&quantity=1",
	} {
		t.Run(body, func(t *testing.T) {
			response, err := handler.Handle(context.Background(), formPostRequest("/cart/items", body))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusBadRequest)
			}
		})
	}
}

func TestCartMutationVariantProductCapsQuantityByVariantStock(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	handler := NewHandler(cartRouteStore())
	response, err := handler.Handle(context.Background(), formPostRequest("/cart/items", "slug=variant-shirt&variant_id=var-small&quantity=10"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusSeeOther)
	}
	lines := decodedCartFromResponse(t, response).Lines()
	want := []cart.Line{{Slug: "variant-shirt", VariantID: "var-small", Quantity: 2}}
	if len(lines) != 1 || lines[0] != want[0] {
		t.Fatalf("cart lines = %#v, want %#v", lines, want)
	}
}

func TestCartQuantityVariantProductCapsBySelectedVariantStock(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	request := formPostRequest("/cart/items/variant-shirt/quantity", "variant_id=var-small&quantity=10")
	request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{{Slug: "variant-shirt", VariantID: "var-small", Quantity: 1}})}

	response, err := NewHandler(cartRouteStore()).Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusSeeOther)
	}
	lines := decodedCartFromResponse(t, response).Lines()
	want := []cart.Line{{Slug: "variant-shirt", VariantID: "var-small", Quantity: 2}}
	if len(lines) != 1 || lines[0] != want[0] {
		t.Fatalf("cart lines = %#v, want %#v", lines, want)
	}
}

func TestPostCartItemsRejectsInactiveOutOfStockOrUnknownSlug(t *testing.T) {
	handler := NewHandler(cartRouteStore())

	for _, slug := range []string{"draft-product", "sold-out", "missing-product"} {
		t.Run(slug, func(t *testing.T) {
			response, err := handler.Handle(context.Background(), formPostRequest("/cart/items", "slug="+slug+"&quantity=1"))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusNotFound)
			}
		})
	}
}

func TestPostCartQuantityRejectsInvalidQuantity(t *testing.T) {
	handler := NewHandler(cartRouteStore())

	for _, body := range []string{"quantity=0", "quantity=-1", "quantity=abc", ""} {
		t.Run(body, func(t *testing.T) {
			response, err := handler.Handle(context.Background(), formPostRequest("/cart/items/mango-sticky-rice-kit/quantity", body))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusBadRequest)
			}
		})
	}
}

func TestCartMutationsRejectFullCart(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	store := cartRouteStore()
	fullLines := make([]cart.Line, 0, cart.MaxLineItems)
	for i := range cart.MaxLineItems {
		slug := "line-" + strconv.Itoa(i)
		store.productsBySlug[slug] = catalog.Product{Slug: slug, Name: slug, Status: catalog.StatusActive, StockQuantity: 1}
		fullLines = append(fullLines, cart.Line{Slug: slug, Quantity: 1})
	}
	handler := NewHandler(store)
	request := formPostRequest("/cart/items", "slug=mango-sticky-rice-kit&quantity=1")
	request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, fullLines)}

	response, err := handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}
}

func TestCheckoutReviewEmptyCartRedirectsToCart(t *testing.T) {
	response, err := NewHandler(cartRouteStore()).Handle(context.Background(), pageRequest(http.MethodGet, "/checkout"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusSeeOther)
	}
	if got := response.Headers["Location"]; got != "/cart" {
		t.Fatalf("Location = %q, want /cart", got)
	}
}

func TestCartPageRenders(t *testing.T) {
	t.Run("empty state", func(t *testing.T) {
		response, err := NewHandler(cartRouteStore()).Handle(context.Background(), pageRequest(http.MethodGet, "/cart"))
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
		}
		assertBodyContains(t, response.Body, []string{`<title>Cart | Thailand Gift Shop</title>`, `Your cart is empty.`, `Browse the latest Thai snacks`, `href="/products"`, `Continue shopping`})
		assertBodyOmits(t, response.Body, []string{`data-testid="cart-line-item"`, `href="/checkout"`, `action="/cart/clear"`})
	})

	t.Run("line items and controls", func(t *testing.T) {
		t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
		request := pageRequest(http.MethodGet, "/cart")
		request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{{Slug: "mango-sticky-rice-kit", Quantity: 2}})}

		response, err := NewHandlerWithProductImagePlaceholderURL(cartRouteStore(), "/images/placeholder-product.jpg").Handle(context.Background(), request)
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
		}
		assertBodyContains(t, response.Body, []string{`Cart (2)`, `data-testid="cart-line-item"`, `src="/images/products/mango-sticky-rice-kit.jpg"`, `alt="Mango Sticky Rice Treats"`, `href="/products/mango-sticky-rice-kit"`, `Mango Sticky Rice Treats`, `$28.99`, `$57.98`, `action="/cart/items/mango-sticky-rice-kit/quantity"`, `name="quantity"`, `value="2"`, `max="5"`, `action="/cart/items/mango-sticky-rice-kit/remove"`, `action="/cart/clear"`, `href="/checkout"`, `Review checkout`, `Continue shopping`, `Clear cart`})
	})
}

func TestCartPageShowsVariantLabelAndBlocksUnavailableVariantUntilRemoved(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	request := pageRequest(http.MethodGet, "/cart")
	request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{{Slug: "variant-shirt", VariantID: "var-small", Quantity: 2}, {Slug: "variant-shirt", VariantID: "var-archived", Quantity: 1}})}

	response, err := NewHandler(cartRouteStore()).Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, response.Body, []string{
		`Thai Linen Shirt`,
		`Size Small`,
		`Size Archived`,
		`Selected size is unavailable. Remove it to continue.`,
		`Remove unavailable sizes before checkout review.`,
		`action="/cart/items/variant-shirt/remove"`,
		`type="hidden" name="variant_id" value="var-archived"`,
	})
	assertBodyOmits(t, response.Body, []string{`href="/checkout"`, `Review checkout`})
}

func TestCartRemoveUnavailableVariantLine(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	request := formPostRequest("/cart/items/variant-shirt/remove", "variant_id=var-archived")
	request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{{Slug: "variant-shirt", VariantID: "var-archived", Quantity: 1}})}

	response, err := NewHandler(cartRouteStore()).Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusSeeOther)
	}
	if len(response.Cookies) != 1 || !strings.Contains(response.Cookies[0], "Max-Age=0") {
		t.Fatalf("cookies = %#v, want clearing cookie", response.Cookies)
	}
}

func TestCheckoutPageRendersReviewOnly(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	request := pageRequest(http.MethodGet, "/checkout")
	request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{{Slug: "mango-sticky-rice-kit", Quantity: 2}})}

	response, err := NewHandlerWithProductImagePlaceholderURL(cartRouteStore(), "/images/placeholder-product.jpg").Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, response.Body, []string{`<title>Checkout Review | Thailand Gift Shop</title>`, `Checkout Review`, `Review only`, `data-testid="checkout-line-item"`, `src="/images/products/mango-sticky-rice-kit.jpg"`, `Mango Sticky Rice Treats`, `Quantity 2`, `$28.99`, `$57.98`, `Shipping and tax are not calculated on this review page.`, `Payment is not collected, and no order is placed from this screen.`, `No customer details or payment details are collected here, and no order is submitted.`, `href="/cart"`, `href="/products"`})
	assertBodyOmits(t, response.Body, []string{`<form`, `name="email"`, `name="address"`, `name="card"`, `payment submit`, `instant purchase`})
}

func TestCheckoutRedirectsWhenCartHasUnavailableVariant(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	request := pageRequest(http.MethodGet, "/checkout")
	request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{{Slug: "variant-shirt", VariantID: "var-archived", Quantity: 1}})}

	response, err := NewHandler(cartRouteStore()).Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusSeeOther)
	}
	if got := response.Headers["Location"]; got != "/cart" {
		t.Fatalf("Location = %q, want /cart", got)
	}
}

func TestCartPageOmitsPIIAndPaymentControls(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	for _, path := range []string{"/cart", "/checkout"} {
		t.Run(path, func(t *testing.T) {
			request := pageRequest(http.MethodGet, path)
			request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{{Slug: "mango-sticky-rice-kit", Quantity: 1}})}

			response, err := NewHandler(cartRouteStore()).Handle(context.Background(), request)
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
			}
			assertBodyOmits(t, response.Body, []string{`name="email"`, `name="address"`, `name="card"`, `card number`, `payment submit`, `instant purchase`})
		})
	}
}

func TestCartPageShowsCappedInventoryQuantities(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	request := pageRequest(http.MethodGet, "/cart")
	request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{{Slug: "mango-sticky-rice-kit", Quantity: 20}, {Slug: "high-stock", Quantity: 150}})}

	response, err := NewHandler(cartRouteStore()).Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	decoded := decodedCartFromResponse(t, response)
	lines := decoded.Lines()
	if len(lines) != 2 || lines[0].Quantity != 5 || lines[1].Quantity != cart.MaxQuantity {
		t.Fatalf("bounded lines = %#v, want stock cap 5 and max quantity %d", lines, cart.MaxQuantity)
	}
	assertBodyContains(t, response.Body, []string{`Cart (104)`, `Mango Sticky Rice Treats`, `High Stock Product`, `max="5"`, `value="5"`, `max="99"`, `value="99"`})
}

func TestCheckoutPageExcludesDroppedStaleItems(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	request := pageRequest(http.MethodGet, "/checkout")
	request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{{Slug: "mango-sticky-rice-kit", Quantity: 2}, {Slug: "draft-product", Quantity: 1}, {Slug: "sold-out", Quantity: 1}, {Slug: "missing-product", Quantity: 1}})}

	response, err := NewHandler(cartRouteStore()).Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	decoded := decodedCartFromResponse(t, response)
	if got := decoded.Lines(); len(got) != 1 || got[0].Slug != "mango-sticky-rice-kit" || got[0].Quantity != 2 {
		t.Fatalf("normalized lines = %#v, want only active in-stock mango", got)
	}
	assertBodyContains(t, response.Body, []string{`Mango Sticky Rice Treats`, `$57.98`})
	assertBodyOmits(t, response.Body, []string{`Draft Product`, `Sold Out`, `missing-product`})
}

func TestCartCookieFlagsHttpOnlySameSitePathMaxAgeAndHttpsSecure(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	request := formPostRequest("/cart/items", "slug=mango-sticky-rice-kit&quantity=1")
	request.Headers["cloudfront-forwarded-proto"] = "https"

	response, err := NewHandler(cartRouteStore()).Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	cookieHeader := response.Cookies[0]
	for _, want := range []string{cart.CookieName + "=", "Path=/", "Max-Age=604800", "HttpOnly", "SameSite=Lax", "Secure"} {
		if !strings.Contains(cookieHeader, want) {
			t.Fatalf("cookie = %q, want %q", cookieHeader, want)
		}
	}

	request = formPostRequest("/cart/items", "slug=mango-sticky-rice-kit&quantity=1")
	response, err = NewHandler(cartRouteStore()).Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if strings.Contains(response.Cookies[0], "Secure") {
		t.Fatalf("localhost HTTP cookie = %q, want no Secure", response.Cookies[0])
	}
}

func TestCartCookieFlagsHttpsSecureWithXForwardedProtoFallback(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	request := formPostRequest("/cart/items", "slug=mango-sticky-rice-kit&quantity=1")
	request.Headers["x-forwarded-proto"] = "https"

	response, err := NewHandler(cartRouteStore()).Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if cookie := response.Cookies[0]; !strings.Contains(cookie, "Secure") {
		t.Fatalf("cookie = %q, want Secure", cookie)
	}
}

func TestCartNormalizesMissingInactiveAndOutOfStockItems(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	request := pageRequest(http.MethodGet, "/cart")
	request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{
		{Slug: "mango-sticky-rice-kit", Quantity: 2},
		{Slug: "draft-product", Quantity: 1},
		{Slug: "sold-out", Quantity: 1},
		{Slug: "missing-product", Quantity: 1},
	})}

	response, err := NewHandler(cartRouteStore()).Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	decoded := decodedCartFromResponse(t, response)
	if got := decoded.Lines(); len(got) != 1 || got[0].Slug != "mango-sticky-rice-kit" || got[0].Quantity != 2 {
		t.Fatalf("normalized lines = %#v, want only active in-stock mango", got)
	}
	assertBodyOmits(t, response.Body, []string{"Draft Product", "Sold Out"})
}

func TestCartInventoryBoundsCapsQuantityToCurrentStockAndNinetyNine(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	request := pageRequest(http.MethodGet, "/cart")
	request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{
		{Slug: "mango-sticky-rice-kit", Quantity: 20},
		{Slug: "high-stock", Quantity: 150},
	})}

	response, err := NewHandler(cartRouteStore()).Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	decoded := decodedCartFromResponse(t, response)
	lines := decoded.Lines()
	if len(lines) != 2 || lines[0].Quantity != 5 || lines[1].Quantity != cart.MaxQuantity {
		t.Fatalf("bounded lines = %#v, want stock cap 5 and max quantity %d", lines, cart.MaxQuantity)
	}
}

func TestCheckoutNever500sWithStaleCartCookie(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	request := pageRequest(http.MethodGet, "/checkout")
	request.Headers = map[string]string{"Cookie": cart.CookieName + "=" + encodedTestCart(t, []cart.Line{{Slug: "missing-product", Quantity: 1}})}

	response, err := NewHandler(cartRouteStore()).Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusSeeOther)
	}
	if got := response.Headers["Location"]; got != "/cart" {
		t.Fatalf("Location = %q, want /cart", got)
	}
	if len(response.Cookies) != 1 || !strings.Contains(response.Cookies[0], "Max-Age=0") {
		t.Fatalf("cookies = %#v, want clearing cookie", response.Cookies)
	}
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

func formPostRequest(path string, body string) events.APIGatewayV2HTTPRequest {
	request := pageRequest(http.MethodPost, path)
	request.Body = body
	request.Headers = map[string]string{"content-type": "application/x-www-form-urlencoded"}
	return request
}

func encodedTestCart(t *testing.T, lines []cart.Line) string {
	t.Helper()
	testCart, err := cart.New(lines)
	if err != nil {
		t.Fatalf("cart.New returned error: %v", err)
	}
	encoded, err := cart.EncodeCookie(testCart, ssrTestCartSecret)
	if err != nil {
		t.Fatalf("EncodeCookie returned error: %v", err)
	}
	return encoded
}

func decodedCartFromResponse(t *testing.T, response events.APIGatewayV2HTTPResponse) cart.Cart {
	t.Helper()
	if len(response.Cookies) != 1 {
		t.Fatalf("response cookies = %#v, want one cart cookie", response.Cookies)
	}
	value, found := namedCookieValue(response.Cookies[0], cart.CookieName)
	if !found {
		t.Fatalf("cart cookie missing from %#v", response.Cookies)
	}
	decoded := cart.DecodeCookie(value, ssrTestCartSecret)
	if decoded.NeedsClear {
		t.Fatalf("decoded cart NeedsClear = true for cookie %q", response.Cookies[0])
	}
	return decoded.Cart
}

func cartRouteStore() *fakeCatalogStore {
	return &fakeCatalogStore{
		productsBySlug: map[string]catalog.Product{
			"mango-sticky-rice-kit": {
				Slug:          "mango-sticky-rice-kit",
				Name:          "Mango Sticky Rice Treats",
				Description:   "Shelf-stable Thai dessert snacks.",
				PriceCents:    2899,
				ImageURL:      "/images/products/mango-sticky-rice-kit.jpg",
				Status:        catalog.StatusActive,
				StockQuantity: 5,
			},
			"high-stock": {
				Slug:          "high-stock",
				Name:          "High Stock Product",
				Description:   "Bulk Thai gift-shop stock.",
				PriceCents:    199,
				ImageURL:      "/images/products/high-stock.jpg",
				Status:        catalog.StatusActive,
				StockQuantity: cart.MaxQuantity + 25,
			},
			"variant-shirt": {
				Slug:        "variant-shirt",
				Name:        "Thai Linen Shirt",
				Description: "Soft linen shirt with market colors.",
				PriceCents:  4499,
				ImageURL:    "/images/products/variant-shirt.jpg",
				Status:      catalog.StatusActive,
				Variants: []catalog.ProductVariant{
					{ID: "var-small", Label: "Small", StockQuantity: 2, Status: catalog.StatusActive, SortOrder: 10},
					{ID: "var-large", Label: "Large", StockQuantity: 7, Status: catalog.StatusActive, SortOrder: 20},
					{ID: "var-sold-out", Label: "Medium", StockQuantity: 0, Status: catalog.StatusActive, SortOrder: 30},
					{ID: "var-archived", Label: "Archived", StockQuantity: 4, Status: catalog.StatusArchived, SortOrder: 40},
				},
			},
			"draft-product": {
				Slug:          "draft-product",
				Name:          "Draft Product",
				Status:        catalog.StatusDraft,
				StockQuantity: 5,
			},
			"sold-out": {
				Slug:          "sold-out",
				Name:          "Sold Out",
				Status:        catalog.StatusActive,
				StockQuantity: 0,
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

func TestHomeRendersDataDrivenAisleLinks(t *testing.T) {
	handler := NewHandler(&fakeCatalogStore{
		categories: []catalog.Category{
			{
				Slug:        "thai-snacks",
				Name:        "Thai Snacks",
				Description: "Crunchy, sweet, and pantry-friendly finds.",
				Status:      catalog.StatusActive,
			},
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
			{
				Slug:        "fourth-active",
				Name:        "Fourth Active Category",
				Description: "Visible in categories, not featured aisles.",
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
	assertBodyContains(t, response.Body, []string{
		`data-testid="home-aisle-card" href="/categories/thai-snacks"`,
		`data-testid="home-aisle-card" href="/categories/temple-bells"`,
		`data-testid="home-aisle-card" href="/categories/jasmine-garlands"`,
		`href="/categories/fourth-active"`,
	})
	if got := strings.Count(response.Body, `data-testid="home-aisle-card"`); got != 3 {
		t.Fatalf("home aisle cards = %d, want 3: %q", got, response.Body)
	}
	assertBodyOmits(t, response.Body, []string{
		`data-testid="home-aisle-card" href="#latest"`,
		`data-testid="home-aisle-card" href="/categories/hidden-category"`,
	})
}

func TestHeaderRendersCartLinkLabelFromNormalizedCookie(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	request := pageRequest(http.MethodGet, "/products")
	request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{{Slug: "mango-sticky-rice-kit", Quantity: 2}})}

	response, err := NewHandler(cartRouteStore()).Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, response.Body, []string{
		`href="/cart">Cart (2)</a>`,
		`Products</a>`,
		`Categories</a>`,
		`Story</a>`,
	})
}

func TestCartBearingPagesReuseNormalizedRequestCart(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)

	for _, path := range []string{"/cart", "/checkout"} {
		t.Run(path, func(t *testing.T) {
			store := cartRouteStore()
			request := pageRequest(http.MethodGet, path)
			request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{{Slug: "mango-sticky-rice-kit", Quantity: 2}})}

			response, err := NewHandler(store).Handle(context.Background(), request)
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
			}
			if len(store.productSlugLookups) != 1 || store.productSlugLookups[0] != "mango-sticky-rice-kit" {
				t.Fatalf("GetProductBySlug lookups = %v, want one normalized cart lookup", store.productSlugLookups)
			}
			assertBodyContains(t, response.Body, []string{`Cart (2)`, `Mango Sticky Rice Treats`})
		})
	}
}

func TestCatalogPageReadsStartConcurrently(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		starts []string
	}{
		{name: "home", path: "/", starts: []string{"ListRecentlyAddedProducts", "ListActiveCategories"}},
		{name: "product listing", path: "/products", starts: []string{"ListActiveProducts", "ListActiveCategories"}},
		{name: "product detail", path: "/products/mango-sticky-rice-kit", starts: []string{"GetProductBySlug:mango-sticky-rice-kit", "ListActiveCategories"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newBlockingCatalogStore()
			handler := NewHandler(store)
			result := make(chan handleResult, 1)
			go func() {
				response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, test.path))
				result <- handleResult{response: response, err: err}
			}()

			if missing := missingCatalogStarts(store.started, test.starts, 200*time.Millisecond); len(missing) > 0 {
				close(store.release)
				drainHandleResult(t, result)
				t.Fatalf("catalog calls did not start concurrently; missing %s", strings.Join(missing, ", "))
			}
			close(store.release)

			got := drainHandleResult(t, result)
			if got.err != nil {
				t.Fatalf("Handle returned error: %v", got.err)
			}
			if got.response.StatusCode != http.StatusOK {
				t.Fatalf("status code = %d, want %d", got.response.StatusCode, http.StatusOK)
			}
		})
	}
}

func TestHeaderRendersMobileNoJSNavigation(t *testing.T) {
	response, err := NewHandler(routeMatrixStore()).Handle(context.Background(), pageRequest(http.MethodGet, "/story"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, response.Body, []string{
		`<details class="md:hidden">`,
		`<summary class="inline-flex h-10 cursor-pointer list-none`,
		`href="/products">Products</a>`,
		`href="/categories">Categories</a>`,
		`href="/story">Story</a>`,
		`href="/cart">Cart</a>`,
	})
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
		`Quantity`,
	})
}

func TestProductDetailRendersCartFormForInStockProduct(t *testing.T) {
	product := catalog.Product{
		ID:            "prod_active_cart",
		Slug:          "thai-tea-sampler",
		Name:          "Thai Tea Selection",
		Description:   "Loose leaf Thai tea and sweet snacks.",
		PriceCents:    2199,
		Status:        catalog.StatusActive,
		StockQuantity: cart.MaxQuantity + 12,
	}
	handler := NewHandler(&fakeCatalogStore{
		productsBySlug: map[string]catalog.Product{product.Slug: product},
	})

	response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/products/thai-tea-sampler"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, response.Body, []string{
		`method="POST"`,
		`action="/cart/items"`,
		`type="hidden" name="slug" value="thai-tea-sampler"`,
		`type="number" name="quantity" value="1" min="1" max="99"`,
		`Add to cart`,
		`Adding this item starts a cart review.`,
		`No payment is collected yet`,
		`shipping and tax are not included`,
		`Stripe payment processing will be added later`,
	})
	assertBodyOmits(t, response.Body, []string{
		`prod_active_cart`,
		`/products/prod_`,
	})
}

func TestProductDetailRendersVariantSelectorForVariantProduct(t *testing.T) {
	product := catalog.Product{
		ID:          "prod_variant_cart",
		Slug:        "variant-shirt",
		Name:        "Thai Linen Shirt",
		Description: "Soft linen shirt with market colors.",
		PriceCents:  4499,
		Status:      catalog.StatusActive,
		Variants: []catalog.ProductVariant{
			{ID: "var-large", Label: "Large", StockQuantity: 7, Status: catalog.StatusActive, SortOrder: 20},
			{ID: "var-small", Label: "Small", StockQuantity: 2, Status: catalog.StatusActive, SortOrder: 10},
			{ID: "var-sold-out", Label: "Medium", StockQuantity: 0, Status: catalog.StatusActive, SortOrder: 30},
			{ID: "var-archived", Label: "Archived", StockQuantity: 4, Status: catalog.StatusArchived, SortOrder: 40},
		},
	}
	handler := NewHandler(&fakeCatalogStore{
		productsBySlug: map[string]catalog.Product{product.Slug: product},
	})

	response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/products/variant-shirt"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, response.Body, []string{
		`data-testid="variant-select"`,
		`name="variant_id" required`,
		`<option value="">Select a size</option>`,
		`<option value="var-small">Small</option>`,
		`<option value="var-large">Large</option>`,
		`<option value="var-sold-out" disabled>Medium - out of stock</option>`,
		`type="number" name="quantity" value="1" min="1" max="9"`,
	})
	assertBodyOmits(t, response.Body, []string{`var-archived`, `Archived`})
}

func TestProductDetailDoesNotRenderAddToCartForOutOfStock(t *testing.T) {
	product := catalog.Product{
		Slug:          "sold-out-tea",
		Name:          "Sold Out Tea",
		Description:   "This tin is currently unavailable.",
		PriceCents:    1199,
		Status:        catalog.StatusActive,
		StockQuantity: 0,
	}
	handler := NewHandler(&fakeCatalogStore{
		productsBySlug: map[string]catalog.Product{product.Slug: product},
	})

	response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/products/sold-out-tea"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, response.Body, []string{
		`Out of stock`,
		`This item cannot be added to cart right now.`,
	})
	assertBodyOmits(t, response.Body, []string{
		`action="/cart/items"`,
		`name="quantity"`,
		`Add to cart`,
	})
}

func TestProductDetailRendersActiveCategoryChips(t *testing.T) {
	product := catalog.Product{
		Slug:          "mango-sticky-rice-kit",
		Name:          "Mango Sticky Rice Treats",
		Description:   "Shelf-stable Thai dessert snacks.",
		PriceCents:    2899,
		Status:        catalog.StatusActive,
		StockQuantity: 4,
		CategorySlugs: []string{"thai-snacks", "pantry"},
	}
	handler := NewHandler(&fakeCatalogStore{
		productsBySlug: map[string]catalog.Product{product.Slug: product},
		categories: []catalog.Category{
			{Slug: "thai-snacks", Name: "Thai Snacks", Description: "Crunchy finds.", Status: catalog.StatusActive},
			{Slug: "pantry", Name: "Pantry", Description: "Shelf-stable staples.", Status: catalog.StatusActive},
		},
	})

	response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/products/mango-sticky-rice-kit"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, response.Body, []string{
		`aria-label="Product categories"`,
		`href="/categories/thai-snacks"`,
		`Thai Snacks`,
		`href="/categories/pantry"`,
		`Pantry`,
	})
}

func TestProductDetailOmitsInactiveCategoryChips(t *testing.T) {
	product := catalog.Product{
		Slug:          "mango-sticky-rice-kit",
		Name:          "Mango Sticky Rice Treats",
		Description:   "Shelf-stable Thai dessert snacks.",
		PriceCents:    2899,
		Status:        catalog.StatusActive,
		StockQuantity: 4,
		CategorySlugs: []string{"thai-snacks", "hidden-category", "missing-category"},
	}
	handler := NewHandler(&fakeCatalogStore{
		productsBySlug: map[string]catalog.Product{product.Slug: product},
		categories: []catalog.Category{
			{Slug: "thai-snacks", Name: "Thai Snacks", Description: "Crunchy finds.", Status: catalog.StatusActive},
			{Slug: "hidden-category", Name: "Hidden Category", Description: "Should not show.", Status: catalog.StatusDraft},
		},
	})

	response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/products/mango-sticky-rice-kit"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, response.Body, []string{
		`href="/categories/thai-snacks"`,
		`Thai Snacks`,
	})
	assertBodyOmits(t, response.Body, []string{
		`href="/categories/hidden-category"`,
		`Hidden Category`,
		`href="/categories/missing-category"`,
	})
}

func TestProductCardsRenderViewDetailsAffordance(t *testing.T) {
	handler := NewHandler(&fakeCatalogStore{
		products: []catalog.Product{
			{
				Slug:          "mango-sticky-rice-kit",
				Name:          "Mango Sticky Rice Treats",
				Description:   "Shelf-stable Thai dessert snacks.",
				PriceCents:    2899,
				Status:        catalog.StatusActive,
				StockQuantity: 4,
			},
		},
	})

	response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/products"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, response.Body, []string{
		`data-testid="product-card" href="/products/mango-sticky-rice-kit"`,
		`View details`,
	})
	assertBodyOmits(t, response.Body, []string{
		`action="/cart/items"`,
		`Add to cart`,
		`name="quantity"`,
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
		`href="/cart"`,
		`<h1 class="mt-3 text-5xl font-black leading-none text-[#2D2A4A] sm:text-6xl">Our Story</h1>`,
		`Bangkok gift shop online`,
		`Thai snacks, souvenirs, textiles, pantry items, decor, wellness, and small keepsakes`,
		`Thailand Gift Shop is a Bangkok gift shop online for Thai snacks, souvenirs, textiles, pantry items, decor, wellness, and small keepsakes.`,
		`Each aisle is shaped for calm browsing: clear categories, strong product images, concise details, and slug-based links that work without JavaScript.`,
		`The shop point of view is market-bright and practical, rooted in the colors, textures, pantry flavors, and compact keepsakes travelers remember from Bangkok gift shops.`,
		`Checkout is review-only for now: no payment is collected yet, and shipping and tax are confirmed later.`,
	})
}

func TestStoryExplainsReviewOnlyCheckout(t *testing.T) {
	response, err := NewHandler(&fakeCatalogStore{}).Handle(context.Background(), pageRequest(http.MethodGet, "/story"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, response.Body, []string{
		`Thai gift-shop catalog`,
		`Review-only checkout: no payment is collected yet, and shipping and tax are confirmed later.`,
		`Checkout is review-only for now: no payment is collected yet, and shipping and tax are confirmed later.`,
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
		"Account",
		"Search",
		"Filter",
		"Sort",
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

type handleResult struct {
	response events.APIGatewayV2HTTPResponse
	err      error
}

type blockingCatalogStore struct {
	started  chan string
	release  chan struct{}
	product  catalog.Product
	category catalog.Category
}

func newBlockingCatalogStore() *blockingCatalogStore {
	return &blockingCatalogStore{
		started: make(chan string, 4),
		release: make(chan struct{}),
		product: catalog.Product{
			Slug:          "mango-sticky-rice-kit",
			Name:          "Mango Sticky Rice Treats",
			Description:   "Shelf-stable Thai dessert snacks.",
			PriceCents:    2899,
			Status:        catalog.StatusActive,
			StockQuantity: 5,
			CategorySlugs: []string{"thai-snacks"},
		},
		category: catalog.Category{
			Slug:        "thai-snacks",
			Name:        "Thai Snacks",
			Description: "Crunchy, sweet, and pantry-friendly finds.",
			Status:      catalog.StatusActive,
		},
	}
}

func (s *blockingCatalogStore) ListActiveProducts(ctx context.Context, limit int) ([]catalog.Product, error) {
	if err := s.wait(ctx, "ListActiveProducts"); err != nil {
		return nil, err
	}
	return []catalog.Product{s.product}, nil
}

func (s *blockingCatalogStore) ListRecentlyAddedProducts(ctx context.Context, limit int) ([]catalog.Product, error) {
	if err := s.wait(ctx, "ListRecentlyAddedProducts"); err != nil {
		return nil, err
	}
	return []catalog.Product{s.product}, nil
}

func (s *blockingCatalogStore) GetProductBySlug(ctx context.Context, slug string) (catalog.Product, bool, error) {
	if err := s.wait(ctx, "GetProductBySlug:"+slug); err != nil {
		return catalog.Product{}, false, err
	}
	if slug != s.product.Slug {
		return catalog.Product{}, false, nil
	}
	return s.product, true, nil
}

func (s *blockingCatalogStore) ListActiveCategories(ctx context.Context) ([]catalog.Category, error) {
	if err := s.wait(ctx, "ListActiveCategories"); err != nil {
		return nil, err
	}
	return []catalog.Category{s.category}, nil
}

func (s *blockingCatalogStore) ListActiveProductsByCategory(ctx context.Context, categorySlug string, limit int) ([]catalog.Product, error) {
	if err := s.wait(ctx, "ListActiveProductsByCategory:"+categorySlug); err != nil {
		return nil, err
	}
	if categorySlug != s.category.Slug {
		return []catalog.Product{}, nil
	}
	return []catalog.Product{s.product}, nil
}

func (s *blockingCatalogStore) wait(ctx context.Context, name string) error {
	select {
	case s.started <- name:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func missingCatalogStarts(started <-chan string, wants []string, timeout time.Duration) []string {
	wantSet := make(map[string]bool, len(wants))
	for _, want := range wants {
		wantSet[want] = true
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for len(wantSet) > 0 {
		select {
		case got := <-started:
			delete(wantSet, got)
		case <-timer.C:
			missing := make([]string, 0, len(wantSet))
			for want := range wantSet {
				missing = append(missing, want)
			}
			return missing
		}
	}

	return nil
}

func drainHandleResult(t *testing.T, result <-chan handleResult) handleResult {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(time.Second):
		t.Fatal("Handle did not finish")
	}
	return handleResult{}
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

type testMetricRecorder struct {
	metrics []observability.Metric
}

func (r *testMetricRecorder) Record(metric observability.Metric) {
	r.metrics = append(r.metrics, metric)
}

func assertRecordedMetric(t *testing.T, recorder *testMetricRecorder, name string, unit string, dimensions map[string]string) {
	t.Helper()
	for _, metric := range recorder.metrics {
		if metric.Name != name || metric.Unit != unit {
			continue
		}
		if metricDimensionsMatch(metric, dimensions) {
			return
		}
	}
	t.Fatalf("metric %q with unit %q and dimensions %#v not recorded; got %#v", name, unit, dimensions, recorder.metrics)
}

func recordedMetricCount(recorder *testMetricRecorder, name string) int {
	count := 0
	for _, metric := range recorder.metrics {
		if metric.Name == name {
			count++
		}
	}
	return count
}

func metricDimensionsMatch(metric observability.Metric, dimensions map[string]string) bool {
	if len(metric.Dimensions) != len(dimensions) {
		return false
	}
	for _, dimension := range metric.Dimensions {
		if dimensions[dimension.Name] != dimension.Value {
			return false
		}
	}
	return true
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
