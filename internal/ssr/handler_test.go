package ssr

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/appenv"
	"github.com/anhydrous99/thailandgiftshop/internal/cart"
	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/email"
	"github.com/anhydrous99/thailandgiftshop/internal/httpapi"
	"github.com/anhydrous99/thailandgiftshop/internal/observability"
	"github.com/anhydrous99/thailandgiftshop/internal/payments"
	"github.com/anhydrous99/thailandgiftshop/internal/staticassets"
	"github.com/aws/aws-lambda-go/events"
	"golang.org/x/crypto/bcrypt"
)

const ssrTestCartSecret = "ssr-cart-test-secret"
const ssrTestSessionSecret = "ssr-customer-session-test-secret"

// ssrTestAccountID matches the 26-char lowercase Crockford base32 identifier
// shape used for addresses and orders.
const ssrTestAccountID = "0123456789abcdefghjkmnpqrs"

var expectedHomeContent = []string{
	"<!doctype html>",
	`<html lang="en" class="scroll-smooth">`,
	`<title>Thailand Gift Shop</title>`,
	`<link rel="icon" href="/static/favicon.ico" sizes="any">`,
	`<link rel="icon" href="/static/favicon.svg" type="image/svg+xml">`,
	`<link rel="apple-touch-icon" href="/static/apple-touch-icon.png" sizes="180x180">`,
	`<link rel="manifest" href="/static/site.webmanifest">`,
	staticassets.AppCSSPath,
	`src="/static/logo.svg"`,
	`srcset="/static/home-hero.jpg"`,
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

func TestFormatPriceUsesThousandsSeparators(t *testing.T) {
	tests := []struct {
		name       string
		priceCents int
		want       string
	}{
		{name: "negative", priceCents: -1, want: "$0.00"},
		{name: "zero", priceCents: 0, want: "$0.00"},
		{name: "under one thousand", priceCents: 2899, want: "$28.99"},
		{name: "one thousand", priceCents: 100000, want: "$1,000.00"},
		{name: "millions", priceCents: 123456789, want: "$1,234,567.89"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := formatPrice(test.priceCents); got != test.want {
				t.Fatalf("formatPrice(%d) = %q, want %q", test.priceCents, got, test.want)
			}
		})
	}
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
			name: "robots txt",
			path: "/robots.txt",
			want: pageRoute{kind: pageRobotsTxt, knownPageShape: true},
		},
		{
			name: "sitemap xml",
			path: "/sitemap.xml",
			want: pageRoute{kind: pageSitemapXML, knownPageShape: true},
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
			name: "account",
			path: "/account",
			want: pageRoute{kind: pageAccount, knownPageShape: true},
		},
		{
			name: "account trailing slash redirect",
			path: "/account/",
			want: pageRoute{kind: pageAccount, redirectTo: "/account", knownPageShape: true},
		},
		{
			name: "account sign up",
			path: "/account/sign-up",
			want: pageRoute{kind: pageAccountSignUp, knownPageShape: true},
		},
		{
			name: "account sign up trailing slash redirect",
			path: "/account/sign-up/",
			want: pageRoute{kind: pageAccountSignUp, redirectTo: "/account/sign-up", knownPageShape: true},
		},
		{
			name: "account sign in",
			path: "/account/sign-in",
			want: pageRoute{kind: pageAccountSignIn, knownPageShape: true},
		},
		{
			name: "account sign in trailing slash redirect",
			path: "/account/sign-in/",
			want: pageRoute{kind: pageAccountSignIn, redirectTo: "/account/sign-in", knownPageShape: true},
		},
		{
			name: "account sign out",
			path: "/account/sign-out",
			want: pageRoute{kind: pageAccountSignOut, knownPageShape: true},
		},
		{
			name: "account password",
			path: "/account/password",
			want: pageRoute{kind: pageAccountPassword, knownPageShape: true},
		},
		{
			name: "account addresses",
			path: "/account/addresses",
			want: pageRoute{kind: pageAccountAddresses, knownPageShape: true},
		},
		{
			name: "account addresses trailing slash redirect",
			path: "/account/addresses/",
			want: pageRoute{kind: pageAccountAddresses, redirectTo: "/account/addresses", knownPageShape: true},
		},
		{
			name: "account address edit",
			path: "/account/addresses/" + ssrTestAccountID + "/edit",
			want: pageRoute{kind: pageAccountAddressEdit, slug: ssrTestAccountID, knownPageShape: true},
		},
		{
			name: "account address edit trailing slash redirect",
			path: "/account/addresses/" + ssrTestAccountID + "/edit/",
			want: pageRoute{kind: pageAccountAddressEdit, slug: ssrTestAccountID, redirectTo: "/account/addresses/" + ssrTestAccountID + "/edit", knownPageShape: true},
		},
		{
			name: "account address update",
			path: "/account/addresses/" + ssrTestAccountID + "/update",
			want: pageRoute{kind: pageAccountAddressUpdate, slug: ssrTestAccountID, knownPageShape: true},
		},
		{
			name: "account address remove",
			path: "/account/addresses/" + ssrTestAccountID + "/remove",
			want: pageRoute{kind: pageAccountAddressRemove, slug: ssrTestAccountID, knownPageShape: true},
		},
		{
			name: "account address default",
			path: "/account/addresses/" + ssrTestAccountID + "/default",
			want: pageRoute{kind: pageAccountAddressDefault, slug: ssrTestAccountID, knownPageShape: true},
		},
		{
			name: "account payment methods",
			path: "/account/payment-methods",
			want: pageRoute{kind: pageAccountPaymentMethods, knownPageShape: true},
		},
		{
			name: "account payment methods trailing slash redirect",
			path: "/account/payment-methods/",
			want: pageRoute{kind: pageAccountPaymentMethods, redirectTo: "/account/payment-methods", knownPageShape: true},
		},
		{
			name: "account payment method add",
			path: "/account/payment-methods/add",
			want: pageRoute{kind: pageAccountPaymentMethodAdd, knownPageShape: true},
		},
		{
			name: "account payment method remove",
			path: "/account/payment-methods/pm_fake_visa_4242/remove",
			want: pageRoute{kind: pageAccountPaymentMethodRemove, slug: "pm_fake_visa_4242", knownPageShape: true},
		},
		{
			name: "account address edit rejects malformed id",
			path: "/account/addresses/UPPERCASE-IS-NOT-AN-ID-1234/edit",
			want: pageRoute{kind: pageUnknown},
		},
		{
			name: "account address update rejects short id",
			path: "/account/addresses/tooshort/update",
			want: pageRoute{kind: pageUnknown},
		},
		{
			name: "account payment method remove rejects malformed id",
			path: "/account/payment-methods/visa4242/remove",
			want: pageRoute{kind: pageUnknown},
		},
		{
			name: "account payment method remove rejects too-short id",
			path: "/account/payment-methods/pm_x/remove",
			want: pageRoute{kind: pageUnknown},
		},
		{
			name: "unknown account subpath",
			path: "/account/unknown",
			want: pageRoute{kind: pageUnknown},
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
			if !strings.Contains(response.Body, "Not found") {
				t.Fatalf("body does not contain %q: %q", "Not found", response.Body)
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
		{name: "account trailing slash", method: http.MethodGet, path: "/account/", location: "/account"},
		{name: "account sign in trailing slash", method: http.MethodGet, path: "/account/sign-in/", location: "/account/sign-in"},
		{name: "account sign up trailing slash", method: http.MethodGet, path: "/account/sign-up/", location: "/account/sign-up"},
		{name: "account addresses trailing slash", method: http.MethodGet, path: "/account/addresses/", location: "/account/addresses"},
		{name: "account address edit trailing slash", method: http.MethodGet, path: "/account/addresses/" + ssrTestAccountID + "/edit/", location: "/account/addresses/" + ssrTestAccountID + "/edit"},
		{name: "account payment methods trailing slash", method: http.MethodGet, path: "/account/payment-methods/", location: "/account/payment-methods"},
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

func TestRobotsTxtAllowsCrawlingAndAdvertisesSitemap(t *testing.T) {
	handler := NewHandler(routeMatrixStore())
	response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/robots.txt"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if got := response.Headers["Content-Type"]; got != textContentType {
		t.Fatalf("Content-Type = %q, want %q", got, textContentType)
	}
	if got := response.Headers["Cache-Control"]; got != seoDiscoveryCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, seoDiscoveryCacheControl)
	}
	assertBodyContains(t, response.Body, []string{
		"User-agent: *\n",
		"Allow: /\n",
		"Sitemap: " + canonicalHost + "/sitemap.xml\n",
	})
	assertBodyOmits(t, response.Body, []string{"Disallow: /cart", "Disallow: /checkout", "admin"})

	headResponse, err := handler.Handle(context.Background(), pageRequest(http.MethodHead, "/robots.txt"))
	if err != nil {
		t.Fatalf("HEAD Handle returned error: %v", err)
	}
	if headResponse.StatusCode != http.StatusOK || headResponse.Body != "" {
		t.Fatalf("HEAD response = status %d body %q, want 200 empty", headResponse.StatusCode, headResponse.Body)
	}
	if got := headResponse.Headers["Cache-Control"]; got != seoDiscoveryCacheControl {
		t.Fatalf("HEAD Cache-Control = %q, want %q", got, seoDiscoveryCacheControl)
	}
}

func TestSitemapIncludesOnlyPublicIndexActiveDetailAndStoryURLs(t *testing.T) {
	activeProduct := catalog.Product{Slug: "thai-tea-sampler", Name: "Thai Tea Selection", Status: catalog.StatusActive, UpdatedAt: time.Date(2026, 6, 2, 9, 0, 0, 0, time.UTC)}
	draftProduct := catalog.Product{Slug: "draft-product", Name: "Draft Product", Status: catalog.StatusDraft, UpdatedAt: time.Date(2026, 6, 3, 9, 0, 0, 0, time.UTC)}
	activeCategory := catalog.Category{Slug: "thai-snacks", Name: "Thai Snacks", Status: catalog.StatusActive, UpdatedAt: time.Date(2026, 6, 4, 9, 0, 0, 0, time.UTC)}
	draftCategory := catalog.Category{Slug: "hidden-category", Name: "Hidden Category", Status: catalog.StatusDraft, UpdatedAt: time.Date(2026, 6, 5, 9, 0, 0, 0, time.UTC)}
	handler := NewHandler(&fakeCatalogStore{
		products:   []catalog.Product{activeProduct, draftProduct},
		categories: []catalog.Category{activeCategory, draftCategory},
	})

	response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/sitemap.xml"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if got := response.Headers["Content-Type"]; got != xmlContentType {
		t.Fatalf("Content-Type = %q, want %q", got, xmlContentType)
	}
	if got := response.Headers["Cache-Control"]; got != seoDiscoveryCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, seoDiscoveryCacheControl)
	}
	assertBodyContains(t, response.Body, []string{
		`<loc>` + canonicalHost + `/</loc>`,
		`<loc>` + canonicalHost + `/products</loc>`,
		`<loc>` + canonicalHost + `/products/thai-tea-sampler</loc>`,
		`<lastmod>2026-06-02</lastmod>`,
		`<loc>` + canonicalHost + `/categories</loc>`,
		`<loc>` + canonicalHost + `/categories/thai-snacks</loc>`,
		`<lastmod>2026-06-04</lastmod>`,
		`<loc>` + canonicalHost + `/story</loc>`,
	})
	assertBodyOmits(t, response.Body, []string{
		"draft-product",
		"hidden-category",
		"/cart",
		"/checkout",
		"/account",
		"/admin",
		"/shop",
		"/about",
	})

	headResponse, err := handler.Handle(context.Background(), pageRequest(http.MethodHead, "/sitemap.xml"))
	if err != nil {
		t.Fatalf("HEAD Handle returned error: %v", err)
	}
	if headResponse.StatusCode != http.StatusOK || headResponse.Body != "" {
		t.Fatalf("HEAD response = status %d body %q, want 200 empty", headResponse.StatusCode, headResponse.Body)
	}
	if got := headResponse.Headers["Cache-Control"]; got != seoDiscoveryCacheControl {
		t.Fatalf("HEAD Cache-Control = %q, want %q", got, seoDiscoveryCacheControl)
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
		"/robots.txt",
		"/sitemap.xml",
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

func TestCartAndCheckoutRenderNoindexRobots(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)

	request := pageRequest(http.MethodGet, "/cart")
	request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{{Slug: "mango-sticky-rice-kit", Quantity: 1}})}
	response, err := NewHandler(cartRouteStore()).Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle cart returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("cart status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if got := response.Headers["X-Robots-Tag"]; got != "noindex, follow" {
		t.Fatalf("cart X-Robots-Tag = %q, want noindex, follow", got)
	}
	if got := response.Headers["Cache-Control"]; got != privatePageCacheControl {
		t.Fatalf("cart Cache-Control = %q, want %q", got, privatePageCacheControl)
	}
	assertBodyContains(t, response.Body, []string{
		`<meta name="robots" content="noindex, follow">`,
		`<link rel="canonical" href="` + canonicalHost + `/cart">`,
	})

	// The checkout page is session-gated now, so the noindex/no-store
	// assertions run against a signed-in render with a server cart.
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	addToTestCart(t, env.handler, jar, "mango-sticky-rice-kit", 1)
	checkoutResponse, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/checkout", jar))
	if err != nil {
		t.Fatalf("Handle checkout returned error: %v", err)
	}
	if checkoutResponse.StatusCode != http.StatusOK {
		t.Fatalf("checkout status code = %d, want %d", checkoutResponse.StatusCode, http.StatusOK)
	}
	if got := checkoutResponse.Headers["X-Robots-Tag"]; got != "noindex, follow" {
		t.Fatalf("checkout X-Robots-Tag = %q, want noindex, follow", got)
	}
	if got := checkoutResponse.Headers["Cache-Control"]; got != privatePageCacheControl {
		t.Fatalf("checkout Cache-Control = %q, want %q", got, privatePageCacheControl)
	}
	assertBodyContains(t, checkoutResponse.Body, []string{
		`<meta name="robots" content="noindex, follow">`,
		`<link rel="canonical" href="` + canonicalHost + `/checkout">`,
	})

	// The anonymous guest checkout render carries the same headers.
	guestRequest := pageRequest(http.MethodGet, "/checkout")
	guestRequest.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{{Slug: "mango-sticky-rice-kit", Quantity: 1}})}
	guestEnv := newAccountTestEnv(t)
	guestResponse, err := guestEnv.handler.Handle(context.Background(), guestRequest)
	if err != nil {
		t.Fatalf("Handle guest checkout returned error: %v", err)
	}
	if guestResponse.StatusCode != http.StatusOK {
		t.Fatalf("guest checkout status code = %d, want %d", guestResponse.StatusCode, http.StatusOK)
	}
	if got := guestResponse.Headers["X-Robots-Tag"]; got != "noindex, follow" {
		t.Fatalf("guest checkout X-Robots-Tag = %q, want noindex, follow", got)
	}
	if got := guestResponse.Headers["Cache-Control"]; got != privatePageCacheControl {
		t.Fatalf("guest checkout Cache-Control = %q, want %q", got, privatePageCacheControl)
	}
	assertBodyContains(t, guestResponse.Body, []string{`data-testid="guest-checkout-form"`})
}

// addToTestCart drives the real POST /cart/items mutation so signed-in tests
// exercise the server-cart write path.
func addToTestCart(t *testing.T, handler *Handler, jar testCookieJar, slug string, quantity int) {
	t.Helper()
	response, err := handler.Handle(context.Background(), jarFormPostRequest("/cart/items", url.Values{
		"slug":     {slug},
		"quantity": {strconv.Itoa(quantity)},
	}, jar))
	if err != nil {
		t.Fatalf("Handle cart add returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("cart add status = %d body %q, want 303", response.StatusCode, response.Body)
	}
	jar.update(t, response)
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

func TestCartMutationRejectsForeignOrigin(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	handler := NewHandler(cartRouteStore())
	request := formPostRequest("/cart/items", "slug=mango-sticky-rice-kit&quantity=1")
	request.Headers["origin"] = "https://evil.example.com"

	response, err := handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status code = %d, want %d for a cross-site cart mutation", response.StatusCode, http.StatusForbidden)
	}
	if len(response.Cookies) != 0 {
		t.Fatalf("response cookies = %#v, want none on a rejected mutation", response.Cookies)
	}
}

func TestCartMutationRejectsMissingOriginInProduction(t *testing.T) {
	t.Setenv(appenv.EnvAppEnvironment, appenv.EnvironmentProduction)
	request := formPostRequest("/cart/items", "slug=mango-sticky-rice-kit&quantity=1")

	if validCartMutationOrigin(request) {
		t.Fatal("validCartMutationOrigin = true, want false for missing Origin/Referer in production")
	}
}

func TestCartMutationAllowsMissingOriginOutsideProduction(t *testing.T) {
	t.Setenv(appenv.EnvAppEnvironment, "development")
	request := formPostRequest("/cart/items", "slug=mango-sticky-rice-kit&quantity=1")

	if !validCartMutationOrigin(request) {
		t.Fatal("validCartMutationOrigin = false, want true for local requests without Origin/Referer")
	}
}

func TestCartMutationAllowsSiteOrigin(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	handler := NewHandler(cartRouteStore())
	request := formPostRequest("/cart/items", "slug=mango-sticky-rice-kit&quantity=1")
	request.Headers["origin"] = "https://thailandgiftshop.com"

	response, err := handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status code = %d, want %d for a same-site cart mutation", response.StatusCode, http.StatusSeeOther)
	}
}

func TestCartMutationAllowsSiteReferer(t *testing.T) {
	t.Setenv(appenv.EnvAppEnvironment, appenv.EnvironmentProduction)
	request := formPostRequest("/cart/items", "slug=mango-sticky-rice-kit&quantity=1")
	request.Headers["referer"] = "https://www.thailandgiftshop.com/products/mango-sticky-rice-kit"

	if !validCartMutationOrigin(request) {
		t.Fatal("validCartMutationOrigin = false, want true for same-site Referer")
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
	if got := response.Headers["Cache-Control"]; got != privatePageCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, privatePageCacheControl)
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
		assertBodyContains(t, response.Body, []string{`<title>Cart | Thailand Gift Shop</title>`, `Your cart is empty.`, `Browse the latest Thai snacks`, `start your cart.`, `href="/products"`, `Continue shopping`})
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
		assertBodyContains(t, response.Body, []string{`Cart (2)`, `data-testid="cart-line-item"`, `src="/images/products/mango-sticky-rice-kit.jpg"`, `alt="Mango Sticky Rice Treats"`, `href="/products/mango-sticky-rice-kit"`, `Mango Sticky Rice Treats`, `$28.99`, `$57.98`, `action="/cart/items/mango-sticky-rice-kit/quantity"`, `name="quantity"`, `value="2"`, `max="5"`, `action="/cart/items/mango-sticky-rice-kit/remove"`, `action="/cart/clear"`, `href="/checkout"`, `Check out`, `Continue shopping`, `Clear cart`, `data-confirm="Remove all items from your cart? This can't be undone."`, `<dialog data-confirm-dialog`})
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
		`Remove unavailable items before checkout.`,
		`action="/cart/items/variant-shirt/remove"`,
		`type="hidden" name="variant_id" value="var-archived"`,
	})
	assertBodyOmits(t, response.Body, []string{`href="/checkout"`, `Check out`})
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

