package catalog

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const (
	entityProduct         = "PRODUCT"
	entityCategory        = "CATEGORY"
	entityCategoryProduct = "CATEGORY_PRODUCT"

	productSK               = "PRODUCT"
	categorySK              = "CATEGORY"
	activeProductsIndexPK   = "PRODUCTS#ACTIVE"
	activeCategoriesIndexPK = "CATEGORIES#ACTIVE"
)

type DynamoConfig struct {
	TableName       string
	SlugIndexName   string
	PublicIndexName string
}

func DynamoConfigFromEnv() (DynamoConfig, bool) {
	tableName := os.Getenv(EnvTableName)
	if tableName == "" {
		return DynamoConfig{}, false
	}

	config := DynamoConfig{
		TableName:       tableName,
		SlugIndexName:   os.Getenv(EnvSlugIndexName),
		PublicIndexName: os.Getenv(EnvPublicIndexName),
	}
	if config.SlugIndexName == "" {
		config.SlugIndexName = DefaultSlugIndexName
	}
	if config.PublicIndexName == "" {
		config.PublicIndexName = DefaultPublicIndexName
	}

	return config, true
}

func NewStoreFromEnv(ctx context.Context) (Store, bool, error) {
	dynamoConfig, ok := DynamoConfigFromEnv()
	if !ok {
		return EmptyStore{}, false, nil
	}

	awsConfig, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("load AWS config for catalog store: %w", err)
	}

	return NewDynamoStore(dynamodb.NewFromConfig(awsConfig), dynamoConfig), true, nil
}

type queryClient interface {
	Query(ctx context.Context, params *dynamodb.QueryInput, optFns ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
}

type DynamoStore struct {
	client          queryClient
	tableName       string
	slugIndexName   string
	publicIndexName string
}

func NewDynamoStore(client queryClient, config DynamoConfig) *DynamoStore {
	return &DynamoStore{
		client:          client,
		tableName:       config.TableName,
		slugIndexName:   config.SlugIndexName,
		publicIndexName: config.PublicIndexName,
	}
}

func (s *DynamoStore) ListActiveProducts(ctx context.Context, limit int) ([]Product, error) {
	items, err := s.query(ctx, dynamodb.QueryInput{
		TableName:              aws.String(s.tableName),
		IndexName:              aws.String(s.publicIndexName),
		KeyConditionExpression: aws.String("#gsi2pk = :pk"),
		ExpressionAttributeNames: map[string]string{
			"#gsi2pk": "gsi2pk",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: activeProductsIndexPK},
		},
		ScanIndexForward: aws.Bool(true),
	}, limit)
	if err != nil {
		return nil, err
	}

	return productsFromItems(items)
}

func (s *DynamoStore) GetProductBySlug(ctx context.Context, slug string) (Product, bool, error) {
	items, err := s.query(ctx, dynamodb.QueryInput{
		TableName:              aws.String(s.tableName),
		IndexName:              aws.String(s.slugIndexName),
		KeyConditionExpression: aws.String("#gsi1pk = :pk"),
		ExpressionAttributeNames: map[string]string{
			"#gsi1pk": "gsi1pk",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: productSlugIndexPK(slug)},
		},
		Limit:            aws.Int32(1),
		ScanIndexForward: aws.Bool(true),
	}, 1)
	if err != nil {
		return Product{}, false, err
	}
	if len(items) == 0 {
		return Product{}, false, nil
	}

	product, err := productFromItem(items[0])
	if err != nil {
		return Product{}, false, err
	}

	return product, true, nil
}

func (s *DynamoStore) ListActiveCategories(ctx context.Context) ([]Category, error) {
	items, err := s.query(ctx, dynamodb.QueryInput{
		TableName:              aws.String(s.tableName),
		IndexName:              aws.String(s.publicIndexName),
		KeyConditionExpression: aws.String("#gsi2pk = :pk"),
		ExpressionAttributeNames: map[string]string{
			"#gsi2pk": "gsi2pk",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: activeCategoriesIndexPK},
		},
		ScanIndexForward: aws.Bool(true),
	}, 0)
	if err != nil {
		return nil, err
	}

	categories := make([]Category, 0, len(items))
	for _, item := range items {
		category, err := categoryFromItem(item)
		if err != nil {
			return nil, err
		}
		categories = append(categories, category)
	}

	return categories, nil
}

