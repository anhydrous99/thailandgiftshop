package catalog

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/appenv"
	"github.com/anhydrous99/thailandgiftshop/internal/observability"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestProductItemRoundTripIncludesInventoryAndNoCurrency(t *testing.T) {
	createdAt := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2026, 6, 6, 12, 30, 0, 0, time.UTC)
	product := Product{
		ID:            "prod_001",
		Slug:          "mango-sticky-rice-kit",
		Name:          "Mango Sticky Rice Treats",
		Description:   "Shelf-stable Thai dessert snacks.",
		PriceCents:    2499,
		ImageURL:      "/images/products/mango.jpg",
		Status:        StatusActive,
		SortOrder:     12,
		StockQuantity: 0,
		Version:       7,
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
		CategorySlugs: []string{"food", "gifts"},
		Variants: []ProductVariant{
			{ID: "var_001_small", Label: "Small", StockQuantity: 2, Status: StatusActive, SortOrder: 10},
			{ID: "var_001_large", Label: "Large", StockQuantity: 0, Status: StatusArchived, SortOrder: 20},
		},
	}

	item, err := productItem(product)
	if err != nil {
		t.Fatalf("productItem returned error: %v", err)
	}

	if _, ok := item["currency"]; ok {
		t.Fatal("product item contains currency; prices should be implicitly USD")
	}
	if got := stringAttribute(t, item, "pk"); got != "PRODUCT#prod_001" {
		t.Fatalf("pk = %q", got)
	}
	if got := stringAttribute(t, item, "gsi1pk"); got != "PRODUCT_SLUG#mango-sticky-rice-kit" {
		t.Fatalf("gsi1pk = %q", got)
	}
	if got := stringAttribute(t, item, "gsi2pk"); got != activeProductsIndexPK {
		t.Fatalf("gsi2pk = %q", got)
	}
	if got := stringAttribute(t, item, "gsi3pk"); got != recentProductsIndexPK {
		t.Fatalf("gsi3pk = %q", got)
	}
	if got := stringAttribute(t, item, "gsi3sk"); got != "PRODUCT#2026-06-01T10:00:00Z#prod_001" {
		t.Fatalf("gsi3sk = %q", got)
	}
	if got := stringAttribute(t, item, "gsi4pk"); got != adminProductsIndexPK {
		t.Fatalf("gsi4pk = %q", got)
	}
	if got := stringAttribute(t, item, "gsi4sk"); got != "PRODUCT#prod_001" {
		t.Fatalf("gsi4sk = %q", got)
	}
	if got := stringAttribute(t, item, "created_at"); got != "2026-06-01T10:00:00Z" {
		t.Fatalf("created_at = %q", got)
	}

	got, err := productFromItem(item)
	if err != nil {
		t.Fatalf("productFromItem returned error: %v", err)
	}
	if !reflect.DeepEqual(got, product) {
		t.Fatalf("product round trip = %#v, want %#v", got, product)
	}
	if got.OutOfStock() {
		t.Fatal("OutOfStock = true, want false when active variant stock is available")
	}
	if stock := got.TotalAvailableStock(); stock != 2 {
		t.Fatalf("TotalAvailableStock = %d, want active variant stock 2", stock)
	}
}

func TestDynamoProductFromItemPreservesLegacyProductWithoutVariantsOrVersion(t *testing.T) {
	item := map[string]types.AttributeValue{
		"pk":             &types.AttributeValueMemberS{Value: "PRODUCT#prod_legacy"},
		"sk":             &types.AttributeValueMemberS{Value: productSK},
		"entity_type":    &types.AttributeValueMemberS{Value: entityProduct},
		"id":             &types.AttributeValueMemberS{Value: "prod_legacy"},
		"slug":           &types.AttributeValueMemberS{Value: "legacy-product"},
		"name":           &types.AttributeValueMemberS{Value: "Legacy Product"},
		"status":         &types.AttributeValueMemberS{Value: string(StatusActive)},
		"stock_quantity": &types.AttributeValueMemberN{Value: "6"},
	}

	product, err := productFromItem(item)
	if err != nil {
		t.Fatalf("productFromItem returned error: %v", err)
	}
	if product.Version != 0 {
		t.Fatalf("legacy product Version = %d, want 0", product.Version)
	}
	if product.UsesVariants() {
		t.Fatal("legacy product UsesVariants = true, want false when variants attribute is missing")
	}
	if got := product.TotalAvailableStock(); got != 6 {
		t.Fatalf("legacy product TotalAvailableStock = %d, want stock_quantity 6", got)
	}
}

