package admin

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/appenv"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestProductionAdminLoginThrottleFromEnvironmentReturnsErrorWhenTableMissing(t *testing.T) {
	t.Setenv(appenv.EnvAppEnvironment, appenv.EnvironmentProduction)
	t.Setenv(EnvAdminLoginAttemptsTableName, "")

	throttle, err := adminLoginThrottleFromEnvironment(context.Background(), testSessionSecret)
	if !errors.Is(err, ErrAdminLoginThrottleNotConfigured) {
		t.Fatalf("adminLoginThrottleFromEnvironment error = %v, want %v", err, ErrAdminLoginThrottleNotConfigured)
	}
	if throttle != nil {
		t.Fatalf("throttle = %#v, want nil", throttle)
	}
}

func TestLocalAdminLoginThrottleFromEnvironmentReturnsNoopWhenTableMissing(t *testing.T) {
	t.Setenv(appenv.EnvAppEnvironment, "development")
	t.Setenv(EnvAdminLoginAttemptsTableName, "")

	throttle, err := adminLoginThrottleFromEnvironment(context.Background(), testSessionSecret)
	if err != nil {
		t.Fatalf("adminLoginThrottleFromEnvironment returned error: %v", err)
	}
	if _, ok := throttle.(noopAdminLoginThrottle); !ok {
		t.Fatalf("throttle = %T, want noopAdminLoginThrottle", throttle)
	}
}

func TestDynamoAdminLoginThrottleReserveWritesHashedClientKeyAndTTL(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	fakeClient := newFakeDynamoAdminLoginThrottleClient()
	throttle := dynamoAdminLoginThrottle{tableName: "attempts", secret: testSessionSecret, client: fakeClient}

	status, err := throttle.ReserveAttempt(context.Background(), "203.0.113.10", now)
	if err != nil {
		t.Fatalf("ReserveAttempt returned error: %v", err)
	}
	if !status.Allowed || status.Locked(now) {
		t.Fatalf("status = %#v, want allowed unlocked reservation", status)
	}

	item := fakeClient.itemForKey(t, throttle.clientKey("203.0.113.10"))
	if key := stringAttribute(item, "client_key"); key == "203.0.113.10" || key != throttle.clientKey("203.0.113.10") {
		t.Fatalf("client_key = %q, want HMAC key and no raw IP", key)
	}
	if count := numberAttribute(item, "failure_count"); count != 1 {
		t.Fatalf("failure_count = %d, want 1", count)
	}
	if firstFailedAt := numberAttribute(item, "first_failed_at"); firstFailedAt != now.Unix() {
		t.Fatalf("first_failed_at = %d, want %d", firstFailedAt, now.Unix())
	}
	if lockedUntil := numberAttribute(item, "locked_until"); lockedUntil != 0 {
		t.Fatalf("locked_until = %d, want 0", lockedUntil)
	}
	if expiresAt := numberAttribute(item, "expires_at"); expiresAt != adminLoginAttemptExpiresAt(now) {
		t.Fatalf("expires_at = %d, want %d", expiresAt, adminLoginAttemptExpiresAt(now))
	}
}

func TestDynamoAdminLoginThrottleLocksOnEighthAttemptAndBlocksNinth(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	fakeClient := newFakeDynamoAdminLoginThrottleClient()
	throttle := dynamoAdminLoginThrottle{tableName: "attempts", secret: testSessionSecret, client: fakeClient}

	for attempt := 1; attempt <= adminLoginAttemptLimit; attempt++ {
		status, err := throttle.ReserveAttempt(context.Background(), "203.0.113.20", now)
		if err != nil {
			t.Fatalf("ReserveAttempt %d returned error: %v", attempt, err)
		}
		if !status.Allowed {
			t.Fatalf("attempt %d status = %#v, want allowed", attempt, status)
		}
		if attempt < adminLoginAttemptLimit && status.Locked(now) {
			t.Fatalf("attempt %d status = %#v, want not locked yet", attempt, status)
		}
		if attempt == adminLoginAttemptLimit {
			wantLockedUntil := now.Add(adminLoginLockout)
			if !status.Locked(now) || !status.LockedUntil.Equal(wantLockedUntil) {
				t.Fatalf("attempt %d status = %#v, want lock until %s", attempt, status, wantLockedUntil)
			}
		}
	}

	blocked, err := throttle.ReserveAttempt(context.Background(), "203.0.113.20", now.Add(time.Second))
	if err != nil {
		t.Fatalf("ReserveAttempt blocked returned error: %v", err)
	}
	if blocked.Allowed || !blocked.Locked(now.Add(time.Second)) {
		t.Fatalf("blocked status = %#v, want denied active lock", blocked)
	}
}

