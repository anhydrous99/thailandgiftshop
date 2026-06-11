package payments

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	stripe "github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/webhook"
)

const webhookSignatureTolerance = 5 * time.Minute

const checkoutSessionEventPrefix = "checkout.session."

const refundEventPrefix = "refund."

var ErrStripeCredentialsNotConfigured = errors.New("stripe credentials not configured")

type StripeCredentials struct {
	SecretKey            string
	WebhookSigningSecret string
}

type stripeSecretCredentials struct {
	SecretKey            string `json:"secret_key"`
	WebhookSigningSecret string `json:"webhook_signing_secret"`
}

type secretsManagerGetValueAPI interface {
	GetSecretValue(ctx context.Context, params *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

// StripeCredentialsFromEnvironment parses inline credentials for local runs,
// or loads the named Secrets Manager JSON secret used by production Lambda.
func StripeCredentialsFromEnvironment(ctx context.Context) (StripeCredentials, error) {
	secretJSON := strings.TrimSpace(os.Getenv(EnvStripeCredentialsSecretJSON))
	if secretJSON != "" {
		return stripeCredentialsFromSecretString(secretJSON)
	}

	secretName := strings.TrimSpace(os.Getenv(EnvStripeCredentialsSecretName))
	if secretName == "" {
		return StripeCredentials{}, ErrStripeCredentialsNotConfigured
	}

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return StripeCredentials{}, fmt.Errorf("load AWS config for Stripe credentials: %w", err)
	}

	return stripeCredentialsFromSecretManager(ctx, secretsmanager.NewFromConfig(cfg), secretName)
}

func stripeCredentialsFromSecretManager(ctx context.Context, client secretsManagerGetValueAPI, secretName string) (StripeCredentials, error) {
	secretName = strings.TrimSpace(secretName)
	if secretName == "" {
		return StripeCredentials{}, ErrStripeCredentialsNotConfigured
	}

	output, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &secretName})
	if err != nil {
		return StripeCredentials{}, fmt.Errorf("get Stripe credentials secret %q: %w", secretName, err)
	}
	if output.SecretString == nil {
		return StripeCredentials{}, ErrStripeCredentialsNotConfigured
	}

	return stripeCredentialsFromSecretString(*output.SecretString)
}

func stripeCredentialsFromSecretString(secretString string) (StripeCredentials, error) {
	var parsed stripeSecretCredentials
	if err := json.Unmarshal([]byte(secretString), &parsed); err != nil {
		return StripeCredentials{}, err
	}
	secretKey := strings.TrimSpace(parsed.SecretKey)
	webhookSigningSecret := strings.TrimSpace(parsed.WebhookSigningSecret)
	if secretKey == "" || webhookSigningSecret == "" {
		return StripeCredentials{}, ErrStripeCredentialsNotConfigured
	}

	return StripeCredentials{SecretKey: secretKey, WebhookSigningSecret: webhookSigningSecret}, nil
}

// StripeProvider talks to Stripe through a client bound to the secret key;
// the package-global stripe key is never mutated.
type StripeProvider struct {
	client               *stripe.Client
	webhookSigningSecret string
}

var _ Provider = (*StripeProvider)(nil)

func NewStripeProvider(credentials StripeCredentials) *StripeProvider {
	return &StripeProvider{
		client:               stripe.NewClient(credentials.SecretKey),
		webhookSigningSecret: credentials.WebhookSigningSecret,
	}
}

func NewStripeProviderFromEnvironment(ctx context.Context) (*StripeProvider, error) {
	credentials, err := StripeCredentialsFromEnvironment(ctx)
	if err != nil {
		return nil, err
	}

	return NewStripeProvider(credentials), nil
}

func (p *StripeProvider) Kind() string {
	return KindStripe
}

