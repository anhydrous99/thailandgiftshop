package ssr

import "github.com/anhydrous99/thailandgiftshop/internal/catalog"

var (
	_ = productListingPageViewModel{}
	_ = productDetailPageViewModel{}
	_ = categoryIndexPageViewModel{}
	_ = categoryDetailPageViewModel{}
	_ = storyPageViewModel{}
	_ = cartPageViewModel{}
	_ = checkoutPageViewModel{}
)

type homePageViewModel struct {
	Metadata                   seoMetadata
	Products                   []catalog.Product
	Categories                 []catalog.Category
	FeaturedCategories         []catalog.Category
	ProductImagePlaceholderURL string
	HeaderCartLabel            string
}

type productListingPageViewModel struct {
	Metadata                   seoMetadata
	Breadcrumbs                []breadcrumbItem
	Products                   []catalog.Product
	Categories                 []catalog.Category
	ProductImagePlaceholderURL string
	HeaderCartLabel            string
}

type productDetailPageViewModel struct {
	Metadata                   seoMetadata
	Breadcrumbs                []breadcrumbItem
	Product                    catalog.Product
	Categories                 []catalog.Category
	ProductImagePlaceholderURL string
	HeaderCartLabel            string
}

type categoryIndexPageViewModel struct {
	Metadata        seoMetadata
	Breadcrumbs     []breadcrumbItem
	Categories      []catalog.Category
	HeaderCartLabel string
}

type categoryDetailPageViewModel struct {
	Metadata                   seoMetadata
	Breadcrumbs                []breadcrumbItem
	Category                   catalog.Category
	Categories                 []catalog.Category
	Products                   []catalog.Product
	RelatedCategories          []catalog.Category
	ProductImagePlaceholderURL string
	HeaderCartLabel            string
}

type storyPageViewModel struct {
	Metadata        seoMetadata
	Breadcrumbs     []breadcrumbItem
	HeaderCartLabel string
}

type cartPageViewModel struct {
	Metadata                   seoMetadata
	Lines                      []cartLineView
	ProductImagePlaceholderURL string
	HeaderCartLabel            string
}

type checkoutPageViewModel struct {
	Metadata                   seoMetadata
	Lines                      []cartLineView
	ProductImagePlaceholderURL string
	HeaderCartLabel            string
}
