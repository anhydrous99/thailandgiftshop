package ssr

import (
	"net/http"
	"regexp"
	"strings"
)

const allowedMethods = http.MethodGet + ", " + http.MethodHead
const cartMutationAllowedMethods = http.MethodPost
const accountFormAllowedMethods = http.MethodGet + ", " + http.MethodHead + ", " + http.MethodPost

type pageKind string

const (
	pageHome           pageKind = "home"
	pageProducts       pageKind = "products"
	pageProductDetail  pageKind = "product-detail"
	pageCategories     pageKind = "categories"
	pageCategoryDetail pageKind = "category-detail"
	pageStory          pageKind = "story"
	pageCart           pageKind = "cart"
	pageCheckout       pageKind = "checkout"
	pageRobotsTxt      pageKind = "robots-txt"
	pageSitemapXML     pageKind = "sitemap-xml"
	pageCartItems      pageKind = "cart-items"
	pageCartQuantity   pageKind = "cart-quantity"
	pageCartRemove     pageKind = "cart-remove"
	pageCartClear      pageKind = "cart-clear"

	pageAccountSignUp               pageKind = "account-sign-up"
	pageAccountSignIn               pageKind = "account-sign-in"
	pageAccountPasswordReset        pageKind = "account-password-reset"
	pageAccountPasswordResetConfirm pageKind = "account-password-reset-confirm"
	pageAccountSignOut              pageKind = "account-sign-out"
	pageAccount                     pageKind = "account"
	pageAccountPassword             pageKind = "account-password"
	pageAccountAddresses            pageKind = "account-addresses"
	pageAccountAddressEdit          pageKind = "account-address-edit"
	pageAccountAddressUpdate        pageKind = "account-address-update"
	pageAccountAddressRemove        pageKind = "account-address-remove"
	pageAccountAddressDefault       pageKind = "account-address-default"
	pageAccountPaymentMethods       pageKind = "account-payment-methods"
	pageAccountPaymentMethodAdd     pageKind = "account-payment-method-add"
	pageAccountPaymentMethodRemove  pageKind = "account-payment-method-remove"

	pageCheckoutPlaceOrder pageKind = "checkout-place-order"
	pageCheckoutConfirm    pageKind = "checkout-confirm"
	pageCheckoutFakePay    pageKind = "checkout-fake-pay"
	pageOrders             pageKind = "orders"
	pageOrderDetail        pageKind = "order-detail"
	pageStripeWebhook      pageKind = "stripe-webhook"

	pageUnknown pageKind = "unknown"
)

// accountIDPattern matches customer-facing record identifiers (addresses,
// orders): 16 random bytes as lowercase Crockford base32, always 26 chars.
var accountIDPattern = regexp.MustCompile(`^[a-z0-9]{26}$`)

// paymentMethodIDPattern matches Stripe payment-method identifiers and the
// fake provider's pm_fake_* values.
var paymentMethodIDPattern = regexp.MustCompile(`^pm_[A-Za-z0-9_]{4,64}$`)

type pageRoute struct {
	kind           pageKind
	slug           string
	redirectTo     string
	knownPageShape bool
}

func ssrMetricRoute(path string) string {
	if path == "/hello-fragment" {
		return "hello_fragment"
	}
	return string(routeForPath(path).kind)
}

