package email

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"

	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
)

func TestFakeSenderCapturesListsAndClearsDeterministically(t *testing.T) {
	now := time.Date(2026, 6, 11, 10, 30, 0, 0, time.UTC)
	sender := NewFakeSenderWithClock(func() time.Time { return now })

	if sender.Kind() != SenderKindFake {
		t.Fatalf("Kind() = %q, want %q", sender.Kind(), SenderKindFake)
	}

	first, err := sender.Send(context.Background(), Message{
		To:       "customer@example.com",
		Subject:  "Your order was placed",
		Text:     "Order TGS-1001 was placed.",
		HTML:     "<p>Order TGS-1001 was placed.</p>",
		Kind:     MessageKindOrderPlaced,
		EventKey: "order:TGS-1001:placed",
	})
	if err != nil {
		t.Fatalf("Send first: %v", err)
	}
	second, err := sender.Send(context.Background(), Message{
		To:       "customer@example.com",
		Subject:  "Your order shipped",
		Text:     "Order TGS-1001 is shipped.",
		HTML:     "<p>Order TGS-1001 is shipped.</p>",
		Kind:     MessageKindOrderStatusChange,
		EventKey: "order:TGS-1001:shipped",
	})
	if err != nil {
		t.Fatalf("Send second: %v", err)
	}

	if first.ID != "email_fake_000001" || second.ID != "email_fake_000002" {
		t.Fatalf("captured IDs = %q, %q; want deterministic sequence", first.ID, second.ID)
	}
	if !first.CreatedAt.Equal(now) || !second.CreatedAt.Equal(now) {
		t.Fatalf("CreatedAt = %s, %s; want %s", first.CreatedAt, second.CreatedAt, now)
	}

	messages := sender.Messages()
	if len(messages) != 2 {
		t.Fatalf("Messages len = %d, want 2", len(messages))
	}
	if messages[0].ID != first.ID || messages[1].ID != second.ID {
		t.Fatalf("Messages order = %#v, want send order", messages)
	}
	messages[0].Subject = "mutated"
	if got := sender.Messages()[0].Subject; got != first.Subject {
		t.Fatalf("Messages returned mutable backing store, got subject %q", got)
	}

	sender.Clear()
	if got := sender.Messages(); len(got) != 0 {
		t.Fatalf("Messages after Clear len = %d, want 0", len(got))
	}
	afterClear, err := sender.Send(context.Background(), Message{
		To:      "customer@example.com",
		Subject: "Reset your password",
		Text:    "Reset text",
		HTML:    "<p>Reset text</p>",
		Kind:    MessageKindPasswordReset,
	})
	if err != nil {
		t.Fatalf("Send after Clear: %v", err)
	}
	if afterClear.ID != "email_fake_000001" {
		t.Fatalf("ID after Clear = %q, want reset deterministic sequence", afterClear.ID)
	}
}

func TestBuildersEmitNonEmptySubjectTextAndHTML(t *testing.T) {
	order := sampleOrder()
	tests := []struct {
		name    string
		message Message
	}{
		{"password reset", BuildPasswordReset("customer@example.com", "https://example.com/account/password-reset/confirm?token=abc")},
		{"order placed", BuildOrderPlaced(order, "https://example.com/orders/tgs-1001")},
		{"order status change", BuildOrderStatusChange(order, commerce.OrderStatusPaid, commerce.OrderStatusShipped, "https://example.com/orders/tgs-1001")},
		{"tracking update", BuildTrackingUpdate(order, "https://example.com/orders/tgs-1001")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if strings.TrimSpace(test.message.Subject) == "" {
				t.Fatal("Subject is empty")
			}
			if strings.TrimSpace(test.message.Text) == "" {
				t.Fatal("Text is empty")
			}
			if strings.TrimSpace(test.message.HTML) == "" {
				t.Fatal("HTML is empty")
			}
		})
	}
}

