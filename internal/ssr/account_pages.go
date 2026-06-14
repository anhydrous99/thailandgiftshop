package ssr

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/httpapi"
	"github.com/anhydrous99/thailandgiftshop/internal/payments"
	"github.com/aws/aws-lambda-go/events"
)

const recentAccountOrderLimit = 3
const maxAddressFieldLength = 120
const maxAddressPostalCodeLength = 20
const maxAddressPhoneLength = 32

const invalidAddressError = "Enter a US shipping address with a name, street, city, state, and ZIP code."
const addressLimitError = "You can save up to 10 addresses. Remove one to add another."
const addressConflictError = "This address changed in another window. Review and try again."

// addressCountryUS is fixed for v1; the form renders it read-only.
const addressCountryUS = "US"

const (
	addressFieldFullName   = "full_name"
	addressFieldLine1      = "line1"
	addressFieldLine2      = "line2"
	addressFieldCity       = "city"
	addressFieldRegion     = "region"
	addressFieldPostalCode = "postal_code"
	addressFieldPhone      = "phone"
)

type addressRegionOption struct {
	Code  string
	Label string
}

var addressRegionOptions = []addressRegionOption{
	{Code: "AL", Label: "Alabama"},
	{Code: "AK", Label: "Alaska"},
	{Code: "AZ", Label: "Arizona"},
	{Code: "AR", Label: "Arkansas"},
	{Code: "CA", Label: "California"},
	{Code: "CO", Label: "Colorado"},
	{Code: "CT", Label: "Connecticut"},
	{Code: "DE", Label: "Delaware"},
	{Code: "DC", Label: "District of Columbia"},
	{Code: "FL", Label: "Florida"},
	{Code: "GA", Label: "Georgia"},
	{Code: "HI", Label: "Hawaii"},
	{Code: "ID", Label: "Idaho"},
	{Code: "IL", Label: "Illinois"},
	{Code: "IN", Label: "Indiana"},
	{Code: "IA", Label: "Iowa"},
	{Code: "KS", Label: "Kansas"},
	{Code: "KY", Label: "Kentucky"},
	{Code: "LA", Label: "Louisiana"},
	{Code: "ME", Label: "Maine"},
	{Code: "MD", Label: "Maryland"},
	{Code: "MA", Label: "Massachusetts"},
	{Code: "MI", Label: "Michigan"},
	{Code: "MN", Label: "Minnesota"},
	{Code: "MS", Label: "Mississippi"},
	{Code: "MO", Label: "Missouri"},
	{Code: "MT", Label: "Montana"},
	{Code: "NE", Label: "Nebraska"},
	{Code: "NV", Label: "Nevada"},
	{Code: "NH", Label: "New Hampshire"},
	{Code: "NJ", Label: "New Jersey"},
	{Code: "NM", Label: "New Mexico"},
	{Code: "NY", Label: "New York"},
	{Code: "NC", Label: "North Carolina"},
	{Code: "ND", Label: "North Dakota"},
	{Code: "OH", Label: "Ohio"},
	{Code: "OK", Label: "Oklahoma"},
	{Code: "OR", Label: "Oregon"},
	{Code: "PA", Label: "Pennsylvania"},
	{Code: "RI", Label: "Rhode Island"},
	{Code: "SC", Label: "South Carolina"},
	{Code: "SD", Label: "South Dakota"},
	{Code: "TN", Label: "Tennessee"},
	{Code: "TX", Label: "Texas"},
	{Code: "UT", Label: "Utah"},
	{Code: "VT", Label: "Vermont"},
	{Code: "VA", Label: "Virginia"},
	{Code: "WA", Label: "Washington"},
	{Code: "WV", Label: "West Virginia"},
	{Code: "WI", Label: "Wisconsin"},
	{Code: "WY", Label: "Wyoming"},
}