// TestCheckoutPageRendersAddressesAndPlaceOrder replaces the review-only
// checkout assertions with positive ones: address radio cards, the priced
// order summary, and the single place-order action.
func TestCheckoutPageRendersAddressesAndPlaceOrder(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	addToTestCart(t, env.handler, jar, "mango-sticky-rice-kit", 2)
	createTestAddress(t, env.handler, jar, "Anong Shopper")

	response, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/checkout", jar))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, response.Body, []string{
		`<title>Checkout | Thailand Gift Shop</title>`,
		`Payment is processed by Stripe. We never see or store your card number. Shipping is free while we launch; tax is not collected yet.`,
		`action="/checkout/place-order"`,
		`<fieldset class="grid gap-3">`,
		`<legend class="sr-only">Choose a shipping address</legend>`,
		`data-testid="checkout-address-option"`,
		`type="radio" name="address_id"`,
		`Anong Shopper`,
		`data-testid="checkout-line-item"`,
		`Mango Sticky Rice Treats`,
		`Quantity 2`,
		`$28.99`,
		`$57.98`,
		`data-testid="place-order-button"`,
		`Continue to payment`,
		`name="csrf_token"`,
	})
}

// createTestAddress saves a US address through the real POST /account/addresses
// flow and returns nothing; tests read IDs back from the addresses page.
func createTestAddress(t *testing.T, handler *Handler, jar testCookieJar, fullName string) {
	t.Helper()
	token := accountCSRFToken(t, handler, jar)
	response, err := handler.Handle(context.Background(), jarFormPostRequest("/account/addresses", url.Values{
		customerCSRFFieldName: {token},
		"full_name":           {fullName},
		"line1":               {"123 Sukhumvit Rd"},
		"city":                {"Bangkok"},
		"region":              {"NY"},
		"postal_code":         {"10110"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle address create returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("address create status = %d body %q, want 303", response.StatusCode, response.Body)
	}
	jar.update(t, response)
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
	if got := response.Headers["Cache-Control"]; got != privatePageCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, privatePageCacheControl)
	}
}

// TestCartPageExplainsStripeCheckout replaces the old PII-omission test with
// positive assertions: the cart explains the Stripe handoff and links to the
// real checkout. Card entry itself happens only on Stripe's hosted page.
func TestCartPageExplainsStripeCheckout(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	request := pageRequest(http.MethodGet, "/cart")
	request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{{Slug: "mango-sticky-rice-kit", Quantity: 1}})}

	response, err := NewHandler(cartRouteStore()).Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, response.Body, []string{
		`Payment is processed by Stripe at checkout. Shipping is free while we launch; tax is not collected yet.`,
		`href="/checkout"`,
		`Check out`,
	})
	// Card numbers are entered exclusively on checkout.stripe.com; the cart
	// never renders card inputs.
	assertBodyOmits(t, response.Body, []string{`name="card"`, `card_number`})
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
	// The cart was auto-adjusted (quantities reduced), so the shopper is told.
	assertBodyContains(t, response.Body, []string{`data-testid="cart-adjusted-notice"`, `We updated your cart`})
}

func TestCartPageOmitsAdjustedNoticeWhenCartUnchanged(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	request := pageRequest(http.MethodGet, "/cart")
	request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{{Slug: "mango-sticky-rice-kit", Quantity: 2}})}

	response, err := NewHandler(cartRouteStore()).Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	assertBodyOmits(t, response.Body, []string{`data-testid="cart-adjusted-notice"`})
}

func TestProductListingCapsProductsAtMaxListing(t *testing.T) {
	products := make([]catalog.Product, 0, maxListingProducts+5)
	for i := 0; i < maxListingProducts+5; i++ {
		products = append(products, catalog.Product{
			ID:            "prod_" + strconv.Itoa(i),
			Slug:          "product-" + strconv.Itoa(i),
			Name:          "Product " + strconv.Itoa(i),
			PriceCents:    100,
			Status:        catalog.StatusActive,
			StockQuantity: 5,
			SortOrder:     i,
		})
	}
	handler := NewHandler(catalog.NewMemoryStore(products, nil))

	response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/products"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if got := strings.Count(response.Body, `data-testid="product-card"`); got != maxListingProducts {
		t.Fatalf("product cards = %d, want the read capped at %d", got, maxListingProducts)
	}
}

func TestCheckoutPageExcludesDroppedStaleItems(t *testing.T) {
	// Stale server-cart lines (draft, sold out, deleted) are normalized away
	// before the signed-in checkout renders, and the repaired cart is written
	// back to both the CART row and the __Host-tgs_cart mirror.
	env := newAccountTestEnvWithProducts(t, append(accountTestCatalogProducts(),
		catalog.Product{ID: "prod_draft", Slug: "draft-product", Name: "Draft Product", Status: catalog.StatusDraft, StockQuantity: 5},
		catalog.Product{ID: "prod_sold_out", Slug: "sold-out", Name: "Sold Out", Status: catalog.StatusActive, StockQuantity: 0},
	))
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	customerID := accountCustomerID(t, env, "shopper@example.com")
	record, _, err := env.commerce.GetCart(context.Background(), customerID)
	if err != nil {
		t.Fatalf("GetCart returned error: %v", err)
	}
	record.CustomerID = customerID
	record.Lines = []cart.Line{
		{Slug: "mango-sticky-rice-kit", Quantity: 2},
		{Slug: "draft-product", Quantity: 1},
		{Slug: "sold-out", Quantity: 1},
		{Slug: "missing-product", Quantity: 1},
	}
	if _, err := env.commerce.PutCart(context.Background(), record); err != nil {
		t.Fatalf("PutCart returned error: %v", err)
	}

	response, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/checkout", jar))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	decoded := decodedCartFromResponse(t, response)
	if got := decoded.Lines(); len(got) != 1 || got[0].Slug != "mango-sticky-rice-kit" || got[0].Quantity != 2 {
		t.Fatalf("normalized mirror lines = %#v, want only active in-stock mango", got)
	}
	if lines := serverCartLines(t, env, customerID); len(lines) != 1 || lines[0].Slug != "mango-sticky-rice-kit" {
		t.Fatalf("repaired server cart = %#v, want only mango", lines)
	}
	assertBodyContains(t, response.Body, []string{`Mango Sticky Rice Treats`, `$57.98`})
	assertBodyOmits(t, response.Body, []string{`Draft Product`, `Sold Out`, `missing-product`})
}

func TestCartCookieFlagsHttpOnlySecureSameSitePathMaxAgeAndHostPrefix(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	// No forwarded-proto header: the __Host- prefix mandates Secure, so the
	// cart cookie sets it unconditionally even on a plain-HTTP request (which
	// browsers still accept over http://127.0.0.1).
	request := formPostRequest("/cart/items", "slug=mango-sticky-rice-kit&quantity=1")

	response, err := NewHandler(cartRouteStore()).Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	cookieHeader := response.Cookies[0]
	if !strings.HasPrefix(cookieHeader, "__Host-tgs_cart=") {
		t.Fatalf("cookie = %q, want __Host-tgs_cart name", cookieHeader)
	}
	for _, want := range []string{cart.CookieName + "=", "Path=/", "Max-Age=604800", "HttpOnly", "SameSite=Lax", "Secure"} {
		if !strings.Contains(cookieHeader, want) {
			t.Fatalf("cookie = %q, want %q", cookieHeader, want)
		}
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
	if got := response.Headers["Cache-Control"]; got != privatePageCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, privatePageCacheControl)
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
			statusCode:   http.StatusNotFound,
			bodyContains: []string{"Not found"},
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

func TestPublicHTMLPagesSetSharedCacheControl(t *testing.T) {
	handler := NewHandler(routeMatrixStore())
	for _, path := range []string{"/", "/products", "/products/thai-tea-sampler", "/categories", "/categories/thai-snacks", "/story"} {
		t.Run(path, func(t *testing.T) {
			response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
			}
			if got := response.Headers["Cache-Control"]; got != catalogPageCacheControl {
				t.Fatalf("Cache-Control = %q, want %q", got, catalogPageCacheControl)
			}
		})
	}
}

func TestCatalogPage404sDoNotSetCacheControl(t *testing.T) {
	handler := NewHandler(routeMatrixStore())
	for _, path := range []string{"/products/missing-product", "/categories/missing-category"} {
		t.Run(path, func(t *testing.T) {
			response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusNotFound)
			}
			if got := response.Headers["Cache-Control"]; got != "" {
				t.Fatalf("Cache-Control = %q, want empty on 404s", got)
			}
		})
	}
}

func TestSSROriginSecretBlocksDirectRequestsWhenConfigured(t *testing.T) {
	t.Setenv(envOriginHeaderSecret, "origin-secret")
	t.Setenv(envPreviousOriginHeaderSecret, "previous-origin-secret")
	handler := NewHandler(routeMatrixStore())

	blocked, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/"))
	if err != nil {
		t.Fatalf("Handle blocked returned error: %v", err)
	}
	if blocked.StatusCode != http.StatusForbidden {
		t.Fatalf("blocked status = %d, want %d", blocked.StatusCode, http.StatusForbidden)
	}
	if blocked.Body != "Forbidden" {
		t.Fatalf("blocked body = %q, want Forbidden", blocked.Body)
	}

	allowedRequest := pageRequest(http.MethodGet, "/")
	allowedRequest.Headers = map[string]string{originSecretHeaderName: "origin-secret"}
	allowed, err := handler.Handle(context.Background(), allowedRequest)
	if err != nil {
		t.Fatalf("Handle allowed returned error: %v", err)
	}
	if allowed.StatusCode != http.StatusOK {
		t.Fatalf("allowed status = %d, want %d", allowed.StatusCode, http.StatusOK)
	}

	previousRequest := pageRequest(http.MethodGet, "/")
	previousRequest.Headers = map[string]string{originSecretHeaderName: "previous-origin-secret"}
	previous, err := handler.Handle(context.Background(), previousRequest)
	if err != nil {
		t.Fatalf("Handle previous returned error: %v", err)
	}
	if previous.StatusCode != http.StatusOK {
		t.Fatalf("previous status = %d, want %d", previous.StatusCode, http.StatusOK)
	}

	mismatchedRequest := pageRequest(http.MethodGet, "/")
	mismatchedRequest.Headers = map[string]string{originSecretHeaderName: "wrong-secret"}
	mismatched, err := handler.Handle(context.Background(), mismatchedRequest)
	if err != nil {
		t.Fatalf("Handle mismatched returned error: %v", err)
	}
	if mismatched.StatusCode != http.StatusForbidden {
		t.Fatalf("mismatched status = %d, want %d", mismatched.StatusCode, http.StatusForbidden)
	}
}

func TestProductionSSROriginSecretMissingBlocksDirectRequests(t *testing.T) {
	t.Setenv(appenv.EnvAppEnvironment, appenv.EnvironmentProduction)
	t.Setenv(envOriginHeaderSecret, "")
	handler := NewHandler(routeMatrixStore())

	response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}
}

func TestLocalSSROriginSecretMissingAllowsDirectRequests(t *testing.T) {
	t.Setenv(appenv.EnvAppEnvironment, "development")
	t.Setenv(envOriginHeaderSecret, "")
	handler := NewHandler(routeMatrixStore())

	response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
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
	value, found := httpapi.NamedCookieValue(response.Cookies[0], cart.CookieName)
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
		staticassets.AppCSSPath,
		`/static/logo.svg`,
		`/static/home-hero.jpg`,
		`Bangkok gift shop online`,
		`Latest products`,
		`No products are available yet.`,
	} {
		if !strings.Contains(response.Body, want) {
			t.Fatalf("body does not contain %q: %q", want, response.Body)
		}
	}
	assertBodyOmits(t, response.Body, []string{
		`/static/vendor/htmx.min.js`,
		`/static/js/enhance.js`,
		`<style>.skip-link`,
	})
}

func TestEnhancementScriptIsScopedToInteractivePages(t *testing.T) {
	handler := NewHandler(routeMatrixStore())
	tests := []struct {
		path        string
		wantScript  bool
		description string
	}{
		{path: "/", description: "home"},
		{path: "/products", description: "product listing"},
		{path: "/categories", description: "category listing"},
		{path: "/story", description: "story"},
		{path: "/products/thai-tea-sampler", wantScript: true, description: "product detail add-to-cart form"},
		{path: "/account/sign-in", wantScript: true, description: "sign-in form"},
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, test.path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
			}

			hasScript := strings.Contains(response.Body, staticassets.EnhanceJSPath)
			if hasScript != test.wantScript {
				t.Fatalf("enhance script presence = %t, want %t in body: %q", hasScript, test.wantScript, response.Body)
			}
			assertBodyOmits(t, response.Body, []string{`/static/vendor/htmx.min.js`})
		})
	}
}

func TestPublicPagesRenderSEOHeadMetadata(t *testing.T) {
	handler := NewHandler(routeMatrixStore())
	tests := []struct {
		path        string
		title       string
		description string
		canonical   string
	}{
		{path: "/", title: "Thailand Gift Shop", description: "Browse a Thai gift-shop catalog of snacks, souvenirs, pantry favorites, textiles, decor, wellness, and small keepsakes.", canonical: canonicalHost + "/"},
		{path: "/products", title: "Products | Thailand Gift Shop", description: "Browse Thai snacks, souvenirs, textiles, pantry items, decor, wellness, and small keepsakes.", canonical: canonicalHost + "/products"},
		{path: "/products/thai-tea-sampler", title: "Thai Tea Selection | Thailand Gift Shop", description: "Loose leaf Thai tea and sweet snacks.", canonical: canonicalHost + "/products/thai-tea-sampler"},
		{path: "/categories", title: "Categories | Thailand Gift Shop", description: "Shop Thai gift-shop finds by aisle, including snacks, souvenirs, textiles, decor, wellness, and pantry favorites.", canonical: canonicalHost + "/categories"},
		{path: "/categories/thai-snacks", title: "Thai Snacks | Thailand Gift Shop", description: "Crunchy, sweet, and pantry-friendly finds.", canonical: canonicalHost + "/categories/thai-snacks"},
		{path: "/story", title: "Our Story | Thailand Gift Shop", description: "Learn how Thailand Gift Shop organizes Thai snacks, souvenirs, textiles, pantry items, decor, wellness, and small keepsakes for calm browsing.", canonical: canonicalHost + "/story"},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, test.path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
			}
			assertBodyContains(t, response.Body, []string{
				`<title>` + test.title + `</title>`,
				`<meta name="description" content="` + test.description + `">`,
				`<link rel="canonical" href="` + test.canonical + `">`,
				`<meta property="og:site_name" content="Thailand Gift Shop">`,
				`<meta property="og:title" content="` + test.title + `">`,
				`<meta property="og:description" content="` + test.description + `">`,
				`<meta property="og:url" content="` + test.canonical + `">`,
				`<meta name="twitter:card" content="summary_large_image">`,
				`<meta name="twitter:title" content="` + test.title + `">`,
				`<meta name="twitter:description" content="` + test.description + `">`,
			})
		})
	}
}

func TestHomeRendersOrganizationAndWebSiteJSONLD(t *testing.T) {
	response, err := NewHandler(routeMatrixStore()).Handle(context.Background(), pageRequest(http.MethodGet, "/"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}

	organization := structuredDataByID(t, response.Body, "structured-data-0")
	if organization["@type"] != "Organization" || organization["name"] != "Thailand Gift Shop" || organization["url"] != canonicalHost {
		t.Fatalf("organization JSON-LD = %#v", organization)
	}
	if organization["logo"] != canonicalHost+"/static/logo.svg" {
		t.Fatalf("organization logo = %#v, want canonical logo", organization["logo"])
	}
	website := structuredDataByID(t, response.Body, "structured-data-1")
	if website["@type"] != "WebSite" || website["name"] != "Thailand Gift Shop" || website["url"] != canonicalHost {
		t.Fatalf("website JSON-LD = %#v", website)
	}
}

func TestPublicPagesRenderVisibleBreadcrumbsAndBreadcrumbListJSONLD(t *testing.T) {
	handler := NewHandler(routeMatrixStore())
	tests := []struct {
		path             string
		structuredDataID string
		breadcrumbs      []breadcrumbItem
	}{
		{path: "/products", structuredDataID: "structured-data-0", breadcrumbs: productListingBreadcrumbs()},
		{path: "/products/thai-tea-sampler", structuredDataID: "structured-data-1", breadcrumbs: productDetailBreadcrumbs(catalog.Product{Slug: "thai-tea-sampler", Name: "Thai Tea Selection"})},
		{path: "/categories", structuredDataID: "structured-data-0", breadcrumbs: categoryIndexBreadcrumbs()},
		{path: "/categories/thai-snacks", structuredDataID: "structured-data-0", breadcrumbs: categoryDetailBreadcrumbs(catalog.Category{Slug: "thai-snacks", Name: "Thai Snacks"})},
		{path: "/story", structuredDataID: "structured-data-0", breadcrumbs: storyBreadcrumbs()},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, test.path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
			}
			assertBodyContains(t, response.Body, []string{`aria-label="Breadcrumb"`})
			for _, breadcrumb := range test.breadcrumbs {
				assertBodyContains(t, response.Body, []string{`href="` + breadcrumb.Path + `"`, breadcrumb.Name})
			}
			assertBreadcrumbJSONLD(t, response.Body, test.structuredDataID, test.breadcrumbs)
		})
	}
}

func TestHomeDoesNotRenderBreadcrumbListWithoutVisibleBreadcrumbs(t *testing.T) {
	response, err := NewHandler(routeMatrixStore()).Handle(context.Background(), pageRequest(http.MethodGet, "/"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertBodyOmits(t, response.Body, []string{`aria-label="Breadcrumb"`, `"@type":"BreadcrumbList"`})
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
	assertBodyContains(t, response.Body, []string{
		`<source type="image/webp" srcset="` + productImageWebPSrcset("/images/products/mango-sticky-rice-kit.jpg") + `"`,
		`<source type="image/jpeg" srcset="` + productImageFallbackSrcset("/images/products/mango-sticky-rice-kit.jpg") + `"`,
		`<source type="image/webp" srcset="` + productImageWebPSrcset("/images/placeholder-product.jpg") + `"`,
	})
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

func TestPublicHeaderRendersSharedCartLabelWithoutCatalogLookupsOrCookies(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	request := pageRequest(http.MethodGet, "/products")
	request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{{Slug: "mango-sticky-rice-kit", Quantity: 2}})}

	store := cartRouteStore()
	response, err := NewHandler(store).Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if len(response.Cookies) != 0 {
		t.Fatalf("response cookies = %v, want none on catalog pages", response.Cookies)
	}
	if got := store.productLookupCount("mango-sticky-rice-kit"); got != 0 {
		t.Fatalf("GetProductBySlug lookups for mango-sticky-rice-kit = %d, want 0 for the header label", got)
	}
	assertBodyContains(t, response.Body, []string{
		`href="/cart">Cart</a>`,
		`Products</a>`,
		`Categories</a>`,
		`Story</a>`,
	})
}

func TestPublicHeaderCartLinkLabelIgnoresCookieLinesWithoutValidatingProducts(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	request := pageRequest(http.MethodGet, "/products")
	request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{{Slug: "missing-product", Quantity: 3}})}

	store := cartRouteStore()
	response, err := NewHandler(store).Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if len(response.Cookies) != 0 {
		t.Fatalf("response cookies = %v, want none on catalog pages", response.Cookies)
	}
	if got := store.productLookupCount("missing-product"); got != 0 {
		t.Fatalf("GetProductBySlug lookups for missing-product = %d, want 0 for the header label", got)
	}
	// Public catalog pages keep a shared cache key; /cart and mutations are the
	// routes that drop unavailable products and repair the cookie.
	assertBodyContains(t, response.Body, []string{`href="/cart">Cart</a>`})
}

