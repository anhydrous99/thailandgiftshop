// Package location validates and normalizes customer-entered shipping
// addresses against Amazon Location Service's standalone Places Geocode API
// (the geo-places namespace, which needs no Place Index resource).
//
// The package is a leaf: it depends only on the AWS SDK and defines its own
// plain Address value type so callers (the ssr layer) map their domain types
// in and out. Callers are expected to FAIL OPEN — when ValidateAddress returns
// an error (timeout, throttling, network), the address should still be
// accepted so a Location outage never blocks a sale.
package location

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/appenv"
	"github.com/anhydrous99/thailandgiftshop/internal/awsconfig"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/geoplaces"
	"github.com/aws/aws-sdk-go-v2/service/geoplaces/types"
)

const (
	// EnvValidatorMode selects the validator implementation: "als", "fake",
	// or "off".
	EnvValidatorMode = "ADDRESS_VALIDATOR_MODE"
	// EnvValidatorRegion optionally pins the geo-places client region. When
	// empty the client inherits the Lambda's AWS_REGION (us-east-1 here),
	// where geo-places is available.
	EnvValidatorRegion = "ADDRESS_VALIDATOR_REGION"

	// ModeALS calls Amazon Location Service.
	ModeALS = "als"
	// ModeFake returns deterministic results for devserver/tests. Disallowed
	// in production.
	ModeFake = "fake"
	// ModeOff disables validation (every address is reported Verified). This
	// is an explicit operational kill-switch and is permitted in production.
	ModeOff = "off"

	// defaultVerifyThreshold is the overall match score at or above which an
	// otherwise-equal address is treated as Verified.
	defaultVerifyThreshold = 0.8
	// defaultRejectFloor is the overall match score below which an address is
	// treated as Unverifiable (clearly undeliverable).
	defaultRejectFloor = 0.5
	// defaultTimeout bounds a single Geocode call so a slow dependency never
	// eats the caller's request budget.
	defaultTimeout = 2 * time.Second
)

// ErrFakeNotAllowedInProduction mirrors the email package guard: the
// deterministic validator must never run in production.
var ErrFakeNotAllowedInProduction = errors.New("fake address validator is not allowed in production")

// Status is the outcome of validating an address.
type Status int

const (
	// StatusVerified means the entered address matched a deliverable place
	// closely enough to accept as-is.
	StatusVerified Status = iota
	// StatusCorrected means a deliverable place was found but it differs from
	// what the customer typed; Result.Standardized holds the suggestion.
	StatusCorrected
	// StatusUnverifiable means no deliverable place matched; the caller
	// should ask the customer to fix the address.
	StatusUnverifiable
)

func (s Status) String() string {
	switch s {
	case StatusVerified:
		return "verified"
	case StatusCorrected:
		return "corrected"
	case StatusUnverifiable:
		return "unverifiable"
	default:
		return "unknown"
	}
}

// Address is a plain US shipping address. Line2 (unit/apt/suite) is carried
// through untouched: geocoders normalize the deliverable street line, not the
// secondary designator.
type Address struct {
	Line1      string
	Line2      string
	City       string
	Region     string
	PostalCode string
	Country    string
}

// Result is the outcome of ValidateAddress. Standardized is only meaningful
// when Status is StatusCorrected.
type Result struct {
	Status       Status
	Standardized Address
	Overall      float64
}

// Validator validates a single shipping address.
type Validator interface {
	ValidateAddress(ctx context.Context, address Address) (Result, error)
}

// geoPlacesGeocodeAPI is the narrow slice of the geo-places client the
// validator needs, so tests can inject a mock.
type geoPlacesGeocodeAPI interface {
	Geocode(ctx context.Context, params *geoplaces.GeocodeInput, optFns ...func(*geoplaces.Options)) (*geoplaces.GeocodeOutput, error)
}