var addressRegionLookup = func() map[string]string {
	lookup := make(map[string]string, len(addressRegionOptions)*2)
	for _, option := range addressRegionOptions {
		lookup[strings.ToUpper(option.Code)] = option.Code
		lookup[strings.ToLower(option.Label)] = option.Code
	}
	return lookup
}()

var orderStatusLabels = map[commerce.OrderStatus]string{
	commerce.OrderStatusPendingPayment: "Pending payment",
	commerce.OrderStatusPaid:           "Paid",
	commerce.OrderStatusShipped:        "Shipped",
	commerce.OrderStatusDelivered:      "Delivered",
	commerce.OrderStatusPaymentFailed:  "Payment failed",
	commerce.OrderStatusExpired:        "Expired",
	commerce.OrderStatusCanceled:       "Canceled",
	commerce.OrderStatusRefundPending:  "Refund pending",
	commerce.OrderStatusRefunded:       "Refunded",
	// Customer-honest label while the admin retries; the admin desk shows
	// "Refund failed".
	commerce.OrderStatusRefundFailed: "Refund delayed",
}

func orderStatusLabel(status commerce.OrderStatus) string {
	if label, found := orderStatusLabels[status]; found {
		return label
	}
	return string(status)
}

// accountPageState carries the per-request banners and errors the overview
// page renders on top of its loaded data.
type accountPageState struct {
	PasswordChanged bool
	PasswordError   string
}

func (h *Handler) renderAccountPage(ctx context.Context, request events.APIGatewayV2HTTPRequest, session commerce.Session, customer commerce.Customer, state accountPageState, statusCode int) events.APIGatewayV2HTTPResponse {
	csrfToken, cookies, err := h.customerCSRFForResponse(request, session)
	if err != nil {
		logAccountError("account: issue csrf token", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccount, nil)
	}
	addresses, err := h.commerce.ListAddresses(ctx, customer.ID)
	if err != nil {
		logAccountError("account: list addresses", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccount, nil)
	}
	page, err := h.commerce.ListOrdersByCustomer(ctx, customer.ID, recentAccountOrderLimit, commerce.OrderCursor{})
	if err != nil {
		logAccountError("account: list orders", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccount, nil)
	}

	vm := accountPageData{
		Metadata:        accountMetadata(),
		Breadcrumbs:     accountBreadcrumbs(),
		HeaderCartLabel: h.cartNavigation(request),
		CSRFToken:       csrfToken,
		Email:           customer.Email,
		PasswordChanged: state.PasswordChanged,
		PasswordError:   state.PasswordError,
		RecentOrders:    accountOrderViews(page.Orders),
	}
	if defaultAddress, found := findAddressByID(addresses, customer.DefaultAddressID); found {
		view := addressRowView(defaultAddress, customer.DefaultAddressID)
		vm.DefaultAddress = &view
	}

	var body bytes.Buffer
	if err := accountPage(vm).Render(ctx, &body); err != nil {
		logAccountError("account: render", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccount, nil)
	}

	return accountHTMLResponse(statusCode, maybeEmptyBody(httpapi.Method(request), body.String()), pageAccount, cookies)
}

func accountOrderViews(orders []commerce.Order) []accountOrderView {
	views := make([]accountOrderView, 0, len(orders))
	for _, order := range orders {
		placedAt := ""
		if !order.CreatedAt.IsZero() {
			placedAt = order.CreatedAt.UTC().Format("Jan 2, 2006")
		}
		views = append(views, accountOrderView{
			ID:          order.ID,
			StatusLabel: orderStatusLabel(order.Status),
			Total:       formatPrice(order.TotalCents),
			PlacedAt:    placedAt,
		})
	}
	return views
}

func findAddressByID(addresses []commerce.Address, id string) (commerce.Address, bool) {
	if id == "" {
		return commerce.Address{}, false
	}
	for _, address := range addresses {
		if address.ID == id {
			return address, true
		}
	}
	return commerce.Address{}, false
}

