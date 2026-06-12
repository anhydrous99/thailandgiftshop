package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/appenv"
	"github.com/anhydrous99/thailandgiftshop/internal/observability"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func stockTestBaseProduct() Product {
	return Product{
		ID:            "prod_base",
		Slug:          "carved-coconut-bowl",
		Name:          "Carved Coconut Bowl",
		PriceCents:    1899,
		Status:        StatusActive,
		SortOrder:     10,
		StockQuantity: 5,
		Version:       3,
		CreatedAt:     time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 6, 5, 9, 0, 0, 0, time.UTC),
		CategorySlugs: []string{"home-decor"},
	}
}

func stockTestVariantProduct() Product {
	return Product{
		ID:         "prod_variant",
		Slug:       "handwoven-indigo-scarf",
		Name:       "Handwoven Indigo Scarf",
		PriceCents: 3499,
		Status:     StatusActive,
		SortOrder:  20,
		Version:    2,
		CreatedAt:  time.Date(2026, 6, 2, 9, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 6, 5, 9, 0, 0, 0, time.UTC),
		Variants: []ProductVariant{
			{ID: "var_s", Label: "S", StockQuantity: 1, Status: StatusActive, SortOrder: 10},
			{ID: "var_m", Label: "M", StockQuantity: 4, Status: StatusActive, SortOrder: 20},
		},
		CategorySlugs: []string{"textiles"},
	}
}

func variantStock(t *testing.T, product Product, variantID string) int {
	t.Helper()
	index := variantIndexByID(product.Variants, variantID)
	if index == -1 {
		t.Fatalf("variant %q not found on product %q", variantID, product.ID)
	}
	return product.Variants[index].StockQuantity
}

func mustGetProduct(t *testing.T, store *MemoryStore, productID string) Product {
	t.Helper()
	product, ok, err := store.GetProductByID(context.Background(), productID)
	if err != nil {
		t.Fatalf("GetProductByID(%q) returned error: %v", productID, err)
	}
	if !ok {
		t.Fatalf("GetProductByID(%q) ok = false, want true", productID)
	}
	return product
}

func TestMemoryStoreAdjustStockReserveAndRelease(t *testing.T) {
	tests := []struct {
		name        string
		adjustments []StockAdjustment
		wantBase    int
		wantS       int
		wantM       int
	}{
		{
			name:        "reserve base product stock",
			adjustments: []StockAdjustment{{ProductID: "prod_base", Delta: -2}},
			wantBase:    3,
			wantS:       1,
			wantM:       4,
		},
		{
			name:        "release base product stock",
			adjustments: []StockAdjustment{{ProductID: "prod_base", Delta: 2}},
			wantBase:    7,
			wantS:       1,
			wantM:       4,
		},
		{
			name:        "reserve entire base stock to zero",
			adjustments: []StockAdjustment{{ProductID: "prod_base", Delta: -5}},
			wantBase:    0,
			wantS:       1,
			wantM:       4,
		},
		{
			name:        "reserve variant-level stock",
			adjustments: []StockAdjustment{{ProductID: "prod_variant", VariantID: "var_m", Delta: -3}},
			wantBase:    5,
			wantS:       1,
			wantM:       1,
		},
		{
			name:        "release variant-level stock",
			adjustments: []StockAdjustment{{ProductID: "prod_variant", VariantID: "var_s", Delta: 4}},
			wantBase:    5,
			wantS:       5,
			wantM:       4,
		},
		{
			name: "reserve across products and variants",
			adjustments: []StockAdjustment{
				{ProductID: "prod_base", Delta: -1},
				{ProductID: "prod_variant", VariantID: "var_s", Delta: -1},
				{ProductID: "prod_variant", VariantID: "var_m", Delta: -2},
			},
			wantBase: 4,
			wantS:    0,
			wantM:    2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore([]Product{stockTestBaseProduct(), stockTestVariantProduct()}, nil)

			if err := store.AdjustStock(context.Background(), test.adjustments); err != nil {
				t.Fatalf("AdjustStock returned error: %v", err)
			}

			base := mustGetProduct(t, store, "prod_base")
			if base.StockQuantity != test.wantBase {
				t.Fatalf("base stock = %d, want %d", base.StockQuantity, test.wantBase)
			}
			variant := mustGetProduct(t, store, "prod_variant")
			if got := variantStock(t, variant, "var_s"); got != test.wantS {
				t.Fatalf("var_s stock = %d, want %d", got, test.wantS)
			}
			if got := variantStock(t, variant, "var_m"); got != test.wantM {
				t.Fatalf("var_m stock = %d, want %d", got, test.wantM)
			}
		})
	}
}

