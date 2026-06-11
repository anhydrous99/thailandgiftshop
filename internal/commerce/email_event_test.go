package commerce

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func testEmailEvent(orderID string, key string, now time.Time) EmailEvent {
	return EmailEvent{
		Key:          key,
		Kind:         "order_placed",
		OrderID:      orderID,
		OrderVersion: 2,
		To:           "shopper@example.test",
		Status:       "reserved",
		Attempts:     1,
		CreatedAt:    now,
	}
}

func TestEmailEventReserveReturnsTrueThenFalseForDuplicate(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			event := testEmailEvent("order123", "order-placed-v2", fixture.clock.Now())

			reserved, err := fixture.store.ReserveEmailEvent(ctx, event)
			if err != nil || !reserved {
				t.Fatalf("first ReserveEmailEvent = %v %v, want reserved", reserved, err)
			}
			reserved, err = fixture.store.ReserveEmailEvent(ctx, event)
			if err != nil || reserved {
				t.Fatalf("duplicate ReserveEmailEvent = %v %v, want duplicate rejected", reserved, err)
			}

			distinct := event
			distinct.Key = "status-paid-v2"
			reserved, err = fixture.store.ReserveEmailEvent(ctx, distinct)
			if err != nil || !reserved {
				t.Fatalf("distinct ReserveEmailEvent = %v %v, want reserved", reserved, err)
			}
		})
	}
}

func TestEmailEventSentAndFailedUpdateOnlyMatchingOrderEventRow(t *testing.T) {
	ctx := context.Background()
	client := newFakeCommerceClient()
	clock := newTestClock()
	store := NewDynamoStoreWithClock(client, testDynamoConfig(), nil, clock.Now)

	first := testEmailEvent("order123", "order-placed-v2", clock.Now())
	second := testEmailEvent("order456", "order-placed-v2", clock.Now())
	for _, event := range []EmailEvent{first, second} {
		reserved, err := store.ReserveEmailEvent(ctx, event)
		if err != nil || !reserved {
			t.Fatalf("ReserveEmailEvent(%s) = %v %v, want reserved", event.OrderID, reserved, err)
		}
	}

	sentAt := clock.Now().Add(5 * time.Minute)
	if err := store.MarkEmailEventSent(ctx, first.OrderID, first.Key, sentAt); err != nil {
		t.Fatalf("MarkEmailEventSent returned error: %v", err)
	}
	firstItem := client.itemForKey(t, orderPK(first.OrderID), emailEventSK(first.Key))
	secondItem := client.itemForKey(t, orderPK(second.OrderID), emailEventSK(second.Key))
	if got := stringAttribute(firstItem, "status"); got != "sent" {
		t.Fatalf("first status = %q, want sent", got)
	}
	if got := stringAttribute(firstItem, "sent_at"); got != "2026-06-08T12:05:00Z" {
		t.Fatalf("first sent_at = %q, want sent timestamp", got)
	}
	if got := stringAttribute(secondItem, "status"); got != "reserved" {
		t.Fatalf("second status = %q, want untouched reserved", got)
	}
	if _, found := secondItem["sent_at"]; found {
		t.Fatalf("second row was marked sent: %#v", secondItem["sent_at"])
	}

	failedAt := sentAt.Add(2 * time.Minute)
	if err := store.MarkEmailEventFailed(ctx, second.OrderID, second.Key, failedAt, "ses rejected recipient"); err != nil {
		t.Fatalf("MarkEmailEventFailed returned error: %v", err)
	}
	firstItem = client.itemForKey(t, orderPK(first.OrderID), emailEventSK(first.Key))
	secondItem = client.itemForKey(t, orderPK(second.OrderID), emailEventSK(second.Key))
	if got := stringAttribute(secondItem, "status"); got != "failed" {
		t.Fatalf("second status = %q, want failed", got)
	}
	if got := stringAttribute(secondItem, "failed_at"); got != "2026-06-08T12:07:00Z" {
		t.Fatalf("second failed_at = %q, want failed timestamp", got)
	}
	if got := stringAttribute(secondItem, "last_error"); got != "ses rejected recipient" {
		t.Fatalf("second last_error = %q, want reason", got)
	}
	if got := stringAttribute(firstItem, "status"); got != "sent" {
		t.Fatalf("first status after second failed = %q, want still sent", got)
	}
	if _, found := firstItem["failed_at"]; found {
		t.Fatalf("first row was marked failed: %#v", firstItem["failed_at"])
	}
}