func addressRowView(address commerce.Address, defaultAddressID string) addressView {
	return addressView{
		ID:         address.ID,
		FullName:   address.FullName,
		Line1:      address.Line1,
		Line2:      address.Line2,
		City:       address.City,
		Region:     address.Region,
		PostalCode: address.PostalCode,
		Country:    address.Country,
		Phone:      address.Phone,
		IsDefault:  address.ID == defaultAddressID,
	}
}

// returnToFromRequest reads the post-action redirect target from the query
// string (GET renders) or the form body (POST re-renders). Re-rendering the
// address book after a validation round-trip is a POST with no query string,
// so without the body fallback the ?next=/checkout handoff would be lost and
// the shopper would land on /account/addresses instead of returning to
// checkout.
func returnToFromRequest(request events.APIGatewayV2HTTPRequest) string {
	if next := strings.TrimSpace(request.QueryStringParameters["next"]); next != "" {
		return next
	}
	if httpapi.Method(request) == http.MethodPost {
		if form, err := httpapi.FormValues(request); err == nil {
			return strings.TrimSpace(form.Get("next"))
		}
	}
	return ""
}

func (h *Handler) renderAddressesPage(ctx context.Context, request events.APIGatewayV2HTTPRequest, session commerce.Session, customer commerce.Customer, form addressFormData, statusCode int) events.APIGatewayV2HTTPResponse {
	csrfToken, cookies, err := h.customerCSRFForResponse(request, session)
	if err != nil {
		logAccountError("addresses: issue csrf token", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountAddresses, nil)
	}
	addresses, err := h.commerce.ListAddresses(ctx, customer.ID)
	if err != nil {
		logAccountError("addresses: list", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountAddresses, nil)
	}

	rows := make([]addressView, 0, len(addresses))
	for _, address := range addresses {
		rows = append(rows, addressRowView(address, customer.DefaultAddressID))
	}
	vm := addressesPageData{
		Metadata:        addressesMetadata(),
		Breadcrumbs:     addressesBreadcrumbs(),
		HeaderCartLabel: h.cartNavigation(request),
		CSRFToken:       csrfToken,
		Addresses:       rows,
		Form:            form,
		AtLimit:         len(addresses) >= commerce.MaxAddressesPerCustomer,
		Next:            sanitizedReturnTo(returnToFromRequest(request), ""),
	}

	var body bytes.Buffer
	if err := addressesPage(vm).Render(ctx, &body); err != nil {
		logAccountError("addresses: render", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountAddresses, nil)
	}

	return accountHTMLResponse(statusCode, maybeEmptyBody(httpapi.Method(request), body.String()), pageAccountAddresses, cookies)
}

func (h *Handler) handleAddressCreate(ctx context.Context, request events.APIGatewayV2HTTPRequest, session commerce.Session, customer commerce.Customer) events.APIGatewayV2HTTPResponse {
	form, err := httpapi.FormValues(request)
	if err != nil {
		return accountHTMLResponse(http.StatusBadRequest, "Bad request", pageAccountAddresses, nil)
	}
	formData, address, valid := parseAddressForm(form)
	if !valid {
		formData.ErrorMessage = invalidAddressError
		return h.renderAddressesPage(ctx, request, session, customer, formData, http.StatusBadRequest)
	}

	switch decision := h.resolveAddressValidation(ctx, form, address); decision.kind {
	case addressReject:
		formData.ErrorMessage = unverifiableAddressError
		return h.renderAddressesPage(ctx, request, session, customer, formData, http.StatusBadRequest)
	case addressConfirm:
		formData.Confirming = true
		formData.Suggestion = decision.suggestion
		return h.renderAddressesPage(ctx, request, session, customer, formData, http.StatusOK)
	default:
		address = decision.address
	}

	address.CustomerID = customer.ID
	if _, err := h.commerce.CreateAddress(ctx, address); err != nil {
		if errors.Is(err, commerce.ErrAddressLimit) {
			formData.ErrorMessage = addressLimitError
			return h.renderAddressesPage(ctx, request, session, customer, formData, http.StatusBadRequest)
		}
		logAccountError("addresses: create", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountAddresses, nil)
	}

	return accountSeeOther(sanitizedReturnTo(form.Get("next"), "/account/addresses"), pageAccountAddresses, nil)
}

