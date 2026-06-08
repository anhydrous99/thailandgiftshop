package ssr

import (
	"encoding/xml"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
)

const canonicalHost = "https://thailandgiftshop.com"
const privatePageCacheControl = "private, no-store"
const seoDiscoveryCacheControl = "public, max-age=300, s-maxage=600"

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
		SocialImage: canonicalURL("/static/home-hero.png"),
	}
}

func noindexMetadata(title string, description string, path string) seoMetadata {
	metadata := metadataForPath(title, description, path)
	metadata.Robots = "noindex, follow"
	return metadata
}

func homeMetadata() seoMetadata {
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
}

func productListingMetadata() seoMetadata {
	return metadataWithBreadcrumbs(metadataForPath(
		"Products | Thailand Gift Shop",
		"Browse Thai snacks, souvenirs, textiles, pantry items, decor, wellness, and small keepsakes.",
		"/products",
	), productListingBreadcrumbs())
}

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

func categoryIndexMetadata() seoMetadata {
	return metadataWithBreadcrumbs(metadataForPath(
		"Categories | Thailand Gift Shop",
		"Shop Thai gift-shop finds by aisle, including snacks, souvenirs, textiles, decor, wellness, and pantry favorites.",
		"/categories",
	), categoryIndexBreadcrumbs())
}

func categoryDetailMetadata(category catalog.Category) seoMetadata {
	return metadataWithBreadcrumbs(
		metadataForPath(category.Name+" | Thailand Gift Shop", category.Description, "/categories/"+category.Slug),
		categoryDetailBreadcrumbs(category),
	)
}

func storyMetadata() seoMetadata {
	return metadataWithBreadcrumbs(metadataForPath(
		"Our Story | Thailand Gift Shop",
		"Learn how Thailand Gift Shop organizes Thai snacks, souvenirs, textiles, pantry items, decor, wellness, and small keepsakes for calm browsing.",
		"/story",
	), storyBreadcrumbs())
}

func cartMetadata() seoMetadata {
	return noindexMetadata(
		"Cart | Thailand Gift Shop",
		"Review quantities for your Thai gift-shop finds before checkout review.",
		"/cart",
	)
}

func checkoutMetadata() seoMetadata {
	return noindexMetadata(
		"Checkout Review | Thailand Gift Shop",
		"Confirm your current cart summary; no customer details, payment details, or order are collected here.",
		"/checkout",
	)
}

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
		return "Browse Thailand Gift Shop for Thai gift-shop finds and review-only checkout."
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
	urls := []sitemapURL{
		{Loc: canonicalURL("/")},
		{Loc: canonicalURL("/products")},
	}
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
	urls = append(urls, sitemapURL{Loc: canonicalURL("/story")})

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
	switch kind {
	case pageCart, pageCheckout:
		return map[string]string{
			"Cache-Control": privatePageCacheControl,
			"X-Robots-Tag":  "noindex, follow",
		}
	case pageRobotsTxt, pageSitemapXML:
		return map[string]string{"Cache-Control": seoDiscoveryCacheControl}
	}
	return nil
}
