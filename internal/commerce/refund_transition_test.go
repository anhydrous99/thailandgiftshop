package commerce

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/observability"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func refundTestOrder(t *testing.T, store Store) Order {
	t.Helper()
	order, err := store.CreateOrder(context.Background(), Order{CustomerID: "customer", Status: OrderStatusRefundPending, StripeRefundID: "re_1", RefundAttempt: 1, TotalCents: 100, Currency: "usd"})
	if err != nil {
		t.Fatal(err)
	}
	return order
}

func refundTestTransition(t *testing.T, store Store, order Order, to OrderStatus, patch OrderPatch) Order {
	t.Helper()
	updated, err := store.TransitionOrder(context.Background(), order.ID, order.Status, to, patch)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func refundTestUnchanged(t *testing.T, store Store, before Order) {
	t.Helper()
	after, found, err := store.GetOrder(context.Background(), before.ID)
	if err != nil || !found || !reflect.DeepEqual(before, after) {
		t.Fatalf("order changed: before=%+v after=%+v found=%v err=%v", before, after, found, err)
	}
}

// A caller expectation must be checked before replay, even with identical
// terminal status/reason, or a writer from attempt 1 can succeed on attempt 2.
func TestRefundTransitionCallerGuard(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		for _, target := range []OrderStatus{OrderStatusRefundFailed, OrderStatusRefunded} {
			for _, terminal := range []bool{false, true} {
				t.Run(fixture.name+"/"+string(target)+"/terminal="+map[bool]string{false: "no", true: "yes"}[terminal], func(t *testing.T) {
					old := refundTestOrder(t, fixture.store)
					reason, id, attempt := "same_reason", "re_2", 2
					current := refundTestTransition(t, fixture.store, old, OrderStatusRefundFailed, OrderPatch{RefundFailureReason: &reason})
					current = refundTestTransition(t, fixture.store, current, OrderStatusRefundPending, OrderPatch{StripeRefundID: &id, RefundAttempt: &attempt})
					if terminal {
						current = refundTestTransition(t, fixture.store, current, target, OrderPatch{RefundFailureReason: &reason})
					}
					_, err := fixture.store.TransitionOrder(context.Background(), old.ID, old.Status, target, OrderPatch{ExpectedVersion: &old.Version, RefundFailureReason: &reason})
					if !errors.Is(err, ErrOrderTransitionConflict) {
						t.Errorf("stale settlement = %v, want conflict", err)
					}
					refundTestUnchanged(t, fixture.store, current)
				})
			}
		}
	}
}

func TestRefundTransitionEquivalentWriters(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			old := refundTestOrder(t, fixture.store)
			patch := OrderPatch{ExpectedVersion: &old.Version}
			winner := refundTestTransition(t, fixture.store, old, OrderStatusRefunded, patch)
			if winner.Version != old.Version+1 || len(winner.StatusHistory) != len(old.StatusHistory)+1 {
				t.Fatal("winner must mutate exactly once")
			}
			for _, version := range []int{old.Version, winner.Version} {
				if _, err := fixture.store.TransitionOrder(context.Background(), old.ID, old.Status, OrderStatusRefunded, OrderPatch{ExpectedVersion: &version}); !errors.Is(err, ErrOrderTransitionConflict) {
					t.Errorf("guarded replay version %d = %v", version, err)
				}
			}
			refundTestUnchanged(t, fixture.store, winner)
			// Nil retains the general store replay contract, even for paid callers.
			if _, err := fixture.store.TransitionOrder(context.Background(), old.ID, OrderStatusPaid, OrderStatusRefunded, OrderPatch{}); err != nil {
				t.Fatal(err)
			}
			refundTestUnchanged(t, fixture.store, winner)
		})
	}
}

type refundDynamoPauseKey struct{}
type refundDynamoClient struct {
	*fakeCommerceClient
	point         string
	entered       chan struct{}
	resume        chan struct{}
	read          *dynamodb.GetItemInput
	update        *dynamodb.UpdateItemInput
	writeErr      error
	decodeFailure bool
}