func TestDynamoAdminLoginThrottleExpiredWindowAndLockReset(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	fakeClient := newFakeDynamoAdminLoginThrottleClient()
	throttle := dynamoAdminLoginThrottle{tableName: "attempts", secret: testSessionSecret, client: fakeClient}
	client := "203.0.113.30"

	fakeClient.putItem(throttle.clientKey(client), map[string]types.AttributeValue{
		"client_key":      &types.AttributeValueMemberS{Value: throttle.clientKey(client)},
		"failure_count":   numberValue(3),
		"first_failed_at": numberValue(now.Add(-adminLoginAttemptWindow - time.Second).Unix()),
		"locked_until":    numberValue(0),
		"expires_at":      numberValue(adminLoginAttemptExpiresAt(now)),
	})
	status, err := throttle.ReserveAttempt(context.Background(), client, now)
	if err != nil {
		t.Fatalf("ReserveAttempt expired window returned error: %v", err)
	}
	if !status.Allowed || status.Locked(now) {
		t.Fatalf("expired window status = %#v, want fresh allowed reservation", status)
	}
	item := fakeClient.itemForKey(t, throttle.clientKey(client))
	if count := numberAttribute(item, "failure_count"); count != 1 {
		t.Fatalf("failure_count after window reset = %d, want 1", count)
	}
	if firstFailedAt := numberAttribute(item, "first_failed_at"); firstFailedAt != now.Unix() {
		t.Fatalf("first_failed_at after window reset = %d, want %d", firstFailedAt, now.Unix())
	}

	expiredLock := now.Add(-time.Second)
	fakeClient.putItem(throttle.clientKey(client), map[string]types.AttributeValue{
		"client_key":      &types.AttributeValueMemberS{Value: throttle.clientKey(client)},
		"failure_count":   numberValue(int64(adminLoginAttemptLimit)),
		"first_failed_at": numberValue(now.Add(-time.Minute).Unix()),
		"locked_until":    numberValue(expiredLock.Unix()),
		"expires_at":      numberValue(adminLoginAttemptExpiresAt(now)),
	})
	status, err = throttle.ReserveAttempt(context.Background(), client, now)
	if err != nil {
		t.Fatalf("ReserveAttempt expired lock returned error: %v", err)
	}
	if !status.Allowed || status.Locked(now) {
		t.Fatalf("expired lock status = %#v, want fresh allowed reservation", status)
	}
	item = fakeClient.itemForKey(t, throttle.clientKey(client))
	if count := numberAttribute(item, "failure_count"); count != 1 {
		t.Fatalf("failure_count after lock reset = %d, want 1", count)
	}
}

func TestDynamoAdminLoginThrottleExpiredWindowDoesNotResetActiveLock(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 20, 0, 0, time.UTC)
	fakeClient := newFakeDynamoAdminLoginThrottleClient()
	throttle := dynamoAdminLoginThrottle{tableName: "attempts", secret: testSessionSecret, client: fakeClient}
	client := "203.0.113.35"
	lockedUntil := now.Add(9 * time.Minute)

	fakeClient.putItem(throttle.clientKey(client), map[string]types.AttributeValue{
		"client_key":      &types.AttributeValueMemberS{Value: throttle.clientKey(client)},
		"failure_count":   numberValue(int64(adminLoginAttemptLimit)),
		"first_failed_at": numberValue(now.Add(-adminLoginAttemptWindow - time.Second).Unix()),
		"locked_until":    numberValue(lockedUntil.Unix()),
		"expires_at":      numberValue(adminLoginAttemptExpiresAt(now)),
	})
	status, err := throttle.ReserveAttempt(context.Background(), client, now)
	if err != nil {
		t.Fatalf("ReserveAttempt returned error: %v", err)
	}
	if status.Allowed || !status.LockedUntil.Equal(lockedUntil) {
		t.Fatalf("status = %#v, want denied active lock until %s", status, lockedUntil)
	}
}