func TestMemoryStoreAdjustStockGroupsDeltasIntoOneVersionedWritePerProduct(t *testing.T) {
	store := NewMemoryStore([]Product{stockTestVariantProduct()}, nil)

	err := store.AdjustStock(context.Background(), []StockAdjustment{
		{ProductID: "prod_variant", VariantID: "var_s", Delta: -1},
		{ProductID: "prod_variant", VariantID: "var_m", Delta: -2},
	})
	if err != nil {
		t.Fatalf("AdjustStock returned error: %v", err)
	}

	product := mustGetProduct(t, store, "prod_variant")
	if product.Version != 3 {
		t.Fatalf("product version = %d, want one bump to 3 for grouped deltas", product.Version)
	}
	if got := variantStock(t, product, "var_s"); got != 0 {
		t.Fatalf("var_s stock = %d, want 0", got)
	}
	if got := variantStock(t, product, "var_m"); got != 2 {
		t.Fatalf("var_m stock = %d, want 2", got)
	}
}

func TestMemoryStoreAdjustStockInsufficientStock(t *testing.T) {
	tests := []struct {
		name        string
		adjustments []StockAdjustment
		want        InsufficientStockError
	}{
		{
			name:        "base product short",
			adjustments: []StockAdjustment{{ProductID: "prod_base", Delta: -6}},
			want:        InsufficientStockError{ProductID: "prod_base", Slug: "carved-coconut-bowl", Available: 5},
		},
		{
			name:        "variant short",
			adjustments: []StockAdjustment{{ProductID: "prod_variant", VariantID: "var_s", Delta: -2}},
			want:        InsufficientStockError{ProductID: "prod_variant", Slug: "handwoven-indigo-scarf", VariantID: "var_s", Available: 1},
		},
		{
			name:        "unknown variant",
			adjustments: []StockAdjustment{{ProductID: "prod_variant", VariantID: "var_missing", Delta: -1}},
			want:        InsufficientStockError{ProductID: "prod_variant", Slug: "handwoven-indigo-scarf", VariantID: "var_missing", Available: 0},
		},
		{
			name: "second delta against same variant short reports pre-call stock",
			adjustments: []StockAdjustment{
				{ProductID: "prod_variant", VariantID: "var_m", Delta: -3},
				{ProductID: "prod_variant", VariantID: "var_m", Delta: -2},
			},
			want: InsufficientStockError{ProductID: "prod_variant", Slug: "handwoven-indigo-scarf", VariantID: "var_m", Available: 4},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore([]Product{stockTestBaseProduct(), stockTestVariantProduct()}, nil)

			err := store.AdjustStock(context.Background(), test.adjustments)
			var insufficient InsufficientStockError
			if !errors.As(err, &insufficient) {
				t.Fatalf("AdjustStock err = %v, want InsufficientStockError", err)
			}
			if insufficient != test.want {
				t.Fatalf("InsufficientStockError = %#v, want %#v", insufficient, test.want)
			}

			base := mustGetProduct(t, store, "prod_base")
			if base.StockQuantity != 5 || base.Version != 3 {
				t.Fatalf("base product changed: stock %d version %d, want untouched 5/3", base.StockQuantity, base.Version)
			}
			variant := mustGetProduct(t, store, "prod_variant")
			if got := variantStock(t, variant, "var_s"); got != 1 {
				t.Fatalf("var_s stock = %d, want untouched 1", got)
			}
			if got := variantStock(t, variant, "var_m"); got != 4 {
				t.Fatalf("var_m stock = %d, want untouched 4", got)
			}
			if variant.Version != 2 {
				t.Fatalf("variant product version = %d, want untouched 2", variant.Version)
			}
		})
	}
}

func TestInsufficientStockErrorMessage(t *testing.T) {
	base := InsufficientStockError{ProductID: "prod_base", Slug: "carved-coconut-bowl", Available: 2}
	if got := base.Error(); !strings.Contains(got, "prod_base") || !strings.Contains(got, "2 available") {
		t.Fatalf("base error message = %q", got)
	}
	variant := InsufficientStockError{ProductID: "prod_variant", VariantID: "var_s", Available: 0}
	if got := variant.Error(); !strings.Contains(got, "var_s") || !strings.Contains(got, "0 available") {
		t.Fatalf("variant error message = %q", got)
	}
}

func TestMemoryStoreAdjustStockMultiProductCompensationReleasesEarlierReserves(t *testing.T) {
	store := NewMemoryStore([]Product{stockTestBaseProduct(), stockTestVariantProduct()}, nil)

	err := store.AdjustStock(context.Background(), []StockAdjustment{
		{ProductID: "prod_base", Delta: -2},
		{ProductID: "prod_variant", VariantID: "var_s", Delta: -3},
	})
	var insufficient InsufficientStockError
	if !errors.As(err, &insufficient) {
		t.Fatalf("AdjustStock err = %v, want InsufficientStockError", err)
	}
	if insufficient.ProductID != "prod_variant" {
		t.Fatalf("InsufficientStockError.ProductID = %q, want prod_variant", insufficient.ProductID)
	}

	base := mustGetProduct(t, store, "prod_base")
	if base.StockQuantity != 5 {
		t.Fatalf("base stock after compensation = %d, want restored 5", base.StockQuantity)
	}
	if base.Version != 5 {
		t.Fatalf("base version = %d, want 5 after reserve and compensating release", base.Version)
	}
	variant := mustGetProduct(t, store, "prod_variant")
	if got := variantStock(t, variant, "var_s"); got != 1 {
		t.Fatalf("var_s stock = %d, want untouched 1", got)
	}
}