func (s *DynamoStore) ListActiveProductsByCategory(ctx context.Context, categorySlug string, limit int) ([]Product, error) {
	items, err := s.query(ctx, dynamodb.QueryInput{
		TableName:              aws.String(s.tableName),
		KeyConditionExpression: aws.String("#pk = :pk AND begins_with(#sk, :product_prefix)"),
		FilterExpression:       aws.String("#status = :active"),
		ExpressionAttributeNames: map[string]string{
			"#pk":     "pk",
			"#sk":     "sk",
			"#status": "status",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":             &types.AttributeValueMemberS{Value: categoryPK(categorySlug)},
			":product_prefix": &types.AttributeValueMemberS{Value: "PRODUCT#"},
			":active":         &types.AttributeValueMemberS{Value: string(StatusActive)},
		},
		ScanIndexForward: aws.Bool(true),
	}, limit)
	if err != nil {
		return nil, err
	}

	return productsFromItems(items)
}

func (s *DynamoStore) query(ctx context.Context, input dynamodb.QueryInput, limit int) ([]map[string]types.AttributeValue, error) {
	if limit > 0 && input.Limit == nil {
		input.Limit = aws.Int32(int32(limit))
	}

	var items []map[string]types.AttributeValue
	for {
		output, err := s.client.Query(ctx, &input)
		if err != nil {
			return nil, err
		}
		items = append(items, output.Items...)
		if limit > 0 && len(items) >= limit {
			return items[:limit], nil
		}
		if len(output.LastEvaluatedKey) == 0 {
			return items, nil
		}

		input.ExclusiveStartKey = output.LastEvaluatedKey
		if limit > 0 {
			input.Limit = aws.Int32(int32(limit - len(items)))
		}
	}
}

type catalogItem struct {
	PK            string   `dynamodbav:"pk"`
	SK            string   `dynamodbav:"sk"`
	EntityType    string   `dynamodbav:"entity_type"`
	ID            string   `dynamodbav:"id,omitempty"`
	Slug          string   `dynamodbav:"slug,omitempty"`
	Name          string   `dynamodbav:"name,omitempty"`
	Description   string   `dynamodbav:"description,omitempty"`
	PriceCents    *int     `dynamodbav:"price_cents,omitempty"`
	ImageURL      string   `dynamodbav:"image_url,omitempty"`
	Status        Status   `dynamodbav:"status,omitempty"`
	SortOrder     int      `dynamodbav:"sort_order"`
	StockQuantity *int     `dynamodbav:"stock_quantity,omitempty"`
	UpdatedAt     string   `dynamodbav:"updated_at,omitempty"`
	CategorySlugs []string `dynamodbav:"category_slugs,omitempty"`
	GSI1PK        string   `dynamodbav:"gsi1pk,omitempty"`
	GSI1SK        string   `dynamodbav:"gsi1sk,omitempty"`
	GSI2PK        string   `dynamodbav:"gsi2pk,omitempty"`
	GSI2SK        string   `dynamodbav:"gsi2sk,omitempty"`
}

func productItem(product Product) (map[string]types.AttributeValue, error) {
	item := catalogItem{
		PK:            productPK(product.ID),
		SK:            productSK,
		EntityType:    entityProduct,
		ID:            product.ID,
		Slug:          product.Slug,
		Name:          product.Name,
		Description:   product.Description,
		PriceCents:    intPtr(product.PriceCents),
		ImageURL:      product.ImageURL,
		Status:        product.Status,
		SortOrder:     product.SortOrder,
		StockQuantity: intPtr(product.StockQuantity),
		UpdatedAt:     formatUpdatedAt(product.UpdatedAt),
		CategorySlugs: product.CategorySlugs,
		GSI1PK:        productSlugIndexPK(product.Slug),
		GSI1SK:        productSK,
	}
	if product.Status == StatusActive {
		item.GSI2PK = activeProductsIndexPK
		item.GSI2SK = productPublicIndexSK(product)
	}

	return attributevalue.MarshalMap(item)
}

