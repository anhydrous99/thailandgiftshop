package checkout

import (
	"context"
	"errors"

	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
)

// errStockStoreNotConfigured fails stock-touching paths fast when no stock
// store was wired (the EmptyStore local-only configuration).
var errStockStoreNotConfigured = errors.New("checkout: stock store not configured")

// ReleaseStockForOrder returns the stock an order reserved, keyed by product
// ID so the release survives admin slug edits made after the reservation.
// Sequential multi-product application and best-effort compensation live in
// catalog.AdjustStock. Shared by the webhook failure paths, the stale
// pending-pointer cleanup, and the admin cancel flow.
func ReleaseStockForOrder(ctx context.Context, stock catalog.StockStore, order commerce.Order) error {
	adjustments := stockAdjustmentsForLines(order.Lines, 1)
	if len(adjustments) == 0 {
		return nil
	}
	if stock == nil {
		return errStockStoreNotConfigured
	}

	return stock.AdjustStock(ctx, adjustments)
}

// reserveStockForOrder decrements stock for the priced lines before the order
// row exists. catalog.AdjustStock applies products sequentially and
// compensates already-reserved products when a later one fails, so a partial
// reservation never leaks out of this call.
func reserveStockForOrder(ctx context.Context, stock catalog.StockStore, lines []commerce.OrderLine) error {
	adjustments := stockAdjustmentsForLines(lines, -1)
	if len(adjustments) == 0 {
		return nil
	}
	if stock == nil {
		return errStockStoreNotConfigured
	}

	return stock.AdjustStock(ctx, adjustments)
}

// stockAdjustmentsForLines maps order lines onto stock deltas: sign -1
// reserves, sign +1 releases. Zero-quantity lines are skipped; product IDs
// pass through untouched so catalog.AdjustStock validates them.
func stockAdjustmentsForLines(lines []commerce.OrderLine, sign int) []catalog.StockAdjustment {
	adjustments := make([]catalog.StockAdjustment, 0, len(lines))
	for _, line := range lines {
		if line.Quantity <= 0 {
			continue
		}
		adjustments = append(adjustments, catalog.StockAdjustment{
			ProductID: line.ProductID,
			VariantID: line.VariantID,
			Delta:     sign * line.Quantity,
		})
	}

	return adjustments
}
