package commerce

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/appenv"
	"github.com/anhydrous99/thailandgiftshop/internal/cart"
	"github.com/anhydrous99/thailandgiftshop/internal/observability"
	"github.com/anhydrous99/thailandgiftshop/internal/signedtoken"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const (
	entityCustomer      = "CUSTOMER"
	entityEmailLock     = "CUSTOMER_EMAIL_LOCK"
	entitySession       = "SESSION"
	entityPasswordReset = "PASSWORD_RESET"
	entityCart          = "CART"
	entityAddress       = "ADDRESS"
	entityOrder         = "ORDER"
	entityEmailEvent    = "EMAIL_EVENT"
	entityStripeEvent   = "STRIPE_EVENT"
	entityThrottle      = "THROTTLE"

	customerSK         = "CUSTOMER"
	emailLockSK        = "EMAIL_LOCK"
	cartSK             = "CART"
	orderSK            = "ORDER"
	emailEventSKPrefix = "EMAIL_EVENT#"
	stripeEventSK      = "EVENT"
	throttleSK         = "THROTTLE"
	passwordResetSK    = "PASSWORD_RESET"
	sessionSKPrefix    = "SESSION#"
	addressSKPrefix    = "ADDRESS#"

	ordersIndexPK = "ORDERS"
)

const stripeEventTTL = 30 * 24 * time.Hour
const batchDeleteAttempts = 5

func customerPK(customerID string) string {
	return "CUSTOMER#" + customerID
}

func emailLockPK(emailNormalized string) string {
	return "CUSTOMER_EMAIL#" + emailNormalized
}

func sessionSK(tokenHash string) string {
	return sessionSKPrefix + tokenHash
}

func addressSK(addressID string) string {
	return addressSKPrefix + addressID
}

func orderPK(orderID string) string {
	return "ORDER#" + orderID
}

func emailEventSK(key string) string {
	return emailEventSKPrefix + base64.RawURLEncoding.EncodeToString([]byte(key))
}

func stripeEventPK(eventID string) string {
	return "STRIPE_EVENT#" + eventID
}

func throttlePK(key string) string {
	return "THROTTLE#" + key
}

func customerOrdersIndexPK(customerID string) string {
	return customerPK(customerID)
}

func customerOrdersIndexSK(order Order) string {
	return "ORDER#" + formatCommerceTime(order.CreatedAt) + "#" + order.ID
}

func ordersIndexSK(order Order) string {
	return formatCommerceTime(order.CreatedAt) + "#" + order.ID
}

// customerOrdersStartKey rebuilds the gsi1 ExclusiveStartKey position for a
// cursor. ExclusiveStartKey is a position, not a row reference: the item does
// not need to exist. gsi1pk is always built from the queried customerID, so
// the key can never address another customer's partition.
func customerOrdersStartKey(customerID string, cursor OrderCursor) map[string]types.AttributeValue {
	marker := Order{ID: cursor.OrderID, CreatedAt: cursor.CreatedAt}
	return map[string]types.AttributeValue{
		"pk":     &types.AttributeValueMemberS{Value: orderPK(cursor.OrderID)},
		"sk":     &types.AttributeValueMemberS{Value: orderSK},
		"gsi1pk": &types.AttributeValueMemberS{Value: customerOrdersIndexPK(customerID)},
		"gsi1sk": &types.AttributeValueMemberS{Value: customerOrdersIndexSK(marker)},
	}
}

// ordersStartKey rebuilds the gsi2 ExclusiveStartKey position for a cursor.
func ordersStartKey(cursor OrderCursor) map[string]types.AttributeValue {
	marker := Order{ID: cursor.OrderID, CreatedAt: cursor.CreatedAt}
	return map[string]types.AttributeValue{
		"pk":     &types.AttributeValueMemberS{Value: orderPK(cursor.OrderID)},
		"sk":     &types.AttributeValueMemberS{Value: orderSK},
		"gsi2pk": &types.AttributeValueMemberS{Value: ordersIndexPK},
		"gsi2sk": &types.AttributeValueMemberS{Value: ordersIndexSK(marker)},
	}
}

type DynamoConfig struct {
	TableName               string
	CustomerOrdersIndexName string
	OrdersIndexName         string
}

func DynamoConfigFromEnv() (DynamoConfig, bool) {
	tableName := os.Getenv(EnvTableName)
	if tableName == "" {
		return DynamoConfig{}, false
	}

	config := DynamoConfig{
		TableName:               tableName,
		CustomerOrdersIndexName: os.Getenv(EnvCustomerOrdersIndexName),
		OrdersIndexName:         os.Getenv(EnvOrdersIndexName),
	}
	if config.CustomerOrdersIndexName == "" {
		config.CustomerOrdersIndexName = DefaultCustomerOrdersIndexName
	}
	if config.OrdersIndexName == "" {
		config.OrdersIndexName = DefaultOrdersIndexName
	}

	return config, true
}

func NewStoreFromEnv(ctx context.Context) (Store, bool, error) {
	return NewStoreFromEnvWithRecorder(ctx, nil)
}

// NewStoreFromEnvWithRecorder wires the production DynamoDB store from env
// vars. When COMMERCE_TABLE_NAME is unset it fails closed in production and
// falls back to an in-memory store everywhere else.
func NewStoreFromEnvWithRecorder(ctx context.Context, metrics observability.Recorder) (Store, bool, error) {
	dynamoConfig, ok := DynamoConfigFromEnv()
	if !ok {
		if appenv.IsProduction() {
			return nil, false, ErrCommerceStoreNotConfigured
		}
		return NewMemoryStore(), false, nil
	}

	awsConfig, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("load AWS config for commerce store: %w", err)
	}

	return NewDynamoStoreWithRecorder(dynamodb.NewFromConfig(awsConfig), dynamoConfig, metrics), true, nil
}

