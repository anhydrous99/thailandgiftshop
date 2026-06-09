package ssr

import (
	"net/http"
	"strings"
)

const allowedMethods = http.MethodGet + ", " + http.MethodHead
const cartMutationAllowedMethods = http.MethodPost

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
	pageUnknown        pageKind = "unknown"
)

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
	case "/robots.txt":
		return pageRoute{kind: pageRobotsTxt, knownPageShape: true}
	case "/sitemap.xml":
		return pageRoute{kind: pageSitemapXML, knownPageShape: true}
	case "/cart/items":
		return pageRoute{kind: pageCartItems, knownPageShape: true}
	case "/cart/clear":
		return pageRoute{kind: pageCartClear, knownPageShape: true}
	}
	if route := cartMutationRouteForPath(path, "/cart/items/", "/quantity", pageCartQuantity); route.knownPageShape {
		return route
	}
	if route := cartMutationRouteForPath(path, "/cart/items/", "/remove", pageCartRemove); route.knownPageShape {
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

func isAllowedPageMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func isAllowedRouteMethod(route pageRoute, method string) bool {
	if isCartMutationRoute(route.kind) {
		return method == http.MethodPost
	}

	return isAllowedPageMethod(method)
}

func allowedMethodsForRoute(route pageRoute) string {
	if isCartMutationRoute(route.kind) {
		return cartMutationAllowedMethods
	}

	return allowedMethods
}

func isCartMutationRoute(kind pageKind) bool {
	return kind == pageCartItems || kind == pageCartQuantity || kind == pageCartRemove || kind == pageCartClear
}
