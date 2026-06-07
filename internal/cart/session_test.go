package cart

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const testSecret = "cart-test-secret"

func TestCartCookieRoundTrip(t *testing.T) {
	cart, err := New([]Line{
		{Slug: "mango-sticky-rice-kit", Quantity: 2},
		{Slug: "jasmine-tea", Quantity: 3},
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	encoded, err := EncodeCookie(cart, testSecret)
	if err != nil {
		t.Fatalf("EncodeCookie returned error: %v", err)
	}
	if len(encoded) > MaxEncodedCookieLength {
		t.Fatalf("encoded cookie length = %d, want <= %d", len(encoded), MaxEncodedCookieLength)
	}

	decoded := DecodeCookie(encoded, testSecret)
	if decoded.NeedsClear {
		t.Fatal("DecodeCookie NeedsClear = true, want false")
	}
	wantLines := []Line{
		{Slug: "mango-sticky-rice-kit", Quantity: 2},
		{Slug: "jasmine-tea", Quantity: 3},
	}
	if got := decoded.Cart.Lines(); !reflect.DeepEqual(got, wantLines) {
		t.Fatalf("decoded lines = %#v, want %#v", got, wantLines)
	}

	decodedPayload := decodeTestPayload(t, encoded)
	if decodedPayload.Version != payloadVersion {
		t.Fatalf("payload version = %d, want %d", decodedPayload.Version, payloadVersion)
	}
	if got := decodedPayload.Lines; !reflect.DeepEqual(got, wantLines) {
		t.Fatalf("payload lines = %#v, want %#v", got, wantLines)
	}
}

func TestCartCookieRoundTripsValidEmptyCart(t *testing.T) {
	encoded, err := EncodeCookie(Empty(), testSecret)
	if err != nil {
		t.Fatalf("EncodeCookie empty cart returned error: %v", err)
	}

	decoded := DecodeCookie(encoded, testSecret)
	if decoded.NeedsClear {
		t.Fatal("DecodeCookie empty cart NeedsClear = true, want false")
	}
	if decoded.Cart.LineCount() != 0 {
		t.Fatalf("empty cart line count = %d, want 0", decoded.Cart.LineCount())
	}
}

func TestCartAddSetRemoveClearAndCounts(t *testing.T) {
	cart := Empty()
	cart, err := cart.Add("mango", 2)
	if err != nil {
		t.Fatalf("Add mango returned error: %v", err)
	}
	cart, err = cart.Add("tea", 4)
	if err != nil {
		t.Fatalf("Add tea returned error: %v", err)
	}
	cart, err = cart.Add("mango", 3)
	if err != nil {
		t.Fatalf("Add duplicate mango returned error: %v", err)
	}

	if got := cart.LineCount(); got != 2 {
		t.Fatalf("line count after add = %d, want 2", got)
	}
	if got := cart.TotalItemCount(); got != 9 {
		t.Fatalf("total item count after add = %d, want 9", got)
	}

	cart, err = cart.SetQuantity("tea", 1)
	if err != nil {
		t.Fatalf("SetQuantity tea returned error: %v", err)
	}
	cart, err = cart.Remove("mango")
	if err != nil {
		t.Fatalf("Remove mango returned error: %v", err)
	}
	if got := cart.Lines(); !reflect.DeepEqual(got, []Line{{Slug: "tea", Quantity: 1}}) {
		t.Fatalf("lines after set/remove = %#v", got)
	}

	cart = cart.Clear()
	if cart.LineCount() != 0 || cart.TotalItemCount() != 0 {
		t.Fatalf("cleared cart line count = %d, total = %d, want zero", cart.LineCount(), cart.TotalItemCount())
	}
}

func TestCartLineIdentityIncludesVariantID(t *testing.T) {
	cart := Empty()
	var err error
	cart, err = cart.AddLine("linen-shirt", "var-small", 2)
	if err != nil {
		t.Fatalf("AddLine small returned error: %v", err)
	}
	cart, err = cart.AddLine("linen-shirt", "var-large", 3)
	if err != nil {
		t.Fatalf("AddLine large returned error: %v", err)
	}
	cart, err = cart.AddLine("linen-shirt", "var-small", 4)
	if err != nil {
		t.Fatalf("AddLine duplicate small returned error: %v", err)
	}

	want := []Line{
		{Slug: "linen-shirt", VariantID: "var-small", Quantity: 6},
		{Slug: "linen-shirt", VariantID: "var-large", Quantity: 3},
	}
	if got := cart.Lines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("variant lines = %#v, want %#v", got, want)
	}

	cart, err = cart.SetLineQuantity("linen-shirt", "var-large", 1)
	if err != nil {
		t.Fatalf("SetLineQuantity large returned error: %v", err)
	}
	cart, err = cart.RemoveLine("linen-shirt", "var-small")
	if err != nil {
		t.Fatalf("RemoveLine small returned error: %v", err)
	}
	want = []Line{{Slug: "linen-shirt", VariantID: "var-large", Quantity: 1}}
	if got := cart.Lines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("variant lines after set/remove = %#v, want %#v", got, want)
	}
}

