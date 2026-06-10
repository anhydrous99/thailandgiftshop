package commerce

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const testSessionSecret = "throttle-test-session-secret"

func TestThrottleKeyHashesValue(t *testing.T) {
	key := ThrottleKey(ThrottleScopeIP, "203.0.113.10", testSessionSecret)
	if !strings.HasPrefix(key, "IP#") {
		t.Fatalf("key = %q, want IP# prefix", key)
	}
	if len(key) != len("IP#")+64 {
		t.Fatalf("key length = %d, want scope + 64 hex chars", len(key))
	}
	if strings.Contains(key, "203.0.113.10") {
		t.Fatalf("key %q leaks the raw value", key)
	}

	if again := ThrottleKey(ThrottleScopeIP, "203.0.113.10", testSessionSecret); again != key {
		t.Fatalf("ThrottleKey is not deterministic: %q != %q", again, key)
	}
	if other := ThrottleKey(ThrottleScopeIP, "203.0.113.11", testSessionSecret); other == key {
		t.Fatalf("different values produced the same key")
	}
	if otherSecret := ThrottleKey(ThrottleScopeIP, "203.0.113.10", "another-secret"); otherSecret == key {
		t.Fatalf("different secrets produced the same key")
	}

	emailKey := ThrottleKey(ThrottleScopeEmail, "shopper@example.test", testSessionSecret)
	if !strings.HasPrefix(emailKey, "EMAIL#") {
		t.Fatalf("email key = %q, want EMAIL# prefix", emailKey)
	}
	if strings.Contains(emailKey, "shopper@example.test") {
		t.Fatalf("email key %q leaks the raw value", emailKey)
	}

	blank := ThrottleKey(ThrottleScopeIP, "   ", testSessionSecret)
	if blank != ThrottleKey(ThrottleScopeIP, "", testSessionSecret) {
		t.Fatalf("blank values should normalize to one unknown key")
	}
}

func TestThrottleDecisionLockedAndRetryAfter(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)

	unlocked := ThrottleDecision{Allowed: true}
	if unlocked.Locked(now) || unlocked.RetryAfter(now) != 0 {
		t.Fatalf("unlocked decision = locked %v retryAfter %d", unlocked.Locked(now), unlocked.RetryAfter(now))
	}

	locked := ThrottleDecision{LockedUntil: now.Add(90 * time.Second)}
	if !locked.Locked(now) || locked.RetryAfter(now) != 90 {
		t.Fatalf("locked decision = locked %v retryAfter %d, want true/90", locked.Locked(now), locked.RetryAfter(now))
	}
	if locked.Locked(now.Add(2 * time.Minute)) {
		t.Fatalf("decision still locked after expiry")
	}
	almost := ThrottleDecision{LockedUntil: now.Add(200 * time.Millisecond)}
	if almost.RetryAfter(now) != 1 {
		t.Fatalf("sub-second RetryAfter = %d, want clamped to 1", almost.RetryAfter(now))
	}
}

func TestReserveLoginAttemptLocksOnEighthAndBlocksNinth(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			key := ThrottleKey(ThrottleScopeIP, "203.0.113.20", testSessionSecret)

			for attempt := 1; attempt <= LoginAttemptLimit; attempt++ {
				decision, err := fixture.store.ReserveLoginAttempt(context.Background(), key, now)
				if err != nil {
					t.Fatalf("ReserveLoginAttempt %d returned error: %v", attempt, err)
				}
				if !decision.Allowed {
					t.Fatalf("attempt %d decision = %#v, want allowed", attempt, decision)
				}
				if attempt < LoginAttemptLimit && decision.Locked(now) {
					t.Fatalf("attempt %d decision = %#v, want not locked yet", attempt, decision)
				}
				if attempt == LoginAttemptLimit {
					wantLockedUntil := now.Add(LoginLockout)
					if !decision.Locked(now) || !decision.LockedUntil.Equal(wantLockedUntil) {
						t.Fatalf("attempt %d decision = %#v, want lock until %s", attempt, decision, wantLockedUntil)
					}
				}
			}

			blocked, err := fixture.store.ReserveLoginAttempt(context.Background(), key, now.Add(time.Second))
			if err != nil {
				t.Fatalf("blocked ReserveLoginAttempt returned error: %v", err)
			}
			if blocked.Allowed || !blocked.Locked(now.Add(time.Second)) {
				t.Fatalf("blocked decision = %#v, want denied active lock", blocked)
			}
		})
	}
}

