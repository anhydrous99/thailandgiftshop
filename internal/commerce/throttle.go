package commerce

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Customer login throttling is a port of the admin reserve-before-verify state
// machine (internal/admin/login_throttle.go) over THROTTLE rows in the
// commerce table: 8 failures per 15-minute window, 15-minute lock, at most 5
// conditional-write contention rounds before failing closed, TTL via
// expires_at.
const (
	ThrottleScopeIP    = "IP"
	ThrottleScopeEmail = "EMAIL"

	LoginAttemptLimit  = 8
	LoginAttemptWindow = 15 * time.Minute
	LoginLockout       = 15 * time.Minute
)

const loginThrottleWriteAttempts = 5
const unknownThrottleValue = "unknown"

// ThrottleKey derives the throttle row key for one scoped value. The value is
// HMAC-SHA256 hashed with the customer session secret so raw IPs and emails
// are never stored.
func ThrottleKey(scope string, value string, secret string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = unknownThrottleValue
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(value))
	return scope + "#" + hex.EncodeToString(mac.Sum(nil))
}

type ThrottleDecision struct {
	Allowed     bool
	LockedUntil time.Time
}

func (d ThrottleDecision) Locked(now time.Time) bool {
	return !d.LockedUntil.IsZero() && now.UTC().Before(d.LockedUntil.UTC())
}

func (d ThrottleDecision) RetryAfter(now time.Time) int {
	if !d.Locked(now) {
		return 0
	}
	seconds := int(d.LockedUntil.UTC().Sub(now.UTC()).Seconds())
	if seconds < 1 {
		return 1
	}
	return seconds
}

func loginAttemptExpiresAt(now time.Time) int64 {
	return now.UTC().Add(LoginAttemptWindow + LoginLockout + time.Hour).Unix()
}

type throttleRecord struct {
	FailureCount  int
	FirstFailedAt time.Time
	LockedUntil   time.Time
}

func (s *DynamoStore) ReserveLoginAttempt(ctx context.Context, key string, now time.Time) (ThrottleDecision, error) {
	now = now.UTC()
	for attempt := 0; attempt < loginThrottleWriteAttempts; attempt++ {
		decision, err := s.reserveThrottleNewWindow(ctx, key, now)
		if err == nil {
			return decision, nil
		}
		if !isConditionalCheckFailed(err) {
			return ThrottleDecision{}, err
		}

		decision, err = s.reserveThrottleIncrement(ctx, key, now)
		if err == nil {
			return decision, nil
		}
		if !isConditionalCheckFailed(err) {
			return ThrottleDecision{}, err
		}

		decision, err = s.reserveThrottleLockingAttempt(ctx, key, now)
		if err == nil {
			return decision, nil
		}
		if !isConditionalCheckFailed(err) {
			return ThrottleDecision{}, err
		}

		record, found, err := s.throttleRecord(ctx, key)
		if err != nil {
			return ThrottleDecision{}, err
		}
		if found && record.LockedUntil.After(now) {
			return ThrottleDecision{LockedUntil: record.LockedUntil}, nil
		}
	}
	return ThrottleDecision{LockedUntil: now.Add(LoginLockout)}, nil
}

