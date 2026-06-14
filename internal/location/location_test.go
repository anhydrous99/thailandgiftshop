package location

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/geoplaces"
	"github.com/aws/aws-sdk-go-v2/service/geoplaces/types"
)

type stubGeocoder struct {
	output   *geoplaces.GeocodeOutput
	err      error
	gotInput *geoplaces.GeocodeInput
}

func (s *stubGeocoder) Geocode(_ context.Context, params *geoplaces.GeocodeInput, _ ...func(*geoplaces.Options)) (*geoplaces.GeocodeOutput, error) {
	s.gotInput = params
	return s.output, s.err
}

func usAddress(line1, city, region, postal string) *types.Address {
	return &types.Address{
		AddressNumber: nil,
		Street:        aws.String(line1),
		Locality:      aws.String(city),
		Region:        &types.Region{Code: aws.String(region)},
		PostalCode:    aws.String(postal),
		Country:       &types.Country{Code2: aws.String("US")},
	}
}

func geocodeOutput(item types.GeocodeResultItem) *geoplaces.GeocodeOutput {
	return &geoplaces.GeocodeOutput{
		PricingBucket: aws.String("Geocode"),
		ResultItems:   []types.GeocodeResultItem{item},
	}
}

func item(placeType types.PlaceType, overall float64, addr *types.Address) types.GeocodeResultItem {
	return types.GeocodeResultItem{
		PlaceId:     aws.String("place-id"),
		PlaceType:   placeType,
		Title:       aws.String("title"),
		Address:     addr,
		MatchScores: &types.MatchScoreDetails{Overall: overall},
	}
}

func entered() Address {
	return Address{Line1: "350 Fifth Ave", City: "New York", Region: "NY", PostalCode: "10118", Country: "US"}
}

func TestALSValidatorClassification(t *testing.T) {
	tests := []struct {
		name       string
		output     *geoplaces.GeocodeOutput
		wantStatus Status
		wantLine1  string // standardized line1 when corrected
	}{
		{
			name:       "verified when components match",
			output:     geocodeOutput(item(types.PlaceTypePointAddress, 0.99, usAddress("350 Fifth Ave", "New York", "NY", "10118"))),
			wantStatus: StatusVerified,
		},
		{
			name:       "corrected when street differs",
			output:     geocodeOutput(item(types.PlaceTypePointAddress, 0.95, usAddress("350 5th Ave", "New York", "NY", "10118"))),
			wantStatus: StatusCorrected,
			wantLine1:  "350 5th Ave",
		},
		{
			name:       "unverifiable below reject floor",
			output:     geocodeOutput(item(types.PlaceTypePointAddress, 0.30, usAddress("350 Fifth Ave", "New York", "NY", "10118"))),
			wantStatus: StatusUnverifiable,
		},
		{
			name:       "unverifiable for coarse place type",
			output:     geocodeOutput(item(types.PlaceTypeStreet, 0.95, usAddress("Fifth Ave", "New York", "NY", "10118"))),
			wantStatus: StatusUnverifiable,
		},
		{
			name:       "unverifiable for empty results",
			output:     &geoplaces.GeocodeOutput{PricingBucket: aws.String("Geocode")},
			wantStatus: StatusUnverifiable,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubGeocoder{output: tc.output}
			result, err := NewALSValidator(stub).ValidateAddress(context.Background(), entered())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Status != tc.wantStatus {
				t.Fatalf("status = %v, want %v", result.Status, tc.wantStatus)
			}
			if tc.wantLine1 != "" && result.Standardized.Line1 != tc.wantLine1 {
				t.Fatalf("standardized line1 = %q, want %q", result.Standardized.Line1, tc.wantLine1)
			}
		})
	}
}

func TestALSValidatorZipPlusFourStaysVerified(t *testing.T) {
	stub := &stubGeocoder{output: geocodeOutput(item(types.PlaceTypePointAddress, 0.97, usAddress("350 Fifth Ave", "New York", "NY", "10118-0110")))}
	result, err := NewALSValidator(stub).ValidateAddress(context.Background(), entered())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusVerified {
		t.Fatalf("status = %v, want verified (zip+4 alone is not a difference)", result.Status)
	}
}

func TestALSValidatorCorrectedWhenAddressNumberCorrected(t *testing.T) {
	it := item(types.PlaceTypePointAddress, 0.99, usAddress("350 Fifth Ave", "New York", "NY", "10118"))
	it.AddressNumberCorrected = aws.Bool(true)
	stub := &stubGeocoder{output: geocodeOutput(it)}
	result, err := NewALSValidator(stub).ValidateAddress(context.Background(), entered())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusCorrected {
		t.Fatalf("status = %v, want corrected when the house number was corrected", result.Status)
	}
}

