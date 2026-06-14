package admin

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/staticassets"
	"github.com/aws/aws-lambda-go/events"
)

const confirmedImageTokenMarker = "test-confirmed-image-token"

func TestProductListRendersAdminProductsAndNewProductLink(t *testing.T) {
	handler, _ := newProductTestHandler(t)
	response := authenticatedProductGet(t, handler, "/admin/products")

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	for _, want := range []string{"Products", "Test Draft", "/products/test-draft", "/admin/products/prod_test_draft/edit", "New product", "data-testid=\"admin-product-row\""} {
		if !strings.Contains(response.Body, want) {
			t.Fatalf("product list missing %q: %q", want, response.Body)
		}
	}
	if strings.Contains(response.Body, ">/test-draft") {
		t.Fatalf("product list used bare public slug path: %q", response.Body)
	}
	assertNoStore(t, response)
}

func TestProductNewFormRendersUploadVariantAndCategoryControls(t *testing.T) {
	handler, _ := newProductTestHandler(t)
	response := authenticatedProductGet(t, handler, "/admin/products/new")

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	for _, want := range []string{"New product", "data-testid=\"product-form\"", "data-testid=\"product-image-input\"", "data-testid=\"variant-editor\"", "name=\"category_slugs\"", "Bangkok Market Finds"} {
		if !strings.Contains(response.Body, want) {
			t.Fatalf("new form missing %q: %q", want, response.Body)
		}
	}
}

// TestProductFormUsesExternalUploadScript guards the CSP fix: the production
// Content-Security-Policy is script-src 'self' with no 'unsafe-inline', so the
// admin image upload must load from an external file, never an inline <script>.
func TestProductFormUsesExternalUploadScript(t *testing.T) {
	handler, _ := newProductTestHandler(t)
	response := authenticatedProductGet(t, handler, "/admin/products/new")

	if strings.Contains(response.Body, "<script>") {
		t.Fatalf("admin product form embeds an inline <script> blocked by the production CSP: %q", response.Body)
	}
	if strings.Contains(response.Body, "addEventListener") || strings.Contains(response.Body, "querySelector") {
		t.Fatalf("inline upload script logic leaked into the admin product HTML: %q", response.Body)
	}
	if !strings.Contains(response.Body, `<script src="`+staticassets.AdminProductUploadJSPath+`" defer>`) {
		t.Fatalf("admin product form missing the external upload script %q: %q", staticassets.AdminProductUploadJSPath, response.Body)
	}
}

func TestProductCreateValidationRejectsExternalImageAndDuplicateVariantLabels(t *testing.T) {
	handler, _ := newProductTestHandler(t)
	values := validProductForm(t, handler)
	values.Set("image_url", "https://example.com/shirt.webp")
	values.Del("image_token")
	values["variant_id"] = []string{"var_s_one", "var_s_two"}
	values["variant_label"] = []string{"S", "s"}
	values["variant_stock"] = []string{"1", "2"}

	response := authenticatedProductPost(t, handler, "/admin/products", values)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body %q", response.StatusCode, http.StatusBadRequest, response.Body)
	}
	for _, want := range []string{"Upload and confirm a product image", "duplicate active variant label", "product-validation-summary"} {
		if !strings.Contains(response.Body, want) {
			t.Fatalf("validation response missing %q: %q", want, response.Body)
		}
	}
}

func TestProductCreateRejectsUnsafeRouteSlugBeforeWrite(t *testing.T) {
	handler, store := newProductTestHandler(t)
	values := validProductForm(t, handler)
	values.Set("slug", "bad/slug")

	response := authenticatedProductPost(t, handler, "/admin/products", values)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body %q", response.StatusCode, http.StatusBadRequest, response.Body)
	}
	if !strings.Contains(response.Body, "Slug must use only lowercase letters, numbers, and hyphens") {
		t.Fatalf("unsafe slug response missing validation copy: %q", response.Body)
	}
	if _, found, err := store.GetProductBySlug(context.Background(), "bad/slug"); err != nil || found {
		t.Fatalf("unsafe slug product write found=%v err=%v, want no write", found, err)
	}
}

