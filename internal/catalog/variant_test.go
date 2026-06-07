package catalog

import (
	"strings"
	"testing"
)

func TestProductVariantStockHelpersUseActiveVariantsOnly(t *testing.T) {
	product := Product{
		StockQuantity: 99,
		Variants: []ProductVariant{
			{ID: "var_large", Label: "Large", StockQuantity: 3, Status: StatusActive, SortOrder: 20},
			{ID: "var_archived", Label: "Large", StockQuantity: 8, Status: StatusArchived, SortOrder: 5},
			{ID: "var_draft", Label: "Draft", StockQuantity: 13, Status: StatusDraft, SortOrder: 15},
			{ID: "var_small", Label: "Small", StockQuantity: 2, Status: StatusActive, SortOrder: 10},
		},
	}

	if !product.UsesVariants() {
		t.Fatal("UsesVariants = false, want true when variants are present")
	}

	active := product.ActiveVariants()
	if len(active) != 2 {
		t.Fatalf("ActiveVariants length = %d, want 2", len(active))
	}
	if active[0].ID != "var_small" || active[1].ID != "var_large" {
		t.Fatalf("ActiveVariants order = %#v, want active variants sorted by sort order", active)
	}

	stock, ok := product.AvailableStockForVariant("var_large")
	if !ok || stock != 3 {
		t.Fatalf("AvailableStockForVariant(var_large) = %d, %v; want 3, true", stock, ok)
	}
	if stock, ok := product.AvailableStockForVariant("var_archived"); ok || stock != 0 {
		t.Fatalf("AvailableStockForVariant(var_archived) = %d, %v; want 0, false", stock, ok)
	}
	if stock, ok := product.AvailableStockForVariant("missing"); ok || stock != 0 {
		t.Fatalf("AvailableStockForVariant(missing) = %d, %v; want 0, false", stock, ok)
	}

	if got := product.TotalAvailableStock(); got != 5 {
		t.Fatalf("TotalAvailableStock = %d, want active variant stock sum 5", got)
	}
	if product.OutOfStock() {
		t.Fatal("OutOfStock = true, want false when active variant stock is available")
	}

	active[0].Label = "mutated result"
	if got := product.Variants[3].Label; got != "Small" {
		t.Fatalf("ActiveVariants mutated source label to %q", got)
	}
}

func TestProductLegacyStockCompatibilityWithoutVariants(t *testing.T) {
	product := Product{StockQuantity: 4}

	if product.UsesVariants() {
		t.Fatal("UsesVariants = true, want false without variants")
	}
	if active := product.ActiveVariants(); len(active) != 0 {
		t.Fatalf("ActiveVariants length = %d, want 0", len(active))
	}
	if got := product.TotalAvailableStock(); got != 4 {
		t.Fatalf("TotalAvailableStock = %d, want legacy stock quantity 4", got)
	}
	if product.OutOfStock() {
		t.Fatal("OutOfStock = true, want false when legacy stock is positive")
	}
	stock, ok := product.AvailableStockForVariant("")
	if !ok || stock != 4 {
		t.Fatalf("AvailableStockForVariant(empty legacy id) = %d, %v; want 4, true", stock, ok)
	}
	if stock, ok := product.AvailableStockForVariant("var_missing"); ok || stock != 0 {
		t.Fatalf("AvailableStockForVariant(non-empty legacy id) = %d, %v; want 0, false", stock, ok)
	}

	zeroStock := Product{StockQuantity: 0}
	if !zeroStock.OutOfStock() {
		t.Fatal("OutOfStock = false, want true when legacy stock quantity is zero")
	}
}

func TestProductVariantValidationRejectsEmptyActiveLabels(t *testing.T) {
	product := Product{Variants: []ProductVariant{{ID: "var_blank", Label: " \t", Status: StatusActive}}}

	err := product.Validate()
	if err == nil {
		t.Fatal("Validate returned nil, want error for empty active variant label")
	}
	if !strings.Contains(err.Error(), "active variant label") {
		t.Fatalf("Validate error = %q, want active variant label context", err)
	}
}

func TestProductVariantValidationRejectsDuplicateActiveLabelsAfterTrimCaseFold(t *testing.T) {
	product := Product{Variants: []ProductVariant{
		{ID: "var_small", Label: " Small ", Status: StatusActive},
		{ID: "var_small_duplicate", Label: "small", Status: StatusActive},
	}}

	err := product.Validate()
	if err == nil {
		t.Fatal("Validate returned nil, want error for duplicate active variant labels")
	}
	if !strings.Contains(err.Error(), "duplicate active variant label") {
		t.Fatalf("Validate error = %q, want duplicate active variant label context", err)
	}
}

func TestProductVariantValidationAllowsArchivedDuplicateLabelsWhenActiveUnique(t *testing.T) {
	product := Product{Variants: []ProductVariant{
		{ID: "var_small", Label: "Small", Status: StatusActive},
		{ID: "var_small_archived", Label: " small ", Status: StatusArchived},
		{ID: "var_large", Label: "Large", Status: StatusActive},
	}}

	if err := product.Validate(); err != nil {
		t.Fatalf("Validate returned error for archived duplicate label: %v", err)
	}
	if got := product.TotalAvailableStock(); got != 0 {
		t.Fatalf("TotalAvailableStock = %d, want 0 when active variants have zero stock", got)
	}
	if !product.OutOfStock() {
		t.Fatal("OutOfStock = false, want true when active variants have zero stock")
	}
}

func TestProductVariantValidationRejectsDuplicateActiveIDsForDistinctLabels(t *testing.T) {
	product := Product{Variants: []ProductVariant{
		{ID: "var_s-m", Label: "S/M", Status: StatusActive},
		{ID: "var_s-m", Label: "S M", Status: StatusActive},
	}}

	err := product.Validate()
	if err == nil {
		t.Fatal("Validate returned nil, want error for duplicate active variant IDs")
	}
	if !strings.Contains(err.Error(), "duplicate active variant ID") {
		t.Fatalf("Validate error = %q, want duplicate active variant ID context", err)
	}
}

func TestValidRouteSlugRejectsUnsafeRouteCharacters(t *testing.T) {
	for _, slug := range []string{"safe-slug-123", "a"} {
		if !ValidRouteSlug(slug) {
			t.Fatalf("ValidRouteSlug(%q) = false, want true", slug)
		}
	}

	for _, slug := range []string{"", "bad slug", "bad/slug", "BadSlug", "bad_slug"} {
		if ValidRouteSlug(slug) {
			t.Fatalf("ValidRouteSlug(%q) = true, want false", slug)
		}
	}
}
