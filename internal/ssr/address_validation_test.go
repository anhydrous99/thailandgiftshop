package ssr

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/anhydrous99/thailandgiftshop/internal/location"
)

// confirmCreateForm builds the address-book create POST body used across the
// validation tests.
func confirmCreateForm(token string, line1 string, extra url.Values) url.Values {
	form := url.Values{
		customerCSRFFieldName: {token},
		"full_name":           {"Anong Shopper"},
		"line1":               {line1},
		"city":                {"Townsville"},
		"region":              {"CA"},
		"postal_code":         {"90001"},
	}
	for key, values := range extra {
		form[key] = values
	}
	return form
}

func TestAddressValidationSuggestionAcceptStoresStandardized(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	token := accountCSRFToken(t, env.handler, jar)

	suggest, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/addresses",
		confirmCreateForm(token, "10 "+location.FakeSuggestMarker+" Blvd", nil), jar))
	if err != nil {
		t.Fatalf("Handle suggest returned error: %v", err)
	}
	jar.update(t, suggest)
	if suggest.StatusCode != http.StatusOK {
		t.Fatalf("suggest status = %d, want %d", suggest.StatusCode, http.StatusOK)
	}
	assertBodyContains(t, suggest.Body, []string{
		`data-testid="address-suggestion"`,
		location.FakeSuggestion.Line1,
		`value="10 ` + location.FakeSuggestMarker + ` Blvd"`, // entered value preserved
		`name="standardized_line1" value="` + location.FakeSuggestion.Line1 + `"`,
	})
	if addrs, _ := env.commerce.ListAddresses(context.Background(), accountCustomerID(t, env, "shopper@example.com")); len(addrs) != 0 {
		t.Fatalf("suggestion must not persist; stored %d addresses", len(addrs))
	}

	confirmToken := hiddenInputValue(t, suggest.Body, customerCSRFFieldName)
	accept, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/addresses",
		confirmCreateForm(confirmToken, "10 "+location.FakeSuggestMarker+" Blvd", url.Values{
			"address_confirmed":        {"1"},
			"address_choice":           {"suggested"},
			"standardized_line1":       {hiddenInputValue(t, suggest.Body, "standardized_line1")},
			"standardized_city":        {hiddenInputValue(t, suggest.Body, "standardized_city")},
			"standardized_region":      {hiddenInputValue(t, suggest.Body, "standardized_region")},
			"standardized_postal_code": {hiddenInputValue(t, suggest.Body, "standardized_postal_code")},
		}), jar))
	if err != nil {
		t.Fatalf("Handle accept returned error: %v", err)
	}
	jar.update(t, accept)
	if accept.StatusCode != http.StatusSeeOther || accept.Headers["Location"] != "/account/addresses" {
		t.Fatalf("accept = %d %q, want 303 /account/addresses", accept.StatusCode, accept.Headers["Location"])
	}

	addrs, err := env.commerce.ListAddresses(context.Background(), accountCustomerID(t, env, "shopper@example.com"))
	if err != nil || len(addrs) != 1 {
		t.Fatalf("ListAddresses = %d (err %v), want 1", len(addrs), err)
	}
	stored := addrs[0]
	if stored.Line1 != location.FakeSuggestion.Line1 || stored.City != location.FakeSuggestion.City ||
		stored.Region != location.FakeSuggestion.Region || stored.PostalCode != location.FakeSuggestion.PostalCode {
		t.Fatalf("stored = %+v, want standardized suggestion %+v", stored, location.FakeSuggestion)
	}
	if stored.FullName != "Anong Shopper" {
		t.Fatalf("stored full name = %q, want the entered name", stored.FullName)
	}
}

func TestAddressValidationRejectsTamperedStandardizedRegion(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	token := accountCSRFToken(t, env.handler, jar)

	suggest, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/addresses",
		confirmCreateForm(token, "10 "+location.FakeSuggestMarker+" Blvd", nil), jar))
	if err != nil {
		t.Fatalf("Handle suggest returned error: %v", err)
	}
	jar.update(t, suggest)
	if suggest.StatusCode != http.StatusOK {
		t.Fatalf("suggest status = %d, want %d", suggest.StatusCode, http.StatusOK)
	}

	confirmToken := hiddenInputValue(t, suggest.Body, customerCSRFFieldName)
	tampered, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/addresses",
		confirmCreateForm(confirmToken, "10 "+location.FakeSuggestMarker+" Blvd", url.Values{
			"address_confirmed":        {"1"},
			"address_choice":           {"suggested"},
			"standardized_line1":       {hiddenInputValue(t, suggest.Body, "standardized_line1")},
			"standardized_city":        {hiddenInputValue(t, suggest.Body, "standardized_city")},
			"standardized_region":      {"ZZ"},
			"standardized_postal_code": {hiddenInputValue(t, suggest.Body, "standardized_postal_code")},
		}), jar))
	if err != nil {
		t.Fatalf("Handle tampered accept returned error: %v", err)
	}
	if tampered.StatusCode != http.StatusBadRequest {
		t.Fatalf("tampered accept status = %d, want %d", tampered.StatusCode, http.StatusBadRequest)
	}
	assertBodyContains(t, tampered.Body, []string{unverifiableAddressError})
	if addrs, _ := env.commerce.ListAddresses(context.Background(), accountCustomerID(t, env, "shopper@example.com")); len(addrs) != 0 {
		t.Fatalf("tampered suggestion must not persist; stored %d", len(addrs))
	}
}

