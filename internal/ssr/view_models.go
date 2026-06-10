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
	_ = signInPageData{}
	_ = signUpPageData{}
	_ = accountPageData{}
	_ = addressesPageData{}
	_ = addressEditPageData{}
	_ = paymentMethodsPageData{}
	_ = ordersPageData{}
	_ = orderDetailPageData{}
	_ = fakePayPageData{}
	_ = checkoutProcessingPageData{}
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

// checkoutLineView is one priced order line on the checkout page. Strings are
// derived from the live cart at render time; Notice carries the per-line
// insufficient-stock feedback after a failed reservation.
type checkoutLineView struct {
	Name         string
	Slug         string
	VariantLabel string
	ImageURL     string
	Quantity     int
	UnitPrice    string
	LineTotal    string
	Notice       string
}

type checkoutPageViewModel struct {
	Metadata        seoMetadata
	Breadcrumbs     []breadcrumbItem
	HeaderCartLabel string
	CSRFToken       string
	Lines           []checkoutLineView
	Addresses       []addressView
	SelectedAddress string
	AddressForm     addressFormData
	Subtotal        string
	Total           string
	Canceled        bool
	ErrorMessage    string
}

type signInPageData struct {
	Metadata        seoMetadata
	Breadcrumbs     []breadcrumbItem
	HeaderCartLabel string
	GuestCSRFToken  string
	Email           string
	ReturnTo        string
	ErrorMessage    string
}

type signUpPageData struct {
	Metadata        seoMetadata
	Breadcrumbs     []breadcrumbItem
	HeaderCartLabel string
	GuestCSRFToken  string
	Email           string
	ReturnTo        string
	ErrorMessage    string
}

// accountOrderView is a frozen snapshot for the overview's recent-orders
// list: strings and cents copied from the order record, never live catalog
// data.
type accountOrderView struct {
	ID          string
	StatusLabel string
	Total       string
	PlacedAt    string
}

type accountPageData struct {
	Metadata        seoMetadata
	Breadcrumbs     []breadcrumbItem
	HeaderCartLabel string
	CSRFToken       string
	Email           string
	DefaultAddress  *addressView
	RecentOrders    []accountOrderView
	PasswordChanged bool
	PasswordError   string
}

type addressView struct {
	ID         string
	FullName   string
	Line1      string
	Line2      string
	City       string
	Region     string
	PostalCode string
	Country    string
	Phone      string
	IsDefault  bool
}

// addressFormData carries submitted values back into a re-rendered form so a
// validation error never wipes the shopper's input.
type addressFormData struct {
	FullName     string
	Line1        string
	Line2        string
	City         string
	Region       string
	PostalCode   string
	Phone        string
	ErrorMessage string
}

type addressesPageData struct {
	Metadata        seoMetadata
	Breadcrumbs     []breadcrumbItem
	HeaderCartLabel string
	CSRFToken       string
	Addresses       []addressView
	Form            addressFormData
	AtLimit         bool
	Next            string
}

type addressEditPageData struct {
	Metadata        seoMetadata
	Breadcrumbs     []breadcrumbItem
	HeaderCartLabel string
	CSRFToken       string
	AddressID       string
	Version         int
	Form            addressFormData
}

type paymentMethodView struct {
	ID     string
	Brand  string
	Last4  string
	Expiry string
}

type paymentMethodsPageData struct {
	Metadata        seoMetadata
	Breadcrumbs     []breadcrumbItem
	HeaderCartLabel string
	CSRFToken       string
	Methods         []paymentMethodView
	Saved           bool
}

// orderRowView is a frozen snapshot row for the order history list: strings
// and cents copied from the order record, never live catalog data.
type orderRowView struct {
	ID          string
	StatusLabel string
	PlacedAt    string
	Total       string
	ItemCount   int
}

type ordersPageData struct {
	Metadata        seoMetadata
	Breadcrumbs     []breadcrumbItem
	HeaderCartLabel string
	Orders          []orderRowView
	NextCursor      string
}

// orderLineView renders one frozen order-snapshot line: every field was
// copied from commerce.OrderLine at purchase time, never from a live
// catalog.Product.
type orderLineView struct {
	Name         string
	VariantLabel string
	ImageURL     string
	Quantity     int
	UnitPrice    string
	LineTotal    string
}

type orderTimelineStep struct {
	StatusLabel string
	At          string
	Actor       string
}

type orderAddressView struct {
	FullName   string
	Line1      string
	Line2      string
	City       string
	Region     string
	PostalCode string
	Country    string
	Phone      string
}

type orderDetailPageData struct {
	Metadata        seoMetadata
	Breadcrumbs     []breadcrumbItem
	HeaderCartLabel string
	Placed          bool
	OrderID         string
	StatusLabel     string
	PlacedAt        string
	Lines           []orderLineView
	Subtotal        string
	Shipping        string
	Tax             string
	Total           string
	Address         orderAddressView
	CardBrand       string
	CardLast4       string
	TrackingCarrier string
	TrackingNumber  string
	Timeline        []orderTimelineStep
}

type fakePayPageData struct {
	Metadata        seoMetadata
	HeaderCartLabel string
	CSRFToken       string
	SessionID       string
	SetupMode       bool
	Total           string
}

// checkoutProcessingPageData renders the no-JS payment-processing interstitial
// that meta-refreshes the confirm URL until the webhook or reconcile lands.
type checkoutProcessingPageData struct {
	Metadata        seoMetadata
	HeaderCartLabel string
	OrderID         string
}