func routeForPath(path string) pageRoute {
	switch path {
	case "/":
		return pageRoute{kind: pageHome, knownPageShape: true}
	case "/shop":
		return pageRoute{kind: pageProducts, redirectTo: "/products", knownPageShape: true}
	case "/about":
		return pageRoute{kind: pageStory, redirectTo: "/story", knownPageShape: true}
	case "/products":
		return pageRoute{kind: pageProducts, knownPageShape: true}
	case "/products/":
		return pageRoute{kind: pageProducts, redirectTo: "/products", knownPageShape: true}
	case "/categories":
		return pageRoute{kind: pageCategories, knownPageShape: true}
	case "/categories/":
		return pageRoute{kind: pageCategories, redirectTo: "/categories", knownPageShape: true}
	case "/story":
		return pageRoute{kind: pageStory, knownPageShape: true}
	case "/story/":
		return pageRoute{kind: pageStory, redirectTo: "/story", knownPageShape: true}
	case "/cart":
		return pageRoute{kind: pageCart, knownPageShape: true}
	case "/cart/":
		return pageRoute{kind: pageCart, redirectTo: "/cart", knownPageShape: true}
	case "/checkout":
		return pageRoute{kind: pageCheckout, knownPageShape: true}
	case "/checkout/":
		return pageRoute{kind: pageCheckout, redirectTo: "/checkout", knownPageShape: true}
	case "/checkout/place-order":
		return pageRoute{kind: pageCheckoutPlaceOrder, knownPageShape: true}
	case "/checkout/confirm":
		return pageRoute{kind: pageCheckoutConfirm, knownPageShape: true}
	case "/checkout/confirm/":
		return pageRoute{kind: pageCheckoutConfirm, redirectTo: "/checkout/confirm", knownPageShape: true}
	case "/checkout/fake-pay":
		return pageRoute{kind: pageCheckoutFakePay, knownPageShape: true}
	case "/checkout/fake-pay/":
		return pageRoute{kind: pageCheckoutFakePay, redirectTo: "/checkout/fake-pay", knownPageShape: true}
	case "/orders":
		return pageRoute{kind: pageOrders, knownPageShape: true}
	case "/orders/":
		return pageRoute{kind: pageOrders, redirectTo: "/orders", knownPageShape: true}
	case "/webhooks/stripe":
		return pageRoute{kind: pageStripeWebhook, knownPageShape: true}
	case "/robots.txt":
		return pageRoute{kind: pageRobotsTxt, knownPageShape: true}
	case "/sitemap.xml":
		return pageRoute{kind: pageSitemapXML, knownPageShape: true}
	case "/cart/items":
		return pageRoute{kind: pageCartItems, knownPageShape: true}
	case "/cart/clear":
		return pageRoute{kind: pageCartClear, knownPageShape: true}
	case "/account":
		return pageRoute{kind: pageAccount, knownPageShape: true}
	case "/account/":
		return pageRoute{kind: pageAccount, redirectTo: "/account", knownPageShape: true}
	case "/account/sign-up":
		return pageRoute{kind: pageAccountSignUp, knownPageShape: true}
	case "/account/sign-up/":
		return pageRoute{kind: pageAccountSignUp, redirectTo: "/account/sign-up", knownPageShape: true}
	case "/account/sign-in":
		return pageRoute{kind: pageAccountSignIn, knownPageShape: true}
	case "/account/sign-in/":
		return pageRoute{kind: pageAccountSignIn, redirectTo: "/account/sign-in", knownPageShape: true}
	case "/account/password-reset":
		return pageRoute{kind: pageAccountPasswordReset, knownPageShape: true}
	case "/account/password-reset/":
		return pageRoute{kind: pageAccountPasswordReset, redirectTo: "/account/password-reset", knownPageShape: true}
	case "/account/password-reset/confirm":
		return pageRoute{kind: pageAccountPasswordResetConfirm, knownPageShape: true}
	case "/account/password-reset/confirm/":
		return pageRoute{kind: pageAccountPasswordResetConfirm, redirectTo: "/account/password-reset/confirm", knownPageShape: true}
	case "/account/sign-out":
		return pageRoute{kind: pageAccountSignOut, knownPageShape: true}
	case "/account/password":
		return pageRoute{kind: pageAccountPassword, knownPageShape: true}
	case "/account/addresses":
		return pageRoute{kind: pageAccountAddresses, knownPageShape: true}
	case "/account/addresses/":
		return pageRoute{kind: pageAccountAddresses, redirectTo: "/account/addresses", knownPageShape: true}
	case "/account/payment-methods":
		return pageRoute{kind: pageAccountPaymentMethods, knownPageShape: true}
	case "/account/payment-methods/":
		return pageRoute{kind: pageAccountPaymentMethods, redirectTo: "/account/payment-methods", knownPageShape: true}
	case "/account/payment-methods/add":
		return pageRoute{kind: pageAccountPaymentMethodAdd, knownPageShape: true}
	}
	if route := cartMutationRouteForPath(path, "/cart/items/", "/quantity", pageCartQuantity); route.knownPageShape {
		return route
	}
	if route := cartMutationRouteForPath(path, "/cart/items/", "/remove", pageCartRemove); route.knownPageShape {
		return route
	}

	if route := accountAddressEditRouteForPath(path); route.knownPageShape {
		return route
	}
	if route := orderDetailRouteForPath(path); route.knownPageShape {
		return route
	}
	if route := accountMutationRouteForPath(path, "/account/addresses/", "/update", pageAccountAddressUpdate, accountIDPattern); route.knownPageShape {
		return route
	}
	if route := accountMutationRouteForPath(path, "/account/addresses/", "/remove", pageAccountAddressRemove, accountIDPattern); route.knownPageShape {
		return route
	}
	if route := accountMutationRouteForPath(path, "/account/addresses/", "/default", pageAccountAddressDefault, accountIDPattern); route.knownPageShape {
		return route
	}
	if route := accountMutationRouteForPath(path, "/account/payment-methods/", "/remove", pageAccountPaymentMethodRemove, paymentMethodIDPattern); route.knownPageShape {
		return route
	}

	if route := detailRouteForPath(path, "/products/", pageProductDetail); route.knownPageShape {
		return route
	}
	if route := detailRouteForPath(path, "/categories/", pageCategoryDetail); route.knownPageShape {
		return route
	}

	return pageRoute{kind: pageUnknown}
}