// NewValidatorFromEnvironment builds the validator selected by
// ADDRESS_VALIDATOR_MODE. An empty mode defaults to ALS in production and the
// fake validator elsewhere.
func NewValidatorFromEnvironment(ctx context.Context) (Validator, error) {
	return NewValidatorFromEnvironmentWithAWSConfigLoader(ctx, awsconfig.NewLoader())
}

func NewValidatorFromEnvironmentWithAWSConfigLoader(ctx context.Context, configLoader awsconfig.Loader) (Validator, error) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv(EnvValidatorMode)))
	if mode == "" {
		if appenv.IsProduction() {
			mode = ModeALS
		} else {
			mode = ModeFake
		}
	}

	switch mode {
	case ModeALS:
		region := strings.TrimSpace(os.Getenv(EnvValidatorRegion))
		awsConfig, err := awsconfig.Load(ctx, configLoader)
		if err != nil {
			return nil, fmt.Errorf("load AWS config for address validator: %w", err)
		}
		if region != "" {
			awsConfig.Region = region
		}
		return NewALSValidatorFromConfig(awsConfig), nil
	case ModeFake:
		if appenv.IsProduction() {
			return nil, ErrFakeNotAllowedInProduction
		}
		return NewFakeValidator(), nil
	case ModeOff:
		return OffValidator{}, nil
	default:
		return nil, fmt.Errorf("unsupported %s value %q", EnvValidatorMode, mode)
	}
}

// OffValidator reports every address as Verified. It is the kill-switch used
// when ADDRESS_VALIDATOR_MODE=off.
type OffValidator struct{}

var _ Validator = OffValidator{}

func (OffValidator) ValidateAddress(_ context.Context, _ Address) (Result, error) {
	return Result{Status: StatusVerified, Overall: 1}, nil
}

// ALSValidator validates addresses with the geo-places Geocode API.
type ALSValidator struct {
	client          geoPlacesGeocodeAPI
	verifyThreshold float64
	rejectFloor     float64
	timeout         time.Duration
}

var _ Validator = (*ALSValidator)(nil)

// NewALSValidatorFromConfig builds an ALSValidator with the default geo-places
// client and tuning.
func NewALSValidatorFromConfig(awsConfig aws.Config) *ALSValidator {
	return NewALSValidator(geoplaces.NewFromConfig(awsConfig))
}

// NewALSValidator builds an ALSValidator around an injected client.
func NewALSValidator(client geoPlacesGeocodeAPI) *ALSValidator {
	return &ALSValidator{
		client:          client,
		verifyThreshold: defaultVerifyThreshold,
		rejectFloor:     defaultRejectFloor,
		timeout:         defaultTimeout,
	}
}

func (v *ALSValidator) ValidateAddress(ctx context.Context, address Address) (Result, error) {
	query := buildQueryText(address)
	if query == "" {
		// Nothing to geocode; let the caller's syntactic gate own this.
		return Result{Status: StatusUnverifiable}, nil
	}

	if v.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, v.timeout)
		defer cancel()
	}

	output, err := v.client.Geocode(ctx, &geoplaces.GeocodeInput{
		QueryText:  aws.String(query),
		MaxResults: aws.Int32(1),
		Language:   aws.String("en"),
		// Storage (not SingleUse): when the shopper accepts a corrected
		// suggestion, the standardized components are persisted to the
		// commerce store, which the Amazon Location Service terms classify as
		// storage rather than single-use display.
		IntendedUse: types.GeocodeIntendedUseStorage,
		Filter:      &types.GeocodeFilter{IncludeCountries: []string{"USA"}},
	})
	if err != nil {
		return Result{}, fmt.Errorf("geocode address: %w", err)
	}
	if output == nil || len(output.ResultItems) == 0 {
		return Result{Status: StatusUnverifiable}, nil
	}

	item := output.ResultItems[0]
	overall := overallScore(item.MatchScores)
	if overall < v.rejectFloor || !isDeliverablePlaceType(item.PlaceType) {
		return Result{Status: StatusUnverifiable, Overall: overall}, nil
	}

	standardized := standardizedAddress(item, address)
	if overall >= v.verifyThreshold && addressesEqual(standardized, address) && !addressNumberCorrected(item) {
		return Result{Status: StatusVerified, Overall: overall}, nil
	}
	return Result{Status: StatusCorrected, Standardized: standardized, Overall: overall}, nil
}