func (p *StripeProvider) EnsureCustomer(ctx context.Context, customerID string, email string) (string, error) {
	params := &stripe.CustomerCreateParams{
		Email:    stripe.String(email),
		Metadata: map[string]string{"customer_id": customerID},
	}
	params.SetIdempotencyKey("cust-" + customerID)

	customer, err := p.client.V1Customers.Create(ctx, params)
	if err != nil {
		return "", fmt.Errorf("create stripe customer: %w", err)
	}

	return customer.ID, nil
}

func (p *StripeProvider) CreatePaymentSession(ctx context.Context, input PaymentSessionInput) (Session, error) {
	checkoutSession, err := p.client.V1CheckoutSessions.Create(ctx, checkoutSessionCreateParams(input))
	if err != nil {
		return Session{}, fmt.Errorf("create stripe checkout session: %w", err)
	}

	return sessionFromCheckoutSession(checkoutSession), nil
}

// checkoutSessionCreateParams builds the hosted Checkout parameters. Line
// items come exclusively from the server-side order snapshot, never from
// client input, so the amount Stripe charges is the amount the order froze.
func checkoutSessionCreateParams(input PaymentSessionInput) *stripe.CheckoutSessionCreateParams {
	params := &stripe.CheckoutSessionCreateParams{
		Mode:              stripe.String(string(stripe.CheckoutSessionModePayment)),
		ClientReferenceID: stripe.String(input.OrderID),
		Customer:          stripe.String(input.StripeCustomerID),
		Metadata: map[string]string{
			"order_id":    input.OrderID,
			"customer_id": input.CustomerID,
		},
		SavedPaymentMethodOptions: &stripe.CheckoutSessionCreateSavedPaymentMethodOptionsParams{
			PaymentMethodSave: stripe.String("enabled"),
		},
		ExpiresAt:  stripe.Int64(input.ExpiresAt.Unix()),
		SuccessURL: stripe.String(input.SuccessURL),
		CancelURL:  stripe.String(input.CancelURL),
	}
	for _, line := range input.Lines {
		params.LineItems = append(params.LineItems, &stripe.CheckoutSessionCreateLineItemParams{
			Quantity: stripe.Int64(int64(line.Quantity)),
			PriceData: &stripe.CheckoutSessionCreateLineItemPriceDataParams{
				Currency:   stripe.String("usd"),
				UnitAmount: stripe.Int64(int64(line.UnitAmountCents)),
				ProductData: &stripe.CheckoutSessionCreateLineItemPriceDataProductDataParams{
					Name: stripe.String(line.Name),
				},
			},
		})
	}
	params.SetIdempotencyKey(paymentSessionIdempotencyKey(input.OrderID, input.Attempt))

	return params
}

func paymentSessionIdempotencyKey(orderID string, attempt int) string {
	return fmt.Sprintf("order-session-%s-%d", orderID, attempt)
}

func (p *StripeProvider) CreateSetupSession(ctx context.Context, input SetupSessionInput) (Session, error) {
	nonce, err := randomHexNonce()
	if err != nil {
		return Session{}, err
	}
	params := &stripe.CheckoutSessionCreateParams{
		Mode:       stripe.String(string(stripe.CheckoutSessionModeSetup)),
		Customer:   stripe.String(input.StripeCustomerID),
		Metadata:   map[string]string{"customer_id": input.CustomerID},
		SuccessURL: stripe.String(input.SuccessURL),
		CancelURL:  stripe.String(input.CancelURL),
	}
	params.SetIdempotencyKey("setup-" + input.CustomerID + "-" + nonce)

	checkoutSession, err := p.client.V1CheckoutSessions.Create(ctx, params)
	if err != nil {
		return Session{}, fmt.Errorf("create stripe setup session: %w", err)
	}

	return sessionFromCheckoutSession(checkoutSession), nil
}

// GetSession retrieves the session server-side with the payment method
// expanded so paid sessions carry the card brand/last4 display strings.
func (p *StripeProvider) GetSession(ctx context.Context, sessionID string) (Session, error) {
	params := &stripe.CheckoutSessionRetrieveParams{}
	params.AddExpand("payment_intent.payment_method")

	checkoutSession, err := p.client.V1CheckoutSessions.Retrieve(ctx, sessionID, params)
	if err != nil {
		if isStripeNotFound(err) {
			return Session{}, ErrSessionNotFound
		}
		return Session{}, fmt.Errorf("retrieve stripe checkout session: %w", err)
	}

	return sessionFromCheckoutSession(checkoutSession), nil
}

