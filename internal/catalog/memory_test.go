package catalog

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestMemoryStoreListActiveProductsFiltersInInputOrderAndHonorsLimit(t *testing.T) {
	store := NewMemoryStore(memoryStoreTestProducts(), memoryStoreTestCategories())

	products, err := store.ListActiveProducts(context.Background(), 2)
	if err != nil {
		t.Fatalf("ListActiveProducts returned error: %v", err)
	}
	if got := productSlugs(products); !reflect.DeepEqual(got, []string{"active-old", "active-fallback"}) {
		t.Fatalf("limited active product slugs = %#v", got)
	}

	products, err = store.ListActiveProducts(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListActiveProducts without limit returned error: %v", err)
	}
	if got := productSlugs(products); !reflect.DeepEqual(got, []string{"active-old", "active-fallback", "active-newer", "active-zero"}) {
		t.Fatalf("unlimited active product slugs = %#v", got)
	}
}

func TestMemoryStoreListRecentlyAddedProductsOrdersActiveNewestFirstAndHonorsLimit(t *testing.T) {
	store := NewMemoryStore(memoryStoreTestProducts(), nil)

	products, err := store.ListRecentlyAddedProducts(context.Background(), 3)
	if err != nil {
		t.Fatalf("ListRecentlyAddedProducts returned error: %v", err)
	}

	want := []string{"active-fallback", "active-newer", "active-old"}
	if got := productSlugs(products); !reflect.DeepEqual(got, want) {
		t.Fatalf("recent product slugs = %#v, want %#v", got, want)
	}
}

func TestMemoryStoreGetProductBySlugReturnsDraftAndRequiresExactMatch(t *testing.T) {
	store := NewMemoryStore(memoryStoreTestProducts(), nil)

	product, ok, err := store.GetProductBySlug(context.Background(), "draft-newest")
	if err != nil {
		t.Fatalf("GetProductBySlug returned error: %v", err)
	}
	if !ok {
		t.Fatal("GetProductBySlug ok = false, want true for draft product")
	}
	if product.Status != StatusDraft {
		t.Fatalf("draft product status = %q", product.Status)
	}

	_, ok, err = store.GetProductBySlug(context.Background(), "Draft-Newest")
	if err != nil {
		t.Fatalf("GetProductBySlug exact miss returned error: %v", err)
	}
	if ok {
		t.Fatal("GetProductBySlug ok = true, want false for case-mismatched slug")
	}
}

func TestMemoryStoreListActiveCategoriesFiltersInInputOrder(t *testing.T) {
	store := NewMemoryStore(nil, memoryStoreTestCategories())

	categories, err := store.ListActiveCategories(context.Background())
	if err != nil {
		t.Fatalf("ListActiveCategories returned error: %v", err)
	}
	if got := categorySlugs(categories); !reflect.DeepEqual(got, []string{"pantry", "home-decor"}) {
		t.Fatalf("active category slugs = %#v", got)
	}
}

func TestMemoryStoreListActiveProductsByCategoryFiltersExactMembershipAndHonorsLimit(t *testing.T) {
	store := NewMemoryStore(memoryStoreTestProducts(), nil)

	products, err := store.ListActiveProductsByCategory(context.Background(), "home-decor", 1)
	if err != nil {
		t.Fatalf("ListActiveProductsByCategory returned error: %v", err)
	}
	if got := productSlugs(products); !reflect.DeepEqual(got, []string{"active-fallback"}) {
		t.Fatalf("limited home-decor product slugs = %#v", got)
	}

	products, err = store.ListActiveProductsByCategory(context.Background(), "home", 0)
	if err != nil {
		t.Fatalf("ListActiveProductsByCategory exact miss returned error: %v", err)
	}
	if len(products) != 0 {
		t.Fatalf("partial category match products = %#v, want none", products)
	}
}