type getItemClient interface {
	GetItem(ctx context.Context, params *dynamodb.GetItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
}

type putItemClient interface {
	PutItem(ctx context.Context, params *dynamodb.PutItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
}

type updateItemClient interface {
	UpdateItem(ctx context.Context, params *dynamodb.UpdateItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
}

type deleteItemClient interface {
	DeleteItem(ctx context.Context, params *dynamodb.DeleteItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
}

type queryClient interface {
	Query(ctx context.Context, params *dynamodb.QueryInput, optFns ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
}

type transactWriteClient interface {
	TransactWriteItems(ctx context.Context, params *dynamodb.TransactWriteItemsInput, optFns ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error)
}

type batchWriteClient interface {
	BatchWriteItem(ctx context.Context, params *dynamodb.BatchWriteItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.BatchWriteItemOutput, error)
}

type dynamoClient interface {
	getItemClient
	putItemClient
	updateItemClient
	deleteItemClient
	queryClient
	transactWriteClient
	batchWriteClient
}

var _ Store = (*DynamoStore)(nil)

type DynamoStore struct {
	get                     getItemClient
	put                     putItemClient
	update                  updateItemClient
	delete                  deleteItemClient
	query                   queryClient
	transact                transactWriteClient
	batch                   batchWriteClient
	tableName               string
	customerOrdersIndexName string
	ordersIndexName         string
	metrics                 observability.Recorder
	now                     func() time.Time
	newID                   func() string
}

func NewDynamoStore(client dynamoClient, config DynamoConfig) *DynamoStore {
	return NewDynamoStoreWithRecorder(client, config, nil)
}

func NewDynamoStoreWithRecorder(client dynamoClient, config DynamoConfig, metrics observability.Recorder) *DynamoStore {
	return NewDynamoStoreWithClock(client, config, metrics, time.Now)
}

func NewDynamoStoreWithClock(client dynamoClient, config DynamoConfig, metrics observability.Recorder, now func() time.Time) *DynamoStore {
	return &DynamoStore{
		get:                     client,
		put:                     client,
		update:                  client,
		delete:                  client,
		query:                   client,
		transact:                client,
		batch:                   client,
		tableName:               config.TableName,
		customerOrdersIndexName: config.CustomerOrdersIndexName,
		ordersIndexName:         config.OrdersIndexName,
		metrics:                 metrics,
		now:                     now,
		newID:                   signedtoken.NewID,
	}
}

func (s *DynamoStore) clock() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

func (s *DynamoStore) generateID() string {
	if s.newID == nil {
		return signedtoken.NewID()
	}
	return s.newID()
}

func (s *DynamoStore) CreateCustomer(ctx context.Context, email string, emailNormalized string, passwordHash string) (Customer, error) {
	now := s.clock()
	customer := Customer{
		ID:              s.generateID(),
		Email:           email,
		EmailNormalized: emailNormalized,
		PasswordHash:    passwordHash,
		Version:         1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	item, err := customerItem(customer)
	if err != nil {
		return Customer{}, err
	}
	lockItem, err := emailLockItem(customer)
	if err != nil {
		return Customer{}, err
	}

	items := []types.TransactWriteItem{
		{Put: &types.Put{
			TableName:           aws.String(s.tableName),
			Item:                item,
			ConditionExpression: aws.String("attribute_not_exists(pk)"),
		}},
		{Put: &types.Put{
			TableName:           aws.String(s.tableName),
			Item:                lockItem,
			ConditionExpression: aws.String("attribute_not_exists(pk)"),
		}},
	}
	// Per-item cancellation classification: each transact item carries its own
	// conflict meaning instead of guessing from a hard-coded position.
	itemConflicts := []error{
		fmt.Errorf("commerce customer id %q already exists", customer.ID),
		ErrEmailTaken,
	}
	if err := s.transactWrite(ctx, items, itemConflicts, "CreateCustomer"); err != nil {
		return Customer{}, err
	}

	return customer, nil
}

func (s *DynamoStore) GetCustomerByEmail(ctx context.Context, emailNormalized string) (Customer, bool, error) {
	item, err := s.getItem(ctx, emailLockPK(emailNormalized), emailLockSK, "GetCustomerByEmail")
	if err != nil {
		return Customer{}, false, err
	}
	if len(item) == 0 {
		return Customer{}, false, nil
	}
	lock, err := emailLockFromItem(item)
	if err != nil {
		return Customer{}, false, err
	}

	return s.GetCustomerByID(ctx, lock.CustomerID)
}

func (s *DynamoStore) GetCustomerByID(ctx context.Context, id string) (Customer, bool, error) {
	item, err := s.getItem(ctx, customerPK(id), customerSK, "GetCustomerByID")
	if err != nil {
		return Customer{}, false, err
	}
	if len(item) == 0 {
		return Customer{}, false, nil
	}
	customer, err := customerFromItem(item)
	if err != nil {
		return Customer{}, false, err
	}
	return customer, true, nil
}

func (s *DynamoStore) SetStripeCustomerID(ctx context.Context, customerID string, stripeID string) (Customer, error) {
	output, err := s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(s.tableName),
		Key:                 itemKey(customerPK(customerID), customerSK),
		ConditionExpression: aws.String("attribute_exists(pk) AND attribute_not_exists(stripe_customer_id)"),
		UpdateExpression:    aws.String("SET stripe_customer_id = :stripe_customer_id, updated_at = :updated_at"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":stripe_customer_id": &types.AttributeValueMemberS{Value: stripeID},
			":updated_at":         &types.AttributeValueMemberS{Value: formatCommerceTime(s.clock())},
		},
		ReturnValues: types.ReturnValueAllNew,
	}, "SetStripeCustomerID")
	if err != nil {
		if isConditionalCheckFailed(err) {
			// Lost the race (or the id was already set): re-read the winner.
			winner, found, getErr := s.GetCustomerByID(ctx, customerID)
			if getErr != nil {
				return Customer{}, getErr
			}
			if !found {
				return Customer{}, fmt.Errorf("commerce customer %q not found", customerID)
			}
			return winner, nil
		}
		return Customer{}, err
	}

	return customerFromItem(output.Attributes)
}

func (s *DynamoStore) SetDefaultAddress(ctx context.Context, customerID string, addressID string, expectedVersion int) error {
	updateExpression := "SET default_address_id = :default_address_id, #version = :new_version, updated_at = :updated_at"
	values := map[string]types.AttributeValue{
		":default_address_id": &types.AttributeValueMemberS{Value: addressID},
		":expected_version":   numberValue(int64(expectedVersion)),
		":new_version":        numberValue(int64(expectedVersion + 1)),
		":updated_at":         &types.AttributeValueMemberS{Value: formatCommerceTime(s.clock())},
	}
	if addressID == "" {
		updateExpression = "SET #version = :new_version, updated_at = :updated_at REMOVE default_address_id"
		delete(values, ":default_address_id")
	}
	_, err := s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(s.tableName),
		Key:                       itemKey(customerPK(customerID), customerSK),
		ConditionExpression:       aws.String("attribute_exists(pk) AND #version = :expected_version"),
		UpdateExpression:          aws.String(updateExpression),
		ExpressionAttributeNames:  map[string]string{"#version": "version"},
		ExpressionAttributeValues: values,
		ReturnValues:              types.ReturnValueNone,
	}, "SetDefaultAddress")
	if isConditionalCheckFailed(err) {
		return fmt.Errorf("%w", ErrVersionConflict)
	}
	return err
}

func (s *DynamoStore) UpdatePassword(ctx context.Context, customerID string, newHash string, expectedVersion int) error {
	_, err := s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                aws.String(s.tableName),
		Key:                      itemKey(customerPK(customerID), customerSK),
		ConditionExpression:      aws.String("attribute_exists(pk) AND #version = :expected_version"),
		UpdateExpression:         aws.String("SET password_hash = :password_hash, #version = :new_version, updated_at = :updated_at"),
		ExpressionAttributeNames: map[string]string{"#version": "version"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":password_hash":    &types.AttributeValueMemberS{Value: newHash},
			":expected_version": numberValue(int64(expectedVersion)),
			":new_version":      numberValue(int64(expectedVersion + 1)),
			":updated_at":       &types.AttributeValueMemberS{Value: formatCommerceTime(s.clock())},
		},
		ReturnValues: types.ReturnValueNone,
	}, "UpdatePassword")
	if isConditionalCheckFailed(err) {
		return fmt.Errorf("%w", ErrVersionConflict)
	}
	return err
}

func (s *DynamoStore) PutSession(ctx context.Context, session Session) error {
	item, err := sessionItem(session)
	if err != nil {
		return err
	}
	return s.putItem(ctx, item, nil, nil, "PutSession")
}

func (s *DynamoStore) GetSession(ctx context.Context, customerID string, tokenHash string) (Session, bool, error) {
	item, err := s.getItem(ctx, customerPK(customerID), sessionSK(tokenHash), "GetSession")
	if err != nil {
		return Session{}, false, err
	}
	if len(item) == 0 {
		return Session{}, false, nil
	}
	// DynamoDB TTL expiry is lazy; callers must re-check ExpiresAt.
	session, err := sessionFromItem(item)
	if err != nil {
		return Session{}, false, err
	}
	return session, true, nil
}

func (s *DynamoStore) DeleteSession(ctx context.Context, customerID string, tokenHash string) error {
	return s.deleteItem(ctx, customerPK(customerID), sessionSK(tokenHash), "DeleteSession")
}

func (s *DynamoStore) DeleteAllSessions(ctx context.Context, customerID string) error {
	if s.batch == nil {
		return errors.New("commerce store is missing a batch write client")
	}
	items, err := s.queryItems(ctx, dynamodb.QueryInput{
		TableName:              aws.String(s.tableName),
		ConsistentRead:         aws.Bool(true),
		KeyConditionExpression: aws.String("#pk = :pk AND begins_with(#sk, :sk_prefix)"),
		ExpressionAttributeNames: map[string]string{
			"#pk": "pk",
			"#sk": "sk",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":        &types.AttributeValueMemberS{Value: customerPK(customerID)},
			":sk_prefix": &types.AttributeValueMemberS{Value: sessionSKPrefix},
		},
	}, 0, "DeleteAllSessions")
	if err != nil {
		return err
	}

	requests := make([]types.WriteRequest, 0, len(items))
	for _, item := range items {
		requests = append(requests, types.WriteRequest{DeleteRequest: &types.DeleteRequest{
			Key: map[string]types.AttributeValue{
				"pk": item["pk"],
				"sk": item["sk"],
			},
		}})
	}

	for start := 0; start < len(requests); start += 25 {
		end := min(start+25, len(requests))
		pending := requests[start:end]
		for attempt := 0; attempt < batchDeleteAttempts; attempt++ {
			started := time.Now()
			output, err := s.batch.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{
				RequestItems: map[string][]types.WriteRequest{s.tableName: pending},
			})
			s.recordCommerceOperation("BatchWriteItem", "DeleteAllSessions", time.Since(started))
			if err != nil {
				return err
			}
			pending = output.UnprocessedItems[s.tableName]
			if len(pending) == 0 {
				break
			}
		}
		if len(pending) > 0 {
			return fmt.Errorf("commerce session delete left %d unprocessed items", len(pending))
		}
	}

	return nil
}

func (s *DynamoStore) PutPasswordResetToken(ctx context.Context, token PasswordResetToken) error {
	item, err := passwordResetTokenItem(token)
	if err != nil {
		return err
	}
	return s.putItem(ctx, item, nil, nil, "PutPasswordResetToken")
}

func (s *DynamoStore) ValidatePasswordResetToken(ctx context.Context, customerID string, tokenHash string, now time.Time) (PasswordResetToken, bool, error) {
	token, found, err := s.getPasswordResetToken(ctx, customerID, "ValidatePasswordResetToken")
	if err != nil || !found {
		return PasswordResetToken{}, found, err
	}
	if !passwordResetTokenUsable(token, tokenHash, now) {
		return PasswordResetToken{}, false, nil
	}
	return token, true, nil
}

func (s *DynamoStore) ConsumePasswordResetToken(ctx context.Context, customerID string, tokenHash string, now time.Time) (PasswordResetToken, bool, error) {
	if s.update == nil {
		return PasswordResetToken{}, false, errors.New("commerce store is missing an update client")
	}
	started := time.Now()
	output, err := s.update.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(s.tableName),
		Key:                 itemKey(customerPK(customerID), passwordResetSK),
		ConditionExpression: aws.String("attribute_exists(pk) AND token_hash = :token_hash AND expires_at > :now AND attribute_not_exists(used_at)"),
		UpdateExpression:    aws.String("SET used_at = :used_at, #version = :new_version"),
		ExpressionAttributeNames: map[string]string{
			"#version": "version",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":token_hash":  &types.AttributeValueMemberS{Value: tokenHash},
			":now":         numberValue(now.UTC().Unix()),
			":used_at":     &types.AttributeValueMemberS{Value: formatCommerceTime(now)},
			":new_version": numberValue(2),
		},
		ReturnValues: types.ReturnValueAllNew,
	})
	s.recordCommerceOperation("UpdateItem", "ConsumePasswordResetToken", time.Since(started))
	if isConditionalCheckFailed(err) {
		return PasswordResetToken{}, false, nil
	}
	if err != nil {
		return PasswordResetToken{}, false, err
	}
	token, err := passwordResetTokenFromItem(output.Attributes)
	if err != nil {
		return PasswordResetToken{}, false, err
	}
	return token, true, nil
}

func (s *DynamoStore) DeletePasswordResetToken(ctx context.Context, customerID string) error {
	return s.deleteItem(ctx, customerPK(customerID), passwordResetSK, "DeletePasswordResetToken")
}