func TestPasswordResetBuilderCopyIsMinimalAndDoesNotLeakInternals(t *testing.T) {
	resetLink := "https://example.com/account/password-reset/confirm?token=reset-token"
	message := BuildPasswordReset("customer@example.com", resetLink)
	combined := message.Subject + "\n" + message.Text + "\n" + message.HTML

	if message.To != "customer@example.com" {
		t.Fatalf("To = %q, want customer email", message.To)
	}
	if message.Kind != MessageKindPasswordReset {
		t.Fatalf("Kind = %q, want %q", message.Kind, MessageKindPasswordReset)
	}
	if !strings.Contains(combined, "30 minutes") {
		t.Fatalf("password reset copy = %q, want 30 minutes", combined)
	}
	if !strings.Contains(message.Text, resetLink) || !strings.Contains(message.HTML, resetLink) {
		t.Fatalf("password reset bodies must include only the supplied reset link")
	}
	for _, forbidden := range []string{"password_hash", "PasswordHash", "bcrypt", "session", "token hash", "token_hash"} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("password reset copy leaked %q in %q", forbidden, combined)
		}
	}
}

func TestOrderBuildersUseOrderEmailIDStatusAndTracking(t *testing.T) {
	order := sampleOrder()
	orderURL := "https://example.com/orders/tgs-1001"

	placed := BuildOrderPlaced(order, orderURL)
	assertOrderMessage(t, placed, MessageKindOrderPlaced, order.Email, order.ID, string(order.Status), orderURL)

	status := BuildOrderStatusChange(order, commerce.OrderStatusPaid, commerce.OrderStatusShipped, orderURL)
	assertOrderMessage(t, status, MessageKindOrderStatusChange, order.Email, order.ID, string(commerce.OrderStatusPaid), orderURL)
	assertOrderMessage(t, status, MessageKindOrderStatusChange, order.Email, order.ID, string(commerce.OrderStatusShipped), orderURL)

	tracking := BuildTrackingUpdate(order, orderURL)
	assertOrderMessage(t, tracking, MessageKindTrackingUpdate, order.Email, order.ID, order.TrackingCarrier, orderURL)
	assertOrderMessage(t, tracking, MessageKindTrackingUpdate, order.Email, order.ID, order.TrackingNumber, orderURL)

	order.TrackingCarrier = ""
	order.TrackingNumber = ""
	withoutTracking := BuildTrackingUpdate(order, orderURL)
	assertOrderMessage(t, withoutTracking, MessageKindTrackingUpdate, order.Email, order.ID, string(order.Status), orderURL)
	if strings.Contains(withoutTracking.Text+withoutTracking.HTML, "TRACK") {
		t.Fatalf("tracking message without data included stale tracking number: %#v", withoutTracking)
	}
}

func TestConfigSelectsSESSenderInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv(EnvSenderMode, SenderKindSES)
	t.Setenv(EnvFromAddress, DefaultFromAddress)
	t.Setenv(EnvSESRegion, "us-east-1")

	config, err := SenderConfigFromEnvironment()
	if err != nil {
		t.Fatalf("SenderConfigFromEnvironment: %v", err)
	}
	if config.Mode != SenderKindSES {
		t.Fatalf("Mode = %q, want %q", config.Mode, SenderKindSES)
	}
	if config.FromAddress != DefaultFromAddress {
		t.Fatalf("FromAddress = %q, want %q", config.FromAddress, DefaultFromAddress)
	}
	if config.SESRegion != "us-east-1" {
		t.Fatalf("SESRegion = %q, want us-east-1", config.SESRegion)
	}
}

func TestConfigRejectsProductionFakeSender(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv(EnvSenderMode, SenderKindFake)
	t.Setenv(EnvFromAddress, DefaultFromAddress)
	t.Setenv(EnvSESRegion, "us-east-1")

	if _, err := SenderConfigFromEnvironment(); err != ErrFakeSenderNotAllowedInProduction {
		t.Fatalf("SenderConfigFromEnvironment error = %v, want %v", err, ErrFakeSenderNotAllowedInProduction)
	}
}