func TestCategoryAndMembershipItemsRoundTrip(t *testing.T) {
	category := Category{
		Slug:        "home-decor",
		Name:        "Home Decor",
		Description: "Decorative gifts from Thailand.",
		Status:      StatusActive,
		SortOrder:   3,
		UpdatedAt:   time.Date(2026, 6, 6, 13, 0, 0, 0, time.UTC),
	}

	categoryMap, err := categoryItem(category)
	if err != nil {
		t.Fatalf("categoryItem returned error: %v", err)
	}
	if got := stringAttribute(t, categoryMap, "pk"); got != "CATEGORY#home-decor" {
		t.Fatalf("category pk = %q", got)
	}
	if got := stringAttribute(t, categoryMap, "gsi2pk"); got != activeCategoriesIndexPK {
		t.Fatalf("category gsi2pk = %q", got)
	}
	if got := stringAttribute(t, categoryMap, "gsi4pk"); got != adminCategoriesIndexPK {
		t.Fatalf("category gsi4pk = %q", got)
	}
	if got := stringAttribute(t, categoryMap, "gsi4sk"); got != "CATEGORY#home-decor" {
		t.Fatalf("category gsi4sk = %q", got)
	}

	gotCategory, err := categoryFromItem(categoryMap)
	if err != nil {
		t.Fatalf("categoryFromItem returned error: %v", err)
	}
	if gotCategory != category {
		t.Fatalf("category round trip = %#v, want %#v", gotCategory, category)
	}

	product := Product{
		ID:            "prod_002",
		Slug:          "teak-elephant",
		Name:          "Teak Elephant",
		PriceCents:    3999,
		ImageURL:      "/images/products/teak.jpg",
		Status:        StatusActive,
		SortOrder:     5,
		StockQuantity: 7,
	}
	membershipMap, err := categoryProductItem(category.Slug, product)
	if err != nil {
		t.Fatalf("categoryProductItem returned error: %v", err)
	}
	if got := stringAttribute(t, membershipMap, "pk"); got != "CATEGORY#home-decor" {
		t.Fatalf("membership pk = %q", got)
	}
	if got := stringAttribute(t, membershipMap, "sk"); got != "PRODUCT#0000000005#prod_002" {
		t.Fatalf("membership sk = %q", got)
	}
	if _, ok := membershipMap["gsi4pk"]; ok {
		t.Fatal("membership unexpectedly includes gsi4pk")
	}
	if _, ok := membershipMap["gsi4sk"]; ok {
		t.Fatal("membership unexpectedly includes gsi4sk")
	}

	gotProduct, err := productFromItem(membershipMap)
	if err != nil {
		t.Fatalf("productFromItem returned error for membership: %v", err)
	}
	if gotProduct.ID != product.ID || gotProduct.StockQuantity != product.StockQuantity {
		t.Fatalf("membership product = %#v, want product id %q and stock %d", gotProduct, product.ID, product.StockQuantity)
	}
}

func TestDemoCatalogSeedItemsIncludesExpectedRows(t *testing.T) {
	items, err := DemoCatalogSeedItems()
	if err != nil {
		t.Fatalf("DemoCatalogSeedItems returned error: %v", err)
	}
	counts := DemoCatalogSeedCounts()
	if len(items) != counts.Items {
		t.Fatalf("seed item count = %d, want %d", len(items), counts.Items)
	}

	var categories int
	var products int
	var productSlugLocks int
	var categoryProducts int
	var marked int
	for _, item := range items {
		if stringAttribute(t, item, "seed_group") == DemoSeedGroup {
			marked++
		}

		switch got := stringAttribute(t, item, "entity_type"); got {
		case entityCategory:
			categories++
			if got := stringAttribute(t, item, "gsi4pk"); got != adminCategoriesIndexPK {
				t.Fatalf("seed category gsi4pk = %q", got)
			}
		case entityProduct:
			products++
			if got := stringAttribute(t, item, "gsi4pk"); got != adminProductsIndexPK {
				t.Fatalf("seed product gsi4pk = %q", got)
			}
			if got := stringAttribute(t, item, "image_url"); !strings.HasPrefix(got, "/images/products/") {
				t.Fatalf("product image_url = %q, want /images/products/ prefix", got)
			}
			if got := stringAttribute(t, item, "created_at"); got == "" {
				t.Fatal("product created_at is empty")
			}
		case entityCategoryProduct:
			categoryProducts++
			if _, ok := item["gsi4pk"]; ok {
				t.Fatal("seed category-product row unexpectedly includes gsi4pk")
			}
		case entityProductSlugLock:
			productSlugLocks++
			if _, ok := item["gsi1pk"]; ok {
				t.Fatal("seed slug lock unexpectedly includes gsi1pk")
			}
			if _, ok := item["gsi4pk"]; ok {
				t.Fatal("seed slug lock unexpectedly includes gsi4pk")
			}
			if got := stringAttribute(t, item, "pk"); !strings.HasPrefix(got, "PRODUCT_SLUG#") {
				t.Fatalf("seed slug lock pk = %q", got)
			}
			if got := stringAttribute(t, item, "product_id"); got == "" {
				t.Fatal("seed slug lock product_id is empty")
			}
		default:
			t.Fatalf("unexpected entity_type %q", got)
		}
	}

	if categories != counts.Categories {
		t.Fatalf("category rows = %d, want %d", categories, counts.Categories)
	}
	if products != counts.Products {
		t.Fatalf("product rows = %d, want %d", products, counts.Products)
	}
	if productSlugLocks != counts.ProductSlugLockRows {
		t.Fatalf("product slug-lock rows = %d, want %d", productSlugLocks, counts.ProductSlugLockRows)
	}
	if categoryProducts != counts.CategoryProductRows {
		t.Fatalf("category-product rows = %d, want %d", categoryProducts, counts.CategoryProductRows)
	}
	if marked != counts.Items {
		t.Fatalf("seed_group-marked rows = %d, want %d", marked, counts.Items)
	}
}

func TestProductDisplayImageURLFallsBackWhenEmpty(t *testing.T) {
	if got := (Product{ImageURL: "/images/products/mango.jpg"}).DisplayImageURL("/images/placeholder-product.jpg"); got != "/images/products/mango.jpg" {
		t.Fatalf("DisplayImageURL = %q", got)
	}
	if got := (Product{}).DisplayImageURL("/images/custom-placeholder.jpg"); got != "/images/custom-placeholder.jpg" {
		t.Fatalf("DisplayImageURL fallback = %q", got)
	}
	if got := (Product{}).DisplayImageURL(""); got != DefaultProductImagePlaceholderURL {
		t.Fatalf("DisplayImageURL default fallback = %q", got)
	}
}