func (s *DynamoStore) getPasswordResetToken(ctx context.Context, customerID string, operation string) (PasswordResetToken, bool, error) {
	item, err := s.getItem(ctx, customerPK(customerID), passwordResetSK, operation)
	if err != nil {
		return PasswordResetToken{}, false, err
	}
	if len(item) == 0 {
		return PasswordResetToken{}, false, nil
	}
	token, err := passwordResetTokenFromItem(item)
	if err != nil {
		return PasswordResetToken{}, false, err
	}
	return token, true, nil
}

func (s *DynamoStore) GetCart(ctx context.Context, customerID string) (CartRecord, bool, error) {
	item, err := s.getItem(ctx, customerPK(customerID), cartSK, "GetCart")
	if err != nil {
		return CartRecord{}, false, err
	}
	if len(item) == 0 {
		return CartRecord{}, false, nil
	}
	record, err := cartFromItem(item)
	if err != nil {
		return CartRecord{}, false, err
	}
	return record, true, nil
}

func (s *DynamoStore) PutCart(ctx context.Context, c CartRecord) (CartRecord, error) {
	record := c
	record.Version = c.Version + 1
	record.UpdatedAt = s.clock()
	item, err := cartItem(record)
	if err != nil {
		return CartRecord{}, err
	}

	err = s.putItem(ctx, item,
		aws.String("attribute_not_exists(pk) OR #version = :expected_version"),
		&putItemExpressions{
			names:  map[string]string{"#version": "version"},
			values: map[string]types.AttributeValue{":expected_version": numberValue(int64(c.Version))},
		},
		"PutCart")
	if isConditionalCheckFailed(err) {
		return CartRecord{}, fmt.Errorf("%w", ErrVersionConflict)
	}
	if err != nil {
		return CartRecord{}, err
	}
	return record, nil
}

func (s *DynamoStore) CreateAddress(ctx context.Context, a Address) (Address, error) {
	existing, err := s.ListAddresses(ctx, a.CustomerID)
	if err != nil {
		return Address{}, err
	}
	if len(existing) >= MaxAddressesPerCustomer {
		return Address{}, fmt.Errorf("%w", ErrAddressLimit)
	}

	if a.ID == "" {
		a.ID = s.generateID()
	}
	now := s.clock()
	a.Version = 1
	a.CreatedAt = now
	a.UpdatedAt = now
	item, err := addressItem(a)
	if err != nil {
		return Address{}, err
	}
	err = s.putItem(ctx, item, aws.String("attribute_not_exists(pk)"), nil, "CreateAddress")
	if isConditionalCheckFailed(err) {
		return Address{}, fmt.Errorf("%w", ErrVersionConflict)
	}
	if err != nil {
		return Address{}, err
	}
	return a, nil
}

func (s *DynamoStore) UpdateAddress(ctx context.Context, a Address, expectedVersion int) (Address, error) {
	builder := newUpdateExpressionBuilder()
	builder.setString("full_name", ":full_name", a.FullName)
	builder.setString("line1", ":line1", a.Line1)
	builder.setOrRemoveString("line2", ":line2", a.Line2)
	builder.setString("city", ":city", a.City)
	// REGION is a DynamoDB reserved word; it must go through an expression
	// attribute name.
	builder.setNamed("#region", "region", ":region", &types.AttributeValueMemberS{Value: a.Region})
	builder.setString("postal_code", ":postal_code", a.PostalCode)
	builder.setString("country", ":country", a.Country)
	builder.setOrRemoveString("phone", ":phone", a.Phone)
	builder.setNamed("#version", "version", ":new_version", numberValue(int64(expectedVersion+1)))
	builder.setString("updated_at", ":updated_at", formatCommerceTime(s.clock()))
	builder.values[":expected_version"] = numberValue(int64(expectedVersion))

	output, err := s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(s.tableName),
		Key:                       itemKey(customerPK(a.CustomerID), addressSK(a.ID)),
		ConditionExpression:       aws.String("attribute_exists(pk) AND #version = :expected_version"),
		UpdateExpression:          aws.String(builder.expression()),
		ExpressionAttributeNames:  builder.names,
		ExpressionAttributeValues: builder.values,
		ReturnValues:              types.ReturnValueAllNew,
	}, "UpdateAddress")
	if isConditionalCheckFailed(err) {
		return Address{}, fmt.Errorf("%w", ErrVersionConflict)
	}
	if err != nil {
		return Address{}, err
	}
	return addressFromItem(output.Attributes)
}

func (s *DynamoStore) DeleteAddress(ctx context.Context, customerID string, addressID string) error {
	return s.deleteItem(ctx, customerPK(customerID), addressSK(addressID), "DeleteAddress")
}

func (s *DynamoStore) GetAddress(ctx context.Context, customerID string, addressID string) (Address, bool, error) {
	item, err := s.getItem(ctx, customerPK(customerID), addressSK(addressID), "GetAddress")
	if err != nil {
		return Address{}, false, err
	}
	if len(item) == 0 {
		return Address{}, false, nil
	}
	address, err := addressFromItem(item)
	if err != nil {
		return Address{}, false, err
	}
	return address, true, nil
}

func (s *DynamoStore) ListAddresses(ctx context.Context, customerID string) ([]Address, error) {
	items, err := s.queryItems(ctx, dynamodb.QueryInput{
		TableName:              aws.String(s.tableName),
		KeyConditionExpression: aws.String("#pk = :pk AND begins_with(#sk, :sk_prefix)"),
		ExpressionAttributeNames: map[string]string{
			"#pk": "pk",
			"#sk": "sk",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":        &types.AttributeValueMemberS{Value: customerPK(customerID)},
			":sk_prefix": &types.AttributeValueMemberS{Value: addressSKPrefix},
		},
		ScanIndexForward: aws.Bool(true),
	}, 0, "ListAddresses")
	if err != nil {
		return nil, err
	}

	addresses := make([]Address, 0, len(items))
	for _, item := range items {
		address, err := addressFromItem(item)
		if err != nil {
			return nil, err
		}
		addresses = append(addresses, address)
	}
	return addresses, nil
}

func (s *DynamoStore) CreateOrder(ctx context.Context, o Order) (Order, error) {
	if o.ID == "" {
		o.ID = s.generateID()
	}
	now := s.clock()
	if o.CreatedAt.IsZero() {
		o.CreatedAt = now
	}
	if o.UpdatedAt.IsZero() {
		o.UpdatedAt = o.CreatedAt
	}
	o.Version = 1
	if len(o.StatusHistory) == 0 {
		o.StatusHistory = []StatusEvent{{Status: o.Status, At: o.CreatedAt, Actor: OrderActorCustomer}}
	}
	item, err := orderItem(o)
	if err != nil {
		return Order{}, err
	}
	err = s.putItem(ctx, item, aws.String("attribute_not_exists(pk)"), nil, "CreateOrder")
	if isConditionalCheckFailed(err) {
		return Order{}, fmt.Errorf("%w", ErrVersionConflict)
	}
	if err != nil {
		return Order{}, err
	}
	return o, nil
}

func (s *DynamoStore) GetOrder(ctx context.Context, orderID string) (Order, bool, error) {
	item, err := s.getItem(ctx, orderPK(orderID), orderSK, "GetOrder")
	if err != nil {
		return Order{}, false, err
	}
	if len(item) == 0 {
		return Order{}, false, nil
	}
	order, err := orderFromItem(item)
	if err != nil {
		return Order{}, false, err
	}
	return order, true, nil
}

func (s *DynamoStore) ListOrdersByCustomer(ctx context.Context, customerID string, limit int, cursor OrderCursor) (OrderPage, error) {
	if customerID == "" {
		// Guest marker: guest orders are sparse in gsi1 and must never be
		// listable as a pseudo-customer. The guard also keeps
		// customerOrdersStartKey from ever building a key for an empty ID.
		return OrderPage{}, nil
	}
	input := dynamodb.QueryInput{
		TableName:              aws.String(s.tableName),
		IndexName:              aws.String(s.customerOrdersIndexName),
		KeyConditionExpression: aws.String("#gsi1pk = :pk"),
		ExpressionAttributeNames: map[string]string{
			"#gsi1pk": "gsi1pk",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: customerOrdersIndexPK(customerID)},
		},
		ScanIndexForward: aws.Bool(false),
	}
	if !cursor.IsZero() {
		input.ExclusiveStartKey = customerOrdersStartKey(customerID, cursor)
	}
	items, err := s.queryItems(ctx, input, probeLimit(limit), "ListOrdersByCustomer")
	if err != nil {
		return OrderPage{}, err
	}
	orders, err := ordersFromItems(items)
	if err != nil {
		return OrderPage{}, err
	}
	return orderPageFromProbe(orders, limit), nil
}

func (s *DynamoStore) ListOrders(ctx context.Context, limit int, cursor OrderCursor) (OrderPage, error) {
	input := dynamodb.QueryInput{
		TableName:              aws.String(s.tableName),
		IndexName:              aws.String(s.ordersIndexName),
		KeyConditionExpression: aws.String("#gsi2pk = :pk"),
		ExpressionAttributeNames: map[string]string{
			"#gsi2pk": "gsi2pk",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: ordersIndexPK},
		},
		ScanIndexForward: aws.Bool(false),
	}
	if !cursor.IsZero() {
		input.ExclusiveStartKey = ordersStartKey(cursor)
	}
	items, err := s.queryItems(ctx, input, probeLimit(limit), "ListOrders")
	if err != nil {
		return OrderPage{}, err
	}
	orders, err := ordersFromItems(items)
	if err != nil {
		return OrderPage{}, err
	}
	return orderPageFromProbe(orders, limit), nil
}