func TestCartBearingPagesReuseNormalizedRequestCart(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)

	// The anonymous checkout request renders the guest layout, and its cart
	// guard still normalizes the cart exactly once first.
	tests := []struct {
		path string
	}{
		{path: "/cart"},
		{path: "/checkout"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			store := cartRouteStore()
			request := pageRequest(http.MethodGet, test.path)
			request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{{Slug: "mango-sticky-rice-kit", Quantity: 2}})}

			response, err := NewHandler(store).Handle(context.Background(), request)
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
			}
			if got := store.productLookupCount("mango-sticky-rice-kit"); got != 1 {
				t.Fatalf("GetProductBySlug lookups for mango-sticky-rice-kit = %d, want 1", got)
			}
			assertBodyContains(t, response.Body, []string{`Cart (2)`, `Mango Sticky Rice Treats`})
			if test.path == "/checkout" {
				assertBodyContains(t, response.Body, []string{`data-testid="guest-checkout-form"`})
			}
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
		{name: "category detail", path: "/categories/thai-snacks", starts: []string{"ListActiveCategories", "ListActiveProductsByCategory:thai-snacks"}},
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

func TestCartProductLookupsStartConcurrently(t *testing.T) {
	store := newCartLookupBlockingStore()
	slugs := []string{"mango-sticky-rice-kit", "thai-tea-cookies", "coconut-rolls"}
	lines := make([]cart.Line, 0, len(slugs))
	for _, slug := range slugs {
		store.productsBySlug[slug] = catalog.Product{Slug: slug, Name: slug, Status: catalog.StatusActive, StockQuantity: 5}
		lines = append(lines, cart.Line{Slug: slug, Quantity: 1})
	}
	handler := NewHandler(store)
	result := make(chan cartProductsBySlugResult, 1)

	go func() {
		products, err := handler.cartProductsBySlug(context.Background(), lines)
		result <- cartProductsBySlugResult{products: products, err: err}
	}()

	if missing := missingCatalogStarts(store.started, slugs, 200*time.Millisecond); len(missing) > 0 {
		store.releaseAll()
		drainCartProductsBySlugResult(t, result)
		t.Fatalf("cart product lookups did not start concurrently; missing %s", strings.Join(missing, ", "))
	}
	store.releaseAll()

	got := drainCartProductsBySlugResult(t, result)
	if got.err != nil {
		t.Fatalf("cartProductsBySlug returned error: %v", got.err)
	}
	if len(got.products) != len(slugs) {
		t.Fatalf("lookup result count = %d, want %d", len(got.products), len(slugs))
	}
	for _, slug := range slugs {
		lookup, ok := got.products[slug]
		if !ok || !lookup.found || lookup.product.Slug != slug {
			t.Fatalf("lookup result for %q = %#v, found in map %t", slug, lookup, ok)
		}
	}
}

func TestCartProductLookupsAreBounded(t *testing.T) {
	store := newCartLookupBlockingStore()
	lineCount := maxCartProductLookupConcurrency + 5
	lines := make([]cart.Line, 0, lineCount)
	for i := range lineCount {
		slug := "bounded-product-" + strconv.Itoa(i)
		store.productsBySlug[slug] = catalog.Product{Slug: slug, Name: slug, Status: catalog.StatusActive, StockQuantity: 5}
		lines = append(lines, cart.Line{Slug: slug, Quantity: 1})
	}
	handler := NewHandler(store)
	result := make(chan cartProductsBySlugResult, 1)

	go func() {
		products, err := handler.cartProductsBySlug(context.Background(), lines)
		result <- cartProductsBySlugResult{products: products, err: err}
	}()

	initialStarts := make([]string, 0, maxCartProductLookupConcurrency)
	for range maxCartProductLookupConcurrency {
		select {
		case slug := <-store.started:
			initialStarts = append(initialStarts, slug)
		case <-time.After(time.Second):
			store.releaseAll()
			drainCartProductsBySlugResult(t, result)
			t.Fatalf("observed %d initial lookup starts, want %d", len(initialStarts), maxCartProductLookupConcurrency)
		}
	}
	store.releaseAll()

	got := drainCartProductsBySlugResult(t, result)
	if got.err != nil {
		t.Fatalf("cartProductsBySlug returned error: %v", got.err)
	}
	if gotMax := store.maxInFlightLookups(); gotMax > maxCartProductLookupConcurrency {
		t.Fatalf("max in-flight lookups = %d, want <= %d", gotMax, maxCartProductLookupConcurrency)
	}
	if len(got.products) != lineCount {
		t.Fatalf("lookup result count = %d, want %d", len(got.products), lineCount)
	}
}

func TestCartProductLookupsDeduplicateSlugs(t *testing.T) {
	store := newCartLookupBlockingStore()
	store.productsBySlug["variant-shirt"] = catalog.Product{
		Slug:   "variant-shirt",
		Name:   "Variant Shirt",
		Status: catalog.StatusActive,
		Variants: []catalog.ProductVariant{
			{ID: "small", Label: "Small", Status: catalog.StatusActive, StockQuantity: 3},
			{ID: "medium", Label: "Medium", Status: catalog.StatusActive, StockQuantity: 4},
		},
	}
	lines := []cart.Line{
		{Slug: "variant-shirt", VariantID: "small", Quantity: 1},
		{Slug: "variant-shirt", VariantID: "medium", Quantity: 2},
		{Slug: "variant-shirt", VariantID: "small", Quantity: 3},
	}
	handler := NewHandler(store)
	result := make(chan cartProductsBySlugResult, 1)

	go func() {
		products, err := handler.cartProductsBySlug(context.Background(), lines)
		result <- cartProductsBySlugResult{products: products, err: err}
	}()

	if missing := missingCatalogStarts(store.started, []string{"variant-shirt"}, 200*time.Millisecond); len(missing) > 0 {
		store.releaseAll()
		drainCartProductsBySlugResult(t, result)
		t.Fatalf("cart product lookup did not start; missing %s", strings.Join(missing, ", "))
	}
	store.releaseAll()

	got := drainCartProductsBySlugResult(t, result)
	if got.err != nil {
		t.Fatalf("cartProductsBySlug returned error: %v", got.err)
	}
	if gotCount := store.productLookupCount("variant-shirt"); gotCount != 1 {
		t.Fatalf("GetProductBySlug lookups for variant-shirt = %d, want 1", gotCount)
	}
	if len(got.products) != 1 {
		t.Fatalf("lookup result count = %d, want 1", len(got.products))
	}
	if lookup := got.products["variant-shirt"]; !lookup.found || lookup.product.Slug != "variant-shirt" {
		t.Fatalf("lookup result for variant-shirt = %#v", lookup)
	}
}

func TestCartProductLookupsReturnHardErrors(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	store := newCartLookupBlockingStore()
	lookupErr := errors.New("catalog lookup failed")
	store.productsBySlug["ok-product"] = catalog.Product{Slug: "ok-product", Name: "OK Product", Status: catalog.StatusActive, StockQuantity: 5}
	store.productsBySlug["error-product"] = catalog.Product{Slug: "error-product", Name: "Error Product", Status: catalog.StatusActive, StockQuantity: 5}
	store.errorsBySlug["error-product"] = lookupErr
	request := pageRequest(http.MethodGet, "/cart")
	request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{
		{Slug: "ok-product", Quantity: 1},
		{Slug: "error-product", Quantity: 1},
	})}
	handler := NewHandler(store)
	result := make(chan handleResult, 1)

	go func() {
		response, err := handler.Handle(context.Background(), request)
		result <- handleResult{response: response, err: err}
	}()

	wantStarts := []string{"ok-product", "error-product"}
	if missing := missingCatalogStarts(store.started, wantStarts, 200*time.Millisecond); len(missing) > 0 {
		store.releaseAll()
		drainHandleResult(t, result)
		t.Fatalf("cart product lookups did not start; missing %s", strings.Join(missing, ", "))
	}
	store.releaseSlug("error-product")

	got := drainHandleResult(t, result)
	if got.err != nil {
		t.Fatalf("Handle returned error: %v", got.err)
	}
	if got.response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status code = %d, want %d", got.response.StatusCode, http.StatusInternalServerError)
	}
	if got.response.Body != "Internal server error" {
		t.Fatalf("body = %q, want %q", got.response.Body, "Internal server error")
	}
	assertCartLookupsCompleted(t, store.completed, wantStarts)
}

func TestCartProductLookupsCancelBlockedWorkers(t *testing.T) {
	store := newCartLookupBlockingStore()
	slugs := []string{"first-blocked", "second-blocked", "third-blocked"}
	lines := make([]cart.Line, 0, len(slugs))
	for _, slug := range slugs {
		store.productsBySlug[slug] = catalog.Product{Slug: slug, Name: slug, Status: catalog.StatusActive, StockQuantity: 5}
		lines = append(lines, cart.Line{Slug: slug, Quantity: 1})
	}
	handler := NewHandler(store)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan cartProductsBySlugResult, 1)

	go func() {
		products, err := handler.cartProductsBySlug(ctx, lines)
		result <- cartProductsBySlugResult{products: products, err: err}
	}()

	if missing := missingCatalogStarts(store.started, slugs, 200*time.Millisecond); len(missing) > 0 {
		store.releaseAll()
		drainCartProductsBySlugResult(t, result)
		t.Fatalf("cart product lookups did not start; missing %s", strings.Join(missing, ", "))
	}
	cancel()

	got := drainCartProductsBySlugResult(t, result)
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("cartProductsBySlug error = %v, want context.Canceled", got.err)
	}
	if got.products != nil {
		t.Fatalf("products = %#v, want nil on cancellation", got.products)
	}
	assertCartLookupsCompleted(t, store.completed, slugs)
}

func TestCartProductLookupsPreserveOrder(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	store := newCartLookupBlockingStore()
	products := []catalog.Product{
		{Slug: "first-product", Name: "First Product", Status: catalog.StatusActive, StockQuantity: 5, PriceCents: 1000},
		{Slug: "second-product", Name: "Second Product", Status: catalog.StatusActive, StockQuantity: 5, PriceCents: 2000},
		{Slug: "third-product", Name: "Third Product", Status: catalog.StatusActive, StockQuantity: 5, PriceCents: 3000},
	}
	for _, product := range products {
		store.productsBySlug[product.Slug] = product
	}
	request := pageRequest(http.MethodGet, "/cart")
	request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{
		{Slug: "first-product", Quantity: 6},
		{Slug: "second-product", Quantity: 7},
		{Slug: "third-product", Quantity: 8},
	})}
	handler := NewHandler(store)
	result := make(chan handleResult, 1)

	go func() {
		response, err := handler.Handle(context.Background(), request)
		result <- handleResult{response: response, err: err}
	}()

	wantSlugs := []string{"first-product", "second-product", "third-product"}
	if missing := missingCatalogStarts(store.started, wantSlugs, 200*time.Millisecond); len(missing) > 0 {
		store.releaseAll()
		drainHandleResult(t, result)
		t.Fatalf("cart product lookups did not start; missing %s", strings.Join(missing, ", "))
	}
	store.releaseSlug("third-product")
	store.releaseSlug("first-product")
	store.releaseSlug("second-product")

	got := drainHandleResult(t, result)
	if got.err != nil {
		t.Fatalf("Handle returned error: %v", got.err)
	}
	if got.response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", got.response.StatusCode, http.StatusOK)
	}
	decoded := decodedCartFromResponse(t, got.response)
	decodedLines := decoded.Lines()
	if len(decodedLines) != len(wantSlugs) {
		t.Fatalf("decoded line count = %d, want %d: %#v", len(decodedLines), len(wantSlugs), decodedLines)
	}
	for index, wantSlug := range wantSlugs {
		if decodedLines[index].Slug != wantSlug || decodedLines[index].Quantity != 5 {
			t.Fatalf("decoded line %d = %#v, want slug %q capped to 5", index, decodedLines[index], wantSlug)
		}
		if gotCount := store.productLookupCount(wantSlug); gotCount != 1 {
			t.Fatalf("GetProductBySlug lookups for %s = %d, want 1", wantSlug, gotCount)
		}
	}
	assertBodyContainsInOrder(t, got.response.Body, []string{"First Product", "Second Product", "Third Product"})
}