func TestDynamoStoreRecordsCatalogOperationMetrics(t *testing.T) {
	config := dynamoTestConfig()
	recorder := &catalogMetricRecorder{}
	queryStore := NewDynamoStoreWithRecorder(&fakeQueryClient{}, config, recorder)
	if _, err := queryStore.ListActiveProducts(context.Background(), 0); err != nil {
		t.Fatalf("ListActiveProducts returned error: %v", err)
	}

	getStore := NewDynamoStoreWithRecorder(nil, config, recorder)
	getStore.getClient = &fakeGetItemClient{}
	if _, _, err := getStore.GetProductByID(context.Background(), "prod_001"); err != nil {
		t.Fatalf("GetProductByID returned error: %v", err)
	}

	adminListStore := NewDynamoStoreWithRecorder(&fakeQueryClient{}, config, recorder)
	if _, err := adminListStore.ListProducts(context.Background()); err != nil {
		t.Fatalf("ListProducts returned error: %v", err)
	}

	writeStore := NewDynamoAdminStoreWithRecorder(&fakeTransactWriteClient{}, config, recorder)
	if _, err := writeStore.CreateCategory(context.Background(), Category{Slug: "metric-category", Name: "Metric Category"}); err != nil {
		t.Fatalf("CreateCategory returned error: %v", err)
	}

	assertCatalogMetric(t, recorder, observability.MetricCatalogOperationMs, observability.UnitMilliseconds, map[string]string{
		"Service":   "catalog",
		"Operation": "Query",
		"Method":    "ListActiveProducts",
	})
	assertCatalogMetric(t, recorder, observability.MetricCatalogOperation, observability.UnitCount, map[string]string{
		"Service":   "catalog",
		"Operation": "Query",
		"Method":    "ListActiveProducts",
	})
	assertCatalogMetric(t, recorder, observability.MetricCatalogOperationMs, observability.UnitMilliseconds, map[string]string{
		"Service":   "catalog",
		"Operation": "GetItem",
		"Method":    "GetProductByID",
	})
	assertCatalogMetric(t, recorder, observability.MetricCatalogOperationMs, observability.UnitMilliseconds, map[string]string{
		"Service":   "catalog",
		"Operation": "Query",
		"Method":    "ListProducts",
	})
	assertCatalogMetric(t, recorder, observability.MetricCatalogOperationMs, observability.UnitMilliseconds, map[string]string{
		"Service":   "catalog",
		"Operation": "TransactWriteItems",
		"Method":    "CreateCategory",
	})
}

func TestDynamoConfigFromEnvDefaultsEntityIndexName(t *testing.T) {
	t.Setenv(EnvTableName, "catalog-table")
	t.Setenv(EnvSlugIndexName, "")
	t.Setenv(EnvPublicIndexName, "")
	t.Setenv(EnvRecentIndexName, "")
	t.Setenv(EnvEntityIndexName, "")

	config, ok := DynamoConfigFromEnv()
	if !ok {
		t.Fatal("DynamoConfigFromEnv ok = false, want true")
	}
	if config.EntityIndexName != DefaultEntityIndexName {
		t.Fatalf("EntityIndexName = %q, want %q", config.EntityIndexName, DefaultEntityIndexName)
	}
}

func TestProductionCatalogStoreEnvironmentMissingCatalogTableFailsClosed(t *testing.T) {
	setMissingCatalogTableEnvironment(t, appenv.EnvironmentProduction)

	store, usedDynamo, err := NewStoreFromEnvWithRecorder(context.Background(), nil)
	if !errors.Is(err, ErrCatalogStoreNotConfigured) {
		t.Fatalf("NewStoreFromEnvWithRecorder err = %v, want ErrCatalogStoreNotConfigured", err)
	}
	if store != nil {
		t.Fatalf("store = %#v, want nil", store)
	}
	if usedDynamo {
		t.Fatal("usedDynamo = true, want false")
	}
}

func TestProductionCatalogAdminStoreEnvironmentMissingCatalogTableFailsClosed(t *testing.T) {
	setMissingCatalogTableEnvironment(t, appenv.EnvironmentProduction)

	store, usedDynamo, err := NewAdminStoreFromEnvWithRecorder(context.Background(), nil)
	if !errors.Is(err, ErrCatalogStoreNotConfigured) {
		t.Fatalf("NewAdminStoreFromEnvWithRecorder err = %v, want ErrCatalogStoreNotConfigured", err)
	}
	if store != nil {
		t.Fatalf("store = %#v, want nil", store)
	}
	if usedDynamo {
		t.Fatal("usedDynamo = true, want false")
	}
}

func TestLocalCatalogStoreEnvironmentMissingCatalogTableUsesEmptyStore(t *testing.T) {
	setMissingCatalogTableEnvironment(t, "development")

	store, usedDynamo, err := NewStoreFromEnvWithRecorder(context.Background(), nil)
	if err != nil {
		t.Fatalf("NewStoreFromEnvWithRecorder returned error: %v", err)
	}
	if _, ok := store.(EmptyStore); !ok {
		t.Fatalf("store = %T, want EmptyStore", store)
	}
	if usedDynamo {
		t.Fatal("usedDynamo = true, want false")
	}
}

