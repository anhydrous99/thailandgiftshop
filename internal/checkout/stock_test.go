package checkout

import (
	"context"
	"errors"
	"testing"

	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/payments"
)

func newStockTestStore() *catalog.MemoryStore {
	return catalog.NewMemoryStore(
		[]catalog.Product{checkoutTestBaseProduct(), checkoutTestVariantProduct()},
		nil,
	)
}

func stockTestQuantities(t *testing.T, store *catalog.MemoryStore) (int, int) {
	t.Helper()
	base, found, err := store.GetProductByID(context.Background(), "prod_base")
	if err != nil || !found {
		t.Fatalf("GetProductByID(prod_base) = found %t, err %v", found, err)
	}
	variant, found, err := store.GetProductByID(context.Background(), "prod_variant")
	if err != nil || !found {
		t.Fatalf("GetProductByID(prod_variant) = found %t, err %v", found, err)
	}
	variantStock, ok := variant.AvailableStockForVariant("var_m")
	if !ok {
		t.Fatalf("var_m not found on prod_variant")
	}
	return base.StockQuantity, variantStock
}

func TestReserveStockForOrder(t *testing.T) {
	tests := []struct {
		name        string
		lines       []commerce.OrderLine
		wantErr     bool
		wantBase    int
		wantVariant int
	}{
		{
			name:        "reserves product and variant stock",
			lines:       checkoutTestLines(),
			wantBase:    3,
			wantVariant: 3,
		},
		{
			name: "skips zero-quantity lines",
			lines: []commerce.OrderLine{
				{ProductID: "prod_base", Quantity: 0},
				{ProductID: "prod_variant", VariantID: "var_m", Quantity: 2},
			},
			wantBase:    5,
			wantVariant: 2,
		},
		{
			name: "insufficient stock leaves everything untouched",
			lines: []commerce.OrderLine{
				{ProductID: "prod_base", Slug: "carved-coconut-bowl", Quantity: 9},
			},
			wantErr:     true,
			wantBase:    5,
			wantVariant: 4,
		},
		{
			name: "later product failure compensates the earlier reservation",
			lines: []commerce.OrderLine{
				{ProductID: "prod_base", Slug: "carved-coconut-bowl", Quantity: 2},
				{ProductID: "prod_variant", Slug: "handwoven-indigo-scarf", VariantID: "var_m", Quantity: 9},
			},
			wantErr:     true,
			wantBase:    5,
			wantVariant: 4,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newStockTestStore()
			err := reserveStockForOrder(context.Background(), store, test.lines)
			if test.wantErr && err == nil {
				t.Fatalf("reserveStockForOrder succeeded, want error")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("reserveStockForOrder returned error: %v", err)
			}
			if test.wantErr {
				var insufficient catalog.InsufficientStockError
				if !errors.As(err, &insufficient) {
					t.Errorf("error = %v, want catalog.InsufficientStockError", err)
				}
			}
			base, variant := stockTestQuantities(t, store)
			if base != test.wantBase || variant != test.wantVariant {
				t.Errorf("stock = base %d / var_m %d, want %d / %d", base, variant, test.wantBase, test.wantVariant)
			}
		})
	}
}

func TestReleaseStockForOrder(t *testing.T) {
	store := newStockTestStore()
	if err := reserveStockForOrder(context.Background(), store, checkoutTestLines()); err != nil {
		t.Fatalf("reserveStockForOrder returned error: %v", err)
	}

	order := commerce.Order{ID: "order00000000000000000000a", Lines: checkoutTestLines()}
	if err := ReleaseStockForOrder(context.Background(), store, order); err != nil {
		t.Fatalf("ReleaseStockForOrder returned error: %v", err)
	}
	base, variant := stockTestQuantities(t, store)
	if base != 5 || variant != 4 {
		t.Errorf("stock after release = base %d / var_m %d, want 5 / 4", base, variant)
	}
}

func TestStockHelpersWithoutStockStore(t *testing.T) {
	lines := checkoutTestLines()

	if err := reserveStockForOrder(context.Background(), nil, lines); err == nil {
		t.Errorf("reserveStockForOrder with nil store succeeded, want error")
	}
	if err := ReleaseStockForOrder(context.Background(), nil, commerce.Order{Lines: lines}); err == nil {
		t.Errorf("ReleaseStockForOrder with nil store succeeded, want error")
	}
	// No adjustments means no stock store is needed at all.
	if err := reserveStockForOrder(context.Background(), nil, nil); err != nil {
		t.Errorf("reserveStockForOrder with no lines returned error: %v", err)
	}
	if err := ReleaseStockForOrder(context.Background(), nil, commerce.Order{}); err != nil {
		t.Errorf("ReleaseStockForOrder with no lines returned error: %v", err)
	}
}

func TestStockAdjustmentsForLines(t *testing.T) {
	lines := []commerce.OrderLine{
		{ProductID: "prod_base", Quantity: 2},
		{ProductID: "prod_variant", VariantID: "var_m", Quantity: 1},
		{ProductID: "prod_skip", Quantity: 0},
	}

	reserve := stockAdjustmentsForLines(lines, -1)
	want := []catalog.StockAdjustment{
		{ProductID: "prod_base", Delta: -2},
		{ProductID: "prod_variant", VariantID: "var_m", Delta: -1},
	}
	if len(reserve) != len(want) {
		t.Fatalf("reserve adjustments = %+v, want %+v", reserve, want)
	}
	for index := range want {
		if reserve[index] != want[index] {
			t.Errorf("reserve adjustment[%d] = %+v, want %+v", index, reserve[index], want[index])
		}
	}

	release := stockAdjustmentsForLines(lines, 1)
	if len(release) != 2 || release[0].Delta != 2 || release[1].Delta != 1 {
		t.Errorf("release adjustments = %+v, want positive deltas 2 and 1", release)
	}
}

func TestSessionLinesComposeVariantNames(t *testing.T) {
	lines := sessionLines(checkoutTestLines())
	want := []payments.SessionLine{
		{Name: "Carved Coconut Bowl", UnitAmountCents: 1899, Quantity: 2},
		{Name: "Handwoven Indigo Scarf — M", UnitAmountCents: 3499, Quantity: 1},
	}
	if len(lines) != len(want) {
		t.Fatalf("session lines = %+v, want %+v", lines, want)
	}
	for index := range want {
		if lines[index] != want[index] {
			t.Errorf("session line[%d] = %+v, want %+v", index, lines[index], want[index])
		}
	}
}