func TestCartProductLookupsDeduplicatedVariantLinesRenderBothLines(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	store := cartRouteStore()
	request := pageRequest(http.MethodGet, "/cart")
	request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{
		{Slug: "variant-shirt", VariantID: "var-small", Quantity: 1},
		{Slug: "variant-shirt", VariantID: "var-large", Quantity: 2},
	})}

	response, err := NewHandler(store).Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if gotCount := store.productLookupCount("variant-shirt"); gotCount != 1 {
		t.Fatalf("GetProductBySlug lookups for variant-shirt = %d, want 1", gotCount)
	}
	if gotLines := strings.Count(response.Body, `data-testid="cart-line-item"`); gotLines != 2 {
		t.Fatalf("rendered cart line count = %d, want 2: %q", gotLines, response.Body)
	}
	assertBodyContains(t, response.Body, []string{
		"Thai Linen Shirt",
		"Size Small",
		"Size Large",
		`type="hidden" name="variant_id" value="var-small"`,
		`type="hidden" name="variant_id" value="var-large"`,
	})
	assertBodyContainsInOrder(t, response.Body, []string{"Size Small", "Size Large"})
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
	if len(store.activeProductLimits) != 1 || store.activeProductLimits[0] != maxListingProducts {
		t.Fatalf("ListActiveProducts limits = %v, want [%d]", store.activeProductLimits, maxListingProducts)
	}
	assertBodyContains(t, response.Body, []string{
		`<h1 class="mt-3 font-display text-5xl font-bold leading-none tracking-tight text-flag-blue sm:text-6xl">Products</h1>`,
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
	if got := store.productLookupCount("mango-sticky-rice-kit"); got != 1 {
		t.Fatalf("GetProductBySlug lookups for mango-sticky-rice-kit = %d, want 1", got)
	}
	assertBodyContains(t, response.Body, []string{
		`href="/products"`,
		`Back to products`,
		`<h1 class="mt-5 font-display text-5xl font-bold leading-none tracking-tight text-flag-blue sm:text-6xl">Mango Sticky Rice Treats</h1>`,
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

func TestProductDetailRendersProductOfferJSONLDOnlyOnDetailPage(t *testing.T) {
	product := catalog.Product{
		Slug:          "mango-sticky-rice-kit",
		Name:          "Mango Sticky Rice Treats",
		Description:   "Shelf-stable Thai dessert snacks.",
		PriceCents:    2899,
		ImageURL:      "/images/products/mango-sticky-rice-kit.jpg",
		Status:        catalog.StatusActive,
		StockQuantity: 4,
	}
	handler := NewHandlerWithProductImagePlaceholderURL(&fakeCatalogStore{
		products:       []catalog.Product{product},
		productsBySlug: map[string]catalog.Product{product.Slug: product},
	}, "/images/placeholder-product.jpg")

	detailResponse, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/products/mango-sticky-rice-kit"))
	if err != nil {
		t.Fatalf("Handle detail returned error: %v", err)
	}
	if detailResponse.StatusCode != http.StatusOK {
		t.Fatalf("detail status code = %d, want %d", detailResponse.StatusCode, http.StatusOK)
	}
	productData := structuredDataByID(t, detailResponse.Body, "structured-data-0")
	if productData["@type"] != "Product" || productData["name"] != product.Name || productData["description"] != product.Description {
		t.Fatalf("product JSON-LD = %#v", productData)
	}
	if productData["url"] != canonicalHost+"/products/mango-sticky-rice-kit" {
		t.Fatalf("product url = %#v", productData["url"])
	}
	if productData["image"] != canonicalHost+"/images/products/mango-sticky-rice-kit.jpg" {
		t.Fatalf("product image = %#v", productData["image"])
	}
	offer, ok := productData["offers"].(map[string]any)
	if !ok {
		t.Fatalf("offers missing or wrong type: %#v", productData["offers"])
	}
	if offer["@type"] != "Offer" || offer["priceCurrency"] != "USD" || offer["price"] != "28.99" || offer["availability"] != "https://schema.org/InStock" {
		t.Fatalf("offer JSON-LD = %#v", offer)
	}

	listingResponse, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, "/products"))
	if err != nil {
		t.Fatalf("Handle listing returned error: %v", err)
	}
	assertBodyOmits(t, listingResponse.Body, []string{`"@type":"Product"`, `"@type":"Offer"`})
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
		`Checkout is handled securely by Stripe.`,
		`Shipping is free while we launch; tax is not collected yet.`,
	})
	assertBodyOmits(t, response.Body, []string{
		`prod_active_cart`,
		`/products/prod_`,
		`href="/categories/hidden-category"`,
		`Hidden Category`,
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
		`<option value="var-small" data-stock="2">Small</option>`,
		`<option value="var-large" data-stock="7">Large</option>`,
		`<option value="var-sold-out" disabled>Medium - out of stock</option>`,
		`type="number" name="quantity" value="1" min="1" max="9"`,
		`data-variant-stock-hint`,
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
			if !strings.Contains(response.Body, "Not found") {
				t.Fatalf("body does not contain %q: %q", "Not found", response.Body)
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
		`<h1 class="mt-3 font-display text-5xl font-bold leading-none tracking-tight text-flag-blue sm:text-6xl">Categories</h1>`,
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
			{
				Slug:        "temple-bells",
				Name:        "Temple Bells",
				Description: "Small brass bells and shrine-side keepsakes.",
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
	if len(store.categoryProductLimits) != 1 || store.categoryProductLimits[0] != maxListingProducts {
		t.Fatalf("ListActiveProductsByCategory limits = %v, want [%d]", store.categoryProductLimits, maxListingProducts)
	}
	assertBodyContains(t, response.Body, []string{
		`href="/categories"`,
		`Back to categories`,
		`<h1 class="mt-5 font-display text-5xl font-bold leading-none tracking-tight text-flag-blue sm:text-6xl">Thai Snacks</h1>`,
		`Crunchy, sweet, and pantry-friendly finds.`,
		`href="/products/thai-tea-sampler"`,
		`Thai Tea Selection`,
		`Loose leaf Thai tea and sweet snacks.`,
		`$21.99`,
		`In stock`,
		`aria-label="More categories"`,
		`Browse more Thai gift-shop categories`,
		`href="/categories/temple-bells"`,
		`Temple Bells`,
	})
	if got := strings.Count(response.Body, `href="/categories/thai-snacks"`); got != 1 {
		t.Fatalf("current category links = %d, want only the breadcrumb link: %q", got, response.Body)
	}
	assertBodyOmits(t, response.Body, []string{
		`href="/products/draft-snack"`,
		`Draft Snack`,
		`prod_active_101`,
		`/products/prod_`,
		`href="/categories/hidden-category"`,
		`Hidden Category`,
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

func TestCategoryDetailMissingInactiveAndUnlistedSlugsReturn404(t *testing.T) {
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
			if !strings.Contains(response.Body, "Not found") {
				t.Fatalf("body does not contain %q: %q", "Not found", response.Body)
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
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	handler := NewHandler(&fakeCatalogStore{err: errors.New("story should not query catalog")})

	request := pageRequest(http.MethodGet, "/story")
	request.Cookies = []string{cart.CookieName + "=" + encodedTestCart(t, []cart.Line{{Slug: "mango-sticky-rice-kit", Quantity: 2}})}
	response, err := handler.Handle(context.Background(), request)
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
		`href="/cart">Cart</a>`,
		`<!doctype html>`,
		`<html lang="en" class="scroll-smooth">`,
		`<title>Our Story | Thailand Gift Shop</title>`,
		`src="/static/logo.svg"`,
		`aria-label="Main navigation"`,
		`href="/products"`,
		`href="/categories"`,
		`href="/story"`,
		`href="/cart"`,
		`<h1 class="mt-3 font-display text-5xl font-bold leading-none tracking-tight text-flag-blue sm:text-6xl">Our Story</h1>`,
		`Bangkok gift shop online`,
		`Thai snacks, souvenirs, textiles, pantry items, decor, wellness, and small keepsakes`,
		`Thailand Gift Shop is a Bangkok gift shop online for Thai snacks, souvenirs, textiles, pantry items, decor, wellness, and small keepsakes.`,
		`Each aisle is shaped for calm browsing: clear categories, strong product images, concise details, and slug-based links that work without JavaScript.`,
		`The shop point of view is market-bright and practical, rooted in the colors, textures, pantry flavors, and compact keepsakes travelers remember from Bangkok gift shops.`,
		`Checkout is handled securely by Stripe — we never see or store card numbers. Shipping is free while we launch; tax is not collected yet.`,
	})
}

// TestStoryExplainsStripeCheckout replaces the review-only story assertion
// with the live-checkout disclosure copy.
func TestStoryExplainsStripeCheckout(t *testing.T) {
	response, err := NewHandler(&fakeCatalogStore{}).Handle(context.Background(), pageRequest(http.MethodGet, "/story"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, response.Body, []string{
		`Thai gift-shop catalog`,
		`Checkout is handled securely by Stripe. We never see or store card numbers.`,
		`Checkout is handled securely by Stripe — we never see or store card numbers. Shipping is free while we launch; tax is not collected yet.`,
	})
	assertBodyOmits(t, response.Body, []string{"review-only", "Review only", "review only"})
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
		"Search",
		"Filter",
		"Sort",
	}
}

type fakeCatalogStore struct {
	mu                     sync.Mutex
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

func (f *fakeCatalogStore) productLookupCount(slug string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	count := 0
	for _, lookup := range f.productSlugLookups {
		if lookup == slug {
			count++
		}
	}
	return count
}

type handleResult struct {
	response events.APIGatewayV2HTTPResponse
	err      error
}

type cartProductsBySlugResult struct {
	products map[string]cartProductLookupResult
	err      error
}

type blockingCatalogStore struct {
	started  chan string
	release  chan struct{}
	product  catalog.Product
	category catalog.Category
}

type cartLookupBlockingStore struct {
	mu                sync.Mutex
	started           chan string
	completed         chan string
	releaseAllCh      chan struct{}
	releaseBySlug     map[string]chan struct{}
	productsBySlug    map[string]catalog.Product
	errorsBySlug      map[string]error
	category          catalog.Category
	products          []catalog.Product
	inFlight          int
	maxInFlight       int
	lookupCountBySlug map[string]int
	releaseAllOnce    sync.Once
	releasedAll       bool
}

var (
	_ = newCartLookupBlockingStore
	_ = (*cartLookupBlockingStore).releaseSlug
	_ = (*cartLookupBlockingStore).releaseAll
	_ = (*cartLookupBlockingStore).maxInFlightLookups
)

func newCartLookupBlockingStore() *cartLookupBlockingStore {
	return &cartLookupBlockingStore{
		started:           make(chan string, 128),
		completed:         make(chan string, 128),
		releaseAllCh:      make(chan struct{}),
		releaseBySlug:     make(map[string]chan struct{}),
		productsBySlug:    make(map[string]catalog.Product),
		errorsBySlug:      make(map[string]error),
		lookupCountBySlug: make(map[string]int),
	}
}

func (s *cartLookupBlockingStore) ListActiveProducts(ctx context.Context, limit int) ([]catalog.Product, error) {
	return s.products, nil
}

func (s *cartLookupBlockingStore) ListRecentlyAddedProducts(ctx context.Context, limit int) ([]catalog.Product, error) {
	return s.products, nil
}

func (s *cartLookupBlockingStore) GetProductBySlug(ctx context.Context, slug string) (catalog.Product, bool, error) {
	s.mu.Lock()
	s.inFlight++
	if s.inFlight > s.maxInFlight {
		s.maxInFlight = s.inFlight
	}
	s.lookupCountBySlug[slug]++
	releaseAllCh := s.releaseAllCh
	releaseCh := s.releaseBySlug[slug]
	if releaseCh == nil {
		releaseCh = make(chan struct{})
		s.releaseBySlug[slug] = releaseCh
	}
	product, found := s.productsBySlug[slug]
	lookupErr := s.errorsBySlug[slug]
	s.mu.Unlock()

	s.started <- slug
	defer func() {
		s.mu.Lock()
		s.inFlight--
		s.mu.Unlock()
		s.completed <- slug
	}()

	select {
	case <-releaseCh:
	case <-releaseAllCh:
	case <-ctx.Done():
		return catalog.Product{}, false, ctx.Err()
	}

	if lookupErr != nil {
		return catalog.Product{}, false, lookupErr
	}
	return product, found, nil
}

func (s *cartLookupBlockingStore) ListActiveCategories(ctx context.Context) ([]catalog.Category, error) {
	if s.category.Slug == "" {
		return []catalog.Category{}, nil
	}
	return []catalog.Category{s.category}, nil
}

func (s *cartLookupBlockingStore) ListActiveProductsByCategory(ctx context.Context, categorySlug string, limit int) ([]catalog.Product, error) {
	if categorySlug != s.category.Slug {
		return []catalog.Product{}, nil
	}
	return s.products, nil
}

func (s *cartLookupBlockingStore) releaseSlug(slug string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.releasedAll {
		return
	}
	if ch, ok := s.releaseBySlug[slug]; ok {
		select {
		case <-ch:
		default:
			close(ch)
		}
		return
	}
	ch := make(chan struct{})
	close(ch)
	s.releaseBySlug[slug] = ch
}

func (s *cartLookupBlockingStore) releaseAll() {
	s.releaseAllOnce.Do(func() {
		s.mu.Lock()
		s.releasedAll = true
		s.mu.Unlock()
		close(s.releaseAllCh)
	})
}

func (s *cartLookupBlockingStore) maxInFlightLookups() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxInFlight
}

func (s *cartLookupBlockingStore) productLookupCount(slug string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lookupCountBySlug[slug]
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

func assertCartLookupsCompleted(t *testing.T, completed <-chan string, wants []string) {
	t.Helper()
	missing := missingCatalogStarts(completed, wants, time.Second)
	if len(missing) > 0 {
		t.Fatalf("cart product lookups did not complete; missing %s", strings.Join(missing, ", "))
	}
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

func drainCartProductsBySlugResult(t *testing.T, result <-chan cartProductsBySlugResult) cartProductsBySlugResult {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(time.Second):
		t.Fatal("cartProductsBySlug did not finish")
	}
	return cartProductsBySlugResult{}
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
	f.mu.Lock()
	f.productSlugLookups = append(f.productSlugLookups, slug)
	f.mu.Unlock()
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

func assertBodyContainsInOrder(t *testing.T, body string, wants []string) {
	t.Helper()
	searchFrom := 0
	for _, want := range wants {
		index := strings.Index(body[searchFrom:], want)
		if index < 0 {
			t.Fatalf("body does not contain %q after byte %d: %q", want, searchFrom, body)
		}
		searchFrom += index + len(want)
	}
}

func assertBreadcrumbJSONLD(t *testing.T, body string, id string, want []breadcrumbItem) {
	t.Helper()
	data := structuredDataByID(t, body, id)
	if data["@type"] != "BreadcrumbList" {
		t.Fatalf("structured data %s type = %#v, want BreadcrumbList", id, data["@type"])
	}
	items, ok := data["itemListElement"].([]any)
	if !ok {
		t.Fatalf("breadcrumb itemListElement missing or wrong type: %#v", data["itemListElement"])
	}
	if len(items) != len(want) {
		t.Fatalf("breadcrumb item count = %d, want %d: %#v", len(items), len(want), items)
	}
	for index, itemValue := range items {
		item, ok := itemValue.(map[string]any)
		if !ok {
			t.Fatalf("breadcrumb item %d wrong type: %#v", index, itemValue)
		}
		if item["@type"] != "ListItem" || item["position"] != float64(index+1) || item["name"] != want[index].Name || item["item"] != canonicalURL(want[index].Path) {
			t.Fatalf("breadcrumb item %d = %#v, want %#v", index, item, want[index])
		}
	}
}

func structuredDataByID(t *testing.T, body string, id string) map[string]any {
	t.Helper()
	startMarker := `<script id="` + id + `" type="application/ld+json">`
	start := strings.Index(body, startMarker)
	if start < 0 {
		t.Fatalf("structured data script %q not found in body: %q", id, body)
	}
	jsonStart := start + len(startMarker)
	end := strings.Index(body[jsonStart:], `</script>`)
	if end < 0 {
		t.Fatalf("structured data script %q is missing closing tag", id)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(body[jsonStart:jsonStart+end]), &data); err != nil {
		t.Fatalf("structured data script %q is not valid JSON: %v", id, err)
	}
	return data
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

// --- Customer accounts, sessions, and server carts ---

type accountTestEnv struct {
	handler  *Handler
	commerce *commerce.MemoryStore
	email    *email.FakeSender
	payments *payments.FakeProvider
	catalog  *catalog.MemoryStore
}

func newAccountTestEnv(t *testing.T) accountTestEnv {
	t.Helper()
	return newAccountTestEnvWithProducts(t, accountTestCatalogProducts())
}

func newAccountTestEnvWithProducts(t *testing.T, products []catalog.Product) accountTestEnv {
	t.Helper()
	t.Setenv(cart.EnvCookieSecret, ssrTestCartSecret)
	t.Setenv(commerce.EnvSessionSecret, ssrTestSessionSecret)
	catalogStore := catalog.NewMemoryStore(products, nil)
	commerceStore := commerce.NewMemoryStore()
	provider := payments.NewFakeProvider()
	handler := NewLocalDemoHandler(catalogStore, commerceStore, provider)
	handler.passwordHashCost = bcrypt.MinCost
	handler.passwordResetFloor = 0 // keep reset-request unit tests fast and deterministic
	fakeEmail, ok := handler.emailSender.(*email.FakeSender)
	if !ok {
		t.Fatalf("handler email sender = %T, want *email.FakeSender", handler.emailSender)
	}
	return accountTestEnv{handler: handler, commerce: commerceStore, email: fakeEmail, payments: provider, catalog: catalogStore}
}

func accountTestCatalogProducts() []catalog.Product {
	return []catalog.Product{
		{
			ID:            "prod_account_mango",
			Slug:          "mango-sticky-rice-kit",
			Name:          "Mango Sticky Rice Treats",
			Description:   "Shelf-stable Thai dessert snacks.",
			PriceCents:    2899,
			ImageURL:      "/images/products/mango-sticky-rice-kit.jpg",
			Status:        catalog.StatusActive,
			StockQuantity: 5,
		},
		{
			ID:            "prod_account_tea",
			Slug:          "thai-tea-sampler",
			Name:          "Thai Tea Selection",
			Description:   "Loose leaf Thai tea and sweet snacks.",
			PriceCents:    2199,
			ImageURL:      "/images/products/thai-tea-sampler.jpg",
			Status:        catalog.StatusActive,
			StockQuantity: 9,
		},
	}
}

// testCookieJar tracks Set-Cookie state across the multi-request account
// flows the way a browser would.
type testCookieJar map[string]string

func (jar testCookieJar) update(t *testing.T, response events.APIGatewayV2HTTPResponse) {
	t.Helper()
	for _, setCookie := range response.Cookies {
		header := http.Header{}
		header.Add("Set-Cookie", setCookie)
		parsed := (&http.Response{Header: header}).Cookies()
		if len(parsed) != 1 {
			t.Fatalf("unparseable Set-Cookie %q", setCookie)
		}
		cookie := parsed[0]
		if cookie.MaxAge < 0 {
			delete(jar, cookie.Name)
			continue
		}
		jar[cookie.Name] = cookie.Value
	}
}

func (jar testCookieJar) requestCookies() []string {
	cookies := make([]string, 0, len(jar))
	for name, value := range jar {
		cookies = append(cookies, name+"="+value)
	}
	sort.Strings(cookies)
	return cookies
}

func cloneJar(jar testCookieJar) testCookieJar {
	cloned := testCookieJar{}
	for name, value := range jar {
		cloned[name] = value
	}
	return cloned
}

func jarPageRequest(method string, path string, jar testCookieJar) events.APIGatewayV2HTTPRequest {
	request := pageRequest(method, path)
	request.Cookies = jar.requestCookies()
	return request
}

func jarFormPostRequest(path string, form url.Values, jar testCookieJar) events.APIGatewayV2HTTPRequest {
	request := formPostRequest(path, form.Encode())
	request.Cookies = jar.requestCookies()
	return request
}

func hiddenInputValue(t *testing.T, body string, name string) string {
	t.Helper()
	marker := `name="` + name + `" value="`
	start := strings.Index(body, marker)
	if start < 0 {
		t.Fatalf("hidden input %q not found in body: %q", name, body)
	}
	rest := body[start+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatalf("hidden input %q is unterminated in body", name)
	}
	return rest[:end]
}

func rawSetCookie(t *testing.T, response events.APIGatewayV2HTTPResponse, name string) string {
	t.Helper()
	for _, setCookie := range response.Cookies {
		if strings.HasPrefix(setCookie, name+"=") {
			return setCookie
		}
	}
	t.Fatalf("Set-Cookie for %q not found in %#v", name, response.Cookies)
	return ""
}

func optionalSetCookie(response events.APIGatewayV2HTTPResponse, name string) (string, bool) {
	for _, setCookie := range response.Cookies {
		if strings.HasPrefix(setCookie, name+"=") {
			return setCookie, true
		}
	}
	return "", false
}

func setCookieValue(t *testing.T, response events.APIGatewayV2HTTPResponse, name string) string {
	t.Helper()
	for _, setCookie := range response.Cookies {
		if value, found := httpapi.NamedCookieValue(setCookie, name); found {
			return value
		}
	}
	t.Fatalf("Set-Cookie for %q not found in %#v", name, response.Cookies)
	return ""
}

func guestCSRFTokenFor(t *testing.T, handler *Handler, jar testCookieJar, path string) string {
	t.Helper()
	response, err := handler.Handle(context.Background(), jarPageRequest(http.MethodGet, path, jar))
	if err != nil {
		t.Fatalf("Handle %s returned error: %v", path, err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%s status = %d, want %d", path, response.StatusCode, http.StatusOK)
	}
	jar.update(t, response)
	return hiddenInputValue(t, response.Body, guestCSRFFieldName)
}

func signUpTestCustomer(t *testing.T, handler *Handler, jar testCookieJar, email string, password string) events.APIGatewayV2HTTPResponse {
	t.Helper()
	token := guestCSRFTokenFor(t, handler, jar, "/account/sign-up")
	response, err := handler.Handle(context.Background(), jarFormPostRequest("/account/sign-up", url.Values{
		guestCSRFFieldName: {token},
		"email":            {email},
		"password":         {password},
	}, jar))
	if err != nil {
		t.Fatalf("sign-up Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign-up status = %d body %q, want %d", response.StatusCode, response.Body, http.StatusSeeOther)
	}
	jar.update(t, response)
	return response
}

func signInTestCustomer(t *testing.T, handler *Handler, jar testCookieJar, email string, password string) events.APIGatewayV2HTTPResponse {
	t.Helper()
	token := guestCSRFTokenFor(t, handler, jar, "/account/sign-in")
	response, err := handler.Handle(context.Background(), jarFormPostRequest("/account/sign-in", url.Values{
		guestCSRFFieldName: {token},
		"email":            {email},
		"password":         {password},
	}, jar))
	if err != nil {
		t.Fatalf("sign-in Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign-in status = %d body %q, want %d", response.StatusCode, response.Body, http.StatusSeeOther)
	}
	jar.update(t, response)
	return response
}

func accountCSRFToken(t *testing.T, handler *Handler, jar testCookieJar) string {
	t.Helper()
	response, err := handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/account", jar))
	if err != nil {
		t.Fatalf("Handle /account returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("/account status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	jar.update(t, response)
	return hiddenInputValue(t, response.Body, customerCSRFFieldName)
}

func accountCustomerID(t *testing.T, env accountTestEnv, email string) string {
	t.Helper()
	customer, found, err := env.commerce.GetCustomerByEmail(context.Background(), commerce.NormalizeEmail(email))
	if err != nil || !found {
		t.Fatalf("GetCustomerByEmail(%q) = found %t, err %v", email, found, err)
	}
	return customer.ID
}

func serverCartLines(t *testing.T, env accountTestEnv, customerID string) []cart.Line {
	t.Helper()
	record, _, err := env.commerce.GetCart(context.Background(), customerID)
	if err != nil {
		t.Fatalf("GetCart returned error: %v", err)
	}
	return record.Lines
}

func TestAccountRouteMethodGates(t *testing.T) {
	postOnlyPaths := []string{
		"/account/sign-out",
		"/account/password",
		"/account/addresses/" + ssrTestAccountID + "/update",
		"/account/addresses/" + ssrTestAccountID + "/remove",
		"/account/addresses/" + ssrTestAccountID + "/default",
		"/account/payment-methods/add",
		"/account/payment-methods/pm_fake_visa_4242/remove",
	}
	for _, path := range postOnlyPaths {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(method+" "+path, func(t *testing.T) {
				response, err := Handle(context.Background(), pageRequest(method, path))
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

	pageOnlyPaths := []string{
		"/account",
		"/account/addresses/" + ssrTestAccountID + "/edit",
		"/account/payment-methods",
	}
	for _, path := range pageOnlyPaths {
		t.Run("POST "+path, func(t *testing.T) {
			response, err := Handle(context.Background(), pageRequest(http.MethodPost, path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusMethodNotAllowed)
			}
			if got := response.Headers["Allow"]; got != allowedMethods {
				t.Fatalf("Allow = %q, want %q", got, allowedMethods)
			}
		})
	}

	dualMethodPaths := []string{"/account/sign-in", "/account/sign-up", "/account/addresses"}
	for _, path := range dualMethodPaths {
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			t.Run(method+" "+path, func(t *testing.T) {
				response, err := Handle(context.Background(), pageRequest(method, path))
				if err != nil {
					t.Fatalf("Handle returned error: %v", err)
				}
				if response.StatusCode != http.StatusMethodNotAllowed {
					t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusMethodNotAllowed)
				}
				if got := response.Headers["Allow"]; got != accountFormAllowedMethods {
					t.Fatalf("Allow = %q, want %q", got, accountFormAllowedMethods)
				}
			})
		}
	}
}

func TestAccountPagesAnonymousRedirectToSignInWithReturnTo(t *testing.T) {
	env := newAccountTestEnv(t)
	tests := []struct {
		method   string
		path     string
		location string
	}{
		{http.MethodGet, "/account", "/account/sign-in?return_to=%2Faccount"},
		{http.MethodGet, "/account/addresses", "/account/sign-in?return_to=%2Faccount%2Faddresses"},
		{http.MethodGet, "/account/payment-methods", "/account/sign-in?return_to=%2Faccount%2Fpayment-methods"},
		{http.MethodGet, "/account/addresses/" + ssrTestAccountID + "/edit", "/account/sign-in?return_to=%2Faccount%2Faddresses%2F" + ssrTestAccountID + "%2Fedit"},
		{http.MethodPost, "/account/password", "/account/sign-in?return_to=%2Faccount"},
		{http.MethodPost, "/account/addresses/" + ssrTestAccountID + "/default", "/account/sign-in?return_to=%2Faccount%2Faddresses"},
		{http.MethodPost, "/account/payment-methods/add", "/account/sign-in?return_to=%2Faccount%2Fpayment-methods"},
	}

	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			response, err := env.handler.Handle(context.Background(), pageRequest(test.method, test.path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusSeeOther {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusSeeOther)
			}
			if got := response.Headers["Location"]; got != test.location {
				t.Fatalf("Location = %q, want %q", got, test.location)
			}
			if got := response.Headers["Cache-Control"]; got != privatePageCacheControl {
				t.Fatalf("Cache-Control = %q, want %q", got, privatePageCacheControl)
			}
		})
	}

	t.Run("anonymous sign-out goes home", func(t *testing.T) {
		response, err := env.handler.Handle(context.Background(), pageRequest(http.MethodPost, "/account/sign-out"))
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if response.StatusCode != http.StatusSeeOther || response.Headers["Location"] != "/" {
			t.Fatalf("response = %d %q, want 303 /", response.StatusCode, response.Headers["Location"])
		}
	})
}

func TestSignUpPageRendersFormWithGuestCSRF(t *testing.T) {
	env := newAccountTestEnv(t)
	response, err := env.handler.Handle(context.Background(), pageRequest(http.MethodGet, "/account/sign-up"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, response.Body, []string{
		`data-testid="signup-form"`,
		`action="/account/sign-up"`,
		`name="guest_csrf_token" value="`,
		`name="email"`,
		`name="password"`,
		`data-password-min-bytes="8"`,
		`maxlength="72"`,
		`Accounts are active immediately`,
	})
	guestCookie := rawSetCookie(t, response, commerce.GuestCSRFCookieName)
	for _, want := range []string{"Path=/", "Max-Age=7200", "HttpOnly", "Secure", "SameSite=Lax"} {
		if !strings.Contains(guestCookie, want) {
			t.Fatalf("guest csrf cookie %q missing %q", guestCookie, want)
		}
	}
	token := hiddenInputValue(t, response.Body, guestCSRFFieldName)
	if cookieValue, _ := httpapi.NamedCookieValue(guestCookie, commerce.GuestCSRFCookieName); cookieValue != token {
		t.Fatalf("guest csrf cookie value %q != hidden field %q (double submit must match)", cookieValue, token)
	}
}

func TestSignInPageRendersPasswordResetLinkAndReturnTo(t *testing.T) {
	env := newAccountTestEnv(t)
	request := pageRequest(http.MethodGet, "/account/sign-in")
	request.QueryStringParameters = map[string]string{"return_to": "/cart"}
	response, err := env.handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, response.Body, []string{
		`data-testid="signin-form"`,
		`action="/account/sign-in"`,
		`maxlength="72"`,
		`href="/account/password-reset"`,
		`Reset it by email`,
		`name="return_to" value="/cart"`,
		`href="/account/sign-up"`,
	})

	maliciousRequest := pageRequest(http.MethodGet, "/account/sign-in")
	maliciousRequest.QueryStringParameters = map[string]string{"return_to": "https://evil.example"}
	maliciousResponse, err := env.handler.Handle(context.Background(), maliciousRequest)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	assertBodyOmits(t, maliciousResponse.Body, []string{"evil.example", `name="return_to"`})
}

func TestSignUpCreatesAccountSessionAndCookies(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	response := signUpTestCustomer(t, env.handler, jar, "Shopper@Example.com", "orchid-market-99")

	if got := response.Headers["Location"]; got != "/account" {
		t.Fatalf("Location = %q, want /account", got)
	}
	if got := response.Headers["Cache-Control"]; got != privatePageCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, privatePageCacheControl)
	}
	if len(response.Cookies) != 5 {
		t.Fatalf("cookies = %#v, want session, csrf, guest clear, guest order clear, cart mirror", response.Cookies)
	}
	sessionCookie := rawSetCookie(t, response, commerce.SessionCookieName)
	for _, want := range []string{"Path=/", "Max-Age=2592000", "HttpOnly", "Secure", "SameSite=Lax"} {
		if !strings.Contains(sessionCookie, want) {
			t.Fatalf("session cookie %q missing %q", sessionCookie, want)
		}
	}
	if csrfCookie := rawSetCookie(t, response, commerce.CSRFCookieName); !strings.Contains(csrfCookie, "Max-Age=2592000") {
		t.Fatalf("csrf cookie = %q, want session-aligned Max-Age", csrfCookie)
	}
	if guestClear := rawSetCookie(t, response, commerce.GuestCSRFCookieName); !strings.Contains(guestClear, "Max-Age=0") {
		t.Fatalf("guest csrf cookie = %q, want clearing", guestClear)
	}
	if guestOrderClear := rawSetCookie(t, response, commerce.GuestOrderCookieName); !strings.Contains(guestOrderClear, "Max-Age=0") {
		t.Fatalf("guest order pointer cookie = %q, want clearing", guestOrderClear)
	}
	if cartMirror := rawSetCookie(t, response, cart.CookieName); !strings.Contains(cartMirror, "Max-Age=0") {
		t.Fatalf("cart mirror = %q, want clearing for an empty cart", cartMirror)
	}

	customer, found, err := env.commerce.GetCustomerByEmail(context.Background(), "shopper@example.com")
	if err != nil || !found {
		t.Fatalf("customer lookup = found %t, err %v", found, err)
	}
	if customer.Email != "Shopper@Example.com" || customer.EmailNormalized != "shopper@example.com" {
		t.Fatalf("customer emails = %q/%q, want display case preserved and normalized lookup", customer.Email, customer.EmailNormalized)
	}
	if bcrypt.CompareHashAndPassword([]byte(customer.PasswordHash), []byte("orchid-market-99")) != nil {
		t.Fatal("stored password hash does not verify the password")
	}

	accountResponse, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/account", jar))
	if err != nil {
		t.Fatalf("Handle /account returned error: %v", err)
	}
	if accountResponse.StatusCode != http.StatusOK {
		t.Fatalf("/account status = %d, want %d", accountResponse.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, accountResponse.Body, []string{"Shopper@Example.com", `action="/account/sign-out"`, `action="/account/password"`, `data-password-min-bytes="8"`, `maxlength="72"`})
}

// TestGuestPointerCookieClearedOnSignIn pins the finishCustomerAuth tail: a
// guest-order pointer is meaningless once signed in, so both sign-in and
// sign-up responses clear it (any stranded guest pending order self-heals via
// the 30-minute expiry webhook).
func TestGuestPointerCookieClearedOnSignIn(t *testing.T) {
	env := newAccountTestEnv(t)
	signUpJar := testCookieJar{}
	signUpTestCustomer(t, env.handler, signUpJar, "shopper@example.com", "orchid-market-99")

	jar := testCookieJar{}
	response := signInTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	if guestOrderClear := rawSetCookie(t, response, commerce.GuestOrderCookieName); !strings.Contains(guestOrderClear, "Max-Age=0") {
		t.Fatalf("guest order pointer cookie = %q, want clearing on sign-in", guestOrderClear)
	}
}

// transientConflictCommerceStore fails CreateCustomer with the retryable
// transient-conflict classification before delegating, simulating the Dynamo
// transaction contention the memory store never produces on its own.
type transientConflictCommerceStore struct {
	commerce.Store
	remainingFailures int
	createCalls       int
}

func (s *transientConflictCommerceStore) CreateCustomer(ctx context.Context, email string, emailNormalized string, passwordHash string) (commerce.Customer, error) {
	s.createCalls++
	if s.remainingFailures > 0 {
		s.remainingFailures--
		return commerce.Customer{}, fmt.Errorf("%w: simulated contention", commerce.ErrTransientConflict)
	}
	return s.Store.CreateCustomer(ctx, email, emailNormalized, passwordHash)
}

func TestSignUpRetriesTransientCreateCustomerConflict(t *testing.T) {
	env := newAccountTestEnv(t)
	store := &transientConflictCommerceStore{Store: env.commerce, remainingFailures: 1}
	env.handler.commerce = store

	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	if store.createCalls != 2 {
		t.Fatalf("CreateCustomer calls = %d, want 2 (one transient conflict, one retry success)", store.createCalls)
	}
	if _, found, err := env.commerce.GetCustomerByEmail(context.Background(), "shopper@example.com"); err != nil || !found {
		t.Fatalf("customer lookup after retried sign-up = found %t, err %v", found, err)
	}
}

func TestSignUpDuplicateEmailShowsSignInPrompt(t *testing.T) {
	env := newAccountTestEnv(t)
	signUpTestCustomer(t, env.handler, testCookieJar{}, "shopper@example.com", "orchid-market-99")

	jar := testCookieJar{}
	token := guestCSRFTokenFor(t, env.handler, jar, "/account/sign-up")
	response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/sign-up", url.Values{
		guestCSRFFieldName: {token},
		"email":            {"shopper@example.com"},
		"password":         {"another-password-1"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}
	assertBodyContains(t, response.Body, []string{
		"An account with this email already exists. Sign in instead.",
		`data-testid="signup-form"`,
	})
}

func TestSignUpRejectsInvalidEmailAndPasswordLengths(t *testing.T) {
	tests := []struct {
		name     string
		email    string
		password string
		message  string
	}{
		{name: "email missing at", email: "shopper.example.com", password: "orchid-market-99", message: invalidEmailError},
		{name: "email two ats", email: "shopper@@example.com", password: "orchid-market-99", message: invalidEmailError},
		{name: "email domain without dot", email: "shopper@localhost", password: "orchid-market-99", message: invalidEmailError},
		{name: "email with spaces", email: "shop per@example.com", password: "orchid-market-99", message: invalidEmailError},
		{name: "email too long", email: strings.Repeat("a", 250) + "@example.com", password: "orchid-market-99", message: invalidEmailError},
		{name: "password seven bytes", email: "shopper@example.com", password: "1234567", message: invalidPasswordError},
		{name: "password 73 bytes is rejected not truncated", email: "shopper@example.com", password: strings.Repeat("p", 73), message: invalidPasswordError},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newAccountTestEnv(t)
			jar := testCookieJar{}
			token := guestCSRFTokenFor(t, env.handler, jar, "/account/sign-up")
			response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/sign-up", url.Values{
				guestCSRFFieldName: {token},
				"email":            {test.email},
				"password":         {test.password},
			}, jar))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusBadRequest)
			}
			assertBodyContains(t, response.Body, []string{test.message})
			if _, found, _ := env.commerce.GetCustomerByEmail(context.Background(), commerce.NormalizeEmail(test.email)); found {
				t.Fatal("invalid sign-up created a customer")
			}
		})
	}
}

func TestSignUpAcceptsPasswordMinimumByBytes(t *testing.T) {
	env := newAccountTestEnv(t)
	password := "密码密" // 3 characters, 9 UTF-8 bytes.
	signUpTestCustomer(t, env.handler, testCookieJar{}, "shopper@example.com", password)

	customer, found, err := env.commerce.GetCustomerByEmail(context.Background(), "shopper@example.com")
	if err != nil || !found {
		t.Fatalf("customer lookup = found %t, err %v", found, err)
	}
	if bcrypt.CompareHashAndPassword([]byte(customer.PasswordHash), []byte(password)) != nil {
		t.Fatal("stored password hash does not verify the byte-length password")
	}
}

func TestSignInHappyPathHonorsValidatedReturnTo(t *testing.T) {
	env := newAccountTestEnv(t)
	signUpTestCustomer(t, env.handler, testCookieJar{}, "shopper@example.com", "orchid-market-99")

	jar := testCookieJar{}
	token := guestCSRFTokenFor(t, env.handler, jar, "/account/sign-in")
	response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/sign-in", url.Values{
		guestCSRFFieldName: {token},
		"email":            {"shopper@example.com"},
		"password":         {"orchid-market-99"},
		"return_to":        {"/cart"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther || response.Headers["Location"] != "/cart" {
		t.Fatalf("response = %d %q, want 303 /cart", response.StatusCode, response.Headers["Location"])
	}
	jar.update(t, response)

	openRedirectJar := testCookieJar{}
	openRedirectToken := guestCSRFTokenFor(t, env.handler, openRedirectJar, "/account/sign-in")
	openRedirectResponse, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/sign-in", url.Values{
		guestCSRFFieldName: {openRedirectToken},
		"email":            {"shopper@example.com"},
		"password":         {"orchid-market-99"},
		"return_to":        {"//evil.example/phish"},
	}, openRedirectJar))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if got := openRedirectResponse.Headers["Location"]; got != "/account" {
		t.Fatalf("open-redirect Location = %q, want /account fallback", got)
	}

	accountResponse, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/account", jar))
	if err != nil {
		t.Fatalf("Handle /account returned error: %v", err)
	}
	if accountResponse.StatusCode != http.StatusOK {
		t.Fatalf("/account status = %d, want %d", accountResponse.StatusCode, http.StatusOK)
	}
}

func TestReturnToRejectsControlCharacterSpliceVectors(t *testing.T) {
	// The WHATWG URL parser strips tab/CR/LF before parsing, so any control
	// character that survives validation can re-create a protocol-relative
	// "//evil.com" inside the Location header.
	for _, value := range []string{
		"/\t/evil.com",
		"/\r/evil.com",
		"/\n/evil.com",
		"/\r\n/evil.com",
		"/\x00/evil.com",
		"/checkout\x1b",
		"/checkout\x7f",
	} {
		if validReturnTo(value) {
			t.Errorf("validReturnTo(%q) = true, want false", value)
		}
	}
	if !validReturnTo("/checkout") {
		t.Error(`validReturnTo("/checkout") = false, want true`)
	}

	env := newAccountTestEnv(t)
	signUpTestCustomer(t, env.handler, testCookieJar{}, "shopper@example.com", "orchid-market-99")

	// The %09-encoded form value reaches the handler decoded to a raw tab.
	jar := testCookieJar{}
	token := guestCSRFTokenFor(t, env.handler, jar, "/account/sign-in")
	response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/sign-in", url.Values{
		guestCSRFFieldName: {token},
		"email":            {"shopper@example.com"},
		"password":         {"orchid-market-99"},
		"return_to":        {"/\t/evil.com"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if got := response.Headers["Location"]; got != "/account" {
		t.Fatalf("tab-spliced return_to Location = %q, want /account fallback", got)
	}
	jar.update(t, response)

	// The address-create "next" field funnels through the same validator.
	addressToken := accountCSRFToken(t, env.handler, jar)
	addressResponse, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/addresses", url.Values{
		customerCSRFFieldName: {addressToken},
		"full_name":           {"Anong Shopper"},
		"line1":               {"123 Sukhumvit Rd"},
		"city":                {"Bangkok"},
		"region":              {"NY"},
		"postal_code":         {"10110"},
		"next":                {"/\r\n/evil.com"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle address create returned error: %v", err)
	}
	if got := addressResponse.Headers["Location"]; got != "/account/addresses" {
		t.Fatalf("CRLF-spliced next Location = %q, want /account/addresses fallback", got)
	}
}

func TestDummyCustomerBcryptHashMatchesProductionCost(t *testing.T) {
	// A cheaper dummy hash would make unknown-email sign-ins measurably
	// faster than wrong-password sign-ins — an email-existence timing oracle.
	cost, err := bcrypt.Cost([]byte(dummyCustomerBcryptHash))
	if err != nil {
		t.Fatalf("bcrypt.Cost returned error: %v", err)
	}
	if cost != customerPasswordBcryptCost {
		t.Fatalf("dummy hash cost = %d, want %d", cost, customerPasswordBcryptCost)
	}
}

func TestSignInInvalidCredentialsAreGeneric(t *testing.T) {
	env := newAccountTestEnv(t)
	signUpTestCustomer(t, env.handler, testCookieJar{}, "shopper@example.com", "orchid-market-99")

	for _, test := range []struct {
		name     string
		email    string
		password string
	}{
		{name: "unknown email burns dummy bcrypt", email: "nobody@example.com", password: "orchid-market-99"},
		{name: "wrong password", email: "shopper@example.com", password: "wrong-password-11"},
	} {
		t.Run(test.name, func(t *testing.T) {
			jar := testCookieJar{}
			token := guestCSRFTokenFor(t, env.handler, jar, "/account/sign-in")
			response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/sign-in", url.Values{
				guestCSRFFieldName: {token},
				"email":            {test.email},
				"password":         {test.password},
			}, jar))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusUnauthorized)
			}
			assertBodyContains(t, response.Body, []string{"Invalid email or password.", `data-testid="signin-form"`})
			for _, setCookie := range response.Cookies {
				if strings.HasPrefix(setCookie, commerce.SessionCookieName+"=") {
					t.Fatalf("failed sign-in set a session cookie: %q", setCookie)
				}
			}
		})
	}
}