func TestProductUpdateRejectsUnsafeRouteSlugBeforeWrite(t *testing.T) {
	handler, store := newProductTestHandler(t)
	created := authenticatedProductPost(t, handler, "/admin/products", validProductForm(t, handler))
	productID := productIDFromRedirect(t, created.Headers["Location"])
	values := validProductForm(t, handler)
	values.Set("version", "1")
	values.Set("slug", "bad slug")

	response := authenticatedProductPost(t, handler, "/admin/products/"+productID+"/edit", values)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body %q", response.StatusCode, http.StatusBadRequest, response.Body)
	}
	for _, want := range []string{"Slug must use only lowercase letters, numbers, and hyphens", "Product slug cannot be changed"} {
		if !strings.Contains(response.Body, want) {
			t.Fatalf("unsafe update slug response missing %q: %q", want, response.Body)
		}
	}
	if _, found, err := store.GetProductBySlug(context.Background(), "bad slug"); err != nil || found {
		t.Fatalf("unsafe update slug product write found=%v err=%v, want no write", found, err)
	}
}

func TestProductUpdateRejectsSlugChangeBeforeWrite(t *testing.T) {
	handler, store := newProductTestHandler(t)
	created := authenticatedProductPost(t, handler, "/admin/products", validProductForm(t, handler))
	productID := productIDFromRedirect(t, created.Headers["Location"])
	values := validProductForm(t, handler)
	values.Set("version", "1")
	values.Set("slug", "renamed-test-shirt")

	response := authenticatedProductPost(t, handler, "/admin/products/"+productID+"/edit", values)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body %q", response.StatusCode, http.StatusBadRequest, response.Body)
	}
	if !strings.Contains(response.Body, "Product slug cannot be changed") {
		t.Fatalf("slug mutation response missing validation copy: %q", response.Body)
	}
	if _, found, err := store.GetProductBySlug(context.Background(), "renamed-test-shirt"); err != nil || found {
		t.Fatalf("renamed slug product write found=%v err=%v, want no write", found, err)
	}
	product, found, err := store.GetProductBySlug(context.Background(), "test-shirt")
	if err != nil || !found || product.Version != 1 {
		t.Fatalf("original product found=%v err=%v product=%#v", found, err, product)
	}
}

func TestProductEditFormLocksSlugInput(t *testing.T) {
	handler, _ := newProductTestHandler(t)
	created := authenticatedProductPost(t, handler, "/admin/products", validProductForm(t, handler))
	productID := productIDFromRedirect(t, created.Headers["Location"])

	response := authenticatedProductGet(t, handler, "/admin/products/"+productID+"/edit")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	for _, want := range []string{`data-testid="product-slug-input"`, `name="slug"`, `value="test-shirt"`, `readonly`} {
		if !strings.Contains(response.Body, want) {
			t.Fatalf("edit form missing %q: %q", want, response.Body)
		}
	}
}

func TestProductCreateRejectsGeneratedActiveVariantIDCollisionBeforeWrite(t *testing.T) {
	handler, store := newProductTestHandler(t)
	values := validProductForm(t, handler)
	values["variant_label"] = []string{"S/M", "S M"}
	values["variant_stock"] = []string{"1", "2"}
	values["variant_status"] = []string{"active", "active"}

	response := authenticatedProductPost(t, handler, "/admin/products", values)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body %q", response.StatusCode, http.StatusBadRequest, response.Body)
	}
	if !strings.Contains(response.Body, "duplicate active variant ID") {
		t.Fatalf("variant ID collision response missing validation copy: %q", response.Body)
	}
	if _, found, err := store.GetProductBySlug(context.Background(), "test-shirt"); err != nil || found {
		t.Fatalf("variant collision product write found=%v err=%v, want no write", found, err)
	}
}

func TestProductCreateRejectsForgedSiteRelativeUploadURLWithoutConfirmation(t *testing.T) {
	handler, store := newProductTestHandler(t)
	values := validProductForm(t, handler)
	values.Set("image_url", "/images/products/uploads/2026/06/11111111-1111-4111-8111-111111111111/original.webp")
	values.Del("image_token")

	response := authenticatedProductPost(t, handler, "/admin/products", values)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body %q", response.StatusCode, http.StatusBadRequest, response.Body)
	}
	if !strings.Contains(response.Body, "Upload and confirm a product image") {
		t.Fatalf("forged upload response missing confirmation error: %q", response.Body)
	}
	if _, found, err := store.GetProductBySlug(context.Background(), "test-shirt"); err != nil || found {
		t.Fatalf("forged image product write found=%v err=%v, want no write", found, err)
	}
}

