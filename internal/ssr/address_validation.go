package ssr

import (
	"context"
	"net/url"
	"strings"

	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/location"
	"github.com/anhydrous99/thailandgiftshop/internal/observability"
)

// unverifiableAddressError is shown when Amazon Location Service cannot match
// the entry to a deliverable address (the hard-block case).
const unverifiableAddressError = "We could not verify this address. Check the street, city, state, and ZIP code."

const (
	addressChoiceSuggested = "suggested"
	addressChoiceEntered   = "entered"
)

type addressDecisionKind int

const (
	// addressProceed: persist decision.address (verified, override, accepted
	// suggestion, or fail-open).
	addressProceed addressDecisionKind = iota
	// addressConfirm: re-render the form with decision.suggestion so the
	// shopper can pick the standardized address or keep their own.
	addressConfirm
	// addressReject: re-render with unverifiableAddressError.
	addressReject
)

type addressDecision struct {
	kind       addressDecisionKind
	address    commerce.Address
	suggestion *addressSuggestionView
}

// resolveAddressValidation is the shared validation gate for the address book
// and guest checkout. It honors an explicit confirm choice, otherwise calls
// the validator and classifies the outcome. It NEVER returns an error: a
// validator failure fails open to addressProceed so a Location outage cannot
// block a sale.
func (h *Handler) resolveAddressValidation(ctx context.Context, form url.Values, entered commerce.Address) addressDecision {
	confirmed := strings.TrimSpace(form.Get("address_confirmed")) == "1"
	choice := strings.TrimSpace(form.Get("address_choice"))

	if confirmed && choice == addressChoiceEntered {
		h.recordAddressValidation("overridden")
		return addressDecision{kind: addressProceed, address: entered}
	}
	if confirmed && choice == addressChoiceSuggested {
		h.recordAddressValidation("accepted")
		return addressDecision{kind: addressProceed, address: standardizedAddressFromForm(form, entered)}
	}

	if h.addressValidator == nil {
		return addressDecision{kind: addressProceed, address: entered}
	}

	result, err := h.addressValidator.ValidateAddress(ctx, locationAddressFromCommerce(entered))
	if err != nil {
		logAccountError("address validation: unavailable", err)
		h.recordAddressValidation("unavailable")
		return addressDecision{kind: addressProceed, address: entered}
	}

	switch result.Status {
	case location.StatusCorrected:
		h.recordAddressValidation("corrected")
		suggestion := addressSuggestionViewFromLocation(result.Standardized)
		return addressDecision{kind: addressConfirm, suggestion: &suggestion}
	case location.StatusUnverifiable:
		h.recordAddressValidation("unverifiable")
		return addressDecision{kind: addressReject}
	default:
		h.recordAddressValidation("verified")
		return addressDecision{kind: addressProceed, address: entered}
	}
}

func (h *Handler) recordAddressValidation(outcome string) {
	if h.metrics == nil {
		return
	}
	h.metrics.Record(observability.Count(
		observability.MetricAddressValidation,
		observability.Dim("Service", "ssr"),
		observability.Dim("Outcome", outcome),
	))
}

func locationAddressFromCommerce(a commerce.Address) location.Address {
	return location.Address{
		Line1:      a.Line1,
		Line2:      a.Line2,
		City:       a.City,
		Region:     a.Region,
		PostalCode: a.PostalCode,
		Country:    a.Country,
	}
}

// standardizedAddressFromForm rebuilds the accepted suggestion from the hidden
// standardized_* inputs, keeping the non-validated fields (name, phone) from
// the entered address and falling back to entered values defensively.
func standardizedAddressFromForm(form url.Values, entered commerce.Address) commerce.Address {
	std := entered
	std.Country = addressCountryUS
	if line1 := strings.TrimSpace(form.Get("standardized_line1")); line1 != "" {
		std.Line1 = line1
	}
	std.Line2 = strings.TrimSpace(form.Get("standardized_line2"))
	if city := strings.TrimSpace(form.Get("standardized_city")); city != "" {
		std.City = city
	}
	if region := strings.TrimSpace(form.Get("standardized_region")); region != "" {
		std.Region = region
	}
	if postal := strings.TrimSpace(form.Get("standardized_postal_code")); postal != "" {
		std.PostalCode = postal
	}
	return std
}

func addressSuggestionViewFromLocation(a location.Address) addressSuggestionView {
	return addressSuggestionView{
		Label:      formatAddressLabel(a),
		Line1:      a.Line1,
		Line2:      a.Line2,
		City:       a.City,
		Region:     a.Region,
		PostalCode: a.PostalCode,
	}
}

// formatAddressLabel renders a one-line US address: "Line1, Line2, City, ST ZIP".
func formatAddressLabel(a location.Address) string {
	parts := make([]string, 0, 3)
	if line1 := strings.TrimSpace(a.Line1); line1 != "" {
		parts = append(parts, line1)
	}
	if line2 := strings.TrimSpace(a.Line2); line2 != "" {
		parts = append(parts, line2)
	}
	cityRegionZip := strings.TrimSpace(strings.TrimSpace(a.City+", "+a.Region) + " " + strings.TrimSpace(a.PostalCode))
	cityRegionZip = strings.TrimPrefix(cityRegionZip, ", ")
	if cityRegionZip != "" {
		parts = append(parts, cityRegionZip)
	}
	return strings.Join(parts, ", ")
}

// enteredAddressLabel renders the shopper's own entry for the confirm panel's
// "keep what I entered" option.
func enteredAddressLabel(form addressFormData) string {
	return formatAddressLabel(location.Address{
		Line1:      form.Line1,
		Line2:      form.Line2,
		City:       form.City,
		Region:     form.Region,
		PostalCode: form.PostalCode,
	})
}
