package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const EnvAdminLoginAttemptsTableName = "ADMIN_LOGIN_ATTEMPTS_TABLE_NAME"

const adminLoginAttemptLimit = 8
const adminLoginAttemptWindow = 15 * time.Minute
const adminLoginLockout = 15 * time.Minute
const adminLoginThrottleWriteAttempts = 5

const unknownAdminLoginClient = "unknown"

type adminLoginThrottle interface {
	ReserveAttempt(context.Context, string, time.Time) (adminLoginThrottleStatus, error)
	Clear(context.Context, string) error
}

type adminLoginThrottleStatus struct {
	Allowed     bool
	LockedUntil time.Time
}

func (s adminLoginThrottleStatus) Locked(now time.Time) bool {
	return !s.LockedUntil.IsZero() && now.UTC().Before(s.LockedUntil.UTC())
}

func (s adminLoginThrottleStatus) RetryAfter(now time.Time) int {
	if !s.Locked(now) {
		return 0
	}
	seconds := int(s.LockedUntil.UTC().Sub(now.UTC()).Seconds())
	if seconds < 1 {
		return 1
	}
	return seconds
}

type noopAdminLoginThrottle struct{}

func (noopAdminLoginThrottle) ReserveAttempt(ctx context.Context, client string, now time.Time) (adminLoginThrottleStatus, error) {
	_ = ctx
	_ = client
	_ = now
	return adminLoginThrottleStatus{Allowed: true}, nil
}

func (noopAdminLoginThrottle) Clear(ctx context.Context, client string) error {
	_ = ctx
	_ = client
	return nil
}

type dynamoAdminLoginThrottleClient interface {
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	UpdateItem(context.Context, *dynamodb.UpdateItemInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
}

type dynamoAdminLoginThrottle struct {
	tableName string
	secret    string
	client    dynamoAdminLoginThrottleClient
}

type adminLoginAttemptRecord struct {
	ClientKey     string
	FailureCount  int
	FirstFailedAt time.Time
	LockedUntil   time.Time
}

func adminLoginThrottleFromEnvironment(ctx context.Context, secret string) (adminLoginThrottle, error) {
	tableName := strings.TrimSpace(os.Getenv(EnvAdminLoginAttemptsTableName))
	if tableName == "" {
		return noopAdminLoginThrottle{}, nil
	}
	awsConfig, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return dynamoAdminLoginThrottle{
		tableName: tableName,
		secret:    secret,
		client:    dynamodb.NewFromConfig(awsConfig),
	}, nil
}

func (t dynamoAdminLoginThrottle) ReserveAttempt(ctx context.Context, client string, now time.Time) (adminLoginThrottleStatus, error) {
	now = now.UTC()
	for attempt := 0; attempt < adminLoginThrottleWriteAttempts; attempt++ {
		status, err := t.reserveNewWindow(ctx, client, now)
		if err == nil {
			return status, nil
		}
		if !isConditionalCheckFailed(err) {
			return adminLoginThrottleStatus{}, err
		}

		status, err = t.reserveIncrement(ctx, client, now)
		if err == nil {
			return status, nil
		}
		if !isConditionalCheckFailed(err) {
			return adminLoginThrottleStatus{}, err
		}

		status, err = t.reserveLockingAttempt(ctx, client, now)
		if err == nil {
			return status, nil
		}
		if !isConditionalCheckFailed(err) {
			return adminLoginThrottleStatus{}, err
		}

		record, found, err := t.record(ctx, client)
		if err != nil {
			return adminLoginThrottleStatus{}, err
		}
		if found && record.LockedUntil.After(now) {
			return adminLoginThrottleStatus{LockedUntil: record.LockedUntil}, nil
		}
	}
	return adminLoginThrottleStatus{LockedUntil: now.Add(adminLoginLockout)}, nil
}

func (t dynamoAdminLoginThrottle) Clear(ctx context.Context, client string) error {
	if t.tableName == "" || t.client == nil {
		return nil
	}
	_, err := t.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(t.tableName),
		Key: map[string]types.AttributeValue{
			"client_key": &types.AttributeValueMemberS{Value: t.clientKey(client)},
		},
	})
	return err
}

func (t dynamoAdminLoginThrottle) record(ctx context.Context, client string) (adminLoginAttemptRecord, bool, error) {
	if t.tableName == "" || t.client == nil {
		return adminLoginAttemptRecord{}, false, nil
	}
	output, err := t.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(t.tableName),
		Key: map[string]types.AttributeValue{
			"client_key": &types.AttributeValueMemberS{Value: t.clientKey(client)},
		},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return adminLoginAttemptRecord{}, false, err
	}
	if len(output.Item) == 0 {
		return adminLoginAttemptRecord{}, false, nil
	}

	record := adminLoginAttemptRecord{ClientKey: t.clientKey(client)}
	record.FailureCount = int(numberAttribute(output.Item, "failure_count"))
	firstFailedAt := numberAttribute(output.Item, "first_failed_at")
	if firstFailedAt <= 0 {
		return adminLoginAttemptRecord{}, false, nil
	}
	record.FirstFailedAt = time.Unix(firstFailedAt, 0).UTC()
	if lockedUntil := numberAttribute(output.Item, "locked_until"); lockedUntil > 0 {
		record.LockedUntil = time.Unix(lockedUntil, 0).UTC()
	}
	return record, true, nil
}