func (h *Handler) handleAddressEditPage(ctx context.Context, request events.APIGatewayV2HTTPRequest, session commerce.Session, customer commerce.Customer, addressID string) events.APIGatewayV2HTTPResponse {
	address, found, err := h.commerce.GetAddress(ctx, customer.ID, addressID)
	if err != nil {
		logAccountError("address edit: load", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountAddressEdit, nil)
	}
	if !found {
		return h.accountNotFoundResponse(ctx, request, pageAccountAddressEdit)
	}

	return h.renderAddressEditPage(ctx, request, session, address.ID, address.Version, addressFormDataFromAddress(address), http.StatusOK)
}

func (h *Handler) renderAddressEditPage(ctx context.Context, request events.APIGatewayV2HTTPRequest, session commerce.Session, addressID string, version int, form addressFormData, statusCode int) events.APIGatewayV2HTTPResponse {
	csrfToken, cookies, err := h.customerCSRFForResponse(request, session)
	if err != nil {
		logAccountError("address edit: issue csrf token", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountAddressEdit, nil)
	}

	vm := addressEditPageData{
		Metadata:        addressEditMetadata(addressID),
		Breadcrumbs:     addressesBreadcrumbs(),
		HeaderCartLabel: h.cartNavigation(request),
		CSRFToken:       csrfToken,
		AddressID:       addressID,
		Version:         version,
		Form:            form,
	}
	var body bytes.Buffer
	if err := addressEditPage(vm).Render(ctx, &body); err != nil {
		logAccountError("address edit: render", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountAddressEdit, nil)
	}

	return accountHTMLResponse(statusCode, maybeEmptyBody(httpapi.Method(request), body.String()), pageAccountAddressEdit, cookies)
}

func (h *Handler) handleAddressUpdate(ctx context.Context, request events.APIGatewayV2HTTPRequest, session commerce.Session, customer commerce.Customer, addressID string) events.APIGatewayV2HTTPResponse {
	current, found, err := h.commerce.GetAddress(ctx, customer.ID, addressID)
	if err != nil {
		logAccountError("address update: load", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountAddressUpdate, nil)
	}
	if !found {
		return h.accountNotFoundResponse(ctx, request, pageAccountAddressUpdate)
	}

	form, err := httpapi.FormValues(request)
	if err != nil {
		return accountHTMLResponse(http.StatusBadRequest, "Bad request", pageAccountAddressUpdate, nil)
	}
	version, err := strconv.Atoi(strings.TrimSpace(form.Get("version")))
	if err != nil || version <= 0 {
		return accountHTMLResponse(http.StatusBadRequest, "Bad request", pageAccountAddressUpdate, nil)
	}
	formData, address, valid := parseAddressForm(form)
	if !valid {
		formData.ErrorMessage = invalidAddressError
		return h.renderAddressEditPage(ctx, request, session, addressID, version, formData, http.StatusBadRequest)
	}

	switch decision := h.resolveAddressValidation(ctx, form, address); decision.kind {
	case addressReject:
		formData.ErrorMessage = unverifiableAddressError
		return h.renderAddressEditPage(ctx, request, session, addressID, version, formData, http.StatusBadRequest)
	case addressConfirm:
		formData.Confirming = true
		formData.Suggestion = decision.suggestion
		return h.renderAddressEditPage(ctx, request, session, addressID, version, formData, http.StatusOK)
	default:
		address = decision.address
	}

	address.CustomerID = customer.ID
	address.ID = addressID
	if _, err := h.commerce.UpdateAddress(ctx, address, version); err != nil {
		if errors.Is(err, commerce.ErrVersionConflict) {
			formData.ErrorMessage = addressConflictError
			return h.renderAddressEditPage(ctx, request, session, addressID, current.Version, formData, http.StatusConflict)
		}
		logAccountError("address update: write", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountAddressUpdate, nil)
	}

	return accountSeeOther("/account/addresses", pageAccountAddressUpdate, nil)
}