func (s *DynamoStore) TransitionOrder(ctx context.Context, orderID string, from OrderStatus, to OrderStatus, patch OrderPatch) (Order, error) {
	order, found, err := s.GetOrder(ctx, orderID)
	if err != nil {
		return Order{}, err
	}
	if !found {
		return Order{}, fmt.Errorf("commerce order %q not found", orderID)
	}
	if order.Status == to {
		// Replayed transition (webhook retry, duplicate delivery): no-op success.
		return order, nil
	}
	if !AllowedOrderTransition(from, to) {
		return Order{}, fmt.Errorf("%w: %s to %s is not allowed", ErrOrderTransitionConflict, from, to)
	}
	if order.Status != from {
		return Order{}, fmt.Errorf("%w: order is %s, not %s", ErrOrderTransitionConflict, order.Status, from)
	}

	now := s.clock()
	event := statusEventItemFrom(StatusEvent{Status: to, At: now, Actor: orderActorOrDefault(patch.Actor)})
	eventValue, err := attributevalue.Marshal([]statusEventItem{event})
	if err != nil {
		return Order{}, err
	}

	builder := newUpdateExpressionBuilder()
	builder.setNamed("#status", "status", ":to_status", &types.AttributeValueMemberS{Value: string(to)})
	builder.setNamed("#version", "version", ":new_version", numberValue(int64(order.Version+1)))
	builder.setString("updated_at", ":updated_at", formatCommerceTime(now))
	builder.setFragment("status_history = list_append(if_not_exists(status_history, :empty_history), :history_event)")
	builder.values[":empty_history"] = &types.AttributeValueMemberL{Value: []types.AttributeValue{}}
	builder.values[":history_event"] = eventValue
	applyOrderPatchToBuilder(builder, patch)
	builder.values[":from_status"] = &types.AttributeValueMemberS{Value: string(from)}
	builder.values[":expected_version"] = numberValue(int64(order.Version))

	output, err := s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(s.tableName),
		Key:                       itemKey(orderPK(orderID), orderSK),
		ConditionExpression:       aws.String("#status = :from_status AND #version = :expected_version"),
		UpdateExpression:          aws.String(builder.expression()),
		ExpressionAttributeNames:  builder.names,
		ExpressionAttributeValues: builder.values,
		ReturnValues:              types.ReturnValueAllNew,
	}, "TransitionOrder")
	if isConditionalCheckFailed(err) {
		return Order{}, fmt.Errorf("%w: order changed concurrently", ErrOrderTransitionConflict)
	}
	if err != nil {
		return Order{}, err
	}

	s.recordOrderTransition(from, to, orderActorOrDefault(patch.Actor))
	return orderFromItem(output.Attributes)
}

// PatchOrder applies field updates while the status stays put — checkout uses
// it to attach the provider session id to a pending_payment order (design
// §7.1 step 7) without minting a status-history entry.
func (s *DynamoStore) PatchOrder(ctx context.Context, orderID string, expectedStatus OrderStatus, expectedVersion int, patch OrderPatch) (Order, error) {
	builder := newUpdateExpressionBuilder()
	builder.setNamed("#version", "version", ":new_version", numberValue(int64(expectedVersion+1)))
	builder.setString("updated_at", ":updated_at", formatCommerceTime(s.clock()))
	applyOrderPatchToBuilder(builder, patch)
	builder.names["#status"] = "status"
	builder.values[":expected_status"] = &types.AttributeValueMemberS{Value: string(expectedStatus)}
	builder.values[":expected_version"] = numberValue(int64(expectedVersion))

	output, err := s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(s.tableName),
		Key:                       itemKey(orderPK(orderID), orderSK),
		ConditionExpression:       aws.String("#status = :expected_status AND #version = :expected_version"),
		UpdateExpression:          aws.String(builder.expression()),
		ExpressionAttributeNames:  builder.names,
		ExpressionAttributeValues: builder.values,
		ReturnValues:              types.ReturnValueAllNew,
	}, "PatchOrder")
	if isConditionalCheckFailed(err) {
		return Order{}, fmt.Errorf("%w", ErrVersionConflict)
	}
	if err != nil {
		return Order{}, err
	}
	return orderFromItem(output.Attributes)
}

func (s *DynamoStore) MarkStripeEventProcessed(ctx context.Context, eventID string, eventType string, orderID string) (bool, error) {
	now := s.clock()
	item, err := stripeEventItem(eventID, eventType, orderID, now)
	if err != nil {
		return false, err
	}
	err = s.putItem(ctx, item, aws.String("attribute_not_exists(pk)"), nil, "MarkStripeEventProcessed")
	if isConditionalCheckFailed(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

func (s *DynamoStore) ReserveEmailEvent(ctx context.Context, event EmailEvent) (bool, error) {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = s.clock()
	}
	if event.Attempts <= 0 {
		event.Attempts = 1
	}
	item, err := emailEventItem(event)
	if err != nil {
		return false, err
	}
	err = s.putItem(ctx, item, aws.String("attribute_not_exists(pk)"), nil, "ReserveEmailEvent")
	if err != nil {
		if isConditionalCheckFailed(err) {
			return s.reserveExistingEmailEvent(ctx, event)
		}
		return false, err
	}
	return true, nil
}

func (s *DynamoStore) reserveExistingEmailEvent(ctx context.Context, event EmailEvent) (bool, error) {
	item, err := s.getItem(ctx, orderPK(event.OrderID), emailEventSK(event.Key), "ReserveEmailEvent")
	if err != nil {
		return false, err
	}
	if len(item) == 0 {
		return false, nil
	}
	existing, err := emailEventFromItem(item)
	if err != nil {
		return false, err
	}
	if !emailEventCanRetry(existing, event.CreatedAt) {
		return false, nil
	}

	event.Attempts = existing.Attempts + 1
	replacement, err := emailEventItem(event)
	if err != nil {
		return false, err
	}
	condition, expressions := retryEmailEventCondition(existing)
	err = s.putItem(ctx, replacement, aws.String(condition), expressions, "ReserveEmailEvent")
	if isConditionalCheckFailed(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func retryEmailEventCondition(existing EmailEvent) (string, *putItemExpressions) {
	expressions := &putItemExpressions{
		names: map[string]string{
			"#status": "status",
		},
		values: map[string]types.AttributeValue{
			":retry_status": &types.AttributeValueMemberS{Value: existing.Status},
		},
	}
	switch existing.Status {
	case "failed":
		expressions.values[":existing_attempts"] = numberValue(int64(existing.Attempts))
		return "attribute_exists(pk) AND #status = :retry_status AND attempts = :existing_attempts", expressions
	default:
		expressions.values[":existing_created_at"] = &types.AttributeValueMemberS{Value: formatCommerceTime(existing.CreatedAt)}
		return "attribute_exists(pk) AND #status = :retry_status AND created_at = :existing_created_at", expressions
	}
}

func (s *DynamoStore) MarkEmailEventSent(ctx context.Context, orderID string, key string, sentAt time.Time) error {
	_, err := s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(s.tableName),
		Key:                 itemKey(orderPK(orderID), emailEventSK(key)),
		ConditionExpression: aws.String("attribute_exists(pk) AND entity_type = :entity_type"),
		UpdateExpression:    aws.String("SET #status = :status, sent_at = :sent_at"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":entity_type": &types.AttributeValueMemberS{Value: entityEmailEvent},
			":status":      &types.AttributeValueMemberS{Value: "sent"},
			":sent_at":     &types.AttributeValueMemberS{Value: formatCommerceTime(sentAt)},
		},
		ReturnValues: types.ReturnValueNone,
	}, "MarkEmailEventSent")
	return err
}

func (s *DynamoStore) MarkEmailEventFailed(ctx context.Context, orderID string, key string, failedAt time.Time, reason string) error {
	_, err := s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(s.tableName),
		Key:                 itemKey(orderPK(orderID), emailEventSK(key)),
		ConditionExpression: aws.String("attribute_exists(pk) AND entity_type = :entity_type"),
		UpdateExpression:    aws.String("SET #status = :status, failed_at = :failed_at, last_error = :last_error"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":entity_type": &types.AttributeValueMemberS{Value: entityEmailEvent},
			":status":      &types.AttributeValueMemberS{Value: "failed"},
			":failed_at":   &types.AttributeValueMemberS{Value: formatCommerceTime(failedAt)},
			":last_error":  &types.AttributeValueMemberS{Value: reason},
		},
		ReturnValues: types.ReturnValueNone,
	}, "MarkEmailEventFailed")
	return err
}

type putItemExpressions struct {
	names  map[string]string
	values map[string]types.AttributeValue
}

func (s *DynamoStore) putItem(ctx context.Context, item map[string]types.AttributeValue, condition *string, expressions *putItemExpressions, method string) error {
	if s.put == nil {
		return errors.New("commerce store is missing a put client")
	}
	input := &dynamodb.PutItemInput{
		TableName:           aws.String(s.tableName),
		Item:                item,
		ConditionExpression: condition,
	}
	if expressions != nil {
		input.ExpressionAttributeNames = expressions.names
		input.ExpressionAttributeValues = expressions.values
	}
	started := time.Now()
	_, err := s.put.PutItem(ctx, input)
	s.recordCommerceOperation("PutItem", method, time.Since(started))
	return err
}

func (s *DynamoStore) getItem(ctx context.Context, pk string, sk string, method string) (map[string]types.AttributeValue, error) {
	if s.get == nil {
		return nil, errors.New("commerce store is missing a get client")
	}
	started := time.Now()
	output, err := s.get.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.tableName),
		Key:       itemKey(pk, sk),
	})
	s.recordCommerceOperation("GetItem", method, time.Since(started))
	if err != nil {
		return nil, err
	}
	return output.Item, nil
}