func TestMemoryStoreAdjustStockCompensationReversesEarlierReleases(t *testing.T) {
	store := NewMemoryStore([]Product{stockTestBaseProduct(), stockTestVariantProduct()}, nil)

	err := store.AdjustStock(context.Background(), []StockAdjustment{
		{ProductID: "prod_base", Delta: 2},
		{ProductID: "prod_variant", VariantID: "var_s", Delta: -3},
	})
	var insufficient InsufficientStockError
	if !errors.As(err, &insufficient) {
		t.Fatalf("AdjustStock err = %v, want InsufficientStockError", err)
	}

	base := mustGetProduct(t, store, "prod_base")
	if base.StockQuantity != 5 {
		t.Fatalf("base stock after failed release = %d, want restored 5", base.StockQuantity)
	}
	if base.Version != 5 {
		t.Fatalf("base version = %d, want release and compensating reserve to bump to 5", base.Version)
	}
}

func TestMemoryStoreAdjustStockReleaseSkipsMissingVariantAndContinues(t *testing.T) {
	store := NewMemoryStore([]Product{stockTestBaseProduct(), stockTestVariantProduct()}, nil)

	err := store.AdjustStock(context.Background(), []StockAdjustment{
		{ProductID: "prod_variant", VariantID: "var_gone", Delta: 2},
		{ProductID: "prod_variant", VariantID: "var_m", Delta: 1},
		{ProductID: "prod_base", Delta: 2},
	})
	if err != nil {
		t.Fatalf("AdjustStock returned error: %v", err)
	}

	variant := mustGetProduct(t, store, "prod_variant")
	if got := variantStock(t, variant, "var_m"); got != 5 {
		t.Fatalf("var_m stock = %d, want released 5", got)
	}
	if got := variantStock(t, variant, "var_s"); got != 1 {
		t.Fatalf("var_s stock = %d, want untouched 1", got)
	}
	base := mustGetProduct(t, store, "prod_base")
	if base.StockQuantity != 7 {
		t.Fatalf("base stock = %d, want released 7 despite the vanished variant", base.StockQuantity)
	}
}

func TestMemoryStoreAdjustStockReleaseSkipsMissingProductAndContinues(t *testing.T) {
	store := NewMemoryStore([]Product{stockTestBaseProduct()}, nil)

	err := store.AdjustStock(context.Background(), []StockAdjustment{
		{ProductID: "prod_deleted", Delta: 2},
		{ProductID: "prod_base", Delta: 3},
	})
	if err != nil {
		t.Fatalf("AdjustStock returned error: %v", err)
	}

	base := mustGetProduct(t, store, "prod_base")
	if base.StockQuantity != 8 {
		t.Fatalf("base stock = %d, want released 8 despite the deleted product", base.StockQuantity)
	}
}

func TestMemoryStoreAdjustStockValidation(t *testing.T) {
	store := NewMemoryStore([]Product{stockTestBaseProduct()}, nil)

	if err := store.AdjustStock(context.Background(), nil); err != nil {
		t.Fatalf("AdjustStock with no adjustments returned error: %v", err)
	}

	err := store.AdjustStock(context.Background(), []StockAdjustment{{ProductID: "", Delta: -1}})
	if err == nil || !strings.Contains(err.Error(), "missing a product ID") {
		t.Fatalf("AdjustStock missing product ID err = %v", err)
	}

	err = store.AdjustStock(context.Background(), []StockAdjustment{{ProductID: "prod_missing", Delta: -1}})
	if err == nil || !strings.Contains(err.Error(), "prod_missing") {
		t.Fatalf("AdjustStock unknown product err = %v", err)
	}
	base := mustGetProduct(t, store, "prod_base")
	if base.StockQuantity != 5 || base.Version != 3 {
		t.Fatalf("base product changed: stock %d version %d, want untouched 5/3", base.StockQuantity, base.Version)
	}
}

type fakeStockClient struct {
	getInputs   []dynamodb.GetItemInput
	getOutputs  map[string][]*dynamodb.GetItemOutput
	getErr      error
	writeInputs []dynamodb.TransactWriteItemsInput
	writeErrs   []error
}

func (f *fakeStockClient) Query(ctx context.Context, input *dynamodb.QueryInput, optFns ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	return &dynamodb.QueryOutput{}, nil
}