func (s *DynamoStore) ClearLoginAttempts(ctx context.Context, keys []string) error {
	if s.delete == nil {
		return errors.New("commerce store is missing a delete client")
	}
	for _, key := range keys {
		started := time.Now()
		_, err := s.delete.DeleteItem(ctx, &dynamodb.DeleteItemInput{
			TableName: aws.String(s.tableName),
			Key:       itemKey(throttlePK(key), throttleSK),
		})
		s.recordCommerceOperation("DeleteItem", "ClearLoginAttempts", time.Since(started))
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *DynamoStore) throttleRecord(ctx context.Context, key string) (throttleRecord, bool, error) {
	if s.get == nil {
		return throttleRecord{}, false, errors.New("commerce store is missing a get client")
	}
	started := time.Now()
	output, err := s.get.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.tableName),
		Key:            itemKey(throttlePK(key), throttleSK),
		ConsistentRead: aws.Bool(true),
	})
	s.recordCommerceOperation("GetItem", "ReserveLoginAttempt", time.Since(started))
	if err != nil {
		return throttleRecord{}, false, err
	}
	if len(output.Item) == 0 {
		return throttleRecord{}, false, nil
	}

	record := throttleRecord{}
	record.FailureCount = int(numberAttribute(output.Item, "failure_count"))
	firstFailedAt := numberAttribute(output.Item, "first_failed_at")
	if firstFailedAt <= 0 {
		return throttleRecord{}, false, nil
	}
	record.FirstFailedAt = time.Unix(firstFailedAt, 0).UTC()
	if lockedUntil := numberAttribute(output.Item, "locked_until"); lockedUntil > 0 {
		record.LockedUntil = time.Unix(lockedUntil, 0).UTC()
	}
	return record, true, nil
}

func (s *DynamoStore) reserveThrottleNewWindow(ctx context.Context, key string, now time.Time) (ThrottleDecision, error) {
	output, err := s.throttleUpdate(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(s.tableName),
		Key:                 itemKey(throttlePK(key), throttleSK),
		ConditionExpression: aws.String("attribute_not_exists(pk) OR (first_failed_at <= :window_start AND (attribute_not_exists(locked_until) OR locked_until <= :zero OR locked_until <= :now)) OR (attribute_exists(locked_until) AND locked_until <= :now AND locked_until > :zero)"),
		UpdateExpression:    aws.String("SET entity_type = :entity_type, failure_count = :one, first_failed_at = :now, locked_until = :zero, expires_at = :expires_at"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":entity_type":  &types.AttributeValueMemberS{Value: entityThrottle},
			":one":          numberValue(1),
			":now":          numberValue(now.Unix()),
			":window_start": numberValue(now.Add(-LoginAttemptWindow).Unix()),
			":zero":         numberValue(0),
			":expires_at":   numberValue(loginAttemptExpiresAt(now)),
		},
		ReturnValues: types.ReturnValueAllNew,
	})
	if err != nil {
		return ThrottleDecision{}, err
	}
	return throttleDecisionFromAttributes(output.Attributes), nil
}

func (s *DynamoStore) reserveThrottleIncrement(ctx context.Context, key string, now time.Time) (ThrottleDecision, error) {
	output, err := s.throttleUpdate(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(s.tableName),
		Key:                 itemKey(throttlePK(key), throttleSK),
		ConditionExpression: aws.String("attribute_exists(pk) AND first_failed_at > :window_start AND failure_count < :lock_threshold AND (attribute_not_exists(locked_until) OR locked_until <= :zero OR locked_until <= :now)"),
		UpdateExpression:    aws.String("SET expires_at = :expires_at ADD failure_count :one"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":one":            numberValue(1),
			":now":            numberValue(now.Unix()),
			":window_start":   numberValue(now.Add(-LoginAttemptWindow).Unix()),
			":zero":           numberValue(0),
			":lock_threshold": numberValue(int64(LoginAttemptLimit - 1)),
			":expires_at":     numberValue(loginAttemptExpiresAt(now)),
		},
		ReturnValues: types.ReturnValueAllNew,
	})
	if err != nil {
		return ThrottleDecision{}, err
	}
	return throttleDecisionFromAttributes(output.Attributes), nil
}

func (s *DynamoStore) reserveThrottleLockingAttempt(ctx context.Context, key string, now time.Time) (ThrottleDecision, error) {
	lockedUntil := now.Add(LoginLockout)
	output, err := s.throttleUpdate(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(s.tableName),
		Key:                 itemKey(throttlePK(key), throttleSK),
		ConditionExpression: aws.String("attribute_exists(pk) AND first_failed_at > :window_start AND failure_count = :lock_threshold AND (attribute_not_exists(locked_until) OR locked_until <= :zero OR locked_until <= :now)"),
		UpdateExpression:    aws.String("SET failure_count = :limit, locked_until = :locked_until, expires_at = :expires_at"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":now":            numberValue(now.Unix()),
			":window_start":   numberValue(now.Add(-LoginAttemptWindow).Unix()),
			":zero":           numberValue(0),
			":lock_threshold": numberValue(int64(LoginAttemptLimit - 1)),
			":limit":          numberValue(int64(LoginAttemptLimit)),
			":locked_until":   numberValue(lockedUntil.Unix()),
			":expires_at":     numberValue(loginAttemptExpiresAt(now)),
		},
		ReturnValues: types.ReturnValueAllNew,
	})
	if err != nil {
		return ThrottleDecision{}, err
	}
	return throttleDecisionFromAttributes(output.Attributes), nil
}