func TestLocalCatalogAdminStoreEnvironmentMissingCatalogTableUsesMemoryStore(t *testing.T) {
	setMissingCatalogTableEnvironment(t, "development")

	store, usedDynamo, err := NewAdminStoreFromEnvWithRecorder(context.Background(), nil)
	if err != nil {
		t.Fatalf("NewAdminStoreFromEnvWithRecorder returned error: %v", err)
	}
	if _, ok := store.(*MemoryStore); !ok {
		t.Fatalf("store = %T, want *MemoryStore", store)
	}
	if usedDynamo {
		t.Fatal("usedDynamo = true, want false")
	}
}

func TestDynamoAdminStoreListProductsUsesEntityIndexAndSortsResults(t *testing.T) {
	second := Product{ID: "prod_002", Slug: "second", Name: "Second", Status: StatusDraft, SortOrder: 20, StockQuantity: 1}
	first := Product{ID: "prod_001", Slug: "first", Name: "First", Status: StatusArchived, SortOrder: 10, StockQuantity: 1}
	secondItem, err := productItem(second)
	if err != nil {
		t.Fatalf("productItem second returned error: %v", err)
	}
	firstItem, err := productItem(first)
	if err != nil {
		t.Fatalf("productItem first returned error: %v", err)
	}
	client := &fakeQueryClient{outputs: []*dynamodb.QueryOutput{{Items: []map[string]types.AttributeValue{secondItem, firstItem}}}}
	store := NewDynamoStore(client, dynamoTestConfig())

	products, err := store.ListProducts(context.Background())
	if err != nil {
		t.Fatalf("ListProducts returned error: %v", err)
	}
	if got := []string{products[0].ID, products[1].ID}; !reflect.DeepEqual(got, []string{"prod_001", "prod_002"}) {
		t.Fatalf("product order = %v", got)
	}
	input := client.inputs[0]
	if got := aws.ToString(input.IndexName); got != "entity-index" {
		t.Fatalf("IndexName = %q", got)
	}
	if got := aws.ToString(input.KeyConditionExpression); got != "#gsi4pk = :pk" {
		t.Fatalf("KeyConditionExpression = %q", got)
	}
	if got := stringAttribute(t, input.ExpressionAttributeValues, ":pk"); got != adminProductsIndexPK {
		t.Fatalf(":pk = %q", got)
	}
}

func TestDynamoAdminStoreListCategoriesUsesEntityIndexAndSortsResults(t *testing.T) {
	second := Category{Slug: "second", Name: "Second", Status: StatusDraft, SortOrder: 20}
	first := Category{Slug: "first", Name: "First", Status: StatusArchived, SortOrder: 10}
	secondItem, err := categoryItem(second)
	if err != nil {
		t.Fatalf("categoryItem second returned error: %v", err)
	}
	firstItem, err := categoryItem(first)
	if err != nil {
		t.Fatalf("categoryItem first returned error: %v", err)
	}
	client := &fakeQueryClient{outputs: []*dynamodb.QueryOutput{{Items: []map[string]types.AttributeValue{secondItem, firstItem}}}}
	store := NewDynamoStore(client, dynamoTestConfig())

	categories, err := store.ListCategories(context.Background())
	if err != nil {
		t.Fatalf("ListCategories returned error: %v", err)
	}
	if got := []string{categories[0].Slug, categories[1].Slug}; !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("category order = %v", got)
	}
	input := client.inputs[0]
	if got := aws.ToString(input.IndexName); got != "entity-index" {
		t.Fatalf("IndexName = %q", got)
	}
	if got := stringAttribute(t, input.ExpressionAttributeValues, ":pk"); got != adminCategoriesIndexPK {
		t.Fatalf(":pk = %q", got)
	}
}

func TestDynamoStoreListActiveProductsUsesPublicIndex(t *testing.T) {
	product := Product{
		ID:            "prod_001",
		Slug:          "mango-sticky-rice-kit",
		Name:          "Mango Sticky Rice Treats",
		PriceCents:    2499,
		Status:        StatusActive,
		SortOrder:     12,
		StockQuantity: 4,
	}
	item, err := productItem(product)
	if err != nil {
		t.Fatalf("productItem returned error: %v", err)
	}
	client := &fakeQueryClient{
		outputs: []*dynamodb.QueryOutput{{Items: []map[string]types.AttributeValue{item}}},
	}
	store := NewDynamoStore(client, DynamoConfig{
		TableName:       "catalog-table",
		SlugIndexName:   "slug-index",
		PublicIndexName: "public-index",
		RecentIndexName: "recent-index",
	})

	products, err := store.ListActiveProducts(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListActiveProducts returned error: %v", err)
	}
	if len(products) != 1 || products[0].ID != product.ID {
		t.Fatalf("products = %#v", products)
	}

	input := client.inputs[0]
	if got := aws.ToString(input.TableName); got != "catalog-table" {
		t.Fatalf("TableName = %q", got)
	}
	if got := aws.ToString(input.IndexName); got != "public-index" {
		t.Fatalf("IndexName = %q", got)
	}
	if got := stringAttribute(t, input.ExpressionAttributeValues, ":pk"); got != activeProductsIndexPK {
		t.Fatalf(":pk = %q", got)
	}
	if got := aws.ToInt32(input.Limit); got != 10 {
		t.Fatalf("Limit = %d, want 10", got)
	}
}