func TestReserveLoginAttemptExpiredWindowResets(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			key := ThrottleKey(ThrottleScopeIP, "203.0.113.30", testSessionSecret)

			for attempt := 0; attempt < 3; attempt++ {
				if _, err := fixture.store.ReserveLoginAttempt(context.Background(), key, now); err != nil {
					t.Fatalf("seed ReserveLoginAttempt returned error: %v", err)
				}
			}

			// After the window lapses the counter restarts: a full fresh run of
			// attempts is available before the lock engages again.
			later := now.Add(LoginAttemptWindow + time.Second)
			for attempt := 1; attempt <= LoginAttemptLimit; attempt++ {
				decision, err := fixture.store.ReserveLoginAttempt(context.Background(), key, later)
				if err != nil {
					t.Fatalf("post-window ReserveLoginAttempt %d returned error: %v", attempt, err)
				}
				if !decision.Allowed {
					t.Fatalf("post-window attempt %d decision = %#v, want allowed", attempt, decision)
				}
				if attempt < LoginAttemptLimit && decision.Locked(later) {
					t.Fatalf("post-window attempt %d locked early: %#v", attempt, decision)
				}
				if attempt == LoginAttemptLimit && !decision.Locked(later) {
					t.Fatalf("post-window attempt %d decision = %#v, want lock", attempt, decision)
				}
			}
		})
	}
}

func TestReserveLoginAttemptExpiredLockResets(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			key := ThrottleKey(ThrottleScopeIP, "203.0.113.40", testSessionSecret)

			for attempt := 0; attempt < LoginAttemptLimit; attempt++ {
				if _, err := fixture.store.ReserveLoginAttempt(context.Background(), key, now); err != nil {
					t.Fatalf("seed ReserveLoginAttempt returned error: %v", err)
				}
			}

			afterLock := now.Add(LoginLockout + time.Second)
			decision, err := fixture.store.ReserveLoginAttempt(context.Background(), key, afterLock)
			if err != nil {
				t.Fatalf("post-lock ReserveLoginAttempt returned error: %v", err)
			}
			if !decision.Allowed || decision.Locked(afterLock) {
				t.Fatalf("post-lock decision = %#v, want fresh allowed reservation", decision)
			}
		})
	}
}

func TestReserveLoginAttemptExpiredWindowDoesNotResetActiveLock(t *testing.T) {
	start := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			key := ThrottleKey(ThrottleScopeIP, "203.0.113.50", testSessionSecret)

			// First failure opens the window at start; the lock engages on the
			// 8th failure ten minutes in, so the lock outlives the window.
			if _, err := fixture.store.ReserveLoginAttempt(context.Background(), key, start); err != nil {
				t.Fatalf("seed ReserveLoginAttempt returned error: %v", err)
			}
			lockTime := start.Add(10 * time.Minute)
			var lockDecision ThrottleDecision
			for attempt := 0; attempt < LoginAttemptLimit-1; attempt++ {
				decision, err := fixture.store.ReserveLoginAttempt(context.Background(), key, lockTime)
				if err != nil {
					t.Fatalf("seed ReserveLoginAttempt returned error: %v", err)
				}
				lockDecision = decision
			}
			wantLockedUntil := lockTime.Add(LoginLockout)
			if !lockDecision.LockedUntil.Equal(wantLockedUntil) {
				t.Fatalf("lock decision = %#v, want locked until %s", lockDecision, wantLockedUntil)
			}

			// Window is stale but the lock is still active: still denied.
			staleWindow := start.Add(LoginAttemptWindow + time.Minute)
			decision, err := fixture.store.ReserveLoginAttempt(context.Background(), key, staleWindow)
			if err != nil {
				t.Fatalf("stale-window ReserveLoginAttempt returned error: %v", err)
			}
			if decision.Allowed || !decision.LockedUntil.Equal(wantLockedUntil) {
				t.Fatalf("stale-window decision = %#v, want denied lock until %s", decision, wantLockedUntil)
			}
		})
	}
}

func TestClearLoginAttemptsResetsCounters(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ipKey := ThrottleKey(ThrottleScopeIP, "203.0.113.60", testSessionSecret)
			emailKey := ThrottleKey(ThrottleScopeEmail, "shopper@example.test", testSessionSecret)

			for attempt := 0; attempt < 5; attempt++ {
				for _, key := range []string{ipKey, emailKey} {
					if _, err := fixture.store.ReserveLoginAttempt(context.Background(), key, now); err != nil {
						t.Fatalf("seed ReserveLoginAttempt returned error: %v", err)
					}
				}
			}

			if err := fixture.store.ClearLoginAttempts(context.Background(), []string{ipKey, emailKey}); err != nil {
				t.Fatalf("ClearLoginAttempts returned error: %v", err)
			}

			// A cleared key has the full budget again.
			for attempt := 1; attempt <= LoginAttemptLimit; attempt++ {
				decision, err := fixture.store.ReserveLoginAttempt(context.Background(), ipKey, now)
				if err != nil {
					t.Fatalf("post-clear ReserveLoginAttempt %d returned error: %v", attempt, err)
				}
				if !decision.Allowed {
					t.Fatalf("post-clear attempt %d decision = %#v, want allowed", attempt, decision)
				}
				if attempt < LoginAttemptLimit && decision.Locked(now) {
					t.Fatalf("post-clear attempt %d locked early: %#v", attempt, decision)
				}
			}
		})
	}
}

