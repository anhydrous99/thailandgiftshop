package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/aws/aws-lambda-go/events"
)

const adminProductAllowedMethods = http.MethodGet + ", " + http.MethodHead + ", " + http.MethodPost
const confirmedProductImageTokenTTL = 30 * time.Minute

var signedProductImagePurpose = []byte("tgs-admin-product-image")

type confirmedProductImagePayload struct {
	Version      int    `json:"version"`
	URL          string `json:"url"`
	SessionNonce string `json:"session_nonce"`
	ExpiresAt    int64  `json:"expires_at"`
}

type adminProductListViewModel struct {
	CSRFValue string
	Products  []catalog.Product
	Filters   adminProductFilters
	Flash     string
}

type adminProductFormViewModel struct {
	CSRFValue  string
	Mode       string
	Action     string
	Title      string
	SubmitText string
	Product    catalog.Product
	ImageToken string
	Categories []catalog.Category
	Errors     []string
	Flash      string
}

type adminProductFilters struct {
	Status   string
	Category string
	Search   string
}

func isAdminProductPath(path string) bool {
	if path == "/admin/products" || path == "/admin/products/new" {
		return true
	}
	return strings.HasPrefix(path, "/admin/products/")
}

func (h *Handler) handleAdminProducts(ctx context.Context, path string, request events.APIGatewayV2HTTPRequest, session adminSession) events.APIGatewayV2HTTPResponse {
	method := requestMethod(request)
	csrfValue, cookies, err := h.csrfForProtectedResponse(request, session)
	if err != nil {
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	store := h.catalogStore()

	if path == "/admin/products" {
		if method == http.MethodGet || method == http.MethodHead {
			products, err := store.ListProducts(ctx)
			if err != nil {
				return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
			}
			body, err := renderAdminProductList(ctx, adminProductListViewModel{CSRFValue: csrfValue, Products: filterAdminProducts(products, productFilters(request)), Filters: productFilters(request), Flash: request.QueryStringParameters["saved"]})
			if err != nil {
				return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
			}
			if method == http.MethodHead {
				body = ""
			}
			return adminHTMLResponse(http.StatusOK, body, nil, cookies)
		}
		if method == http.MethodPost {
			return h.createProduct(ctx, request, session, csrfValue, cookies)
		}
	}

	if path == "/admin/products/new" {
		if method != http.MethodGet && method != http.MethodHead {
			return adminHTMLResponse(http.StatusMethodNotAllowed, "Method not allowed", map[string]string{"Allow": adminProductAllowedMethods}, nil)
		}
		body, err := h.productForm(ctx, csrfValue, newProductFormViewModel(csrfValue, nil))
		if err != nil {
			return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
		}
		if method == http.MethodHead {
			body = ""
		}
		return adminHTMLResponse(http.StatusOK, body, nil, cookies)
	}

	productID, action, ok := adminProductIDAction(path)
	if !ok {
		return adminHTMLResponse(http.StatusNotFound, "Not found", nil, nil)
	}
	product, found, err := store.GetProductByID(ctx, productID)
	if err != nil {
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	if !found {
		return adminHTMLResponse(http.StatusNotFound, "Not found", nil, nil)
	}

	if action == "edit" {
		if method == http.MethodGet || method == http.MethodHead {
			vm := editProductFormViewModel(csrfValue, product, nil, request.QueryStringParameters["saved"])
			body, err := h.productForm(ctx, csrfValue, vm)
			if err != nil {
				return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
			}
			if method == http.MethodHead {
				body = ""
			}
			return adminHTMLResponse(http.StatusOK, body, nil, cookies)
		}
		if method == http.MethodPost {
			return h.updateProduct(ctx, request, session, product, csrfValue, cookies)
		}
	}
	if action == "archive" && method == http.MethodPost {
		return h.archiveProduct(ctx, product)
	}

	return adminHTMLResponse(http.StatusMethodNotAllowed, "Method not allowed", map[string]string{"Allow": adminProductAllowedMethods}, nil)
}

func adminProductIDAction(path string) (string, string, bool) {
	remainder, ok := strings.CutPrefix(path, "/admin/products/")
	if !ok {
		return "", "", false
	}
	productID, action, ok := strings.Cut(remainder, "/")
	if !ok || productID == "" || strings.Contains(action, "/") {
		return "", "", false
	}
	return productID, action, action == "edit" || action == "archive"
}

func (h *Handler) catalogStore() catalog.AdminStore {
	if h.catalog == nil {
		h.catalog = catalog.NewMemoryStore(nil, nil)
	}
	return h.catalog
}

func (h *Handler) createProduct(ctx context.Context, request events.APIGatewayV2HTTPRequest, session adminSession, csrfValue string, cookies []string) events.APIGatewayV2HTTPResponse {
	values, err := formValues(request)
	if err != nil {
		h.recordCatalogWrite(metricEntityProduct, metricOperationCreate, metricOutcomeValidationError)
		return adminHTMLResponse(http.StatusBadRequest, "Invalid form", nil, nil)
	}
	product, imageToken, errorsList := productFromForm(values, catalog.Product{}, session, h.credentials.SessionSecret, h.currentTime(), true)
	if len(errorsList) > 0 {
		h.recordCatalogWrite(metricEntityProduct, metricOperationCreate, metricOutcomeValidationError)
		return h.productFormResponse(ctx, csrfValue, cookies, newProductFormViewModel(csrfValue, errorsList).withProduct(product, imageToken), http.StatusBadRequest)
	}
	written, err := h.catalogStore().CreateProduct(ctx, product)
	if err != nil {
		h.recordCatalogWrite(metricEntityProduct, metricOperationCreate, catalogWriteOutcome(err))
		return h.productWriteError(ctx, csrfValue, cookies, newProductFormViewModel(csrfValue, nil).withProduct(product, imageToken), err)
	}
	h.recordCatalogWrite(metricEntityProduct, metricOperationCreate, metricOutcomeSuccess)
	return adminRedirectResponse(http.StatusSeeOther, "/admin/products/"+url.PathEscape(written.ID)+"/edit?saved=created", nil)
}

func (h *Handler) updateProduct(ctx context.Context, request events.APIGatewayV2HTTPRequest, session adminSession, previous catalog.Product, csrfValue string, cookies []string) events.APIGatewayV2HTTPResponse {
	values, err := formValues(request)
	if err != nil {
		h.recordCatalogWrite(metricEntityProduct, metricOperationUpdate, metricOutcomeValidationError)
		return adminHTMLResponse(http.StatusBadRequest, "Invalid form", nil, nil)
	}
	if strconv.Itoa(previous.Version) != strings.TrimSpace(values.Get("version")) {
		h.recordCatalogWrite(metricEntityProduct, metricOperationUpdate, metricOutcomeConflict)
		vm := editProductFormViewModel(csrfValue, previous, []string{"This product changed while you were editing. Reload and try again."}, "")
		return h.productFormResponse(ctx, csrfValue, cookies, vm, http.StatusConflict)
	}
	product, imageToken, errorsList := productFromForm(values, previous, session, h.credentials.SessionSecret, h.currentTime(), false)
	if len(errorsList) > 0 {
		h.recordCatalogWrite(metricEntityProduct, metricOperationUpdate, metricOutcomeValidationError)
		vm := editProductFormViewModel(csrfValue, product, errorsList, "")
		vm.ImageToken = imageToken
		return h.productFormResponse(ctx, csrfValue, cookies, vm, http.StatusBadRequest)
	}
	written, err := h.catalogStore().UpdateProduct(ctx, previous, product)
	if err != nil {
		h.recordCatalogWrite(metricEntityProduct, metricOperationUpdate, catalogWriteOutcome(err))
		vm := editProductFormViewModel(csrfValue, product, nil, "")
		vm.ImageToken = imageToken
		return h.productWriteError(ctx, csrfValue, cookies, vm, err)
	}
	h.recordCatalogWrite(metricEntityProduct, metricOperationUpdate, metricOutcomeSuccess)
	return adminRedirectResponse(http.StatusSeeOther, "/admin/products/"+url.PathEscape(written.ID)+"/edit?saved=updated", nil)
}

func (h *Handler) archiveProduct(ctx context.Context, product catalog.Product) events.APIGatewayV2HTTPResponse {
	_, err := h.catalogStore().ArchiveProduct(ctx, product)
	if err != nil {
		h.recordCatalogWrite(metricEntityProduct, metricOperationArchive, catalogWriteOutcome(err))
		if errors.Is(err, catalog.ErrVersionConflict) {
			return adminHTMLResponse(http.StatusConflict, "Version conflict", nil, nil)
		}
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	h.recordCatalogWrite(metricEntityProduct, metricOperationArchive, metricOutcomeSuccess)
	return adminRedirectResponse(http.StatusSeeOther, "/admin/products?status=archived&saved=archived", nil)
}

func (h *Handler) productWriteError(ctx context.Context, csrfValue string, cookies []string, vm adminProductFormViewModel, err error) events.APIGatewayV2HTTPResponse {
	status := http.StatusBadRequest
	message := "Product could not be saved."
	if errors.Is(err, catalog.ErrSlugConflict) {
		message = "Slug is already used by another product."
	} else if errors.Is(err, catalog.ErrVersionConflict) {
		status = http.StatusConflict
		message = "This product changed while you were editing. Reload and try again."
	} else if strings.Contains(err.Error(), "variant") {
		message = err.Error()
	} else {
		status = http.StatusInternalServerError
		message = "Internal server error"
	}
	vm.Errors = append(vm.Errors, message)
	return h.productFormResponse(ctx, csrfValue, cookies, vm, status)
}

func (h *Handler) productFormResponse(ctx context.Context, csrfValue string, cookies []string, vm adminProductFormViewModel, status int) events.APIGatewayV2HTTPResponse {
	body, err := h.productForm(ctx, csrfValue, vm)
	if err != nil {
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	return adminHTMLResponse(status, body, nil, cookies)
}

func (h *Handler) productForm(ctx context.Context, csrfValue string, vm adminProductFormViewModel) (string, error) {
	categories, err := h.catalogStore().ListCategories(ctx)
	if err != nil {
		return "", err
	}
	vm.CSRFValue = csrfValue
	vm.Categories = categories
	return renderAdminProductForm(ctx, vm)
}

func productFromForm(values url.Values, previous catalog.Product, session adminSession, secret string, now time.Time, create bool) (catalog.Product, string, []string) {
	product := previous
	imageToken := strings.TrimSpace(values.Get("image_token"))
	if create {
		id, err := randomUUID()
		if err != nil {
			return product, imageToken, []string{"Could not create product ID."}
		}
		product = catalog.Product{ID: "prod_" + strings.ReplaceAll(id, "-", ""), CreatedAt: now.UTC(), Version: 0}
	}
	product.Name = strings.TrimSpace(values.Get("name"))
	product.Slug = strings.TrimSpace(values.Get("slug"))
	product.Description = strings.TrimSpace(values.Get("description"))
	product.ImageURL = strings.TrimSpace(values.Get("image_url"))
	product.Status = catalog.Status(strings.TrimSpace(values.Get("status")))
	product.CategorySlugs = cleanStrings(values["category_slugs"])
	product.UpdatedAt = now.UTC()
	product.PriceCents = centsFromDollars(values.Get("price"))
	product.SortOrder = intFromForm(values.Get("sort_order"))
	product.StockQuantity = intFromForm(values.Get("stock_quantity"))
	product.Variants = variantsFromForm(values)

	errorsList := validateProductForm(product, previous, imageToken, session, secret, now, create)
	return product, imageToken, errorsList
}

func validateProductForm(product catalog.Product, previous catalog.Product, imageToken string, session adminSession, secret string, now time.Time, create bool) []string {
	_ = create
	var errorsList []string
	if product.Name == "" {
		errorsList = append(errorsList, "Name is required.")
	}
	if product.Slug == "" {
		errorsList = append(errorsList, "Slug is required.")
	} else if !catalog.ValidRouteSlug(product.Slug) {
		errorsList = append(errorsList, "Slug must use only lowercase letters, numbers, and hyphens.")
	}
	if product.PriceCents <= 0 {
		errorsList = append(errorsList, "Price must be greater than zero.")
	}
	if product.Status != catalog.StatusActive && product.Status != catalog.StatusDraft && product.Status != catalog.StatusArchived {
		errorsList = append(errorsList, "Choose active, draft, or archived status.")
	}
	if product.ImageURL != "" && product.ImageURL != previous.ImageURL && !validConfirmedProductImageToken(imageToken, product.ImageURL, session, secret, now) {
		errorsList = append(errorsList, "Upload and confirm a product image before saving.")
	}
	if err := product.Validate(); err != nil {
		errorsList = append(errorsList, err.Error())
	}
	return errorsList
}

func variantsFromForm(values url.Values) []catalog.ProductVariant {
	labels := values["variant_label"]
	stocks := values["variant_stock"]
	statuses := values["variant_status"]
	ids := values["variant_id"]
	variants := make([]catalog.ProductVariant, 0, len(labels))
	for i, labelValue := range labels {
		label := strings.TrimSpace(labelValue)
		stock := intAt(stocks, i)
		status := catalog.Status(valueAt(statuses, i, string(catalog.StatusActive)))
		id := strings.TrimSpace(valueAt(ids, i, ""))
		if label == "" && stock == 0 && id == "" {
			continue
		}
		if id == "" {
			id = variantID(label, i)
		}
		variants = append(variants, catalog.ProductVariant{ID: id, Label: label, StockQuantity: stock, Status: status, SortOrder: (i + 1) * 10})
	}
	return variants
}

func variantID(label string, index int) string {
	clean := strings.ToLower(strings.TrimSpace(label))
	clean = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, clean)
	clean = strings.Trim(clean, "-")
	if clean == "" {
		clean = fmt.Sprintf("variant-%d", index+1)
	}
	return "var_" + clean
}

func newProductFormViewModel(csrfValue string, errorsList []string) adminProductFormViewModel {
	return adminProductFormViewModel{CSRFValue: csrfValue, Mode: "new", Action: "/admin/products", Title: "New product", SubmitText: "Create product", Product: catalog.Product{Status: catalog.StatusDraft, Variants: defaultVariantRows()}, Errors: errorsList}
}

func editProductFormViewModel(csrfValue string, product catalog.Product, errorsList []string, flash string) adminProductFormViewModel {
	if len(product.Variants) == 0 {
		product.Variants = defaultVariantRows()
	}
	return adminProductFormViewModel{CSRFValue: csrfValue, Mode: "edit", Action: "/admin/products/" + url.PathEscape(product.ID) + "/edit", Title: "Edit product", SubmitText: "Save product", Product: product, Errors: errorsList, Flash: flash}
}

func (vm adminProductFormViewModel) withProduct(product catalog.Product, imageToken string) adminProductFormViewModel {
	vm.Product = product
	vm.ImageToken = imageToken
	if vm.Mode == "" || vm.Mode == "new" {
		vm.Mode = "new"
		vm.Action = "/admin/products"
		vm.Title = "New product"
		vm.SubmitText = "Create product"
	} else {
		vm.Action = "/admin/products/" + url.PathEscape(product.ID) + "/edit"
	}
	if len(vm.Product.Variants) == 0 {
		vm.Product.Variants = defaultVariantRows()
	}
	return vm
}

func newConfirmedProductImageToken(imageURL string, session adminSession, secret string, now time.Time) (string, error) {
	if !strings.HasPrefix(imageURL, "/images/products/uploads/") || session.Nonce == "" || secret == "" {
		return "", errInvalidProductImageUpload
	}
	return encodeSignedValue(confirmedProductImagePayload{
		Version:      signedValueVersion,
		URL:          imageURL,
		SessionNonce: session.Nonce,
		ExpiresAt:    now.UTC().Add(confirmedProductImageTokenTTL).Unix(),
	}, secret, signedProductImagePurpose)
}

func validConfirmedProductImageToken(value string, imageURL string, session adminSession, secret string, now time.Time) bool {
	var payload confirmedProductImagePayload
	if !decodeSignedValue(value, secret, signedProductImagePurpose, &payload) {
		return false
	}
	if payload.Version != signedValueVersion || payload.URL != imageURL || payload.SessionNonce != session.Nonce || payload.ExpiresAt == 0 {
		return false
	}
	if !strings.HasPrefix(payload.URL, "/images/products/uploads/") {
		return false
	}
	return now.UTC().Before(time.Unix(payload.ExpiresAt, 0).UTC()) && now.UTC().Before(session.ExpiresAt)
}

func defaultVariantRows() []catalog.ProductVariant {
	return []catalog.ProductVariant{
		{Status: catalog.StatusActive, SortOrder: 10},
		{Status: catalog.StatusActive, SortOrder: 20},
		{Status: catalog.StatusActive, SortOrder: 30},
	}
}

func productFilters(request events.APIGatewayV2HTTPRequest) adminProductFilters {
	return adminProductFilters{Status: strings.TrimSpace(request.QueryStringParameters["status"]), Category: strings.TrimSpace(request.QueryStringParameters["category"]), Search: strings.TrimSpace(request.QueryStringParameters["q"])}
}

func filterAdminProducts(products []catalog.Product, filters adminProductFilters) []catalog.Product {
	filtered := make([]catalog.Product, 0, len(products))
	search := strings.ToLower(filters.Search)
	for _, product := range products {
		if filters.Status != "" && string(product.Status) != filters.Status {
			continue
		}
		if filters.Category != "" && !slices.Contains(product.CategorySlugs, filters.Category) {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(product.Name+" "+product.Slug), search) {
			continue
		}
		filtered = append(filtered, product)
	}
	return filtered
}

func cleanStrings(values []string) []string {
	cleaned := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		cleaned = append(cleaned, trimmed)
	}
	return cleaned
}

func centsFromDollars(value string) int {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0
	}
	dollars, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0
	}
	return int(dollars*100 + 0.5)
}

