package signedtoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

const testSecret = "signedtoken-test-secret"

var testPurpose = []byte("tgs-test-purpose")

type testPayload struct {
	Version   int    `json:"version"`
	Subject   string `json:"subject"`
	ExpiresAt int64  `json:"expires_at"`
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		payload testPayload
	}{
		{
			name:    "session-shaped payload",
			payload: testPayload{Version: 1, Subject: "01hxz9k2v8m4p6q0s3t5w7y9ab", ExpiresAt: 1769990400},
		},
		{
			name:    "zero values",
			payload: testPayload{},
		},
		{
			name:    "subject with separator characters",
			payload: testPayload{Version: 1, Subject: "dots.and.spaces and unicode ไทย", ExpiresAt: -1},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, err := Encode(test.payload, testSecret, testPurpose)
			if err != nil {
				t.Fatalf("Encode returned error: %v", err)
			}
			if len(value) > MaxSignedValueLength {
				t.Fatalf("encoded length = %d, want <= %d", len(value), MaxSignedValueLength)
			}

			var decoded testPayload
			if !Decode(value, testSecret, testPurpose, &decoded) {
				t.Fatal("Decode = false, want true")
			}
			if !reflect.DeepEqual(decoded, test.payload) {
				t.Fatalf("decoded payload = %#v, want %#v", decoded, test.payload)
			}
		})
	}
}

func TestEncodeMatchesAdminEnvelopeFormat(t *testing.T) {
	payload := testPayload{Version: 1, Subject: "shopper", ExpiresAt: 1769990400}
	value, err := Encode(payload, testSecret, testPurpose)
	if err != nil {
		t.Fatalf("Encode returned error: %v", err)
	}

	encodedPayload, encodedSignature, ok := strings.Cut(value, ".")
	if !ok {
		t.Fatalf("encoded value %q missing payload.signature separator", value)
	}
	jsonPayload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		t.Fatalf("payload segment is not RawURL base64: %v", err)
	}
	if want := `{"version":1,"subject":"shopper","expires_at":1769990400}`; string(jsonPayload) != want {
		t.Fatalf("payload JSON = %q, want %q", jsonPayload, want)
	}

	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write(testPurpose)
	mac.Write([]byte{0})
	mac.Write(jsonPayload)
	if want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil)); encodedSignature != want {
		t.Fatalf("signature segment = %q, want HMAC-SHA256(purpose || 0x00 || payload) = %q", encodedSignature, want)
	}
}

func TestEncodeReturnsErrorForUnmarshalablePayload(t *testing.T) {
	if _, err := Encode(make(chan int), testSecret, testPurpose); err == nil {
		t.Fatal("Encode of channel payload returned nil error, want JSON marshal error")
	}
}

func TestDecodeRejectsInvalidValues(t *testing.T) {
	valid := mustEncode(t, testPayload{Version: 1, Subject: "shopper", ExpiresAt: 1769990400})

	tests := []struct {
		name    string
		value   string
		secret  string
		purpose []byte
	}{
		{name: "empty value", value: "", secret: testSecret, purpose: testPurpose},
		{name: "missing separator", value: strings.ReplaceAll(valid, ".", "_"), secret: testSecret, purpose: testPurpose},
		{name: "extra separator after signature", value: valid + ".extra", secret: testSecret, purpose: testPurpose},
		{name: "payload not base64", value: "!!!." + segment(t, valid, 1), secret: testSecret, purpose: testPurpose},
		{name: "signature not base64", value: segment(t, valid, 0) + ".!!!", secret: testSecret, purpose: testPurpose},
		{name: "tampered payload", value: tamperSegment(t, valid, 0), secret: testSecret, purpose: testPurpose},
		{name: "tampered signature", value: tamperSegment(t, valid, 1), secret: testSecret, purpose: testPurpose},
		{name: "truncated signature", value: valid[:len(valid)-2], secret: testSecret, purpose: testPurpose},
		{name: "wrong secret", value: valid, secret: "another-secret", purpose: testPurpose},
		{name: "cross-purpose use", value: valid, secret: testSecret, purpose: []byte("tgs-other-purpose")},
		{name: "purpose prefix only", value: valid, secret: testSecret, purpose: testPurpose[:len(testPurpose)-1]},
		{name: "oversize value", value: oversizeValue(t), secret: testSecret, purpose: testPurpose},
		{name: "correctly signed non-JSON payload", value: rawSignedValue("not json", testSecret, testPurpose), secret: testSecret, purpose: testPurpose},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var decoded testPayload
			if Decode(test.value, test.secret, test.purpose, &decoded) {
				t.Fatal("Decode = true, want false")
			}
		})
	}
}