func (f *fakeStockClient) GetItem(ctx context.Context, input *dynamodb.GetItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.getInputs = append(f.getInputs, *input)
	if f.getErr != nil {
		return nil, f.getErr
	}
	pk := ""
	if value, ok := input.Key["pk"].(*types.AttributeValueMemberS); ok {
		pk = value.Value
	}
	queue := f.getOutputs[pk]
	if len(queue) == 0 {
		return &dynamodb.GetItemOutput{}, nil
	}
	output := queue[0]
	if len(queue) > 1 {
		f.getOutputs[pk] = queue[1:]
	}
	return output, nil
}

func (f *fakeStockClient) TransactWriteItems(ctx context.Context, input *dynamodb.TransactWriteItemsInput, optFns ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	f.writeInputs = append(f.writeInputs, *input)
	if len(f.writeErrs) > 0 {
		err := f.writeErrs[0]
		f.writeErrs = f.writeErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	return &dynamodb.TransactWriteItemsOutput{}, nil
}

func stockGetOutput(t *testing.T, product Product) *dynamodb.GetItemOutput {
	t.Helper()
	item, err := productItem(product)
	if err != nil {
		t.Fatalf("productItem returned error: %v", err)
	}
	return &dynamodb.GetItemOutput{Item: item}
}

func newStockDynamoStore(client *fakeStockClient, recorder observability.Recorder) *DynamoStore {
	return NewDynamoReadWriteStoreWithRecorder(client, dynamoTestConfig(), recorder)
}

func countStockAdjustMetrics(recorder *catalogMetricRecorder, outcome string) int {
	count := 0
	for _, metric := range recorder.metrics {
		if metric.Name != observability.MetricStockAdjust {
			continue
		}
		if catalogMetricDimensionsMatch(metric, map[string]string{"Service": "catalog", "Outcome": outcome}) {
			count++
		}
	}
	return count
}

func TestDynamoStoreAdjustStockWritesThroughVersionedTransactionAndRewritesMembershipRow(t *testing.T) {
	product := stockTestBaseProduct()
	client := &fakeStockClient{getOutputs: map[string][]*dynamodb.GetItemOutput{
		"PRODUCT#prod_base": {stockGetOutput(t, product)},
	}}
	recorder := &catalogMetricRecorder{}
	store := newStockDynamoStore(client, recorder)

	if err := store.AdjustStock(context.Background(), []StockAdjustment{{ProductID: "prod_base", Delta: -2}}); err != nil {
		t.Fatalf("AdjustStock returned error: %v", err)
	}

	if len(client.writeInputs) != 1 {
		t.Fatalf("transact write count = %d, want 1", len(client.writeInputs))
	}
	items := client.writeInputs[0].TransactItems
	if len(items) != 3 {
		t.Fatalf("transaction item count = %d, want product, slug lock, and membership row", len(items))
	}

	productPut := items[0].Put
	if productPut == nil {
		t.Fatal("first transaction item is not a Put")
	}
	if got := aws.ToString(productPut.ConditionExpression); got != "#version = :expected_version" {
		t.Fatalf("product condition = %q, want versioned update", got)
	}
	if got := numberAttribute(t, productPut.ExpressionAttributeValues, ":expected_version"); got != "3" {
		t.Fatalf("expected version = %q, want 3", got)
	}
	written, err := productFromItem(productPut.Item)
	if err != nil {
		t.Fatalf("product item did not round trip: %v", err)
	}
	if written.StockQuantity != 3 || written.Version != 4 {
		t.Fatalf("written product stock/version = %d/%d, want 3/4", written.StockQuantity, written.Version)
	}

	slugPut := items[1].Put
	if slugPut == nil {
		t.Fatal("second transaction item is not the slug lock Put")
	}
	if got := stringAttribute(t, slugPut.Item, "pk"); got != "PRODUCT_SLUG#carved-coconut-bowl" {
		t.Fatalf("slug lock pk = %q", got)
	}

	membershipPut := items[2].Put
	if membershipPut == nil {
		t.Fatal("third transaction item is not the membership Put")
	}
	if got := stringAttribute(t, membershipPut.Item, "pk"); got != "CATEGORY#home-decor" {
		t.Fatalf("membership pk = %q", got)
	}
	membership, err := productFromItem(membershipPut.Item)
	if err != nil {
		t.Fatalf("membership item did not round trip: %v", err)
	}
	if membership.StockQuantity != 3 || membership.Version != 4 {
		t.Fatalf("membership stock/version = %d/%d, want denormalized 3/4", membership.StockQuantity, membership.Version)
	}

	if got := countStockAdjustMetrics(recorder, stockOutcomeReserve); got != 1 {
		t.Fatalf("reserve metric count = %d, want 1", got)
	}
	assertCatalogMetric(t, recorder, observability.MetricCatalogOperationMs, observability.UnitMilliseconds, map[string]string{
		"Service":   "catalog",
		"Operation": "TransactWriteItems",
		"Method":    "AdjustStock",
	})
}

func TestDynamoStoreAdjustStockVariantLevelRewritesVariantsInProductAndMembershipRows(t *testing.T) {
	product := stockTestVariantProduct()
	client := &fakeStockClient{getOutputs: map[string][]*dynamodb.GetItemOutput{
		"PRODUCT#prod_variant": {stockGetOutput(t, product)},
	}}
	recorder := &catalogMetricRecorder{}
	store := newStockDynamoStore(client, recorder)

	err := store.AdjustStock(context.Background(), []StockAdjustment{
		{ProductID: "prod_variant", VariantID: "var_s", Delta: -1},
		{ProductID: "prod_variant", VariantID: "var_m", Delta: -2},
	})
	if err != nil {
		t.Fatalf("AdjustStock returned error: %v", err)
	}

	if len(client.writeInputs) != 1 {
		t.Fatalf("transact write count = %d, want grouped single write", len(client.writeInputs))
	}
	items := client.writeInputs[0].TransactItems
	written, err := productFromItem(items[0].Put.Item)
	if err != nil {
		t.Fatalf("product item did not round trip: %v", err)
	}
	if got := variantStock(t, written, "var_s"); got != 0 {
		t.Fatalf("written var_s stock = %d, want 0", got)
	}
	if got := variantStock(t, written, "var_m"); got != 2 {
		t.Fatalf("written var_m stock = %d, want 2", got)
	}
	if written.Version != 3 {
		t.Fatalf("written version = %d, want 3", written.Version)
	}

	membership, err := productFromItem(items[len(items)-1].Put.Item)
	if err != nil {
		t.Fatalf("membership item did not round trip: %v", err)
	}
	if got := variantStock(t, membership, "var_s"); got != 0 {
		t.Fatalf("membership var_s stock = %d, want denormalized 0", got)
	}
	if got := variantStock(t, membership, "var_m"); got != 2 {
		t.Fatalf("membership var_m stock = %d, want denormalized 2", got)
	}
}

func TestDynamoStoreAdjustStockReleaseRecordsReleaseOutcome(t *testing.T) {
	product := stockTestBaseProduct()
	client := &fakeStockClient{getOutputs: map[string][]*dynamodb.GetItemOutput{
		"PRODUCT#prod_base": {stockGetOutput(t, product)},
	}}
	recorder := &catalogMetricRecorder{}
	store := newStockDynamoStore(client, recorder)

	if err := store.AdjustStock(context.Background(), []StockAdjustment{{ProductID: "prod_base", Delta: 2}}); err != nil {
		t.Fatalf("AdjustStock returned error: %v", err)
	}

	written, err := productFromItem(client.writeInputs[0].TransactItems[0].Put.Item)
	if err != nil {
		t.Fatalf("product item did not round trip: %v", err)
	}
	if written.StockQuantity != 7 {
		t.Fatalf("written stock = %d, want released 7", written.StockQuantity)
	}
	if got := countStockAdjustMetrics(recorder, stockOutcomeRelease); got != 1 {
		t.Fatalf("release metric count = %d, want 1", got)
	}
	if got := countStockAdjustMetrics(recorder, stockOutcomeReserve); got != 0 {
		t.Fatalf("reserve metric count = %d, want 0", got)
	}
}

func TestDynamoStoreAdjustStockRetriesOnVersionConflictWithReRead(t *testing.T) {
	first := stockTestBaseProduct()
	second := stockTestBaseProduct()
	second.StockQuantity = 4
	second.Version = 4
	client := &fakeStockClient{
		getOutputs: map[string][]*dynamodb.GetItemOutput{
			"PRODUCT#prod_base": {stockGetOutput(t, first), stockGetOutput(t, second)},
		},
		writeErrs: []error{transactionCanceledAt(0), nil},
	}
	recorder := &catalogMetricRecorder{}
	store := newStockDynamoStore(client, recorder)

	if err := store.AdjustStock(context.Background(), []StockAdjustment{{ProductID: "prod_base", Delta: -2}}); err != nil {
		t.Fatalf("AdjustStock returned error: %v", err)
	}

	if len(client.getInputs) != 2 {
		t.Fatalf("GetItem count = %d, want re-read after conflict", len(client.getInputs))
	}
	if len(client.writeInputs) != 2 {
		t.Fatalf("transact write count = %d, want retry after conflict", len(client.writeInputs))
	}
	retryPut := client.writeInputs[1].TransactItems[0].Put
	if got := numberAttribute(t, retryPut.ExpressionAttributeValues, ":expected_version"); got != "4" {
		t.Fatalf("retry expected version = %q, want re-read version 4", got)
	}
	written, err := productFromItem(retryPut.Item)
	if err != nil {
		t.Fatalf("retry product item did not round trip: %v", err)
	}
	if written.StockQuantity != 2 || written.Version != 5 {
		t.Fatalf("retry stock/version = %d/%d, want delta applied to re-read product 2/5", written.StockQuantity, written.Version)
	}
	if got := countStockAdjustMetrics(recorder, stockOutcomeConflict); got != 1 {
		t.Fatalf("conflict metric count = %d, want 1", got)
	}
	if got := countStockAdjustMetrics(recorder, stockOutcomeReserve); got != 1 {
		t.Fatalf("reserve metric count = %d, want 1", got)
	}
}

func transactionCanceledWithReasons(codes ...string) *types.TransactionCanceledException {
	reasons := make([]types.CancellationReason, 0, len(codes))
	for _, code := range codes {
		reasons = append(reasons, types.CancellationReason{Code: aws.String(code)})
	}
	return &types.TransactionCanceledException{CancellationReasons: reasons}
}

func TestDynamoStoreAdjustStockRetriesTransientTransactionCancellations(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"transaction conflict cancellation", transactionCanceledWithReasons("None", "TransactionConflict", "None")},
		{"throttling cancellation", transactionCanceledWithReasons("ThrottlingError")},
		{"provisioned throughput cancellation", transactionCanceledWithReasons("ProvisionedThroughputExceeded")},
		{"transaction conflict exception", &types.TransactionConflictException{Message: aws.String("conflict")}},
		{"transaction in progress exception", &types.TransactionInProgressException{Message: aws.String("in progress")}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeStockClient{
				getOutputs: map[string][]*dynamodb.GetItemOutput{
					"PRODUCT#prod_base": {stockGetOutput(t, stockTestBaseProduct())},
				},
				writeErrs: []error{test.err, nil},
			}
			recorder := &catalogMetricRecorder{}
			store := newStockDynamoStore(client, recorder)

			if err := store.AdjustStock(context.Background(), []StockAdjustment{{ProductID: "prod_base", Delta: -2}}); err != nil {
				t.Fatalf("AdjustStock returned error: %v", err)
			}

			if len(client.writeInputs) != 2 {
				t.Fatalf("transact write count = %d, want retry after transient cancellation", len(client.writeInputs))
			}
			if len(client.getInputs) != 2 {
				t.Fatalf("GetItem count = %d, want re-read before the retry", len(client.getInputs))
			}
			if got := countStockAdjustMetrics(recorder, stockOutcomeConflict); got != 1 {
				t.Fatalf("conflict metric count = %d, want 1", got)
			}
			if got := countStockAdjustMetrics(recorder, stockOutcomeReserve); got != 1 {
				t.Fatalf("reserve metric count = %d, want 1", got)
			}
		})
	}
}