func TestProductCreateSavesConfirmedImageVariantsAndCategories(t *testing.T) {
	handler, store := newProductTestHandler(t)
	values := validProductForm(t, handler)

	response := authenticatedProductPost(t, handler, "/admin/products", values)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body %q", response.StatusCode, http.StatusSeeOther, response.Body)
	}
	if !strings.Contains(response.Headers["Location"], "/admin/products/prod_") || !strings.Contains(response.Headers["Location"], "/edit?saved=created") {
		t.Fatalf("Location = %q, want edit redirect by stable product ID", response.Headers["Location"])
	}
	product, found, err := store.GetProductBySlug(context.Background(), "test-shirt")
	if err != nil || !found {
		t.Fatalf("created product found=%v err=%v", found, err)
	}
	if product.ImageURL != "/images/products/uploads/2026/06/11111111-1111-4111-8111-111111111111/original.webp" || product.Status != catalog.StatusActive || product.Version != 1 {
		t.Fatalf("created product = %#v", product)
	}
	if len(product.Variants) != 3 || product.Variants[0].Label != "S" || product.Variants[1].StockQuantity != 2 || product.Variants[2].Label != "XL" || product.TotalAvailableStock() != 3 {
		t.Fatalf("created variants = %#v", product.Variants)
	}
	if len(product.CategorySlugs) != 1 || product.CategorySlugs[0] != "market-finds" {
		t.Fatalf("created categories = %#v", product.CategorySlugs)
	}
}

func TestProductDuplicateSlugReturnsConflictSummary(t *testing.T) {
	handler, _ := newProductTestHandler(t)
	values := validProductForm(t, handler)
	_ = authenticatedProductPost(t, handler, "/admin/products", values)

	response := authenticatedProductPost(t, handler, "/admin/products", values)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body %q", response.StatusCode, http.StatusBadRequest, response.Body)
	}
	if !strings.Contains(response.Body, "Slug is already used by another product") {
		t.Fatalf("duplicate response missing slug conflict: %q", response.Body)
	}
}

func TestProductUpdateRejectsStaleVersionConflict(t *testing.T) {
	handler, _ := newProductTestHandler(t)
	values := validProductForm(t, handler)
	created := authenticatedProductPost(t, handler, "/admin/products", values)
	productID := productIDFromRedirect(t, created.Headers["Location"])

	values.Set("version", "0")
	response := authenticatedProductPost(t, handler, "/admin/products/"+productID+"/edit", values)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body %q", response.StatusCode, http.StatusConflict, response.Body)
	}
	if !strings.Contains(response.Body, "changed while you were editing") {
		t.Fatalf("stale response missing conflict copy: %q", response.Body)
	}
}

func TestProductArchiveHidesPublicVisibilityAndKeepsAdminArchivedRow(t *testing.T) {
	handler, store := newProductTestHandler(t)
	created := authenticatedProductPost(t, handler, "/admin/products", validProductForm(t, handler))
	productID := productIDFromRedirect(t, created.Headers["Location"])
	product, found, err := store.GetProductBySlug(context.Background(), "test-shirt")
	if err != nil || !found || product.Status != catalog.StatusActive {
		t.Fatalf("active created product found=%v err=%v product=%#v", found, err, product)
	}

	archiveResponse := authenticatedProductPost(t, handler, "/admin/products/"+productID+"/archive", url.Values{})
	if archiveResponse.StatusCode != http.StatusSeeOther {
		t.Fatalf("archive status = %d, want %d", archiveResponse.StatusCode, http.StatusSeeOther)
	}
	archived, found, err := store.GetProductBySlug(context.Background(), "test-shirt")
	if err != nil || !found || archived.Status != catalog.StatusArchived {
		t.Fatalf("archived product found=%v err=%v product=%#v", found, err, archived)
	}
	listResponse := authenticatedProductGet(t, handler, "/admin/products?status=archived")
	if !strings.Contains(listResponse.Body, "Test Shirt") || !strings.Contains(listResponse.Body, "archived") {
		t.Fatalf("archived list missing product: %q", listResponse.Body)
	}
	activeProducts, err := store.ListActiveProducts(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListActiveProducts returned error: %v", err)
	}
	for _, activeProduct := range activeProducts {
		if activeProduct.Slug == "test-shirt" {
			t.Fatalf("archived product remained publicly active: %#v", activeProduct)
		}
	}
}