func detailRouteForPath(path string, prefix string, kind pageKind) pageRoute {
	slug, ok := strings.CutPrefix(path, prefix)
	if !ok {
		return pageRoute{kind: pageUnknown}
	}

	if slug == "" {
		return pageRoute{kind: pageUnknown}
	}
	if trimmedSlug, ok := strings.CutSuffix(slug, "/"); ok {
		slug = trimmedSlug
		if slug == "" || strings.Contains(slug, "/") {
			return pageRoute{kind: pageUnknown}
		}
		return pageRoute{kind: kind, slug: slug, redirectTo: prefix + slug, knownPageShape: true}
	}
	if strings.Contains(slug, "/") {
		return pageRoute{kind: pageUnknown}
	}

	return pageRoute{kind: kind, slug: slug, knownPageShape: true}
}

func cartMutationRouteForPath(path string, prefix string, suffix string, kind pageKind) pageRoute {
	slug, ok := strings.CutPrefix(path, prefix)
	if !ok {
		return pageRoute{kind: pageUnknown}
	}
	slug, ok = strings.CutSuffix(slug, suffix)
	if !ok {
		return pageRoute{kind: pageUnknown}
	}
	if slug == "" || strings.Contains(slug, "/") {
		return pageRoute{kind: pageUnknown}
	}

	return pageRoute{kind: kind, slug: slug, knownPageShape: true}
}

// accountMutationRouteForPath parses "<prefix><id><suffix>" account paths the
// way cartMutationRouteForPath parses cart ones, additionally requiring the
// id segment to match the route's identifier pattern; anything else stays an
// unknown route and 404s instead of reaching a handler.
func accountMutationRouteForPath(path string, prefix string, suffix string, kind pageKind, idPattern *regexp.Regexp) pageRoute {
	route := cartMutationRouteForPath(path, prefix, suffix, kind)
	if !route.knownPageShape || !idPattern.MatchString(route.slug) {
		return pageRoute{kind: pageUnknown}
	}

	return route
}

// orderDetailRouteForPath parses "/orders/{orderID}" (and its trailing-slash
// 308), requiring the 26-char order identifier shape; anything else stays an
// unknown route and 404s.
func orderDetailRouteForPath(path string) pageRoute {
	id, ok := strings.CutPrefix(path, "/orders/")
	if !ok {
		return pageRoute{kind: pageUnknown}
	}
	if trimmed, ok := strings.CutSuffix(id, "/"); ok {
		if !accountIDPattern.MatchString(trimmed) {
			return pageRoute{kind: pageUnknown}
		}
		return pageRoute{kind: pageOrderDetail, slug: trimmed, redirectTo: "/orders/" + trimmed, knownPageShape: true}
	}
	if accountIDPattern.MatchString(id) {
		return pageRoute{kind: pageOrderDetail, slug: id, knownPageShape: true}
	}

	return pageRoute{kind: pageUnknown}
}