func (s *DynamoStore) throttleUpdate(ctx context.Context, input *dynamodb.UpdateItemInput) (*dynamodb.UpdateItemOutput, error) {
	if s.update == nil {
		return nil, errors.New("commerce store is missing an update client")
	}
	started := time.Now()
	output, err := s.update.UpdateItem(ctx, input)
	s.recordCommerceOperation("UpdateItem", "ReserveLoginAttempt", time.Since(started))
	return output, err
}

func throttleDecisionFromAttributes(item map[string]types.AttributeValue) ThrottleDecision {
	return ThrottleDecision{
		Allowed:     true,
		LockedUntil: unixAttributeTime(item, "locked_until"),
	}
}

// memoryThrottleRecord mirrors the THROTTLE row for the in-memory store.
type memoryThrottleRecord struct {
	failureCount  int
	firstFailedAt time.Time
	lockedUntil   time.Time
	expiresAt     time.Time
}

func (s *MemoryStore) ReserveLoginAttempt(ctx context.Context, key string, now time.Time) (ThrottleDecision, error) {
	_ = ctx
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	record, found := s.throttle[key]
	windowStart := now.Add(-LoginAttemptWindow)
	lockActive := !record.lockedUntil.IsZero() && record.lockedUntil.After(now)
	lockExpired := !record.lockedUntil.IsZero() && !record.lockedUntil.After(now)
	expiresAt := time.Unix(loginAttemptExpiresAt(now), 0).UTC()

	switch {
	case !found || lockExpired || (!record.firstFailedAt.After(windowStart) && !lockActive):
		s.throttle[key] = memoryThrottleRecord{failureCount: 1, firstFailedAt: now, expiresAt: expiresAt}
		return ThrottleDecision{Allowed: true}, nil
	case lockActive:
		return ThrottleDecision{LockedUntil: record.lockedUntil}, nil
	case record.failureCount < LoginAttemptLimit-1:
		record.failureCount++
		record.expiresAt = expiresAt
		s.throttle[key] = record
		return ThrottleDecision{Allowed: true}, nil
	case record.failureCount == LoginAttemptLimit-1:
		record.failureCount = LoginAttemptLimit
		record.lockedUntil = now.Add(LoginLockout)
		record.expiresAt = expiresAt
		s.throttle[key] = record
		return ThrottleDecision{Allowed: true, LockedUntil: record.lockedUntil}, nil
	default:
		return ThrottleDecision{LockedUntil: now.Add(LoginLockout)}, nil
	}
}

func (s *MemoryStore) ClearLoginAttempts(ctx context.Context, keys []string) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range keys {
		delete(s.throttle, key)
	}
	return nil
}

func numberValue(value int64) *types.AttributeValueMemberN {
	return &types.AttributeValueMemberN{Value: strconv.FormatInt(value, 10)}
}

func numberAttribute(item map[string]types.AttributeValue, name string) int64 {
	value, ok := item[name].(*types.AttributeValueMemberN)
	if !ok {
		return 0
	}
	parsed, err := strconv.ParseInt(value.Value, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func unixAttributeTime(item map[string]types.AttributeValue, name string) time.Time {
	value := numberAttribute(item, name)
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(value, 0).UTC()
}

func isConditionalCheckFailed(err error) bool {
	var conditionalCheckFailed *types.ConditionalCheckFailedException
	return errors.As(err, &conditionalCheckFailed)
}