func newProductTestHandler(t *testing.T) (*Handler, *catalog.MemoryStore) {
	t.Helper()
	handler, _ := newAuthTestHandler(t)
	store := catalog.NewMemoryStore([]catalog.Product{{ID: "prod_test_draft", Slug: "test-draft", Name: "Test Draft", PriceCents: 1200, Status: catalog.StatusDraft, SortOrder: 10, Version: 1}}, []catalog.Category{{Slug: "market-finds", Name: "Bangkok Market Finds", Status: catalog.StatusActive, SortOrder: 10}})
	handler.catalog = store
	return handler, store
}

func authenticatedProductGet(t *testing.T, handler *Handler, path string) events.APIGatewayV2HTTPResponse {
	t.Helper()
	login := loginResponse(t, handler)
	requestPath := path
	query := ""
	if before, after, found := strings.Cut(path, "?"); found {
		requestPath = before
		query = after
	}
	request := adminRequest(http.MethodGet, requestPath)
	request.RawQueryString = query
	request.QueryStringParameters = map[string]string{}
	if queryValues, err := url.ParseQuery(query); err == nil {
		for key, values := range queryValues {
			if len(values) > 0 {
				request.QueryStringParameters[key] = values[0]
			}
		}
	}
	request.Cookies = []string{cookiePair(t, responseCookie(t, login, adminSessionCookieName)), cookiePair(t, responseCookie(t, login, adminCSRFCookieName))}
	response, err := handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle GET %s returned error: %v", path, err)
	}
	return response
}

func authenticatedProductPost(t *testing.T, handler *Handler, path string, values url.Values) events.APIGatewayV2HTTPResponse {
	t.Helper()
	login := loginResponse(t, handler)
	sessionPair := cookiePair(t, responseCookie(t, login, adminSessionCookieName))
	csrfPair := cookiePair(t, responseCookie(t, login, adminCSRFCookieName))
	_, csrfToken, _ := strings.Cut(csrfPair, "=")
	submittedValues := cloneFormValues(values)
	if submittedValues.Get("image_token") == confirmedImageTokenMarker {
		request := adminRequest(http.MethodGet, "/admin/products/new")
		request.Cookies = []string{sessionPair}
		session, authenticated := handler.authenticatedSession(context.Background(), request)
		if !authenticated {
			t.Fatal("test session did not authenticate")
		}
		token, err := newConfirmedProductImageToken(submittedValues.Get("image_url"), session, handler.credentials.SessionSecret, handler.currentTime())
		if err != nil {
			t.Fatalf("newConfirmedProductImageToken returned error: %v", err)
		}
		submittedValues.Set("image_token", token)
	}
	submittedValues.Set("csrf_token", csrfToken)
	request := adminFormRequest(http.MethodPost, path, submittedValues)
	request.Cookies = []string{sessionPair, csrfPair}
	response, err := handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle POST %s returned error: %v", path, err)
	}
	return response
}

func validProductForm(t *testing.T, handler *Handler) url.Values {
	t.Helper()
	imageURL := "/images/products/uploads/2026/06/11111111-1111-4111-8111-111111111111/original.webp"
	_ = handler
	return url.Values{
		"name":           {"Test Shirt"},
		"slug":           {"test-shirt"},
		"description":    {"A soft cotton shirt for admin product tests."},
		"price":          {"24.99"},
		"sort_order":     {"15"},
		"stock_quantity": {"0"},
		"status":         {"active"},
		"image_url":      {imageURL},
		"image_token":    {confirmedImageTokenMarker},
		"category_slugs": {"market-finds"},
		"variant_label":  {"S", "M", "XL"},
		"variant_stock":  {"1", "2", "0"},
		"variant_status": {"active", "active", "active"},
	}
}

func cloneFormValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, value := range values {
		cloned[key] = slices.Clone(value)
	}
	return cloned
}

func productIDFromRedirect(t *testing.T, location string) string {
	t.Helper()
	parts := strings.Split(location, "/")
	if len(parts) < 4 {
		t.Fatalf("Location = %q, cannot extract product ID", location)
	}
	return parts[3]
}