func (c *refundDynamoClient) pause(ctx context.Context, point string) error {
	if ctx.Value(refundDynamoPauseKey{}) != true || c.point != point {
		return nil
	}
	select {
	case c.entered <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-c.resume:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *refundDynamoClient) GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	if ctx.Value(refundDynamoPauseKey{}) == true {
		c.read = in
	}
	if err := c.pause(ctx, "read"); err != nil {
		return nil, err
	}
	return c.fakeCommerceClient.GetItem(ctx, in, opts...)
}
func (c *refundDynamoClient) UpdateItem(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	if ctx.Value(refundDynamoPauseKey{}) == true {
		c.update = in
	}
	if err := c.pause(ctx, "update"); err != nil {
		return nil, err
	}
	if c.writeErr != nil {
		return nil, c.writeErr
	}
	// The shared fake deliberately lacks NULL equality. Model only this
	// type-mismatch case locally: NULL is present (not absent), and cannot
	// equal a numeric expected version. Never weaken or bypass the request.
	c.mu.Lock()
	_, nullVersion := c.items[fakeItemKey(in.Key)]["version"].(*types.AttributeValueMemberNULL)
	c.mu.Unlock()
	if nullVersion && strings.Contains(aws.ToString(in.ConditionExpression), "#version = :expected_version") {
		if _, numeric := in.ExpressionAttributeValues[":expected_version"].(*types.AttributeValueMemberN); numeric {
			return nil, &types.ConditionalCheckFailedException{}
		}
	}
	out, err := c.fakeCommerceClient.UpdateItem(ctx, in, opts...)
	if err == nil && c.decodeFailure {
		out.Attributes["version"] = &types.AttributeValueMemberS{Value: "invalid"}
	}
	return out, err
}

