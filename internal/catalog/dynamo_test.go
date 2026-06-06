package catalog

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

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
		Name:          "Mango Sticky Rice Kit",
		Description:   "A shelf-stable dessert gift kit.",
		PriceCents:    2499,
		ImageURL:      "/images/products/mango.jpg",
		Status:        StatusActive,
		SortOrder:     12,
		StockQuantity: 0,
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
		CategorySlugs: []string{"food", "gifts"},
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
	if !got.OutOfStock() {
		t.Fatal("OutOfStock = false, want true when stock quantity is zero")
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
	var categoryProducts int
	var marked int
	for _, item := range items {
		if stringAttribute(t, item, "seed_group") == DemoSeedGroup {
			marked++
		}

		switch got := stringAttribute(t, item, "entity_type"); got {
		case entityCategory:
			categories++
		case entityProduct:
			products++
			if got := stringAttribute(t, item, "image_url"); !strings.HasPrefix(got, "/images/products/") {
				t.Fatalf("product image_url = %q, want /images/products/ prefix", got)
			}
			if got := stringAttribute(t, item, "created_at"); got == "" {
				t.Fatal("product created_at is empty")
			}
		case entityCategoryProduct:
			categoryProducts++
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

func TestDynamoStoreListActiveProductsUsesPublicIndex(t *testing.T) {
	product := Product{
		ID:            "prod_001",
		Slug:          "mango-sticky-rice-kit",
		Name:          "Mango Sticky Rice Kit",
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
		Name:          "Elephant Pouch Set",
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
		Name:          "Mango Sticky Rice Kit",
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

type fakeQueryClient struct {
	inputs  []dynamodb.QueryInput
	outputs []*dynamodb.QueryOutput
	err     error
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