func TestDynamoAdminLoginThrottleClearDeletesHashedClientKey(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	fakeClient := newFakeDynamoAdminLoginThrottleClient()
	throttle := dynamoAdminLoginThrottle{tableName: "attempts", secret: testSessionSecret, client: fakeClient}
	client := "203.0.113.40"
	if _, err := throttle.ReserveAttempt(context.Background(), client, now); err != nil {
		t.Fatalf("ReserveAttempt returned error: %v", err)
	}

	if err := throttle.Clear(context.Background(), client); err != nil {
		t.Fatalf("Clear returned error: %v", err)
	}
	if fakeClient.hasItem(throttle.clientKey(client)) {
		t.Fatalf("item for hashed client key was not deleted")
	}
	if fakeClient.deletedKey == client {
		t.Fatalf("deleted raw client key %q, want hashed key", fakeClient.deletedKey)
	}
}

func TestDynamoAdminLoginThrottleConditionalContentionRetriesAndFailsClosed(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	fakeClient := newFakeDynamoAdminLoginThrottleClient()
	fakeClient.forcedConditionalFailures = 2
	throttle := dynamoAdminLoginThrottle{tableName: "attempts", secret: testSessionSecret, client: fakeClient}

	status, err := throttle.ReserveAttempt(context.Background(), "203.0.113.50", now)
	if err != nil {
		t.Fatalf("ReserveAttempt returned error: %v", err)
	}
	if !status.Allowed || fakeClient.updateCalls < 3 {
		t.Fatalf("status = %#v, updateCalls = %d; want retry then allowed", status, fakeClient.updateCalls)
	}

	failingClient := newFakeDynamoAdminLoginThrottleClient()
	failingClient.forcedConditionalFailures = 100
	throttle = dynamoAdminLoginThrottle{tableName: "attempts", secret: testSessionSecret, client: failingClient}
	status, err = throttle.ReserveAttempt(context.Background(), "203.0.113.60", now)
	if err != nil {
		t.Fatalf("ReserveAttempt fail-closed returned error: %v", err)
	}
	if status.Allowed || !status.Locked(now) {
		t.Fatalf("fail-closed status = %#v, want denied lockout", status)
	}
}

type fakeDynamoAdminLoginThrottleClient struct {
	mutex                     sync.Mutex
	items                     map[string]map[string]types.AttributeValue
	forcedConditionalFailures int
	updateCalls               int
	deletedKey                string
}

func newFakeDynamoAdminLoginThrottleClient() *fakeDynamoAdminLoginThrottleClient {
	return &fakeDynamoAdminLoginThrottleClient{items: map[string]map[string]types.AttributeValue{}}
}

func (f *fakeDynamoAdminLoginThrottleClient) GetItem(ctx context.Context, input *dynamodb.GetItemInput, options ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	_ = ctx
	_ = options
	f.mutex.Lock()
	defer f.mutex.Unlock()
	key := keyAttribute(input.Key)
	item := cloneAttributeMap(f.items[key])
	return &dynamodb.GetItemOutput{Item: item}, nil
}