func TestDecodeAcceptsValueAtMaxLength(t *testing.T) {
	for size := 1; size <= MaxSignedValueLength; size++ {
		payload := testPayload{Version: 1, Subject: strings.Repeat("a", size), ExpiresAt: 1769990400}
		value, err := Encode(payload, testSecret, testPurpose)
		if err != nil {
			t.Fatalf("Encode returned error: %v", err)
		}
		if len(value) != MaxSignedValueLength {
			continue
		}

		var decoded testPayload
		if !Decode(value, testSecret, testPurpose, &decoded) {
			t.Fatal("Decode at exactly MaxSignedValueLength = false, want true")
		}
		if !reflect.DeepEqual(decoded, payload) {
			t.Fatal("decoded payload at MaxSignedValueLength does not match input")
		}
		return
	}
	t.Fatal("could not construct a value of exactly MaxSignedValueLength")
}

func TestRandomToken(t *testing.T) {
	tests := []struct {
		name    string
		n       int
		wantErr bool
	}{
		{name: "one byte", n: 1},
		{name: "sixteen bytes", n: 16},
		{name: "thirty-two bytes", n: 32},
		{name: "zero bytes", n: 0, wantErr: true},
		{name: "negative count", n: -4, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			token, err := RandomToken(test.n)
			if test.wantErr {
				if err == nil {
					t.Fatalf("RandomToken(%d) returned nil error, want error", test.n)
				}
				return
			}
			if err != nil {
				t.Fatalf("RandomToken(%d) returned error: %v", test.n, err)
			}
			decoded, err := base64.RawURLEncoding.DecodeString(token)
			if err != nil {
				t.Fatalf("token %q is not RawURL base64: %v", token, err)
			}
			if len(decoded) != test.n {
				t.Fatalf("decoded token length = %d, want %d", len(decoded), test.n)
			}
		})
	}
}

func TestRandomTokenUniqueness(t *testing.T) {
	const draws = 1000
	seen := make(map[string]struct{}, draws)
	for range draws {
		token, err := RandomToken(16)
		if err != nil {
			t.Fatalf("RandomToken returned error: %v", err)
		}
		if _, duplicate := seen[token]; duplicate {
			t.Fatalf("RandomToken produced duplicate %q", token)
		}
		seen[token] = struct{}{}
	}
}

func TestNewIDFormat(t *testing.T) {
	pattern := regexp.MustCompile(`^[a-z0-9]{26}$`)
	for range 1000 {
		id := NewID()
		if !pattern.MatchString(id) {
			t.Fatalf("NewID() = %q, want match for ^[a-z0-9]{26}$", id)
		}
		if strings.ContainsAny(id, "ilou") {
			t.Fatalf("NewID() = %q contains a character outside the Crockford alphabet (i, l, o, u)", id)
		}
	}
}

func TestNewIDUniqueness(t *testing.T) {
	const draws = 10000
	seen := make(map[string]struct{}, draws)
	for range draws {
		id := NewID()
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("NewID produced duplicate %q", id)
		}
		seen[id] = struct{}{}
	}
}

func mustEncode(t *testing.T, payload testPayload) string {
	t.Helper()
	value, err := Encode(payload, testSecret, testPurpose)
	if err != nil {
		t.Fatalf("Encode returned error: %v", err)
	}
	return value
}

// segment returns the payload (index 0) or signature (index 1) half of an
// encoded value.
func segment(t *testing.T, value string, index int) string {
	t.Helper()
	payload, signature, ok := strings.Cut(value, ".")
	if !ok {
		t.Fatalf("encoded value %q missing separator", value)
	}
	if index == 0 {
		return payload
	}
	return signature
}

// tamperSegment flips one byte inside the chosen segment and reassembles the
// value, leaving everything else (including the other segment) intact.
func tamperSegment(t *testing.T, value string, index int) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(segment(t, value, index))
	if err != nil {
		t.Fatalf("segment %d is not RawURL base64: %v", index, err)
	}
	raw[len(raw)-1] ^= 0x01
	tampered := base64.RawURLEncoding.EncodeToString(raw)
	if index == 0 {
		return tampered + "." + segment(t, value, 1)
	}
	return segment(t, value, 0) + "." + tampered
}

func oversizeValue(t *testing.T) string {
	t.Helper()
	value := mustEncode(t, testPayload{Version: 1, Subject: strings.Repeat("a", 4*MaxSignedValueLength), ExpiresAt: 1769990400})
	if len(value) <= MaxSignedValueLength {
		t.Fatalf("oversize fixture length = %d, want > %d", len(value), MaxSignedValueLength)
	}
	return value
}

// rawSignedValue builds a correctly signed envelope around an arbitrary
// (non-JSON) payload so tests can prove Decode also validates the payload
// structure, not just the signature.
func rawSignedValue(payload, secret string, purpose []byte) string {
	encodedPayload := base64.RawURLEncoding.EncodeToString([]byte(payload))
	encodedSignature := base64.RawURLEncoding.EncodeToString(signPayload([]byte(payload), secret, purpose))
	return encodedPayload + "." + encodedSignature
}