func (s *DynamoStore) deleteItem(ctx context.Context, pk string, sk string, method string) error {
	if s.delete == nil {
		return errors.New("commerce store is missing a delete client")
	}
	started := time.Now()
	_, err := s.delete.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.tableName),
		Key:       itemKey(pk, sk),
	})
	s.recordCommerceOperation("DeleteItem", method, time.Since(started))
	return err
}

func (s *DynamoStore) updateItem(ctx context.Context, input *dynamodb.UpdateItemInput, method string) (*dynamodb.UpdateItemOutput, error) {
	if s.update == nil {
		return nil, errors.New("commerce store is missing an update client")
	}
	started := time.Now()
	output, err := s.update.UpdateItem(ctx, input)
	s.recordCommerceOperation("UpdateItem", method, time.Since(started))
	return output, err
}

func (s *DynamoStore) queryItems(ctx context.Context, input dynamodb.QueryInput, limit int, method string) ([]map[string]types.AttributeValue, error) {
	if s.query == nil {
		return nil, errors.New("commerce store is missing a query client")
	}
	if limit > 0 && input.Limit == nil {
		input.Limit = aws.Int32(int32(limit))
	}

	var items []map[string]types.AttributeValue
	for {
		started := time.Now()
		output, err := s.query.Query(ctx, &input)
		s.recordCommerceOperation("Query", method, time.Since(started))
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

func (s *DynamoStore) transactWrite(ctx context.Context, items []types.TransactWriteItem, itemConflicts []error, method string) error {
	if s.transact == nil {
		return errors.New("commerce store is missing a transaction client")
	}
	started := time.Now()
	_, err := s.transact.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: items,
	})
	s.recordCommerceOperation("TransactWriteItems", method, time.Since(started))
	if err != nil {
		return classifyTransactionCancellation(err, itemConflicts)
	}

	return nil
}

// classifyTransactionCancellation maps a canceled transaction onto the error
// registered for the specific item whose condition failed. itemConflicts is
// index-aligned with the submitted TransactWriteItems by construction at each
// call site, so the meaning travels with the item.
//
// Cancellations caused only by transient contention — transaction-vs-
// transaction conflicts, throttling, or an already-in-progress transaction —
// surface as ErrTransientConflict so callers can retry, mirroring
// internal/catalog's isRetryableTransactionConflict. A condition failure
// always wins over the transient classification.
func classifyTransactionCancellation(err error, itemConflicts []error) error {
	var conflict *types.TransactionConflictException
	if errors.As(err, &conflict) {
		return fmt.Errorf("%w: %w", ErrTransientConflict, err)
	}
	var inProgress *types.TransactionInProgressException
	if errors.As(err, &inProgress) {
		return fmt.Errorf("%w: %w", ErrTransientConflict, err)
	}
	var canceled *types.TransactionCanceledException
	if !errors.As(err, &canceled) {
		return err
	}
	retryable := false
	hardFailure := false
	for index, reason := range canceled.CancellationReasons {
		switch aws.ToString(reason.Code) {
		case "ConditionalCheckFailed":
			if index < len(itemConflicts) && itemConflicts[index] != nil {
				return fmt.Errorf("%w", itemConflicts[index])
			}
			hardFailure = true
		case "", "None":
		case "TransactionConflict", "ThrottlingError", "ProvisionedThroughputExceeded":
			retryable = true
		default:
			hardFailure = true
		}
	}
	if retryable && !hardFailure {
		return fmt.Errorf("%w: %w", ErrTransientConflict, err)
	}

	return err
}

func (s *DynamoStore) recordCommerceOperation(operation string, method string, duration time.Duration) {
	if s.metrics == nil {
		return
	}
	dimensions := []observability.Dimension{
		observability.Dim("Service", "commerce"),
		observability.Dim("Operation", operation),
		observability.Dim("Method", method),
	}
	s.metrics.Record(observability.Duration(observability.MetricCommerceOperationMs, duration, dimensions...))
	s.metrics.Record(observability.Count(observability.MetricCommerceOperation, dimensions...))
}

func (s *DynamoStore) recordOrderTransition(from OrderStatus, to OrderStatus, actor string) {
	if s.metrics == nil {
		return
	}
	s.metrics.Record(observability.Count(observability.MetricOrderTransition,
		observability.Dim("Service", "commerce"),
		observability.Dim("From", string(from)),
		observability.Dim("To", string(to)),
		observability.Dim("Actor", actor),
	))
}

func itemKey(pk string, sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: pk},
		"sk": &types.AttributeValueMemberS{Value: sk},
	}
}

type updateExpressionBuilder struct {
	sets    []string
	removes []string
	names   map[string]string
	values  map[string]types.AttributeValue
}

func newUpdateExpressionBuilder() *updateExpressionBuilder {
	return &updateExpressionBuilder{
		names:  map[string]string{},
		values: map[string]types.AttributeValue{},
	}
}

func (b *updateExpressionBuilder) set(attribute string, placeholder string, value types.AttributeValue) {
	b.sets = append(b.sets, attribute+" = "+placeholder)
	b.values[placeholder] = value
}

func (b *updateExpressionBuilder) setNamed(nameToken string, attribute string, placeholder string, value types.AttributeValue) {
	b.names[nameToken] = attribute
	b.set(nameToken, placeholder, value)
}

func (b *updateExpressionBuilder) setString(attribute string, placeholder string, value string) {
	b.set(attribute, placeholder, &types.AttributeValueMemberS{Value: value})
}

func (b *updateExpressionBuilder) setOrRemoveString(attribute string, placeholder string, value string) {
	if value == "" {
		b.remove(attribute)
		return
	}
	b.setString(attribute, placeholder, value)
}

func (b *updateExpressionBuilder) setFragment(fragment string) {
	b.sets = append(b.sets, fragment)
}

func (b *updateExpressionBuilder) remove(attribute string) {
	b.removes = append(b.removes, attribute)
}

func (b *updateExpressionBuilder) expression() string {
	expression := "SET " + strings.Join(b.sets, ", ")
	if len(b.removes) > 0 {
		expression += " REMOVE " + strings.Join(b.removes, ", ")
	}
	return expression
}

func applyOrderPatchToBuilder(builder *updateExpressionBuilder, patch OrderPatch) {
	setOrRemove := func(attribute string, placeholder string, value *string) {
		if value == nil {
			return
		}
		builder.setOrRemoveString(attribute, placeholder, *value)
	}
	setOrRemoveTime := func(attribute string, placeholder string, value *time.Time) {
		if value == nil {
			return
		}
		if value.IsZero() {
			builder.remove(attribute)
			return
		}
		builder.setString(attribute, placeholder, formatCommerceTime(*value))
	}

	setOrRemove("stripe_checkout_session_id", ":stripe_checkout_session_id", patch.StripeCheckoutSessionID)
	setOrRemove("stripe_payment_intent_id", ":stripe_payment_intent_id", patch.StripePaymentIntentID)
	setOrRemove("payment_card_brand", ":payment_card_brand", patch.PaymentCardBrand)
	setOrRemove("payment_card_last4", ":payment_card_last4", patch.PaymentCardLast4)
	setOrRemove("tracking_carrier", ":tracking_carrier", patch.TrackingCarrier)
	setOrRemove("tracking_number", ":tracking_number", patch.TrackingNumber)
	setOrRemove("stripe_refund_id", ":stripe_refund_id", patch.StripeRefundID)
	setOrRemove("refund_failure_reason", ":refund_failure_reason", patch.RefundFailureReason)
	if patch.RefundAttempt != nil {
		builder.set("refund_attempt", ":refund_attempt", numberValue(int64(*patch.RefundAttempt)))
	}
	if patch.CheckoutAttempt != nil {
		builder.set("checkout_attempt", ":checkout_attempt", numberValue(int64(*patch.CheckoutAttempt)))
	}
	setOrRemoveTime("paid_at", ":paid_at", patch.PaidAt)
	setOrRemoveTime("shipped_at", ":shipped_at", patch.ShippedAt)
	setOrRemoveTime("delivered_at", ":delivered_at", patch.DeliveredAt)
	setOrRemoveTime("refunded_at", ":refunded_at", patch.RefundedAt)
	setOrRemoveTime("stock_released_at", ":stock_released_at", patch.StockReleasedAt)
}

// Item shapes — snake_case dynamodbav tags, *int pointers for numerics that
// must persist zero, entity_type validated on unmarshal. Only ORDER rows carry
// GSI attributes.

type customerDynamoItem struct {
	PK               string `dynamodbav:"pk"`
	SK               string `dynamodbav:"sk"`
	EntityType       string `dynamodbav:"entity_type"`
	CustomerID       string `dynamodbav:"customer_id"`
	Email            string `dynamodbav:"email"`
	EmailNormalized  string `dynamodbav:"email_normalized"`
	PasswordHash     string `dynamodbav:"password_hash"`
	EmailVerified    bool   `dynamodbav:"email_verified"`
	StripeCustomerID string `dynamodbav:"stripe_customer_id,omitempty"`
	DefaultAddressID string `dynamodbav:"default_address_id,omitempty"`
	Version          *int   `dynamodbav:"version,omitempty"`
	CreatedAt        string `dynamodbav:"created_at,omitempty"`
	UpdatedAt        string `dynamodbav:"updated_at,omitempty"`
}

type emailLockDynamoItem struct {
	PK              string `dynamodbav:"pk"`
	SK              string `dynamodbav:"sk"`
	EntityType      string `dynamodbav:"entity_type"`
	EmailNormalized string `dynamodbav:"email_normalized"`
	CustomerID      string `dynamodbav:"customer_id"`
	CreatedAt       string `dynamodbav:"created_at,omitempty"`
}