func TestCartSetQuantityCanAddAndRemove(t *testing.T) {
	cart := Empty()
	cart, err := cart.SetQuantity("mango", 8)
	if err != nil {
		t.Fatalf("SetQuantity new line returned error: %v", err)
	}
	if got := cart.Lines(); !reflect.DeepEqual(got, []Line{{Slug: "mango", Quantity: 8}}) {
		t.Fatalf("lines after set new = %#v", got)
	}

	cart, err = cart.SetQuantity("mango", 0)
	if err != nil {
		t.Fatalf("SetQuantity zero returned error: %v", err)
	}
	if got := cart.LineCount(); got != 0 {
		t.Fatalf("line count after zero quantity = %d, want 0", got)
	}
}

func TestCartRejectsInvalidSlugs(t *testing.T) {
	for _, slug := range []string{"", "   ", "products/mango"} {
		t.Run(fmt.Sprintf("new_%q", slug), func(t *testing.T) {
			if _, err := New([]Line{{Slug: slug, Quantity: 1}}); !errors.Is(err, ErrInvalidSlug) {
				t.Fatalf("New error = %v, want ErrInvalidSlug", err)
			}
		})

		t.Run(fmt.Sprintf("add_%q", slug), func(t *testing.T) {
			_, err := Empty().Add(slug, 1)
			if !errors.Is(err, ErrInvalidSlug) {
				t.Fatalf("Add error = %v, want ErrInvalidSlug", err)
			}
		})

		t.Run(fmt.Sprintf("set_%q", slug), func(t *testing.T) {
			_, err := Empty().SetQuantity(slug, 1)
			if !errors.Is(err, ErrInvalidSlug) {
				t.Fatalf("SetQuantity error = %v, want ErrInvalidSlug", err)
			}
		})

		t.Run(fmt.Sprintf("remove_%q", slug), func(t *testing.T) {
			_, err := Empty().Remove(slug)
			if !errors.Is(err, ErrInvalidSlug) {
				t.Fatalf("Remove error = %v, want ErrInvalidSlug", err)
			}
		})
	}
}

func TestCartCookieNormalizesDuplicateSlugs(t *testing.T) {
	encoded := signedTestCookie(t, cookiePayload{
		Version: payloadVersion,
		Lines: []Line{
			{Slug: "mango", Quantity: 60},
			{Slug: "tea", Quantity: 2},
			{Slug: "mango", Quantity: 60},
		},
	})

	decoded := DecodeCookie(encoded, testSecret)
	if decoded.NeedsClear {
		t.Fatal("DecodeCookie duplicate lines NeedsClear = true, want false")
	}
	want := []Line{{Slug: "mango", Quantity: MaxQuantity}, {Slug: "tea", Quantity: 2}}
	if got := decoded.Cart.Lines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized duplicate lines = %#v, want %#v", got, want)
	}
}

func TestCartCookiePreservesLegacyLinesWithoutVariantID(t *testing.T) {
	encoded := signedRawTestCookie(t, []byte(`{"version":1,"lines":[{"slug":"mango","quantity":2}]}`))

	decoded := DecodeCookie(encoded, testSecret)
	if decoded.NeedsClear {
		t.Fatal("DecodeCookie legacy no-variant line NeedsClear = true, want false")
	}
	want := []Line{{Slug: "mango", Quantity: 2}}
	if got := decoded.Cart.Lines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy decoded lines = %#v, want %#v", got, want)
	}
}