// buildQueryText assembles the deliverable street line for geocoding. Line2
// (unit/apt) is intentionally excluded: it rarely affects the match and can
// depress the score.
func buildQueryText(a Address) string {
	parts := make([]string, 0, 3)
	if line1 := strings.TrimSpace(a.Line1); line1 != "" {
		parts = append(parts, line1)
	}
	if city := strings.TrimSpace(a.City); city != "" {
		parts = append(parts, city)
	}
	regionZip := strings.TrimSpace(strings.TrimSpace(a.Region) + " " + strings.TrimSpace(a.PostalCode))
	if regionZip != "" {
		parts = append(parts, regionZip)
	}
	return strings.Join(parts, ", ")
}

func overallScore(scores *types.MatchScoreDetails) float64 {
	if scores == nil {
		// Missing scores on a returned result: rely on the place-type gate
		// rather than blocking a real address.
		return 1
	}
	return scores.Overall
}

// isDeliverablePlaceType keeps only matches precise enough to ship to. Coarse
// matches (Street/Locality/PostalCode/Region centroids) mean the house number
// did not resolve.
func isDeliverablePlaceType(placeType types.PlaceType) bool {
	switch placeType {
	case types.PlaceTypePointAddress,
		types.PlaceTypeInterpolatedAddress,
		types.PlaceTypeSecondaryAddress,
		types.PlaceTypeInferredSecondaryAddress:
		return true
	default:
		return false
	}
}

func addressNumberCorrected(item types.GeocodeResultItem) bool {
	return item.AddressNumberCorrected != nil && *item.AddressNumberCorrected
}

// standardizedAddress maps the geo-places result onto our Address, preserving
// the customer's Line2 and falling back to the entered value for any component
// the service omits.
func standardizedAddress(item types.GeocodeResultItem, entered Address) Address {
	std := Address{
		Line1:      entered.Line1,
		Line2:      entered.Line2,
		City:       entered.City,
		Region:     entered.Region,
		PostalCode: entered.PostalCode,
		Country:    "US",
	}
	addr := item.Address
	if addr == nil {
		return std
	}
	if line1 := joinLine1(aws.ToString(addr.AddressNumber), aws.ToString(addr.Street)); line1 != "" {
		std.Line1 = line1
	}
	if locality := strings.TrimSpace(aws.ToString(addr.Locality)); locality != "" {
		std.City = locality
	}
	if addr.Region != nil {
		if code := strings.TrimSpace(aws.ToString(addr.Region.Code)); code != "" {
			std.Region = code
		}
	}
	if postal := strings.TrimSpace(aws.ToString(addr.PostalCode)); postal != "" {
		std.PostalCode = postal
	}
	return std
}

func joinLine1(number string, street string) string {
	number = strings.TrimSpace(number)
	street = strings.TrimSpace(street)
	switch {
	case number != "" && street != "":
		return number + " " + street
	case street != "":
		return street
	default:
		return ""
	}
}

// addressesEqual compares the deliverable components after normalization. ZIP
// is compared on the leading 5 digits so adopting a ZIP+4 alone is not treated
// as a difference.
func addressesEqual(a Address, b Address) bool {
	return normalizeForCompare(a.Line1) == normalizeForCompare(b.Line1) &&
		normalizeForCompare(a.City) == normalizeForCompare(b.City) &&
		normalizeForCompare(a.Region) == normalizeForCompare(b.Region) &&
		zip5(a.PostalCode) == zip5(b.PostalCode)
}

func normalizeForCompare(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, ".", "")
	s = strings.ReplaceAll(s, ",", "")
	return strings.Join(strings.Fields(s), " ")
}

func zip5(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '-'); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}