func TestDynamoStoreAdjustStockDoesNotRetryGenuineTransactionFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"validation cancellation", transactionCanceledWithReasons("None", "ValidationError")},
		{"validation alongside transaction conflict", transactionCanceledWithReasons("TransactionConflict", "ValidationError")},
		{"access denied", errors.New("api error AccessDeniedException: not authorized")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeStockClient{
				getOutputs: map[string][]*dynamodb.GetItemOutput{
					"PRODUCT#prod_base": {stockGetOutput(t, stockTestBaseProduct())},
				},
				writeErrs: []error{test.err},
			}
			recorder := &catalogMetricRecorder{}
			store := newStockDynamoStore(client, recorder)

			err := store.AdjustStock(context.Background(), []StockAdjustment{{ProductID: "prod_base", Delta: -2}})
			if err == nil {
				t.Fatal("AdjustStock err = nil, want genuine failure surfaced")
			}
			if errors.Is(err, ErrVersionConflict) {
				t.Fatalf("AdjustStock err = %v, want raw failure rather than version conflict", err)
			}
			if len(client.writeInputs) != 1 {
				t.Fatalf("transact write count = %d, want no retry for genuine failure", len(client.writeInputs))
			}
			if got := countStockAdjustMetrics(recorder, stockOutcomeConflict); got != 0 {
				t.Fatalf("conflict metric count = %d, want 0", got)
			}
		})
	}
}