// ExpireSession expires an open hosted checkout session so the shopper can
// no longer pay it (canceled orders must not stay payable). Stripe refuses to
// expire sessions that are not open; that error is returned wrapped and the
// checkout layer classifies it by re-reading the session.
func (p *StripeProvider) ExpireSession(ctx context.Context, sessionID string) error {
	if _, err := p.client.V1CheckoutSessions.Expire(ctx, sessionID, &stripe.CheckoutSessionExpireParams{}); err != nil {
		if isStripeNotFound(err) {
			return ErrSessionNotFound
		}
		return fmt.Errorf("expire stripe checkout session: %w", err)
	}

	return nil
}

// CreateRefund issues a full refund of the payment intent's remaining charge.
// The idempotency key dedupes double-clicks per (order, attempt) within
// Stripe's retention window (~24h); beyond it, a retried create against an
// already-fully-refunded charge maps to ErrChargeAlreadyRefunded.
func (p *StripeProvider) CreateRefund(ctx context.Context, input RefundInput) (Refund, error) {
	refund, err := p.client.V1Refunds.Create(ctx, refundCreateParams(input))
	if err != nil {
		// Post-idempotency-window retry against an already-refunded charge
		// (Stripe purges idempotency keys after ~24h): surface the dedicated
		// sentinel so callers stop telling the admin to retry.
		if isStripeChargeAlreadyRefunded(err) {
			return Refund{}, fmt.Errorf("%w: payment intent %s", ErrChargeAlreadyRefunded, input.PaymentIntentID)
		}
		return Refund{}, fmt.Errorf("create stripe refund: %w", err)
	}

	return refundFromStripeRefund(refund), nil
}

// GetRefund retrieves a refund for reconciliation; unknown IDs map to
// ErrRefundNotFound.
func (p *StripeProvider) GetRefund(ctx context.Context, refundID string) (Refund, error) {
	refund, err := p.client.V1Refunds.Retrieve(ctx, refundID, &stripe.RefundRetrieveParams{})
	if err != nil {
		if isStripeNotFound(err) {
			return Refund{}, ErrRefundNotFound
		}
		return Refund{}, fmt.Errorf("retrieve stripe refund: %w", err)
	}

	return refundFromStripeRefund(refund), nil
}

// refundCreateParams builds the refund parameters: metadata.order_id routes
// the refund.* webhooks back to the order, metadata.attempt lets the webhook
// heal distinguish a newer retry from a stale one, and Amount stays nil so
// Stripe refunds the full remaining charge.
func refundCreateParams(input RefundInput) *stripe.RefundCreateParams {
	params := &stripe.RefundCreateParams{
		PaymentIntent: stripe.String(input.PaymentIntentID),
		Reason:        stripe.String(string(stripe.RefundReasonRequestedByCustomer)),
		Metadata: map[string]string{
			"order_id": input.OrderID,
			"attempt":  strconv.Itoa(input.Attempt),
		},
	}
	params.SetIdempotencyKey(refundIdempotencyKey(input.OrderID, input.Attempt))

	return params
}

func refundIdempotencyKey(orderID string, attempt int) string {
	return fmt.Sprintf("order-refund-%s-%d", orderID, attempt)
}

func refundFromStripeRefund(refund *stripe.Refund) Refund {
	out := Refund{
		ID:            refund.ID,
		OrderID:       refund.Metadata["order_id"],
		Status:        string(refund.Status),
		AmountCents:   int(refund.Amount),
		FailureReason: string(refund.FailureReason),
	}
	// Absent/garbage metadata (dashboard-issued refunds) parses to 0.
	if attempt, err := strconv.Atoi(refund.Metadata["attempt"]); err == nil && attempt > 0 {
		out.Attempt = attempt
	}
	// Unexpanded webhook payloads carry payment_intent as a bare ID;
	// stripe.Refund's custom UnmarshalJSON still populates PaymentIntent.ID,
	// so this works for both expanded API responses and raw event payloads.
	if refund.PaymentIntent != nil {
		out.PaymentIntentID = refund.PaymentIntent.ID
	}

	return out
}