func (h *Handler) handleAddressRemove(ctx context.Context, request events.APIGatewayV2HTTPRequest, customer commerce.Customer, addressID string) events.APIGatewayV2HTTPResponse {
	_ = request // kept for handler-signature symmetry with the other address routes
	if err := h.commerce.DeleteAddress(ctx, customer.ID, addressID); err != nil {
		logAccountError("address remove: delete", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountAddressRemove, nil)
	}
	if customer.DefaultAddressID == addressID {
		if err := h.setDefaultAddressWithRetry(ctx, customer, ""); err != nil {
			logAccountError("address remove: clear default", err)
			return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountAddressRemove, nil)
		}
	}

	return accountSeeOther("/account/addresses", pageAccountAddressRemove, nil)
}

func (h *Handler) handleAddressDefault(ctx context.Context, request events.APIGatewayV2HTTPRequest, customer commerce.Customer, addressID string) events.APIGatewayV2HTTPResponse {
	_, found, err := h.commerce.GetAddress(ctx, customer.ID, addressID)
	if err != nil {
		logAccountError("address default: load", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountAddressDefault, nil)
	}
	if !found {
		return h.accountNotFoundResponse(ctx, request, pageAccountAddressDefault)
	}
	if err := h.setDefaultAddressWithRetry(ctx, customer, addressID); err != nil {
		logAccountError("address default: write", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountAddressDefault, nil)
	}

	return accountSeeOther("/account/addresses", pageAccountAddressDefault, nil)
}

// setDefaultAddressWithRetry retries the versioned customer write once after
// a conflict with a freshly read version (the conflicting write was another
// tab of the same customer).
func (h *Handler) setDefaultAddressWithRetry(ctx context.Context, customer commerce.Customer, addressID string) error {
	err := h.commerce.SetDefaultAddress(ctx, customer.ID, addressID, customer.Version)
	if !errors.Is(err, commerce.ErrVersionConflict) {
		return err
	}

	fresh, found, err := h.commerce.GetCustomerByID(ctx, customer.ID)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("customer disappeared during default-address update")
	}
	return h.commerce.SetDefaultAddress(ctx, fresh.ID, addressID, fresh.Version)
}

func parseAddressForm(form url.Values) (addressFormData, commerce.Address, bool) {
	region, regionOK := normalizeAddressRegion(form.Get(addressFieldRegion))
	formData := addressFormData{
		FullName:   strings.TrimSpace(form.Get(addressFieldFullName)),
		Line1:      strings.TrimSpace(form.Get(addressFieldLine1)),
		Line2:      strings.TrimSpace(form.Get(addressFieldLine2)),
		City:       strings.TrimSpace(form.Get(addressFieldCity)),
		Region:     region,
		PostalCode: strings.TrimSpace(form.Get(addressFieldPostalCode)),
		Phone:      strings.TrimSpace(form.Get(addressFieldPhone)),
	}

	if formData.FullName == "" {
		addAddressFieldError(&formData, addressFieldFullName, "Enter the recipient's full name.")
	} else if len(formData.FullName) > maxAddressFieldLength {
		addAddressFieldError(&formData, addressFieldFullName, "Enter a full name with 120 characters or fewer.")
	}
	if formData.Line1 == "" {
		addAddressFieldError(&formData, addressFieldLine1, "Enter a street address.")
	} else if len(formData.Line1) > maxAddressFieldLength {
		addAddressFieldError(&formData, addressFieldLine1, "Enter address line 1 with 120 characters or fewer.")
	}
	if len(formData.Line2) > maxAddressFieldLength {
		addAddressFieldError(&formData, addressFieldLine2, "Enter address line 2 with 120 characters or fewer.")
	}
	if formData.City == "" {
		addAddressFieldError(&formData, addressFieldCity, "Enter a city.")
	} else if len(formData.City) > maxAddressFieldLength {
		addAddressFieldError(&formData, addressFieldCity, "Enter a city with 120 characters or fewer.")
	}
	if formData.Region == "" {
		addAddressFieldError(&formData, addressFieldRegion, "Choose a state.")
	} else if !regionOK {
		addAddressFieldError(&formData, addressFieldRegion, "Choose a valid state.")
	}
	if formData.PostalCode == "" {
		addAddressFieldError(&formData, addressFieldPostalCode, "Enter a ZIP code.")
	} else if len(formData.PostalCode) > maxAddressPostalCodeLength {
		addAddressFieldError(&formData, addressFieldPostalCode, "Enter a ZIP code with 20 characters or fewer.")
	}
	if len(formData.Phone) > maxAddressPhoneLength {
		addAddressFieldError(&formData, addressFieldPhone, "Enter a phone number with 32 characters or fewer.")
	}

	return formData, commerce.Address{
		FullName:   formData.FullName,
		Line1:      formData.Line1,
		Line2:      formData.Line2,
		City:       formData.City,
		Region:     formData.Region,
		PostalCode: formData.PostalCode,
		Country:    addressCountryUS,
		Phone:      formData.Phone,
	}, len(formData.FieldErrors) == 0
}

