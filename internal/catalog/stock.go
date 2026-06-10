package catalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/anhydrous99/thailandgiftshop/internal/observability"
)

const stockAdjustMaxAttempts = 5

const (
	stockOutcomeReserve        = "reserve"
	stockOutcomeRelease        = "release"
	stockOutcomeConflict       = "conflict"
	stockOutcomeRollbackError  = "rollback_error"
	stockOutcomeReleaseSkipped = "release_skipped"
)

var _ StockStore = (*DynamoStore)(nil)
var _ StockStore = (*MemoryStore)(nil)

// stockLogger writes structured JSON lines to stdout, where the Lambda
// runtime forwards them to CloudWatch Logs alongside the EMF metric records.
var stockLogger = slog.New(slog.NewJSONHandler(os.Stdout, nil))

// stockProductStore is the slice of the admin store the stock adjustment
// algorithm needs. DynamoStore and MemoryStore both satisfy it, which keeps
// their AdjustStock behavior identical by construction.
type stockProductStore interface {
	GetProductByID(ctx context.Context, productID string) (Product, bool, error)
	UpdateProduct(ctx context.Context, previous Product, product Product) (Product, error)
}

// AdjustStock applies the adjustments grouped per product through the
// existing versioned UpdateProduct transaction path (product put, slug lock,
// membership-row rewrite) so denormalized category stock stays in sync.
func (s *DynamoStore) AdjustStock(ctx context.Context, adjustments []StockAdjustment) error {
	return adjustStock(ctx, dynamoStockWriter{store: s}, s.metrics, adjustments)
}

// AdjustStock mirrors the DynamoStore behavior for local demo and test runs.
func (s *MemoryStore) AdjustStock(ctx context.Context, adjustments []StockAdjustment) error {
	return adjustStock(ctx, s, nil, adjustments)
}

// dynamoStockWriter routes stock writes through the shared updateProduct
// transaction while labeling the catalog-operation metrics "AdjustStock".
type dynamoStockWriter struct {
	store *DynamoStore
}

func (w dynamoStockWriter) GetProductByID(ctx context.Context, productID string) (Product, bool, error) {
	return w.store.GetProductByID(ctx, productID)
}

func (w dynamoStockWriter) UpdateProduct(ctx context.Context, previous Product, product Product) (Product, error) {
	return w.store.updateProduct(ctx, previous, product, "AdjustStock")
}

type productStockAdjustments struct {
	productID   string
	adjustments []StockAdjustment
}

func adjustStock(ctx context.Context, store stockProductStore, metrics observability.Recorder, adjustments []StockAdjustment) error {
	groups, err := groupStockAdjustments(adjustments)
	if err != nil {
		return err
	}
	for index, group := range groups {
		if err := adjustProductStock(ctx, store, metrics, group); err != nil {
			compensateStockAdjustments(ctx, store, metrics, groups[:index])
			return err
		}
	}

	return nil
}

func groupStockAdjustments(adjustments []StockAdjustment) ([]productStockAdjustments, error) {
	groups := make([]productStockAdjustments, 0, len(adjustments))
	indexByProduct := map[string]int{}
	for _, adjustment := range adjustments {
		if adjustment.ProductID == "" {
			return nil, errors.New("stock adjustment is missing a product ID")
		}
		index, ok := indexByProduct[adjustment.ProductID]
		if !ok {
			index = len(groups)
			indexByProduct[adjustment.ProductID] = index
			groups = append(groups, productStockAdjustments{productID: adjustment.ProductID})
		}
		groups[index].adjustments = append(groups[index].adjustments, adjustment)
	}

	return groups, nil
}

func adjustProductStock(ctx context.Context, store stockProductStore, metrics observability.Recorder, group productStockAdjustments) error {
	for attempt := 0; attempt < stockAdjustMaxAttempts; attempt++ {
		product, found, err := store.GetProductByID(ctx, group.productID)
		if err != nil {
			return err
		}
		if !found {
			if stockAdjustOutcome(group.adjustments) == stockOutcomeRelease {
				// The product was deleted after the reservation; there is no
				// stock row left to return units to, so the release skips it
				// rather than aborting the rest of the order's release.
				recordStockAdjust(metrics, stockOutcomeReleaseSkipped)
				stockLogger.Warn("skipping stock release for missing product",
					slog.String("service", "catalog"),
					slog.String("product_id", group.productID),
				)
				return nil
			}
			return fmt.Errorf("catalog product %q not found for stock adjustment", group.productID)
		}
		next, skipped, err := applyStockAdjustments(product, group.adjustments)
		if err != nil {
			return err
		}
		if len(skipped) == len(group.adjustments) {
			recordSkippedStockReleases(metrics, skipped)
			return nil
		}
		if _, err := store.UpdateProduct(ctx, product, next); err != nil {
			if errors.Is(err, ErrVersionConflict) || isRetryableTransactionConflict(err) {
				recordStockAdjust(metrics, stockOutcomeConflict)
				continue
			}
			return err
		}
		recordSkippedStockReleases(metrics, skipped)
		recordStockAdjust(metrics, stockAdjustOutcome(group.adjustments))
		return nil
	}

	return fmt.Errorf("stock adjustment for product %q failed after %d attempts: %w", group.productID, stockAdjustMaxAttempts, ErrVersionConflict)
}