func (p *StripeProvider) ListPaymentMethods(ctx context.Context, stripeCustomerID string) ([]PaymentMethod, error) {
	listParams := &stripe.PaymentMethodListParams{
		Customer: stripe.String(stripeCustomerID),
		Type:     stripe.String(string(stripe.PaymentMethodTypeCard)),
	}

	paymentMethods := []PaymentMethod{}
	for paymentMethod, err := range p.client.V1PaymentMethods.List(ctx, listParams) {
		if err != nil {
			return nil, fmt.Errorf("list stripe payment methods: %w", err)
		}
		if paymentMethod.Card == nil {
			continue
		}
		paymentMethods = append(paymentMethods, PaymentMethod{
			ID:       paymentMethod.ID,
			Brand:    string(paymentMethod.Card.Brand),
			Last4:    paymentMethod.Card.Last4,
			ExpMonth: int(paymentMethod.Card.ExpMonth),
			ExpYear:  int(paymentMethod.Card.ExpYear),
		})
	}

	return paymentMethods, nil
}

// DetachPaymentMethod verifies the payment method belongs to the given
// Stripe customer before detaching; foreign or unknown payment methods
// surface as ErrPaymentMethodNotFound so handlers can answer 404.
func (p *StripeProvider) DetachPaymentMethod(ctx context.Context, stripeCustomerID string, paymentMethodID string) error {
	paymentMethod, err := p.client.V1PaymentMethods.Retrieve(ctx, paymentMethodID, &stripe.PaymentMethodRetrieveParams{})
	if err != nil {
		if isStripeNotFound(err) {
			return ErrPaymentMethodNotFound
		}
		return fmt.Errorf("retrieve stripe payment method: %w", err)
	}
	if paymentMethod.Customer == nil || paymentMethod.Customer.ID != stripeCustomerID {
		return ErrPaymentMethodNotFound
	}

	if _, err := p.client.V1PaymentMethods.Detach(ctx, paymentMethodID, &stripe.PaymentMethodDetachParams{}); err != nil {
		return fmt.Errorf("detach stripe payment method: %w", err)
	}

	return nil
}

func (p *StripeProvider) ParseWebhook(payload []byte, signatureHeader string, now time.Time) (Event, error) {
	return parseStripeSignedWebhook(payload, signatureHeader, p.webhookSigningSecret, now)
}

// parseStripeSignedWebhook verifies a Stripe-Signature header over the exact
// raw payload bytes and maps the event. stripe-go's webhook.ConstructEvent
// variants compare the signed timestamp against the wall clock, so the
// staleness window is enforced here against the injected now and the
// constant-time signature comparison is delegated to
// webhook.ConstructEventWithOptions with tolerance checking disabled.
// IgnoreAPIVersionMismatch keeps verification independent of the API version
// pinned on the webhook endpoint: the handful of fields read here (ids,
// type, metadata, payment_status, amount_total, mode) are stable across
// versions, and a version drift must not silently drop paid orders.
func parseStripeSignedWebhook(payload []byte, signatureHeader string, signingSecret string, now time.Time) (Event, error) {
	timestamp, err := webhookSignatureTimestamp(signatureHeader)
	if err != nil {
		return Event{}, err
	}
	if now.Sub(timestamp) > webhookSignatureTolerance {
		return Event{}, webhook.ErrTooOld
	}

	stripeEvent, err := webhook.ConstructEventWithOptions(payload, signatureHeader, signingSecret, webhook.ConstructEventOptions{
		IgnoreTolerance:          true,
		IgnoreAPIVersionMismatch: true,
	})
	if err != nil {
		return Event{}, err
	}

	return eventFromStripeEvent(stripeEvent)
}