type sessionDynamoItem struct {
	PK         string `dynamodbav:"pk"`
	SK         string `dynamodbav:"sk"`
	EntityType string `dynamodbav:"entity_type"`
	CustomerID string `dynamodbav:"customer_id"`
	Nonce      string `dynamodbav:"nonce"`
	CreatedAt  string `dynamodbav:"created_at,omitempty"`
	ExpiresAt  int64  `dynamodbav:"expires_at"`
}

type passwordResetTokenDynamoItem struct {
	PK         string `dynamodbav:"pk"`
	SK         string `dynamodbav:"sk"`
	EntityType string `dynamodbav:"entity_type"`
	CustomerID string `dynamodbav:"customer_id"`
	TokenHash  string `dynamodbav:"token_hash"`
	ExpiresAt  int64  `dynamodbav:"expires_at"`
	CreatedAt  string `dynamodbav:"created_at"`
	UsedAt     string `dynamodbav:"used_at,omitempty"`
	Version    *int   `dynamodbav:"version,omitempty"`
}

type emailEventDynamoItem struct {
	PK           string `dynamodbav:"pk"`
	SK           string `dynamodbav:"sk"`
	EntityType   string `dynamodbav:"entity_type"`
	EventKey     string `dynamodbav:"event_key"`
	Kind         string `dynamodbav:"kind"`
	OrderID      string `dynamodbav:"order_id"`
	OrderVersion *int   `dynamodbav:"order_version"`
	To           string `dynamodbav:"to"`
	Status       string `dynamodbav:"status"`
	Attempts     *int   `dynamodbav:"attempts"`
	LastError    string `dynamodbav:"last_error,omitempty"`
	CreatedAt    string `dynamodbav:"created_at"`
	SentAt       string `dynamodbav:"sent_at,omitempty"`
	FailedAt     string `dynamodbav:"failed_at,omitempty"`
}

type cartLineDynamoItem struct {
	Slug      string `dynamodbav:"slug"`
	VariantID string `dynamodbav:"variant_id,omitempty"`
	Quantity  *int   `dynamodbav:"quantity"`
}

type cartDynamoItem struct {
	PK                 string               `dynamodbav:"pk"`
	SK                 string               `dynamodbav:"sk"`
	EntityType         string               `dynamodbav:"entity_type"`
	CustomerID         string               `dynamodbav:"customer_id"`
	Lines              []cartLineDynamoItem `dynamodbav:"lines"`
	PendingOrderID     string               `dynamodbav:"pending_order_id,omitempty"`
	PendingFingerprint string               `dynamodbav:"pending_fingerprint,omitempty"`
	Version            *int                 `dynamodbav:"version,omitempty"`
	UpdatedAt          string               `dynamodbav:"updated_at,omitempty"`
}

type addressDynamoItem struct {
	PK         string `dynamodbav:"pk"`
	SK         string `dynamodbav:"sk"`
	EntityType string `dynamodbav:"entity_type"`
	CustomerID string `dynamodbav:"customer_id"`
	AddressID  string `dynamodbav:"address_id"`
	FullName   string `dynamodbav:"full_name"`
	Line1      string `dynamodbav:"line1"`
	Line2      string `dynamodbav:"line2,omitempty"`
	City       string `dynamodbav:"city"`
	Region     string `dynamodbav:"region"`
	PostalCode string `dynamodbav:"postal_code"`
	Country    string `dynamodbav:"country"`
	Phone      string `dynamodbav:"phone,omitempty"`
	Version    *int   `dynamodbav:"version,omitempty"`
	CreatedAt  string `dynamodbav:"created_at,omitempty"`
	UpdatedAt  string `dynamodbav:"updated_at,omitempty"`
}

type orderLineDynamoItem struct {
	Slug           string `dynamodbav:"slug"`
	ProductID      string `dynamodbav:"product_id"`
	Name           string `dynamodbav:"name"`
	VariantID      string `dynamodbav:"variant_id,omitempty"`
	VariantLabel   string `dynamodbav:"variant_label,omitempty"`
	UnitPriceCents *int   `dynamodbav:"unit_price_cents"`
	Quantity       *int   `dynamodbav:"quantity"`
	LineTotalCents *int   `dynamodbav:"line_total_cents"`
	ImageURL       string `dynamodbav:"image_url"`
}

type orderAddressDynamoItem struct {
	FullName   string `dynamodbav:"full_name"`
	Line1      string `dynamodbav:"line1"`
	Line2      string `dynamodbav:"line2,omitempty"`
	City       string `dynamodbav:"city"`
	Region     string `dynamodbav:"region"`
	PostalCode string `dynamodbav:"postal_code"`
	Country    string `dynamodbav:"country"`
	Phone      string `dynamodbav:"phone,omitempty"`
}

type statusEventItem struct {
	Status string `dynamodbav:"status"`
	At     string `dynamodbav:"at"`
	Actor  string `dynamodbav:"actor"`
}

type orderDynamoItem struct {
	PK                      string                 `dynamodbav:"pk"`
	SK                      string                 `dynamodbav:"sk"`
	EntityType              string                 `dynamodbav:"entity_type"`
	OrderID                 string                 `dynamodbav:"order_id"`
	CustomerID              string                 `dynamodbav:"customer_id"`
	Email                   string                 `dynamodbav:"email"`
	Status                  string                 `dynamodbav:"status"`
	Version                 *int                   `dynamodbav:"version,omitempty"`
	Lines                   []orderLineDynamoItem  `dynamodbav:"lines"`
	SubtotalCents           *int                   `dynamodbav:"subtotal_cents"`
	ShippingCents           *int                   `dynamodbav:"shipping_cents"`
	TaxCents                *int                   `dynamodbav:"tax_cents"`
	TotalCents              *int                   `dynamodbav:"total_cents"`
	Currency                string                 `dynamodbav:"currency"`
	ShippingAddress         orderAddressDynamoItem `dynamodbav:"shipping_address"`
	CartFingerprint         string                 `dynamodbav:"cart_fingerprint"`
	StripeCheckoutSessionID string                 `dynamodbav:"stripe_checkout_session_id,omitempty"`
	StripePaymentIntentID   string                 `dynamodbav:"stripe_payment_intent_id,omitempty"`
	PaymentCardBrand        string                 `dynamodbav:"payment_card_brand,omitempty"`
	PaymentCardLast4        string                 `dynamodbav:"payment_card_last4,omitempty"`
	TrackingCarrier         string                 `dynamodbav:"tracking_carrier,omitempty"`
	TrackingNumber          string                 `dynamodbav:"tracking_number,omitempty"`
	StripeRefundID          string                 `dynamodbav:"stripe_refund_id,omitempty"`
	RefundAttempt           *int                   `dynamodbav:"refund_attempt,omitempty"`
	RefundFailureReason     string                 `dynamodbav:"refund_failure_reason,omitempty"`
	CheckoutAttempt         *int                   `dynamodbav:"checkout_attempt,omitempty"`
	StatusHistory           []statusEventItem      `dynamodbav:"status_history"`
	CreatedAt               string                 `dynamodbav:"created_at,omitempty"`
	UpdatedAt               string                 `dynamodbav:"updated_at,omitempty"`
	PaidAt                  string                 `dynamodbav:"paid_at,omitempty"`
	ShippedAt               string                 `dynamodbav:"shipped_at,omitempty"`
	DeliveredAt             string                 `dynamodbav:"delivered_at,omitempty"`
	RefundedAt              string                 `dynamodbav:"refunded_at,omitempty"`
	StockReleasedAt         string                 `dynamodbav:"stock_released_at,omitempty"`
	GSI1PK                  string                 `dynamodbav:"gsi1pk,omitempty"`
	GSI1SK                  string                 `dynamodbav:"gsi1sk,omitempty"`
	GSI2PK                  string                 `dynamodbav:"gsi2pk"`
	GSI2SK                  string                 `dynamodbav:"gsi2sk"`
}

type stripeEventDynamoItem struct {
	PK          string `dynamodbav:"pk"`
	SK          string `dynamodbav:"sk"`
	EntityType  string `dynamodbav:"entity_type"`
	EventID     string `dynamodbav:"event_id"`
	EventType   string `dynamodbav:"event_type"`
	OrderID     string `dynamodbav:"order_id,omitempty"`
	ProcessedAt string `dynamodbav:"processed_at"`
	ExpiresAt   int64  `dynamodbav:"expires_at"`
}

func customerItem(customer Customer) (map[string]types.AttributeValue, error) {
	return attributevalue.MarshalMap(customerDynamoItem{
		PK:               customerPK(customer.ID),
		SK:               customerSK,
		EntityType:       entityCustomer,
		CustomerID:       customer.ID,
		Email:            customer.Email,
		EmailNormalized:  customer.EmailNormalized,
		PasswordHash:     customer.PasswordHash,
		EmailVerified:    customer.EmailVerified,
		StripeCustomerID: customer.StripeCustomerID,
		DefaultAddressID: customer.DefaultAddressID,
		Version:          intPtr(customer.Version),
		CreatedAt:        formatCommerceTime(customer.CreatedAt),
		UpdatedAt:        formatCommerceTime(customer.UpdatedAt),
	})
}

func customerFromItem(item map[string]types.AttributeValue) (Customer, error) {
	var record customerDynamoItem
	if err := attributevalue.UnmarshalMap(item, &record); err != nil {
		return Customer{}, err
	}
	if record.EntityType != entityCustomer {
		return Customer{}, fmt.Errorf("commerce item is %q, not customer", record.EntityType)
	}
	createdAt, err := parseCommerceTime("created_at", record.CreatedAt)
	if err != nil {
		return Customer{}, err
	}
	updatedAt, err := parseCommerceTime("updated_at", record.UpdatedAt)
	if err != nil {
		return Customer{}, err
	}

	return Customer{
		ID:               record.CustomerID,
		Email:            record.Email,
		EmailNormalized:  record.EmailNormalized,
		PasswordHash:     record.PasswordHash,
		EmailVerified:    record.EmailVerified,
		StripeCustomerID: record.StripeCustomerID,
		DefaultAddressID: record.DefaultAddressID,
		Version:          intValue(record.Version),
		CreatedAt:        createdAt,
		UpdatedAt:        updatedAt,
	}, nil
}

