package admin

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
)

func TestCategoryListAndNewFormRenderAdminControls(t *testing.T) {
	handler, _ := newCategoryTestHandler(t)

	listResponse := authenticatedProductGet(t, handler, "/admin/categories")
	if listResponse.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, want %d", listResponse.StatusCode, http.StatusOK)
	}
	for _, want := range []string{"Categories", "Summer Shirts", "/categories/summer-shirts", "data-testid=\"admin-category-row\"", "data-testid=\"new-category-link\""} {
		if !strings.Contains(listResponse.Body, want) {
			t.Fatalf("category list missing %q: %q", want, listResponse.Body)
		}
	}
	if strings.Contains(listResponse.Body, ">/summer-shirts") {
		t.Fatalf("category list used bare public slug path: %q", listResponse.Body)
	}

	newResponse := authenticatedProductGet(t, handler, "/admin/categories/new")
	if newResponse.StatusCode != http.StatusOK {
		t.Fatalf("new status = %d, want %d", newResponse.StatusCode, http.StatusOK)
	}
	for _, want := range []string{"New category", "data-testid=\"category-form\"", "data-testid=\"category-status-select\"", "data-testid=\"category-product-checkbox\"", "Test Shirt"} {
		if !strings.Contains(newResponse.Body, want) {
			t.Fatalf("category form missing %q: %q", want, newResponse.Body)
		}
	}
}

func TestCategoryCreateRejectsUnsafeRouteSlugBeforeWrite(t *testing.T) {
	handler, store := newCategoryTestHandler(t)
	values := validCategoryForm()
	values.Set("slug", "bad/slug")

	response := authenticatedProductPost(t, handler, "/admin/categories", values)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body %q", response.StatusCode, http.StatusBadRequest, response.Body)
	}
	if !strings.Contains(response.Body, "Slug must use only lowercase letters, numbers, and hyphens") {
		t.Fatalf("unsafe slug response missing validation copy: %q", response.Body)
	}
	if categoryExists(t, store, "bad/slug") {
		t.Fatal("unsafe category slug was written")
	}
}

func TestCategoryUpdateRejectsUnsafeRouteSlugBeforeWrite(t *testing.T) {
	handler, store := newCategoryTestHandler(t)
	values := validCategoryForm()
	values.Set("slug", "summer-caps")
	createResponse := authenticatedProductPost(t, handler, "/admin/categories", values)
	if createResponse.StatusCode != http.StatusSeeOther {
		t.Fatalf("create status = %d, want %d; body %q", createResponse.StatusCode, http.StatusSeeOther, createResponse.Body)
	}

	updateValues := validCategoryForm()
	updateValues.Set("slug", "bad slug")
	updateValues.Set("version", "1")
	response := authenticatedProductPost(t, handler, "/admin/categories/summer-caps", updateValues)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body %q", response.StatusCode, http.StatusBadRequest, response.Body)
	}
	for _, want := range []string{"Slug must use only lowercase letters, numbers, and hyphens", "Category slug cannot be changed"} {
		if !strings.Contains(response.Body, want) {
			t.Fatalf("unsafe update slug response missing %q: %q", want, response.Body)
		}
	}
	if categoryExists(t, store, "bad slug") {
		t.Fatal("unsafe updated category slug was written")
	}
}