func TestRefundDynamoInterleaving(t *testing.T) {
	for _, point := range []string{"read", "update"} {
		for _, outcome := range []string{"pending2", "same_status2", "equivalent", "version_patch"} {
			t.Run(point+"/"+outcome, func(t *testing.T) {
				client := &refundDynamoClient{fakeCommerceClient: newFakeCommerceClient(), point: point, entered: make(chan struct{}, 1), resume: make(chan struct{})}
				recorder := &capturingRecorder{}
				store := NewDynamoStoreWithRecorder(client, testDynamoConfig(), recorder)
				old := refundTestOrder(t, store)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				done := make(chan error, 1)
				joined := false
				t.Cleanup(func() {
					cancel()
					if !joined {
						select {
						case <-done:
						case <-time.After(6 * time.Second):
							t.Error("worker did not exit")
						}
					}
				})
				go func() {
					_, err := store.TransitionOrder(context.WithValue(ctx, refundDynamoPauseKey{}, true), old.ID, old.Status, OrderStatusRefundFailed, OrderPatch{ExpectedVersion: &old.Version})
					done <- err
				}()
				select {
				case <-client.entered:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				current := old
				if outcome == "version_patch" {
					var err error
					current, err = store.PatchOrder(ctx, old.ID, old.Status, old.Version, OrderPatch{})
					if err != nil {
						t.Fatal(err)
					}
				} else {
					current = refundTestTransition(t, store, current, OrderStatusRefundFailed, OrderPatch{})
					if outcome != "equivalent" {
						id, attempt := "re_2", 2
						current = refundTestTransition(t, store, current, OrderStatusRefundPending, OrderPatch{StripeRefundID: &id, RefundAttempt: &attempt})
						if outcome == "same_status2" {
							current = refundTestTransition(t, store, current, OrderStatusRefundFailed, OrderPatch{})
						}
					}
				}
				metricsBefore := len(recorder.recorded())
				close(client.resume)
				select {
				case err := <-done:
					joined = true
					if !errors.Is(err, ErrOrderTransitionConflict) {
						t.Errorf("loser = %v", err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				refundTestUnchanged(t, store, current)
				if client.read == nil || !aws.ToBool(client.read.ConsistentRead) {
					t.Error("order reread must be strongly consistent")
				}
				if point == "update" {
					in := client.update
					if in == nil {
						t.Fatal("actual conditional update not intercepted")
					}
					if aws.ToString(in.ConditionExpression) != "#status = :from_status AND #version = :expected_version" || numberAttribute(in.ExpressionAttributeValues, ":expected_version") != int64(old.Version) || numberAttribute(in.ExpressionAttributeValues, ":new_version") != int64(old.Version+1) || stringAttribute(in.ExpressionAttributeValues, ":from_status") != string(old.Status) || in.ExpressionAttributeNames["#status"] != "status" || in.ExpressionAttributeNames["#version"] != "version" || in.ReturnValues != types.ReturnValueAllNew {
						t.Fatalf("incorrect atomic request: %+v", in)
					}
				}
				for _, m := range recorder.recorded()[metricsBefore:] {
					if m.Name == observability.MetricOrderTransition {
						t.Error("loser recorded transition metric")
					}
				}
			})
		}
	}
}

func TestRefundLegacyIdentityWithPresentVersion(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			old := refundTestOrder(t, fixture.store)
			id, attempt := "", 0
			legacy, err := fixture.store.PatchOrder(context.Background(), old.ID, old.Status, old.Version, OrderPatch{StripeRefundID: &id, RefundAttempt: &attempt})
			if err != nil {
				t.Fatal(err)
			}
			winner := refundTestTransition(t, fixture.store, legacy, OrderStatusRefunded, OrderPatch{ExpectedVersion: &legacy.Version})
			if winner.StripeRefundID != "" || winner.RefundAttempt != 0 || winner.Version != legacy.Version+1 || len(winner.StatusHistory) != len(legacy.StatusHistory)+1 {
				t.Fatal("legacy settlement changed identity or history incorrectly")
			}
		})
	}
}

func TestRefundDynamoOmittedIdentityWithPositiveVersion(t *testing.T) {
	client := newFakeCommerceClient()
	store := NewDynamoStoreWithClock(client, testDynamoConfig(), nil, func() time.Time { return testBaseTime })
	legacy, err := store.CreateOrder(context.Background(), Order{CustomerID: "customer", Status: OrderStatusRefundPending, TotalCents: 100, Currency: "usd"})
	if err != nil {
		t.Fatal(err)
	}
	item := client.itemForKey(t, orderPK(legacy.ID), orderSK)
	if _, found := item["stripe_refund_id"]; found {
		t.Fatal("legacy refund ID is not omitted")
	}
	if _, found := item["refund_attempt"]; found {
		t.Fatal("legacy refund attempt is not omitted")
	}
	if numberAttribute(item, "version") <= 0 {
		t.Fatal("normal version must be present and positive")
	}
	legacy, found, err := store.GetOrder(context.Background(), legacy.ID)
	if err != nil || !found {
		t.Fatalf("legacy read: found=%v error=%v", found, err)
	}
	winner := refundTestTransition(t, store, legacy, OrderStatusRefunded, OrderPatch{ExpectedVersion: &legacy.Version})
	if winner.StripeRefundID != "" || winner.RefundAttempt != 0 || winner.Version != legacy.Version+1 || len(winner.StatusHistory) != len(legacy.StatusHistory)+1 {
		t.Fatal("omitted-identity settlement changed identity/version/history incorrectly")
	}
}

func TestRefundDynamoLegacyVersionAndErrors(t *testing.T) {
	for _, value := range []string{"absent", "zero", "positive", "null", "malformed"} {
		t.Run(value, func(t *testing.T) {
			client := &refundDynamoClient{fakeCommerceClient: newFakeCommerceClient()}
			store := NewDynamoStore(client, testDynamoConfig())
			old := refundTestOrder(t, store)
			client.mu.Lock()
			item := client.items[orderPK(old.ID)+"\x00"+orderSK]
			delete(item, "refund_attempt")
			delete(item, "stripe_refund_id")
			switch value {
			case "absent":
				delete(item, "version")
			case "zero":
				item["version"] = numberValue(0)
			case "positive":
				item["version"] = numberValue(2)
			case "null":
				item["version"] = &types.AttributeValueMemberNULL{Value: true}
			case "malformed":
				item["version"] = &types.AttributeValueMemberN{Value: "bad"}
			}
			before := cloneAttributeMap(item)
			client.mu.Unlock()
			zero := 0
			_, err := store.TransitionOrder(context.WithValue(context.Background(), refundDynamoPauseKey{}, true), old.ID, old.Status, OrderStatusRefunded, OrderPatch{ExpectedVersion: &zero})
			if value == "absent" || value == "zero" {
				if err != nil {
					t.Fatal(err)
				}
				in := client.update
				want := "#status = :from_status AND #version = :expected_version OR #status = :from_status AND attribute_not_exists(#version)"
				if in == nil || aws.ToString(in.ConditionExpression) != want {
					t.Fatalf("zero CAS condition = %+v", in)
				}
				// Explicit truth table checks both arms' status binding and positive
				// version exclusion; this is not just fake integration success.
				for _, status := range []OrderStatus{old.Status, OrderStatusPaid} {
					for _, v := range []int{-1, 0, 1} {
						candidate := map[string]types.AttributeValue{"status": &types.AttributeValueMemberS{Value: string(status)}}
						if v >= 0 {
							candidate["version"] = numberValue(int64(v))
						}
						if got := conditionHolds(want, in.ExpressionAttributeNames, in.ExpressionAttributeValues, candidate); got != (status == old.Status && v <= 0) {
							t.Errorf("condition truth table: status=%s version=%d result=%v", status, v, got)
						}
					}
				}
				for k := range client.itemForKey(t, orderPK(old.ID), orderSK) {
					if strings.Contains(strings.ToLower(k), "expected") {
						t.Errorf("guard persisted as %s", k)
					}
				}
			} else {
				if err == nil {
					t.Fatal("invalid/nonzero version accepted zero guard")
				}
				if !reflect.DeepEqual(before, client.itemForKey(t, orderPK(old.ID), orderSK)) {
					t.Fatal("failed guard mutated item")
				}
			}
			// Positive expectation must never accept an absent legacy version.
			client.mu.Lock()
			delete(client.items[orderPK(old.ID)+"\x00"+orderSK], "version")
			client.mu.Unlock()
			one := 1
			if _, err := store.TransitionOrder(context.Background(), old.ID, old.Status, OrderStatusRefundFailed, OrderPatch{ExpectedVersion: &one}); !errors.Is(err, ErrOrderTransitionConflict) {
				t.Errorf("positive CAS against absence = %v", err)
			}
		})
	}
	for _, writeErr := range []error{errors.New("transport unavailable"), context.Canceled, &types.ConditionalCheckFailedException{}} {
		t.Run(writeErr.Error(), func(t *testing.T) {
			client := &refundDynamoClient{fakeCommerceClient: newFakeCommerceClient(), writeErr: writeErr}
			store := NewDynamoStore(client, testDynamoConfig())
			old := refundTestOrder(t, store)
			// Compare persisted bytes, including Dynamo's second-precision times.
			old, _, _ = store.GetOrder(context.Background(), old.ID)
			_, err := store.TransitionOrder(context.Background(), old.ID, old.Status, OrderStatusRefunded, OrderPatch{ExpectedVersion: &old.Version})
			if isConditionalCheckFailed(writeErr) {
				if !errors.Is(err, ErrOrderTransitionConflict) {
					t.Fatal(err)
				}
			} else if !errors.Is(err, writeErr) || errors.Is(err, ErrOrderTransitionConflict) {
				t.Fatalf("persistence error masked: %v", err)
			}
			refundTestUnchanged(t, store, old)
		})
	}
	t.Run("decode_failure", func(t *testing.T) {
		client := &refundDynamoClient{fakeCommerceClient: newFakeCommerceClient(), decodeFailure: true}
		store := NewDynamoStore(client, testDynamoConfig())
		old := refundTestOrder(t, store)
		if _, err := store.TransitionOrder(context.Background(), old.ID, old.Status, OrderStatusRefunded, OrderPatch{ExpectedVersion: &old.Version}); err == nil || errors.Is(err, ErrOrderTransitionConflict) {
			t.Fatalf("decode error masked: %v", err)
		}
	})
}