func TestMemoryStoreDefensivelyCopiesInputsAndResults(t *testing.T) {
	products := []Product{{
		ID:            "prod_copy",
		Slug:          "copy-source",
		Status:        StatusActive,
		CategorySlugs: []string{"pantry"},
	}}
	categories := []Category{{Slug: "pantry", Status: StatusActive}}
	store := NewMemoryStore(products, categories)

	products[0].Slug = "mutated-input"
	products[0].CategorySlugs[0] = "mutated-category"
	categories[0].Slug = "mutated-category"

	listedProducts, err := store.ListActiveProducts(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListActiveProducts returned error: %v", err)
	}
	if got := listedProducts[0].Slug; got != "copy-source" {
		t.Fatalf("stored product slug = %q, want copy-source", got)
	}
	if got := listedProducts[0].CategorySlugs[0]; got != "pantry" {
		t.Fatalf("stored product category = %q, want pantry", got)
	}

	listedProducts[0].CategorySlugs[0] = "mutated-result"
	product, ok, err := store.GetProductBySlug(context.Background(), "copy-source")
	if err != nil {
		t.Fatalf("GetProductBySlug returned error: %v", err)
	}
	if !ok {
		t.Fatal("GetProductBySlug ok = false, want true")
	}
	if got := product.CategorySlugs[0]; got != "pantry" {
		t.Fatalf("stored product category after result mutation = %q, want pantry", got)
	}

	listedCategories, err := store.ListActiveCategories(context.Background())
	if err != nil {
		t.Fatalf("ListActiveCategories returned error: %v", err)
	}
	if got := listedCategories[0].Slug; got != "pantry" {
		t.Fatalf("stored category slug = %q, want pantry", got)
	}
}

func TestNewDemoStoreReturnsDeterministicCatalogStore(t *testing.T) {
	store := NewDemoStore()

	products, err := store.ListActiveProducts(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListActiveProducts returned error: %v", err)
	}
	if len(products) != 11 {
		t.Fatalf("active demo product count = %d, want 11", len(products))
	}

	product, ok, err := store.GetProductBySlug(context.Background(), "brass-temple-bell")
	if err != nil {
		t.Fatalf("GetProductBySlug returned error: %v", err)
	}
	if !ok {
		t.Fatal("GetProductBySlug ok = false, want true for draft demo product")
	}
	if product.Status != StatusDraft {
		t.Fatalf("demo draft status = %q", product.Status)
	}

	categories, err := store.ListActiveCategories(context.Background())
	if err != nil {
		t.Fatalf("ListActiveCategories returned error: %v", err)
	}
	if len(categories) != len(DemoCatalogCategories()) {
		t.Fatalf("active demo category count = %d, want %d", len(categories), len(DemoCatalogCategories()))
	}
}

func memoryStoreTestProducts() []Product {
	return []Product{
		{
			ID:            "prod_active_old",
			Slug:          "active-old",
			Status:        StatusActive,
			CreatedAt:     time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC),
			UpdatedAt:     time.Date(2026, 6, 2, 9, 0, 0, 0, time.UTC),
			CategorySlugs: []string{"pantry"},
		},
		{
			ID:            "prod_draft_newest",
			Slug:          "draft-newest",
			Status:        StatusDraft,
			CreatedAt:     time.Date(2026, 6, 6, 9, 0, 0, 0, time.UTC),
			CategorySlugs: []string{"home-decor"},
		},
		{
			ID:            "prod_active_fallback",
			Slug:          "active-fallback",
			Status:        StatusActive,
			UpdatedAt:     time.Date(2026, 6, 5, 9, 0, 0, 0, time.UTC),
			CategorySlugs: []string{"home-decor", "pantry"},
		},
		{
			ID:            "prod_archived",
			Slug:          "archived-hidden",
			Status:        StatusArchived,
			CreatedAt:     time.Date(2026, 6, 7, 9, 0, 0, 0, time.UTC),
			CategorySlugs: []string{"pantry"},
		},
		{
			ID:            "prod_active_newer",
			Slug:          "active-newer",
			Status:        StatusActive,
			CreatedAt:     time.Date(2026, 6, 4, 9, 0, 0, 0, time.UTC),
			CategorySlugs: []string{"home-decor"},
		},
		{
			ID:            "prod_active_zero",
			Slug:          "active-zero",
			Status:        StatusActive,
			CategorySlugs: []string{"pantry"},
		},
	}
}

func memoryStoreTestCategories() []Category {
	return []Category{
		{Slug: "pantry", Status: StatusActive},
		{Slug: "draft-category", Status: StatusDraft},
		{Slug: "home-decor", Status: StatusActive},
	}
}

func productSlugs(products []Product) []string {
	slugs := make([]string, len(products))
	for i, product := range products {
		slugs[i] = product.Slug
	}
	return slugs
}

func categorySlugs(categories []Category) []string {
	slugs := make([]string, len(categories))
	for i, category := range categories {
		slugs[i] = category.Slug
	}
	return slugs
}