func (t dynamoAdminLoginThrottle) reserveNewWindow(ctx context.Context, client string, now time.Time) (adminLoginThrottleStatus, error) {
	if t.tableName == "" || t.client == nil {
		return adminLoginThrottleStatus{Allowed: true}, nil
	}
	output, err := t.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(t.tableName),
		Key:                 t.key(client),
		ConditionExpression: aws.String("attribute_not_exists(client_key) OR (first_failed_at <= :window_start AND (attribute_not_exists(locked_until) OR locked_until <= :zero OR locked_until <= :now)) OR (attribute_exists(locked_until) AND locked_until <= :now AND locked_until > :zero)"),
		UpdateExpression:    aws.String("SET failure_count = :one, first_failed_at = :now, locked_until = :zero, expires_at = :expires_at"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":one":          numberValue(1),
			":now":          numberValue(now.Unix()),
			":window_start": numberValue(now.Add(-adminLoginAttemptWindow).Unix()),
			":zero":         numberValue(0),
			":expires_at":   numberValue(adminLoginAttemptExpiresAt(now)),
		},
		ReturnValues: types.ReturnValueAllNew,
	})
	if err != nil {
		return adminLoginThrottleStatus{}, err
	}
	return statusFromAttributes(output.Attributes, true), nil
}

func (t dynamoAdminLoginThrottle) reserveIncrement(ctx context.Context, client string, now time.Time) (adminLoginThrottleStatus, error) {
	if t.tableName == "" || t.client == nil {
		return adminLoginThrottleStatus{Allowed: true}, nil
	}
	output, err := t.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(t.tableName),
		Key:                 t.key(client),
		ConditionExpression: aws.String("attribute_exists(client_key) AND first_failed_at > :window_start AND failure_count < :lock_threshold AND (attribute_not_exists(locked_until) OR locked_until <= :zero OR locked_until <= :now)"),
		UpdateExpression:    aws.String("SET expires_at = :expires_at ADD failure_count :one"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":one":            numberValue(1),
			":now":            numberValue(now.Unix()),
			":window_start":   numberValue(now.Add(-adminLoginAttemptWindow).Unix()),
			":zero":           numberValue(0),
			":lock_threshold": numberValue(int64(adminLoginAttemptLimit - 1)),
			":expires_at":     numberValue(adminLoginAttemptExpiresAt(now)),
		},
		ReturnValues: types.ReturnValueAllNew,
	})
	if err != nil {
		return adminLoginThrottleStatus{}, err
	}
	return statusFromAttributes(output.Attributes, true), nil
}

func (t dynamoAdminLoginThrottle) reserveLockingAttempt(ctx context.Context, client string, now time.Time) (adminLoginThrottleStatus, error) {
	if t.tableName == "" || t.client == nil {
		return adminLoginThrottleStatus{Allowed: true}, nil
	}
	lockedUntil := now.Add(adminLoginLockout)
	output, err := t.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(t.tableName),
		Key:                 t.key(client),
		ConditionExpression: aws.String("attribute_exists(client_key) AND first_failed_at > :window_start AND failure_count = :lock_threshold AND (attribute_not_exists(locked_until) OR locked_until <= :zero OR locked_until <= :now)"),
		UpdateExpression:    aws.String("SET failure_count = :limit, locked_until = :locked_until, expires_at = :expires_at"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":now":            numberValue(now.Unix()),
			":window_start":   numberValue(now.Add(-adminLoginAttemptWindow).Unix()),
			":zero":           numberValue(0),
			":lock_threshold": numberValue(int64(adminLoginAttemptLimit - 1)),
			":limit":          numberValue(int64(adminLoginAttemptLimit)),
			":locked_until":   numberValue(lockedUntil.Unix()),
			":expires_at":     numberValue(adminLoginAttemptExpiresAt(now)),
		},
		ReturnValues: types.ReturnValueAllNew,
	})
	if err != nil {
		return adminLoginThrottleStatus{}, err
	}
	return statusFromAttributes(output.Attributes, true), nil
}

func statusFromAttributes(item map[string]types.AttributeValue, allowed bool) adminLoginThrottleStatus {
	return adminLoginThrottleStatus{
		Allowed:     allowed,
		LockedUntil: unixAttributeTime(item, "locked_until"),
	}
}

func adminLoginAttemptExpiresAt(now time.Time) int64 {
	return now.UTC().Add(adminLoginAttemptWindow + adminLoginLockout + time.Hour).Unix()
}

func (t dynamoAdminLoginThrottle) key(client string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"client_key": &types.AttributeValueMemberS{Value: t.clientKey(client)},
	}
}

func (t dynamoAdminLoginThrottle) clientKey(client string) string {
	client = strings.TrimSpace(client)
	if client == "" {
		client = unknownAdminLoginClient
	}
	mac := hmac.New(sha256.New, []byte(t.secret))
	mac.Write([]byte(client))
	return hex.EncodeToString(mac.Sum(nil))
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
