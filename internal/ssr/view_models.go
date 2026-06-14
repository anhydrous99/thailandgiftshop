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
	_ = passwordResetRequestPageData{}
	_ = passwordResetConfirmPageData{}
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

// policyPageViewModel drives the shared static content page used for shipping,
// returns, privacy, terms, and contact. Draft marks legal copy still pending
// review; ContactEmail, when set, renders a prominent mailto link.
type policyPageViewModel struct {
	Metadata        seoMetadata
	Breadcrumbs     []breadcrumbItem
	HeaderCartLabel string
	Eyebrow         string
	Title           string
	Intro           string
	Draft           bool
	ContactEmail    string
	Sections        []policySection
	UpdatedNote     string
}

type policySection struct {
	Heading    string
	Paragraphs []string
}

type cartPageViewModel struct {
	Metadata                   seoMetadata
	Lines                      []cartLineView
	ProductImagePlaceholderURL string
	HeaderCartLabel            string
	// CartAdjusted shows the auto-update notice when normalization removed
	// out-of-stock lines or reduced quantities on this read.
	CartAdjusted bool
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
	// AddressForm carries the guest layout's address entry values (re-render
	// preservation after a failed POST).
	AddressForm  addressFormData
	Subtotal     string
	Shipping     string
	Tax          string
	Total        string
	Canceled     bool
	ErrorMessage string
	// Guest switches the layout to the account-less checkout: email + address
	// entry on one form, posted with the guest_csrf_token field.
	Guest      bool
	GuestEmail string
}

type signInPageData struct {
	Metadata        seoMetadata
	Breadcrumbs     []breadcrumbItem
	HeaderCartLabel string
	GuestCSRFToken  string
	Email           string
	ReturnTo        string
	ErrorMessage    string
	SuccessMessage  string
}

type signUpPageData struct {
	Metadata        seoMetadata
	Breadcrumbs     []breadcrumbItem
	HeaderCartLabel string
	GuestCSRFToken  string
	Email           string
	ReturnTo        string
	ErrorMessage    string
	EmailError      string
	PasswordError   string
}

type passwordResetRequestPageData struct {
	Metadata        seoMetadata
	HeaderCartLabel string
	GuestCSRFToken  string
	Email           string
	Sent            bool
	ErrorMessage    string
	EmailError      string
}

type passwordResetConfirmPageData struct {
	Metadata        seoMetadata
	HeaderCartLabel string
	GuestCSRFToken  string
	Token           string
	ErrorMessage    string
	PasswordError   string
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
	FieldErrors  map[string]string
	// Confirming and Suggestion drive the address-validation "suggest &
	// confirm" panel: when Confirming is true the form re-renders with the
	// standardized Suggestion alongside the shopper's entry so they can pick
	// which one to use.
	Confirming bool
	Suggestion *addressSuggestionView
}

func (f addressFormData) FieldError(field string) string {
	if f.FieldErrors == nil {
		return ""
	}
	return f.FieldErrors[field]
}

// addressSuggestionView is the standardized address Amazon Location Service
// proposed for a corrected entry. Label is the one-line display form; the
// component fields are echoed as hidden inputs so accepting the suggestion
// needs no second geocode call.
type addressSuggestionView struct {
	Label      string
	Line1      string
	Line2      string
	City       string
	Region     string
	PostalCode string
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
	// NextCursor is the URL-ready signed ?after token for the next-older
	// page (already query-escaped); empty when this is the last page.
	NextCursor string
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
	// Guest marks the tokenized read-only view: no Back-to-orders link, the
	// save-this-link notice, and the sign-up upsell on the placed banner.
	Guest bool
	// GuestAccessURL echoes the presented access token as a self-link; it is
	// never minted on render (minting is the confirm branch's job alone).
	GuestAccessURL string
}

type fakePayPageData struct {
	Metadata        seoMetadata
	HeaderCartLabel string
	CSRFToken       string
	SessionID       string
	SetupMode       bool
	Total           string
	// Guest switches the hidden CSRF field name to guest_csrf_token and hides
	// the save-card checkbox (guests have no provider customer to save to).
	Guest bool
}

// checkoutProcessingPageData renders the no-JS payment-processing interstitial
// with a manual confirm retry link while the webhook or reconcile lands.
type checkoutProcessingPageData struct {
	Metadata        seoMetadata
	HeaderCartLabel string
	OrderID         string
	RefreshURL      string
}