func TestSignInIssuesFreshSessionPerLogin(t *testing.T) {
	env := newAccountTestEnv(t)
	firstJar := testCookieJar{}
	firstResponse := signUpTestCustomer(t, env.handler, firstJar, "shopper@example.com", "orchid-market-99")
	firstSession := setCookieValue(t, firstResponse, commerce.SessionCookieName)

	secondJar := testCookieJar{}
	secondResponse := signInTestCustomer(t, env.handler, secondJar, "shopper@example.com", "orchid-market-99")
	secondSession := setCookieValue(t, secondResponse, commerce.SessionCookieName)

	if firstSession == secondSession {
		t.Fatal("second login reused the first session token; sessions must be freshly server-minted")
	}
	for name, jar := range map[string]testCookieJar{"first": firstJar, "second": secondJar} {
		response, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/account", jar))
		if err != nil {
			t.Fatalf("Handle /account (%s) returned error: %v", name, err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("/account (%s) status = %d, want %d", name, response.StatusCode, http.StatusOK)
		}
	}
}

func TestSignInThrottledAfterRepeatedFailures(t *testing.T) {
	env := newAccountTestEnv(t)
	signUpTestCustomer(t, env.handler, testCookieJar{}, "shopper@example.com", "orchid-market-99")

	jar := testCookieJar{}
	token := guestCSRFTokenFor(t, env.handler, jar, "/account/sign-in")
	failedAttempt := func() events.APIGatewayV2HTTPResponse {
		response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/sign-in", url.Values{
			guestCSRFFieldName: {token},
			"email":            {"shopper@example.com"},
			"password":         {"wrong-password-11"},
		}, jar))
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		return response
	}

	for attempt := 1; attempt <= commerce.LoginAttemptLimit; attempt++ {
		if response := failedAttempt(); response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want %d", attempt, response.StatusCode, http.StatusUnauthorized)
		}
	}

	throttled := failedAttempt()
	if throttled.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("throttled status = %d, want %d", throttled.StatusCode, http.StatusTooManyRequests)
	}
	retryAfter, err := strconv.Atoi(throttled.Headers["Retry-After"])
	if err != nil || retryAfter < 1 {
		t.Fatalf("Retry-After = %q, want positive seconds", throttled.Headers["Retry-After"])
	}
	if throttled.Body != throttledBody {
		t.Fatalf("throttled body = %q, want generic %q", throttled.Body, throttledBody)
	}

	// Reserve-before-verify: even the correct password is rejected while
	// locked.
	correct, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/sign-in", url.Values{
		guestCSRFFieldName: {token},
		"email":            {"shopper@example.com"},
		"password":         {"orchid-market-99"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if correct.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("locked correct-password status = %d, want %d", correct.StatusCode, http.StatusTooManyRequests)
	}
}

