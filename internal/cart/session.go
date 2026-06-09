package cart

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	EnvCookieSecret        = "CART_COOKIE_SECRET"
	CookieName             = "tgs_cart"
	CookieMaxAge           = 604800
	MaxLineItems           = 50
	MaxEncodedCookieLength = 3072
	MaxQuantity            = 99
)

const payloadVersion = 1

// cookieIssuedAtSkew tolerates clock drift between the host that signed a
// cookie and the host validating it.
const cookieIssuedAtSkew = 5 * time.Minute

var (
	ErrInvalidSlug     = errors.New("invalid cart slug")
	ErrInvalidQuantity = errors.New("invalid cart quantity")
	ErrLineItemLimit   = errors.New("cart line item limit reached")
	ErrCookieTooLarge  = errors.New("cart cookie payload too large")
)

type Line struct {
	Slug      string `json:"slug"`
	VariantID string `json:"variant_id,omitempty"`
	Quantity  int    `json:"quantity"`
}

type Cart struct {
	lines []Line
}

type DecodeResult struct {
	Cart       Cart
	NeedsClear bool
}

type cookiePayload struct {
	Version  int    `json:"version"`
	IssuedAt int64  `json:"iat"`
	Lines    []Line `json:"lines"`
}

func New(lines []Line) (Cart, error) {
	return normalizeLines(lines, true, false)
}

func Empty() Cart {
	return Cart{}
}

func (c Cart) Lines() []Line {
	return cloneLines(c.lines)
}

func (c Cart) Add(slug string, quantity int) (Cart, error) {
	return c.AddLine(slug, "", quantity)
}

func (c Cart) AddLine(slug string, variantID string, quantity int) (Cart, error) {
	if !validSlug(slug) {
		return c, ErrInvalidSlug
	}
	if quantity <= 0 {
		return c, nil
	}

	lines := cloneLines(c.lines)
	for i, line := range lines {
		if line.Slug != slug || line.VariantID != variantID {
			continue
		}
		lines[i].Quantity = capQuantity(line.Quantity + quantity)
		return Cart{lines: lines}, nil
	}
	if len(lines) >= MaxLineItems {
		return c, ErrLineItemLimit
	}

	lines = append(lines, Line{Slug: slug, VariantID: variantID, Quantity: capQuantity(quantity)})
	return Cart{lines: lines}, nil
}

func (c Cart) SetQuantity(slug string, quantity int) (Cart, error) {
	return c.SetLineQuantity(slug, "", quantity)
}

func (c Cart) SetLineQuantity(slug string, variantID string, quantity int) (Cart, error) {
	if !validSlug(slug) {
		return c, ErrInvalidSlug
	}

	lines := cloneLines(c.lines)
	for i, line := range lines {
		if line.Slug != slug || line.VariantID != variantID {
			continue
		}
		if quantity <= 0 {
			return Cart{lines: append(lines[:i], lines[i+1:]...)}, nil
		}
		lines[i].Quantity = capQuantity(quantity)
		return Cart{lines: lines}, nil
	}
	if quantity <= 0 {
		return c, nil
	}
	if len(lines) >= MaxLineItems {
		return c, ErrLineItemLimit
	}

	lines = append(lines, Line{Slug: slug, VariantID: variantID, Quantity: capQuantity(quantity)})
	return Cart{lines: lines}, nil
}

func (c Cart) Remove(slug string) (Cart, error) {
	return c.RemoveLine(slug, "")
}

func (c Cart) RemoveLine(slug string, variantID string) (Cart, error) {
	if !validSlug(slug) {
		return c, ErrInvalidSlug
	}

	lines := cloneLines(c.lines)
	for i, line := range lines {
		if line.Slug == slug && line.VariantID == variantID {
			return Cart{lines: append(lines[:i], lines[i+1:]...)}, nil
		}
	}
	return c, nil
}

func (c Cart) Clear() Cart {
	return Cart{}
}

func (c Cart) TotalItemCount() int {
	total := 0
	for _, line := range c.lines {
		total += line.Quantity
	}
	return total
}

func (c Cart) LineCount() int {
	return len(c.lines)
}

func EncodeCookie(c Cart, secret string) (string, error) {
	return encodeCookieAt(c, secret, time.Now())
}