func categoryItem(category Category) (map[string]types.AttributeValue, error) {
	item := catalogItem{
		PK:          categoryPK(category.Slug),
		SK:          categorySK,
		EntityType:  entityCategory,
		Slug:        category.Slug,
		Name:        category.Name,
		Description: category.Description,
		Status:      category.Status,
		SortOrder:   category.SortOrder,
		UpdatedAt:   formatUpdatedAt(category.UpdatedAt),
	}
	if category.Status == StatusActive {
		item.GSI2PK = activeCategoriesIndexPK
		item.GSI2SK = categoryPublicIndexSK(category)
	}

	return attributevalue.MarshalMap(item)
}

func categoryProductItem(categorySlug string, product Product) (map[string]types.AttributeValue, error) {
	return attributevalue.MarshalMap(catalogItem{
		PK:            categoryPK(categorySlug),
		SK:            categoryProductSK(product),
		EntityType:    entityCategoryProduct,
		ID:            product.ID,
		Slug:          product.Slug,
		Name:          product.Name,
		Description:   product.Description,
		PriceCents:    intPtr(product.PriceCents),
		ImageURL:      product.ImageURL,
		Status:        product.Status,
		SortOrder:     product.SortOrder,
		StockQuantity: intPtr(product.StockQuantity),
		UpdatedAt:     formatUpdatedAt(product.UpdatedAt),
	})
}

func productsFromItems(items []map[string]types.AttributeValue) ([]Product, error) {
	products := make([]Product, 0, len(items))
	for _, item := range items {
		product, err := productFromItem(item)
		if err != nil {
			return nil, err
		}
		products = append(products, product)
	}

	return products, nil
}

func productFromItem(item map[string]types.AttributeValue) (Product, error) {
	var record catalogItem
	if err := attributevalue.UnmarshalMap(item, &record); err != nil {
		return Product{}, err
	}
	if record.EntityType != entityProduct && record.EntityType != entityCategoryProduct {
		return Product{}, fmt.Errorf("catalog item is %q, not product", record.EntityType)
	}

	updatedAt, err := parseUpdatedAt(record.UpdatedAt)
	if err != nil {
		return Product{}, err
	}

	return Product{
		ID:            record.ID,
		Slug:          record.Slug,
		Name:          record.Name,
		Description:   record.Description,
		PriceCents:    intValue(record.PriceCents),
		ImageURL:      record.ImageURL,
		Status:        record.Status,
		SortOrder:     record.SortOrder,
		StockQuantity: intValue(record.StockQuantity),
		UpdatedAt:     updatedAt,
		CategorySlugs: record.CategorySlugs,
	}, nil
}

func categoryFromItem(item map[string]types.AttributeValue) (Category, error) {
	var record catalogItem
	if err := attributevalue.UnmarshalMap(item, &record); err != nil {
		return Category{}, err
	}
	if record.EntityType != entityCategory {
		return Category{}, fmt.Errorf("catalog item is %q, not category", record.EntityType)
	}

	updatedAt, err := parseUpdatedAt(record.UpdatedAt)
	if err != nil {
		return Category{}, err
	}

	return Category{
		Slug:        record.Slug,
		Name:        record.Name,
		Description: record.Description,
		Status:      record.Status,
		SortOrder:   record.SortOrder,
		UpdatedAt:   updatedAt,
	}, nil
}

func productPK(id string) string {
	return "PRODUCT#" + id
}

func categoryPK(slug string) string {
	return "CATEGORY#" + slug
}

func productSlugIndexPK(slug string) string {
	return "PRODUCT_SLUG#" + slug
}

func productPublicIndexSK(product Product) string {
	return fmt.Sprintf("PRODUCT#%010d#%s", product.SortOrder, product.ID)
}

func categoryPublicIndexSK(category Category) string {
	return fmt.Sprintf("CATEGORY#%010d#%s", category.SortOrder, category.Slug)
}

func categoryProductSK(product Product) string {
	return fmt.Sprintf("PRODUCT#%010d#%s", product.SortOrder, product.ID)
}

func formatUpdatedAt(updatedAt time.Time) string {
	if updatedAt.IsZero() {
		return ""
	}

	return updatedAt.UTC().Format(time.RFC3339)
}

func parseUpdatedAt(updatedAt string) (time.Time, error) {
	if updatedAt == "" {
		return time.Time{}, nil
	}

	parsed, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse catalog updated_at %q: %w", updatedAt, err)
	}

	return parsed, nil
}

func intPtr(value int) *int {
	return &value
}

func intValue(value *int) int {
	if value == nil {
		return 0
	}

	return *value
}