func TestDynamoStoreListRecentlyAddedProductsUsesRecentIndex(t *testing.T) {
	product := Product{
		ID:            "prod_011",
		Slug:          "elephant-pouch-set",
		Name:          "Elephant Cotton Pouches",
		PriceCents:    2499,
		Status:        StatusActive,
		SortOrder:     110,
		StockQuantity: 4,
		CreatedAt:     time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC),
	}
	item, err := productItem(product)
	if err != nil {
		t.Fatalf("productItem returned error: %v", err)
	}
	client := &fakeQueryClient{
		outputs: []*dynamodb.QueryOutput{{Items: []map[string]types.AttributeValue{item}}},
	}
	store := NewDynamoStore(client, DynamoConfig{
		TableName:       "catalog-table",
		SlugIndexName:   "slug-index",
		PublicIndexName: "public-index",
		RecentIndexName: "recent-index",
	})

	products, err := store.ListRecentlyAddedProducts(context.Background(), 8)
	if err != nil {
		t.Fatalf("ListRecentlyAddedProducts returned error: %v", err)
	}
	if len(products) != 1 || products[0].ID != product.ID {
		t.Fatalf("products = %#v", products)
	}

	input := client.inputs[0]
	if got := aws.ToString(input.TableName); got != "catalog-table" {
		t.Fatalf("TableName = %q", got)
	}
	if got := aws.ToString(input.IndexName); got != "recent-index" {
		t.Fatalf("IndexName = %q", got)
	}
	if got := aws.ToBool(input.ScanIndexForward); got {
		t.Fatal("ScanIndexForward = true, want newest products first")
	}
	if got := stringAttribute(t, input.ExpressionAttributeValues, ":pk"); got != recentProductsIndexPK {
		t.Fatalf(":pk = %q", got)
	}
	if got := aws.ToInt32(input.Limit); got != 8 {
		t.Fatalf("Limit = %d, want 8", got)
	}
}

func TestDynamoStoreGetProductBySlugUsesSlugIndex(t *testing.T) {
	product := Product{
		ID:            "prod_001",
		Slug:          "mango-sticky-rice-kit",
		Name:          "Mango Sticky Rice Treats",
		PriceCents:    2499,
		Status:        StatusActive,
		SortOrder:     12,
		StockQuantity: 4,
	}
	item, err := productItem(product)
	if err != nil {
		t.Fatalf("productItem returned error: %v", err)
	}
	client := &fakeQueryClient{
		outputs: []*dynamodb.QueryOutput{{Items: []map[string]types.AttributeValue{item}}},
	}
	store := NewDynamoStore(client, DynamoConfig{
		TableName:       "catalog-table",
		SlugIndexName:   "slug-index",
		PublicIndexName: "public-index",
		RecentIndexName: "recent-index",
	})

	got, ok, err := store.GetProductBySlug(context.Background(), product.Slug)
	if err != nil {
		t.Fatalf("GetProductBySlug returned error: %v", err)
	}
	if !ok {
		t.Fatal("GetProductBySlug ok = false, want true")
	}
	if got.ID != product.ID {
		t.Fatalf("product ID = %q, want %q", got.ID, product.ID)
	}

	input := client.inputs[0]
	if got := aws.ToString(input.IndexName); got != "slug-index" {
		t.Fatalf("IndexName = %q", got)
	}
	if got := stringAttribute(t, input.ExpressionAttributeValues, ":pk"); got != "PRODUCT_SLUG#mango-sticky-rice-kit" {
		t.Fatalf(":pk = %q", got)
	}
	if got := aws.ToInt32(input.Limit); got != 1 {
		t.Fatalf("Limit = %d, want 1", got)
	}
}

func TestDynamoStoreListActiveProductsByCategoryUsesBaseTable(t *testing.T) {
	product := Product{
		ID:            "prod_002",
		Slug:          "teak-elephant",
		Name:          "Teak Elephant",
		PriceCents:    3999,
		Status:        StatusActive,
		SortOrder:     5,
		StockQuantity: 2,
	}
	item, err := categoryProductItem("home-decor", product)
	if err != nil {
		t.Fatalf("categoryProductItem returned error: %v", err)
	}
	client := &fakeQueryClient{
		outputs: []*dynamodb.QueryOutput{{Items: []map[string]types.AttributeValue{item}}},
	}
	store := NewDynamoStore(client, DynamoConfig{
		TableName:       "catalog-table",
		SlugIndexName:   "slug-index",
		PublicIndexName: "public-index",
		RecentIndexName: "recent-index",
	})

	products, err := store.ListActiveProductsByCategory(context.Background(), "home-decor", 6)
	if err != nil {
		t.Fatalf("ListActiveProductsByCategory returned error: %v", err)
	}
	if len(products) != 1 || products[0].ID != product.ID {
		t.Fatalf("products = %#v", products)
	}

	input := client.inputs[0]
	if input.IndexName != nil {
		t.Fatalf("IndexName = %q, want nil for base table category query", aws.ToString(input.IndexName))
	}
	if got := aws.ToString(input.KeyConditionExpression); got != "#pk = :pk AND begins_with(#sk, :product_prefix)" {
		t.Fatalf("KeyConditionExpression = %q", got)
	}
	if got := aws.ToString(input.FilterExpression); got != "#status = :active" {
		t.Fatalf("FilterExpression = %q", got)
	}
	if got := stringAttribute(t, input.ExpressionAttributeValues, ":pk"); got != "CATEGORY#home-decor" {
		t.Fatalf(":pk = %q", got)
	}
	if got := stringAttribute(t, input.ExpressionAttributeValues, ":active"); got != string(StatusActive) {
		t.Fatalf(":active = %q", got)
	}
}