func TestDynamoEmailEventItemShape(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	event := testEmailEvent("order123", "order-placed/v2?attempt=1", now)
	attrs, err := emailEventItem(event)
	if err != nil {
		t.Fatalf("emailEventItem returned error: %v", err)
	}

	for _, required := range []string{"pk", "sk", "entity_type", "event_key", "kind", "order_id", "order_version", "to", "status", "attempts", "created_at"} {
		if _, found := attrs[required]; !found {
			t.Fatalf("email event item is missing %q: %#v", required, attrs)
		}
	}
	for _, omitted := range []string{"gsi1pk", "gsi1sk", "gsi2pk", "gsi2sk", "body", "html_body", "text_body", "message", "reset_token", "guest_access_token", "url"} {
		if _, found := attrs[omitted]; found {
			t.Fatalf("email event item includes %q", omitted)
		}
	}
	if got := stringAttribute(attrs, "pk"); got != "ORDER#order123" {
		t.Fatalf("pk = %q, want ORDER#order123", got)
	}
	if got := stringAttribute(attrs, "sk"); got == "EMAIL_EVENT#order-placed/v2?attempt=1" || !strings.HasPrefix(got, "EMAIL_EVENT#") {
		t.Fatalf("sk = %q, want normalized EMAIL_EVENT# key", got)
	}
	if got := stringAttribute(attrs, "entity_type"); got != "EMAIL_EVENT" {
		t.Fatalf("entity_type = %q, want EMAIL_EVENT", got)
	}
	if got := stringAttribute(attrs, "event_key"); got != event.Key {
		t.Fatalf("event_key = %q, want original key", got)
	}
	if got := numberAttribute(attrs, "order_version"); got != int64(event.OrderVersion) {
		t.Fatalf("order_version = %d, want %d", got, event.OrderVersion)
	}
	roundTrip, err := emailEventFromItem(attrs)
	if err != nil {
		t.Fatalf("emailEventFromItem returned error: %v", err)
	}
	if roundTrip.Key != event.Key || roundTrip.Kind != event.Kind || roundTrip.OrderID != event.OrderID || roundTrip.OrderVersion != event.OrderVersion || !roundTrip.CreatedAt.Equal(event.CreatedAt) {
		t.Fatalf("emailEventFromItem = %#v, want event metadata", roundTrip)
	}
}

func TestEmailEventSensitiveContentExcluded(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	event := testEmailEvent("order123", "order-placed-v2", now)
	event.LastError = "temporary provider failure"
	attrs, err := emailEventItem(event)
	if err != nil {
		t.Fatalf("emailEventItem returned error: %v", err)
	}

	for _, forbidden := range []string{
		"<html", "Your order has shipped", "reset_token", "raw_token", "guest_access_token", "access=", "https://", "http://", "/checkout/confirm", "/account/reset-password",
	} {
		assertAttributeMapDoesNotContain(t, attrs, forbidden)
	}
	for _, forbiddenAttribute := range []string{"body", "html", "html_body", "text", "text_body", "full_message", "raw_url", "order_url", "reset_url", "access_url", "token", "reset_token", "guest_access_token"} {
		if _, found := attrs[forbiddenAttribute]; found {
			t.Fatalf("email event row contains forbidden attribute %q", forbiddenAttribute)
		}
	}
}

func assertAttributeMapDoesNotContain(t *testing.T, attrs map[string]types.AttributeValue, forbidden string) {
	t.Helper()
	for name, value := range attrs {
		if strings.Contains(attributeValueText(value), forbidden) {
			t.Fatalf("attribute %s contains forbidden %q: %#v", name, forbidden, value)
		}
	}
}

func attributeValueText(value types.AttributeValue) string {
	switch typed := value.(type) {
	case *types.AttributeValueMemberS:
		return typed.Value
	case *types.AttributeValueMemberN:
		return typed.Value
	case *types.AttributeValueMemberBOOL:
		if typed.Value {
			return "true"
		}
		return "false"
	case *types.AttributeValueMemberL:
		var builder strings.Builder
		for _, element := range typed.Value {
			builder.WriteString(attributeValueText(element))
		}
		return builder.String()
	case *types.AttributeValueMemberM:
		var builder strings.Builder
		for _, element := range typed.Value {
			builder.WriteString(attributeValueText(element))
		}
		return builder.String()
	default:
		return ""
	}
}