func TestDynamoStoreAdjustStockExhaustsRetriesAndReturnsVersionConflict(t *testing.T) {
	conflicts := make([]error, stockAdjustMaxAttempts)
	for index := range conflicts {
		conflicts[index] = transactionCanceledAt(0)
	}
	client := &fakeStockClient{
		getOutputs: map[string][]*dynamodb.GetItemOutput{
			"PRODUCT#prod_base": {stockGetOutput(t, stockTestBaseProduct())},
		},
		writeErrs: conflicts,
	}
	recorder := &catalogMetricRecorder{}
	store := newStockDynamoStore(client, recorder)

	err := store.AdjustStock(context.Background(), []StockAdjustment{{ProductID: "prod_base", Delta: -1}})
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("AdjustStock err = %v, want wrapped ErrVersionConflict", err)
	}
	if len(client.writeInputs) != stockAdjustMaxAttempts {
		t.Fatalf("transact write count = %d, want %d attempts", len(client.writeInputs), stockAdjustMaxAttempts)
	}
	if got := countStockAdjustMetrics(recorder, stockOutcomeConflict); got != stockAdjustMaxAttempts {
		t.Fatalf("conflict metric count = %d, want %d", got, stockAdjustMaxAttempts)
	}
	if got := countStockAdjustMetrics(recorder, stockOutcomeReserve); got != 0 {
		t.Fatalf("reserve metric count = %d, want 0", got)
	}
}