func TestALSValidatorPreservesLine2(t *testing.T) {
	in := entered()
	in.Line2 = "Apt 5"
	stub := &stubGeocoder{output: geocodeOutput(item(types.PlaceTypePointAddress, 0.95, usAddress("350 5th Ave", "New York", "NY", "10118")))}
	result, err := NewALSValidator(stub).ValidateAddress(context.Background(), in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Standardized.Line2 != "Apt 5" {
		t.Fatalf("standardized line2 = %q, want %q", result.Standardized.Line2, "Apt 5")
	}
}

func TestALSValidatorPropagatesError(t *testing.T) {
	stub := &stubGeocoder{err: errors.New("throttled")}
	_, err := NewALSValidator(stub).ValidateAddress(context.Background(), entered())
	if err == nil {
		t.Fatal("expected error so the caller can fail open")
	}
}

func TestALSValidatorQueryTextExcludesLine2(t *testing.T) {
	in := entered()
	in.Line2 = "Apt 5"
	stub := &stubGeocoder{output: geocodeOutput(item(types.PlaceTypePointAddress, 0.99, usAddress("350 Fifth Ave", "New York", "NY", "10118")))}
	if _, err := NewALSValidator(stub).ValidateAddress(context.Background(), in); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	query := aws.ToString(stub.gotInput.QueryText)
	if strings.Contains(strings.ToLower(query), "apt 5") {
		t.Fatalf("query text %q should not contain line2", query)
	}
	if stub.gotInput.Filter == nil || len(stub.gotInput.Filter.IncludeCountries) != 1 || stub.gotInput.Filter.IncludeCountries[0] != "USA" {
		t.Fatalf("expected USA country filter, got %+v", stub.gotInput.Filter)
	}
	if stub.gotInput.IntendedUse != types.GeocodeIntendedUseStorage {
		t.Fatalf("IntendedUse = %q, want Storage (accepted suggestions are persisted)", stub.gotInput.IntendedUse)
	}
}

func TestFakeValidatorMarkers(t *testing.T) {
	fake := NewFakeValidator()

	verified, _ := fake.ValidateAddress(context.Background(), Address{Line1: "100 Main St"})
	if verified.Status != StatusVerified {
		t.Fatalf("default status = %v, want verified", verified.Status)
	}

	rejected, _ := fake.ValidateAddress(context.Background(), Address{Line1: "1 " + FakeRejectMarker + " Rd"})
	if rejected.Status != StatusUnverifiable {
		t.Fatalf("reject marker status = %v, want unverifiable", rejected.Status)
	}

	suggested, _ := fake.ValidateAddress(context.Background(), Address{Line1: FakeSuggestMarker, Line2: "Suite 9"})
	if suggested.Status != StatusCorrected {
		t.Fatalf("suggest marker status = %v, want corrected", suggested.Status)
	}
	if suggested.Standardized.Line1 != FakeSuggestion.Line1 {
		t.Fatalf("suggestion line1 = %q, want %q", suggested.Standardized.Line1, FakeSuggestion.Line1)
	}
	if suggested.Standardized.Line2 != "Suite 9" {
		t.Fatalf("suggestion should preserve line2, got %q", suggested.Standardized.Line2)
	}
}

func TestFakeValidatorFuncOverride(t *testing.T) {
	want := errors.New("boom")
	fake := &FakeValidator{Func: func(context.Context, Address) (Result, error) {
		return Result{}, want
	}}
	if _, err := fake.ValidateAddress(context.Background(), entered()); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestOffValidatorAlwaysVerifies(t *testing.T) {
	result, err := OffValidator{}.ValidateAddress(context.Background(), Address{Line1: "1 " + FakeRejectMarker})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusVerified {
		t.Fatalf("off validator status = %v, want verified", result.Status)
	}
}

func TestNewValidatorFromEnvironment(t *testing.T) {
	t.Setenv("APP_ENV", "")

	t.Run("off mode", func(t *testing.T) {
		t.Setenv(EnvValidatorMode, ModeOff)
		v, err := NewValidatorFromEnvironment(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := v.(OffValidator); !ok {
			t.Fatalf("got %T, want OffValidator", v)
		}
	})

	t.Run("fake mode outside production", func(t *testing.T) {
		t.Setenv(EnvValidatorMode, ModeFake)
		v, err := NewValidatorFromEnvironment(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := v.(*FakeValidator); !ok {
			t.Fatalf("got %T, want *FakeValidator", v)
		}
	})

	t.Run("fake mode rejected in production", func(t *testing.T) {
		t.Setenv("APP_ENV", "production")
		t.Setenv(EnvValidatorMode, ModeFake)
		if _, err := NewValidatorFromEnvironment(context.Background()); !errors.Is(err, ErrFakeNotAllowedInProduction) {
			t.Fatalf("err = %v, want ErrFakeNotAllowedInProduction", err)
		}
	})

	t.Run("unsupported mode", func(t *testing.T) {
		t.Setenv(EnvValidatorMode, "bogus")
		if _, err := NewValidatorFromEnvironment(context.Background()); err == nil {
			t.Fatal("expected error for unsupported mode")
		}
	})
}