func TestCategoryCreateDuplicateUpdateMembershipAndArchive(t *testing.T) {
	handler, store := newCategoryTestHandler(t)
	values := validCategoryForm()
	values.Set("slug", "summer-caps")
	values.Set("sort_order", "30")

	createResponse := authenticatedProductPost(t, handler, "/admin/categories", values)
	if createResponse.StatusCode != http.StatusSeeOther {
		t.Fatalf("create status = %d, want %d; body %q", createResponse.StatusCode, http.StatusSeeOther, createResponse.Body)
	}
	if createResponse.Headers["Location"] != "/admin/categories/summer-caps/edit?saved=created" {
		t.Fatalf("create Location = %q", createResponse.Headers["Location"])
	}
	category := categoryBySlug(t, store, "summer-caps")
	if category.Version != 1 || category.SortOrder != 30 || category.Status != catalog.StatusActive {
		t.Fatalf("created category = %#v", category)
	}
	products, err := store.ListActiveProductsByCategory(context.Background(), "summer-caps", 0)
	if err != nil {
		t.Fatalf("ListActiveProductsByCategory returned error: %v", err)
	}
	if len(products) != 1 || products[0].Slug != "test-shirt" {
		t.Fatalf("category products after create = %#v", products)
	}

	duplicateResponse := authenticatedProductPost(t, handler, "/admin/categories", values)
	if duplicateResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate status = %d, want %d; body %q", duplicateResponse.StatusCode, http.StatusBadRequest, duplicateResponse.Body)
	}
	if !strings.Contains(duplicateResponse.Body, "Slug is already used by another category") {
		t.Fatalf("duplicate response missing slug conflict: %q", duplicateResponse.Body)
	}

	editResponse := authenticatedProductGet(t, handler, "/admin/categories/summer-caps/edit")
	if !strings.Contains(editResponse.Body, "checked") || !strings.Contains(editResponse.Body, "Test Shirt") {
		t.Fatalf("edit response missing checked membership: %q", editResponse.Body)
	}

	updateValues := validCategoryForm()
	updateValues.Set("name", "Summer Caps Updated")
	updateValues.Set("slug", "summer-caps")
	updateValues.Set("description", "Updated summer cap aisle.")
	updateValues.Set("status", "draft")
	updateValues.Set("sort_order", "5")
	updateValues.Set("version", "1")
	updateValues.Del("product_ids")
	updateResponse := authenticatedProductPost(t, handler, "/admin/categories/summer-caps", updateValues)
	if updateResponse.StatusCode != http.StatusSeeOther {
		t.Fatalf("update status = %d, want %d; body %q", updateResponse.StatusCode, http.StatusSeeOther, updateResponse.Body)
	}
	updated := categoryBySlug(t, store, "summer-caps")
	if updated.Version != 2 || updated.Name != "Summer Caps Updated" || updated.SortOrder != 5 || updated.Status != catalog.StatusDraft {
		t.Fatalf("updated category = %#v", updated)
	}
	product, found, err := store.GetProductByID(context.Background(), "prod_test_shirt")
	if err != nil || !found {
		t.Fatalf("GetProductByID found=%v err=%v", found, err)
	}
	if slices.Contains(product.CategorySlugs, "summer-caps") {
		t.Fatalf("product retained removed category membership: %#v", product.CategorySlugs)
	}

	activateValues := updateValues
	activateValues.Set("status", "active")
	activateValues.Set("version", "2")
	activateValues["product_ids"] = []string{"prod_test_shirt"}
	activateResponse := authenticatedProductPost(t, handler, "/admin/categories/summer-caps", activateValues)
	if activateResponse.StatusCode != http.StatusSeeOther {
		t.Fatalf("activate status = %d, want %d", activateResponse.StatusCode, http.StatusSeeOther)
	}
	archiveResponse := authenticatedProductPost(t, handler, "/admin/categories/summer-caps/archive", url.Values{})
	if archiveResponse.StatusCode != http.StatusSeeOther {
		t.Fatalf("archive status = %d, want %d", archiveResponse.StatusCode, http.StatusSeeOther)
	}
	archived := categoryBySlug(t, store, "summer-caps")
	if archived.Status != catalog.StatusArchived || archived.Version != 4 {
		t.Fatalf("archived category = %#v", archived)
	}
	activeCategories, err := store.ListActiveCategories(context.Background())
	if err != nil {
		t.Fatalf("ListActiveCategories returned error: %v", err)
	}
	for _, activeCategory := range activeCategories {
		if activeCategory.Slug == "summer-caps" {
			t.Fatalf("archived category remained public: %#v", activeCategory)
		}
	}
	activeProducts, err := store.ListActiveProducts(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListActiveProducts returned error: %v", err)
	}
	if len(activeProducts) == 0 || activeProducts[0].Slug != "test-shirt" || activeProducts[0].Status != catalog.StatusActive {
		t.Fatalf("archive changed product public visibility: %#v", activeProducts)
	}
}

func TestCategoryMembershipEditorUpdatesProductEditCheckboxes(t *testing.T) {
	handler, store := newCategoryTestHandler(t)
	values := validCategoryForm()
	values.Set("slug", "summer-caps")
	values.Set("version", "0")
	values["product_ids"] = []string{"prod_test_shirt"}

	createResponse := authenticatedProductPost(t, handler, "/admin/categories", values)
	if createResponse.StatusCode != http.StatusSeeOther {
		t.Fatalf("create status = %d, want %d; body %q", createResponse.StatusCode, http.StatusSeeOther, createResponse.Body)
	}
	product, found, err := store.GetProductByID(context.Background(), "prod_test_shirt")
	if err != nil || !found {
		t.Fatalf("GetProductByID found=%v err=%v", found, err)
	}
	if !slices.Contains(product.CategorySlugs, "summer-caps") {
		t.Fatalf("product missing category membership: %#v", product.CategorySlugs)
	}

	productEditResponse := authenticatedProductGet(t, handler, "/admin/products/prod_test_shirt/edit")
	if !strings.Contains(productEditResponse.Body, `value="summer-caps" checked`) {
		t.Fatalf("product edit did not reflect category membership: %q", productEditResponse.Body)
	}
}

func newCategoryTestHandler(t *testing.T) (*Handler, *catalog.MemoryStore) {
	t.Helper()
	handler, _ := newAuthTestHandler(t)
	store := catalog.NewMemoryStore([]catalog.Product{{ID: "prod_test_shirt", Slug: "test-shirt", Name: "Test Shirt", Description: "A soft cotton shirt.", PriceCents: 2499, Status: catalog.StatusActive, SortOrder: 10, StockQuantity: 3, Version: 1}}, []catalog.Category{{Slug: "summer-shirts", Name: "Summer Shirts", Description: "Lightweight summer styles.", Status: catalog.StatusActive, SortOrder: 10, Version: 1}})
	handler.catalog = store
	return handler, store
}

func validCategoryForm() url.Values {
	return url.Values{
		"name":        {"Summer Shirts"},
		"slug":        {"summer-shirts"},
		"description": {"Lightweight cotton shirts for hot market days."},
		"status":      {"active"},
		"sort_order":  {"20"},
		"version":     {"0"},
		"product_ids": {"prod_test_shirt"},
	}
}

func categoryExists(t *testing.T, store *catalog.MemoryStore, slug string) bool {
	t.Helper()
	categories, err := store.ListCategories(context.Background())
	if err != nil {
		t.Fatalf("ListCategories returned error: %v", err)
	}
	for _, category := range categories {
		if category.Slug == slug {
			return true
		}
	}
	return false
}

func categoryBySlug(t *testing.T, store *catalog.MemoryStore, slug string) catalog.Category {
	t.Helper()
	categories, err := store.ListCategories(context.Background())
	if err != nil {
		t.Fatalf("ListCategories returned error: %v", err)
	}
	for _, category := range categories {
		if category.Slug == slug {
			return category
		}
	}
	t.Fatalf("category %q not found in %#v", slug, categories)
	return catalog.Category{}
}