// webhookSignatureTimestamp extracts the t= component of a Stripe-Signature
// header, mirroring stripe-go's parser ("t=1495999758,v1=ABC,...").
func webhookSignatureTimestamp(signatureHeader string) (time.Time, error) {
	if signatureHeader == "" {
		return time.Time{}, webhook.ErrNotSigned
	}
	for _, pair := range strings.Split(signatureHeader, ",") {
		name, value, ok := strings.Cut(pair, "=")
		if !ok || name != "t" {
			continue
		}
		seconds, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return time.Time{}, webhook.ErrInvalidHeader
		}
		return time.Unix(seconds, 0), nil
	}

	return time.Time{}, webhook.ErrInvalidHeader
}

func eventFromStripeEvent(stripeEvent stripe.Event) (Event, error) {
	event := Event{ID: stripeEvent.ID, Type: string(stripeEvent.Type)}
	if strings.HasPrefix(event.Type, refundEventPrefix) {
		if stripeEvent.Data == nil || len(stripeEvent.Data.Raw) == 0 {
			return Event{}, fmt.Errorf("stripe event %s has no refund payload", stripeEvent.ID)
		}
		var stripeRefund stripe.Refund
		if err := json.Unmarshal(stripeEvent.Data.Raw, &stripeRefund); err != nil {
			return Event{}, fmt.Errorf("decode stripe event %s refund payload: %w", stripeEvent.ID, err)
		}
		event.Refund = refundFromStripeRefund(&stripeRefund)
		event.OrderID = event.Refund.OrderID
		return event, nil
	}
	if !strings.HasPrefix(event.Type, checkoutSessionEventPrefix) {
		return event, nil
	}
	if stripeEvent.Data == nil || len(stripeEvent.Data.Raw) == 0 {
		return Event{}, fmt.Errorf("stripe event %s has no session payload", stripeEvent.ID)
	}

	var checkoutSession stripe.CheckoutSession
	if err := json.Unmarshal(stripeEvent.Data.Raw, &checkoutSession); err != nil {
		return Event{}, fmt.Errorf("decode stripe event %s session payload: %w", stripeEvent.ID, err)
	}
	session := sessionFromCheckoutSession(&checkoutSession)
	event.SessionID = session.ID
	event.OrderID = session.OrderID
	event.Session = session

	return event, nil
}

func sessionFromCheckoutSession(checkoutSession *stripe.CheckoutSession) Session {
	session := Session{
		ID:               checkoutSession.ID,
		URL:              checkoutSession.URL,
		OrderID:          checkoutSession.Metadata["order_id"],
		PaymentStatus:    string(checkoutSession.PaymentStatus),
		Status:           string(checkoutSession.Status),
		AmountTotalCents: int(checkoutSession.AmountTotal),
		Mode:             string(checkoutSession.Mode),
	}
	if session.OrderID == "" {
		session.OrderID = checkoutSession.ClientReferenceID
	}
	if paymentIntent := checkoutSession.PaymentIntent; paymentIntent != nil {
		session.PaymentIntentID = paymentIntent.ID
		if paymentMethod := paymentIntent.PaymentMethod; paymentMethod != nil && paymentMethod.Card != nil {
			session.CardBrand = string(paymentMethod.Card.Brand)
			session.CardLast4 = paymentMethod.Card.Last4
		}
	}

	return session
}

func isStripeNotFound(err error) bool {
	var stripeError *stripe.Error
	return errors.As(err, &stripeError) && stripeError.HTTPStatusCode == http.StatusNotFound
}

func isStripeChargeAlreadyRefunded(err error) bool {
	var stripeError *stripe.Error
	return errors.As(err, &stripeError) && stripeError.Code == stripe.ErrorCodeChargeAlreadyRefunded
}

func randomHexNonce() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}

	return hex.EncodeToString(buffer), nil
}