func TestSignUpThrottledByIP(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	token := guestCSRFTokenFor(t, env.handler, jar, "/account/sign-up")
	invalidAttempt := func() events.APIGatewayV2HTTPResponse {
		response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/sign-up", url.Values{
			guestCSRFFieldName: {token},
			"email":            {"not-an-email"},
			"password":         {"orchid-market-99"},
		}, jar))
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		return response
	}

	for attempt := 1; attempt <= commerce.LoginAttemptLimit; attempt++ {
		if response := invalidAttempt(); response.StatusCode != http.StatusBadRequest {
			t.Fatalf("attempt %d status = %d, want %d", attempt, response.StatusCode, http.StatusBadRequest)
		}
	}
	throttled := invalidAttempt()
	if throttled.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("throttled status = %d, want %d", throttled.StatusCode, http.StatusTooManyRequests)
	}
	if throttled.Headers["Retry-After"] == "" {
		t.Fatal("throttled response missing Retry-After")
	}
}

func TestGuestCSRFMatrixOnSignIn(t *testing.T) {
	env := newAccountTestEnv(t)
	signUpTestCustomer(t, env.handler, testCookieJar{}, "shopper@example.com", "orchid-market-99")

	validJar := testCookieJar{}
	validToken := guestCSRFTokenFor(t, env.handler, validJar, "/account/sign-in")
	otherJar := testCookieJar{}
	otherToken := guestCSRFTokenFor(t, env.handler, otherJar, "/account/sign-in")

	tests := []struct {
		name string
		jar  testCookieJar
		form url.Values
	}{
		{
			name: "missing cookie",
			jar:  testCookieJar{},
			form: url.Values{guestCSRFFieldName: {validToken}, "email": {"shopper@example.com"}, "password": {"orchid-market-99"}},
		},
		{
			name: "missing field",
			jar:  validJar,
			form: url.Values{"email": {"shopper@example.com"}, "password": {"orchid-market-99"}},
		},
		{
			name: "cookie field mismatch",
			jar:  validJar,
			form: url.Values{guestCSRFFieldName: {otherToken}, "email": {"shopper@example.com"}, "password": {"orchid-market-99"}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/sign-in", test.form, test.jar))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusForbidden {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusForbidden)
			}
			for _, setCookie := range response.Cookies {
				if strings.HasPrefix(setCookie, commerce.SessionCookieName+"=") {
					t.Fatalf("CSRF-rejected sign-in set a session cookie: %q", setCookie)
				}
			}
		})
	}
}

func TestCustomerCSRFMatrix(t *testing.T) {
	env := newAccountTestEnv(t)
	jarA := testCookieJar{}
	signUpTestCustomer(t, env.handler, jarA, "first@example.com", "orchid-market-99")
	tokenA := accountCSRFToken(t, env.handler, jarA)
	jarB := testCookieJar{}
	signUpTestCustomer(t, env.handler, jarB, "second@example.com", "orchid-market-99")
	tokenB := accountCSRFToken(t, env.handler, jarB)

	missingCookieJar := cloneJar(jarA)
	delete(missingCookieJar, commerce.CSRFCookieName)
	crossSessionJar := cloneJar(jarA)
	crossSessionJar[commerce.CSRFCookieName] = jarB[commerce.CSRFCookieName]

	tests := []struct {
		name string
		jar  testCookieJar
		form url.Values
	}{
		{name: "missing csrf cookie", jar: missingCookieJar, form: url.Values{customerCSRFFieldName: {tokenA}}},
		{name: "missing csrf field", jar: jarA, form: url.Values{}},
		{name: "cookie field mismatch", jar: jarA, form: url.Values{customerCSRFFieldName: {"not-the-cookie-value"}}},
		{name: "cross-session token", jar: crossSessionJar, form: url.Values{customerCSRFFieldName: {tokenB}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/sign-out", test.form, test.jar))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusForbidden {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusForbidden)
			}
		})
	}

	// The session survived every rejected POST.
	stillSignedIn, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/account", jarA))
	if err != nil {
		t.Fatalf("Handle /account returned error: %v", err)
	}
	if stillSignedIn.StatusCode != http.StatusOK {
		t.Fatalf("/account status = %d, want %d after rejected CSRF posts", stillSignedIn.StatusCode, http.StatusOK)
	}

	// CSRF is validated before the password form is even parsed: a valid
	// change request without a token is rejected and changes nothing.
	noTokenChange, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/password", url.Values{
		"current_password": {"orchid-market-99"},
		"new_password":     {"replacement-pw-55"},
	}, jarA))
	if err != nil {
		t.Fatalf("Handle password change returned error: %v", err)
	}
	if noTokenChange.StatusCode != http.StatusForbidden {
		t.Fatalf("password change without CSRF = %d, want %d", noTokenChange.StatusCode, http.StatusForbidden)
	}
	signInTestCustomer(t, env.handler, testCookieJar{}, "first@example.com", "orchid-market-99")
}

func TestSignOutDeletesSessionAndClearsCookies(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")

	cartResponse, err := env.handler.Handle(context.Background(), jarFormPostRequest("/cart/items", url.Values{
		"slug":     {"mango-sticky-rice-kit"},
		"quantity": {"2"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle cart add returned error: %v", err)
	}
	jar.update(t, cartResponse)

	sessionCookieBeforeSignOut := jar[commerce.SessionCookieName]
	token := accountCSRFToken(t, env.handler, jar)
	response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/sign-out", url.Values{
		customerCSRFFieldName: {token},
	}, jar))
	if err != nil {
		t.Fatalf("Handle sign-out returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther || response.Headers["Location"] != "/" {
		t.Fatalf("sign-out response = %d %q, want 303 /", response.StatusCode, response.Headers["Location"])
	}
	for _, name := range []string{commerce.SessionCookieName, commerce.CSRFCookieName, cart.CookieName} {
		if raw := rawSetCookie(t, response, name); !strings.Contains(raw, "Max-Age=0") {
			t.Fatalf("sign-out cookie %q = %q, want clearing", name, raw)
		}
	}
	jar.update(t, response)

	redirected, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/account", jar))
	if err != nil {
		t.Fatalf("Handle /account returned error: %v", err)
	}
	if redirected.StatusCode != http.StatusSeeOther {
		t.Fatalf("/account after sign-out status = %d, want %d", redirected.StatusCode, http.StatusSeeOther)
	}

	// The session row is gone server-side; replaying the captured cookie
	// stays anonymous.
	replayJar := testCookieJar{commerce.SessionCookieName: sessionCookieBeforeSignOut}
	replayed, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/account", replayJar))
	if err != nil {
		t.Fatalf("Handle replay returned error: %v", err)
	}
	if replayed.StatusCode != http.StatusSeeOther {
		t.Fatalf("replayed session status = %d, want %d", replayed.StatusCode, http.StatusSeeOther)
	}
}

func TestSignOutWithDeadSessionStillClearsAllCustomerCookies(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	customerID := accountCustomerID(t, env, "shopper@example.com")

	cartResponse, err := env.handler.Handle(context.Background(), jarFormPostRequest("/cart/items", url.Values{
		"slug":     {"mango-sticky-rice-kit"},
		"quantity": {"2"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle cart add returned error: %v", err)
	}
	jar.update(t, cartResponse)

	// The session dies server-side (password change on another device).
	if err := env.commerce.DeleteAllSessions(context.Background(), customerID); err != nil {
		t.Fatalf("DeleteAllSessions returned error: %v", err)
	}

	// CSRF can never validate against a dead session, so the sign-out click
	// degrades to an idempotent cookie-clearing no-op — including the cart
	// mirror, which must not leak (or later merge) on a shared machine.
	response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/sign-out", url.Values{}, jar))
	if err != nil {
		t.Fatalf("Handle sign-out returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther || response.Headers["Location"] != "/" {
		t.Fatalf("dead-session sign-out = %d %q, want 303 /", response.StatusCode, response.Headers["Location"])
	}
	for _, name := range []string{commerce.SessionCookieName, commerce.CSRFCookieName, cart.CookieName} {
		if raw := rawSetCookie(t, response, name); !strings.Contains(raw, "Max-Age=0") {
			t.Fatalf("dead-session sign-out cookie %q = %q, want clearing", name, raw)
		}
	}

	// A sign-out with no cookies at all still scrubs everything.
	bareResponse, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/sign-out", url.Values{}, testCookieJar{}))
	if err != nil {
		t.Fatalf("Handle bare sign-out returned error: %v", err)
	}
	if bareResponse.StatusCode != http.StatusSeeOther || bareResponse.Headers["Location"] != "/" {
		t.Fatalf("bare sign-out = %d %q, want 303 /", bareResponse.StatusCode, bareResponse.Headers["Location"])
	}
	for _, name := range []string{commerce.SessionCookieName, commerce.CSRFCookieName, cart.CookieName} {
		if raw := rawSetCookie(t, bareResponse, name); !strings.Contains(raw, "Max-Age=0") {
			t.Fatalf("bare sign-out cookie %q = %q, want clearing", name, raw)
		}
	}
}

func TestPasswordChangeRevokesOtherSessions(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	otherDeviceJar := testCookieJar{}
	signInTestCustomer(t, env.handler, otherDeviceJar, "shopper@example.com", "orchid-market-99")
	customerID := accountCustomerID(t, env, "shopper@example.com")
	env.email.Clear()
	submitPasswordResetRequest(t, env.handler, testCookieJar{}, "shopper@example.com")
	resetPayload, ok := env.handler.decodePasswordResetToken(resetTokenFromLastMessage(t, env.email))
	if !ok {
		t.Fatal("password reset token did not decode")
	}
	resetHash := hashPasswordResetToken(resetPayload.Token)
	if _, found, err := env.commerce.ValidatePasswordResetToken(context.Background(), customerID, resetHash, env.handler.currentTime()); err != nil || !found {
		t.Fatalf("reset token before password change found=%t err=%v, want valid", found, err)
	}

	token := accountCSRFToken(t, env.handler, jar)
	response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/password", url.Values{
		customerCSRFFieldName: {token},
		"current_password":    {"orchid-market-99"},
		"new_password":        {"replacement-pw-55"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle password change returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther || response.Headers["Location"] != "/account?password_changed=1" {
		t.Fatalf("password change = %d %q, want 303 /account?password_changed=1", response.StatusCode, response.Headers["Location"])
	}
	jar.update(t, response)

	bannerRequest := jarPageRequest(http.MethodGet, "/account", jar)
	bannerRequest.QueryStringParameters = map[string]string{"password_changed": "1"}
	bannerResponse, err := env.handler.Handle(context.Background(), bannerRequest)
	if err != nil {
		t.Fatalf("Handle /account returned error: %v", err)
	}
	if bannerResponse.StatusCode != http.StatusOK {
		t.Fatalf("/account status = %d, want %d", bannerResponse.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, bannerResponse.Body, []string{"Password changed. Other devices have been signed out."})

	otherDevice, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/account", otherDeviceJar))
	if err != nil {
		t.Fatalf("Handle other device returned error: %v", err)
	}
	if otherDevice.StatusCode != http.StatusSeeOther {
		t.Fatalf("other device status = %d, want %d (sessions revoked)", otherDevice.StatusCode, http.StatusSeeOther)
	}
	if _, found, err := env.commerce.ValidatePasswordResetToken(context.Background(), customerID, resetHash, env.handler.currentTime()); err != nil || found {
		t.Fatalf("reset token after password change found=%t err=%v, want deleted", found, err)
	}

	oldPasswordJar := testCookieJar{}
	oldToken := guestCSRFTokenFor(t, env.handler, oldPasswordJar, "/account/sign-in")
	oldPassword, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/sign-in", url.Values{
		guestCSRFFieldName: {oldToken},
		"email":            {"shopper@example.com"},
		"password":         {"orchid-market-99"},
	}, oldPasswordJar))
	if err != nil {
		t.Fatalf("Handle old-password sign-in returned error: %v", err)
	}
	if oldPassword.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old password sign-in = %d, want %d", oldPassword.StatusCode, http.StatusUnauthorized)
	}
	signInTestCustomer(t, env.handler, testCookieJar{}, "shopper@example.com", "replacement-pw-55")
}

// failingDeleteAllSessionsStore injects a transient outage into session
// revocation.
type failingDeleteAllSessionsStore struct {
	commerce.Store
}

func (failingDeleteAllSessionsStore) DeleteAllSessions(ctx context.Context, customerID string) error {
	return errors.New("transient revocation outage")
}

func TestPasswordChangeFailsClosedWhenSessionRevocationFails(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	token := accountCSRFToken(t, env.handler, jar)

	env.handler.commerce = failingDeleteAllSessionsStore{Store: env.commerce}
	response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/password", url.Values{
		customerCSRFFieldName: {token},
		"current_password":    {"orchid-market-99"},
		"new_password":        {"replacement-pw-55"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle password change returned error: %v", err)
	}
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("password change with failed revocation = %d, want %d", response.StatusCode, http.StatusInternalServerError)
	}
	env.handler.commerce = env.commerce

	// Fail closed: the password must be unchanged, so a retry with the
	// current password can re-trigger revocation.
	signInTestCustomer(t, env.handler, testCookieJar{}, "shopper@example.com", "orchid-market-99")
	newPasswordJar := testCookieJar{}
	newPasswordToken := guestCSRFTokenFor(t, env.handler, newPasswordJar, "/account/sign-in")
	newPassword, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/sign-in", url.Values{
		guestCSRFFieldName: {newPasswordToken},
		"email":            {"shopper@example.com"},
		"password":         {"replacement-pw-55"},
	}, newPasswordJar))
	if err != nil {
		t.Fatalf("Handle new-password sign-in returned error: %v", err)
	}
	if newPassword.StatusCode != http.StatusUnauthorized {
		t.Fatalf("new password sign-in = %d, want %d (password must not have committed)", newPassword.StatusCode, http.StatusUnauthorized)
	}
}

func TestPasswordChangeValidatesCurrentAndNewPassword(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	token := accountCSRFToken(t, env.handler, jar)

	wrongCurrent, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/password", url.Values{
		customerCSRFFieldName: {token},
		"current_password":    {"not-my-password"},
		"new_password":        {"replacement-pw-55"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if wrongCurrent.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong current status = %d, want %d", wrongCurrent.StatusCode, http.StatusBadRequest)
	}
	assertBodyContains(t, wrongCurrent.Body, []string{"Current password is incorrect."})

	shortNew, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/password", url.Values{
		customerCSRFFieldName: {token},
		"current_password":    {"orchid-market-99"},
		"new_password":        {"short"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if shortNew.StatusCode != http.StatusBadRequest {
		t.Fatalf("short new status = %d, want %d", shortNew.StatusCode, http.StatusBadRequest)
	}
	assertBodyContains(t, shortNew.Body, []string{invalidPasswordError})

	// Both rejections left the password untouched.
	signInTestCustomer(t, env.handler, testCookieJar{}, "shopper@example.com", "orchid-market-99")
}

func TestMergeOnLoginUsesMaxQuantityAndRewritesMirror(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	customerID := accountCustomerID(t, env, "shopper@example.com")

	addResponse, err := env.handler.Handle(context.Background(), jarFormPostRequest("/cart/items", url.Values{
		"slug":     {"mango-sticky-rice-kit"},
		"quantity": {"2"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle cart add returned error: %v", err)
	}
	jar.update(t, addResponse)
	if lines := serverCartLines(t, env, customerID); len(lines) != 1 || lines[0].Quantity != 2 {
		t.Fatalf("server cart = %#v, want mango quantity 2", lines)
	}

	// Anonymous browsing on another device: mango 3 (more than the server's
	// 2) plus a new product.
	anonymousJar := testCookieJar{
		cart.CookieName: encodedTestCart(t, []cart.Line{
			{Slug: "mango-sticky-rice-kit", Quantity: 3},
			{Slug: "thai-tea-sampler", Quantity: 1},
		}),
	}
	token := guestCSRFTokenFor(t, env.handler, anonymousJar, "/account/sign-in")
	response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/sign-in", url.Values{
		guestCSRFFieldName: {token},
		"email":            {"shopper@example.com"},
		"password":         {"orchid-market-99"},
	}, anonymousJar))
	if err != nil {
		t.Fatalf("Handle sign-in returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign-in status = %d, want %d", response.StatusCode, http.StatusSeeOther)
	}

	mirror := setCookieValue(t, response, cart.CookieName)
	decoded := cart.DecodeCookie(mirror, ssrTestCartSecret)
	if decoded.NeedsClear {
		t.Fatalf("mirror cookie failed to decode: %q", mirror)
	}
	mirrorLines := decoded.Cart.Lines()
	if len(mirrorLines) != 2 ||
		mirrorLines[0].Slug != "mango-sticky-rice-kit" || mirrorLines[0].Quantity != 3 ||
		mirrorLines[1].Slug != "thai-tea-sampler" || mirrorLines[1].Quantity != 1 {
		t.Fatalf("mirror lines = %#v, want server-first mango max(2,3)=3 then tea 1", mirrorLines)
	}
	serverLines := serverCartLines(t, env, customerID)
	if len(serverLines) != 2 || serverLines[0].Quantity != 3 || serverLines[1].Quantity != 1 {
		t.Fatalf("server lines = %#v, want merged max quantities", serverLines)
	}

	anonymousJar.update(t, response)
	headerResponse, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/products", anonymousJar))
	if err != nil {
		t.Fatalf("Handle /products returned error: %v", err)
	}
	assertBodyContains(t, headerResponse.Body, []string{`href="/cart">Cart</a>`})

	// Replaying the merged cookie through another login is idempotent: max
	// semantics keep the quantities stable.
	replayJar := testCookieJar{cart.CookieName: mirror}
	replayToken := guestCSRFTokenFor(t, env.handler, replayJar, "/account/sign-in")
	replayResponse, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/sign-in", url.Values{
		guestCSRFFieldName: {replayToken},
		"email":            {"shopper@example.com"},
		"password":         {"orchid-market-99"},
	}, replayJar))
	if err != nil {
		t.Fatalf("Handle replay sign-in returned error: %v", err)
	}
	if replayResponse.StatusCode != http.StatusSeeOther {
		t.Fatalf("replay sign-in status = %d, want %d", replayResponse.StatusCode, http.StatusSeeOther)
	}
	if lines := serverCartLines(t, env, customerID); len(lines) != 2 || lines[0].Quantity != 3 || lines[1].Quantity != 1 {
		t.Fatalf("server lines after replay = %#v, want unchanged max quantities", lines)
	}
}