func TestCartCookieNormalizesDuplicateVariantLinesBySlugAndVariantID(t *testing.T) {
	encoded := signedTestCookie(t, cookiePayload{
		Version: payloadVersion,
		Lines: []Line{
			{Slug: "linen-shirt", VariantID: "var-small", Quantity: 2},
			{Slug: "linen-shirt", VariantID: "var-large", Quantity: 3},
			{Slug: "linen-shirt", VariantID: "var-small", Quantity: 4},
		},
	})

	decoded := DecodeCookie(encoded, testSecret)
	if decoded.NeedsClear {
		t.Fatal("DecodeCookie variant duplicate lines NeedsClear = true, want false")
	}
	want := []Line{
		{Slug: "linen-shirt", VariantID: "var-small", Quantity: 6},
		{Slug: "linen-shirt", VariantID: "var-large", Quantity: 3},
	}
	if got := decoded.Cart.Lines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized variant lines = %#v, want %#v", got, want)
	}
}

func TestCartQuantityAndLineCaps(t *testing.T) {
	cart, err := New([]Line{
		{Slug: "mango", Quantity: 150},
		{Slug: "tea", Quantity: 40},
		{Slug: "tea", Quantity: 80},
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	want := []Line{{Slug: "mango", Quantity: MaxQuantity}, {Slug: "tea", Quantity: MaxQuantity}}
	if got := cart.Lines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("quantity capped lines = %#v, want %#v", got, want)
	}

	cart = Empty()
	for i := range MaxLineItems {
		cart, err = cart.Add(fmt.Sprintf("slug-%02d", i), 1)
		if err != nil {
			t.Fatalf("Add line %d returned error: %v", i, err)
		}
	}
	if got := cart.LineCount(); got != MaxLineItems {
		t.Fatalf("full cart line count = %d, want %d", got, MaxLineItems)
	}
	if _, err := cart.Add("extra", 1); !errors.Is(err, ErrLineItemLimit) {
		t.Fatalf("Add over line cap error = %v, want ErrLineItemLimit", err)
	}
	if _, err := cart.SetQuantity("extra", 1); !errors.Is(err, ErrLineItemLimit) {
		t.Fatalf("SetQuantity over line cap error = %v, want ErrLineItemLimit", err)
	}

	cappedInput, err := New(cartLineCapFixture(MaxLineItems + 5))
	if err != nil {
		t.Fatalf("New over line cap returned error: %v", err)
	}
	if got := cappedInput.LineCount(); got != MaxLineItems {
		t.Fatalf("normalized over-cap line count = %d, want %d", got, MaxLineItems)
	}
}

func TestCartCookieRejectsTamper(t *testing.T) {
	cart, err := New([]Line{{Slug: "mango", Quantity: 2}})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	encoded, err := EncodeCookie(cart, testSecret)
	if err != nil {
		t.Fatalf("EncodeCookie returned error: %v", err)
	}

	encodedPayload, signature, ok := strings.Cut(encoded, ".")
	if !ok {
		t.Fatalf("encoded cookie = %q, want payload.signature", encoded)
	}
	jsonPayload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		t.Fatalf("DecodeString payload returned error: %v", err)
	}
	tamperedPayload := strings.Replace(string(jsonPayload), "mango", "papaya", 1)
	tampered := base64.RawURLEncoding.EncodeToString([]byte(tamperedPayload)) + "." + signature

	decoded := DecodeCookie(tampered, testSecret)
	if !decoded.NeedsClear {
		t.Fatal("DecodeCookie tampered NeedsClear = false, want true")
	}
	if decoded.Cart.LineCount() != 0 {
		t.Fatalf("tampered cart line count = %d, want 0", decoded.Cart.LineCount())
	}
}

