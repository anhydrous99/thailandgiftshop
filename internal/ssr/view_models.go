package ssr

import "github.com/anhydrous99/thailandgiftshop/internal/catalog"

var (
	_ = productListingPageViewModel{}
	_ = productDetailPageViewModel{}
	_ = categoryIndexPageViewModel{}
	_ = categoryDetailPageViewModel{}
	_ = storyPageViewModel{}
)

type homePageViewModel struct {
	Products                   []catalog.Product
	Categories                 []catalog.Category
	ProductImagePlaceholderURL string
}

type productListingPageViewModel struct {
	Products                   []catalog.Product
	Categories                 []catalog.Category
	ProductImagePlaceholderURL string
}

type productDetailPageViewModel struct {
	Product                    catalog.Product
	ProductImagePlaceholderURL string
}

type categoryIndexPageViewModel struct {
	Categories []catalog.Category
}

type categoryDetailPageViewModel struct {
	Category                   catalog.Category
	Categories                 []catalog.Category
	Products                   []catalog.Product
	ProductImagePlaceholderURL string
}

type storyPageViewModel struct{}