func TestStaleMirrorCookieIsNotMergedOnNextSignIn(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	customerID := accountCustomerID(t, env, "shopper@example.com")

	addResponse, err := env.handler.Handle(context.Background(), jarFormPostRequest("/cart/items", url.Values{
		"slug":     {"mango-sticky-rice-kit"},
		"quantity": {"2"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle cart add returned error: %v", err)
	}
	mirrorValue := setCookieValue(t, addResponse, cart.CookieName)
	if !cart.DecodeCookie(mirrorValue, ssrTestCartSecret).Mirror {
		t.Fatalf("signed-in mutation cookie is not mirror-flagged: %q", mirrorValue)
	}

	// The customer pays on another device (server cart cleared) and a
	// password change revokes every session without touching this device's
	// cookies.
	record, _, err := env.commerce.GetCart(context.Background(), customerID)
	if err != nil {
		t.Fatalf("GetCart returned error: %v", err)
	}
	record.CustomerID = customerID
	record.Lines = nil
	if _, err := env.commerce.PutCart(context.Background(), record); err != nil {
		t.Fatalf("PutCart returned error: %v", err)
	}
	if err := env.commerce.DeleteAllSessions(context.Background(), customerID); err != nil {
		t.Fatalf("DeleteAllSessions returned error: %v", err)
	}

	// Signing back in with only the stale mirror must not resurrect the
	// paid-and-cleared lines: a mirror is a dead echo of server-cart state,
	// not anonymous shopping.
	staleJar := testCookieJar{cart.CookieName: mirrorValue}
	signInResponse := signInTestCustomer(t, env.handler, staleJar, "shopper@example.com", "orchid-market-99")
	if lines := serverCartLines(t, env, customerID); len(lines) != 0 {
		t.Fatalf("server cart after stale-mirror sign-in = %#v, want empty", lines)
	}
	if raw := rawSetCookie(t, signInResponse, cart.CookieName); !strings.Contains(raw, "Max-Age=0") {
		t.Fatalf("stale-mirror sign-in cart cookie = %q, want clearing mirror of the empty cart", raw)
	}

	// A genuine anonymous cookie (no mirror flag — also the pre-flag legacy
	// shape) still merges.
	anonymousJar := testCookieJar{cart.CookieName: encodedTestCart(t, []cart.Line{{Slug: "thai-tea-sampler", Quantity: 1}})}
	signInTestCustomer(t, env.handler, anonymousJar, "shopper@example.com", "orchid-market-99")
	lines := serverCartLines(t, env, customerID)
	if len(lines) != 1 || lines[0].Slug != "thai-tea-sampler" || lines[0].Quantity != 1 {
		t.Fatalf("server cart after anonymous-cookie sign-in = %#v, want tea 1", lines)
	}
}

func TestSignedInCartMutationsWriteThroughServerCart(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	customerID := accountCustomerID(t, env, "shopper@example.com")

	addResponse, err := env.handler.Handle(context.Background(), jarFormPostRequest("/cart/items", url.Values{
		"slug":     {"mango-sticky-rice-kit"},
		"quantity": {"2"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle cart add returned error: %v", err)
	}
	if addResponse.StatusCode != http.StatusSeeOther || addResponse.Headers["Location"] != "/cart" {
		t.Fatalf("cart add = %d %q, want 303 /cart", addResponse.StatusCode, addResponse.Headers["Location"])
	}
	mirror := cart.DecodeCookie(setCookieValue(t, addResponse, cart.CookieName), ssrTestCartSecret)
	if got := mirror.Cart.Lines(); len(got) != 1 || got[0].Quantity != 2 {
		t.Fatalf("mirror after add = %#v, want mango 2", got)
	}
	if lines := serverCartLines(t, env, customerID); len(lines) != 1 || lines[0].Quantity != 2 {
		t.Fatalf("server cart after add = %#v, want mango 2", lines)
	}
	jar.update(t, addResponse)

	// Another device with no cart cookie still sees the server cart, and the
	// render re-emits the mirror (self-healing).
	bareJar := cloneJar(jar)
	delete(bareJar, cart.CookieName)
	cartPageResponse, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/cart", bareJar))
	if err != nil {
		t.Fatalf("Handle /cart returned error: %v", err)
	}
	if cartPageResponse.StatusCode != http.StatusOK {
		t.Fatalf("/cart status = %d, want %d", cartPageResponse.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, cartPageResponse.Body, []string{"Mango Sticky Rice Treats", "Cart (2)"})
	rehealed := cart.DecodeCookie(setCookieValue(t, cartPageResponse, cart.CookieName), ssrTestCartSecret)
	if got := rehealed.Cart.Lines(); len(got) != 1 || got[0].Quantity != 2 {
		t.Fatalf("re-emitted mirror = %#v, want mango 2", got)
	}

	quantityResponse, err := env.handler.Handle(context.Background(), jarFormPostRequest("/cart/items/mango-sticky-rice-kit/quantity", url.Values{
		"quantity": {"4"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle quantity returned error: %v", err)
	}
	jar.update(t, quantityResponse)
	if lines := serverCartLines(t, env, customerID); len(lines) != 1 || lines[0].Quantity != 4 {
		t.Fatalf("server cart after quantity = %#v, want mango 4", lines)
	}

	// The signed-in checkout page still renders from the server cart.
	checkoutResponse, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/checkout", jar))
	if err != nil {
		t.Fatalf("Handle /checkout returned error: %v", err)
	}
	if checkoutResponse.StatusCode != http.StatusOK {
		t.Fatalf("/checkout status = %d, want %d", checkoutResponse.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, checkoutResponse.Body, []string{"Mango Sticky Rice Treats"})

	clearResponse, err := env.handler.Handle(context.Background(), jarFormPostRequest("/cart/clear", url.Values{}, jar))
	if err != nil {
		t.Fatalf("Handle clear returned error: %v", err)
	}
	if raw := rawSetCookie(t, clearResponse, cart.CookieName); !strings.Contains(raw, "Max-Age=0") {
		t.Fatalf("clear mirror = %q, want clearing cookie", raw)
	}
	if lines := serverCartLines(t, env, customerID); len(lines) != 0 {
		t.Fatalf("server cart after clear = %#v, want empty", lines)
	}
}

// oversizedCartCatalogProducts returns enough realistic-slug products that a
// server cart spanning them encodes past cart.MaxEncodedCookieLength while
// staying under cart.MaxLineItems.
func oversizedCartCatalogProducts(count int) []catalog.Product {
	products := make([]catalog.Product, count)
	for i := range products {
		slug := fmt.Sprintf("aromatic-jasmine-handmade-%02d", i)
		products[i] = catalog.Product{
			ID:            "prod_oversized_" + strconv.Itoa(i),
			Slug:          slug,
			Name:          "Oversized Cart Product " + strconv.Itoa(i),
			Description:   "Fixture product for mirror-overflow coverage.",
			PriceCents:    1099,
			Status:        catalog.StatusActive,
			StockQuantity: 9,
		}
	}
	return products
}

func TestOversizedServerCartDegradesMirrorAndNeverFailsRequests(t *testing.T) {
	products := oversizedCartCatalogProducts(49)
	env := newAccountTestEnvWithProducts(t, products)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	customerID := accountCustomerID(t, env, "shopper@example.com")

	// Persist a server cart whose mirror cannot encode within the cookie
	// limit (the server cart, unlike the cookie, has no size bound).
	lines := make([]cart.Line, 48)
	for i := range lines {
		lines[i] = cart.Line{Slug: products[i].Slug, Quantity: 1}
	}
	oversized, err := cart.New(lines)
	if err != nil {
		t.Fatalf("cart.New returned error: %v", err)
	}
	if _, err := cart.EncodeCookie(oversized, ssrTestCartSecret); !errors.Is(err, cart.ErrCookieTooLarge) {
		t.Fatalf("EncodeCookie fixture error = %v, want ErrCookieTooLarge (fixture too small)", err)
	}
	record, _, err := env.commerce.GetCart(context.Background(), customerID)
	if err != nil {
		t.Fatalf("GetCart returned error: %v", err)
	}
	record.CustomerID = customerID
	record.Lines = lines
	if _, err := env.commerce.PutCart(context.Background(), record); err != nil {
		t.Fatalf("PutCart returned error: %v", err)
	}

	// Renders degrade to a truncated mirror instead of 500ing.
	cartPageResponse, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/cart", jar))
	if err != nil {
		t.Fatalf("Handle /cart returned error: %v", err)
	}
	if cartPageResponse.StatusCode != http.StatusOK {
		t.Fatalf("/cart status = %d body %q, want %d", cartPageResponse.StatusCode, cartPageResponse.Body, http.StatusOK)
	}
	mirror := cart.DecodeCookie(setCookieValue(t, cartPageResponse, cart.CookieName), ssrTestCartSecret)
	if mirror.NeedsClear || !mirror.Mirror {
		t.Fatalf("oversized-cart mirror NeedsClear = %t Mirror = %t, want decodable mirror", mirror.NeedsClear, mirror.Mirror)
	}
	if got := mirror.Cart.LineCount(); got == 0 || got >= len(lines) {
		t.Fatalf("truncated mirror line count = %d, want a non-empty strict prefix of %d", got, len(lines))
	}
	jar.update(t, cartPageResponse)

	// Mutations — including the one that crosses the limit — still succeed.
	mutationResponse, err := env.handler.Handle(context.Background(), jarFormPostRequest("/cart/items", url.Values{
		"slug":     {products[48].Slug},
		"quantity": {"1"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle cart add returned error: %v", err)
	}
	if mutationResponse.StatusCode != http.StatusSeeOther || mutationResponse.Headers["Location"] != "/cart" {
		t.Fatalf("oversized cart add = %d %q, want 303 /cart", mutationResponse.StatusCode, mutationResponse.Headers["Location"])
	}
	if got := len(serverCartLines(t, env, customerID)); got != 49 {
		t.Fatalf("server cart line count after add = %d, want 49", got)
	}

	// Sign-in (the merge path) from any device keeps working too.
	freshJar := testCookieJar{}
	signInTestCustomer(t, env.handler, freshJar, "shopper@example.com", "orchid-market-99")
	if got := len(serverCartLines(t, env, customerID)); got != 49 {
		t.Fatalf("server cart line count after sign-in = %d, want 49", got)
	}
}

func TestCartBearingRoutesClearDeadSessionCookies(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	customerID := accountCustomerID(t, env, "shopper@example.com")

	if err := env.commerce.DeleteAllSessions(context.Background(), customerID); err != nil {
		t.Fatalf("DeleteAllSessions returned error: %v", err)
	}

	response, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/cart", jar))
	if err != nil {
		t.Fatalf("Handle /cart returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("/cart status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	for _, name := range []string{commerce.SessionCookieName, commerce.CSRFCookieName} {
		if raw := rawSetCookie(t, response, name); !strings.Contains(raw, "Max-Age=0") {
			t.Fatalf("/cart dead-session cookie %q = %q, want clearing", name, raw)
		}
	}
}

// conflictingPutCartStore forces version conflicts on the first `conflicts`
// PutCart calls, then delegates — the multi-writer race the memory store
// cannot produce on its own.
type conflictingPutCartStore struct {
	commerce.Store
	conflicts int
	calls     int
}

func (s *conflictingPutCartStore) PutCart(ctx context.Context, c commerce.CartRecord) (commerce.CartRecord, error) {
	s.calls++
	if s.calls <= s.conflicts {
		return commerce.CartRecord{}, commerce.ErrVersionConflict
	}
	return s.Store.PutCart(ctx, c)
}

func TestSignedInCartMutationRetriesVersionRacesAndPRGsOnContention(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	customerID := accountCustomerID(t, env, "shopper@example.com")
	addResponse, err := env.handler.Handle(context.Background(), jarFormPostRequest("/cart/items", url.Values{
		"slug":     {"mango-sticky-rice-kit"},
		"quantity": {"2"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle cart add returned error: %v", err)
	}
	jar.update(t, addResponse)

	// A triple race (three conflicting writers) still lands the mutation.
	env.handler.commerce = &conflictingPutCartStore{Store: env.commerce, conflicts: 3}
	tripleRace, err := env.handler.Handle(context.Background(), jarFormPostRequest("/cart/items/mango-sticky-rice-kit/quantity", url.Values{
		"quantity": {"4"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle triple-race quantity returned error: %v", err)
	}
	if tripleRace.StatusCode != http.StatusSeeOther || tripleRace.Headers["Location"] != "/cart" {
		t.Fatalf("triple-race mutation = %d %q, want 303 /cart", tripleRace.StatusCode, tripleRace.Headers["Location"])
	}
	if lines := serverCartLines(t, env, customerID); len(lines) != 1 || lines[0].Quantity != 4 {
		t.Fatalf("server cart after triple race = %#v, want mango 4", lines)
	}

	// Exhausted retries PRG back to /cart (self-heal) instead of 500ing.
	env.handler.commerce = &conflictingPutCartStore{Store: env.commerce, conflicts: 1 << 10}
	contended, err := env.handler.Handle(context.Background(), jarFormPostRequest("/cart/items/mango-sticky-rice-kit/quantity", url.Values{
		"quantity": {"7"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle contended quantity returned error: %v", err)
	}
	if contended.StatusCode != http.StatusSeeOther || contended.Headers["Location"] != "/cart" {
		t.Fatalf("contended mutation = %d %q, want 303 /cart", contended.StatusCode, contended.Headers["Location"])
	}
	env.handler.commerce = env.commerce
	if lines := serverCartLines(t, env, customerID); len(lines) != 1 || lines[0].Quantity != 4 {
		t.Fatalf("server cart after contention = %#v, want unchanged mango 4", lines)
	}
}

// failingGetSessionStore injects a transient outage into session resolution
// while the request still presents a session cookie.
type failingGetSessionStore struct {
	commerce.Store
}

func (failingGetSessionStore) GetSession(ctx context.Context, customerID string, tokenHash string) (commerce.Session, bool, error) {
	return commerce.Session{}, false, errors.New("transient session outage")
}

func TestTransientSessionStoreErrorFailsCartMutationsButNotRenders(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	customerID := accountCustomerID(t, env, "shopper@example.com")
	addResponse, err := env.handler.Handle(context.Background(), jarFormPostRequest("/cart/items", url.Values{
		"slug":     {"mango-sticky-rice-kit"},
		"quantity": {"2"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle cart add returned error: %v", err)
	}
	jar.update(t, addResponse)

	env.handler.commerce = failingGetSessionStore{Store: env.commerce}

	// A mutation must not silently land in the cookie cart, where the next
	// signed-in render would discard it.
	mutation, err := env.handler.Handle(context.Background(), jarFormPostRequest("/cart/items", url.Values{
		"slug":     {"thai-tea-sampler"},
		"quantity": {"1"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle mutation returned error: %v", err)
	}
	if mutation.StatusCode != http.StatusInternalServerError {
		t.Fatalf("mutation during session outage = %d, want %d", mutation.StatusCode, http.StatusInternalServerError)
	}

	// Renders keep the graceful anonymous fallback, without clearing the
	// still-valid session cookie over a blip.
	render, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/cart", jar))
	if err != nil {
		t.Fatalf("Handle /cart returned error: %v", err)
	}
	if render.StatusCode != http.StatusOK {
		t.Fatalf("/cart during session outage = %d, want %d", render.StatusCode, http.StatusOK)
	}
	for _, setCookie := range render.Cookies {
		if strings.HasPrefix(setCookie, commerce.SessionCookieName+"=") {
			t.Fatalf("render during outage touched the session cookie: %q", setCookie)
		}
	}

	env.handler.commerce = env.commerce
	if lines := serverCartLines(t, env, customerID); len(lines) != 1 || lines[0].Slug != "mango-sticky-rice-kit" || lines[0].Quantity != 2 {
		t.Fatalf("server cart after outage = %#v, want untouched mango 2", lines)
	}
}

func TestHeadRequestsOmitBodyOnAccountAndCheckoutNotFoundBranches(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")

	for _, path := range []string{
		"/orders/" + ssrTestAccountID,
		"/account/addresses/" + ssrTestAccountID + "/edit",
		"/checkout/confirm",
		"/checkout/fake-pay",
	} {
		t.Run(path, func(t *testing.T) {
			get, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, path, jar))
			if err != nil {
				t.Fatalf("Handle GET returned error: %v", err)
			}
			if get.StatusCode != http.StatusNotFound || get.Body == "" {
				t.Fatalf("GET = %d body length %d, want 404 with a body", get.StatusCode, len(get.Body))
			}

			head, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodHead, path, jar))
			if err != nil {
				t.Fatalf("Handle HEAD returned error: %v", err)
			}
			if head.StatusCode != http.StatusNotFound {
				t.Fatalf("HEAD status = %d, want %d", head.StatusCode, http.StatusNotFound)
			}
			if head.Body != "" {
				t.Fatalf("HEAD body = %q, want empty", head.Body)
			}
		})
	}
}

func TestAccountPagesSetPrivateNoStoreAndNoindex(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	token := accountCSRFToken(t, env.handler, jar)
	createResponse, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/addresses", url.Values{
		customerCSRFFieldName: {token},
		"full_name":           {"Anong Shopper"},
		"line1":               {"123 Sukhumvit Rd"},
		"city":                {"Bangkok"},
		"region":              {"NY"},
		"postal_code":         {"10110"},
	}, jar))
	if err != nil || createResponse.StatusCode != http.StatusSeeOther {
		t.Fatalf("address create = %d, err %v, want 303", createResponse.StatusCode, err)
	}
	listResponse, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/account/addresses", jar))
	if err != nil {
		t.Fatalf("Handle addresses returned error: %v", err)
	}
	addressID := firstAddressIDFromBody(t, listResponse.Body)

	anonymousPaths := []string{"/account/sign-in", "/account/sign-up"}
	signedInPaths := []string{"/account", "/account/addresses", "/account/addresses/" + addressID + "/edit", "/account/payment-methods"}

	for _, path := range append(anonymousPaths, signedInPaths...) {
		t.Run(path, func(t *testing.T) {
			requestJar := testCookieJar{}
			if !strings.Contains(path, "sign-") {
				requestJar = jar
			}
			response, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, path, requestJar))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
			}
			if got := response.Headers["Cache-Control"]; got != privatePageCacheControl {
				t.Fatalf("Cache-Control = %q, want %q", got, privatePageCacheControl)
			}
			if got := response.Headers["X-Robots-Tag"]; got != "noindex, follow" {
				t.Fatalf("X-Robots-Tag = %q, want noindex, follow", got)
			}
			assertBodyContains(t, response.Body, []string{`<meta name="robots" content="noindex, follow">`})

			headResponse, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodHead, path, requestJar))
			if err != nil {
				t.Fatalf("HEAD Handle returned error: %v", err)
			}
			if headResponse.StatusCode != http.StatusOK || headResponse.Body != "" {
				t.Fatalf("HEAD = status %d body %q, want 200 empty", headResponse.StatusCode, headResponse.Body)
			}
		})
	}
}

func firstAddressIDFromBody(t *testing.T, body string) string {
	t.Helper()
	marker := `href="/account/addresses/`
	start := strings.Index(body, marker)
	if start < 0 {
		t.Fatalf("no address edit link found in body: %q", body)
	}
	rest := body[start+len(marker):]
	end := strings.Index(rest, "/edit")
	if end < 0 {
		t.Fatalf("address edit link is malformed in body: %q", body)
	}
	id := rest[:end]
	if !accountIDPattern.MatchString(id) {
		t.Fatalf("address id %q does not match the id pattern", id)
	}
	return id
}

func TestAddressLifecycle(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")

	emptyList, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/account/addresses", jar))
	if err != nil {
		t.Fatalf("Handle addresses returned error: %v", err)
	}
	jar.update(t, emptyList)
	assertBodyContains(t, emptyList.Body, []string{"No addresses yet.", `data-testid="address-form"`})
	token := hiddenInputValue(t, emptyList.Body, customerCSRFFieldName)

	invalidCreate, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/addresses", url.Values{
		customerCSRFFieldName: {token},
		"full_name":           {"Anong Shopper"},
		"line1":               {"123 Sukhumvit Rd"},
		"region":              {"NY"},
		"postal_code":         {"10110"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle invalid create returned error: %v", err)
	}
	if invalidCreate.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid create status = %d, want %d", invalidCreate.StatusCode, http.StatusBadRequest)
	}
	assertBodyContains(t, invalidCreate.Body, []string{invalidAddressError, `value="Anong Shopper"`, `aria-describedby="address-city-error"`, `Enter a city.`})

	invalidRegion, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/addresses", url.Values{
		customerCSRFFieldName: {token},
		"full_name":           {"Anong Shopper"},
		"line1":               {"123 Sukhumvit Rd"},
		"city":                {"Bangkok"},
		"region":              {"XX"},
		"postal_code":         {"10110"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle invalid-region create returned error: %v", err)
	}
	if invalidRegion.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid-region create status = %d, want %d", invalidRegion.StatusCode, http.StatusBadRequest)
	}
	assertBodyContains(t, invalidRegion.Body, []string{invalidAddressError, `aria-describedby="address-region-error"`, `Choose a valid state.`})

	create, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/addresses", url.Values{
		customerCSRFFieldName: {token},
		"full_name":           {"Anong Shopper"},
		"line1":               {"123 Sukhumvit Rd"},
		"line2":               {"Apt 4"},
		"city":                {"Bangkok"},
		"region":              {"NY"},
		"postal_code":         {"10110"},
		"phone":               {"+1 212 555 0100"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle create returned error: %v", err)
	}
	if create.StatusCode != http.StatusSeeOther || create.Headers["Location"] != "/account/addresses" {
		t.Fatalf("create = %d %q, want 303 /account/addresses", create.StatusCode, create.Headers["Location"])
	}

	list, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/account/addresses", jar))
	if err != nil {
		t.Fatalf("Handle list returned error: %v", err)
	}
	assertBodyContains(t, list.Body, []string{`data-testid="address-row"`, "Anong Shopper", "123 Sukhumvit Rd", "Bangkok, NY 10110"})
	addressID := firstAddressIDFromBody(t, list.Body)

	editPage, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/account/addresses/"+addressID+"/edit", jar))
	if err != nil {
		t.Fatalf("Handle edit page returned error: %v", err)
	}
	if editPage.StatusCode != http.StatusOK {
		t.Fatalf("edit page status = %d, want %d", editPage.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, editPage.Body, []string{`data-testid="address-form"`, `value="Anong Shopper"`, `name="version" value="1"`})

	update, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/addresses/"+addressID+"/update", url.Values{
		customerCSRFFieldName: {token},
		"version":             {"1"},
		"full_name":           {"Anong Shopper"},
		"line1":               {"123 Sukhumvit Rd"},
		"city":                {"Chiang Mai"},
		"region":              {"NY"},
		"postal_code":         {"10110"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle update returned error: %v", err)
	}
	if update.StatusCode != http.StatusSeeOther {
		t.Fatalf("update status = %d, want %d", update.StatusCode, http.StatusSeeOther)
	}
	updatedList, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/account/addresses", jar))
	if err != nil {
		t.Fatalf("Handle updated list returned error: %v", err)
	}
	assertBodyContains(t, updatedList.Body, []string{"Chiang Mai, NY 10110"})

	staleUpdate, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/addresses/"+addressID+"/update", url.Values{
		customerCSRFFieldName: {token},
		"version":             {"1"},
		"full_name":           {"Anong Shopper"},
		"line1":               {"123 Sukhumvit Rd"},
		"city":                {"Phuket"},
		"region":              {"NY"},
		"postal_code":         {"10110"},
	}, jar))
	if err != nil {
		t.Fatalf("Handle stale update returned error: %v", err)
	}
	if staleUpdate.StatusCode != http.StatusConflict {
		t.Fatalf("stale update status = %d, want %d", staleUpdate.StatusCode, http.StatusConflict)
	}
	assertBodyContains(t, staleUpdate.Body, []string{addressConflictError})

	makeDefault, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/addresses/"+addressID+"/default", url.Values{
		customerCSRFFieldName: {token},
	}, jar))
	if err != nil {
		t.Fatalf("Handle default returned error: %v", err)
	}
	if makeDefault.StatusCode != http.StatusSeeOther {
		t.Fatalf("default status = %d, want %d", makeDefault.StatusCode, http.StatusSeeOther)
	}
	defaultList, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/account/addresses", jar))
	if err != nil {
		t.Fatalf("Handle default list returned error: %v", err)
	}
	assertBodyContains(t, defaultList.Body, []string{">Default</span>", `data-confirm="Remove this address? This can't be undone."`, `<dialog data-confirm-dialog`})
	overview, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/account", jar))
	if err != nil {
		t.Fatalf("Handle overview returned error: %v", err)
	}
	assertBodyContains(t, overview.Body, []string{"123 Sukhumvit Rd"})

	remove, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/addresses/"+addressID+"/remove", url.Values{
		customerCSRFFieldName: {token},
	}, jar))
	if err != nil {
		t.Fatalf("Handle remove returned error: %v", err)
	}
	if remove.StatusCode != http.StatusSeeOther {
		t.Fatalf("remove status = %d, want %d", remove.StatusCode, http.StatusSeeOther)
	}
	removedList, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/account/addresses", jar))
	if err != nil {
		t.Fatalf("Handle removed list returned error: %v", err)
	}
	assertBodyContains(t, removedList.Body, []string{"No addresses yet."})
	clearedOverview, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/account", jar))
	if err != nil {
		t.Fatalf("Handle cleared overview returned error: %v", err)
	}
	assertBodyContains(t, clearedOverview.Body, []string{"No default address yet."})

	// Unknown but well-formed ids 404 (never 403).
	missingEdit, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/account/addresses/"+ssrTestAccountID+"/edit", jar))
	if err != nil {
		t.Fatalf("Handle missing edit returned error: %v", err)
	}
	if missingEdit.StatusCode != http.StatusNotFound {
		t.Fatalf("missing edit status = %d, want %d", missingEdit.StatusCode, http.StatusNotFound)
	}
	missingDefault, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/addresses/"+ssrTestAccountID+"/default", url.Values{
		customerCSRFFieldName: {token},
	}, jar))
	if err != nil {
		t.Fatalf("Handle missing default returned error: %v", err)
	}
	if missingDefault.StatusCode != http.StatusNotFound {
		t.Fatalf("missing default status = %d, want %d", missingDefault.StatusCode, http.StatusNotFound)
	}
}

func TestAddressEditNormalizesLegacyRegionForSelect(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	customerID := accountCustomerID(t, env, "shopper@example.com")

	address, err := env.commerce.CreateAddress(context.Background(), commerce.Address{
		CustomerID: customerID,
		FullName:   "Legacy Shopper",
		Line1:      "123 Legacy Rd",
		City:       "Los Angeles",
		Region:     "California",
		PostalCode: "90001",
		Country:    addressCountryUS,
	})
	if err != nil {
		t.Fatalf("CreateAddress returned error: %v", err)
	}

	editPage, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/account/addresses/"+address.ID+"/edit", jar))
	if err != nil {
		t.Fatalf("Handle legacy edit page returned error: %v", err)
	}
	if editPage.StatusCode != http.StatusOK {
		t.Fatalf("legacy edit page status = %d, want %d", editPage.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, editPage.Body, []string{`name="region"`, `<option value="CA" selected>California</option>`})
}

func TestAddressCreateEnforcesLimitAndValidatedNext(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	token := accountCSRFToken(t, env.handler, jar)

	createForm := func(name string, next string) url.Values {
		form := url.Values{
			customerCSRFFieldName: {token},
			"full_name":           {name},
			"line1":               {"123 Sukhumvit Rd"},
			"city":                {"Bangkok"},
			"region":              {"NY"},
			"postal_code":         {"10110"},
		}
		if next != "" {
			form.Set("next", next)
		}
		return form
	}

	nextResponse, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/addresses", createForm("Anong 1", "/checkout"), jar))
	if err != nil {
		t.Fatalf("Handle next create returned error: %v", err)
	}
	if nextResponse.Headers["Location"] != "/checkout" {
		t.Fatalf("next Location = %q, want /checkout", nextResponse.Headers["Location"])
	}
	badNextResponse, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/addresses", createForm("Anong 2", "https://evil.example"), jar))
	if err != nil {
		t.Fatalf("Handle bad-next create returned error: %v", err)
	}
	if badNextResponse.Headers["Location"] != "/account/addresses" {
		t.Fatalf("bad next Location = %q, want fallback /account/addresses", badNextResponse.Headers["Location"])
	}

	for index := 3; index <= commerce.MaxAddressesPerCustomer; index++ {
		response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/addresses", createForm("Anong "+strconv.Itoa(index), ""), jar))
		if err != nil {
			t.Fatalf("Handle create %d returned error: %v", index, err)
		}
		if response.StatusCode != http.StatusSeeOther {
			t.Fatalf("create %d status = %d, want %d", index, response.StatusCode, http.StatusSeeOther)
		}
	}

	overLimit, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/addresses", createForm("Anong 11", ""), jar))
	if err != nil {
		t.Fatalf("Handle over-limit create returned error: %v", err)
	}
	if overLimit.StatusCode != http.StatusBadRequest {
		t.Fatalf("over-limit status = %d, want %d", overLimit.StatusCode, http.StatusBadRequest)
	}
	assertBodyContains(t, overLimit.Body, []string{addressLimitError})
}

func TestPaymentMethodsLifecycle(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	customerID := accountCustomerID(t, env, "shopper@example.com")

	emptyPage, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/account/payment-methods", jar))
	if err != nil {
		t.Fatalf("Handle payment methods returned error: %v", err)
	}
	jar.update(t, emptyPage)
	assertBodyContains(t, emptyPage.Body, []string{"No saved cards yet.", `action="/account/payment-methods/add"`, "We never see or store card numbers"})
	token := hiddenInputValue(t, emptyPage.Body, customerCSRFFieldName)

	addResponse, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/payment-methods/add", url.Values{
		customerCSRFFieldName: {token},
	}, jar))
	if err != nil {
		t.Fatalf("Handle add returned error: %v", err)
	}
	if addResponse.StatusCode != http.StatusSeeOther {
		t.Fatalf("add status = %d, want %d", addResponse.StatusCode, http.StatusSeeOther)
	}
	setupSessionID := "cs_fake_setup_" + customerID
	if got := addResponse.Headers["Location"]; got != "/checkout/fake-pay?session_id="+setupSessionID {
		t.Fatalf("add Location = %q, want fake setup session URL", got)
	}

	customer, _, err := env.commerce.GetCustomerByID(context.Background(), customerID)
	if err != nil || customer.StripeCustomerID != "cus_fake_"+customerID {
		t.Fatalf("stripe customer id = %q err %v, want lazily-created cus_fake id", customer.StripeCustomerID, err)
	}

	successURL, err := env.payments.MarkSetupComplete(setupSessionID)
	if err != nil {
		t.Fatalf("MarkSetupComplete returned error: %v", err)
	}
	if !strings.HasSuffix(successURL, "/account/payment-methods?saved=1") {
		t.Fatalf("setup success URL = %q, want /account/payment-methods?saved=1 suffix", successURL)
	}

	savedRequest := jarPageRequest(http.MethodGet, "/account/payment-methods", jar)
	savedRequest.QueryStringParameters = map[string]string{"saved": "1"}
	savedPage, err := env.handler.Handle(context.Background(), savedRequest)
	if err != nil {
		t.Fatalf("Handle saved page returned error: %v", err)
	}
	assertBodyContains(t, savedPage.Body, []string{
		"Card saved.",
		`data-testid="payment-method-row"`,
		"Visa •••• 4242",
		"Expires 12/2034",
		`action="/account/payment-methods/pm_fake_visa_4242/remove"`,
		`data-confirm="Remove this card? You can add it again later."`,
		`<dialog data-confirm-dialog`,
	})

	removeResponse, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/payment-methods/pm_fake_visa_4242/remove", url.Values{
		customerCSRFFieldName: {token},
	}, jar))
	if err != nil {
		t.Fatalf("Handle remove returned error: %v", err)
	}
	if removeResponse.StatusCode != http.StatusSeeOther || removeResponse.Headers["Location"] != "/account/payment-methods" {
		t.Fatalf("remove = %d %q, want 303 /account/payment-methods", removeResponse.StatusCode, removeResponse.Headers["Location"])
	}
	removedPage, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/account/payment-methods", jar))
	if err != nil {
		t.Fatalf("Handle removed page returned error: %v", err)
	}
	assertBodyContains(t, removedPage.Body, []string{"No saved cards yet."})

	repeatRemove, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/payment-methods/pm_fake_visa_4242/remove", url.Values{
		customerCSRFFieldName: {token},
	}, jar))
	if err != nil {
		t.Fatalf("Handle repeat remove returned error: %v", err)
	}
	if repeatRemove.StatusCode != http.StatusNotFound {
		t.Fatalf("repeat remove status = %d, want %d", repeatRemove.StatusCode, http.StatusNotFound)
	}
}