func emailLockItem(customer Customer) (map[string]types.AttributeValue, error) {
	return attributevalue.MarshalMap(emailLockDynamoItem{
		PK:              emailLockPK(customer.EmailNormalized),
		SK:              emailLockSK,
		EntityType:      entityEmailLock,
		EmailNormalized: customer.EmailNormalized,
		CustomerID:      customer.ID,
		CreatedAt:       formatCommerceTime(customer.CreatedAt),
	})
}

func emailLockFromItem(item map[string]types.AttributeValue) (emailLockDynamoItem, error) {
	var record emailLockDynamoItem
	if err := attributevalue.UnmarshalMap(item, &record); err != nil {
		return emailLockDynamoItem{}, err
	}
	if record.EntityType != entityEmailLock {
		return emailLockDynamoItem{}, fmt.Errorf("commerce item is %q, not customer email lock", record.EntityType)
	}
	return record, nil
}

func sessionItem(session Session) (map[string]types.AttributeValue, error) {
	return attributevalue.MarshalMap(sessionDynamoItem{
		PK:         customerPK(session.CustomerID),
		SK:         sessionSK(session.TokenHash),
		EntityType: entitySession,
		CustomerID: session.CustomerID,
		Nonce:      session.Nonce,
		CreatedAt:  formatCommerceTime(session.CreatedAt),
		ExpiresAt:  session.ExpiresAt.UTC().Unix(),
	})
}

func sessionFromItem(item map[string]types.AttributeValue) (Session, error) {
	var record sessionDynamoItem
	if err := attributevalue.UnmarshalMap(item, &record); err != nil {
		return Session{}, err
	}
	if record.EntityType != entitySession {
		return Session{}, fmt.Errorf("commerce item is %q, not session", record.EntityType)
	}
	createdAt, err := parseCommerceTime("created_at", record.CreatedAt)
	if err != nil {
		return Session{}, err
	}

	return Session{
		CustomerID: record.CustomerID,
		TokenHash:  strings.TrimPrefix(record.SK, sessionSKPrefix),
		Nonce:      record.Nonce,
		CreatedAt:  createdAt,
		ExpiresAt:  time.Unix(record.ExpiresAt, 0).UTC(),
	}, nil
}

func passwordResetTokenItem(token PasswordResetToken) (map[string]types.AttributeValue, error) {
	return attributevalue.MarshalMap(passwordResetTokenDynamoItem{
		PK:         customerPK(token.CustomerID),
		SK:         passwordResetSK,
		EntityType: entityPasswordReset,
		CustomerID: token.CustomerID,
		TokenHash:  token.TokenHash,
		ExpiresAt:  token.ExpiresAt.UTC().Unix(),
		CreatedAt:  formatCommerceTime(token.CreatedAt),
		UsedAt:     formatCommerceTime(token.UsedAt),
		Version:    intPtr(token.Version),
	})
}

func passwordResetTokenFromItem(item map[string]types.AttributeValue) (PasswordResetToken, error) {
	var record passwordResetTokenDynamoItem
	if err := attributevalue.UnmarshalMap(item, &record); err != nil {
		return PasswordResetToken{}, err
	}
	if record.EntityType != entityPasswordReset {
		return PasswordResetToken{}, fmt.Errorf("commerce item is %q, not password reset token", record.EntityType)
	}
	createdAt, err := parseCommerceTime("created_at", record.CreatedAt)
	if err != nil {
		return PasswordResetToken{}, err
	}
	usedAt, err := parseCommerceTime("used_at", record.UsedAt)
	if err != nil {
		return PasswordResetToken{}, err
	}
	return PasswordResetToken{
		CustomerID: record.CustomerID,
		TokenHash:  record.TokenHash,
		CreatedAt:  createdAt,
		ExpiresAt:  time.Unix(record.ExpiresAt, 0).UTC(),
		UsedAt:     usedAt,
		Version:    intValue(record.Version),
	}, nil
}

func passwordResetTokenUsable(token PasswordResetToken, tokenHash string, now time.Time) bool {
	return token.TokenHash == tokenHash && token.UsedAt.IsZero() && token.ExpiresAt.After(now.UTC())
}

func cartItem(record CartRecord) (map[string]types.AttributeValue, error) {
	lines := make([]cartLineDynamoItem, 0, len(record.Lines))
	for _, line := range record.Lines {
		lines = append(lines, cartLineDynamoItem{
			Slug:      line.Slug,
			VariantID: line.VariantID,
			Quantity:  intPtr(line.Quantity),
		})
	}
	return attributevalue.MarshalMap(cartDynamoItem{
		PK:                 customerPK(record.CustomerID),
		SK:                 cartSK,
		EntityType:         entityCart,
		CustomerID:         record.CustomerID,
		Lines:              lines,
		PendingOrderID:     record.PendingOrderID,
		PendingFingerprint: record.PendingFingerprint,
		Version:            intPtr(record.Version),
		UpdatedAt:          formatCommerceTime(record.UpdatedAt),
	})
}

func cartFromItem(item map[string]types.AttributeValue) (CartRecord, error) {
	var record cartDynamoItem
	if err := attributevalue.UnmarshalMap(item, &record); err != nil {
		return CartRecord{}, err
	}
	if record.EntityType != entityCart {
		return CartRecord{}, fmt.Errorf("commerce item is %q, not cart", record.EntityType)
	}
	updatedAt, err := parseCommerceTime("updated_at", record.UpdatedAt)
	if err != nil {
		return CartRecord{}, err
	}

	lines := make([]cart.Line, 0, len(record.Lines))
	for _, line := range record.Lines {
		lines = append(lines, cart.Line{
			Slug:      line.Slug,
			VariantID: line.VariantID,
			Quantity:  intValue(line.Quantity),
		})
	}

	return CartRecord{
		CustomerID:         record.CustomerID,
		Lines:              lines,
		PendingOrderID:     record.PendingOrderID,
		PendingFingerprint: record.PendingFingerprint,
		Version:            intValue(record.Version),
		UpdatedAt:          updatedAt,
	}, nil
}

func addressItem(address Address) (map[string]types.AttributeValue, error) {
	return attributevalue.MarshalMap(addressDynamoItem{
		PK:         customerPK(address.CustomerID),
		SK:         addressSK(address.ID),
		EntityType: entityAddress,
		CustomerID: address.CustomerID,
		AddressID:  address.ID,
		FullName:   address.FullName,
		Line1:      address.Line1,
		Line2:      address.Line2,
		City:       address.City,
		Region:     address.Region,
		PostalCode: address.PostalCode,
		Country:    address.Country,
		Phone:      address.Phone,
		Version:    intPtr(address.Version),
		CreatedAt:  formatCommerceTime(address.CreatedAt),
		UpdatedAt:  formatCommerceTime(address.UpdatedAt),
	})
}

func addressFromItem(item map[string]types.AttributeValue) (Address, error) {
	var record addressDynamoItem
	if err := attributevalue.UnmarshalMap(item, &record); err != nil {
		return Address{}, err
	}
	if record.EntityType != entityAddress {
		return Address{}, fmt.Errorf("commerce item is %q, not address", record.EntityType)
	}
	createdAt, err := parseCommerceTime("created_at", record.CreatedAt)
	if err != nil {
		return Address{}, err
	}
	updatedAt, err := parseCommerceTime("updated_at", record.UpdatedAt)
	if err != nil {
		return Address{}, err
	}

	return Address{
		CustomerID: record.CustomerID,
		ID:         record.AddressID,
		FullName:   record.FullName,
		Line1:      record.Line1,
		Line2:      record.Line2,
		City:       record.City,
		Region:     record.Region,
		PostalCode: record.PostalCode,
		Country:    record.Country,
		Phone:      record.Phone,
		Version:    intValue(record.Version),
		CreatedAt:  createdAt,
		UpdatedAt:  updatedAt,
	}, nil
}

func statusEventItemFrom(event StatusEvent) statusEventItem {
	return statusEventItem{
		Status: string(event.Status),
		At:     formatCommerceTime(event.At),
		Actor:  event.Actor,
	}
}

