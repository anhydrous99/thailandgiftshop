package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/appenv"
	"github.com/anhydrous99/thailandgiftshop/internal/observability"
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
	entityProductSlugLock = "PRODUCT_SLUG_LOCK"

	productSK               = "PRODUCT"
	categorySK              = "CATEGORY"
	slugLockSK              = "SLUG_LOCK"
	activeProductsIndexPK   = "PRODUCTS#ACTIVE"
	recentProductsIndexPK   = "PRODUCTS#ACTIVE#RECENT"
	activeCategoriesIndexPK = "CATEGORIES#ACTIVE"
	adminProductsIndexPK    = "PRODUCTS"
	adminCategoriesIndexPK  = "CATEGORIES"
)

var (
	ErrSlugConflict              = errors.New("catalog slug already exists")
	ErrVersionConflict           = errors.New("catalog version conflict")
	ErrCatalogStoreNotConfigured = errors.New("catalog store not configured")
)

type DynamoConfig struct {
	TableName       string
	SlugIndexName   string
	PublicIndexName string
	RecentIndexName string
	EntityIndexName string
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
		RecentIndexName: os.Getenv(EnvRecentIndexName),
		EntityIndexName: os.Getenv(EnvEntityIndexName),
	}
	if config.SlugIndexName == "" {
		config.SlugIndexName = DefaultSlugIndexName
	}
	if config.PublicIndexName == "" {
		config.PublicIndexName = DefaultPublicIndexName
	}
	if config.RecentIndexName == "" {
		config.RecentIndexName = DefaultRecentIndexName
	}
	if config.EntityIndexName == "" {
		config.EntityIndexName = DefaultEntityIndexName
	}

	return config, true
}

func NewStoreFromEnv(ctx context.Context) (Store, bool, error) {
	return NewStoreFromEnvWithRecorder(ctx, nil)
}

func NewStoreFromEnvWithRecorder(ctx context.Context, metrics observability.Recorder) (Store, bool, error) {
	dynamoConfig, ok := DynamoConfigFromEnv()
	if !ok {
		if appenv.IsProduction() {
			return nil, false, ErrCatalogStoreNotConfigured
		}
		return EmptyStore{}, false, nil
	}

	awsConfig, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("load AWS config for catalog store: %w", err)
	}

	return NewDynamoStoreWithRecorder(dynamodb.NewFromConfig(awsConfig), dynamoConfig, metrics), true, nil
}

type queryClient interface {
	Query(ctx context.Context, params *dynamodb.QueryInput, optFns ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
}

type transactWriteClient interface {
	TransactWriteItems(ctx context.Context, params *dynamodb.TransactWriteItemsInput, optFns ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error)
}

type getItemClient interface {
	GetItem(ctx context.Context, params *dynamodb.GetItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
}

type DynamoStore struct {
	client          queryClient
	getClient       getItemClient
	writeClient     transactWriteClient
	tableName       string
	slugIndexName   string
	publicIndexName string
	recentIndexName string
	entityIndexName string
	metrics         observability.Recorder
}

func NewDynamoAdminStore(client transactWriteClient, config DynamoConfig) *DynamoStore {
	return NewDynamoAdminStoreWithRecorder(client, config, nil)
}

func NewDynamoAdminStoreWithRecorder(client transactWriteClient, config DynamoConfig, metrics observability.Recorder) *DynamoStore {
	store := NewDynamoStoreWithRecorder(nil, config, metrics)
	store.writeClient = client
	return store
}

func NewDynamoReadWriteStore(client interface {
	queryClient
	transactWriteClient
	getItemClient
}, config DynamoConfig) *DynamoStore {
	return NewDynamoReadWriteStoreWithRecorder(client, config, nil)
}

func NewDynamoReadWriteStoreWithRecorder(client interface {
	queryClient
	transactWriteClient
	getItemClient
}, config DynamoConfig, metrics observability.Recorder) *DynamoStore {
	store := NewDynamoStoreWithRecorder(client, config, metrics)
	store.getClient = client
	store.writeClient = client
	return store
}

func NewAdminStoreFromEnv(ctx context.Context) (AdminStore, bool, error) {
	return NewAdminStoreFromEnvWithRecorder(ctx, nil)
}

func NewAdminStoreFromEnvWithRecorder(ctx context.Context, metrics observability.Recorder) (AdminStore, bool, error) {
	dynamoConfig, ok := DynamoConfigFromEnv()
	if !ok {
		if appenv.IsProduction() {
			return nil, false, ErrCatalogStoreNotConfigured
		}
		return NewMemoryStore(nil, nil), false, nil
	}
	awsConfig, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("load AWS config for catalog admin store: %w", err)
	}
	return NewDynamoReadWriteStoreWithRecorder(dynamodb.NewFromConfig(awsConfig), dynamoConfig, metrics), true, nil
}

func (s *DynamoStore) ListProducts(ctx context.Context) ([]Product, error) {
	items, err := s.query(ctx, dynamodb.QueryInput{
		TableName:              aws.String(s.tableName),
		IndexName:              aws.String(s.entityIndexName),
		KeyConditionExpression: aws.String("#gsi4pk = :pk"),
		ExpressionAttributeNames: map[string]string{
			"#gsi4pk": "gsi4pk",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: adminProductsIndexPK},
		},
		ScanIndexForward: aws.Bool(true),
	}, 0, "ListProducts")
	if err != nil {
		return nil, err
	}
	products, err := productsFromItems(items)
	if err != nil {
		return nil, err
	}
	sortProducts(products)
	return products, nil
}