func (f *fakeDynamoAdminLoginThrottleClient) UpdateItem(ctx context.Context, input *dynamodb.UpdateItemInput, options ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	_ = ctx
	_ = options
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.updateCalls++
	if f.forcedConditionalFailures > 0 {
		f.forcedConditionalFailures--
		return nil, &types.ConditionalCheckFailedException{}
	}

	key := keyAttribute(input.Key)
	item := cloneAttributeMap(f.items[key])
	if item == nil {
		item = map[string]types.AttributeValue{}
	}
	if !f.conditionMatches(aws.ToString(input.ConditionExpression), item, input.ExpressionAttributeValues) {
		return nil, &types.ConditionalCheckFailedException{}
	}
	item["client_key"] = &types.AttributeValueMemberS{Value: key}
	switch updateExpression := aws.ToString(input.UpdateExpression); {
	case strings.Contains(updateExpression, "failure_count = :one"):
		item["failure_count"] = input.ExpressionAttributeValues[":one"]
		item["first_failed_at"] = input.ExpressionAttributeValues[":now"]
		item["locked_until"] = input.ExpressionAttributeValues[":zero"]
		item["expires_at"] = input.ExpressionAttributeValues[":expires_at"]
	case strings.Contains(updateExpression, "ADD failure_count"):
		item["failure_count"] = numberValue(numberAttribute(item, "failure_count") + numberAttribute(input.ExpressionAttributeValues, ":one"))
		item["expires_at"] = input.ExpressionAttributeValues[":expires_at"]
	case strings.Contains(updateExpression, "failure_count = :limit"):
		item["failure_count"] = input.ExpressionAttributeValues[":limit"]
		item["locked_until"] = input.ExpressionAttributeValues[":locked_until"]
		item["expires_at"] = input.ExpressionAttributeValues[":expires_at"]
	default:
		panic("unhandled update expression: " + updateExpression)
	}
	f.items[key] = cloneAttributeMap(item)
	return &dynamodb.UpdateItemOutput{Attributes: cloneAttributeMap(item)}, nil
}

func (f *fakeDynamoAdminLoginThrottleClient) DeleteItem(ctx context.Context, input *dynamodb.DeleteItemInput, options ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	_ = ctx
	_ = options
	f.mutex.Lock()
	defer f.mutex.Unlock()
	key := keyAttribute(input.Key)
	f.deletedKey = key
	delete(f.items, key)
	return &dynamodb.DeleteItemOutput{}, nil
}

func (f *fakeDynamoAdminLoginThrottleClient) conditionMatches(condition string, item map[string]types.AttributeValue, values map[string]types.AttributeValue) bool {
	exists := stringAttribute(item, "client_key") != ""
	firstFailedAt := numberAttribute(item, "first_failed_at")
	failureCount := numberAttribute(item, "failure_count")
	lockedUntil := numberAttribute(item, "locked_until")
	now := numberAttribute(values, ":now")
	windowStart := numberAttribute(values, ":window_start")
	zero := numberAttribute(values, ":zero")
	switch {
	case strings.Contains(condition, "attribute_not_exists(client_key)"):
		return !exists || (firstFailedAt <= windowStart && (lockedUntil <= zero || lockedUntil <= now)) || (lockedUntil <= now && lockedUntil > zero)
	case strings.Contains(condition, "failure_count < :lock_threshold"):
		return exists && firstFailedAt > windowStart && failureCount < numberAttribute(values, ":lock_threshold") && (lockedUntil <= zero || lockedUntil <= now)
	case strings.Contains(condition, "failure_count = :lock_threshold"):
		return exists && firstFailedAt > windowStart && failureCount == numberAttribute(values, ":lock_threshold") && (lockedUntil <= zero || lockedUntil <= now)
	default:
		panic("unhandled condition expression: " + condition)
	}
}

func (f *fakeDynamoAdminLoginThrottleClient) putItem(key string, item map[string]types.AttributeValue) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.items[key] = cloneAttributeMap(item)
}

func (f *fakeDynamoAdminLoginThrottleClient) itemForKey(t *testing.T, key string) map[string]types.AttributeValue {
	t.Helper()
	f.mutex.Lock()
	defer f.mutex.Unlock()
	item := cloneAttributeMap(f.items[key])
	if item == nil {
		t.Fatalf("missing item for key %q", key)
	}
	return item
}

func (f *fakeDynamoAdminLoginThrottleClient) hasItem(key string) bool {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	_, found := f.items[key]
	return found
}

func keyAttribute(key map[string]types.AttributeValue) string {
	return stringAttribute(key, "client_key")
}

func stringAttribute(item map[string]types.AttributeValue, name string) string {
	value, ok := item[name].(*types.AttributeValueMemberS)
	if !ok {
		return ""
	}
	return value.Value
}

func cloneAttributeMap(item map[string]types.AttributeValue) map[string]types.AttributeValue {
	if item == nil {
		return nil
	}
	clone := make(map[string]types.AttributeValue, len(item))
	for key, value := range item {
		clone[key] = value
	}
	return clone
}