func TestPaymentMethodRemoveIsOwnershipGuarded(t *testing.T) {
	env := newAccountTestEnv(t)

	// Customer A saves a card.
	jarA := testCookieJar{}
	signUpTestCustomer(t, env.handler, jarA, "first@example.com", "orchid-market-99")
	customerAID := accountCustomerID(t, env, "first@example.com")
	tokenA := accountCSRFToken(t, env.handler, jarA)
	if _, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/payment-methods/add", url.Values{customerCSRFFieldName: {tokenA}}, jarA)); err != nil {
		t.Fatalf("Handle add returned error: %v", err)
	}
	if _, err := env.payments.MarkSetupComplete("cs_fake_setup_" + customerAID); err != nil {
		t.Fatalf("MarkSetupComplete returned error: %v", err)
	}

	// Customer B with no provider customer at all: 404, never 403.
	jarB := testCookieJar{}
	signUpTestCustomer(t, env.handler, jarB, "second@example.com", "orchid-market-99")
	tokenB := accountCSRFToken(t, env.handler, jarB)
	noProviderCustomer, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/payment-methods/pm_fake_visa_4242/remove", url.Values{customerCSRFFieldName: {tokenB}}, jarB))
	if err != nil {
		t.Fatalf("Handle foreign remove returned error: %v", err)
	}
	if noProviderCustomer.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign remove status = %d, want %d", noProviderCustomer.StatusCode, http.StatusNotFound)
	}

	// Customer B with a provider customer but no such card: still 404, and
	// A's card survives.
	if _, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/payment-methods/add", url.Values{customerCSRFFieldName: {tokenB}}, jarB)); err != nil {
		t.Fatalf("Handle B add returned error: %v", err)
	}
	foreignRemove, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/payment-methods/pm_fake_visa_4242/remove", url.Values{customerCSRFFieldName: {tokenB}}, jarB))
	if err != nil {
		t.Fatalf("Handle foreign remove returned error: %v", err)
	}
	if foreignRemove.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign remove with provider customer = %d, want %d", foreignRemove.StatusCode, http.StatusNotFound)
	}
	methods, err := env.payments.ListPaymentMethods(context.Background(), "cus_fake_"+customerAID)
	if err != nil || len(methods) != 1 {
		t.Fatalf("customer A methods = %#v err %v, want the saved card untouched", methods, err)
	}
}

func TestSignedInVisitorOnAuthPagesRedirectsToAccount(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")

	for _, path := range []string{"/account/sign-in", "/account/sign-up"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			t.Run(method+" "+path, func(t *testing.T) {
				var request events.APIGatewayV2HTTPRequest
				if method == http.MethodPost {
					request = jarFormPostRequest(path, url.Values{}, jar)
				} else {
					request = jarPageRequest(method, path, jar)
				}
				response, err := env.handler.Handle(context.Background(), request)
				if err != nil {
					t.Fatalf("Handle returned error: %v", err)
				}
				if response.StatusCode != http.StatusSeeOther || response.Headers["Location"] != "/account" {
					t.Fatalf("response = %d %q, want 303 /account", response.StatusCode, response.Headers["Location"])
				}
			})
		}
	}
}

func TestAccountOverviewShowsRecentOrdersNewestFirst(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	customerID := accountCustomerID(t, env, "shopper@example.com")

	orderIDs := []string{
		strings.Repeat("a", 25) + "1",
		strings.Repeat("a", 25) + "2",
		strings.Repeat("a", 25) + "3",
		strings.Repeat("a", 25) + "4",
	}
	for index, orderID := range orderIDs {
		if _, err := env.commerce.CreateOrder(context.Background(), commerce.Order{
			ID:         orderID,
			CustomerID: customerID,
			Email:      "shopper@example.com",
			Status:     commerce.OrderStatusPaid,
			TotalCents: 5798 + index,
			Currency:   "usd",
			CreatedAt:  time.Date(2026, 6, 1+index, 9, 0, 0, 0, time.UTC),
		}); err != nil {
			t.Fatalf("CreateOrder %s returned error: %v", orderID, err)
		}
	}

	response, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/account", jar))
	if err != nil {
		t.Fatalf("Handle /account returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("/account status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, response.Body, []string{
		`href="/orders/` + orderIDs[3] + `"`,
		`href="/orders/` + orderIDs[2] + `"`,
		`href="/orders/` + orderIDs[1] + `"`,
		"Paid",
		"$58.01",
		"Jun 4, 2026",
	})
	assertBodyOmits(t, response.Body, []string{orderIDs[0]})
}

func TestCustomerAuthMetricsRecorded(t *testing.T) {
	env := newAccountTestEnv(t)
	recorder := &testMetricRecorder{}
	env.handler.metrics = recorder

	signUpTestCustomer(t, env.handler, testCookieJar{}, "shopper@example.com", "orchid-market-99")
	assertRecordedMetric(t, recorder, observability.MetricCustomerAuth, observability.UnitCount, map[string]string{
		"Service":   "ssr",
		"Operation": "sign_up",
		"Outcome":   "success",
	})

	jar := testCookieJar{}
	token := guestCSRFTokenFor(t, env.handler, jar, "/account/sign-in")
	if _, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/sign-in", url.Values{
		guestCSRFFieldName: {token},
		"email":            {"shopper@example.com"},
		"password":         {"wrong-password-11"},
	}, jar)); err != nil {
		t.Fatalf("Handle failed sign-in returned error: %v", err)
	}
	assertRecordedMetric(t, recorder, observability.MetricCustomerAuth, observability.UnitCount, map[string]string{
		"Service":   "ssr",
		"Operation": "sign_in",
		"Outcome":   "invalid",
	})
}

func TestHeaderAndFooterRenderStaticAccountLink(t *testing.T) {
	handler := NewHandlerWithProductImagePlaceholderURL(routeMatrixStore(), "/images/placeholder-product.jpg")
	for _, path := range []string{"/", "/story"} {
		t.Run(path, func(t *testing.T) {
			response, err := handler.Handle(context.Background(), pageRequest(http.MethodGet, path))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
			}
			assertBodyContains(t, response.Body, []string{
				`href="/account">Account</a>`,
				"Checkout is handled securely by Stripe. We never see or store card numbers.",
				`href="https://github.com/anhydrous99" target="_blank" rel="noopener noreferrer">find me on GitHub</a>`,
			})
		})
	}
}