func (s *DynamoStore) GetProductByID(ctx context.Context, productID string) (Product, bool, error) {
	if s.getClient == nil {
		return Product{}, false, fmt.Errorf("catalog admin store is missing a get client")
	}
	started := time.Now()
	output, err := s.getClient.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: productPK(productID)},
			"sk": &types.AttributeValueMemberS{Value: productSK},
		},
	})
	s.recordCatalogOperation("GetItem", "GetProductByID", time.Since(started))
	if err != nil {
		return Product{}, false, err
	}
	if len(output.Item) == 0 {
		return Product{}, false, nil
	}
	product, err := productFromItem(output.Item)
	if err != nil {
		return Product{}, false, err
	}
	return product, true, nil
}

func (s *DynamoStore) ListCategories(ctx context.Context) ([]Category, error) {
	items, err := s.query(ctx, dynamodb.QueryInput{
		TableName:              aws.String(s.tableName),
		IndexName:              aws.String(s.entityIndexName),
		KeyConditionExpression: aws.String("#gsi4pk = :pk"),
		ExpressionAttributeNames: map[string]string{
			"#gsi4pk": "gsi4pk",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: adminCategoriesIndexPK},
		},
		ScanIndexForward: aws.Bool(true),
	}, 0, "ListCategories")
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
	sort.SliceStable(categories, func(i int, j int) bool {
		if categories[i].SortOrder == categories[j].SortOrder {
			return categories[i].Name < categories[j].Name
		}
		return categories[i].SortOrder < categories[j].SortOrder
	})
	return categories, nil
}

func (s *DynamoStore) CreateProduct(ctx context.Context, product Product) (Product, error) {
	if err := product.Validate(); err != nil {
		return Product{}, err
	}
	product.Version = 1
	items, err := s.productWriteItems(Product{}, product, true)
	if err != nil {
		return Product{}, err
	}
	if err := s.transactWrite(ctx, items, "CreateProduct"); err != nil {
		return Product{}, err
	}

	return product, nil
}

func (s *DynamoStore) UpdateProduct(ctx context.Context, previous Product, product Product) (Product, error) {
	return s.updateProduct(ctx, previous, product, "UpdateProduct")
}

func (s *DynamoStore) updateProduct(ctx context.Context, previous Product, product Product, method string) (Product, error) {
	if err := product.Validate(); err != nil {
		return Product{}, err
	}
	product.Version = previous.Version + 1
	if product.CreatedAt.IsZero() {
		product.CreatedAt = previous.CreatedAt
	}
	items, err := s.productWriteItems(previous, product, false)
	if err != nil {
		return Product{}, err
	}
	if err := s.transactWrite(ctx, items, method); err != nil {
		return Product{}, err
	}

	return product, nil
}

func (s *DynamoStore) ArchiveProduct(ctx context.Context, product Product) (Product, error) {
	archived := product
	archived.Status = StatusArchived
	return s.updateProduct(ctx, product, archived, "ArchiveProduct")
}