func TestCartCookieRejectsOversize(t *testing.T) {
	decoded := DecodeCookie(strings.Repeat("a", MaxEncodedCookieLength+1), testSecret)
	if !decoded.NeedsClear {
		t.Fatal("DecodeCookie oversized NeedsClear = false, want true")
	}
	if decoded.Cart.LineCount() != 0 {
		t.Fatalf("oversized cart line count = %d, want 0", decoded.Cart.LineCount())
	}

	longSlug := strings.Repeat("a", MaxEncodedCookieLength)
	cart, err := New([]Line{{Slug: longSlug, Quantity: 1}})
	if err != nil {
		t.Fatalf("New long slug returned error: %v", err)
	}
	if _, err := EncodeCookie(cart, testSecret); !errors.Is(err, ErrCookieTooLarge) {
		t.Fatalf("EncodeCookie oversized error = %v, want ErrCookieTooLarge", err)
	}
}

func TestCartCookieRejectsMalformed(t *testing.T) {
	malformed := []string{
		"not-a-cookie",
		"one.two.three",
		"%%%.signature",
		signedRawTestCookie(t, []byte("{")),
		signedTestCookie(t, cookiePayload{Version: payloadVersion, Lines: []Line{{Slug: "mango", Quantity: 0}}}),
		signedTestCookie(t, cookiePayload{Version: payloadVersion, Lines: []Line{{Slug: "mango", Quantity: -1}}}),
	}

	for _, value := range malformed {
		t.Run(value, func(t *testing.T) {
			decoded := DecodeCookie(value, testSecret)
			if !decoded.NeedsClear {
				t.Fatal("DecodeCookie malformed NeedsClear = false, want true")
			}
			if decoded.Cart.LineCount() != 0 {
				t.Fatalf("malformed cart line count = %d, want 0", decoded.Cart.LineCount())
			}
		})
	}
}

func TestCartCookieRejectsUnsupportedVersion(t *testing.T) {
	encoded := signedTestCookie(t, cookiePayload{
		Version: payloadVersion + 1,
		Lines:   []Line{{Slug: "mango", Quantity: 1}},
	})

	decoded := DecodeCookie(encoded, testSecret)
	if !decoded.NeedsClear {
		t.Fatal("DecodeCookie unsupported version NeedsClear = false, want true")
	}
	if decoded.Cart.LineCount() != 0 {
		t.Fatalf("unsupported version cart line count = %d, want 0", decoded.Cart.LineCount())
	}
}

func TestCartCookieRejectsInvalidSlugs(t *testing.T) {
	for _, slug := range []string{"", "products/mango"} {
		encoded := signedTestCookie(t, cookiePayload{
			Version: payloadVersion,
			Lines:   []Line{{Slug: slug, Quantity: 1}},
		})

		decoded := DecodeCookie(encoded, testSecret)
		if !decoded.NeedsClear {
			t.Fatalf("DecodeCookie slug %q NeedsClear = false, want true", slug)
		}
		if decoded.Cart.LineCount() != 0 {
			t.Fatalf("invalid slug cart line count = %d, want 0", decoded.Cart.LineCount())
		}
	}
}

func decodeTestPayload(t *testing.T, encoded string) cookiePayload {
	t.Helper()

	encodedPayload, _, ok := strings.Cut(encoded, ".")
	if !ok {
		t.Fatalf("encoded cookie = %q, want payload.signature", encoded)
	}
	jsonPayload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		t.Fatalf("DecodeString payload returned error: %v", err)
	}

	var payload cookiePayload
	if err := json.Unmarshal(jsonPayload, &payload); err != nil {
		t.Fatalf("Unmarshal payload returned error: %v", err)
	}
	return payload
}

func signedTestCookie(t *testing.T, payload cookiePayload) string {
	t.Helper()

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal payload returned error: %v", err)
	}
	return signedRawTestCookie(t, jsonPayload)
}

func signedRawTestCookie(t *testing.T, jsonPayload []byte) string {
	t.Helper()

	encodedPayload := base64.RawURLEncoding.EncodeToString(jsonPayload)
	encodedSignature := base64.RawURLEncoding.EncodeToString(signPayload(jsonPayload, testSecret))
	return encodedPayload + "." + encodedSignature
}

func cartLineCapFixture(count int) []Line {
	lines := make([]Line, count)
	for i := range lines {
		lines[i] = Line{Slug: fmt.Sprintf("fixture-%02d", i), Quantity: 1}
	}
	return lines
}