func orderItem(order Order) (map[string]types.AttributeValue, error) {
	lines := make([]orderLineDynamoItem, 0, len(order.Lines))
	for _, line := range order.Lines {
		lines = append(lines, orderLineDynamoItem{
			Slug:           line.Slug,
			ProductID:      line.ProductID,
			Name:           line.Name,
			VariantID:      line.VariantID,
			VariantLabel:   line.VariantLabel,
			UnitPriceCents: intPtr(line.UnitPriceCents),
			Quantity:       intPtr(line.Quantity),
			LineTotalCents: intPtr(line.LineTotalCents),
			ImageURL:       line.ImageURL,
		})
	}
	history := make([]statusEventItem, 0, len(order.StatusHistory))
	for _, event := range order.StatusHistory {
		history = append(history, statusEventItemFrom(event))
	}

	item := orderDynamoItem{
		PK:            orderPK(order.ID),
		SK:            orderSK,
		EntityType:    entityOrder,
		OrderID:       order.ID,
		CustomerID:    order.CustomerID,
		Email:         order.Email,
		Status:        string(order.Status),
		Version:       intPtr(order.Version),
		Lines:         lines,
		SubtotalCents: intPtr(order.SubtotalCents),
		ShippingCents: intPtr(order.ShippingCents),
		TaxCents:      intPtr(order.TaxCents),
		TotalCents:    intPtr(order.TotalCents),
		Currency:      order.Currency,
		ShippingAddress: orderAddressDynamoItem{
			FullName:   order.ShippingAddress.FullName,
			Line1:      order.ShippingAddress.Line1,
			Line2:      order.ShippingAddress.Line2,
			City:       order.ShippingAddress.City,
			Region:     order.ShippingAddress.Region,
			PostalCode: order.ShippingAddress.PostalCode,
			Country:    order.ShippingAddress.Country,
			Phone:      order.ShippingAddress.Phone,
		},
		CartFingerprint:         order.CartFingerprint,
		StripeCheckoutSessionID: order.StripeCheckoutSessionID,
		StripePaymentIntentID:   order.StripePaymentIntentID,
		PaymentCardBrand:        order.PaymentCardBrand,
		PaymentCardLast4:        order.PaymentCardLast4,
		TrackingCarrier:         order.TrackingCarrier,
		TrackingNumber:          order.TrackingNumber,
		StripeRefundID:          order.StripeRefundID,
		RefundFailureReason:     order.RefundFailureReason,
		StatusHistory:           history,
		CreatedAt:               formatCommerceTime(order.CreatedAt),
		UpdatedAt:               formatCommerceTime(order.UpdatedAt),
		PaidAt:                  formatCommerceTime(order.PaidAt),
		ShippedAt:               formatCommerceTime(order.ShippedAt),
		DeliveredAt:             formatCommerceTime(order.DeliveredAt),
		RefundedAt:              formatCommerceTime(order.RefundedAt),
		StockReleasedAt:         formatCommerceTime(order.StockReleasedAt),
		GSI2PK:                  ordersIndexPK,
		GSI2SK:                  ordersIndexSK(order),
	}
	if order.CustomerID != "" {
		// Guest orders (CustomerID == "") are sparse in gsi1: DynamoDB skips
		// items missing an index key attribute, so they never pollute the
		// customer-orders index while staying fully listable on gsi2.
		item.GSI1PK = customerOrdersIndexPK(order.CustomerID)
		item.GSI1SK = customerOrdersIndexSK(order)
	}
	if order.CheckoutAttempt != 0 {
		item.CheckoutAttempt = intPtr(order.CheckoutAttempt)
	}
	if order.RefundAttempt != 0 {
		item.RefundAttempt = intPtr(order.RefundAttempt)
	}

	return attributevalue.MarshalMap(item)
}

func ordersFromItems(items []map[string]types.AttributeValue) ([]Order, error) {
	orders := make([]Order, 0, len(items))
	for _, item := range items {
		order, err := orderFromItem(item)
		if err != nil {
			return nil, err
		}
		orders = append(orders, order)
	}
	return orders, nil
}

func orderFromItem(item map[string]types.AttributeValue) (Order, error) {
	var record orderDynamoItem
	if err := attributevalue.UnmarshalMap(item, &record); err != nil {
		return Order{}, err
	}
	if record.EntityType != entityOrder {
		return Order{}, fmt.Errorf("commerce item is %q, not order", record.EntityType)
	}

	createdAt, err := parseCommerceTime("created_at", record.CreatedAt)
	if err != nil {
		return Order{}, err
	}
	updatedAt, err := parseCommerceTime("updated_at", record.UpdatedAt)
	if err != nil {
		return Order{}, err
	}
	paidAt, err := parseCommerceTime("paid_at", record.PaidAt)
	if err != nil {
		return Order{}, err
	}
	shippedAt, err := parseCommerceTime("shipped_at", record.ShippedAt)
	if err != nil {
		return Order{}, err
	}
	deliveredAt, err := parseCommerceTime("delivered_at", record.DeliveredAt)
	if err != nil {
		return Order{}, err
	}
	refundedAt, err := parseCommerceTime("refunded_at", record.RefundedAt)
	if err != nil {
		return Order{}, err
	}
	stockReleasedAt, err := parseCommerceTime("stock_released_at", record.StockReleasedAt)
	if err != nil {
		return Order{}, err
	}

	lines := make([]OrderLine, 0, len(record.Lines))
	for _, line := range record.Lines {
		lines = append(lines, OrderLine{
			Slug:           line.Slug,
			ProductID:      line.ProductID,
			Name:           line.Name,
			VariantID:      line.VariantID,
			VariantLabel:   line.VariantLabel,
			UnitPriceCents: intValue(line.UnitPriceCents),
			Quantity:       intValue(line.Quantity),
			LineTotalCents: intValue(line.LineTotalCents),
			ImageURL:       line.ImageURL,
		})
	}
	history := make([]StatusEvent, 0, len(record.StatusHistory))
	for _, event := range record.StatusHistory {
		at, err := parseCommerceTime("status_history.at", event.At)
		if err != nil {
			return Order{}, err
		}
		history = append(history, StatusEvent{Status: OrderStatus(event.Status), At: at, Actor: event.Actor})
	}

	return Order{
		ID:            record.OrderID,
		CustomerID:    record.CustomerID,
		Email:         record.Email,
		Status:        OrderStatus(record.Status),
		Version:       intValue(record.Version),
		Lines:         lines,
		SubtotalCents: intValue(record.SubtotalCents),
		ShippingCents: intValue(record.ShippingCents),
		TaxCents:      intValue(record.TaxCents),
		TotalCents:    intValue(record.TotalCents),
		Currency:      record.Currency,
		ShippingAddress: OrderAddress{
			FullName:   record.ShippingAddress.FullName,
			Line1:      record.ShippingAddress.Line1,
			Line2:      record.ShippingAddress.Line2,
			City:       record.ShippingAddress.City,
			Region:     record.ShippingAddress.Region,
			PostalCode: record.ShippingAddress.PostalCode,
			Country:    record.ShippingAddress.Country,
			Phone:      record.ShippingAddress.Phone,
		},
		CartFingerprint:         record.CartFingerprint,
		StripeCheckoutSessionID: record.StripeCheckoutSessionID,
		StripePaymentIntentID:   record.StripePaymentIntentID,
		PaymentCardBrand:        record.PaymentCardBrand,
		PaymentCardLast4:        record.PaymentCardLast4,
		TrackingCarrier:         record.TrackingCarrier,
		TrackingNumber:          record.TrackingNumber,
		StripeRefundID:          record.StripeRefundID,
		RefundAttempt:           intValue(record.RefundAttempt),
		RefundFailureReason:     record.RefundFailureReason,
		CheckoutAttempt:         intValue(record.CheckoutAttempt),
		StatusHistory:           history,
		CreatedAt:               createdAt,
		UpdatedAt:               updatedAt,
		PaidAt:                  paidAt,
		ShippedAt:               shippedAt,
		DeliveredAt:             deliveredAt,
		RefundedAt:              refundedAt,
		StockReleasedAt:         stockReleasedAt,
	}, nil
}

func stripeEventItem(eventID string, eventType string, orderID string, now time.Time) (map[string]types.AttributeValue, error) {
	return attributevalue.MarshalMap(stripeEventDynamoItem{
		PK:          stripeEventPK(eventID),
		SK:          stripeEventSK,
		EntityType:  entityStripeEvent,
		EventID:     eventID,
		EventType:   eventType,
		OrderID:     orderID,
		ProcessedAt: formatCommerceTime(now),
		ExpiresAt:   now.UTC().Add(stripeEventTTL).Unix(),
	})
}

func emailEventItem(event EmailEvent) (map[string]types.AttributeValue, error) {
	return attributevalue.MarshalMap(emailEventDynamoItem{
		PK:           orderPK(event.OrderID),
		SK:           emailEventSK(event.Key),
		EntityType:   entityEmailEvent,
		EventKey:     event.Key,
		Kind:         event.Kind,
		OrderID:      event.OrderID,
		OrderVersion: intPtr(event.OrderVersion),
		To:           event.To,
		Status:       event.Status,
		Attempts:     intPtr(event.Attempts),
		LastError:    event.LastError,
		CreatedAt:    formatCommerceTime(event.CreatedAt),
		SentAt:       formatCommerceTime(event.SentAt),
		FailedAt:     formatCommerceTime(event.FailedAt),
	})
}

func emailEventFromItem(item map[string]types.AttributeValue) (EmailEvent, error) {
	var record emailEventDynamoItem
	if err := attributevalue.UnmarshalMap(item, &record); err != nil {
		return EmailEvent{}, err
	}
	if record.EntityType != entityEmailEvent {
		return EmailEvent{}, fmt.Errorf("commerce item is %q, not email event", record.EntityType)
	}
	createdAt, err := parseCommerceTime("created_at", record.CreatedAt)
	if err != nil {
		return EmailEvent{}, err
	}
	sentAt, err := parseCommerceTime("sent_at", record.SentAt)
	if err != nil {
		return EmailEvent{}, err
	}
	failedAt, err := parseCommerceTime("failed_at", record.FailedAt)
	if err != nil {
		return EmailEvent{}, err
	}

	return EmailEvent{
		Key:          record.EventKey,
		Kind:         record.Kind,
		OrderID:      record.OrderID,
		OrderVersion: intValue(record.OrderVersion),
		To:           record.To,
		Status:       record.Status,
		Attempts:     intValue(record.Attempts),
		LastError:    record.LastError,
		CreatedAt:    createdAt,
		SentAt:       sentAt,
		FailedAt:     failedAt,
	}, nil
}

func formatCommerceTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}

	return value.UTC().Format(time.RFC3339)
}

func parseCommerceTime(attributeName string, value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}

	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse commerce %s %q: %w", attributeName, value, err)
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