func TestConfigRejectsProductionMissingSenderConfiguration(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"no mode", map[string]string{}},
		{"ses without from", map[string]string{EnvSenderMode: SenderKindSES, EnvSESRegion: "us-east-1"}},
		{"ses without region", map[string]string{EnvSenderMode: SenderKindSES, EnvFromAddress: DefaultFromAddress}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("APP_ENV", "production")
			for key, value := range test.env {
				t.Setenv(key, value)
			}
			if _, err := SenderConfigFromEnvironment(); err != ErrEmailSenderNotConfigured {
				t.Fatalf("SenderConfigFromEnvironment error = %v, want %v", err, ErrEmailSenderNotConfigured)
			}
		})
	}
}

func TestConfigRejectsProductionWrongFromAddress(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv(EnvSenderMode, SenderKindSES)
	t.Setenv(EnvFromAddress, "orders@thailandgiftshop.com")
	t.Setenv(EnvSESRegion, "us-east-1")

	if _, err := SenderConfigFromEnvironment(); err != ErrEmailSenderNotConfigured {
		t.Fatalf("SenderConfigFromEnvironment error = %v, want %v", err, ErrEmailSenderNotConfigured)
	}
}

func TestSESSenderMapsMessageToSimpleEmail(t *testing.T) {
	client := &captureSESClient{}
	sender := NewSESSender(client, DefaultFromAddress)
	message := Message{
		To:      "customer@example.com",
		Subject: "Your order was placed",
		Text:    "Order TGS-1001 was placed.",
		HTML:    "<p>Order TGS-1001 was placed.</p>",
		Kind:    MessageKindOrderPlaced,
	}

	sent, err := sender.Send(context.Background(), message)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if sent.ID != "ses-message-123" {
		t.Fatalf("sent ID = %q, want SES message ID", sent.ID)
	}
	input := client.input
	if input == nil {
		t.Fatal("SendEmail was not called")
	}
	if got := value(input.FromEmailAddress); got != DefaultFromAddress {
		t.Fatalf("FromEmailAddress = %q, want %q", got, DefaultFromAddress)
	}
	if len(input.Destination.ToAddresses) != 1 || input.Destination.ToAddresses[0] != message.To {
		t.Fatalf("ToAddresses = %#v, want %q", input.Destination.ToAddresses, message.To)
	}
	if got := value(input.Content.Simple.Subject.Data); got != message.Subject {
		t.Fatalf("subject = %q, want %q", got, message.Subject)
	}
	if got := value(input.Content.Simple.Body.Text.Data); got != message.Text {
		t.Fatalf("text = %q, want %q", got, message.Text)
	}
	if got := value(input.Content.Simple.Body.Html.Data); got != message.HTML {
		t.Fatalf("html = %q, want %q", got, message.HTML)
	}
}

func assertOrderMessage(t *testing.T, message Message, kind string, email string, orderID string, want string, orderURL string) {
	t.Helper()
	if message.To != email {
		t.Fatalf("To = %q, want %q", message.To, email)
	}
	if message.Kind != kind {
		t.Fatalf("Kind = %q, want %q", message.Kind, kind)
	}
	combined := message.Subject + "\n" + message.Text + "\n" + message.HTML
	for _, part := range []string{orderID, want} {
		if !strings.Contains(combined, part) {
			t.Fatalf("message %q missing %q", combined, part)
		}
	}
	if !strings.Contains(message.Text, orderURL) || !strings.Contains(message.HTML, orderURL) {
		t.Fatalf("message bodies must include supplied order URL")
	}
}

func sampleOrder() commerce.Order {
	return commerce.Order{
		ID:              "TGS-1001",
		Email:           "customer@example.com",
		Status:          commerce.OrderStatusShipped,
		TrackingCarrier: "Thailand Post",
		TrackingNumber:  "TRACK123456",
	}
}

type captureSESClient struct {
	input *sesv2.SendEmailInput
}

func (c *captureSESClient) SendEmail(ctx context.Context, input *sesv2.SendEmailInput, optFns ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
	_ = ctx
	_ = optFns
	c.input = input
	messageID := "ses-message-123"
	return &sesv2.SendEmailOutput{MessageId: &messageID}, nil
}

func value(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