func (s *DynamoStore) CreateCategory(ctx context.Context, category Category) (Category, error) {
	category.Version = 1
	item, err := categoryItem(category)
	if err != nil {
		return Category{}, err
	}
	items := []types.TransactWriteItem{{Put: &types.Put{
		TableName:           aws.String(s.tableName),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(#pk)"),
		ExpressionAttributeNames: map[string]string{
			"#pk": "pk",
		},
	}}}
	if err := s.transactWrite(ctx, items, "CreateCategory"); err != nil {
		if errors.Is(err, ErrVersionConflict) {
			return Category{}, fmt.Errorf("%w", ErrSlugConflict)
		}
		return Category{}, err
	}

	return category, nil
}

func (s *DynamoStore) UpdateCategory(ctx context.Context, category Category, expectedVersion int) (Category, error) {
	return s.updateCategory(ctx, category, expectedVersion, "UpdateCategory")
}

func (s *DynamoStore) updateCategory(ctx context.Context, category Category, expectedVersion int, method string) (Category, error) {
	category.Version = expectedVersion + 1
	item, err := categoryItem(category)
	if err != nil {
		return Category{}, err
	}
	items := []types.TransactWriteItem{{Put: &types.Put{
		TableName:           aws.String(s.tableName),
		Item:                item,
		ConditionExpression: aws.String("#version = :expected_version"),
		ExpressionAttributeNames: map[string]string{
			"#version": "version",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":expected_version": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", expectedVersion)},
		},
	}}}
	if err := s.transactWrite(ctx, items, method); err != nil {
		return Category{}, err
	}

	return category, nil
}

func (s *DynamoStore) ArchiveCategory(ctx context.Context, category Category) (Category, error) {
	archived := category
	archived.Status = StatusArchived
	return s.updateCategory(ctx, archived, category.Version, "ArchiveCategory")
}

func NewDynamoStore(client queryClient, config DynamoConfig) *DynamoStore {
	return NewDynamoStoreWithRecorder(client, config, nil)
}

func NewDynamoStoreWithRecorder(client queryClient, config DynamoConfig, metrics observability.Recorder) *DynamoStore {
	return &DynamoStore{
		client:          client,
		tableName:       config.TableName,
		slugIndexName:   config.SlugIndexName,
		publicIndexName: config.PublicIndexName,
		recentIndexName: config.RecentIndexName,
		entityIndexName: config.EntityIndexName,
		metrics:         metrics,
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
	}, limit, "ListActiveProducts")
	if err != nil {
		return nil, err
	}

	return productsFromItems(items)
}

func (s *DynamoStore) ListRecentlyAddedProducts(ctx context.Context, limit int) ([]Product, error) {
	items, err := s.query(ctx, dynamodb.QueryInput{
		TableName:              aws.String(s.tableName),
		IndexName:              aws.String(s.recentIndexName),
		KeyConditionExpression: aws.String("#gsi3pk = :pk"),
		ExpressionAttributeNames: map[string]string{
			"#gsi3pk": "gsi3pk",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: recentProductsIndexPK},
		},
		ScanIndexForward: aws.Bool(false),
	}, limit, "ListRecentlyAddedProducts")
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
	}, 1, "GetProductBySlug")
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
	}, 0, "ListActiveCategories")
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
	}, limit, "ListActiveProductsByCategory")
	if err != nil {
		return nil, err
	}

	return productsFromItems(items)
}