func TestDynamoAdminStoreCreateProductTransactionWritesProductSlugLockMembershipsAndVariants(t *testing.T) {
	client := &fakeTransactWriteClient{}
	store := NewDynamoAdminStore(client, dynamoTestConfig())
	product := Product{
		ID:            "prod_admin_001",
		Slug:          "admin-mango-kit",
		Name:          "Admin Mango Kit",
		PriceCents:    2999,
		ImageURL:      "/images/products/admin-mango.jpg",
		Status:        StatusActive,
		SortOrder:     14,
		StockQuantity: 0,
		CreatedAt:     time.Date(2026, 6, 7, 9, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC),
		CategorySlugs: []string{"market-finds", "snacks-sweets"},
		Variants:      []ProductVariant{{ID: "var_small", Label: "Small", StockQuantity: 5, Status: StatusActive, SortOrder: 10}},
	}

	written, err := store.CreateProduct(context.Background(), product)
	if err != nil {
		t.Fatalf("CreateProduct returned error: %v", err)
	}
	if written.Version != 1 {
		t.Fatalf("created Version = %d, want 1", written.Version)
	}
	input := client.inputs[0]
	if len(input.TransactItems) != 4 {
		t.Fatalf("transaction item count = %d, want product, slug lock, and two memberships", len(input.TransactItems))
	}
	productPut := input.TransactItems[0].Put
	if productPut == nil {
		t.Fatal("first transaction item is not a Put")
	}
	if got := aws.ToString(productPut.ConditionExpression); got != "attribute_not_exists(#pk)" {
		t.Fatalf("product create condition = %q", got)
	}
	if got := stringAttribute(t, productPut.Item, "gsi2pk"); got != activeProductsIndexPK {
		t.Fatalf("active product public index pk = %q", got)
	}
	if got := stringAttribute(t, productPut.Item, "gsi3pk"); got != recentProductsIndexPK {
		t.Fatalf("active product recent index pk = %q", got)
	}
	if got := stringAttribute(t, productPut.Item, "gsi4pk"); got != adminProductsIndexPK {
		t.Fatalf("product admin index pk = %q", got)
	}
	productRoundTrip, err := productFromItem(productPut.Item)
	if err != nil {
		t.Fatalf("created product item did not round trip: %v", err)
	}
	if productRoundTrip.Version != 1 || len(productRoundTrip.Variants) != 1 || productRoundTrip.Variants[0].ID != "var_small" {
		t.Fatalf("created product round trip = %#v", productRoundTrip)
	}

	slugPut := input.TransactItems[1].Put
	if slugPut == nil {
		t.Fatal("second transaction item is not slug lock Put")
	}
	if _, ok := slugPut.Item["gsi1pk"]; ok {
		t.Fatal("slug lock unexpectedly includes gsi1pk")
	}
	if _, ok := slugPut.Item["gsi4pk"]; ok {
		t.Fatal("slug lock unexpectedly includes gsi4pk")
	}
	if got := stringAttribute(t, slugPut.Item, "pk"); got != "PRODUCT_SLUG#admin-mango-kit" {
		t.Fatalf("slug lock pk = %q", got)
	}
	if got := stringAttribute(t, slugPut.Item, "product_id"); got != product.ID {
		t.Fatalf("slug lock product_id = %q", got)
	}
	for index, wantCategory := range []string{"market-finds", "snacks-sweets"} {
		put := input.TransactItems[index+2].Put
		if put == nil {
			t.Fatalf("membership transaction item %d is not a Put", index+2)
		}
		if got := stringAttribute(t, put.Item, "pk"); got != "CATEGORY#"+wantCategory {
			t.Fatalf("membership pk = %q", got)
		}
		if _, ok := put.Item["gsi4pk"]; ok {
			t.Fatal("membership unexpectedly includes gsi4pk")
		}
		membership, err := productFromItem(put.Item)
		if err != nil {
			t.Fatalf("membership item did not round trip: %v", err)
		}
		if membership.Name != product.Name || len(membership.Variants) != 1 || membership.Version != 1 {
			t.Fatalf("membership denormalized product = %#v", membership)
		}
	}
}

func TestDynamoAdminStoreUpdateProductVersionAndReplacesMembershipRows(t *testing.T) {
	client := &fakeTransactWriteClient{}
	store := NewDynamoAdminStore(client, dynamoTestConfig())
	previous := Product{
		ID:            "prod_admin_002",
		Slug:          "teak-elephant",
		Name:          "Teak Elephant",
		PriceCents:    3999,
		Status:        StatusActive,
		SortOrder:     5,
		StockQuantity: 2,
		Version:       3,
		CreatedAt:     time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC),
		CategorySlugs: []string{"home-decor", "souvenirs"},
	}
	updated := previous
	updated.Name = "Carved Teak Elephant"
	updated.ImageURL = "/images/products/teak-new.jpg"
	updated.SortOrder = 7
	updated.StockQuantity = 9
	updated.CategorySlugs = []string{"home-decor", "market-finds"}
	updated.UpdatedAt = time.Date(2026, 6, 7, 11, 0, 0, 0, time.UTC)

	written, err := store.UpdateProduct(context.Background(), previous, updated)
	if err != nil {
		t.Fatalf("UpdateProduct returned error: %v", err)
	}
	if written.Version != 4 {
		t.Fatalf("updated Version = %d, want 4", written.Version)
	}
	items := client.inputs[0].TransactItems
	if len(items) != 6 {
		t.Fatalf("transaction item count = %d, want product, slug lock, two stale deletes, two puts", len(items))
	}
	productPut := items[0].Put
	if got := aws.ToString(productPut.ConditionExpression); got != "#version = :expected_version" {
		t.Fatalf("product update condition = %q", got)
	}
	if got := numberAttribute(t, productPut.ExpressionAttributeValues, ":expected_version"); got != "3" {
		t.Fatalf("expected version value = %q", got)
	}
	for index, wantSK := range []string{"PRODUCT#0000000005#prod_admin_002", "PRODUCT#0000000005#prod_admin_002"} {
		deleteItem := items[index+2].Delete
		if deleteItem == nil {
			t.Fatalf("stale membership item %d is not a Delete", index+2)
		}
		if got := stringAttribute(t, deleteItem.Key, "sk"); got != wantSK {
			t.Fatalf("stale membership delete sk = %q", got)
		}
	}
	for _, index := range []int{4, 5} {
		membership, err := productFromItem(items[index].Put.Item)
		if err != nil {
			t.Fatalf("updated membership did not round trip: %v", err)
		}
		if membership.Name != updated.Name || membership.ImageURL != updated.ImageURL || membership.StockQuantity != 9 || membership.Version != 4 {
			t.Fatalf("updated membership denormalized product = %#v", membership)
		}
	}
}