// applyStockAdjustments applies the grouped deltas to a copy of the product.
// Positive deltas (releases) targeting a variant that no longer exists are
// returned as skipped instead of failing: the variant was removed between the
// reservation and the release, so there is no stock row to return units to.
// Negative deltas (reserves) keep strict missing-variant behavior.
func applyStockAdjustments(product Product, adjustments []StockAdjustment) (Product, []StockAdjustment, error) {
	next := cloneProduct(product)
	var skipped []StockAdjustment
	for _, adjustment := range adjustments {
		if adjustment.VariantID == "" {
			updated := next.StockQuantity + adjustment.Delta
			if updated < 0 {
				return Product{}, nil, InsufficientStockError{
					ProductID: product.ID,
					Slug:      product.Slug,
					Available: product.StockQuantity,
				}
			}
			next.StockQuantity = updated
			continue
		}

		index := variantIndexByID(next.Variants, adjustment.VariantID)
		if index == -1 {
			if adjustment.Delta > 0 {
				skipped = append(skipped, adjustment)
				continue
			}
			return Product{}, nil, InsufficientStockError{
				ProductID: product.ID,
				Slug:      product.Slug,
				VariantID: adjustment.VariantID,
				Available: 0,
			}
		}
		updated := next.Variants[index].StockQuantity + adjustment.Delta
		if updated < 0 {
			return Product{}, nil, InsufficientStockError{
				ProductID: product.ID,
				Slug:      product.Slug,
				VariantID: adjustment.VariantID,
				Available: product.Variants[index].StockQuantity,
			}
		}
		next.Variants[index].StockQuantity = updated
	}

	return next, skipped, nil
}

// recordSkippedStockReleases warns and counts each positive-delta adjustment
// that was dropped because its variant vanished after the reservation.
func recordSkippedStockReleases(metrics observability.Recorder, skipped []StockAdjustment) {
	for _, adjustment := range skipped {
		recordStockAdjust(metrics, stockOutcomeReleaseSkipped)
		stockLogger.Warn("skipping stock release for missing variant",
			slog.String("service", "catalog"),
			slog.String("product_id", adjustment.ProductID),
			slog.String("variant_id", adjustment.VariantID),
			slog.Int("delta", adjustment.Delta),
		)
	}
}

func variantIndexByID(variants []ProductVariant, variantID string) int {
	for index, variant := range variants {
		if variant.ID == variantID {
			return index
		}
	}

	return -1
}

// compensateStockAdjustments best-effort releases the stock that earlier
// products in the same call already reserved before a later product failed.
// Only negative deltas are inverted: re-reserving an already released line
// could itself fail on insufficient stock, and extra released stock is
// harmless because no money has moved yet. A compensation failure is alarmed
// (Outcome=rollback_error) and logged for manual stock repair.
func compensateStockAdjustments(ctx context.Context, store stockProductStore, metrics observability.Recorder, applied []productStockAdjustments) {
	for _, group := range applied {
		releases := make([]StockAdjustment, 0, len(group.adjustments))
		for _, adjustment := range group.adjustments {
			if adjustment.Delta >= 0 {
				continue
			}
			releases = append(releases, StockAdjustment{
				ProductID: adjustment.ProductID,
				VariantID: adjustment.VariantID,
				Delta:     -adjustment.Delta,
			})
		}
		if len(releases) == 0 {
			continue
		}
		if err := adjustProductStock(ctx, store, metrics, productStockAdjustments{productID: group.productID, adjustments: releases}); err != nil {
			recordStockAdjust(metrics, stockOutcomeRollbackError)
			stockLogger.Error("stock adjustment compensation failed",
				slog.String("service", "catalog"),
				slog.String("product_id", group.productID),
				slog.String("error", err.Error()),
			)
		}
	}
}

func stockAdjustOutcome(adjustments []StockAdjustment) string {
	for _, adjustment := range adjustments {
		if adjustment.Delta < 0 {
			return stockOutcomeReserve
		}
	}

	return stockOutcomeRelease
}

func recordStockAdjust(metrics observability.Recorder, outcome string) {
	if metrics == nil {
		return
	}
	metrics.Record(observability.Count(
		observability.MetricStockAdjust,
		observability.Dim("Service", "catalog"),
		observability.Dim("Outcome", outcome),
	))
}