func addAddressFieldError(form *addressFormData, field string, message string) {
	if form.FieldErrors == nil {
		form.FieldErrors = map[string]string{}
	}
	if form.FieldErrors[field] == "" {
		form.FieldErrors[field] = message
	}
}

func validAddressRegion(region string) bool {
	_, ok := normalizeAddressRegion(region)
	return ok
}

func normalizeAddressRegion(region string) (string, bool) {
	trimmed := strings.TrimSpace(region)
	if trimmed == "" {
		return "", false
	}
	if code, ok := addressRegionLookup[strings.ToUpper(trimmed)]; ok {
		return code, true
	}
	if code, ok := addressRegionLookup[strings.ToLower(trimmed)]; ok {
		return code, true
	}
	return strings.ToUpper(trimmed), false
}

func addressFormDataFromAddress(address commerce.Address) addressFormData {
	region, _ := normalizeAddressRegion(address.Region)
	return addressFormData{
		FullName:   address.FullName,
		Line1:      address.Line1,
		Line2:      address.Line2,
		City:       address.City,
		Region:     region,
		PostalCode: address.PostalCode,
		Phone:      address.Phone,
	}
}

func (h *Handler) renderPaymentMethodsPage(ctx context.Context, request events.APIGatewayV2HTTPRequest, session commerce.Session, customer commerce.Customer, statusCode int) events.APIGatewayV2HTTPResponse {
	csrfToken, cookies, err := h.customerCSRFForResponse(request, session)
	if err != nil {
		logAccountError("payment methods: issue csrf token", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPaymentMethods, nil)
	}

	// The saved-card list is fetched live from the provider per render — we
	// persist no card data, only the opaque provider customer id.
	methods := []paymentMethodView(nil)
	if customer.StripeCustomerID != "" {
		if h.payments == nil {
			logAccountError("payment methods: list", errors.New("payments provider not configured"))
			return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPaymentMethods, nil)
		}
		listed, err := h.payments.ListPaymentMethods(ctx, customer.StripeCustomerID)
		if err != nil {
			logAccountError("payment methods: list", err)
			return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPaymentMethods, nil)
		}
		methods = paymentMethodViews(listed)
	}

	vm := paymentMethodsPageData{
		Metadata:        paymentMethodsMetadata(),
		Breadcrumbs:     paymentMethodsBreadcrumbs(),
		HeaderCartLabel: h.cartNavigation(request),
		CSRFToken:       csrfToken,
		Methods:         methods,
		Saved:           request.QueryStringParameters["saved"] == "1",
	}
	var body bytes.Buffer
	if err := paymentMethodsPage(vm).Render(ctx, &body); err != nil {
		logAccountError("payment methods: render", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPaymentMethods, nil)
	}

	return accountHTMLResponse(statusCode, maybeEmptyBody(httpapi.Method(request), body.String()), pageAccountPaymentMethods, cookies)
}