// accountAddressEditRouteForPath parses the one GET account page with a path
// parameter, "/account/addresses/{id}/edit", including its trailing-slash 308.
func accountAddressEditRouteForPath(path string) pageRoute {
	id, ok := strings.CutPrefix(path, "/account/addresses/")
	if !ok {
		return pageRoute{kind: pageUnknown}
	}
	if trimmed, ok := strings.CutSuffix(id, "/edit/"); ok {
		if !accountIDPattern.MatchString(trimmed) {
			return pageRoute{kind: pageUnknown}
		}
		return pageRoute{kind: pageAccountAddressEdit, slug: trimmed, redirectTo: "/account/addresses/" + trimmed + "/edit", knownPageShape: true}
	}
	if trimmed, ok := strings.CutSuffix(id, "/edit"); ok && accountIDPattern.MatchString(trimmed) {
		return pageRoute{kind: pageAccountAddressEdit, slug: trimmed, knownPageShape: true}
	}

	return pageRoute{kind: pageUnknown}
}

func isAllowedPageMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func isAllowedRouteMethod(route pageRoute, method string) bool {
	if isCartMutationRoute(route.kind) || isAccountMutationRoute(route.kind) || isCheckoutMutationRoute(route.kind) {
		return method == http.MethodPost
	}
	if isAccountFormRoute(route.kind) || route.kind == pageCheckoutFakePay {
		return isAllowedPageMethod(method) || method == http.MethodPost
	}

	return isAllowedPageMethod(method)
}

func allowedMethodsForRoute(route pageRoute) string {
	if isCartMutationRoute(route.kind) || isAccountMutationRoute(route.kind) || isCheckoutMutationRoute(route.kind) {
		return cartMutationAllowedMethods
	}
	if isAccountFormRoute(route.kind) || route.kind == pageCheckoutFakePay {
		return accountFormAllowedMethods
	}

	return allowedMethods
}

func isCartMutationRoute(kind pageKind) bool {
	return kind == pageCartItems || kind == pageCartQuantity || kind == pageCartRemove || kind == pageCartClear
}

// isAccountFormRoute reports the GET,HEAD,POST routes: a rendered page whose
// POST handles the page's own form submission.
func isAccountFormRoute(kind pageKind) bool {
	return kind == pageAccountSignUp || kind == pageAccountSignIn || kind == pageAccountPasswordReset || kind == pageAccountPasswordResetConfirm || kind == pageAccountAddresses
}

// isAccountMutationRoute reports the POST-only customer routes.
func isAccountMutationRoute(kind pageKind) bool {
	switch kind {
	case pageAccountSignOut, pageAccountPassword, pageAccountAddressUpdate, pageAccountAddressRemove, pageAccountAddressDefault, pageAccountPaymentMethodAdd, pageAccountPaymentMethodRemove:
		return true
	}

	return false
}

// isAccountRoute reports every route dispatched to the account handler.
func isAccountRoute(kind pageKind) bool {
	switch kind {
	case pageAccount, pageAccountAddressEdit, pageAccountPaymentMethods:
		return true
	}

	return isAccountFormRoute(kind) || isAccountMutationRoute(kind)
}

// isCheckoutMutationRoute reports the POST-only checkout routes. The webhook
// is also POST-only but authenticates with the provider signature instead of
// a customer session, so it dispatches separately.
func isCheckoutMutationRoute(kind pageKind) bool {
	return kind == pageCheckoutPlaceOrder || kind == pageStripeWebhook
}

// isCheckoutFlowRoute reports every session-gated checkout and order route
// dispatched to the checkout-flow handler.
func isCheckoutFlowRoute(kind pageKind) bool {
	switch kind {
	case pageCheckout, pageCheckoutPlaceOrder, pageCheckoutConfirm, pageCheckoutFakePay, pageOrders, pageOrderDetail:
		return true
	}

	return false
}