func TestDynamoStoreAdjustStockReleaseSkipsMissingVariantAndRecordsSkipMetric(t *testing.T) {
	client := &fakeStockClient{getOutputs: map[string][]*dynamodb.GetItemOutput{
		"PRODUCT#prod_variant": {stockGetOutput(t, stockTestVariantProduct())},
	}}
	recorder := &catalogMetricRecorder{}
	store := newStockDynamoStore(client, recorder)

	err := store.AdjustStock(context.Background(), []StockAdjustment{
		{ProductID: "prod_variant", VariantID: "var_gone", Delta: 1},
		{ProductID: "prod_variant", VariantID: "var_m", Delta: 2},
	})
	if err != nil {
		t.Fatalf("AdjustStock returned error: %v", err)
	}

	if len(client.writeInputs) != 1 {
		t.Fatalf("transact write count = %d, want 1", len(client.writeInputs))
	}
	written, err := productFromItem(client.writeInputs[0].TransactItems[0].Put.Item)
	if err != nil {
		t.Fatalf("product item did not round trip: %v", err)
	}
	if got := variantStock(t, written, "var_m"); got != 6 {
		t.Fatalf("written var_m stock = %d, want released 6", got)
	}
	if got := countStockAdjustMetrics(recorder, stockOutcomeReleaseSkipped); got != 1 {
		t.Fatalf("release_skipped metric count = %d, want 1", got)
	}
	if got := countStockAdjustMetrics(recorder, stockOutcomeRelease); got != 1 {
		t.Fatalf("release metric count = %d, want 1", got)
	}
}

func TestDynamoStoreAdjustStockReleaseWithAllVariantsGoneSkipsWrite(t *testing.T) {
	client := &fakeStockClient{getOutputs: map[string][]*dynamodb.GetItemOutput{
		"PRODUCT#prod_variant": {stockGetOutput(t, stockTestVariantProduct())},
	}}
	recorder := &catalogMetricRecorder{}
	store := newStockDynamoStore(client, recorder)

	err := store.AdjustStock(context.Background(), []StockAdjustment{
		{ProductID: "prod_variant", VariantID: "var_gone", Delta: 1},
	})
	if err != nil {
		t.Fatalf("AdjustStock returned error: %v", err)
	}

	if len(client.writeInputs) != 0 {
		t.Fatalf("transact write count = %d, want no write when every release was skipped", len(client.writeInputs))
	}
	if got := countStockAdjustMetrics(recorder, stockOutcomeReleaseSkipped); got != 1 {
		t.Fatalf("release_skipped metric count = %d, want 1", got)
	}
	if got := countStockAdjustMetrics(recorder, stockOutcomeRelease); got != 0 {
		t.Fatalf("release metric count = %d, want 0", got)
	}
}

func TestDynamoStoreAdjustStockReleaseSkipsMissingProductAndContinues(t *testing.T) {
	client := &fakeStockClient{getOutputs: map[string][]*dynamodb.GetItemOutput{
		"PRODUCT#prod_base": {stockGetOutput(t, stockTestBaseProduct())},
	}}
	recorder := &catalogMetricRecorder{}
	store := newStockDynamoStore(client, recorder)

	err := store.AdjustStock(context.Background(), []StockAdjustment{
		{ProductID: "prod_deleted", Delta: 2},
		{ProductID: "prod_base", Delta: 2},
	})
	if err != nil {
		t.Fatalf("AdjustStock returned error: %v", err)
	}

	if len(client.writeInputs) != 1 {
		t.Fatalf("transact write count = %d, want only the surviving product written", len(client.writeInputs))
	}
	written, err := productFromItem(client.writeInputs[0].TransactItems[0].Put.Item)
	if err != nil {
		t.Fatalf("product item did not round trip: %v", err)
	}
	if written.ID != "prod_base" || written.StockQuantity != 7 {
		t.Fatalf("written product = %q stock %d, want prod_base released to 7", written.ID, written.StockQuantity)
	}
	if got := countStockAdjustMetrics(recorder, stockOutcomeReleaseSkipped); got != 1 {
		t.Fatalf("release_skipped metric count = %d, want 1", got)
	}
	if got := countStockAdjustMetrics(recorder, stockOutcomeRelease); got != 1 {
		t.Fatalf("release metric count = %d, want 1", got)
	}
}