func encodeCookieAt(c Cart, secret string, now time.Time) (string, error) {
	normalized, err := normalizeLines(c.lines, true, false)
	if err != nil {
		return "", err
	}

	jsonPayload, err := json.Marshal(cookiePayload{
		Version:  payloadVersion,
		IssuedAt: now.UTC().Unix(),
		Lines:    normalized.lines,
	})
	if err != nil {
		return "", err
	}

	encodedPayload := base64.RawURLEncoding.EncodeToString(jsonPayload)
	encodedSignature := base64.RawURLEncoding.EncodeToString(signPayload(jsonPayload, secret))
	encoded := encodedPayload + "." + encodedSignature
	if len(encoded) > MaxEncodedCookieLength {
		return "", ErrCookieTooLarge
	}

	return encoded, nil
}

func DecodeCookie(value string, secret string) DecodeResult {
	return decodeCookieAt(value, secret, time.Now())
}

func decodeCookieAt(value string, secret string, now time.Time) DecodeResult {
	if len(value) == 0 || len(value) > MaxEncodedCookieLength {
		return invalidDecodeResult()
	}

	encodedPayload, encodedSignature, ok := strings.Cut(value, ".")
	if !ok || strings.Contains(encodedSignature, ".") {
		return invalidDecodeResult()
	}

	jsonPayload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return invalidDecodeResult()
	}
	signature, err := base64.RawURLEncoding.DecodeString(encodedSignature)
	if err != nil {
		return invalidDecodeResult()
	}
	if !hmac.Equal(signature, signPayload(jsonPayload, secret)) {
		return invalidDecodeResult()
	}

	var payload cookiePayload
	if err := json.Unmarshal(jsonPayload, &payload); err != nil {
		return invalidDecodeResult()
	}
	if payload.Version != payloadVersion {
		return invalidDecodeResult()
	}
	if !validIssuedAt(payload.IssuedAt, now) {
		return invalidDecodeResult()
	}

	normalized, err := normalizeLines(payload.Lines, true, true)
	if err != nil {
		return invalidDecodeResult()
	}

	return DecodeResult{Cart: normalized}
}

func invalidDecodeResult() DecodeResult {
	return DecodeResult{Cart: Cart{}, NeedsClear: true}
}

// validIssuedAt bounds how long a signed cookie stays valid server-side; the
// browser Max-Age alone cannot expire a captured cookie value.
func validIssuedAt(issuedAt int64, now time.Time) bool {
	if issuedAt <= 0 {
		return false
	}
	issued := time.Unix(issuedAt, 0)
	if issued.After(now.Add(cookieIssuedAtSkew)) {
		return false
	}
	return now.Sub(issued) <= CookieMaxAge*time.Second
}

func normalizeLines(lines []Line, capLineItems bool, rejectNonPositive bool) (Cart, error) {
	normalized := make([]Line, 0, min(len(lines), MaxLineItems))
	indexes := make(map[string]int, min(len(lines), MaxLineItems))

	for _, line := range lines {
		if !validSlug(line.Slug) {
			return Cart{}, ErrInvalidSlug
		}
		if line.Quantity <= 0 {
			if rejectNonPositive {
				return Cart{}, ErrInvalidQuantity
			}
			continue
		}

		key := lineKey(line)
		if index, ok := indexes[key]; ok {
			normalized[index].Quantity = capQuantity(normalized[index].Quantity + line.Quantity)
			continue
		}
		if len(normalized) >= MaxLineItems {
			if capLineItems {
				continue
			}
			return Cart{}, ErrLineItemLimit
		}

		indexes[key] = len(normalized)
		normalized = append(normalized, Line{Slug: line.Slug, VariantID: line.VariantID, Quantity: capQuantity(line.Quantity)})
	}

	return Cart{lines: normalized}, nil
}

func lineKey(line Line) string {
	return line.Slug + "\x00" + line.VariantID
}

func validSlug(slug string) bool {
	return strings.TrimSpace(slug) != "" && !strings.Contains(slug, "/")
}

func capQuantity(quantity int) int {
	if quantity > MaxQuantity {
		return MaxQuantity
	}
	return quantity
}

func cloneLines(lines []Line) []Line {
	cloned := make([]Line, len(lines))
	copy(cloned, lines)
	return cloned
}

func signPayload(payload []byte, secret string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return mac.Sum(nil)
}
