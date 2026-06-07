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
	Products                   []catalog.Product
	Categories                 []catalog.Category
	FeaturedCategories         []catalog.Category
	ProductImagePlaceholderURL string
	HeaderCartLabel            string
}

type productListingPageViewModel struct {
	Products                   []catalog.Product
	Categories                 []catalog.Category
	ProductImagePlaceholderURL string
	HeaderCartLabel            string
}

type productDetailPageViewModel struct {
	Product                    catalog.Product
	Categories                 []catalog.Category
	ProductImagePlaceholderURL string
	HeaderCartLabel            string
}

type categoryIndexPageViewModel struct {
	Categories      []catalog.Category
	HeaderCartLabel string
}

type categoryDetailPageViewModel struct {
	Category                   catalog.Category
	Categories                 []catalog.Category
	Products                   []catalog.Product
	ProductImagePlaceholderURL string
	HeaderCartLabel            string
}

type storyPageViewModel struct {
	HeaderCartLabel string
}

type cartPageViewModel struct {
	Lines                      []cartLineView
	ProductImagePlaceholderURL string
	HeaderCartLabel            string
}

type checkoutPageViewModel struct {
	Lines                      []cartLineView
	ProductImagePlaceholderURL string
	HeaderCartLabel            string
}
