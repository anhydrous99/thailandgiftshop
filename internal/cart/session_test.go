package cart

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testSecret = "cart-test-secret"

// The cart cookie name must carry the __Host- prefix so browsers enforce
// Secure + Path=/ + no Domain, matching the customer and admin cookies.
func TestCookieNameHasHostPrefix(t *testing.T) {
	if !strings.HasPrefix(CookieName, "__Host-") {
		t.Fatalf("CookieName = %q, want __Host- prefix", CookieName)
	}
}

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
	if decodedPayload.IssuedAt <= 0 {
		t.Fatalf("payload issued at = %d, want positive unix seconds", decodedPayload.IssuedAt)
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
		Version:  payloadVersion,
		IssuedAt: time.Now().Unix(),
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
	encoded := signedRawTestCookie(t, fmt.Appendf(nil, `{"version":1,"iat":%d,"lines":[{"slug":"mango","quantity":2}]}`, time.Now().Unix()))

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
		Version:  payloadVersion,
		IssuedAt: time.Now().Unix(),
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

func TestMirrorCookieRoundTripSetsMirrorFlag(t *testing.T) {
	mirrorCart := mustCartFromLines(t, []Line{{Slug: "mango", Quantity: 2}})

	encoded, err := EncodeMirrorCookie(mirrorCart, testSecret)
	if err != nil {
		t.Fatalf("EncodeMirrorCookie returned error: %v", err)
	}
	decoded := DecodeCookie(encoded, testSecret)
	if decoded.NeedsClear {
		t.Fatal("DecodeCookie mirror NeedsClear = true, want false")
	}
	if !decoded.Mirror {
		t.Fatal("DecodeCookie mirror Mirror = false, want true")
	}
	if got := decoded.Cart.Lines(); !reflect.DeepEqual(got, []Line{{Slug: "mango", Quantity: 2}}) {
		t.Fatalf("mirror lines = %#v, want mango 2", got)
	}

	anonymous, err := EncodeCookie(mirrorCart, testSecret)
	if err != nil {
		t.Fatalf("EncodeCookie returned error: %v", err)
	}
	if decoded := DecodeCookie(anonymous, testSecret); decoded.Mirror {
		t.Fatal("anonymous cookie decoded with Mirror = true, want false")
	}
}

func TestLegacyCookieWithoutMirrorClaimDecodesAsAnonymous(t *testing.T) {
	encoded := signedRawTestCookie(t, fmt.Appendf(nil, `{"version":1,"iat":%d,"lines":[{"slug":"mango","quantity":2}]}`, time.Now().Unix()))

	decoded := DecodeCookie(encoded, testSecret)
	if decoded.NeedsClear {
		t.Fatal("DecodeCookie legacy NeedsClear = true, want false")
	}
	if decoded.Mirror {
		t.Fatal("legacy cookie decoded with Mirror = true, want false")
	}
}

func TestEncodeMirrorCookieTruncatedDropsTrailingLinesUntilItFits(t *testing.T) {
	oversized := mustCartFromLines(t, oversizedCartLines())

	if _, err := EncodeCookie(oversized, testSecret); !errors.Is(err, ErrCookieTooLarge) {
		t.Fatalf("EncodeCookie oversized error = %v, want ErrCookieTooLarge (fixture too small)", err)
	}

	encoded, err := EncodeMirrorCookieTruncated(oversized, testSecret)
	if err != nil {
		t.Fatalf("EncodeMirrorCookieTruncated returned error: %v", err)
	}
	if len(encoded) > MaxEncodedCookieLength {
		t.Fatalf("truncated cookie length = %d, want <= %d", len(encoded), MaxEncodedCookieLength)
	}

	decoded := DecodeCookie(encoded, testSecret)
	if decoded.NeedsClear {
		t.Fatal("DecodeCookie truncated mirror NeedsClear = true, want false")
	}
	if !decoded.Mirror {
		t.Fatal("truncated mirror Mirror = false, want true")
	}
	got := decoded.Cart.Lines()
	want := oversized.Lines()
	if len(got) == 0 || len(got) >= len(want) {
		t.Fatalf("truncated line count = %d, want a non-empty strict prefix of %d", len(got), len(want))
	}
	if !reflect.DeepEqual(got, want[:len(got)]) {
		t.Fatalf("truncated lines = %#v, want leading prefix of original lines", got)
	}
}

func TestEncodeMirrorCookieTruncatedKeepsSmallCartsIntact(t *testing.T) {
	small := mustCartFromLines(t, []Line{
		{Slug: "mango", Quantity: 2},
		{Slug: "tea", Quantity: 1},
	})

	encoded, err := EncodeMirrorCookieTruncated(small, testSecret)
	if err != nil {
		t.Fatalf("EncodeMirrorCookieTruncated returned error: %v", err)
	}
	decoded := DecodeCookie(encoded, testSecret)
	if !decoded.Mirror {
		t.Fatal("small mirror Mirror = false, want true")
	}
	if got := decoded.Cart.Lines(); !reflect.DeepEqual(got, small.Lines()) {
		t.Fatalf("small mirror lines = %#v, want %#v", got, small.Lines())
	}
}

// oversizedCartLines builds a cart that individually-valid lines push past
// MaxEncodedCookieLength (realistic slug lengths, well under MaxLineItems).
func oversizedCartLines() []Line {
	lines := make([]Line, 48)
	for i := range lines {
		lines[i] = Line{Slug: fmt.Sprintf("aromatic-jasmine-handmade-%02d", i), Quantity: 1}
	}
	return lines
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
		signedTestCookie(t, cookiePayload{Version: payloadVersion, IssuedAt: time.Now().Unix(), Lines: []Line{{Slug: "mango", Quantity: 0}}}),
		signedTestCookie(t, cookiePayload{Version: payloadVersion, IssuedAt: time.Now().Unix(), Lines: []Line{{Slug: "mango", Quantity: -1}}}),
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

func TestCartCookieRejectsMissingIssuedAt(t *testing.T) {
	// Cookies signed before issued-at validation existed carry no iat claim;
	// they clear once and the next cart mutation mints a fresh cookie.
	encoded := signedTestCookie(t, cookiePayload{
		Version: payloadVersion,
		Lines:   []Line{{Slug: "mango", Quantity: 1}},
	})

	decoded := DecodeCookie(encoded, testSecret)
	if !decoded.NeedsClear {
		t.Fatal("DecodeCookie missing issued-at NeedsClear = false, want true")
	}
	if decoded.Cart.LineCount() != 0 {
		t.Fatalf("missing issued-at cart line count = %d, want 0", decoded.Cart.LineCount())
	}
}

func TestCartCookieIssuedAtBounds(t *testing.T) {
	issued := time.Now()
	encoded, err := encodeCookieAt(mustCartFromLines(t, []Line{{Slug: "mango", Quantity: 1}}), testSecret, issued, false)
	if err != nil {
		t.Fatalf("encodeCookieAt returned error: %v", err)
	}

	tests := []struct {
		name      string
		now       time.Time
		wantClear bool
	}{
		{name: "fresh", now: issued, wantClear: false},
		{name: "near expiry", now: issued.Add(CookieMaxAge*time.Second - time.Second), wantClear: false},
		{name: "expired", now: issued.Add(CookieMaxAge*time.Second + time.Second), wantClear: true},
		{name: "future within skew", now: issued.Add(-cookieIssuedAtSkew + time.Minute), wantClear: false},
		{name: "future beyond skew", now: issued.Add(-cookieIssuedAtSkew - time.Minute), wantClear: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decoded := decodeCookieAt(encoded, testSecret, test.now)
			if decoded.NeedsClear != test.wantClear {
				t.Fatalf("NeedsClear = %t, want %t", decoded.NeedsClear, test.wantClear)
			}
			wantLines := 1
			if test.wantClear {
				wantLines = 0
			}
			if got := decoded.Cart.LineCount(); got != wantLines {
				t.Fatalf("line count = %d, want %d", got, wantLines)
			}
		})
	}
}

func TestCartMerge(t *testing.T) {
	tests := []struct {
		name   string
		server Cart
		cookie Cart
		want   []Line
	}{
		{
			name:   "both empty",
			server: Empty(),
			cookie: Empty(),
			want:   []Line{},
		},
		{
			name:   "empty server side keeps cookie lines in cookie order",
			server: Empty(),
			cookie: Cart{lines: []Line{
				{Slug: "tea", Quantity: 3},
				{Slug: "mango", Quantity: 2},
			}},
			want: []Line{
				{Slug: "tea", Quantity: 3},
				{Slug: "mango", Quantity: 2},
			},
		},
		{
			name: "empty cookie side keeps server lines in server order",
			server: Cart{lines: []Line{
				{Slug: "mango", Quantity: 2},
				{Slug: "tea", Quantity: 3},
			}},
			cookie: Empty(),
			want: []Line{
				{Slug: "mango", Quantity: 2},
				{Slug: "tea", Quantity: 3},
			},
		},
		{
			name: "max wins per line key in either direction",
			server: Cart{lines: []Line{
				{Slug: "mango", Quantity: 5},
				{Slug: "tea", Quantity: 1},
			}},
			cookie: Cart{lines: []Line{
				{Slug: "mango", Quantity: 2},
				{Slug: "tea", Quantity: 7},
			}},
			want: []Line{
				{Slug: "mango", Quantity: 5},
				{Slug: "tea", Quantity: 7},
			},
		},
		{
			name: "union keeps server order first then cookie-only lines in cookie order",
			server: Cart{lines: []Line{
				{Slug: "mango", Quantity: 2},
				{Slug: "tea", Quantity: 3},
			}},
			cookie: Cart{lines: []Line{
				{Slug: "silk-scarf", Quantity: 1},
				{Slug: "tea", Quantity: 9},
				{Slug: "coconut-soap", Quantity: 4},
			}},
			want: []Line{
				{Slug: "mango", Quantity: 2},
				{Slug: "tea", Quantity: 9},
				{Slug: "silk-scarf", Quantity: 1},
				{Slug: "coconut-soap", Quantity: 4},
			},
		},
		{
			name: "line identity includes variant id",
			server: Cart{lines: []Line{
				{Slug: "linen-shirt", VariantID: "var-small", Quantity: 2},
			}},
			cookie: Cart{lines: []Line{
				{Slug: "linen-shirt", VariantID: "var-small", Quantity: 6},
				{Slug: "linen-shirt", VariantID: "var-large", Quantity: 3},
			}},
			want: []Line{
				{Slug: "linen-shirt", VariantID: "var-small", Quantity: 6},
				{Slug: "linen-shirt", VariantID: "var-large", Quantity: 3},
			},
		},
		{
			name: "quantities cap at MaxQuantity",
			server: Cart{lines: []Line{
				{Slug: "mango", Quantity: 150},
				{Slug: "tea", Quantity: 1},
			}},
			cookie: Cart{lines: []Line{
				{Slug: "tea", Quantity: 120},
				{Slug: "silk-scarf", Quantity: 130},
			}},
			want: []Line{
				{Slug: "mango", Quantity: MaxQuantity},
				{Slug: "tea", Quantity: MaxQuantity},
				{Slug: "silk-scarf", Quantity: MaxQuantity},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Merge(test.server, test.cookie).Lines(); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Merge lines = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestCartMergeIdempotentRemerge(t *testing.T) {
	server := mustCartFromLines(t, []Line{
		{Slug: "mango", Quantity: 5},
		{Slug: "tea", Quantity: 1},
	})
	cookie := mustCartFromLines(t, []Line{
		{Slug: "tea", Quantity: 7},
		{Slug: "silk-scarf", Quantity: 2},
	})

	once := Merge(server, cookie)
	twice := Merge(once, cookie)
	if got, want := twice.Lines(), once.Lines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("re-merged lines = %#v, want %#v", got, want)
	}
}

func TestCartMergeTruncatesAtMaxLineItems(t *testing.T) {
	serverLines := make([]Line, MaxLineItems-2)
	for i := range serverLines {
		serverLines[i] = Line{Slug: fmt.Sprintf("server-%02d", i), Quantity: 1}
	}
	cookieLines := []Line{
		{Slug: "server-10", Quantity: 5},
		{Slug: "cookie-00", Quantity: 2},
		{Slug: "cookie-01", Quantity: 3},
		{Slug: "cookie-02", Quantity: 4},
		{Slug: "cookie-03", Quantity: 6},
	}

	merged := Merge(mustCartFromLines(t, serverLines), mustCartFromLines(t, cookieLines))
	if got := merged.LineCount(); got != MaxLineItems {
		t.Fatalf("merged line count = %d, want %d", got, MaxLineItems)
	}

	want := make([]Line, 0, MaxLineItems)
	want = append(want, serverLines...)
	want[10].Quantity = 5
	want = append(want, Line{Slug: "cookie-00", Quantity: 2}, Line{Slug: "cookie-01", Quantity: 3})
	if got := merged.Lines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("truncated merged lines = %#v, want %#v", got, want)
	}
}

func TestCartMergeAppliesMaxToExistingKeysWhenServerCartIsFull(t *testing.T) {
	serverLines := cartLineCapFixture(MaxLineItems)
	cookieLines := []Line{
		{Slug: "fixture-25", Quantity: 8},
		{Slug: "cookie-only", Quantity: 4},
	}

	merged := Merge(mustCartFromLines(t, serverLines), mustCartFromLines(t, cookieLines))
	if got := merged.LineCount(); got != MaxLineItems {
		t.Fatalf("merged line count = %d, want %d", got, MaxLineItems)
	}

	want := cartLineCapFixture(MaxLineItems)
	want[25].Quantity = 8
	if got := merged.Lines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("full server merged lines = %#v, want %#v", got, want)
	}
}

func mustCartFromLines(t *testing.T, lines []Line) Cart {
	t.Helper()

	cart, err := New(lines)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	return cart
}

func TestCartCookieRejectsInvalidSlugs(t *testing.T) {
	for _, slug := range []string{"", "products/mango"} {
		encoded := signedTestCookie(t, cookiePayload{
			Version:  payloadVersion,
			IssuedAt: time.Now().Unix(),
			Lines:    []Line{{Slug: slug, Quantity: 1}},
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