func TestAddressValidationConfirmPreservesNext(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	token := accountCSRFToken(t, env.handler, jar)

	suggest, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/addresses",
		confirmCreateForm(token, "10 "+location.FakeSuggestMarker+" Blvd", url.Values{"next": {"/checkout"}}), jar))
	if err != nil {
		t.Fatalf("Handle suggest returned error: %v", err)
	}
	jar.update(t, suggest)
	if suggest.StatusCode != http.StatusOK {
		t.Fatalf("suggest status = %d, want %d", suggest.StatusCode, http.StatusOK)
	}
	// The confirm re-render is a POST with no query string; without the body
	// fallback, next would be lost and accepting the suggestion would not
	// return the shopper to checkout.
	assertBodyContains(t, suggest.Body, []string{`name="next" value="/checkout"`})

	confirmToken := hiddenInputValue(t, suggest.Body, customerCSRFFieldName)
	accept, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/addresses",
		confirmCreateForm(confirmToken, "10 "+location.FakeSuggestMarker+" Blvd", url.Values{
			"next":                     {"/checkout"},
			"address_confirmed":        {"1"},
			"address_choice":           {"suggested"},
			"standardized_line1":       {hiddenInputValue(t, suggest.Body, "standardized_line1")},
			"standardized_city":        {hiddenInputValue(t, suggest.Body, "standardized_city")},
			"standardized_region":      {hiddenInputValue(t, suggest.Body, "standardized_region")},
			"standardized_postal_code": {hiddenInputValue(t, suggest.Body, "standardized_postal_code")},
		}), jar))
	if err != nil {
		t.Fatalf("Handle accept returned error: %v", err)
	}
	if accept.StatusCode != http.StatusSeeOther || accept.Headers["Location"] != "/checkout" {
		t.Fatalf("accept = %d %q, want 303 /checkout", accept.StatusCode, accept.Headers["Location"])
	}
}

func TestAddressValidationOverrideKeepsEntered(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	token := accountCSRFToken(t, env.handler, jar)

	enteredLine1 := "10 " + location.FakeSuggestMarker + " Blvd"
	suggest, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/addresses",
		confirmCreateForm(token, enteredLine1, nil), jar))
	if err != nil {
		t.Fatalf("Handle suggest returned error: %v", err)
	}
	jar.update(t, suggest)

	confirmToken := hiddenInputValue(t, suggest.Body, customerCSRFFieldName)
	override, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/addresses",
		confirmCreateForm(confirmToken, enteredLine1, url.Values{
			"address_confirmed": {"1"},
			"address_choice":    {"entered"},
		}), jar))
	if err != nil {
		t.Fatalf("Handle override returned error: %v", err)
	}
	jar.update(t, override)
	if override.StatusCode != http.StatusSeeOther {
		t.Fatalf("override status = %d, want %d", override.StatusCode, http.StatusSeeOther)
	}

	addrs, err := env.commerce.ListAddresses(context.Background(), accountCustomerID(t, env, "shopper@example.com"))
	if err != nil || len(addrs) != 1 {
		t.Fatalf("ListAddresses = %d (err %v), want 1", len(addrs), err)
	}
	if addrs[0].Line1 != enteredLine1 {
		t.Fatalf("stored line1 = %q, want entered %q", addrs[0].Line1, enteredLine1)
	}
}

func TestAddressValidationRejectsUnverifiable(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	token := accountCSRFToken(t, env.handler, jar)

	reject, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/addresses",
		confirmCreateForm(token, "10 "+location.FakeRejectMarker+" Way", nil), jar))
	if err != nil {
		t.Fatalf("Handle reject returned error: %v", err)
	}
	jar.update(t, reject)
	if reject.StatusCode != http.StatusBadRequest {
		t.Fatalf("reject status = %d, want %d", reject.StatusCode, http.StatusBadRequest)
	}
	assertBodyContains(t, reject.Body, []string{unverifiableAddressError})

	if addrs, _ := env.commerce.ListAddresses(context.Background(), accountCustomerID(t, env, "shopper@example.com")); len(addrs) != 0 {
		t.Fatalf("unverifiable address must not persist; stored %d", len(addrs))
	}
}

func TestAddressValidationFailsOpenOnValidatorError(t *testing.T) {
	env := newAccountTestEnv(t)
	env.handler.addressValidator = &location.FakeValidator{
		Func: func(context.Context, location.Address) (location.Result, error) {
			return location.Result{}, errors.New("location service unavailable")
		},
	}
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "shopper@example.com", "orchid-market-99")
	token := accountCSRFToken(t, env.handler, jar)

	create, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/addresses",
		confirmCreateForm(token, "123 Real St", nil), jar))
	if err != nil {
		t.Fatalf("Handle create returned error: %v", err)
	}
	jar.update(t, create)
	if create.StatusCode != http.StatusSeeOther {
		t.Fatalf("fail-open create status = %d, want %d (never block a sale on a validation outage)", create.StatusCode, http.StatusSeeOther)
	}
	if addrs, _ := env.commerce.ListAddresses(context.Background(), accountCustomerID(t, env, "shopper@example.com")); len(addrs) != 1 {
		t.Fatalf("fail-open should persist the address; stored %d", len(addrs))
	}
}
