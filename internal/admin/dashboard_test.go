package admin

import (
	"net/http"
	"strings"
	"testing"

	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/staticassets"
)

// TestDashboardUsesExternalScript guards the CSP fix: the dashboard upload-
// failure counter must load from an external file, never an inline <script>.
func TestDashboardUsesExternalScript(t *testing.T) {
	handler, _ := newAuthTestHandler(t)
	response := authenticatedProductGet(t, handler, "/admin")

	if strings.Contains(response.Body, "<script>") {
		t.Fatalf("admin dashboard embeds an inline <script> blocked by the production CSP: %q", response.Body)
	}
	if strings.Contains(response.Body, "sessionStorage") || strings.Contains(response.Body, "querySelector") {
		t.Fatalf("inline dashboard script logic leaked into the admin dashboard HTML: %q", response.Body)
	}
	if !strings.Contains(response.Body, `<script src="`+staticassets.AdminDashboardJSPath+`" defer>`) {
		t.Fatalf("admin dashboard missing the external script %q: %q", staticassets.AdminDashboardJSPath, response.Body)
	}
}

func TestDashboardMetricsCountsCatalogOperationalState(t *testing.T) {
	handler, _ := newAuthTestHandler(t)
	handler.catalog = catalog.NewMemoryStore([]catalog.Product{
		{ID: "prod_active", Slug: "active", Name: "Active", Status: catalog.StatusActive, SortOrder: 10, Variants: []catalog.ProductVariant{
			{ID: "var_active_low", Label: "S", StockQuantity: 1, Status: catalog.StatusActive, SortOrder: 10},
			{ID: "var_active_two", Label: "M", StockQuantity: 2, Status: catalog.StatusActive, SortOrder: 20},
			{ID: "var_active_archived", Label: "XL", StockQuantity: 0, Status: catalog.StatusArchived, SortOrder: 30},
		}},
		{ID: "prod_draft", Slug: "draft", Name: "Draft", Status: catalog.StatusDraft, SortOrder: 20, Variants: []catalog.ProductVariant{
			{ID: "var_draft_low", Label: "One size", StockQuantity: 1, Status: catalog.StatusActive, SortOrder: 10},
		}},
		{ID: "prod_archived", Slug: "archived", Name: "Archived", Status: catalog.StatusArchived, SortOrder: 30, Variants: []catalog.ProductVariant{
			{ID: "var_archived_low", Label: "Archive", StockQuantity: 1, Status: catalog.StatusActive, SortOrder: 10},
		}},
	}, []catalog.Category{
		{Slug: "market", Name: "Market", Status: catalog.StatusActive, SortOrder: 10},
		{Slug: "draft-aisle", Name: "Draft Aisle", Status: catalog.StatusDraft, SortOrder: 20},
	})

	response := authenticatedProductGet(t, handler, "/admin")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	for _, want := range []string{
		`data-testid="admin-metric-products"`, `>3</p>`,
		`data-testid="admin-metric-products-active"`, `>1</p>`,
		`data-testid="admin-metric-products-draft"`,
		`data-testid="admin-metric-products-archived"`,
		`data-testid="admin-metric-categories"`, `>2</p>`,
		`data-testid="admin-metric-low-stock"`,
		`data-testid="admin-metric-upload-failures"`,
		`data-testid="admin-nav-products"`,
		`data-testid="admin-nav-categories"`,
	} {
		if !strings.Contains(response.Body, want) {
			t.Fatalf("dashboard missing %q: %q", want, response.Body)
		}
	}
	if !strings.Contains(response.Body, `data-testid="admin-metric-low-stock" class="mt-3 font-display text-4xl font-bold leading-none text-flag-blue">3</p>`) {
		t.Fatalf("dashboard low-stock metric did not exclude archived product/variant as expected: %q", response.Body)
	}
	assertNoStore(t, response)
}

func TestProductFilterEmptyStateIsDeterministic(t *testing.T) {
	handler, _ := newProductTestHandler(t)
	response := authenticatedProductGet(t, handler, "/admin/products?status=archived&q=does-not-exist-999")

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	for _, want := range []string{
		`data-testid="admin-products-filter-form"`,
		`data-testid="admin-products-search-input"`,
		`data-testid="admin-products-status-filter"`,
		`data-testid="admin-products-empty" role="status"`,
		`No products match these filters.`,
	} {
		if !strings.Contains(response.Body, want) {
			t.Fatalf("product empty state missing %q: %q", want, response.Body)
		}
	}
	if strings.Contains(response.Body, `data-testid="admin-product-row"`) {
		t.Fatalf("product empty state rendered product rows: %q", response.Body)
	}
}

func TestCategoryFilterEmptyStateIsDeterministic(t *testing.T) {
	handler, _ := newCategoryTestHandler(t)
	response := authenticatedProductGet(t, handler, "/admin/categories?status=archived&q=does-not-exist-999")

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	for _, want := range []string{
		`data-testid="admin-categories-filter-form"`,
		`data-testid="admin-categories-search-input"`,
		`data-testid="admin-categories-status-filter"`,
		`data-testid="admin-categories-empty" role="status"`,
		`No categories match these filters.`,
	} {
		if !strings.Contains(response.Body, want) {
			t.Fatalf("category empty state missing %q: %q", want, response.Body)
		}
	}
	if strings.Contains(response.Body, `data-testid="admin-category-row"`) {
		t.Fatalf("category empty state rendered category rows: %q", response.Body)
	}
}

func TestValidationAndFlashAccessibilitySelectors(t *testing.T) {
	handler, _ := newProductTestHandler(t)
	flashResponse := authenticatedProductGet(t, handler, "/admin/products?saved=updated")
	if !strings.Contains(flashResponse.Body, `data-testid="admin-flash" role="alert"`) {
		t.Fatalf("product flash missing alert role: %q", flashResponse.Body)
	}

	values := validProductForm(t, handler)
	values.Set("name", "")
	validationResponse := authenticatedProductPost(t, handler, "/admin/products", values)
	if validationResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", validationResponse.StatusCode, http.StatusBadRequest)
	}
	for _, want := range []string{
		`data-testid="product-validation-summary" role="alert"`,
		`data-testid="product-image-status" role="status" aria-live="polite"`,
		`data-testid="product-save-button"`,
		`disabled:cursor-not-allowed`,
	} {
		if !strings.Contains(validationResponse.Body, want) {
			t.Fatalf("validation/accessibility response missing %q: %q", want, validationResponse.Body)
		}
	}
}