func (s *DynamoStore) query(ctx context.Context, input dynamodb.QueryInput, limit int, method string) ([]map[string]types.AttributeValue, error) {
	if limit > 0 && input.Limit == nil {
		input.Limit = aws.Int32(int32(limit))
	}

	var items []map[string]types.AttributeValue
	for {
		started := time.Now()
		output, err := s.client.Query(ctx, &input)
		s.recordCatalogOperation("Query", method, time.Since(started))
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

func sortProducts(products []Product) {
	sort.SliceStable(products, func(i int, j int) bool {
		if products[i].SortOrder == products[j].SortOrder {
			return products[i].Name < products[j].Name
		}
		return products[i].SortOrder < products[j].SortOrder
	})
}

func (s *DynamoStore) productWriteItems(previous Product, product Product, create bool) ([]types.TransactWriteItem, error) {
	item, err := productItem(product)
	if err != nil {
		return nil, err
	}
	slugLock, err := productSlugLockItem(product)
	if err != nil {
		return nil, err
	}

	putProduct := &types.Put{
		TableName: aws.String(s.tableName),
		Item:      item,
	}
	if create {
		putProduct.ConditionExpression = aws.String("attribute_not_exists(#pk)")
		putProduct.ExpressionAttributeNames = map[string]string{"#pk": "pk"}
	} else {
		putProduct.ConditionExpression = aws.String("#version = :expected_version")
		putProduct.ExpressionAttributeNames = map[string]string{"#version": "version"}
		putProduct.ExpressionAttributeValues = map[string]types.AttributeValue{
			":expected_version": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", previous.Version)},
		}
	}

	items := []types.TransactWriteItem{{Put: putProduct}}
	lockPut := &types.Put{
		TableName: aws.String(s.tableName),
		Item:      slugLock,
	}
	if create {
		lockPut.ConditionExpression = aws.String("attribute_not_exists(#pk)")
		lockPut.ExpressionAttributeNames = map[string]string{"#pk": "pk"}
	} else {
		lockPut.ConditionExpression = aws.String("attribute_not_exists(#pk) OR #product_id = :product_id")
		lockPut.ExpressionAttributeNames = map[string]string{
			"#pk":         "pk",
			"#product_id": "product_id",
		}
		lockPut.ExpressionAttributeValues = map[string]types.AttributeValue{
			":product_id": &types.AttributeValueMemberS{Value: product.ID},
		}
	}
	items = append(items, types.TransactWriteItem{Put: lockPut})

	if !create {
		for _, deleteItem := range staleCategoryProductDeletes(s.tableName, previous, product) {
			items = append(items, deleteItem)
		}
	}
	for _, categorySlug := range product.CategorySlugs {
		membership, err := categoryProductItem(categorySlug, product)
		if err != nil {
			return nil, err
		}
		items = append(items, types.TransactWriteItem{Put: &types.Put{
			TableName: aws.String(s.tableName),
			Item:      membership,
		}})
	}

	return items, nil
}

func (s *DynamoStore) transactWrite(ctx context.Context, items []types.TransactWriteItem, method string) error {
	if s.writeClient == nil {
		return fmt.Errorf("catalog admin store is missing a transaction client")
	}
	started := time.Now()
	_, err := s.writeClient.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: items,
	})
	s.recordCatalogOperation("TransactWriteItems", method, time.Since(started))
	if err != nil {
		return classifyTransactionError(err)
	}

	return nil
}

func (s *DynamoStore) recordCatalogOperation(operation string, method string, duration time.Duration) {
	if s.metrics == nil {
		return
	}
	dimensions := []observability.Dimension{
		observability.Dim("Service", "catalog"),
		observability.Dim("Operation", operation),
		observability.Dim("Method", method),
	}
	s.metrics.Record(observability.Duration(observability.MetricCatalogOperationMs, duration, dimensions...))
	s.metrics.Record(observability.Count(observability.MetricCatalogOperation, dimensions...))
}

func classifyTransactionError(err error) error {
	var canceled *types.TransactionCanceledException
	if !errors.As(err, &canceled) {
		return err
	}
	for index, reason := range canceled.CancellationReasons {
		if aws.ToString(reason.Code) != "ConditionalCheckFailed" {
			continue
		}
		if index == 1 {
			return fmt.Errorf("%w", ErrSlugConflict)
		}
		return fmt.Errorf("%w", ErrVersionConflict)
	}

	return err
}

func staleCategoryProductDeletes(tableName string, previous Product, product Product) []types.TransactWriteItem {
	currentKeys := map[string]bool{}
	for _, categorySlug := range product.CategorySlugs {
		currentKeys[categoryPK(categorySlug)+"\x00"+categoryProductSK(product)] = true
	}

	var deletes []types.TransactWriteItem
	for _, categorySlug := range previous.CategorySlugs {
		keyID := categoryPK(categorySlug) + "\x00" + categoryProductSK(previous)
		if currentKeys[keyID] {
			continue
		}
		deletes = append(deletes, types.TransactWriteItem{Delete: &types.Delete{
			TableName: aws.String(tableName),
			Key: map[string]types.AttributeValue{
				"pk": &types.AttributeValueMemberS{Value: categoryPK(categorySlug)},
				"sk": &types.AttributeValueMemberS{Value: categoryProductSK(previous)},
			},
		}})
	}

	return deletes
}