func TestDynamoAdminStoreDuplicateSlugAndStaleVersionConflicts(t *testing.T) {
	product := Product{ID: "prod_admin_003", Slug: "duplicate-slug", Status: StatusActive, Version: 2}

	duplicateClient := &fakeTransactWriteClient{err: transactionCanceledAt(1)}
	duplicateStore := NewDynamoAdminStore(duplicateClient, dynamoTestConfig())
	if _, err := duplicateStore.CreateProduct(context.Background(), product); !errors.Is(err, ErrSlugConflict) {
		t.Fatalf("CreateProduct duplicate err = %v, want ErrSlugConflict", err)
	}

	staleClient := &fakeTransactWriteClient{err: transactionCanceledAt(0)}
	staleStore := NewDynamoAdminStore(staleClient, dynamoTestConfig())
	if _, err := staleStore.UpdateProduct(context.Background(), product, product); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("UpdateProduct stale err = %v, want ErrVersionConflict", err)
	}
}

func TestDynamoAdminStoreArchiveProductRemovesPublicIndexesAndKeepsAdminRows(t *testing.T) {
	client := &fakeTransactWriteClient{}
	store := NewDynamoAdminStore(client, dynamoTestConfig())
	product := Product{
		ID:            "prod_admin_004",
		Slug:          "archive-me",
		Name:          "Archive Me",
		Status:        StatusActive,
		SortOrder:     22,
		Version:       6,
		CreatedAt:     time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC),
		CategorySlugs: []string{"pantry"},
	}

	archived, err := store.ArchiveProduct(context.Background(), product)
	if err != nil {
		t.Fatalf("ArchiveProduct returned error: %v", err)
	}
	if archived.Status != StatusArchived || archived.Version != 7 {
		t.Fatalf("archived product = %#v", archived)
	}
	items := client.inputs[0].TransactItems
	productPut := items[0].Put.Item
	if _, ok := productPut["gsi2pk"]; ok {
		t.Fatal("archived product retained public index pk")
	}
	if _, ok := productPut["gsi3pk"]; ok {
		t.Fatal("archived product retained recent index pk")
	}
	if got := stringAttribute(t, productPut, "gsi4pk"); got != adminProductsIndexPK {
		t.Fatalf("archived product admin index pk = %q", got)
	}
	if got := stringAttribute(t, productPut, "pk"); got != "PRODUCT#prod_admin_004" {
		t.Fatalf("archived product pk = %q", got)
	}
	membership, err := productFromItem(items[len(items)-1].Put.Item)
	if err != nil {
		t.Fatalf("archived membership did not round trip: %v", err)
	}
	if membership.Status != StatusArchived || membership.Version != 7 {
		t.Fatalf("archived membership = %#v", membership)
	}
}

func TestDynamoAdminStoreCategoryCreateUpdateArchiveUsesVersionAndPublicIndex(t *testing.T) {
	client := &fakeTransactWriteClient{}
	store := NewDynamoAdminStore(client, dynamoTestConfig())
	category := Category{Slug: "admin-category", Name: "Admin Category", Status: StatusActive, SortOrder: 9, UpdatedAt: time.Date(2026, 6, 7, 13, 0, 0, 0, time.UTC)}

	created, err := store.CreateCategory(context.Background(), category)
	if err != nil {
		t.Fatalf("CreateCategory returned error: %v", err)
	}
	if created.Version != 1 {
		t.Fatalf("created category version = %d, want 1", created.Version)
	}
	if got := stringAttribute(t, client.inputs[0].TransactItems[0].Put.Item, "gsi2pk"); got != activeCategoriesIndexPK {
		t.Fatalf("active category public index pk = %q", got)
	}
	if got := stringAttribute(t, client.inputs[0].TransactItems[0].Put.Item, "gsi4pk"); got != adminCategoriesIndexPK {
		t.Fatalf("category admin index pk = %q", got)
	}

	updated := created
	updated.Name = "Updated Category"
	written, err := store.UpdateCategory(context.Background(), updated, 1)
	if err != nil {
		t.Fatalf("UpdateCategory returned error: %v", err)
	}
	if written.Version != 2 {
		t.Fatalf("updated category version = %d, want 2", written.Version)
	}
	if got := numberAttribute(t, client.inputs[1].TransactItems[0].Put.ExpressionAttributeValues, ":expected_version"); got != "1" {
		t.Fatalf("category expected version = %q", got)
	}

	archived, err := store.ArchiveCategory(context.Background(), written)
	if err != nil {
		t.Fatalf("ArchiveCategory returned error: %v", err)
	}
	if archived.Status != StatusArchived || archived.Version != 3 {
		t.Fatalf("archived category = %#v", archived)
	}
	if _, ok := client.inputs[2].TransactItems[0].Put.Item["gsi2pk"]; ok {
		t.Fatal("archived category retained public index pk")
	}
	if got := stringAttribute(t, client.inputs[2].TransactItems[0].Put.Item, "gsi4pk"); got != adminCategoriesIndexPK {
		t.Fatalf("archived category admin index pk = %q", got)
	}
}