func intFromForm(value string) int {
	parsed, _ := strconv.Atoi(strings.TrimSpace(value))
	return parsed
}

func intAt(values []string, index int) int {
	if index >= len(values) {
		return 0
	}
	return intFromForm(values[index])
}

func valueAt(values []string, index int, fallback string) string {
	if index >= len(values) || strings.TrimSpace(values[index]) == "" {
		return fallback
	}
	return strings.TrimSpace(values[index])
}

func renderAdminProductList(ctx context.Context, vm adminProductListViewModel) (string, error) {
	var body strings.Builder
	if err := adminProductListPage(vm).Render(ctx, &body); err != nil {
		return "", err
	}
	return body.String(), nil
}

func renderAdminProductForm(ctx context.Context, vm adminProductFormViewModel) (string, error) {
	var body strings.Builder
	if err := adminProductFormPage(vm).Render(ctx, &body); err != nil {
		return "", err
	}
	return body.String(), nil
}

func formatAdminPrice(cents int) string {
	return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
}

func priceInputValue(cents int) string {
	if cents == 0 {
		return ""
	}
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

func statusSelected(status catalog.Status, value string) bool {
	return string(status) == value
}

func categoryChecked(product catalog.Product, slug string) bool {
	return slices.Contains(product.CategorySlugs, slug)
}

type localProductImageUploads struct{}

func (localProductImageUploads) Presign(ctx context.Context, request productImagePresignRequest, now time.Time) (productImagePresignResponse, error) {
	contentType, extension, ok := allowedProductImageType(request.ContentType)
	if !ok || !validUploadSize(request.SizeBytes) {
		return productImagePresignResponse{}, errInvalidProductImageUpload
	}
	key, err := generatedProductImageUploadKey(defaultProductImagesKeyPrefix, extension, now)
	if err != nil {
		return productImagePresignResponse{}, err
	}
	return productImagePresignResponse{URL: "/admin/uploads/product-image/local", Fields: map[string]string{"Content-Type": contentType}, Key: key, ExpiresAt: now.UTC().Add(productImageUploadPresignTTL), MaxSizeBytes: maxProductImageUploadBytes, ContentType: contentType}, nil
}

func (localProductImageUploads) Confirm(ctx context.Context, request productImageConfirmRequest) (string, error) {
	_, extension, ok := allowedProductImageType(request.ContentType)
	if !ok || !validUploadSize(request.SizeBytes) {
		return "", errInvalidProductImageUpload
	}
	if err := validateProductImageUploadKey(defaultProductImagesKeyPrefix, request.Key, extension); err != nil {
		return "", err
	}
	return "/" + request.Key, nil
}