func paymentMethodViews(methods []payments.PaymentMethod) []paymentMethodView {
	views := make([]paymentMethodView, 0, len(methods))
	for _, method := range methods {
		expiry := ""
		if method.ExpMonth > 0 && method.ExpYear > 0 {
			expiry = strconv.Itoa(method.ExpMonth) + "/" + strconv.Itoa(method.ExpYear)
		}
		views = append(views, paymentMethodView{
			ID:     method.ID,
			Brand:  brandDisplayName(method.Brand),
			Last4:  method.Last4,
			Expiry: expiry,
		})
	}
	return views
}

func brandDisplayName(brand string) string {
	if brand == "" {
		return "Card"
	}
	return strings.ToUpper(brand[:1]) + brand[1:]
}

func (h *Handler) handlePaymentMethodAdd(ctx context.Context, customer commerce.Customer) events.APIGatewayV2HTTPResponse {
	if h.payments == nil {
		logAccountError("payment method add", errors.New("payments provider not configured"))
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPaymentMethodAdd, nil)
	}

	stripeCustomerID := customer.StripeCustomerID
	if stripeCustomerID == "" {
		ensured, err := h.payments.EnsureCustomer(ctx, customer.ID, customer.Email)
		if err != nil {
			logAccountError("payment method add: ensure customer", err)
			return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPaymentMethodAdd, nil)
		}
		// The conditional write resolves races to one winner; use whichever
		// id the store kept.
		winner, err := h.commerce.SetStripeCustomerID(ctx, customer.ID, ensured)
		if err != nil {
			logAccountError("payment method add: persist stripe customer", err)
			return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPaymentMethodAdd, nil)
		}
		stripeCustomerID = winner.StripeCustomerID
	}

	baseURL := h.publicBaseURL()
	session, err := h.payments.CreateSetupSession(ctx, payments.SetupSessionInput{
		CustomerID:       customer.ID,
		StripeCustomerID: stripeCustomerID,
		SuccessURL:       baseURL + "/account/payment-methods?saved=1",
		CancelURL:        baseURL + "/account/payment-methods",
	})
	if err != nil {
		logAccountError("payment method add: create setup session", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPaymentMethodAdd, nil)
	}

	return accountSeeOther(session.URL, pageAccountPaymentMethodAdd, nil)
}

func (h *Handler) handlePaymentMethodRemove(ctx context.Context, customer commerce.Customer, paymentMethodID string) events.APIGatewayV2HTTPResponse {
	// Not-found (never 403) for missing provider customers and foreign or
	// unknown payment methods alike: the IDOR guard must not leak existence.
	if customer.StripeCustomerID == "" || h.payments == nil {
		return accountHTMLResponse(http.StatusNotFound, "Not found", pageAccountPaymentMethodRemove, nil)
	}
	if err := h.payments.DetachPaymentMethod(ctx, customer.StripeCustomerID, paymentMethodID); err != nil {
		if errors.Is(err, payments.ErrPaymentMethodNotFound) {
			return accountHTMLResponse(http.StatusNotFound, "Not found", pageAccountPaymentMethodRemove, nil)
		}
		logAccountError("payment method remove: detach", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPaymentMethodRemove, nil)
	}

	return accountSeeOther("/account/payment-methods", pageAccountPaymentMethodRemove, nil)
}

// publicBaseURL is the absolute origin used for provider redirect URLs,
// sourced from the checkout service wiring and falling back to the
// environment default.
func (h *Handler) publicBaseURL() string {
	if h.checkout != nil {
		if baseURL := strings.TrimSpace(h.checkout.BaseURL); baseURL != "" {
			return strings.TrimSuffix(baseURL, "/")
		}
	}
	return payments.PublicBaseURLFromEnvironment()
}
