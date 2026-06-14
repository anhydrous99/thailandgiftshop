package location

import (
	"context"
	"strings"
)

// Marker substrings let devserver and Playwright drive every branch
// deterministically without a network call. They are matched case-insensitively
// against address line 1.
const (
	// FakeRejectMarker forces StatusUnverifiable.
	FakeRejectMarker = "force-reject"
	// FakeSuggestMarker forces StatusCorrected with FakeSuggestion.
	FakeSuggestMarker = "force-suggest"
)

// FakeSuggestion is the canonical address the fake validator proposes for an
// address containing FakeSuggestMarker. The customer's Line2 is preserved by
// ValidateAddress.
var FakeSuggestion = Address{
	Line1:      "350 Fifth Ave",
	City:       "New York",
	Region:     "NY",
	PostalCode: "10118",
	Country:    "US",
}

// FakeValidator returns deterministic results. By default any address is
// Verified, which keeps existing checkout flows green; the markers above force
// the other branches. Func, when set, fully overrides this behavior so unit
// tests can exercise fail-open and bespoke cases.
type FakeValidator struct {
	Func func(ctx context.Context, address Address) (Result, error)
}

var _ Validator = (*FakeValidator)(nil)

// NewFakeValidator builds a FakeValidator with the default marker behavior.
func NewFakeValidator() *FakeValidator {
	return &FakeValidator{}
}

func (f *FakeValidator) ValidateAddress(ctx context.Context, address Address) (Result, error) {
	if f.Func != nil {
		return f.Func(ctx, address)
	}

	line1 := strings.ToLower(address.Line1)
	switch {
	case strings.Contains(line1, FakeRejectMarker):
		return Result{Status: StatusUnverifiable}, nil
	case strings.Contains(line1, FakeSuggestMarker):
		suggestion := FakeSuggestion
		suggestion.Line2 = address.Line2
		return Result{Status: StatusCorrected, Standardized: suggestion, Overall: 0.9}, nil
	default:
		return Result{Status: StatusVerified, Overall: 1}, nil
	}
}
