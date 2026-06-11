package email

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/appenv"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

const (
	EnvSenderMode  = "EMAIL_SENDER_MODE"
	EnvFromAddress = "EMAIL_FROM_ADDRESS"
	EnvSESRegion   = "EMAIL_SES_REGION"

	DefaultFromAddress = "noreply@thailandgiftshop.com"

	SenderKindFake = "fake"
	SenderKindSES  = "ses"
)

var (
	ErrEmailSenderNotConfigured         = errors.New("email sender not configured")
	ErrFakeSenderNotAllowedInProduction = errors.New("fake email sender is not allowed in production")
)

type SenderConfig struct {
	Mode        string
	FromAddress string
	SESRegion   string
}

func SenderConfigFromEnvironment() (SenderConfig, error) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv(EnvSenderMode)))
	fromAddress := strings.TrimSpace(os.Getenv(EnvFromAddress))
	sesRegion := strings.TrimSpace(os.Getenv(EnvSESRegion))

	switch mode {
	case SenderKindSES:
		if err := validateSESConfig(fromAddress, sesRegion); err != nil {
			return SenderConfig{}, err
		}
		return SenderConfig{Mode: SenderKindSES, FromAddress: fromAddress, SESRegion: sesRegion}, nil
	case SenderKindFake:
		if appenv.IsProduction() {
			return SenderConfig{}, ErrFakeSenderNotAllowedInProduction
		}
		return SenderConfig{Mode: SenderKindFake}, nil
	case "":
		if fromAddress != "" || sesRegion != "" {
			if err := validateSESConfig(fromAddress, sesRegion); err != nil {
				return SenderConfig{}, err
			}
			return SenderConfig{Mode: SenderKindSES, FromAddress: fromAddress, SESRegion: sesRegion}, nil
		}
		if appenv.IsProduction() {
			return SenderConfig{}, ErrEmailSenderNotConfigured
		}
		return SenderConfig{Mode: SenderKindFake}, nil
	default:
		return SenderConfig{}, fmt.Errorf("unsupported %s value %q", EnvSenderMode, mode)
	}
}

func validateSESConfig(fromAddress string, sesRegion string) error {
	if fromAddress != DefaultFromAddress || sesRegion == "" {
		return ErrEmailSenderNotConfigured
	}
	return nil
}

func NewSenderFromEnvironment(ctx context.Context) (Sender, error) {
	senderConfig, err := SenderConfigFromEnvironment()
	if err != nil {
		return nil, err
	}
	if senderConfig.Mode == SenderKindFake {
		if sender := sharedFakeSenderForEnvironment(); sender != nil {
			return sender, nil
		}
		return NewFakeSender(), nil
	}

	awsConfig, err := config.LoadDefaultConfig(ctx, config.WithRegion(senderConfig.SESRegion))
	if err != nil {
		return nil, fmt.Errorf("load AWS config for SES sender: %w", err)
	}
	return NewSESSenderFromConfig(awsConfig, senderConfig.FromAddress), nil
}

type sesSendEmailAPI interface {
	SendEmail(ctx context.Context, input *sesv2.SendEmailInput, optFns ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error)
}

type SESSender struct {
	client      sesSendEmailAPI
	fromAddress string
}

var _ Sender = (*SESSender)(nil)

func NewSESSenderFromConfig(awsConfig aws.Config, fromAddress string) *SESSender {
	return NewSESSender(sesv2.NewFromConfig(awsConfig), fromAddress)
}

func NewSESSender(client sesSendEmailAPI, fromAddress string) *SESSender {
	return &SESSender{client: client, fromAddress: strings.TrimSpace(fromAddress)}
}

func (s *SESSender) Kind() string {
	return SenderKindSES
}

func (s *SESSender) Send(ctx context.Context, message Message) (Message, error) {
	output, err := s.client.SendEmail(ctx, &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(s.fromAddress),
		Destination: &types.Destination{
			ToAddresses: []string{strings.TrimSpace(message.To)},
		},
		Content: &types.EmailContent{
			Simple: &types.Message{
				Subject: &types.Content{Data: aws.String(message.Subject)},
				Body: &types.Body{
					Text: &types.Content{Data: aws.String(message.Text)},
					Html: &types.Content{Data: aws.String(message.HTML)},
				},
			},
		},
	})
	if err != nil {
		return Message{}, fmt.Errorf("send SES email: %w", err)
	}
	if output != nil && output.MessageId != nil {
		message.ID = *output.MessageId
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = time.Now().UTC()
	}
	return message, nil
}

const (
	MessageKindPasswordReset     = "password_reset"
	MessageKindOrderPlaced       = "order_placed"
	MessageKindOrderStatusChange = "order_status_change"
	MessageKindTrackingUpdate    = "tracking_update"
)

type Message struct {
	ID        string
	To        string
	Subject   string
	Text      string
	HTML      string
	Kind      string
	EventKey  string
	CreatedAt time.Time
}

type Sender interface {
	Kind() string
	Send(ctx context.Context, message Message) (Message, error)
}

type FakeSender struct {
	mu       sync.Mutex
	now      func() time.Time
	nextID   int
	messages []Message
}

var fakeSenderForEnvironment struct {
	mu     sync.RWMutex
	sender *FakeSender
}

var _ Sender = (*FakeSender)(nil)

func NewFakeSender() *FakeSender {
	return NewFakeSenderWithClock(time.Now)
}

func NewFakeSenderWithClock(now func() time.Time) *FakeSender {
	return &FakeSender{now: now}
}

func UseFakeSenderForEnvironment(sender *FakeSender) {
	fakeSenderForEnvironment.mu.Lock()
	defer fakeSenderForEnvironment.mu.Unlock()

	fakeSenderForEnvironment.sender = sender
}

func sharedFakeSenderForEnvironment() *FakeSender {
	fakeSenderForEnvironment.mu.RLock()
	defer fakeSenderForEnvironment.mu.RUnlock()

	return fakeSenderForEnvironment.sender
}

func (s *FakeSender) Kind() string {
	return SenderKindFake
}

func (s *FakeSender) Send(ctx context.Context, message Message) (Message, error) {
	_ = ctx

	s.mu.Lock()
	defer s.mu.Unlock()

	if message.ID == "" {
		s.nextID++
		message.ID = fmt.Sprintf("email_fake_%06d", s.nextID)
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = s.clock()
	}
	s.messages = append(s.messages, message)

	return message, nil
}

func (s *FakeSender) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()

	return cloneMessages(s.messages)
}

func (s *FakeSender) List() []Message {
	return s.Messages()
}

func (s *FakeSender) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID = 0
	s.messages = nil
}

func (s *FakeSender) ClearRecipient(recipient string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	recipient = strings.TrimSpace(recipient)
	if recipient == "" {
		return
	}
	kept := s.messages[:0]
	for _, message := range s.messages {
		if strings.TrimSpace(message.To) != recipient {
			kept = append(kept, message)
		}
	}
	s.messages = kept
}

func (s *FakeSender) clock() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

func cloneMessages(messages []Message) []Message {
	cloned := make([]Message, len(messages))
	copy(cloned, messages)
	return cloned
}