func TestDynamoStoreAdjustStockMultiProductFailureCompensatesEarlierProducts(t *testing.T) {
	base := stockTestBaseProduct()
	reserved := stockTestBaseProduct()
	reserved.StockQuantity = 3
	reserved.Version = 4
	variant := stockTestVariantProduct()
	client := &fakeStockClient{
		getOutputs: map[string][]*dynamodb.GetItemOutput{
			"PRODUCT#prod_base":    {stockGetOutput(t, base), stockGetOutput(t, reserved)},
			"PRODUCT#prod_variant": {stockGetOutput(t, variant)},
		},
	}
	recorder := &catalogMetricRecorder{}
	store := newStockDynamoStore(client, recorder)

	err := store.AdjustStock(context.Background(), []StockAdjustment{
		{ProductID: "prod_base", Delta: -2},
		{ProductID: "prod_variant", VariantID: "var_s", Delta: -3},
	})
	var insufficient InsufficientStockError
	if !errors.As(err, &insufficient) {
		t.Fatalf("AdjustStock err = %v, want InsufficientStockError", err)
	}
	if insufficient.ProductID != "prod_variant" || insufficient.Available != 1 {
		t.Fatalf("InsufficientStockError = %#v, want prod_variant with 1 available", insufficient)
	}

	if len(client.writeInputs) != 2 {
		t.Fatalf("transact write count = %d, want reserve plus compensating release", len(client.writeInputs))
	}
	compensation, err := productFromItem(client.writeInputs[1].TransactItems[0].Put.Item)
	if err != nil {
		t.Fatalf("compensation product item did not round trip: %v", err)
	}
	if compensation.ID != "prod_base" || compensation.StockQuantity != 5 {
		t.Fatalf("compensation product = %q stock %d, want prod_base restored to 5", compensation.ID, compensation.StockQuantity)
	}
	if got := countStockAdjustMetrics(recorder, stockOutcomeReserve); got != 1 {
		t.Fatalf("reserve metric count = %d, want 1", got)
	}
	if got := countStockAdjustMetrics(recorder, stockOutcomeRelease); got != 1 {
		t.Fatalf("release metric count = %d, want compensating release recorded", got)
	}
	if got := countStockAdjustMetrics(recorder, stockOutcomeRollbackError); got != 0 {
		t.Fatalf("rollback_error metric count = %d, want 0", got)
	}
}

func TestDynamoStoreAdjustStockCompensationFailureRecordsRollbackError(t *testing.T) {
	base := stockTestBaseProduct()
	reserved := stockTestBaseProduct()
	reserved.StockQuantity = 3
	reserved.Version = 4
	variant := stockTestVariantProduct()
	writeErrs := []error{nil}
	for attempt := 0; attempt < stockAdjustMaxAttempts; attempt++ {
		writeErrs = append(writeErrs, transactionCanceledAt(0))
	}
	client := &fakeStockClient{
		getOutputs: map[string][]*dynamodb.GetItemOutput{
			"PRODUCT#prod_base":    {stockGetOutput(t, base), stockGetOutput(t, reserved)},
			"PRODUCT#prod_variant": {stockGetOutput(t, variant)},
		},
		writeErrs: writeErrs,
	}
	recorder := &catalogMetricRecorder{}
	store := newStockDynamoStore(client, recorder)

	err := store.AdjustStock(context.Background(), []StockAdjustment{
		{ProductID: "prod_base", Delta: -2},
		{ProductID: "prod_variant", VariantID: "var_s", Delta: -3},
	})
	var insufficient InsufficientStockError
	if !errors.As(err, &insufficient) {
		t.Fatalf("AdjustStock err = %v, want original InsufficientStockError even when compensation fails", err)
	}
	if got := len(client.writeInputs); got != 1+stockAdjustMaxAttempts {
		t.Fatalf("transact write count = %d, want reserve plus %d compensation attempts", got, stockAdjustMaxAttempts)
	}
	if got := countStockAdjustMetrics(recorder, stockOutcomeRollbackError); got != 1 {
		t.Fatalf("rollback_error metric count = %d, want 1", got)
	}
}

func TestNewStoreFromEnvWithRecorderReturnsStockStoreCapableStore(t *testing.T) {
	t.Setenv(appenv.EnvAppEnvironment, "development")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv(EnvTableName, "catalog-table")
	t.Setenv(EnvSlugIndexName, "")
	t.Setenv(EnvPublicIndexName, "")
	t.Setenv(EnvRecentIndexName, "")
	t.Setenv(EnvEntityIndexName, "")

	store, usedDynamo, err := NewStoreFromEnvWithRecorder(context.Background(), nil)
	if err != nil {
		t.Fatalf("NewStoreFromEnvWithRecorder returned error: %v", err)
	}
	if !usedDynamo {
		t.Fatal("usedDynamo = false, want true")
	}
	if _, ok := store.(StockStore); !ok {
		t.Fatalf("store %T does not implement StockStore", store)
	}
	dynamoStore, ok := store.(*DynamoStore)
	if !ok {
		t.Fatalf("store = %T, want *DynamoStore", store)
	}
	if dynamoStore.getClient == nil || dynamoStore.writeClient == nil {
		t.Fatal("env-constructed store is missing the get or transact-write client needed by AdjustStock")
	}
}