func TestDynamoReserveLoginAttemptWritesThrottleRowShape(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	client := newFakeCommerceClient()
	store := NewDynamoStore(client, testDynamoConfig())
	key := ThrottleKey(ThrottleScopeIP, "203.0.113.70", testSessionSecret)

	decision, err := store.ReserveLoginAttempt(context.Background(), key, now)
	if err != nil {
		t.Fatalf("ReserveLoginAttempt returned error: %v", err)
	}
	if !decision.Allowed || decision.Locked(now) {
		t.Fatalf("decision = %#v, want allowed unlocked reservation", decision)
	}

	item := client.itemForKey(t, throttlePK(key), throttleSK)
	if got := stringAttribute(item, "pk"); !strings.HasPrefix(got, "THROTTLE#IP#") || strings.Contains(got, "203.0.113.70") {
		t.Fatalf("pk = %q, want hashed THROTTLE#IP# key with no raw IP", got)
	}
	if got := stringAttribute(item, "entity_type"); got != "THROTTLE" {
		t.Fatalf("entity_type = %q, want THROTTLE", got)
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
	if expiresAt := numberAttribute(item, "expires_at"); expiresAt != loginAttemptExpiresAt(now) {
		t.Fatalf("expires_at = %d, want %d", expiresAt, loginAttemptExpiresAt(now))
	}
	for _, gsiAttribute := range []string{"gsi1pk", "gsi1sk", "gsi2pk", "gsi2sk"} {
		if _, found := item[gsiAttribute]; found {
			t.Fatalf("throttle row carries %s; lock rows must have no GSI attributes", gsiAttribute)
		}
	}
}

func TestDynamoClearLoginAttemptsDeletesHashedKeys(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	client := newFakeCommerceClient()
	store := NewDynamoStore(client, testDynamoConfig())
	key := ThrottleKey(ThrottleScopeEmail, "shopper@example.test", testSessionSecret)

	if _, err := store.ReserveLoginAttempt(context.Background(), key, now); err != nil {
		t.Fatalf("ReserveLoginAttempt returned error: %v", err)
	}
	if err := store.ClearLoginAttempts(context.Background(), []string{key}); err != nil {
		t.Fatalf("ClearLoginAttempts returned error: %v", err)
	}
	if client.hasItem(throttlePK(key), throttleSK) {
		t.Fatalf("throttle row was not deleted")
	}
	for _, deleted := range client.deletedKeys {
		if strings.Contains(deleted, "shopper@example.test") {
			t.Fatalf("deleted raw email key %q, want hashed key", deleted)
		}
	}
}

func TestDynamoReserveLoginAttemptContentionRetriesAndFailsClosed(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	client := newFakeCommerceClient()
	client.forcedThrottleConditionalFailures = 2
	store := NewDynamoStore(client, testDynamoConfig())
	key := ThrottleKey(ThrottleScopeIP, "203.0.113.80", testSessionSecret)

	decision, err := store.ReserveLoginAttempt(context.Background(), key, now)
	if err != nil {
		t.Fatalf("ReserveLoginAttempt returned error: %v", err)
	}
	if !decision.Allowed || client.throttleUpdateCalls < 3 {
		t.Fatalf("decision = %#v, updateCalls = %d; want retry then allowed", decision, client.throttleUpdateCalls)
	}

	failingClient := newFakeCommerceClient()
	failingClient.forcedThrottleConditionalFailures = 100
	store = NewDynamoStore(failingClient, testDynamoConfig())
	decision, err = store.ReserveLoginAttempt(context.Background(), ThrottleKey(ThrottleScopeIP, "203.0.113.90", testSessionSecret), now)
	if err != nil {
		t.Fatalf("fail-closed ReserveLoginAttempt returned error: %v", err)
	}
	if decision.Allowed || !decision.Locked(now) {
		t.Fatalf("fail-closed decision = %#v, want denied lockout", decision)
	}
}

func TestDynamoReserveLoginAttemptSurfacesUnexpectedErrors(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	store := NewDynamoStore(&failingUpdateClient{}, testDynamoConfig())

	if _, err := store.ReserveLoginAttempt(context.Background(), "IP#abc", now); err == nil {
		t.Fatalf("ReserveLoginAttempt with failing client succeeded, want error")
	}
}

// failingUpdateClient returns a non-conditional error from UpdateItem so the
// reserve loop surfaces it instead of retrying.
type failingUpdateClient struct {
	fakeCommerceClient
}

func (f *failingUpdateClient) UpdateItem(ctx context.Context, input *dynamodb.UpdateItemInput, options ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	_ = ctx
	_ = input
	_ = options
	return nil, &types.ProvisionedThroughputExceededException{}
}