type catalogItem struct {
	PK            string           `dynamodbav:"pk"`
	SK            string           `dynamodbav:"sk"`
	EntityType    string           `dynamodbav:"entity_type"`
	ID            string           `dynamodbav:"id,omitempty"`
	Slug          string           `dynamodbav:"slug,omitempty"`
	Name          string           `dynamodbav:"name,omitempty"`
	Description   string           `dynamodbav:"description,omitempty"`
	PriceCents    *int             `dynamodbav:"price_cents,omitempty"`
	ImageURL      string           `dynamodbav:"image_url,omitempty"`
	Status        Status           `dynamodbav:"status,omitempty"`
	SortOrder     int              `dynamodbav:"sort_order"`
	StockQuantity *int             `dynamodbav:"stock_quantity,omitempty"`
	Version       *int             `dynamodbav:"version,omitempty"`
	CreatedAt     string           `dynamodbav:"created_at,omitempty"`
	UpdatedAt     string           `dynamodbav:"updated_at,omitempty"`
	CategorySlugs []string         `dynamodbav:"category_slugs,omitempty"`
	Variants      []ProductVariant `dynamodbav:"variants,omitempty"`
	ProductID     string           `dynamodbav:"product_id,omitempty"`
	GSI1PK        string           `dynamodbav:"gsi1pk,omitempty"`
	GSI1SK        string           `dynamodbav:"gsi1sk,omitempty"`
	GSI2PK        string           `dynamodbav:"gsi2pk,omitempty"`
	GSI2SK        string           `dynamodbav:"gsi2sk,omitempty"`
	GSI3PK        string           `dynamodbav:"gsi3pk,omitempty"`
	GSI3SK        string           `dynamodbav:"gsi3sk,omitempty"`
	GSI4PK        string           `dynamodbav:"gsi4pk,omitempty"`
	GSI4SK        string           `dynamodbav:"gsi4sk,omitempty"`
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
		Version:       intPtr(product.Version),
		CreatedAt:     formatCatalogTime(product.CreatedAt),
		UpdatedAt:     formatUpdatedAt(product.UpdatedAt),
		CategorySlugs: product.CategorySlugs,
		Variants:      product.Variants,
		GSI1PK:        productSlugIndexPK(product.Slug),
		GSI1SK:        productSK,
		GSI4PK:        adminProductsIndexPK,
		GSI4SK:        productAdminIndexSK(product),
	}
	if product.Status == StatusActive {
		item.GSI2PK = activeProductsIndexPK
		item.GSI2SK = productPublicIndexSK(product)
		item.GSI3PK = recentProductsIndexPK
		item.GSI3SK = productRecentIndexSK(product)
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
		Version:     intPtr(category.Version),
		UpdatedAt:   formatUpdatedAt(category.UpdatedAt),
		GSI4PK:      adminCategoriesIndexPK,
		GSI4SK:      categoryAdminIndexSK(category),
	}
	if category.Status == StatusActive {
		item.GSI2PK = activeCategoriesIndexPK
		item.GSI2SK = categoryPublicIndexSK(category)
	}

	return attributevalue.MarshalMap(item)
}

func productSlugLockItem(product Product) (map[string]types.AttributeValue, error) {
	return attributevalue.MarshalMap(catalogItem{
		PK:         productSlugIndexPK(product.Slug),
		SK:         slugLockSK,
		EntityType: entityProductSlugLock,
		Slug:       product.Slug,
		ProductID:  product.ID,
		UpdatedAt:  formatUpdatedAt(product.UpdatedAt),
	})
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
		Version:       intPtr(product.Version),
		CreatedAt:     formatCatalogTime(product.CreatedAt),
		UpdatedAt:     formatUpdatedAt(product.UpdatedAt),
		Variants:      product.Variants,
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
	createdAt, err := parseCatalogTime("created_at", record.CreatedAt)
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
		Version:       intValue(record.Version),
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
		CategorySlugs: record.CategorySlugs,
		Variants:      record.Variants,
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
		Version:     intValue(record.Version),
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

func productRecentIndexSK(product Product) string {
	return fmt.Sprintf("PRODUCT#%s#%s", formatCatalogTime(productRecentTime(product)), product.ID)
}

func productAdminIndexSK(product Product) string {
	return "PRODUCT#" + product.ID
}

func productRecentTime(product Product) time.Time {
	if !product.CreatedAt.IsZero() {
		return product.CreatedAt
	}
	if !product.UpdatedAt.IsZero() {
		return product.UpdatedAt
	}

	return time.Unix(0, 0).UTC()
}

func categoryPublicIndexSK(category Category) string {
	return fmt.Sprintf("CATEGORY#%010d#%s", category.SortOrder, category.Slug)
}

func categoryAdminIndexSK(category Category) string {
	return "CATEGORY#" + category.Slug
}

func categoryProductSK(product Product) string {
	return fmt.Sprintf("PRODUCT#%010d#%s", product.SortOrder, product.ID)
}

func formatUpdatedAt(updatedAt time.Time) string {
	return formatCatalogTime(updatedAt)
}

func formatCatalogTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}

	return value.UTC().Format(time.RFC3339)
}

func parseUpdatedAt(updatedAt string) (time.Time, error) {
	return parseCatalogTime("updated_at", updatedAt)
}

func parseCatalogTime(attributeName string, value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}

	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse catalog %s %q: %w", attributeName, value, err)
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