func TestDynamoAdminStoreDuplicateCategorySlugReturnsSlugConflict(t *testing.T) {
	client := &fakeTransactWriteClient{err: transactionCanceledAt(0)}
	store := NewDynamoAdminStore(client, dynamoTestConfig())
	category := Category{Slug: "duplicate-category", Name: "Duplicate Category", Status: StatusActive, SortOrder: 11}

	if _, err := store.CreateCategory(context.Background(), category); !errors.Is(err, ErrSlugConflict) {
		t.Fatalf("CreateCategory duplicate err = %v, want ErrSlugConflict", err)
	}
}

func setMissingCatalogTableEnvironment(t *testing.T, appEnvironment string) {
	t.Helper()

	t.Setenv(appenv.EnvAppEnvironment, appEnvironment)
	t.Setenv(EnvTableName, "")
	t.Setenv(EnvSlugIndexName, "")
	t.Setenv(EnvPublicIndexName, "")
	t.Setenv(EnvRecentIndexName, "")
	t.Setenv(EnvEntityIndexName, "")
}

func stringAttribute(t *testing.T, attributes map[string]types.AttributeValue, key string) string {
	t.Helper()

	value, ok := attributes[key]
	if !ok {
		t.Fatalf("attribute %q is missing", key)
	}
	member, ok := value.(*types.AttributeValueMemberS)
	if !ok {
		t.Fatalf("attribute %q is %T, want string attribute", key, value)
	}

	return member.Value
}

func numberAttribute(t *testing.T, attributes map[string]types.AttributeValue, key string) string {
	t.Helper()

	value, ok := attributes[key]
	if !ok {
		t.Fatalf("attribute %q is missing", key)
	}
	member, ok := value.(*types.AttributeValueMemberN)
	if !ok {
		t.Fatalf("attribute %q is %T, want number attribute", key, value)
	}

	return member.Value
}

func dynamoTestConfig() DynamoConfig {
	return DynamoConfig{
		TableName:       "catalog-table",
		SlugIndexName:   "slug-index",
		PublicIndexName: "public-index",
		RecentIndexName: "recent-index",
		EntityIndexName: "entity-index",
	}
}

type fakeQueryClient struct {
	inputs  []dynamodb.QueryInput
	outputs []*dynamodb.QueryOutput
	err     error
}

type fakeGetItemClient struct {
	inputs []dynamodb.GetItemInput
	output *dynamodb.GetItemOutput
	err    error
}

type fakeTransactWriteClient struct {
	inputs []dynamodb.TransactWriteItemsInput
	err    error
}

type catalogMetricRecorder struct {
	metrics []observability.Metric
}

func (r *catalogMetricRecorder) Record(metric observability.Metric) {
	r.metrics = append(r.metrics, metric)
}

func (f *fakeTransactWriteClient) TransactWriteItems(ctx context.Context, input *dynamodb.TransactWriteItemsInput, optFns ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	f.inputs = append(f.inputs, *input)
	if f.err != nil {
		return nil, f.err
	}

	return &dynamodb.TransactWriteItemsOutput{}, nil
}

func transactionCanceledAt(index int) error {
	reasons := make([]types.CancellationReason, index+1)
	reasons[index] = types.CancellationReason{Code: aws.String("ConditionalCheckFailed")}
	return &types.TransactionCanceledException{CancellationReasons: reasons}
}

func (f *fakeQueryClient) Query(ctx context.Context, input *dynamodb.QueryInput, optFns ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	f.inputs = append(f.inputs, *input)
	if f.err != nil {
		return nil, f.err
	}
	if len(f.outputs) == 0 {
		return &dynamodb.QueryOutput{}, nil
	}

	output := f.outputs[0]
	f.outputs = f.outputs[1:]
	return output, nil
}

func (f *fakeGetItemClient) GetItem(ctx context.Context, input *dynamodb.GetItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.inputs = append(f.inputs, *input)
	if f.err != nil {
		return nil, f.err
	}
	if f.output == nil {
		return &dynamodb.GetItemOutput{}, nil
	}
	return f.output, nil
}

func assertCatalogMetric(t *testing.T, recorder *catalogMetricRecorder, name string, unit string, dimensions map[string]string) {
	t.Helper()
	for _, metric := range recorder.metrics {
		if metric.Name != name || metric.Unit != unit {
			continue
		}
		if catalogMetricDimensionsMatch(metric, dimensions) {
			return
		}
	}
	t.Fatalf("metric %q with unit %q and dimensions %#v not recorded; got %#v", name, unit, dimensions, recorder.metrics)
}

func catalogMetricDimensionsMatch(metric observability.Metric, dimensions map[string]string) bool {
	if len(metric.Dimensions) != len(dimensions) {
		return false
	}
	for _, dimension := range metric.Dimensions {
		if dimensions[dimension.Name] != dimension.Value {
			return false
		}
	}
	return true
}
