package ssr

import (
	"encoding/xml"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
)

const canonicalHost = "https://thailandgiftshop.com"
const privatePageCacheControl = "private, no-store"
const seoDiscoveryCacheControl = "public, max-age=300, s-maxage=600"

// Browsers revalidate every visit (max-age=0) so admin edits show up on
// refresh; CloudFront keeps a shared edge copy for five minutes (s-maxage).
const catalogPageCacheControl = "public, max-age=0, s-maxage=300"

type seoMetadata struct {
	Title       string
	Description string
	Canonical   string
	Robots      string
	SocialImage string
	JSONLD      []any
}

type breadcrumbItem struct {
	Name string
	Path string
}

type sitemapURLSet struct {
	XMLName xml.Name     `xml:"urlset"`
	Xmlns   string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

func metadataForPath(title string, description string, path string) seoMetadata {
	return seoMetadata{
		Title:       title,
		Description: normalizeMetaDescription(description),
		Canonical:   canonicalURL(path),
		SocialImage: canonicalURL("/static/home-hero.jpg"),
	}
}

func noindexMetadata(title string, description string, path string) seoMetadata {
	metadata := metadataForPath(title, description, path)
	metadata.Robots = "noindex, follow"
	return metadata
}

// The metadata for pages without dynamic content is built once per warm
// Lambda and shared across requests. The returned seoMetadata (including
// its JSONLD slice and maps) must be treated as immutable by callers.
var homeMetadata = sync.OnceValue(func() seoMetadata {
	metadata := metadataForPath(
		"Thailand Gift Shop",
		"Browse a Thai gift-shop catalog of snacks, souvenirs, pantry favorites, textiles, decor, wellness, and small keepsakes.",
		"/",
	)
	metadata.JSONLD = []any{
		map[string]any{
			"@context": "https://schema.org",
			"@type":    "Organization",
			"name":     "Thailand Gift Shop",
			"url":      canonicalHost,
			"logo":     canonicalURL("/static/logo.svg"),
		},
		map[string]any{
			"@context": "https://schema.org",
			"@type":    "WebSite",
			"name":     "Thailand Gift Shop",
			"url":      canonicalHost,
		},
	}
	return metadata
})

var productListingMetadata = sync.OnceValue(func() seoMetadata {
	return metadataWithBreadcrumbs(metadataForPath(
		"Products | Thailand Gift Shop",
		"Browse Thai snacks, souvenirs, textiles, pantry items, decor, wellness, and small keepsakes.",
		"/products",
	), productListingBreadcrumbs())
})

func productDetailMetadata(product catalog.Product, placeholderURL string) seoMetadata {
	metadata := metadataForPath(
		product.Name+" | Thailand Gift Shop",
		product.Description,
		"/products/"+product.Slug,
	)
	metadata.SocialImage = canonicalURL(product.DisplayImageURL(placeholderURL))
	metadata.JSONLD = []any{productJSONLD(product, placeholderURL)}
	return metadataWithBreadcrumbs(metadata, productDetailBreadcrumbs(product))
}

var categoryIndexMetadata = sync.OnceValue(func() seoMetadata {
	return metadataWithBreadcrumbs(metadataForPath(
		"Categories | Thailand Gift Shop",
		"Shop Thai gift-shop finds by aisle, including snacks, souvenirs, textiles, decor, wellness, and pantry favorites.",
		"/categories",
	), categoryIndexBreadcrumbs())
})

func categoryDetailMetadata(category catalog.Category) seoMetadata {
	return metadataWithBreadcrumbs(
		metadataForPath(category.Name+" | Thailand Gift Shop", category.Description, "/categories/"+category.Slug),
		categoryDetailBreadcrumbs(category),
	)
}

var storyMetadata = sync.OnceValue(func() seoMetadata {
	return metadataWithBreadcrumbs(metadataForPath(
		"Our Story | Thailand Gift Shop",
		"Learn how Thailand Gift Shop organizes Thai snacks, souvenirs, textiles, pantry items, decor, wellness, and small keepsakes for calm browsing.",
		"/story",
	), storyBreadcrumbs())
})

var shippingMetadata = sync.OnceValue(func() seoMetadata {
	return metadataWithBreadcrumbs(metadataForPath(
		"Shipping | Thailand Gift Shop",
		"How Thailand Gift Shop ships Thai snacks, souvenirs, and keepsakes within the United States.",
		"/shipping",
	), shippingBreadcrumbs())
})

var returnsMetadata = sync.OnceValue(func() seoMetadata {
	return metadataWithBreadcrumbs(metadataForPath(
		"Returns and refunds | Thailand Gift Shop",
		"How returns and full refunds work for Thailand Gift Shop orders.",
		"/returns",
	), returnsBreadcrumbs())
})

var contactMetadata = sync.OnceValue(func() seoMetadata {
	return metadataWithBreadcrumbs(metadataForPath(
		"Contact us | Thailand Gift Shop",
		"Reach Thailand Gift Shop for help with orders, shipping, returns, and product questions.",
		"/contact",
	), contactBreadcrumbs())
})

// Privacy and Terms are noindex while their copy is a draft pending legal
// review, so search engines do not surface placeholder legal text.
var privacyMetadata = sync.OnceValue(func() seoMetadata {
	return metadataWithBreadcrumbs(noindexMetadata(
		"Privacy Policy | Thailand Gift Shop",
		"How Thailand Gift Shop handles your personal information.",
		"/privacy",
	), privacyBreadcrumbs())
})

var termsMetadata = sync.OnceValue(func() seoMetadata {
	return metadataWithBreadcrumbs(noindexMetadata(
		"Terms of Service | Thailand Gift Shop",
		"The terms that govern your use of Thailand Gift Shop.",
		"/terms",
	), termsBreadcrumbs())
})

var cartMetadata = sync.OnceValue(func() seoMetadata {
	return noindexMetadata(
		"Cart | Thailand Gift Shop",
		"Review quantities for your Thai gift-shop finds before secure Stripe checkout.",
		"/cart",
	)
})

var checkoutMetadata = sync.OnceValue(func() seoMetadata {
	return noindexMetadata(
		"Checkout | Thailand Gift Shop",
		"Choose a shipping address and continue to secure Stripe payment for your Thai gift-shop finds.",
		"/checkout",
	)
})

var checkoutConfirmMetadata = sync.OnceValue(func() seoMetadata {
	return noindexMetadata(
		"Payment processing | Thailand Gift Shop",
		"We are confirming your Stripe payment. This page refreshes on its own.",
		"/checkout/confirm",
	)
})

var fakePayMetadata = sync.OnceValue(func() seoMetadata {
	return noindexMetadata(
		"Demo payment | Thailand Gift Shop",
		"Demo payment page for local runs. No real charge is made.",
		"/checkout/fake-pay",
	)
})

var ordersMetadata = sync.OnceValue(func() seoMetadata {
	return noindexMetadata(
		"Orders | Thailand Gift Shop",
		"Track the status, totals, and shipping of your Thailand Gift Shop orders.",
		"/orders",
	)
})

func orderDetailMetadata(orderID string) seoMetadata {
	return noindexMetadata(
		"Order "+orderID+" | Thailand Gift Shop",
		"Order status, items, shipping address, and tracking for your Thailand Gift Shop order.",
		"/orders/"+orderID,
	)
}

var signInMetadata = sync.OnceValue(func() seoMetadata {
	return noindexMetadata(
		"Sign in | Thailand Gift Shop",
		"Sign in to your Thailand Gift Shop account to check orders, addresses, and saved cards.",
		"/account/sign-in",
	)
})

var signUpMetadata = sync.OnceValue(func() seoMetadata {
	return noindexMetadata(
		"Create account | Thailand Gift Shop",
		"Create a Thailand Gift Shop account to check out, track orders, and save addresses.",
		"/account/sign-up",
	)
})

var passwordResetRequestMetadata = sync.OnceValue(func() seoMetadata {
	return noindexMetadata(
		"Reset password | Thailand Gift Shop",
		"Request a password reset link for your Thailand Gift Shop account.",
		"/account/password-reset",
	)
})

var passwordResetConfirmMetadata = sync.OnceValue(func() seoMetadata {
	return noindexMetadata(
		"Choose new password | Thailand Gift Shop",
		"Choose a new password for your Thailand Gift Shop account.",
		"/account/password-reset/confirm",
	)
})

var accountMetadata = sync.OnceValue(func() seoMetadata {
	return noindexMetadata(
		"Account | Thailand Gift Shop",
		"Manage your Thailand Gift Shop account: recent orders, default address, and password.",
		"/account",
	)
})

var addressesMetadata = sync.OnceValue(func() seoMetadata {
	return noindexMetadata(
		"Addresses | Thailand Gift Shop",
		"Save and manage the shipping addresses for your Thailand Gift Shop orders.",
		"/account/addresses",
	)
})

func addressEditMetadata(addressID string) seoMetadata {
	return noindexMetadata(
		"Edit address | Thailand Gift Shop",
		"Update a saved shipping address for your Thailand Gift Shop orders.",
		"/account/addresses/"+addressID+"/edit",
	)
}

var paymentMethodsMetadata = sync.OnceValue(func() seoMetadata {
	return noindexMetadata(
		"Saved cards | Thailand Gift Shop",
		"Manage the cards saved with Stripe for faster Thailand Gift Shop checkout.",
		"/account/payment-methods",
	)
})

func metadataWithBreadcrumbs(metadata seoMetadata, breadcrumbs []breadcrumbItem) seoMetadata {
	if len(breadcrumbs) == 0 {
		return metadata
	}
	metadata.JSONLD = append(metadata.JSONLD, breadcrumbJSONLD(breadcrumbs))
	return metadata
}

func homeBreadcrumb() breadcrumbItem {
	return breadcrumbItem{Name: "Home", Path: "/"}
}

func productListingBreadcrumbs() []breadcrumbItem {
	return []breadcrumbItem{homeBreadcrumb(), {Name: "Products", Path: "/products"}}
}

func productDetailBreadcrumbs(product catalog.Product) []breadcrumbItem {
	return []breadcrumbItem{homeBreadcrumb(), {Name: "Products", Path: "/products"}, {Name: product.Name, Path: "/products/" + product.Slug}}
}

func categoryIndexBreadcrumbs() []breadcrumbItem {
	return []breadcrumbItem{homeBreadcrumb(), {Name: "Categories", Path: "/categories"}}
}

func categoryDetailBreadcrumbs(category catalog.Category) []breadcrumbItem {
	return []breadcrumbItem{homeBreadcrumb(), {Name: "Categories", Path: "/categories"}, {Name: category.Name, Path: "/categories/" + category.Slug}}
}

func storyBreadcrumbs() []breadcrumbItem {
	return []breadcrumbItem{homeBreadcrumb(), {Name: "Our Story", Path: "/story"}}
}

func shippingBreadcrumbs() []breadcrumbItem {
	return []breadcrumbItem{homeBreadcrumb(), {Name: "Shipping", Path: "/shipping"}}
}

func returnsBreadcrumbs() []breadcrumbItem {
	return []breadcrumbItem{homeBreadcrumb(), {Name: "Returns and refunds", Path: "/returns"}}
}

func contactBreadcrumbs() []breadcrumbItem {
	return []breadcrumbItem{homeBreadcrumb(), {Name: "Contact us", Path: "/contact"}}
}

func privacyBreadcrumbs() []breadcrumbItem {
	return []breadcrumbItem{homeBreadcrumb(), {Name: "Privacy Policy", Path: "/privacy"}}
}

func termsBreadcrumbs() []breadcrumbItem {
	return []breadcrumbItem{homeBreadcrumb(), {Name: "Terms of Service", Path: "/terms"}}
}

func accountBreadcrumbs() []breadcrumbItem {
	return []breadcrumbItem{homeBreadcrumb(), {Name: "Account", Path: "/account"}}
}

func addressesBreadcrumbs() []breadcrumbItem {
	return []breadcrumbItem{homeBreadcrumb(), {Name: "Account", Path: "/account"}, {Name: "Addresses", Path: "/account/addresses"}}
}

func paymentMethodsBreadcrumbs() []breadcrumbItem {
	return []breadcrumbItem{homeBreadcrumb(), {Name: "Account", Path: "/account"}, {Name: "Saved cards", Path: "/account/payment-methods"}}
}

func checkoutBreadcrumbs() []breadcrumbItem {
	return []breadcrumbItem{homeBreadcrumb(), {Name: "Cart", Path: "/cart"}, {Name: "Checkout", Path: "/checkout"}}
}

func ordersBreadcrumbs() []breadcrumbItem {
	return []breadcrumbItem{homeBreadcrumb(), {Name: "Account", Path: "/account"}, {Name: "Orders", Path: "/orders"}}
}

func orderDetailBreadcrumbs(orderID string) []breadcrumbItem {
	return []breadcrumbItem{homeBreadcrumb(), {Name: "Account", Path: "/account"}, {Name: "Orders", Path: "/orders"}, {Name: "Order " + orderID, Path: "/orders/" + orderID}}
}

// guestOrderDetailBreadcrumbs degrades the trail for tokenized guest access:
// guests have no /account or /orders pages, so it is Home → this order.
func guestOrderDetailBreadcrumbs(orderID string) []breadcrumbItem {
	return []breadcrumbItem{homeBreadcrumb(), {Name: "Order " + orderID, Path: "/orders/" + orderID}}
}

func breadcrumbJSONLD(breadcrumbs []breadcrumbItem) map[string]any {
	items := make([]map[string]any, 0, len(breadcrumbs))
	for index, breadcrumb := range breadcrumbs {
		items = append(items, map[string]any{
			"@type":    "ListItem",
			"position": index + 1,
			"name":     breadcrumb.Name,
			"item":     canonicalURL(breadcrumb.Path),
		})
	}
	return map[string]any{
		"@context":        "https://schema.org",
		"@type":           "BreadcrumbList",
		"itemListElement": items,
	}
}

func productJSONLD(product catalog.Product, placeholderURL string) map[string]any {
	availability := "https://schema.org/InStock"
	if product.OutOfStock() {
		availability = "https://schema.org/OutOfStock"
	}

	return map[string]any{
		"@context":    "https://schema.org",
		"@type":       "Product",
		"name":        product.Name,
		"description": product.Description,
		"image":       canonicalURL(product.DisplayImageURL(placeholderURL)),
		"url":         canonicalURL("/products/" + product.Slug),
		"offers": map[string]any{
			"@type":         "Offer",
			"priceCurrency": "USD",
			"price":         fmt.Sprintf("%.2f", float64(max(product.PriceCents, 0))/100),
			"availability":  availability,
			"url":           canonicalURL("/products/" + product.Slug),
		},
	}
}

func canonicalURL(pathOrURL string) string {
	value := strings.TrimSpace(pathOrURL)
	if value == "" {
		return canonicalHost + "/"
	}
	if parsed, err := url.Parse(value); err == nil && parsed.IsAbs() {
		return value
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return canonicalHost + value
}

func normalizeMetaDescription(description string) string {
	description = strings.Join(strings.Fields(description), " ")
	if description == "" {
		return "Browse Thailand Gift Shop for Thai snacks, souvenirs, pantry favorites, and small keepsakes."
	}
	return description
}

func robotsTxt() string {
	return strings.Join([]string{
		"User-agent: *",
		"Allow: /",
		"Sitemap: " + canonicalURL("/sitemap.xml"),
		"",
	}, "\n")
}

func sitemapXML(products []catalog.Product, categories []catalog.Category) (string, error) {
	urls := make([]sitemapURL, 0, len(products)+len(categories)+4)
	urls = append(urls,
		sitemapURL{Loc: canonicalURL("/")},
		sitemapURL{Loc: canonicalURL("/products")},
	)
	for _, product := range products {
		if product.Status != catalog.StatusActive {
			continue
		}
		urls = append(urls, sitemapURL{
			Loc:     canonicalURL("/products/" + product.Slug),
			LastMod: sitemapLastMod(product.UpdatedAt),
		})
	}
	urls = append(urls, sitemapURL{Loc: canonicalURL("/categories")})
	for _, category := range categories {
		if category.Status != catalog.StatusActive {
			continue
		}
		urls = append(urls, sitemapURL{
			Loc:     canonicalURL("/categories/" + category.Slug),
			LastMod: sitemapLastMod(category.UpdatedAt),
		})
	}
	urls = append(urls,
		sitemapURL{Loc: canonicalURL("/story")},
		sitemapURL{Loc: canonicalURL("/shipping")},
		sitemapURL{Loc: canonicalURL("/returns")},
		sitemapURL{Loc: canonicalURL("/contact")},
	)

	data, err := xml.MarshalIndent(sitemapURLSet{
		Xmlns: "http://www.sitemaps.org/schemas/sitemap/0.9",
		URLs:  urls,
	}, "", "  ")
	if err != nil {
		return "", err
	}
	return xml.Header + string(data) + "\n", nil
}

func sitemapLastMod(updatedAt time.Time) string {
	if updatedAt.IsZero() {
		return ""
	}
	return updatedAt.UTC().Format("2006-01-02")
}

func seoHeadersForRoute(kind pageKind) map[string]string {
	// Every customer-account route — pages, auth forms, and POST redirects —
	// is private and never edge-cached.
	if isAccountRoute(kind) {
		return map[string]string{
			"Cache-Control": privatePageCacheControl,
			"X-Robots-Tag":  "noindex, follow",
		}
	}
	if isCheckoutFlowRoute(kind) {
		return map[string]string{
			"Cache-Control": privatePageCacheControl,
			"X-Robots-Tag":  "noindex, follow",
		}
	}
	switch kind {
	case pageCart:
		return map[string]string{
			"Cache-Control": privatePageCacheControl,
			"X-Robots-Tag":  "noindex, follow",
		}
	case pageStripeWebhook:
		return map[string]string{"Cache-Control": "no-store"}
	case pageRobotsTxt, pageSitemapXML:
		return map[string]string{"Cache-Control": seoDiscoveryCacheControl}
	case pageHome, pageProducts, pageProductDetail, pageCategories, pageCategoryDetail, pageStory, pageShipping, pageReturns, pageContact:
		return map[string]string{"Cache-Control": catalogPageCacheControl}
	case pagePrivacy, pageTerms:
		// Public and edge-cacheable, but noindex while the legal copy is a
		// draft pending review.
		return map[string]string{"Cache-Control": catalogPageCacheControl, "X-Robots-Tag": "noindex, follow"}
	}
	return nil
}

func usesSharedPublicPageCache(kind pageKind) bool {
	switch kind {
	case pageHome, pageProducts, pageProductDetail, pageCategories, pageCategoryDetail, pageStory,
		pageShipping, pageReturns, pageContact, pagePrivacy, pageTerms:
		return true
	}
	return false
}
